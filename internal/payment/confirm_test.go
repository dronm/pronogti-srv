package payment

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dronm/paytochkabank"
)

func confirmationCandidate() ConfirmationCandidate {
	now := time.Now().UTC()
	return ConfirmationCandidate{
		Attempt:   Attempt{ID: "d012765b-e754-43bd-a625-8b1da622ecee", OrderID: "bd59edf4-fd07-4263-9764-23f502d0fedd", CompanyID: 781898, PaymentProfileID: 4, Environment: "sandbox", QRCID: "AS0000000001", State: "pending", AmountKopecks: 12345, Currency: "RUB", CreatedAt: now.Add(-time.Minute)},
		Profile:   Profile{ID: 4, CompanyID: 781898, Environment: "sandbox", CustomerCode: "customer-a", SBPMerchantID: "merchant-a", AccountID: "account-a/bic-a", HTTPTimeoutSeconds: 5},
		ClaimedAt: now,
	}
}

func TestSDKCheckerOnlyAcceptedMatchingQRConfirms(t *testing.T) {
	for _, tc := range []struct {
		name, status, qrc, ref string
		paid                   bool
	}{
		{"accepted", "Accepted", "AS0000000001", "bank-reference", true},
		{"confirmed is intermediate", "Confirmed", "AS0000000001", "bank-reference", false},
		{"initiated", "Initiated", "AS0000000001", "bank-reference", false},
		{"rejected attempt does not expire QR", "Rejected", "AS0000000001", "bank-reference", false},
		{"unknown status", "Paid", "AS0000000001", "bank-reference", false},
		{"other QR", "Accepted", "OTHER-QR", "bank-reference", false},
		{"missing bank reference", "Accepted", "AS0000000001", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/sbp/v1.0/get-sbp-payments" {
					t.Errorf("unexpected bank request %s %s", r.Method, r.URL.Path)
				}
				if r.URL.Query().Get("customerCode") != "customer-a" || r.URL.Query().Get("qrcId") != "AS0000000001" || r.URL.Query().Get("fromDate") == "" {
					t.Errorf("identity/date filters missing: %s", r.URL.RawQuery)
				}
				if r.Header.Get("Authorization") != "Bearer phony.jwt.token" {
					t.Error("bank auth missing")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"Data": map[string]any{"Payments": []map[string]any{{"qrcId": tc.qrc, "status": tc.status, "refTransactionId": tc.ref}}}, "Meta": map[string]any{"totalPages": 1}})
			}))
			defer server.Close()
			candidate := confirmationCandidate()
			candidate.Profile.APIBaseURL = server.URL
			observation, err := (SDKChecker{}).Check(context.Background(), candidate, "phony.jwt.token")
			if err != nil || observation.Paid != tc.paid || calls != 1 {
				t.Fatalf("observation=%+v calls=%d err=%v", observation, calls, err)
			}
			if tc.paid && (observation.QRCID != candidate.Attempt.QRCID || observation.RefTransactionID != tc.ref) {
				t.Fatal("successful observation lost payment identity")
			}
		})
	}
}

func TestSDKCheckerPaginatesAndRejectsConflictingAcceptedReferences(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(strconv.FormatBool(conflict), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				page := r.URL.Query().Get("page")
				status := "InProgress"
				ref := "bank-b"
				if page == "2" || conflict {
					status = "Accepted"
				}
				if conflict && page == "1" {
					ref = "bank-a"
				}
				_, _ = io.WriteString(w, `{"Data":{"Payments":[{"qrcId":"AS0000000001","status":"`+status+`","refTransactionId":"`+ref+`"}]},"Meta":{"totalPages":2}}`)
			}))
			defer server.Close()
			candidate := confirmationCandidate()
			candidate.Profile.APIBaseURL = server.URL
			observation, err := (SDKChecker{}).Check(context.Background(), candidate, "phony.jwt.token")
			if calls != 2 || conflict && err == nil || !conflict && (err != nil || !observation.Paid) {
				t.Fatalf("calls=%d observation=%+v err=%v", calls, observation, err)
			}
		})
	}
}

