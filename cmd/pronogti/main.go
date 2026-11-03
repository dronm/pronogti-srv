package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dronm/pronogti/internal/config"
	"github.com/dronm/pronogti/internal/fiscal"
	"github.com/dronm/pronogti/internal/httpapi"
	"github.com/dronm/pronogti/internal/kiosk"
	"github.com/dronm/pronogti/internal/payment"
	"github.com/dronm/pronogti/internal/salon"
	"github.com/dronm/pronogti/internal/services"
	"github.com/dronm/pronogti/internal/settlement"
	"github.com/dronm/webapp"
	"github.com/dronm/webapp/database"
	weblogger "github.com/dronm/webapp/logger"
	websession "github.com/dronm/webapp/session"
)

var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

func main() {
	if err := run(); err != nil {
		slog.Error("pronogti stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", config.DefaultConfigPath, "path to JSON configuration")
	flag.Parse()
	if *configPath == config.DefaultConfigPath && flag.NArg() > 0 {
		*configPath = flag.Arg(0)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if err := weblogger.Init(cfg.Debug.LogLevel, cfg.Debug.LogFormat); err != nil {
		return fmt.Errorf("initialize logger: %w", err)
	}
	if cfg.Debug.LogConfig {
		slog.Info("config loaded", "path", *configPath, "http_addr", cfg.HTTP.Addr)
	}
	if !cfg.Session.Enabled {
		return errors.New("Redis sessions are required for the customer API; session.enabled must be true")
	}
	dbConfig, err := cfg.Database.Resolve()
	if err != nil {
		return err
	}
	db, err := database.Open(dbConfig, nil)
	if err != nil {
		return fmt.Errorf("open kiosk database: %w", err)
	}
	defer db.Close()
	redisConfig, err := cfg.Session.Redis.Resolve()
	if err != nil {
		return err
	}
	sessionManager, err := websession.NewRedisManager(cfg.Session.WebappConfig(), redisConfig)
	if err != nil {
		return fmt.Errorf("initialize Redis session manager: %w", err)
	}
	sessionManager.StartGC(os.Stderr, cfg.Debug.LogLevel)
	defer sessionManager.Core().CloseProvider()
	defer sessionManager.Core().StopGC()
	registry := kiosk.NewPostgresRegistry(db)
	salons := salon.NewPostgresRegistry(db)
	backend, err := registerServices(cfg, registry, salons, db)
	if err != nil {
		return fmt.Errorf("register services: %w", err)
	}
	workers := newBackgroundWorkers()
	defer func() {
		shutdownTimeout, err := cfg.ShutdownTimeoutDuration()
		if err != nil {
			shutdownTimeout = 10 * time.Second
		}
		shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := workers.Stop(shutdownContext); err != nil {
			slog.Error("background worker shutdown timed out; unfinished operations retain their journal identities", "error", err)
		}
	}()
	confirmationRepository := payment.NewPostgresRepository(db)
	webhook, err := configuredWebhook(cfg.Payments, func(ctx context.Context, profileID int64, notification payment.VerifiedSBPNotification) error {
		if profileID == 0 {
			return confirmationRepository.ReceiveAny(ctx, notification)
		}
		return confirmationRepository.Receive(ctx, profileID, notification)
	}, confirmationRepository.ReceiveCard)
	if err != nil {
		return err
	}
	if cfg.Payments.WorkerEnabled {
		poll, err := cfg.Payments.PollDuration()
		if err != nil {
			return err
		}
		mockDelay, err := cfg.Payments.MockPaymentDuration()
		if err != nil {
			return err
		}
		worker := payment.NewConfirmationWorker(confirmationRepository, payment.SDKChecker{MockExternal: cfg.Payments.MockExternal}, payment.NewEnvironmentTokens(cfg.Payments.JWTKeyEnv), payment.ConfirmationOptions{
			PollInterval: poll, MockExternal: cfg.Payments.MockExternal, MockAutoPayAfter: mockDelay,
		})
		workers.Start("payment confirmation", worker.Run)
		cardWorker := payment.NewCardConfirmationWorker(confirmationRepository, payment.SDKCardChecker{MockExternal: cfg.Payments.MockExternal}, payment.NewEnvironmentTokens(cfg.Payments.JWTKeyEnv), payment.ConfirmationOptions{
			PollInterval: poll, MockExternal: cfg.Payments.MockExternal, MockAutoPayAfter: mockDelay,
		})
		workers.Start("card payment confirmation", cardWorker.Run)
	}
	if cfg.Settlement.WorkerEnabled {
		poll, err := cfg.Settlement.PollDuration()
		if err != nil {
			return err
		}
		worker := settlement.NewWorker(settlement.NewPostgresRepository(db), services.NewSettlementProvider(backend), settlement.Options{
			AllowLive: cfg.Settlement.AllowLive, PollInterval: poll,
		})
		workers.Start("YClients settlement", worker.Run)
	}
	if cfg.Fiscal.WorkerEnabled {
		poll, err := cfg.Fiscal.PollDuration()
		if err != nil {
			return err
		}
		worker := fiscal.NewWorker(fiscal.NewPostgresRepository(db), fiscal.HTTPDriver{}, fiscal.Options{
			AllowLive: cfg.Fiscal.AllowLive, PollInterval: poll,
		})
		workers.Start("fiscal receipt", worker.Run)
	}

	requestTimeout, err := cfg.RequestTimeout()
	if err != nil {
		return err
	}
	appContext := &webapp.AppContext{DB: db, DefaultTimeout: requestTimeout}
	deviceManager := kiosk.NewManager(registry, kiosk.Options{
		CustomerMaxLifeTime: cfg.Session.CustomerMaxLifeTime,
		CustomerMaxIdleTime: cfg.Session.CustomerMaxIdleTime,
		Salons:              salons,
	})
	sessionResolver := kiosk.NewDeviceSessionResolver(
		kiosk.NewSessionResolver(sessionManager, cfg.Session.WebappConfig()),
		deviceManager, registry, cfg.Session.KioskCookieConfig(),
	)
	middlewares := []webapp.Middleware{
		webapp.RecoverMiddleware(),
		webapp.QueryIDMiddleware(""),
		webapp.RequestLoggerMiddleware(),
		webapp.CORSMiddleware(cfg.CORS.WebappConfig()),
		kiosk.OriginMiddleware(cfg.CORS.AllowedOrigins),
		kiosk.DeviceCookieMiddleware(cfg.Session.KioskCookieConfig()),
		webapp.SessionMiddleware(sessionResolver),
		httpapi.SalonScopeMiddleware(),
	}
	router := webapp.MustBuildRouter(appContext, httpapi.BuildRoutes(), middlewares...)
	handler := httpapi.WithTochkaWebhook(router, webhook)

	readTimeout, err := cfg.HTTPReadTimeout()
	if err != nil {
		return err
	}
	writeTimeout, err := cfg.HTTPWriteTimeout()
	if err != nil {
		return err
	}
	idleTimeout, err := cfg.HTTPIdleTimeout()
	if err != nil {
		return err
	}
	shutdownTimeout, err := cfg.ShutdownTimeoutDuration()
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           handler,
		ReadHeaderTimeout: readTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	serverErrors := make(chan error, 1)
	go func() {
		slog.Info("pronogti started", "addr", cfg.HTTP.Addr, "version", Version, "commit", Commit, "build_date", BuildDate)
		serverErrors <- server.ListenAndServe()
	}()
	stop, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-stop.Done():
	}
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		_ = server.Close()
		return fmt.Errorf("shutdown HTTP server: %w", err)
	}
	slog.Info("pronogti stopped gracefully")
	return nil
}
