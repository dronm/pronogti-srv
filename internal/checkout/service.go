package checkout

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/dronm/webapp"
)

var orderIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type Service struct {
	provider   Provider
	repository Repository
	options    Options
	now        func() time.Time
}

func NewService(provider Provider, repository Repository, options Options) *Service {
	if options.MaxLifeTime <= 0 {
		options.MaxLifeTime = 900
	}
	return &Service{provider: provider, repository: repository, options: options, now: time.Now}
}

func (s *Service) Prepare(ctx context.Context, actor Actor, key string, request PrepareRequest) (Order, error) {
	if err := s.ready(ctx, actor); err != nil {
		return Order{}, err
	}
	if !validIdempotencyKey(key) {
		return Order{}, webapp.BadRequest("Idempotency-Key must contain 1 to 128 visible ASCII characters", nil)
	}
	normalized, err := normalizeRequest(request)
	if err != nil {
		return Order{}, err
	}
	if actor.Guest && (normalized.VisitID != 0 || normalized.RecordID != 0) {
		return Order{}, webapp.Forbidden("a guest cannot check out an existing customer's visit", nil)
	}
	owner := ownerFor(actor)
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return Order{}, err
	}
	digest := sha256.Sum256(encoded)
	requestHash := hex.EncodeToString(digest[:])
	previous, err := s.repository.FindByKey(ctx, owner, key)
	if err == nil {
		return s.replay(ctx, actor, owner, previous, requestHash)
	}
	if !errors.Is(err, ErrNotFound) {
		return Order{}, fmt.Errorf("find checkout: %w", err)
	}

	visitLines := map[string]VisitLine{}
	if normalized.VisitID > 0 {
		visit, err := s.provider.LoadVisit(ctx, actor.CompanyID, actor.ClientID)
		if err != nil {
			return Order{}, err
		}
		if visit.VisitID != normalized.VisitID || visit.RecordID != normalized.RecordID || len(visit.Items) == 0 {
			return Order{}, webapp.Conflict("the unpaid visit has changed or is already paid; refresh it before checkout", nil)
		}
		for _, line := range visit.Items {
			lineKey := itemKey(Selection{Kind: line.Kind, ID: line.ID, LineID: line.LineID, StaffID: line.StaffID})
			if _, duplicate := visitLines[lineKey]; duplicate {
				return Order{}, webapp.Conflict("the unpaid visit contains ambiguous line identifiers", nil)
			}
			visitLines[lineKey] = line
		}
	}

	var catalogSelections []Selection
	for _, selected := range normalized.Items {
		if selected.Source == SourceCatalog {
			catalogSelections = append(catalogSelections, selected)
		}
	}
	catalogLines := map[string]CatalogItem{}
	if len(catalogSelections) > 0 {
		catalog, err := s.provider.LoadCatalog(ctx, actor.CompanyID, catalogSelections)
		if err != nil {
			return Order{}, err
		}
		for _, line := range catalog {
			lineKey := itemKey(line.Selection)
			if _, duplicate := catalogLines[lineKey]; duplicate {
				return Order{}, webapp.Conflict("the catalog contains ambiguous item prices", nil)
			}
			catalogLines[lineKey] = line
		}
	}

	items := make([]Item, 0, len(normalized.Items))
	var subtotal int64
	for _, selected := range normalized.Items {
		item := Item{Selection: selected}
		if selected.Source == SourceVisit {
			line, exists := visitLines[itemKey(selected)]
			if !exists {
				return Order{}, webapp.Conflict("a selected unpaid visit line has changed; refresh it before checkout", nil)
			}
			quantity, _, err := normalizeQuantity(line.Quantity)
			if err != nil || quantity != selected.Quantity {
				return Order{}, webapp.Conflict("an unpaid visit line's quantity cannot be changed during checkout", nil)
			}
			if line.AmountKopecks <= 0 || line.AmountKopecks > MaxMoney || line.UnitPriceKopecks < 0 || line.UnitPriceKopecks > MaxMoney {
				return Order{}, webapp.Conflict("the unpaid visit contains an unsupported amount", nil)
			}
			item.Title, item.StaffName = line.Title, line.StaffName
			item.UnitPriceKopecks, item.AmountKopecks = line.UnitPriceKopecks, line.AmountKopecks
		} else {
			line, exists := catalogLines[itemKey(selected)]
			if !exists {
				return Order{}, webapp.Conflict("a selected item or staff member is unavailable for this salon", nil)
			}
			_, milli, _ := normalizeQuantity(selected.Quantity)
			amount, err := lineAmount(line.UnitPriceKopecks, milli)
			if err != nil {
				return Order{}, webapp.BadRequest("the selected item amount is outside the supported range", nil)
			}
			item.Title, item.StaffName = line.Title, line.StaffName
			item.UnitPriceKopecks, item.AmountKopecks = line.UnitPriceKopecks, amount
		}
		if item.Title == "" {
			return Order{}, webapp.Conflict("a selected item has no current title", nil)
		}
		subtotal, err = addMoney(subtotal, item.AmountKopecks)
		if err != nil {
			return Order{}, webapp.BadRequest(err.Error(), nil)
		}
		items = append(items, item)
	}
	if err := s.validateActor(ctx, actor); err != nil {
		return Order{}, err
	}
	id, err := newOrderID()
	if err != nil {
		return Order{}, err
	}
	now := s.now()
	order := Order{
		ID: id, Owner: owner, VisitID: normalized.VisitID, RecordID: normalized.RecordID,
		Items: items, SubtotalKopecks: subtotal, PayableKopecks: subtotal,
		Currency: "RUB", State: StatePrepared, IdempotencyKey: key, RequestHash: requestHash,
		ExpiresAt: now.Add(time.Duration(s.options.MaxLifeTime) * time.Second),
		CreatedAt: now, UpdatedAt: now, YClientsSyncStatus: "not_started", CustomerContact: actor.Contact,
	}
	stored, err := s.repository.Create(ctx, order)
	if err != nil {
		return Order{}, fmt.Errorf("persist checkout: %w", err)
	}
	// Concurrent requests may have inserted the same key while prices were read.
	return s.replay(ctx, actor, owner, stored, requestHash)
}

