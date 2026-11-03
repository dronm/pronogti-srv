package kiosk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/pronogti/internal/salon"
	"github.com/dronm/session"
	"github.com/dronm/webapp"
)

const (
	CustomerHeader    = "X-Customer-Session"
	kioskKey          = "kiosk"
	activeCustomerKey = "customer_active"
	scopeKey          = "salon_scope"
)

type scopeState struct {
	ID           string
	Mode         string
	CompanyID    int64
	Slug         string
	SalonVersion int64
}

// Manager stores public and kiosk identities through the existing session API.
// Customer interactions expire independently from the device registration.
type Manager struct {
	registry Registry
	options  Options
	now      func() time.Time
}

func NewManager(registry Registry, options Options) *Manager {
	if options.CustomerMaxLifeTime <= 0 {
		options.CustomerMaxLifeTime = 1800
	}
	if options.CustomerMaxIdleTime <= 0 {
		options.CustomerMaxIdleTime = 180
	}
	return &Manager{registry: registry, options: options, now: time.Now}
}

func KioskRequired() error {
	return webapp.Unauthorized("kiosk session required", map[string]any{"code": "kiosk_required"})
}

func SalonRequired() error {
	return webapp.Unauthorized("select a salon before starting a customer session", map[string]any{"code": "salon_required"})
}

func CustomerRequired() error {
	return webapp.Unauthorized("customer session required", map[string]any{"code": "customer_session_required"})
}

// NormalizePhone accepts Russian phone input and stores a canonical eleven digits.
func NormalizePhone(value string) (string, error) {
	var digits strings.Builder
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == '+' || r == '(' || r == ')' || r == '-' || r == ' ':
		default:
			return "", webapp.BadRequest("phone should contain a valid Russian telephone number", nil)
		}
	}
	phone := digits.String()
	if len(phone) == 10 {
		phone = "7" + phone
	}
	if len(phone) == 11 && phone[0] == '8' {
		phone = "7" + phone[1:]
	}
	if len(phone) != 11 || phone[0] != '7' {
		return "", webapp.BadRequest("phone should contain eleven digits beginning with 7", nil)
	}
	return phone, nil
}

func (m *Manager) Bind(ctx context.Context, sess session.Session, token string) (models.SessionResponse, error) {
	if sess == nil || m.registry == nil {
		return models.SessionResponse{}, KioskRequired()
	}
	if strings.TrimSpace(token) == "" || len(token) > 512 {
		return models.SessionResponse{}, webapp.BadRequest("kiosk token is required", nil)
	}
	device, err := m.registry.FindByToken(ctx, token)
	if errors.Is(err, ErrKioskNotFound) || err == nil && (!device.IsActive || device.CompanyID <= 0 || device.ID <= 0) {
		return models.SessionResponse{}, KioskRequired()
	}
	if err != nil {
		return models.SessionResponse{}, fmt.Errorf("find kiosk: %w", err)
	}
	scope, err := m.kioskScope(ctx, device, "")
	if err != nil {
		return models.SessionResponse{}, err
	}
	if err := m.clearCustomer(sess, ""); err != nil {
		return models.SessionResponse{}, err
	}
	if err := sess.Set(kioskKey, device); err != nil {
		return models.SessionResponse{}, fmt.Errorf("set kiosk session: %w", err)
	}
	if err := m.storeScope(sess, scope); err != nil {
		return models.SessionResponse{}, err
	}
	return m.response(scope, nil), nil
}

// ClearKiosk revokes cached device identity without discarding a separate public
// salon interaction. Durable device credential validation is handled by the resolver.
func (m *Manager) ClearKiosk(sess session.Session) error {
	if sess == nil {
		return nil
	}
	if err := sess.Delete(kioskKey); err != nil {
		return err
	}
	var state scopeState
	if err := sess.Get(scopeKey, &state); err == nil && state.Mode == "kiosk" {
		if err := sess.Delete(scopeKey); err != nil {
			return err
		}
		if err := m.clearCustomer(sess, ""); err != nil {
			return err
		}
	}
	return sess.Flush()
}

