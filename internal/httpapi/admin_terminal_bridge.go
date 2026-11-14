package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/dronm/pronogti/internal/terminalbridge"
	"github.com/dronm/webapp"
)

type TerminalBridgeAdmin interface {
	RegisterAgent(context.Context, terminalbridge.AgentInput) (terminalbridge.AgentCredentials, error)
	RotateAgentToken(context.Context, string) (terminalbridge.AgentCredentials, error)
	SetAgentEnabled(context.Context, string, bool) error
	GetAgent(context.Context, string) (terminalbridge.Agent, error)
	RegisterTerminal(context.Context, terminalbridge.TerminalInput) (terminalbridge.Terminal, error)
	SetTerminalEnabled(context.Context, int64, bool) error
	GetTerminal(context.Context, int64) (terminalbridge.Terminal, error)
	ListAgents(context.Context) ([]terminalbridge.Agent, error)
	ListTerminals(context.Context) ([]terminalbridge.Terminal, error)
	ListCommands(context.Context) ([]terminalbridge.CommandStatus, error)
	GetCommand(context.Context, string) (terminalbridge.CommandStatus, error)
	CreateResolution(context.Context, string, terminalbridge.ResolutionInput) (terminalbridge.Resolution, error)
}

func (h *adminHandler) registerTerminalBridge(mux *http.ServeMux) {
	if h.options.TerminalBridge == nil {
		return
	}
	for _, route := range []string{
		"GET /admin/terminal-agents", "POST /admin/terminal-agents", "GET /admin/terminal-agents/{agent_id}", "PATCH /admin/terminal-agents/{agent_id}", "POST /admin/terminal-agents/{agent_id}/rotate-token",
		"GET /admin/payment-terminals", "POST /admin/payment-terminals", "GET /admin/payment-terminals/{terminal_id}", "PATCH /admin/payment-terminals/{terminal_id}",
		"GET /admin/terminal-commands", "GET /admin/terminal-commands/{command_id}", "POST /admin/terminal-commands/{command_id}/resolve",
	} {
		mux.HandleFunc(route, h.terminalAdministration)
	}
}

func (h *adminHandler) terminalAdministration(w http.ResponseWriter, r *http.Request) {
	operator, err := administratorIdentity(h.service, r, r.Method != http.MethodGet)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	store := h.options.TerminalBridge
	var value any
	status := http.StatusOK
	switch r.Pattern {
	case "GET /admin/terminal-agents":
		value, err = store.ListAgents(r.Context())
	case "POST /admin/terminal-agents":
		var input terminalbridge.AgentInput
		if err = decodeAdminJSON(w, r, &input); err == nil {
			value, err = store.RegisterAgent(r.Context(), input)
			status = http.StatusCreated
		}
	case "GET /admin/terminal-agents/{agent_id}":
		value, err = store.GetAgent(r.Context(), r.PathValue("agent_id"))
	case "POST /admin/terminal-agents/{agent_id}/rotate-token":
		if err = decodeAdminJSON(w, r, &struct{}{}); err == nil {
			value, err = store.RotateAgentToken(r.Context(), r.PathValue("agent_id"))
		}
	case "PATCH /admin/terminal-agents/{agent_id}":
		var input struct {
			Enabled *bool `json:"enabled"`
		}
		if err = decodeAdminJSON(w, r, &input); err == nil {
			if input.Enabled == nil {
				err = webapp.BadRequest("enabled is required", nil)
			} else {
				err = store.SetAgentEnabled(r.Context(), r.PathValue("agent_id"), *input.Enabled)
			}
		}
	case "GET /admin/payment-terminals":
		value, err = store.ListTerminals(r.Context())
	case "POST /admin/payment-terminals":
		var input terminalbridge.TerminalInput
		if err = decodeAdminJSON(w, r, &input); err == nil {
			value, err = store.RegisterTerminal(r.Context(), input)
			status = http.StatusCreated
		}
	case "GET /admin/payment-terminals/{terminal_id}":
		id, parseErr := strconv.ParseInt(r.PathValue("terminal_id"), 10, 64)
		if parseErr != nil || id <= 0 {
			err = webapp.BadRequest("invalid terminal id", nil)
		} else {
			value, err = store.GetTerminal(r.Context(), id)
		}
	case "PATCH /admin/payment-terminals/{terminal_id}":
		id, parseErr := strconv.ParseInt(r.PathValue("terminal_id"), 10, 64)
		if parseErr != nil || id <= 0 {
			err = webapp.BadRequest("invalid terminal id", nil)
			break
		}
		var input struct {
			Enabled *bool `json:"enabled"`
		}
		if err = decodeAdminJSON(w, r, &input); err == nil {
			if input.Enabled == nil {
				err = webapp.BadRequest("enabled is required", nil)
			} else {
				err = store.SetTerminalEnabled(r.Context(), id, *input.Enabled)
			}
		}
	case "GET /admin/terminal-commands":
		value, err = store.ListCommands(r.Context())
	case "GET /admin/terminal-commands/{command_id}":
		var id string
		id, err = refundPathID(r, "command_id")
		if err == nil {
			value, err = store.GetCommand(r.Context(), id)
		}
	case "POST /admin/terminal-commands/{command_id}/resolve":
		var id string
		id, err = refundPathID(r, "command_id")
		if err != nil {
			break
		}
		var input terminalbridge.ResolutionInput
		if err = decodeAdminJSONLimit(w, r, &input, 32<<10); err == nil {
			input.OperatorID = operator.ID
			value, err = store.CreateResolution(r.Context(), id, input)
			status = http.StatusAccepted
		}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	_ = webapp.WriteJSON(w, status, value)
}
