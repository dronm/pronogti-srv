package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/dronm/pronogti/internal/adminauth"
	"github.com/dronm/pronogti/internal/terminalbridge"
	"github.com/dronm/session"
	"github.com/dronm/webapp"
	websession "github.com/dronm/webapp/session"
)

const terminalAdminCommandID = "f07d820d-080f-4ae8-91ed-b896b28945b6"

type terminalAdminStub struct {
	TerminalBridgeAdmin
	rotatedAgent    string
	rotations       int
	resolvedCommand string
	resolution      terminalbridge.ResolutionInput
	resolutions     int
	changedAgent    string
	changedTerminal int64
	enabled         bool
}

func (s *terminalAdminStub) RotateAgentToken(_ context.Context, id string) (terminalbridge.AgentCredentials, error) {
	s.rotatedAgent = id
	s.rotations++
	return terminalbridge.AgentCredentials{Agent: terminalbridge.Agent{ID: id}, Token: "new-one-time-agent-token"}, nil
}

func (s *terminalAdminStub) ListAgents(context.Context) ([]terminalbridge.Agent, error) {
	return []terminalbridge.Agent{{ID: "agent-1"}}, nil
}

func (s *terminalAdminStub) GetAgent(_ context.Context, id string) (terminalbridge.Agent, error) {
	return terminalbridge.Agent{ID: id}, nil
}

func (s *terminalAdminStub) ListTerminals(context.Context) ([]terminalbridge.Terminal, error) {
	return []terminalbridge.Terminal{}, nil
}

func (s *terminalAdminStub) GetTerminal(_ context.Context, id int64) (terminalbridge.Terminal, error) {
	return terminalbridge.Terminal{ID: id}, nil
}

func (s *terminalAdminStub) ListCommands(context.Context) ([]terminalbridge.CommandStatus, error) {
	return []terminalbridge.CommandStatus{}, nil
}

func (s *terminalAdminStub) GetCommand(_ context.Context, id string) (terminalbridge.CommandStatus, error) {
	return terminalbridge.CommandStatus{Command: terminalbridge.Command{CommandID: id}}, nil
}

func (s *terminalAdminStub) SetAgentEnabled(_ context.Context, id string, enabled bool) error {
	s.changedAgent, s.enabled = id, enabled
	return nil
}

func (s *terminalAdminStub) SetTerminalEnabled(_ context.Context, id int64, enabled bool) error {
	s.changedTerminal, s.enabled = id, enabled
	return nil
}

func (s *terminalAdminStub) CreateResolution(_ context.Context, id string, input terminalbridge.ResolutionInput) (terminalbridge.Resolution, error) {
	// The repository owns evidence validation; the HTTP layer must preserve
	// its rejection without replacing missing values from query parameters.
	if input.Decision == "" || input.Reason == "" || input.Reference == "" || input.Evidence == "" {
		return terminalbridge.Resolution{}, webapp.BadRequest("operator evidence is required", nil)
	}
	s.resolvedCommand, s.resolution = id, input
	s.resolutions++
	return terminalbridge.Resolution{CommandID: id, Decision: input.Decision}, nil
}

type terminalAdminAuth struct {
	*adminTestService
	role string
}

func (a *terminalAdminAuth) Current(ctx context.Context, sess session.Session) (adminauth.Response, error) {
	response, err := a.adminTestService.Current(ctx, sess)
	if response.User != nil && a.role != "" {
		response.User.RoleID = a.role
	}
	return response, err
}

func newTerminalAdminHTTP(t *testing.T, store *terminalAdminStub) (http.Handler, *http.Cookie, string, *terminalAdminAuth) {
	t.Helper()
	auth := &terminalAdminAuth{adminTestService: &adminTestService{}}
	sessions := NewAdminSessions(&adminTestManager{}, websession.Config{CookieName: "admin_test", HTTPOnly: true, SameSite: http.SameSiteLaxMode, MaxLifeTime: 3600, MaxIdleTime: 600}, "/", auth.Invalidate)
	handler := NewAdminHandler(auth, sessions, AdminHTTPOptions{AllowedOrigins: []string{"http://localhost:5174"}, TerminalBridge: store})
	login := adminRequest(handler, http.MethodPost, "/admin/login", `{"name":"admin","pwd":"password"}`, nil, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login: %d %s", login.Code, login.Body)
	}
	return handler, login.Result().Cookies()[0], adminTestResponse().Auth.Token, auth
}

