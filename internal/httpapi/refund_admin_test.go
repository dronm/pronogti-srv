package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dronm/pronogti/internal/adminauth"
	"github.com/dronm/pronogti/internal/refund"
	websession "github.com/dronm/webapp/session"
)

const testRefundPaymentID = "4c81bddc-e877-4b27-aa45-41ed28c3e00c"
const testRefundID = "fe14038c-8850-41fb-bc85-420ed7d9e312"

type refundAdminFixture struct {
	quotedPayment  string
	createdPayment string
	createdBy      int64
	idempotencyKey string
	request        refund.CreateRequest
	created        int
	readPayment    string
	readRefund     string
}

func (f *refundAdminFixture) Quote(_ context.Context, paymentID string) (refund.Quote, error) {
	f.quotedPayment = paymentID
	return refund.Quote{}, nil
}

func (f *refundAdminFixture) Create(_ context.Context, paymentID string, operatorID int64, key string, req refund.CreateRequest) (refund.Refund, error) {
	f.createdPayment, f.createdBy, f.idempotencyKey = paymentID, operatorID, key
	f.request = req
	f.created++
	return refund.Refund{ID: testRefundID}, nil
}

func (f *refundAdminFixture) Get(_ context.Context, paymentID, refundID string) (refund.Refund, error) {
	f.readPayment, f.readRefund = paymentID, refundID
	return refund.Refund{ID: refundID}, nil
}

func TestRefundRoutesRequireAdminAndStrictSelections(t *testing.T) {
	const origin = "http://localhost:5174"
	const body = `{"items":[{"line_index":0,"quantity":"1.5"}]}`
	const path = "/admin/payments/" + testRefundPaymentID + "/refunds"
	fixture := &refundAdminFixture{}
	auth := &adminTestService{}
	sessions := NewAdminSessions(&adminTestManager{}, websession.Config{
		CookieName:  "admin_test",
		HTTPOnly:    true,
		SameSite:    http.SameSiteLaxMode,
		MaxLifeTime: 3600,
		MaxIdleTime: 600,
	}, "/", auth.Invalidate)
	handler := NewAdminHandler(auth, sessions, AdminHTTPOptions{AllowedOrigins: []string{origin}, Refunds: fixture})
	login := adminRequest(handler, http.MethodPost, "/admin/login", `{"name":"admin","pwd":"password"}`, nil, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login: %d %s", login.Code, login.Body)
	}
	cookie := login.Result().Cookies()[0]
	nonce := adminTestResponse().Auth.Token

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

	if got := adminRequest(handler, http.MethodGet, path, "", nil, ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous refund quote: %d %s", got.Code, got.Body)
	}
	if got := send(http.MethodGet, path, "", "", ""); got.Code != http.StatusOK || fixture.quotedPayment != testRefundPaymentID {
		t.Fatalf("refund quote: %d %s", got.Code, got.Body)
	}
	if got := send(http.MethodPost, path, body, "", "refund-attempt-1"); got.Code != http.StatusForbidden {
		t.Fatalf("refund without nonce: %d %s", got.Code, got.Body)
	}
	if got := send(http.MethodPost, path, body, nonce, ""); got.Code != http.StatusBadRequest {
		t.Fatalf("refund without idempotency key: %d %s", got.Code, got.Body)
	}
	if got := send(http.MethodPost, "/admin/payments/invalid/refunds", body, nonce, "refund-attempt-1"); got.Code != http.StatusBadRequest {
		t.Fatalf("refund with invalid ID: %d %s", got.Code, got.Body)
	}
	for _, invalid := range []string{
		`{"items":null}`,
		`{"items":[{"quantity":"1"}]}`,
		`{"items":[{"line_index":null,"quantity":"1"}]}`,
		`{"items":[{"line_index":0,"quantity":1}]}`,
		`{"items":[{"line_index":0,"quantity":"1","amount_kopecks":100}]}`,
	} {
		if got := send(http.MethodPost, path, invalid, nonce, "refund-attempt-1"); got.Code != http.StatusBadRequest {
			t.Fatalf("invalid refund body accepted: %d %s", got.Code, got.Body)
		}
	}
	if fixture.created != 0 {
		t.Fatal("invalid request reached refund service")
	}

	created := send(http.MethodPost, path, body, nonce, "refund-attempt-1")
	if created.Code != http.StatusAccepted || created.Header().Get("Location") != "refunds/"+testRefundID {
		t.Fatalf("create refund: %d %s", created.Code, created.Body)
	}
	if fixture.created != 1 || fixture.createdPayment != testRefundPaymentID || fixture.createdBy != 1 || fixture.idempotencyKey != "refund-attempt-1" {
		t.Fatalf("wrong operator or idempotency context: %+v", fixture)
	}
	if len(fixture.request.Items) != 1 || fixture.request.Items[0] != (refund.Selection{LineIndex: 0, Quantity: "1.5"}) {
		t.Fatalf("wrong refund selection: %+v", fixture.request)
	}
	read := send(http.MethodGet, path+"/"+testRefundID, "", "", "")
	if read.Code != http.StatusOK || fixture.readPayment != testRefundPaymentID || fixture.readRefund != testRefundID || fixture.created != 1 {
		t.Fatalf("read refund without mutating: %d %s", read.Code, read.Body)
	}
}