type confirmationRepoFake struct {
	candidates        []ConfirmationCandidate
	claimErr          error
	observations      []BankObservation
	releases          []string
	saveContextErrors []error
}

func (r *confirmationRepoFake) ClaimConfirmation(context.Context) (ConfirmationCandidate, error) {
	if r.claimErr != nil {
		return ConfirmationCandidate{}, r.claimErr
	}
	if len(r.candidates) == 0 {
		return ConfirmationCandidate{}, ErrNoConfirmation
	}
	c := r.candidates[0]
	r.candidates = r.candidates[1:]
	return c, nil
}
func (r *confirmationRepoFake) Observe(ctx context.Context, _ ConfirmationCandidate, o BankObservation) error {
	r.observations = append(r.observations, o)
	r.saveContextErrors = append(r.saveContextErrors, ctx.Err())
	return nil
}
func (r *confirmationRepoFake) ReleaseConfirmation(ctx context.Context, _ ConfirmationCandidate, code string) error {
	r.releases = append(r.releases, code)
	r.saveContextErrors = append(r.saveContextErrors, ctx.Err())
	return nil
}
func (*confirmationRepoFake) Receive(context.Context, int64, VerifiedSBPNotification) error {
	return nil
}

type confirmationCheckerFake struct {
	calls   int
	result  BankObservation
	err     error
	onCheck func(context.Context)
}

func (c *confirmationCheckerFake) Check(ctx context.Context, _ ConfirmationCandidate, _ string) (BankObservation, error) {
	c.calls++
	if c.onCheck != nil {
		c.onCheck(ctx)
	}
	return c.result, c.err
}

type confirmationTokensFake struct {
	calls int
	err   error
}

func (t *confirmationTokensFake) Token(context.Context, Profile) (string, error) {
	t.calls++
	return "phony.jwt.token", t.err
}

func TestConfirmationWorkerDemoCompletionIsServerOnlyAndEnvironmentBound(t *testing.T) {
	for _, environment := range []string{"mock", "sandbox", "production"} {
		t.Run(environment, func(t *testing.T) {
			candidate := confirmationCandidate()
			candidate.Attempt.Environment = environment
			candidate.Profile.Environment = environment
			repo := &confirmationRepoFake{candidates: []ConfirmationCandidate{candidate}}
			checker := &confirmationCheckerFake{result: BankObservation{Status: "InProgress"}}
			tokens := &confirmationTokensFake{}
			worker := NewConfirmationWorker(repo, checker, tokens, ConfirmationOptions{MockAutoPayAfter: time.Second})
			worked, err := worker.ProcessOne(context.Background())
			if err != nil || !worked || len(repo.observations) != 1 {
				t.Fatalf("worked=%v observations=%+v err=%v", worked, repo.observations, err)
			}
			if environment == "mock" {
				if checker.calls != 0 || tokens.calls != 0 || !repo.observations[0].Paid || repo.observations[0].RefTransactionID != "mock-"+candidate.Attempt.ID {
					t.Fatal("mock did not confirm independently without bank/token access")
				}
			} else if checker.calls != 1 || tokens.calls != 1 || repo.observations[0].Paid {
				t.Fatal("demo delay confirmed a non-mock payment")
			}
		})
	}
}

func TestConfirmationWorkerDisabledDemoAndExternalMockDoNotAutoPay(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(strconv.FormatBool(external), func(t *testing.T) {
			candidate := confirmationCandidate()
			candidate.Attempt.Environment = "mock"
			candidate.Profile.Environment = "mock"
			repo := &confirmationRepoFake{candidates: []ConfirmationCandidate{candidate}}
			checker := &confirmationCheckerFake{result: BankObservation{Status: "InProgress"}}
			tokens := &confirmationTokensFake{}
			worker := NewConfirmationWorker(repo, checker, tokens, ConfirmationOptions{MockExternal: external})
			_, err := worker.ProcessOne(context.Background())
			if err != nil || len(repo.observations) != 1 || repo.observations[0].Paid {
				t.Fatalf("observation=%+v err=%v", repo.observations, err)
			}
			if external && (checker.calls != 1 || tokens.calls != 1) || !external && (checker.calls != 0 || tokens.calls != 0) {
				t.Fatal("wrong mock confirmation path")
			}
		})
	}
}

