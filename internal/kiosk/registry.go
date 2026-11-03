// Package kiosk implements kiosk and customer session boundaries using the
// application's github.com/dronm/session provider.
package kiosk

import (
	"context"
	"errors"

	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/pronogti/internal/salon"
)

// ErrKioskNotFound also covers disabled kiosks and invalid credentials.
var ErrKioskNotFound = errors.New("kiosk not found")

type Kiosk struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	CompanyID      int64  `json:"company_id"`
	IsActive       bool   `json:"-"`
	SessionVersion int64  `json:"-"`
}

type Registry interface {
	FindByToken(context.Context, string) (Kiosk, error)
	FindByID(context.Context, int64) (Kiosk, error)
}

type Options struct {
	CustomerMaxLifeTime int64
	CustomerMaxIdleTime int64
	Salons              salon.Registry
}

// Scope is resolved from the server-side session and current registry records.
// ScopeID changes whenever the selected salon or device interaction changes.
type Scope struct {
	CompanyID int64
	Mode      string
	Salon     *models.SalonIdentity
	Kiosk     *Kiosk
	ScopeID   string
}

// ScopeHeader correlates a request with the salon selection displayed by the
// client. It is separate from both the device credential and customer nonce.
const ScopeHeader = "X-Salon-Session"

type scopeContextKey struct{}

func ContextWithScope(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, scopeContextKey{}, id)
}

func ScopeFromContext(ctx context.Context) string {
	value, _ := ctx.Value(scopeContextKey{}).(string)
	return value
}
