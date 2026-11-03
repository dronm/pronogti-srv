package models

import "time"

// CustomerSessionID identifies one interaction, not the Redis session cookie.
type CustomerSession struct {
	SessionID  string    `json:"session_id"`
	ClientID   int64     `json:"client_id"`
	Phone      string    `json:"phone"`
	Name       string    `json:"name"`
	Email      string    `json:"email"`
	Guest      bool      `json:"guest"`
	Registered bool      `json:"registered"`
	ExpiresAt  time.Time `json:"expires_at"`
	// ScopeID binds the interaction to one server-selected salon context.
	ScopeID string `json:"-"`
}

type SalonIdentity struct {
	CompanyID int64  `json:"company_id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
}

type KioskIdentity struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	CompanyID int64  `json:"company_id"`
}

type SessionResponse struct {
	Kiosk                      *KioskIdentity   `json:"kiosk"`
	Salon                      *SalonIdentity   `json:"salon"`
	Mode                       string           `json:"mode"`
	ScopeID                    string           `json:"scope_id"`
	Customer                   *CustomerSession `json:"customer"`
	CustomerIdleTimeoutSeconds int64            `json:"customer_idle_timeout_seconds"`
}