func (s *Service) Get(ctx context.Context, actor Actor, id string) (Order, error) {
	if err := s.ready(ctx, actor); err != nil {
		return Order{}, err
	}
	if !orderIDPattern.MatchString(id) {
		return Order{}, webapp.BadRequest("checkout id must be a valid UUID", nil)
	}
	order, err := s.repository.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Order{}, webapp.NotFound("checkout not found", nil)
	}
	if err != nil {
		return Order{}, fmt.Errorf("read checkout: %w", err)
	}
	if !sameOwner(ownerFor(actor), order.Owner) {
		return Order{}, webapp.NotFound("checkout not found", nil)
	}
	if err := s.usable(order); err != nil {
		return Order{}, err
	}
	if err := s.validateActor(ctx, actor); err != nil {
		return Order{}, err
	}
	if s.options.ValidateOrder != nil {
		if err := s.options.ValidateOrder(ctx, order); err != nil {
			return Order{}, err
		}
	}
	if err := s.validateActor(ctx, actor); err != nil {
		return Order{}, err
	}
	return order, nil
}

func (s *Service) ready(ctx context.Context, actor Actor) error {
	if ctx == nil {
		return fmt.Errorf("checkout context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.provider == nil || s.repository == nil {
		return fmt.Errorf("checkout service is not configured")
	}
	if actor.CompanyID <= 0 || actor.ScopeID == "" || actor.CustomerSessionID == "" || actor.Mode != "public" && actor.Mode != "kiosk" || actor.Guest && actor.ClientID != 0 || !actor.Guest && actor.ClientID <= 0 {
		return webapp.Unauthorized("a current customer session is required for checkout", nil)
	}
	if actor.Mode == "kiosk" && (actor.KioskID == nil || *actor.KioskID <= 0) || actor.Mode == "public" && actor.KioskID != nil {
		return webapp.Unauthorized("the checkout entry mode does not match the current session", nil)
	}
	return nil
}

func (s *Service) validateActor(ctx context.Context, actor Actor) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.options.ValidateActor != nil {
		return s.options.ValidateActor(ctx, actor)
	}
	return nil
}

