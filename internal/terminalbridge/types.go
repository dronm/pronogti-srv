// Package terminalbridge coordinates kiosk terminal payments and authenticated
// outbound Terminal Bridge agents. It never opens a terminal or reads card data.
package terminalbridge

import (
	"encoding/json"
	"time"
)

const Method = "terminal_card"

type Options struct {
	Enabled         bool
	AllowLive       bool
	AllowSimulated  bool
	CommandTTL      time.Duration
	MaxPoll         time.Duration
	Lease           time.Duration
	HeartbeatMaxAge time.Duration
}

type Agent struct {
	credentialHash string
	ID             string          `json:"id"`
	CompanyID      int64           `json:"company_id"`
	KioskID        int64           `json:"kiosk_id"`
	Environment    string          `json:"environment"`
	Enabled        bool            `json:"enabled"`
	LastSeenAt     *time.Time      `json:"last_seen_at"`
	Heartbeat      json.RawMessage `json:"heartbeat,omitempty"`
}

type AgentInput struct {
	ID          string `json:"id"`
	CompanyID   int64  `json:"company_id"`
	KioskID     int64  `json:"kiosk_id"`
	Environment string `json:"environment"`
	Enabled     bool   `json:"enabled"`
}

type AgentCredentials struct {
	Agent Agent  `json:"agent"`
	Token string `json:"token"`
}

type TerminalInput struct {
	AgentID          string `json:"agent_id"`
	CompanyID        int64  `json:"company_id"`
	KioskID          int64  `json:"kiosk_id"`
	PaymentProfileID int64  `json:"payment_profile_id"`
	TerminalID       string `json:"terminal_id"`
	BankTerminalID   string `json:"bank_terminal_id"`
	PhysicalID       string `json:"physical_id"`
	Enabled          bool   `json:"enabled"`
}

type Terminal struct {
	ID int64 `json:"id"`
	TerminalInput
}

type DeviceStatus struct {
	TerminalID         string `json:"terminal_id"`
	Connected          bool   `json:"connected"`
	ConnectorReachable bool   `json:"connector_reachable"`
	Simulated          bool   `json:"simulated"`
	Message            string `json:"message"`
	Capabilities       struct {
		Sale           bool `json:"sale"`
		Lookup         bool `json:"lookup"`
		Cancel         bool `json:"cancel"`
		Refund         bool `json:"refund"`
		Reconciliation bool `json:"reconciliation"`
	} `json:"capabilities"`
}

type Heartbeat struct {
	Version             string         `json:"version"`
	Terminals           []DeviceStatus `json:"terminals"`
	Active              bool           `json:"active"`
	Simulated           bool           `json:"simulated"`
	SupportsResolutions bool           `json:"supports_resolutions"`
	At                  time.Time      `json:"at"`
}

type ClaimRequest struct {
	TerminalIDs []string `json:"terminal_ids"`
	WaitSeconds int      `json:"wait_seconds"`
}

type Command struct {
	CommandID     string    `json:"command_id"`
	PaymentID     string    `json:"payment_id"`
	TerminalID    string    `json:"terminal_id"`
	AmountKopecks int64     `json:"amount_kopecks"`
	Currency      string    `json:"currency"`
	ExpiresAt     time.Time `json:"expires_at"`
}

type Outcome struct {
	State             string    `json:"state"`
	CommandID         string    `json:"command_id"`
	PaymentID         string    `json:"payment_id"`
	TerminalID        string    `json:"terminal_id"`
	AmountKopecks     int64     `json:"amount_kopecks"`
	Currency          string    `json:"currency"`
	TransactionID     string    `json:"transaction_id,omitempty"`
	RRN               string    `json:"rrn,omitempty"`
	STAN              string    `json:"stan,omitempty"`
	AuthorizationCode string    `json:"authorization_code,omitempty"`
	ResponseCode      string    `json:"response_code,omitempty"`
	ErrorCode         string    `json:"error_code,omitempty"`
	ErrorMessage      string    `json:"error_message,omitempty"`
	FinishedAt        time.Time `json:"finished_at"`
	Simulated         bool      `json:"simulated"`
}

type Event struct {
	EventID      string    `json:"event_id"`
	AgentID      string    `json:"agent_id"`
	CommandID    string    `json:"command_id"`
	PaymentID    string    `json:"payment_id"`
	TerminalID   string    `json:"terminal_id"`
	State        string    `json:"state"`
	Outcome      *Outcome  `json:"outcome,omitempty"`
	ResolutionID string    `json:"resolution_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type Delivery struct {
	Command    *Command    `json:"command,omitempty"`
	Resolution *Resolution `json:"resolution,omitempty"`
}

type ResolutionInput struct {
	Decision   string `json:"decision"`
	Reason     string `json:"reason"`
	Reference  string `json:"reference"`
	Evidence   string `json:"evidence"`
	OperatorID int64  `json:"-"`
}

type Resolution struct {
	ResolutionID  string    `json:"resolution_id"`
	CommandID     string    `json:"command_id"`
	PaymentID     string    `json:"payment_id"`
	TerminalID    string    `json:"terminal_id"`
	Decision      string    `json:"decision"`
	AmountKopecks int64     `json:"amount_kopecks"`
	Currency      string    `json:"currency"`
	Reason        string    `json:"reason"`
	Reference     string    `json:"reference"`
	ResolvedAt    time.Time `json:"resolved_at"`
}

type CommandStatus struct {
	Command
	OrderID           string          `json:"order_id"`
	CompanyID         int64           `json:"company_id"`
	KioskID           int64           `json:"kiosk_id"`
	PaymentProfileID  int64           `json:"payment_profile_id"`
	AgentID           string          `json:"agent_id"`
	DeviceID          int64           `json:"device_id"`
	BankTerminalID    string          `json:"bank_terminal_id"`
	PhysicalID        string          `json:"physical_id"`
	Environment       string          `json:"environment"`
	State             string          `json:"state"`
	LeaseUntil        *time.Time      `json:"lease_until,omitempty"`
	FirstClaimedAt    *time.Time      `json:"first_claimed_at,omitempty"`
	Outcome           json.RawMessage `json:"outcome,omitempty"`
	TransactionID     string          `json:"transaction_id,omitempty"`
	RRN               string          `json:"rrn,omitempty"`
	STAN              string          `json:"stan,omitempty"`
	AuthorizationCode string          `json:"authorization_code,omitempty"`
	ResponseCode      string          `json:"response_code,omitempty"`
	ErrorCode         string          `json:"error_code,omitempty"`
	ErrorMessage      string          `json:"error_message,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	ResolutionPending bool            `json:"resolution_pending"`
}

type TerminalDetails struct {
	CommandID         string `json:"command_id"`
	TerminalID        string `json:"terminal_id"`
	AgentID           string `json:"agent_id"`
	State             string `json:"state"`
	RRN               string `json:"rrn,omitempty"`
	TransactionID     string `json:"transaction_id,omitempty"`
	ErrorCode         string `json:"error_code,omitempty"`
	ErrorMessage      string `json:"error_message,omitempty"`
	ResolutionPending bool   `json:"resolution_pending"`
}
