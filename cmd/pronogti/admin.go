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
	"github.com/dronm/pronogti/internal/httpapi"
	websession "github.com/dronm/webapp/session"
)

func configuredAdministrator(cfg *config.Config, db ds.Provider) (http.Handler, func(), error) {
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
	handler := httpapi.NewAdminHandler(service, sessions, httpapi.AdminHTTPOptions{AllowedOrigins: cfg.Admin.AllowedOrigins, RequestTimeout: requestTimeout, Version: Version, Commit: Commit, BuildDate: BuildDate, Navigation: adminnav.NewService(db), GeneratedRoutes: generated, GeneratedPermissions: permissions})
	return handler, cleanup, nil
}
