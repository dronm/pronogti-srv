package httpapi

import (
	"context"
	"fmt"

	"github.com/dronm/goyclients"
	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/kiosk"
	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/pronogti/internal/payment"
	"github.com/dronm/pronogti/internal/salon"
	"github.com/dronm/session"
	"github.com/dronm/webapp"
)

// Backend is the existing YClients functionality. Company and customer identity
// are supplied by the request-scoped Redis session, never by a URL selector.
type Backend interface {
	FetchCompanies(context.Context) ([]*goyclients.Company, error)
	FindClientByPhone(context.Context, int64, string) (goyclients.Client, error)
	FetchPayDraft(context.Context, int64, int64) (models.PayDraft, error)
	AddClient(context.Context, int64, models.NewClient) (models.ClientKey, error)
	FetchStaffList(context.Context, int64) ([]goyclients.Staff, error)
	FetchServiceCatList(context.Context, int64, *int64) ([]*goyclients.ServiceCategory, error)
	FetchServiceList(context.Context, int64, *int64, *int64, string) ([]*goyclients.Service, error)
	FetchGoodsList(context.Context, int64, string) ([]*goyclients.Good, error)
}

type service struct {
	backend   Backend
	session   session.Session
	manager   *kiosk.Manager
	salons    salon.Registry
	kioskOnly bool
	checkout  *CheckoutDependencies
}

// CheckoutDependencies are optional for callers that use only the session API.
// Runtime wiring always supplies the durable store and fresh YClients provider.
type CheckoutDependencies struct {
	Provider      checkout.Provider
	Repository    checkout.Repository
	MaxLifeTime   int64
	ValidateOrder func(context.Context, checkout.Order) error
	Payment       *payment.Dependencies
}

func RegisterServices(backend Backend, registry kiosk.Registry, options kiosk.Options, checkoutOptions ...CheckoutDependencies) {
	manager := kiosk.NewManager(registry, options)
	var dependency *CheckoutDependencies
	if len(checkoutOptions) > 0 {
		dependency = &checkoutOptions[0]
	}
	for _, name := range []string{"Customer", "Kiosk"} {
		kioskOnly := name == "Kiosk"
		webapp.MustRegisterService(name, &service{}, func(ctx webapp.ServiceContext) any {
			return &service{backend: backend, session: ctx.Session, manager: manager, salons: options.Salons, kioskOnly: kioskOnly, checkout: dependency}
		})
	}
}

func (s *service) BindKiosk(ctx context.Context, input bindKioskRequest) (models.SessionResponse, error) {
	return s.manager.Bind(ctx, s.session, input.Token)
}

func (s *service) SessionInfo(ctx context.Context) (models.SessionResponse, error) {
	if s.kioskOnly {
		return s.manager.KioskCurrent(ctx, s.session)
	}
	return s.manager.Current(ctx, s.session)
}

func (s *service) Salons(ctx context.Context) ([]models.SalonIdentity, error) {
	if s.salons == nil {
		return nil, fmt.Errorf("salon registry is not configured")
	}
	entries, err := s.salons.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]models.SalonIdentity, 0, len(entries))
	for _, entry := range entries {
		if entry.IsActive {
			result = append(result, entry.SalonIdentity)
		}
	}
	return result, nil
}

func (s *service) SelectSalon(ctx context.Context, input selectSalonRequest) (models.SessionResponse, error) {
	return s.manager.SelectSalon(ctx, s.session, input.Slug)
}

func (s *service) requireChannel(ctx context.Context) error {
	if s.kioskOnly {
		_, err := s.manager.KioskCurrent(ctx, s.session)
		return err
	}
	if kiosk.ScopeFromContext(ctx) == "" {
		return kiosk.SalonRequired()
	}
	return nil
}

func (s *service) requireScope(ctx context.Context) (kiosk.Scope, error) {
	if err := s.requireChannel(ctx); err != nil {
		return kiosk.Scope{}, err
	}
	return s.manager.RequireScope(ctx, s.session)
}

func (s *service) requireCustomer(ctx context.Context, nonce string) (kiosk.Scope, models.CustomerSession, error) {
	if err := s.requireChannel(ctx); err != nil {
		return kiosk.Scope{}, models.CustomerSession{}, err
	}
	return s.manager.RequireCustomer(ctx, s.session, nonce)
}

func (s *service) StartCustomer(ctx context.Context, input startCustomerRequest) (models.SessionResponse, error) {
	if err := s.requireChannel(ctx); err != nil {
		return models.SessionResponse{}, err
	}
	device, state, err := s.manager.StartCustomer(ctx, s.session, input.Phone, input.Guest)
	if err != nil {
		return models.SessionResponse{}, err
	}
	if !state.Guest {
		client, err := s.backend.FindClientByPhone(ctx, device.CompanyID, state.Phone)
		if err != nil {
			_ = s.manager.End(ctx, s.session, state.SessionID)
			return models.SessionResponse{}, err
		}
		state.ClientID = int64(client.ID)
		state.Name = client.Name
		state.Email = client.Email
		state.Registered = state.ClientID > 0
		if err := s.manager.UpdateCustomer(s.session, state); err != nil {
			return models.SessionResponse{}, err
		}
	}
	return s.currentForNonce(ctx, state.SessionID)
}

