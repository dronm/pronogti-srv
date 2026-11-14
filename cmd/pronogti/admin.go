package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/pronogti/internal/adminauth"
	"github.com/dronm/pronogti/internal/adminnav"
	"github.com/dronm/pronogti/internal/config"
	"github.com/dronm/pronogti/internal/fiscal"
	"github.com/dronm/pronogti/internal/httpapi"
	"github.com/dronm/pronogti/internal/payment"
	"github.com/dronm/pronogti/internal/refund"
	"github.com/dronm/pronogti/internal/terminalbridge"
	websession "github.com/dronm/webapp/session"
)

func configuredAdministrator(cfg *config.Config, db ds.Provider, workers *backgroundWorkers, paymentTokens payment.TokenSource) (http.Handler, func(), error) {
	if !cfg.Admin.Enabled {
		return nil, func() {}, nil
	}
	redisConfig, err := cfg.Session.Redis.Resolve()
	if err != nil {
		return nil, nil, err
	}
	redisConfig.Namespace = cfg.Admin.RedisNamespace
	manager, err := websession.NewRedisManager(cfg.Admin.WebappConfig(cfg.Session), redisConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize administrator Redis session manager: %w", err)
	}
	manager.StartGC(os.Stderr, cfg.Debug.LogLevel)
	cleanup := func() { manager.Core().StopGC(); manager.Core().CloseProvider() }
	requestTimeout, err := cfg.RequestTimeout()
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	service := adminauth.NewService(adminauth.NewPostgresRepository(db), adminauth.Options{MaxLifeTime: time.Duration(cfg.Admin.MaxLifeTime) * time.Second})
	sessions := httpapi.NewAdminSessions(manager.Core(), cfg.Admin.WebappConfig(cfg.Session), cfg.Admin.CookiePath, service.Invalidate)
	generated, permissions, err := httpapi.NewGeneratedAdminHandler(service, db, requestTimeout)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	options := httpapi.AdminHTTPOptions{
		TerminalBridge: terminalbridge.NewRepository(db, terminalBridgeOptions(cfg)),
		AllowedOrigins: cfg.Admin.AllowedOrigins, RequestTimeout: requestTimeout,
		Shifts:  fiscal.NewShiftService(fiscal.NewPostgresRepository(db), fiscal.HTTPDriver{}, cfg.Fiscal.AllowLive),
		Version: Version, Commit: Commit, BuildDate: BuildDate,
		Navigation: adminnav.NewService(db), GeneratedRoutes: generated, GeneratedPermissions: permissions,
	}
	if cfg.Refunds.Enabled {
		refunds := refund.NewService(refund.NewPostgresRepository(db),
			refund.SDKBank{Tokens: paymentTokens},
			fiscal.HTTPDriver{}, cfg.Refunds.AllowLive && cfg.Fiscal.AllowLive)
		options.Refunds = refunds
		workers.Start("payment refunds", refunds.Run)
	}
	handler := httpapi.NewAdminHandler(service, sessions, options)
	return handler, cleanup, nil
}
