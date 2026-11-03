package main

import (
	"context"
	"net/http"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/pronogti/internal/cache"
	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/config"
	"github.com/dronm/pronogti/internal/fiscal"
	"github.com/dronm/pronogti/internal/httpapi"
	"github.com/dronm/pronogti/internal/kiosk"
	"github.com/dronm/pronogti/internal/payment"
	"github.com/dronm/pronogti/internal/salon"
	"github.com/dronm/pronogti/internal/services"
	"github.com/dronm/pronogti/internal/settlement"
)

func registerServices(cfg *config.Config, registry kiosk.Registry, salons salon.Registry, db ds.Provider) (*services.YClientService, error) {
	clientTimeout, err := cfg.ClientTimeout()
	if err != nil {
		return nil, err
	}
	cacheTTL, err := cfg.CacheTTLDuration()
	if err != nil {
		return nil, err
	}
	client, err := services.NewYClient(
		cfg.YClients,
		false,
		cfg.SaveYClientsToken,
		&http.Client{Timeout: clientTimeout},
	)
	if err != nil {
		return nil, err
	}
	backend := services.NewYClientService(client, cache.New(cacheTTL))
	settlements := settlement.NewPostgresRepository(db)
	settlementProvider := services.NewSettlementProvider(backend)
	backend.SetPaidVisitLookup(settlements.HeldPaidVisits)
	httpapi.RegisterServices(backend, registry, kiosk.Options{CustomerMaxLifeTime: cfg.Session.CustomerMaxLifeTime, CustomerMaxIdleTime: cfg.Session.CustomerMaxIdleTime, Salons: salons}, httpapi.CheckoutDependencies{
		Provider: services.NewCheckoutProvider(backend), Repository: checkout.NewPostgresRepository(db), MaxLifeTime: cfg.Checkout.MaxLifeTime,
		ValidateOrder: fiscal.NewPostgresRepository(db).ValidateCheckout,
		Payment: &payment.Dependencies{
			Repository: payment.NewPostgresRepository(db),
			Bank:       &payment.SDKBank{MockExternal: cfg.Payments.MockExternal},
			CardBank:   &payment.SDKCardBank{MockExternal: cfg.Payments.MockExternal},
			Tokens:     payment.NewEnvironmentTokens(cfg.Payments.JWTKeyEnv),
			ValidatePayable: func(ctx context.Context, order checkout.Order) error {
				if err := services.NewCheckoutProvider(backend).ValidatePayable(ctx, order); err != nil {
					return err
				}
				if err := settlements.ValidateCheckout(ctx, order); err != nil {
					return err
				}
				profile, err := settlements.CheckoutProfile(ctx, order.ID)
				if err != nil {
					return err
				}
				return settlementProvider.Preflight(ctx, order, profile)
			},
			CreatingTimeout: time.Duration(cfg.Payments.CreatingTimeoutSeconds) * time.Second,
			MockExternal:    cfg.Payments.MockExternal,
		},
	})
	return backend, nil
}