func (m *Manager) RequireKiosk(ctx context.Context, sess session.Session) (Kiosk, error) {
	if sess == nil || m.registry == nil {
		return Kiosk{}, KioskRequired()
	}
	var stored Kiosk
	if err := sess.Get(kioskKey, &stored); err != nil || stored.ID <= 0 {
		return Kiosk{}, KioskRequired()
	}
	device, err := m.registry.FindByID(ctx, stored.ID)
	if errors.Is(err, ErrKioskNotFound) || err == nil && (!device.IsActive || device.CompanyID <= 0 || device.CompanyID != stored.CompanyID || device.SessionVersion != stored.SessionVersion) {
		_ = m.ClearKiosk(sess)
		return Kiosk{}, KioskRequired()
	}
	if err != nil {
		return Kiosk{}, fmt.Errorf("read kiosk binding: %w", err)
	}
	return device, nil
}

// SelectSalon establishes public company scope. The slug is resolved by the
// registry; the caller never supplies an authoritative company or client ID.
func (m *Manager) SelectSalon(ctx context.Context, sess session.Session, slug string) (models.SessionResponse, error) {
	if sess == nil || m.options.Salons == nil {
		return models.SessionResponse{}, SalonRequired()
	}
	entry, err := m.options.Salons.FindBySlug(ctx, slug)
	if errors.Is(err, salon.ErrSalonNotFound) || err == nil && !validSalon(entry) {
		return models.SessionResponse{}, webapp.NotFound("salon is unavailable", nil)
	}
	if err != nil {
		return models.SessionResponse{}, fmt.Errorf("find salon: %w", err)
	}
	id, err := randomID()
	if err != nil {
		return models.SessionResponse{}, err
	}
	scope := Scope{ScopeID: id, CompanyID: entry.CompanyID, Mode: "public", Salon: &entry.SalonIdentity}
	if err := m.clearCustomer(sess, ""); err != nil {
		return models.SessionResponse{}, err
	}
	if err := m.storeScopeVersion(sess, scope, entry.SessionVersion); err != nil {
		return models.SessionResponse{}, err
	}
	return m.response(scope, nil), nil
}

func (m *Manager) RequireScope(ctx context.Context, sess session.Session) (Scope, error) {
	scope, err := m.resolveScope(ctx, sess)
	if err != nil {
		return Scope{}, err
	}
	if expected := ScopeFromContext(ctx); expected != "" && expected != scope.ScopeID {
		return Scope{}, SalonRequired()
	}
	return scope, nil
}

func (m *Manager) resolveScope(ctx context.Context, sess session.Session) (Scope, error) {
	if sess == nil {
		return Scope{}, SalonRequired()
	}
	var state scopeState
	if err := sess.Get(scopeKey, &state); err != nil || state.ID == "" {
		// Upgrade an existing kiosk Redis record created before salon contexts.
		var stored Kiosk
		if err := sess.Get(kioskKey, &stored); err != nil || stored.ID <= 0 {
			return Scope{}, SalonRequired()
		}
		device, err := m.RequireKiosk(ctx, sess)
		if err != nil {
			return Scope{}, err
		}
		scope, err := m.kioskScope(ctx, device, "")
		if err != nil {
			return Scope{}, err
		}
		if err := m.storeScope(sess, scope); err != nil {
			return Scope{}, err
		}
		return scope, nil
	}
	if state.Mode == "kiosk" {
		device, err := m.RequireKiosk(ctx, sess)
		if err != nil {
			return Scope{}, err
		}
		if device.CompanyID != state.CompanyID {
			return Scope{}, SalonRequired()
		}
		return m.kioskScope(ctx, device, state.ID)
	}
	if state.Mode != "public" || m.options.Salons == nil {
		return Scope{}, SalonRequired()
	}
	entry, err := m.options.Salons.FindBySlug(ctx, state.Slug)
	if errors.Is(err, salon.ErrSalonNotFound) || err == nil && (!validSalon(entry) || entry.CompanyID != state.CompanyID || entry.SessionVersion != state.SalonVersion) {
		_ = m.clearCustomer(sess, "")
		_ = sess.Delete(scopeKey)
		return Scope{}, SalonRequired()
	}
	if err != nil {
		return Scope{}, fmt.Errorf("read salon binding: %w", err)
	}
	return Scope{ScopeID: state.ID, CompanyID: entry.CompanyID, Mode: "public", Salon: &entry.SalonIdentity}, nil
}

