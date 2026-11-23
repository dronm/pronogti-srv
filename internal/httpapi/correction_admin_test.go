package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dronm/pronogti/internal/adminauth"
	"github.com/dronm/pronogti/internal/correction"
	websession "github.com/dronm/webapp/session"
)

type correctionAdminFixture struct {
	orderID, correctionID, key  string
	operatorID                  int64
	created, previewed, resumed int
	request                     correction.CreateRequest
}

func (f *correctionAdminFixture) Quote(_ context.Context, orderID string) (correction.Quote, error) {
	f.orderID = orderID
	return correction.Quote{OrderID: orderID}, nil
}

func (f *correctionAdminFixture) Preview(_ context.Context, orderID string, request correction.CreateRequest) (correction.Preview, error) {
	f.orderID, f.request = orderID, request
	f.previewed++
	return correction.Preview{}, nil
}

func (f *correctionAdminFixture) Create(_ context.Context, orderID string, operatorID int64, key string, request correction.CreateRequest) (correction.Correction, error) {
	f.orderID, f.operatorID, f.key, f.request = orderID, operatorID, key, request
	f.created++
	return correction.Correction{ID: testRefundID, OrderID: orderID}, nil
}

func (f *correctionAdminFixture) Get(_ context.Context, orderID, correctionID string) (correction.Correction, error) {
	f.orderID, f.correctionID = orderID, correctionID
	return correction.Correction{ID: correctionID, OrderID: orderID}, nil
}

func (f *correctionAdminFixture) List(context.Context) ([]correction.Correction, error) {
	return []correction.Correction{}, nil
}

func (f *correctionAdminFixture) Resume(_ context.Context, orderID, correctionID string, operatorID int64, key string) (correction.Correction, error) {
	f.orderID, f.correctionID, f.operatorID, f.key = orderID, correctionID, operatorID, key
	f.resumed++
	return correction.Correction{ID: correctionID}, nil
}

func TestCorrectionRoutesAuthenticatePreviewCreateAndResume(t *testing.T) {
	const origin = "http://localhost:5174"
	const path = "/admin/payment-orders/" + testRefundPaymentID + "/corrections"
	const body = `{"source_version":"sale","reason":"Исправление ставки НДС","basis":"voluntary","calculation_date":"2026-11-10","receipt":{"calculationSign":1,"totalKopecks":100}}`
	fixture := &correctionAdminFixture{}
	auth := &adminTestService{}
	sessions := NewAdminSessions(&adminTestManager{}, websession.Config{
		CookieName: "admin_test", HTTPOnly: true, SameSite: http.SameSiteLaxMode,
		MaxLifeTime: 3600, MaxIdleTime: 600,
	}, "/", auth.Invalidate)
	handler := NewAdminHandler(auth, sessions, AdminHTTPOptions{AllowedOrigins: []string{origin}, Corrections: fixture})
	login := adminRequest(handler, http.MethodPost, "/admin/login", `{"name":"admin","pwd":"password"}`, nil, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login: %d %s", login.Code, login.Body)
	}
	cookie, nonce := login.Result().Cookies()[0], adminTestResponse().Auth.Token
	send := func(method, target, body, nonce, key string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, target, strings.NewReader(body))
		request.Header.Set("Origin", origin)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(adminauth.SessionHeader, nonce)
		if key != "" {
			request.Header.Set(refundIdempotencyHeader, key)
		}
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if result := adminRequest(handler, http.MethodGet, path, "", nil, ""); result.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d %s", result.Code, result.Body)
	}
	for _, target := range []string{path, "/admin/fiscal-corrections", path + "/" + testRefundID} {
		if result := send(http.MethodGet, target, "", "", ""); result.Code != http.StatusOK {
			t.Fatalf("read %s: %d %s", target, result.Code, result.Body)
		}
	}
	for _, target := range []string{path, path + "/preview", path + "/" + testRefundID + "/resume"} {
		if result := send(http.MethodPost, target, body, "", "key-1"); result.Code != http.StatusForbidden {
			t.Fatalf("mutation without nonce: %d %s", result.Code, result.Body)
		}
	}
	for _, invalid := range []string{
		`{"receipt":{"totalKopecks":1.5}}`,
		`{"receipt":{"items":[{"quantity":1}]}}`,
		`{"driver_base_url":"http://example.org"}`,
		`{"operator_id":2}`,
	} {
		if result := send(http.MethodPost, path, invalid, nonce, "key-1"); result.Code != http.StatusBadRequest {
			t.Fatalf("invalid body reached service: %d %s", result.Code, result.Body)
		}
	}
	if fixture.created != 0 {
		t.Fatal("invalid request created a correction")
	}
	if result := send(http.MethodPost, path, body, nonce, ""); result.Code != http.StatusBadRequest {
		t.Fatalf("missing key: %d %s", result.Code, result.Body)
	}
	if result := send(http.MethodPost, "/admin/payment-orders/invalid/corrections", body, nonce, "key-1"); result.Code != http.StatusBadRequest {
		t.Fatalf("invalid order: %d %s", result.Code, result.Body)
	}
	if result := send(http.MethodPost, path+"/preview", body, nonce, ""); result.Code != http.StatusOK || fixture.previewed != 1 || fixture.created != 0 {
		t.Fatalf("preview changed state: %d %s", result.Code, result.Body)
	}
	created := send(http.MethodPost, path, body, nonce, "correction-key-1")
	if created.Code != http.StatusAccepted || created.Header().Get("Location") != "corrections/"+testRefundID || fixture.operatorID != 1 || fixture.key != "correction-key-1" || fixture.created != 1 {
		t.Fatalf("create: %d %s fixture=%+v", created.Code, created.Body, fixture)
	}
	if result := send(http.MethodPost, path+"/"+testRefundID+"/resume", `{}`, nonce, "resume-1"); result.Code != http.StatusAccepted || fixture.resumed != 1 || fixture.key != "resume-1" {
		t.Fatalf("resume: %d %s", result.Code, result.Body)
	}
}
