package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/dronm/pronogti/internal/terminalbridge"
	"github.com/dronm/webapp"
)

type TerminalBridgeAgents interface {
	Authenticate(context.Context, string, string) (terminalbridge.Agent, error)
	Heartbeat(context.Context, terminalbridge.Agent, terminalbridge.Heartbeat) error
	Claim(context.Context, terminalbridge.Agent, terminalbridge.ClaimRequest) (*terminalbridge.Delivery, error)
	Events(context.Context, terminalbridge.Agent, []terminalbridge.Event) ([]string, error)
}

var terminalAgentID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// WithTerminalBridge keeps the independently authenticated device protocol out
// of customer Redis sessions, cookies, and salon selection middleware.
func WithTerminalBridge(public, bridge http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/terminal-bridge" || strings.HasPrefix(r.URL.Path, "/terminal-bridge/") {
			if bridge == nil {
				http.NotFound(w, r)
				return
			}
			bridge.ServeHTTP(w, r)
			return
		}
		public.ServeHTTP(w, r)
	})
}

func NewTerminalBridgeHandler(store TerminalBridgeAgents, maxPoll time.Duration) http.Handler {
	if maxPoll <= 0 {
		maxPoll = 25 * time.Second
	}
	mux := http.NewServeMux()
	serve := func(action string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if r.Header.Get("Origin") != "" {
				webapp.WriteServiceError(w, webapp.Forbidden("terminal agents do not accept browser-origin requests", nil))
				return
			}
			if store == nil {
				webapp.WriteServiceError(w, webapp.NewHTTPError(http.StatusServiceUnavailable, "terminal_bridge_unavailable", "terminal bridge is unavailable", nil))
				return
			}
			id := r.PathValue("agent_id")
			values := r.Header.Values("Authorization")
			if !terminalAgentID.MatchString(id) || len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") || strings.TrimSpace(strings.TrimPrefix(values[0], "Bearer ")) == "" {
				webapp.WriteServiceError(w, webapp.Unauthorized("terminal agent authentication required", nil))
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), maxPoll+5*time.Second)
			defer cancel()
			agent, err := store.Authenticate(ctx, id, strings.TrimPrefix(values[0], "Bearer "))
			if err != nil {
				webapp.WriteServiceError(w, err)
				return
			}
			switch action {
			case "claim":
				var request terminalbridge.ClaimRequest
				if err := decodeTerminalJSON(w, r, &request); err != nil {
					webapp.WriteServiceError(w, err)
					return
				}
				delivery, err := store.Claim(ctx, agent, request)
				if err != nil {
					webapp.WriteServiceError(w, err)
					return
				}
				if delivery == nil {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				_ = webapp.WriteJSON(w, http.StatusOK, delivery)
			case "events":
				var request struct {
					Events []terminalbridge.Event `json:"events"`
				}
				if err := decodeTerminalJSON(w, r, &request); err != nil {
					webapp.WriteServiceError(w, err)
					return
				}
				ack, err := store.Events(ctx, agent, request.Events)
				if err != nil {
					webapp.WriteServiceError(w, err)
					return
				}
				_ = webapp.WriteJSON(w, http.StatusOK, map[string]any{"acknowledged_event_ids": ack})
			case "heartbeat":
				var request terminalbridge.Heartbeat
				if err := decodeTerminalJSON(w, r, &request); err != nil {
					webapp.WriteServiceError(w, err)
					return
				}
				if err := store.Heartbeat(ctx, agent, request); err != nil {
					webapp.WriteServiceError(w, err)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}
		}
	}
	mux.HandleFunc("POST /terminal-bridge/v1/agents/{agent_id}/commands/claim", serve("claim"))
	mux.HandleFunc("POST /terminal-bridge/v1/agents/{agent_id}/events", serve("events"))
	mux.HandleFunc("POST /terminal-bridge/v1/agents/{agent_id}/heartbeat", serve("heartbeat"))
	return webapp.Chain(webapp.RecoverMiddleware(), webapp.QueryIDMiddleware(""), webapp.RequestLoggerMiddleware())(mux)
}

func decodeTerminalJSON(w http.ResponseWriter, r *http.Request, target any) error {
	typeName, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typeName != "application/json" || r.Body == nil {
		return webapp.BadRequest("terminal requests require an application/json object", nil)
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		return webapp.BadRequest("terminal JSON request exceeds the size limit or cannot be read", nil)
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return webapp.BadRequest("terminal request must be a JSON object", nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return webapp.BadRequest("invalid terminal JSON request", nil)
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return webapp.BadRequest("terminal request must contain one JSON object", nil)
	}
	return nil
}