func (m *Manager) Current(ctx context.Context, sess session.Session) (models.SessionResponse, error) {
	if sess == nil {
		return m.response(Scope{}, nil), nil
	}
	var storedScope scopeState
	var storedKiosk Kiosk
	hasScope := sess.Get(scopeKey, &storedScope) == nil && storedScope.ID != ""
	hasKiosk := sess.Get(kioskKey, &storedKiosk) == nil && storedKiosk.ID > 0
	if !hasScope && !hasKiosk {
		return m.response(Scope{}, nil), nil
	}
	scope, err := m.RequireScope(ctx, sess)
	if err != nil {
		return models.SessionResponse{}, err
	}
	customer, err := m.currentCustomer(sess, "", scope.ScopeID)
	if err != nil {
		return models.SessionResponse{}, err
	}
	return m.response(scope, customer), nil
}

// KioskCurrent is the strict device-mode counterpart to the public bootstrap.
// It does not change an explicit public selection through a GET request.
func (m *Manager) KioskCurrent(ctx context.Context, sess session.Session) (models.SessionResponse, error) {
	if _, err := m.RequireKiosk(ctx, sess); err != nil {
		return models.SessionResponse{}, err
	}
	current, err := m.Current(ctx, sess)
	if err != nil {
		return models.SessionResponse{}, err
	}
	if current.Mode != "kiosk" {
		return models.SessionResponse{}, KioskRequired()
	}
	return current, nil
}

func (m *Manager) StartCustomer(ctx context.Context, sess session.Session, phone string, guest bool) (Scope, models.CustomerSession, error) {
	scope, err := m.RequireScope(ctx, sess)
	if err != nil {
		return Scope{}, models.CustomerSession{}, err
	}
	if guest && strings.TrimSpace(phone) != "" {
		return Scope{}, models.CustomerSession{}, webapp.BadRequest("provide a phone or choose guest, not both", nil)
	}
	if !guest {
		phone, err = NormalizePhone(phone)
		if err != nil {
			return Scope{}, models.CustomerSession{}, err
		}
	}
	id, err := randomID()
	if err != nil {
		return Scope{}, models.CustomerSession{}, err
	}
	state := models.CustomerSession{SessionID: id, ScopeID: scope.ScopeID, Phone: phone, Guest: guest, ExpiresAt: m.now().Add(time.Duration(m.options.CustomerMaxLifeTime) * time.Second)}
	if err := m.clearCustomer(sess, ""); err != nil {
		return Scope{}, models.CustomerSession{}, err
	}
	if err := sess.Set(customerKey(state.SessionID), state); err != nil {
		return Scope{}, models.CustomerSession{}, err
	}
	if err := sess.Set(activityKey(state.SessionID), m.now()); err != nil {
		return Scope{}, models.CustomerSession{}, err
	}
	// A late request can publish only a nonce whose state retains its old scope
	// correlation. RequireCustomer and Current reject it after any scope change.
	if err := sess.Set(activeCustomerKey, state.SessionID); err != nil {
		return Scope{}, models.CustomerSession{}, err
	}
	if err := sess.Flush(); err != nil {
		return Scope{}, models.CustomerSession{}, err
	}
	return scope, state, nil
}

func (m *Manager) UpdateCustomer(sess session.Session, state models.CustomerSession) error {
	var scope scopeState
	if err := sess.Get(scopeKey, &scope); err != nil || scope.ID == "" || scope.ID != state.ScopeID {
		return CustomerRequired()
	}
	current, err := m.currentCustomer(sess, state.SessionID, scope.ID)
	if err != nil {
		return err
	}
	if current == nil {
		return CustomerRequired()
	}
	if err := sess.Set(customerKey(state.SessionID), state); err != nil {
		return err
	}
	return sess.Flush()
}

func (m *Manager) RequireCustomer(ctx context.Context, sess session.Session, nonce string) (Scope, models.CustomerSession, error) {
	scope, err := m.RequireScope(ctx, sess)
	if err != nil {
		return Scope{}, models.CustomerSession{}, err
	}
	if nonce == "" {
		return Scope{}, models.CustomerSession{}, CustomerRequired()
	}
	state, err := m.currentCustomer(sess, nonce, scope.ScopeID)
	if err != nil {
		return Scope{}, models.CustomerSession{}, err
	}
	if state == nil {
		return Scope{}, models.CustomerSession{}, CustomerRequired()
	}
	return scope, *state, nil
}

func (m *Manager) Touch(ctx context.Context, sess session.Session, nonce string) (models.SessionResponse, error) {
	scope, state, err := m.RequireCustomer(ctx, sess, nonce)
	if err != nil {
		return models.SessionResponse{}, err
	}
	if err := sess.Set(activityKey(nonce), m.now()); err != nil {
		return models.SessionResponse{}, err
	}
	if err := sess.Flush(); err != nil {
		return models.SessionResponse{}, err
	}
	return m.response(scope, &state), nil
}