func TestConfirmationWorkerRejectsCrossIdentityAndMissingReferences(t *testing.T) {
	for _, mutate := range []func(*ConfirmationCandidate, *BankObservation){
		func(c *ConfirmationCandidate, _ *BankObservation) { c.Profile.CompanyID++ },
		func(c *ConfirmationCandidate, _ *BankObservation) { c.Attempt.QRCID = "" },
		func(_ *ConfirmationCandidate, o *BankObservation) { o.QRCID = "other-qr" },
		func(_ *ConfirmationCandidate, o *BankObservation) { o.RefTransactionID = "" },
		func(_ *ConfirmationCandidate, o *BankObservation) { o.Status = "Confirmed" },
	} {
		candidate := confirmationCandidate()
		observation := BankObservation{QRCID: candidate.Attempt.QRCID, Status: "Accepted", Paid: true, RefTransactionID: "bank-reference"}
		mutate(&candidate, &observation)
		repo := &confirmationRepoFake{candidates: []ConfirmationCandidate{candidate}}
		checker := &confirmationCheckerFake{result: observation}
		_, err := NewConfirmationWorker(repo, checker, &confirmationTokensFake{}, ConfirmationOptions{}).ProcessOne(context.Background())
		if err != nil || len(repo.observations) != 0 || len(repo.releases) != 1 {
			t.Fatalf("unsafe confirmation persisted: %+v %+v %v", repo.observations, repo.releases, err)
		}
	}
}

func TestConfirmationWorkerCancellationPreservesRetryAndDoesNotRequireBrowser(t *testing.T) {
	candidate := confirmationCandidate()
	repo := &confirmationRepoFake{candidates: []ConfirmationCandidate{candidate}}
	ctx, cancel := context.WithCancel(context.Background())
	checker := &confirmationCheckerFake{err: context.Canceled, onCheck: func(context.Context) { cancel() }}
	worked, err := NewConfirmationWorker(repo, checker, &confirmationTokensFake{}, ConfirmationOptions{}).ProcessOne(ctx)
	if err != nil || !worked || len(repo.releases) != 1 || repo.releases[0] != "bank_confirmation_unavailable" || repo.saveContextErrors[0] != nil {
		t.Fatalf("worked=%v releases=%+v saveContexts=%+v err=%v", worked, repo.releases, repo.saveContextErrors, err)
	}
}

func signConfirmationWebhook(t *testing.T, key *rsa.PrivateKey, payload string) []byte {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	body := header + "." + base64.RawURLEncoding.EncodeToString([]byte(payload))
	digest := sha256.Sum256([]byte(body))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return []byte(body + "." + base64.RawURLEncoding.EncodeToString(signature))
}