func TestTerminalAdministrationRequiresAdminSessionAndMutationNonce(t *testing.T) {
	store := &terminalAdminStub{}
	handler, cookie, nonce, auth := newTerminalAdminHTTP(t, store)
	for _, path := range []string{
		"/admin/terminal-agents", "/admin/terminal-agents/agent-1",
		"/admin/payment-terminals", "/admin/payment-terminals/1",
		"/admin/terminal-commands", "/admin/terminal-commands/" + terminalAdminCommandID,
	} {
		if got := adminRequest(handler, http.MethodGet, path+"?role=admin&operator_id=99", "", nil, ""); got.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous administrative read accepted: %s: %d %s", path, got.Code, got.Body)
		}
		got := adminRequest(handler, http.MethodGet, path, "", cookie, "")
		if got.Code != http.StatusOK || got.Header().Get("Cache-Control") != "no-store" || strings.Contains(got.Body.String(), "new-one-time-agent-token") {
			t.Fatalf("read without mutation nonce failed or leaked credentials: %s: %d %s", path, got.Code, got.Body)
		}
	}
	const rotate = "/admin/terminal-agents/agent-1/rotate-token"
	for _, supplied := range []string{"", "wrong"} {
		if got := adminRequest(handler, http.MethodPost, rotate, `{}`, cookie, supplied); got.Code != http.StatusForbidden {
			t.Fatalf("token rotation accepted without the current nonce: %d %s", got.Code, got.Body)
		}
	}
	if got := adminRequest(handler, http.MethodPost, rotate, `{}`, nil, nonce); got.Code != http.StatusUnauthorized || store.rotations != 0 {
		t.Fatalf("token rotation accepted without administrator session: %d %s", got.Code, got.Body)
	}
	got := adminRequest(handler, http.MethodPost, rotate+"?agent_id=other&operator_id=99", `{}`, cookie, nonce)
	if got.Code != http.StatusOK || store.rotations != 1 || store.rotatedAgent != "agent-1" || got.Header().Get("Cache-Control") != "no-store" || !strings.Contains(got.Body.String(), "new-one-time-agent-token") {
		t.Fatalf("token rotation did not bind to the authorized path: %d %s", got.Code, got.Body)
	}
	if got := adminRequest(handler, http.MethodPost, rotate, `{"token":"caller-selected-secret"}`, cookie, nonce); got.Code != http.StatusBadRequest || store.rotations != 1 {
		t.Fatal("caller was allowed to select or override the generated agent credential")
	}
	auth.role = "manager"
	if got := adminRequest(handler, http.MethodGet, "/admin/terminal-agents?role=admin", "", cookie, ""); got.Code != http.StatusUnauthorized {
		t.Fatal("a nonadministrator role gained terminal administration through query parameters")
	}
	if got := adminRequest(handler, http.MethodPost, rotate, `{}`, cookie, nonce); got.Code != http.StatusUnauthorized || store.rotations != 1 {
		t.Fatal("a nonadministrator role rotated an agent credential")
	}
}