func (s *Service) replay(ctx context.Context, actor Actor, owner Owner, order Order, requestHash string) (Order, error) {
	if !sameOwner(owner, order.Owner) {
		return Order{}, webapp.NotFound("checkout not found", nil)
	}
	if order.RequestHash != requestHash {
		return Order{}, webapp.Conflict("Idempotency-Key was already used with different checkout selections", nil)
	}
	if err := s.usable(order); err != nil {
		return Order{}, err
	}
	if err := s.validateActor(ctx, actor); err != nil {
		return Order{}, err
	}
	if s.options.ValidateOrder != nil {
		if err := s.options.ValidateOrder(ctx, order); err != nil {
			return Order{}, err
		}
	}
	if err := s.validateActor(ctx, actor); err != nil {
		return Order{}, err
	}
	return order, nil
}

func (s *Service) usable(order Order) error {
	const message = "checkout has expired or is no longer payable; prepare it again with a new Idempotency-Key"
	if order.State != StatePrepared {
		return webapp.Conflict(message, map[string]any{"code": "checkout_not_prepared"})
	}
	if !s.now().Before(order.ExpiresAt) {
		return webapp.Conflict(message, map[string]any{"code": "checkout_expired"})
	}
	return nil
}

func normalizeRequest(request PrepareRequest) (PrepareRequest, error) {
	if request.BonusToApplyKopecks != 0 {
		return PrepareRequest{}, webapp.Conflict("bonus redemption is unavailable until server-side write-off is implemented", nil)
	}
	if request.VisitID < 0 || request.RecordID < 0 || (request.VisitID == 0) != (request.RecordID == 0) {
		return PrepareRequest{}, webapp.BadRequest("visit_id and record_id must both be positive or both be zero", nil)
	}
	if len(request.Items) == 0 || len(request.Items) > 200 {
		return PrepareRequest{}, webapp.BadRequest("checkout requires between 1 and 200 selected items", nil)
	}
	request.Items = append([]Selection(nil), request.Items...)
	seen := map[string]bool{}
	for index := range request.Items {
		item := &request.Items[index]
		if item.Source != SourceVisit && item.Source != SourceCatalog || item.Kind != KindGood && item.Kind != KindService || item.ID <= 0 || item.StaffID < 0 || item.LineID < 0 || item.Source == SourceCatalog && item.StaffID == 0 {
			return PrepareRequest{}, webapp.BadRequest("each checkout item requires a valid source, kind, item id and staff id", nil)
		}
		if item.Source == SourceVisit && request.VisitID == 0 || item.Source == SourceCatalog && item.LineID != 0 {
			return PrepareRequest{}, webapp.BadRequest("visit lines require a visit, and catalog lines cannot supply a transaction line id", nil)
		}
		quantity, milli, err := normalizeQuantity(item.Quantity)
		if err != nil || item.Kind == KindService && milli%quantityScale != 0 {
			return PrepareRequest{}, webapp.BadRequest("quantity must be a positive decimal with at most three places; services require whole units", nil)
		}
		item.Quantity = quantity
		key := item.Source + ":" + itemKey(*item)
		if seen[key] {
			return PrepareRequest{}, webapp.BadRequest("duplicate checkout selections must be combined into one quantity", nil)
		}
		seen[key] = true
	}
	sort.Slice(request.Items, func(i, j int) bool {
		left, right := request.Items[i], request.Items[j]
		return left.Source+":"+itemKey(left) < right.Source+":"+itemKey(right)
	})
	return request, nil
}

func validIdempotencyKey(key string) bool {
	if len(key) == 0 || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func ownerFor(actor Actor) Owner {
	scope := sha256.Sum256([]byte(actor.ScopeID))
	customer := sha256.Sum256([]byte(actor.CustomerSessionID))
	return Owner{
		CompanyID: actor.CompanyID, KioskID: actor.KioskID, Mode: actor.Mode, ClientID: actor.ClientID,
		ScopeHash: hex.EncodeToString(scope[:]), CustomerSessionHash: hex.EncodeToString(customer[:]),
	}
}

func sameOwner(left, right Owner) bool {
	if left.CompanyID != right.CompanyID || left.Mode != right.Mode || left.ClientID != right.ClientID || left.ScopeHash != right.ScopeHash || left.CustomerSessionHash != right.CustomerSessionHash || (left.KioskID == nil) != (right.KioskID == nil) {
		return false
	}
	return left.KioskID == nil || *left.KioskID == *right.KioskID
}

func itemKey(item Selection) string {
	return fmt.Sprintf("%s:%d:%d:%d", item.Kind, item.ID, item.LineID, item.StaffID)
}

func newOrderID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate checkout id: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:]), nil
}