func (m *Manager) End(ctx context.Context, sess session.Session, nonce string) error {
	if _, _, err := m.RequireCustomer(ctx, sess, nonce); err != nil {
		return err
	}
	return m.clearCustomer(sess, nonce)
}

func (m *Manager) currentCustomer(sess session.Session, expected, scopeID string) (*models.CustomerSession, error) {
	var nonce string
	if err := sess.Get(activeCustomerKey, &nonce); err != nil || nonce == "" {
		return nil, nil
	}
	if expected != "" && nonce != expected {
		return nil, CustomerRequired()
	}
	var state models.CustomerSession
	var lastActivity time.Time
	if err := sess.Get(customerKey(nonce), &state); err != nil || state.SessionID != nonce {
		return nil, nil
	}
	if state.ScopeID == "" || state.ScopeID != scopeID {
		if err := m.clearCustomer(sess, nonce); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err := sess.Get(activityKey(nonce), &lastActivity); err != nil {
		return nil, nil
	}
	if !m.now().Before(state.ExpiresAt) || !m.now().Before(lastActivity.Add(time.Duration(m.options.CustomerMaxIdleTime)*time.Second)) {
		if err := m.clearCustomer(sess, nonce); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return &state, nil
}

func (m *Manager) clearCustomer(sess session.Session, expected string) error {
	var nonce string
	if err := sess.Get(activeCustomerKey, &nonce); err != nil {
		return nil
	}
	if expected != "" && nonce != expected {
		return CustomerRequired()
	}
	if nonce != "" {
		if err := sess.Delete(customerKey(nonce)); err != nil {
			return err
		}
		if err := sess.Delete(activityKey(nonce)); err != nil {
			return err
		}
	}
	// Concurrent transitions can cause an empty interaction, never reuse of a
	// previous customer's identity under the next salon or device context.
	if err := sess.Delete(activeCustomerKey); err != nil {
		return err
	}
	return sess.Flush()
}

func (m *Manager) kioskScope(ctx context.Context, device Kiosk, id string) (Scope, error) {
	if id == "" {
		var err error
		id, err = randomID()
		if err != nil {
			return Scope{}, err
		}
	}
	scope := Scope{ScopeID: id, Mode: "kiosk", CompanyID: device.CompanyID, Kiosk: &device}
	if m.options.Salons != nil {
		entry, err := m.options.Salons.FindByCompanyID(ctx, device.CompanyID)
		if errors.Is(err, salon.ErrSalonNotFound) || err == nil && !validSalon(entry) {
			return Scope{}, SalonRequired()
		}
		if err != nil {
			return Scope{}, fmt.Errorf("find kiosk salon: %w", err)
		}
		scope.Salon = &entry.SalonIdentity
	}
	return scope, nil
}

func (m *Manager) storeScope(sess session.Session, scope Scope) error {
	return m.storeScopeVersion(sess, scope, 0)
}

func (m *Manager) storeScopeVersion(sess session.Session, scope Scope, version int64) error {
	state := scopeState{ID: scope.ScopeID, Mode: scope.Mode, CompanyID: scope.CompanyID, SalonVersion: version}
	if scope.Salon != nil {
		state.Slug = scope.Salon.Slug
	}
	if err := sess.Set(scopeKey, state); err != nil {
		return err
	}
	return sess.Flush()
}

func validSalon(entry salon.Entry) bool {
	return entry.IsActive && entry.CompanyID > 0 && entry.Slug != "" && entry.SessionVersion > 0
}

func (m *Manager) response(scope Scope, customer *models.CustomerSession) models.SessionResponse {
	result := models.SessionResponse{Salon: scope.Salon, Mode: scope.Mode, ScopeID: scope.ScopeID, Customer: customer, CustomerIdleTimeoutSeconds: m.options.CustomerMaxIdleTime}
	if scope.Kiosk != nil {
		result.Kiosk = &models.KioskIdentity{ID: scope.Kiosk.ID, Name: scope.Kiosk.Name, CompanyID: scope.Kiosk.CompanyID}
	}
	return result
}

func randomID() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func customerKey(nonce string) string { return "customer:" + nonce }
func activityKey(nonce string) string { return "customer_activity:" + nonce }
