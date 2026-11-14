package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dronm/pronogti/internal/terminalbridge"
	"github.com/dronm/webapp"
)

type terminalAgentFixture struct {
	authCalls, claimCalls, eventCalls, heartbeatCalls int
	delivery                                          *terminalbridge.Delivery
	agentID, token                                    string
}

func (f *terminalAgentFixture) Authenticate(_ context.Context, id, token string) (terminalbridge.Agent, error) {
	f.authCalls++
	f.agentID = id
	f.token = token
	if id != "agent-1" || token != "test-agent-token" {
		return terminalbridge.Agent{}, webapp.Unauthorized("invalid agent credentials", nil)
	}
	return terminalbridge.Agent{ID: id, CompanyID: 1, KioskID: 2}, nil
}
func (f *terminalAgentFixture) Claim(_ context.Context, agent terminalbridge.Agent, request terminalbridge.ClaimRequest) (*terminalbridge.Delivery, error) {
	f.claimCalls++
	return f.delivery, nil
}
func (f *terminalAgentFixture) Heartbeat(_ context.Context, agent terminalbridge.Agent, request terminalbridge.Heartbeat) error {
	f.heartbeatCalls++
	return nil
}
func (f *terminalAgentFixture) Events(_ context.Context, agent terminalbridge.Agent, events []terminalbridge.Event) ([]string, error) {
	f.eventCalls++
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.EventID)
	}
	return ids, nil
}

func terminalRequest(handler http.Handler, path, body, token, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestTerminalAgentAuthenticationIsIndependentAndPrecedesBody(t *testing.T) {
	fixture := &terminalAgentFixture{}
	publicCalls := 0
	handler := WithTerminalBridge(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { publicCalls++; w.WriteHeader(299) }), NewTerminalBridgeHandler(fixture, time.Second))
	path := "/terminal-bridge/v1/agents/agent-1/commands/claim"
	for _, test := range []struct {
		token, origin, body string
		status              int
	}{
		{"", "", `{"malformed":`, 401},
		{"wrong-token", "", `{}`, 401},
		{"test-agent-token", "https://kiosk.example.test", `{}`, 403},
		{"test-agent-token", "", `{"terminal_ids":["terminal-1"],"wait_seconds":1}`, 204},
	} {
		response := terminalRequest(handler, path, test.body, test.token, test.origin)
		if response.Code != test.status {
			t.Fatalf("status=%d body=%s expected=%d", response.Code, response.Body, test.status)
		}
	}
	if publicCalls != 0 || fixture.claimCalls != 1 {
		t.Fatal("agent requests entered customer stack or invalid request executed", publicCalls, fixture.claimCalls)
	}
	if fixture.agentID != "agent-1" || fixture.token != "test-agent-token" {
		t.Fatal("incorrect identity passed to authenticator")
	}
	response := terminalRequest(handler, "/terminal-bridge/unknown", `{}`, "", "")
	if response.Code != 404 || publicCalls != 0 {
		t.Fatal("unknown agent namespace fell through to customer stack")
	}
}

func TestTerminalProtocolReturnsCommandsResolutionsAndExplicitAcks(t *testing.T) {
	fixture := &terminalAgentFixture{delivery: &terminalbridge.Delivery{Command: &terminalbridge.Command{CommandID: "command-1", PaymentID: "payment-1", TerminalID: "terminal-1", AmountKopecks: 12345, Currency: "RUB"}}}
	handler := NewTerminalBridgeHandler(fixture, time.Second)
	base := "/terminal-bridge/v1/agents/agent-1"
	response := terminalRequest(handler, base+"/commands/claim", `{"terminal_ids":["terminal-1"],"wait_seconds":1}`, "test-agent-token", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"amount_kopecks":12345`) {
		t.Fatal("command contract changed", response.Code, response.Body)
	}
	fixture.delivery = &terminalbridge.Delivery{Resolution: &terminalbridge.Resolution{ResolutionID: "resolution-1", Decision: "not_paid"}}
	response = terminalRequest(handler, base+"/commands/claim", `{"terminal_ids":["terminal-1"]}`, "test-agent-token", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"resolution_id":"resolution-1"`) {
		t.Fatal("resolution contract changed", response.Code, response.Body)
	}
	response = terminalRequest(handler, base+"/events", `{"events":[{"event_id":"event-1","agent_id":"agent-1","state":"queued"}]}`, "test-agent-token", "")
	var ack struct {
		IDs []string `json:"acknowledged_event_ids"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &ack) != nil || len(ack.IDs) != 1 || ack.IDs[0] != "event-1" {
		t.Fatal("event acknowledgement must explicitly list committed IDs", response.Code, response.Body)
	}
	response = terminalRequest(handler, base+"/heartbeat", `{"supports_resolutions":true,"terminals":[{"terminal_id":"terminal-1","connected":false,"connector_reachable":true,"simulated":false,"capabilities":{"sale":true}}]}`, "test-agent-token", "")
	if response.Code != 204 || fixture.heartbeatCalls != 1 {
		t.Fatal("heartbeat contract rejected", response.Code, response.Body)
	}
}

func TestTerminalAgentJSONIsBoundedAndStrict(t *testing.T) {
	fixture := &terminalAgentFixture{}
	handler := NewTerminalBridgeHandler(fixture, time.Second)
	path := "/terminal-bridge/v1/agents/agent-1/events"
	for _, body := range []string{`[]`, `null`, `{"events":[],"token":"extra"}`, `{"events":[]} {}`, strings.Repeat(" ", 1<<20) + `{}`} {
		response := terminalRequest(handler, path, body, "test-agent-token", "")
		if response.Code != 400 {
			t.Fatal("invalid agent JSON accepted", response.Code)
		}
	}
	if fixture.eventCalls != 0 {
		t.Fatal("invalid events reached store")
	}
}
