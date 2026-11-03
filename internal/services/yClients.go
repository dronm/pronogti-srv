// Package services
package services

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/dronm/goyclients"
	"github.com/dronm/pronogti/internal/cache"
	"github.com/dronm/pronogti/internal/config"
	"github.com/dronm/pronogti/internal/settlement"
)

type authAttempt struct {
	done chan struct{}
	err  error
}

type contextHTTPClient struct {
	context context.Context
	client  goyclients.HTTPClient
}

func (c contextHTTPClient) Do(request *http.Request) (*http.Response, error) {
	return c.client.Do(request.WithContext(c.context))
}

type YClientT struct {
	mu     sync.Mutex
	Client *goyclients.YClients
	Auth   *goyclients.Auth

	authMu      sync.Mutex
	authAttempt *authAttempt
	authedAt    time.Time
	persist     func(token string, at time.Time) error
}

func (c *YClientT) isAuthValid() bool {
	if c.authedAt.IsZero() {
		return false
	}
	// TODO: replace with real expiration logic
	return time.Since(c.authedAt) < time.Hour
}

func (c *YClientT) auth(ctx context.Context) error {
	var attempt *authAttempt
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.authMu.Lock()
		if err := ctx.Err(); err != nil {
			c.authMu.Unlock()
			return err
		}
		if c.isAuthValid() {
			c.authMu.Unlock()
			return nil
		}
		if pending := c.authAttempt; pending != nil {
			c.authMu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-pending.done:
				// A canceled refresh must not cancel unrelated waiting requests.
				if errors.Is(pending.err, context.Canceled) || errors.Is(pending.err, context.DeadlineExceeded) {
					continue
				}
				return pending.err
			}
		}
		attempt = &authAttempt{done: make(chan struct{})}
		c.authAttempt = attempt
		c.authMu.Unlock()
		break
	}

	// Authentication mutates Client.User, so serialize it with all domain calls.
	release, err := c.lockClient(ctx)
	var token string
	var authedAt time.Time
	if err == nil {
		err = c.Client.Auth(c.Auth)
		token = c.Client.User.UserToken
		authedAt = time.Now()
		release()
	}

	// Never hold the API mutex while updating auth state or persisting a token.
	c.authMu.Lock()
	defer c.authMu.Unlock()

	attempt.err = err
	c.authAttempt = nil
	if err == nil {
		c.authedAt = authedAt
		if c.persist != nil {
			if err := c.persist(token, c.authedAt); err != nil {
				slog.Error("persist YClients token", "error", err)
			}
		}
	}
	close(attempt.done)

	return err
}

// lockClient binds every request in a serialized YClients operation to ctx.
// Its release function restores the original transport before unlocking.
func (c *YClientT) lockClient(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	original := c.Client.HTTPClient
	c.Client.HTTPClient = contextHTTPClient{context: ctx, client: original}
	return func() {
		c.Client.HTTPClient = original
		c.mu.Unlock()
	}, nil
}

// NewYClient initializes one shared YClients connection without authenticating.
// persist is called after successful authentication and may be nil.
func NewYClient(
	cfg config.YClients,
	isDebug bool,
	persist func(token string, at time.Time) error,
	httpClients ...goyclients.HTTPClient,
) (*YClientT, error) {
	if cfg.Login == "" {
		return nil, errors.New("YClients Login not set")
	} else if cfg.Password == "" {
		return nil, errors.New("YClients Password not set")
	} else if cfg.PartnerToken == "" {
		return nil, errors.New("YClients PartnerToken not set")
	}
	if len(httpClients) > 1 {
		return nil, errors.New("only one YClients HTTP client may be supplied")
	}
	var httpClient goyclients.HTTPClient
	if len(httpClients) == 1 {
		httpClient = httpClients[0]
	}

	client := &YClientT{
		Client:  goyclients.NewYClients(cfg.PartnerToken, httpClient),
		Auth:    &goyclients.Auth{Login: cfg.Login, Password: cfg.Password},
		persist: persist,
	}
	goyclients.IsDebug = isDebug

	if cfg.UserToken != "" {
		client.Client.User.UserToken = cfg.UserToken
		if cfg.AuthedAt != nil {
			client.authedAt = *cfg.AuthedAt
		}
	}

	return client, nil
}

type YClientService struct {
	yClient         *YClientT
	cache           *cache.Cache
	paidVisitLookup func(context.Context, int64, int64) ([]settlement.VisitReference, error)
}

func NewYClientService(client *YClientT, cache *cache.Cache) *YClientService {
	return &YClientService{yClient: client, cache: cache}
}