func TestWebhookAuthenticatesBeforeDurableReceiptAndNormalizesMoney(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := paytochkabank.NewWebhookVerifier(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	var saved VerifiedSBPNotification
	handler := NewWebhookHandler(verifier, func(_ context.Context, profileID int64, n VerifiedSBPNotification) error {
		calls.Add(1)
		saved = n
		if profileID != 4 {
			t.Errorf("profile=%d", profileID)
		}
		return nil
	})
	payload := `{"webhookType":"incomingSbpPayment","customerCode":"customer-a","merchantId":"merchant-a","qrcId":"AS0000000001","operationId":"bank-op","refTransactionId":"bank-reference","paymentType":"sbpPayment","amount":123.45}`
	for _, badSignature := range []bool{false, true} {
		raw := signConfirmationWebhook(t, key, payload)
		if badSignature {
			parts := strings.Split(string(raw), ".")
			signature, decodeErr := base64.RawURLEncoding.DecodeString(parts[2])
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			signature[0] ^= 1
			parts[2] = base64.RawURLEncoding.EncodeToString(signature)
			raw = []byte(strings.Join(parts, "."))
		}
		request := httptest.NewRequest(http.MethodPost, "/payments/tochka/webhook/4", strings.NewReader(string(raw)))
		request.Header.Set("Content-Type", "text/plain; charset=utf-8")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if badSignature && response.Code != 401 || !badSignature && response.Code != 200 {
			t.Fatalf("bad=%v code=%d", badSignature, response.Code)
		}
	}
	if calls.Load() != 1 || saved.AmountKopecks != 12345 || saved.TransactionID != "bank-op" || saved.EventKey == "" {
		t.Fatalf("calls=%d notification=%+v", calls.Load(), saved)
	}
}

func TestWebhookUnmatchedSignedTestsAreAcknowledgedAndDBFailureRetries(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	verifier, _ := paytochkabank.NewWebhookVerifier(&key.PublicKey)
	for _, failure := range []bool{false, true} {
		calls := 0
		handler := NewWebhookHandler(verifier, func(context.Context, int64, VerifiedSBPNotification) error {
			calls++
			if failure {
				return errors.New("database password secret")
			}
			return nil
		})
		raw := signConfirmationWebhook(t, key, `{"webhookType":"incomingSbpPayment","customerCode":"test-customer","merchantId":"test-merchant","qrcId":"test-unmatched-qr","amount":1}`)
		r := httptest.NewRequest(http.MethodPost, "/payments/tochka/webhook/4", strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "text/plain")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if calls != 1 || failure && w.Code != 503 || !failure && w.Code != 200 || strings.Contains(w.Body.String(), "password") {
			t.Fatalf("failure=%v calls=%d code=%d body=%s", failure, calls, w.Code, w.Body.String())
		}
	}
}

func TestWebhookSharedCallbackRoutesOnlyAfterSignatureVerification(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	verifier, _ := paytochkabank.NewWebhookVerifier(&key.PublicKey)
	var profile int64 = -1
	handler := NewWebhookHandler(verifier, func(_ context.Context, id int64, n VerifiedSBPNotification) error {
		profile = id
		if n.CustomerCode != "customer-a" || n.QRCID != "qrc-a" {
			t.Fatal("verified identity was lost")
		}
		return nil
	})
	raw := signConfirmationWebhook(t, key, `{"webhookType":"incomingSbpPayment","customerCode":"customer-a","merchantId":"merchant-a","qrcId":"qrc-a","amount":1}`)
	r := httptest.NewRequest(http.MethodPost, "/payments/tochka/webhook", strings.NewReader(string(raw)))
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 || profile != 0 {
		t.Fatalf("generic callback code=%d profile=%d", w.Code, profile)
	}
}

func TestWebhookIgnoresOtherVerifiedEventsAndRejectsUntrustedInput(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	verifier, _ := paytochkabank.NewWebhookVerifier(&key.PublicKey)
	handler := NewWebhookHandler(verifier, func(context.Context, int64, VerifiedSBPNotification) error {
		t.Fatal("unrelated/invalid event reached durable receiver")
		return nil
	})
	for _, tc := range []struct {
		method, contentType, body string
		code                      int
	}{
		{"POST", "text/plain", string(signConfirmationWebhook(t, key, `{"webhookType":"incomingPayment"}`)), 200},
		{"POST", "application/json", `{}`, 415},
		{"POST", "text/plain", "not-a-jwt", 401},
		{"POST", "text/plain", strings.Repeat("x", (1<<20)+1), 413},
		{"GET", "text/plain", "", 405},
	} {
		r := httptest.NewRequest(tc.method, "/payments/tochka/webhook/4", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", tc.contentType)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("method=%s type=%s got=%d want=%d", tc.method, tc.contentType, w.Code, tc.code)
		}
	}
}