func (s *service) EndCustomer(ctx context.Context, input customerRequest) (*models.SessionResponse, error) {
	if err := s.requireChannel(ctx); err != nil {
		return nil, err
	}
	if err := s.manager.End(ctx, s.session, input.Nonce); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *service) TouchCustomer(ctx context.Context, input customerRequest) (models.SessionResponse, error) {
	if err := s.requireChannel(ctx); err != nil {
		return models.SessionResponse{}, err
	}
	return s.manager.Touch(ctx, s.session, input.Nonce)
}

func (s *service) Company(ctx context.Context) (*goyclients.Company, error) {
	device, err := s.requireScope(ctx)
	if err != nil {
		return nil, err
	}
	companies, err := s.backend.FetchCompanies(ctx)
	if err != nil {
		return nil, err
	}
	for _, company := range companies {
		if company != nil && int64(company.ID) == device.CompanyID {
			return company, nil
		}
	}
	return nil, webapp.NotFound("assigned company is unavailable", nil)
}

func (s *service) Staff(ctx context.Context) ([]goyclients.Staff, error) {
	device, err := s.requireScope(ctx)
	if err != nil {
		return nil, err
	}
	return s.backend.FetchStaffList(ctx, device.CompanyID)
}

func (s *service) ServiceCategories(ctx context.Context, input serviceCategoriesRequest) ([]*goyclients.ServiceCategory, error) {
	device, err := s.requireScope(ctx)
	if err != nil {
		return nil, err
	}
	return s.backend.FetchServiceCatList(ctx, device.CompanyID, input.StaffID)
}

func (s *service) Services(ctx context.Context, input serviceListRequest) ([]*goyclients.Service, error) {
	device, err := s.requireScope(ctx)
	if err != nil {
		return nil, err
	}
	return s.backend.FetchServiceList(ctx, device.CompanyID, input.StaffID, input.CategoryID, input.Term)
}

func (s *service) Goods(ctx context.Context, input goodsListRequest) ([]*goyclients.Good, error) {
	device, err := s.requireScope(ctx)
	if err != nil {
		return nil, err
	}
	return s.backend.FetchGoodsList(ctx, device.CompanyID, input.Term)
}

func (s *service) PayDraft(ctx context.Context, input customerRequest) (models.PayDraft, error) {
	device, customer, err := s.requireCustomer(ctx, input.Nonce)
	if err != nil {
		return models.PayDraft{}, err
	}
	if customer.Guest {
		return models.PayDraft{Items: []models.PayDraftItem{}}, nil
	}
	if !customer.Registered || customer.ClientID <= 0 {
		return models.PayDraft{}, webapp.Conflict("customer registration is required", nil)
	}
	return s.backend.FetchPayDraft(ctx, device.CompanyID, customer.ClientID)
}

func (s *service) RegisterClient(ctx context.Context, input kioskRegisterRequest) (models.SessionResponse, error) {
	device, customer, err := s.requireCustomer(ctx, input.Nonce)
	if err != nil {
		return models.SessionResponse{}, err
	}
	if customer.Guest {
		return models.SessionResponse{}, webapp.Conflict("start a phone customer session before registering", nil)
	}
	phone, err := kiosk.NormalizePhone(input.Client.Phone)
	if err != nil {
		return models.SessionResponse{}, err
	}
	if phone != customer.Phone {
		return models.SessionResponse{}, webapp.Forbidden("registration phone differs from the current customer session", nil)
	}
	if customer.Registered {
		return s.currentForNonce(ctx, input.Nonce)
	}
	input.Client.Phone = customer.Phone
	// Check once more before creation so an existing external client is reused.
	existing, err := s.backend.FindClientByPhone(ctx, device.CompanyID, customer.Phone)
	if err != nil {
		return models.SessionResponse{}, err
	}
	if existing.ID > 0 {
		customer.ClientID = int64(existing.ID)
		customer.Name, customer.Email = existing.Name, existing.Email
	} else {
		key, err := s.backend.AddClient(ctx, device.CompanyID, input.Client)
		if err != nil {
			return models.SessionResponse{}, err
		}
		if key.ID <= 0 {
			return models.SessionResponse{}, fmt.Errorf("YClients registration returned an invalid client ID")
		}
		customer.ClientID = key.ID
		if input.Client.Name != nil {
			customer.Name = *input.Client.Name
		}
		if input.Client.Email != nil {
			customer.Email = *input.Client.Email
		}
	}
	customer.Registered = true
	if err := s.manager.UpdateCustomer(s.session, customer); err != nil {
		return models.SessionResponse{}, err
	}
	return s.currentForNonce(ctx, input.Nonce)
}

func (s *service) currentForNonce(ctx context.Context, nonce string) (models.SessionResponse, error) {
	if _, _, err := s.requireCustomer(ctx, nonce); err != nil {
		return models.SessionResponse{}, err
	}
	return s.manager.Current(ctx, s.session)
}