func TestTerminalResolutionUsesSessionOperatorAndFrozenCommandIdentity(t *testing.T) {
	store := &terminalAdminStub{}
	handler, cookie, nonce, _ := newTerminalAdminHTTP(t, store)
	const path = "/admin/terminal-commands/" + terminalAdminCommandID + "/resolve"
	const body = `{"decision":"not_paid","reason":"Bank verified no charge","reference":"investigation-17","evidence":"Original terminal and bank records checked"}`
	if got := adminRequest(handler, http.MethodPost, path, body, nil, nonce); got.Code != http.StatusUnauthorized {
		t.Fatal("anonymous operator resolution accepted")
	}
	if got := adminRequest(handler, http.MethodPost, path, body, cookie, ""); got.Code != http.StatusForbidden {
		t.Fatal("operator resolution accepted without mutation nonce")
	}
	for _, invalid := range []string{
		`null`, `[]`, `{}`, `{"decision":"not_paid"}`, body + `{}`,
		strings.TrimSuffix(body, "}") + `,"operator_id":99}`,
		strings.TrimSuffix(body, "}") + `,"command_id":"another-command"}`,
		strings.TrimSuffix(body, "}") + `,"payment_id":"another-payment"}`,
		strings.TrimSuffix(body, "}") + `,"terminal_id":"another-terminal"}`,
		strings.TrimSuffix(body, "}") + `,"amount_kopecks":1}`,
		strings.TrimSuffix(body, "}") + `,"currency":"USD"}`,
	} {
		if got := adminRequest(handler, http.MethodPost, path, invalid, cookie, nonce); got.Code != http.StatusBadRequest {
			t.Fatalf("resolution accepted untrusted or incomplete identity/evidence: %d %s", got.Code, got.Body)
		}
	}
	for _, id := range []string{"invalid", "00000000-0000-0000-0000-000000000000", terminalAdminCommandID + "extra"} {
		if got := adminRequest(handler, http.MethodPost, "/admin/terminal-commands/"+id+"/resolve", body, cookie, nonce); got.Code != http.StatusBadRequest {
			t.Fatalf("malformed resolution command ID accepted: %s: %d", id, got.Code)
		}
	}
	if store.resolutions != 0 {
		t.Fatal("an invalid request caused an operator resolution")
	}
	got := adminRequest(handler, http.MethodPost, path+"?operator_id=999&command_id=untrusted&amount_kopecks=1", body, cookie, nonce)
	if got.Code != http.StatusAccepted || store.resolutions != 1 || store.resolvedCommand != terminalAdminCommandID || store.resolution.OperatorID != 1 || store.resolution.Decision != "not_paid" || store.resolution.Reference != "investigation-17" {
		t.Fatalf("resolution did not retain the path and session identity: %d %s: %+v", got.Code, got.Body, store.resolution)
	}
}

func TestTerminalAdministrativePATCHCannotReassignFrozenDevice(t *testing.T) {
	store := &terminalAdminStub{}
	handler, cookie, nonce, _ := newTerminalAdminHTTP(t, store)
	for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":false,"agent_id":"other"}`, `{"enabled":false,"company_id":99}`, `{"enabled":false,"bank_terminal_id":"other"}`, `{"enabled":false,"physical_id":"other"}`} {
		if got := adminRequest(handler, http.MethodPatch, "/admin/payment-terminals/17", body, cookie, nonce); got.Code != http.StatusBadRequest {
			t.Fatalf("frozen terminal binding was patchable: %d %s", got.Code, got.Body)
		}
	}
	for _, id := range []string{"not-an-integer", "0", "-1"} {
		if got := adminRequest(handler, http.MethodPatch, "/admin/payment-terminals/"+id, `{"enabled":false}`, cookie, nonce); got.Code != http.StatusBadRequest {
			t.Fatal("invalid terminal ID was accepted")
		}
	}
	if store.changedTerminal != 0 {
		t.Fatal("invalid patch changed a terminal")
	}
	if got := adminRequest(handler, http.MethodPatch, "/admin/payment-terminals/17?terminal_id=99", `{"enabled":false}`, cookie, nonce); got.Code != http.StatusOK || store.changedTerminal != 17 || store.enabled {
		t.Fatalf("terminal enablement used an untrusted identity: %d %s", got.Code, got.Body)
	}
	if got := adminRequest(handler, http.MethodPatch, "/admin/terminal-agents/agent-1?agent_id=other", `{"enabled":false}`, cookie, nonce); got.Code != http.StatusOK || store.changedAgent != "agent-1" || store.enabled {
		t.Fatalf("agent enablement used an untrusted identity: %d %s", got.Code, got.Body)
	}
}
