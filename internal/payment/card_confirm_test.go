package payment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func cardConfirmationCandidate() ConfirmationCandidate {
	c := confirmationCandidate()
	c.Attempt.Method = "card"
	c.Attempt.QRCID = ""
	c.Attempt.OperationID = "card-bank-operation"
	c.Profile.CustomerCode = "300000092"
	c.Profile.AcquiringMerchantID = "200000000001056"
	c.Profile.CardsEnabled = true
	return c
}

func cardSuccessObservation(c ConfirmationCandidate) CardObservation {
	return CardObservation{OperationID: c.Attempt.OperationID, Status: "APPROVED", CustomerCode: c.Profile.CustomerCode,
		MerchantID: c.Profile.AcquiringMerchantID, AmountKopecks: c.Attempt.AmountKopecks, Paid: true, PaymentType: "card", TransactionID: "bank-transaction"}
}

func TestSDKCardCheckerUsesOriginalOperationAndOnlyApprovedCredits(t *testing.T) {
	for _, status := range []string{"CREATED", "AUTHORIZED", "WAIT_FULL_PAYMENT", "APPROVED", "EXPIRED", "ON-REFUND", "REFUNDED", "REFUNDED_PARTIALLY", "NEW_BANK_STATUS"} {
		t.Run(status, func(t *testing.T) {
			candidate := cardConfirmationCandidate()
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/acquiring/v1.0/payments/"+candidate.Attempt.OperationID || r.Header.Get("Authorization") != "Bearer phony.jwt.token" {
					t.Error("incorrect card status request")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"Data": map[string]any{"Operation": []map[string]any{{
					"operationId": candidate.Attempt.OperationID, "customerCode": candidate.Profile.CustomerCode, "merchantId": candidate.Profile.AcquiringMerchantID,
					"amount": "123.45", "status": status, "paymentType": "card", "paymentLink": "https://bank.example/pay", "transactionId": "bank-transaction",
				}}}})
			}))
			defer server.Close()
			candidate.Profile.APIBaseURL = server.URL
			got, err := (SDKCardChecker{}).Check(context.Background(), candidate, "phony.jwt.token")
			if err != nil || calls != 1 || got.Paid != (status == "APPROVED") || got.Status != status || got.AmountKopecks != 12345 || got.TransactionID != "bank-transaction" {
				t.Fatalf("calls=%d observation=%+v err=%v", calls, got, err)
			}
		})
	}
}

func TestSDKCardCheckerRejectsMismatchedIdentityAndInvalidHostedURL(t *testing.T) {
	for _, field := range []string{"customerCode", "merchantId", "amount", "operationId", "paymentLinkId", "paymentType", "paymentLink"} {
		t.Run(field, func(t *testing.T) {
			candidate := cardConfirmationCandidate()
			wire := map[string]any{"operationId": candidate.Attempt.OperationID, "customerCode": candidate.Profile.CustomerCode, "merchantId": candidate.Profile.AcquiringMerchantID, "amount": "123.45", "status": "APPROVED", "paymentType": "card", "paymentLink": "https://bank.example/pay"}
			switch field {
			case "amount":
				wire[field] = "123.46"
			case "paymentType":
				wire[field] = "sbp"
			case "paymentLink":
				wire[field] = "javascript:alert(1)"
			default:
				wire[field] = "other-identity"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"Data": map[string]any{"Operation": []map[string]any{wire}}})
			}))
			defer server.Close()
			candidate.Profile.APIBaseURL = server.URL
			if got, err := (SDKCardChecker{}).Check(context.Background(), candidate, "phony.jwt.token"); err == nil || got.Paid {
				t.Fatalf("mismatched bank response accepted: %+v %v", got, err)
			}
		})
	}
}

func TestSDKCardCheckerDoesNotFollowRedirectOrRecreateLostOperation(t *testing.T) {
	redirected := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected++ }))
	defer target.Close()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	candidate := cardConfirmationCandidate()
	candidate.Profile.APIBaseURL = server.URL
	if _, err := (SDKCardChecker{}).Check(context.Background(), candidate, "phony.jwt.token"); err == nil || calls != 1 || redirected != 0 {
		t.Fatal("bank redirect was followed or treated as credit")
	}
	candidate.Attempt.OperationID = ""
	if _, err := (SDKCardChecker{}).Check(context.Background(), candidate, "phony.jwt.token"); err == nil || calls != 1 {
		t.Fatal("missing operation triggered an unverified bank request")
	}
}

type cardConfirmationRepoFake struct {
	candidate         ConfirmationCandidate
	claimErr          error
	observations      []CardObservation
	releases          []string
	saveContextErrors []error
}

func (r *cardConfirmationRepoFake) ClaimCardConfirmation(context.Context) (ConfirmationCandidate, error) {
	return r.candidate, r.claimErr
}
func (r *cardConfirmationRepoFake) ObserveCard(ctx context.Context, _ ConfirmationCandidate, o CardObservation) error {
	r.observations = append(r.observations, o)
	r.saveContextErrors = append(r.saveContextErrors, ctx.Err())
	return nil
}
func (r *cardConfirmationRepoFake) ReleaseConfirmation(ctx context.Context, _ ConfirmationCandidate, code string) error {
	r.releases = append(r.releases, code)
	r.saveContextErrors = append(r.saveContextErrors, ctx.Err())
	return nil
}

type cardCheckerFake struct {
	calls       int
	observation CardObservation
	err         error
	onCheck     func(context.Context)
}

func (c *cardCheckerFake) Check(ctx context.Context, _ ConfirmationCandidate, _ string) (CardObservation, error) {
	c.calls++
	if c.onCheck != nil {
		c.onCheck(ctx)
	}
	return c.observation, c.err
}

func TestCardConfirmationWorkerDemoIsServerOnlyAndNeverAppliesToLiveBank(t *testing.T) {
	for _, environment := range []string{"mock", "sandbox", "production"} {
		t.Run(environment, func(t *testing.T) {
			candidate := cardConfirmationCandidate()
			candidate.Attempt.Environment = environment
			candidate.Profile.Environment = environment
			repo := &cardConfirmationRepoFake{candidate: candidate}
			checker := &cardCheckerFake{observation: cardSuccessObservation(candidate)}
			checker.observation.Paid = false
			checker.observation.Status = "AUTHORIZED"
			tokens := &confirmationTokensFake{}
			worked, err := NewCardConfirmationWorker(repo, checker, tokens, ConfirmationOptions{MockAutoPayAfter: time.Second}).ProcessOne(context.Background())
			if err != nil || !worked || len(repo.observations) != 1 {
				t.Fatalf("worked=%v err=%v", worked, err)
			}
			if environment == "mock" {
				if checker.calls != 0 || tokens.calls != 0 || !repo.observations[0].Paid || repo.observations[0].TransactionID != "mock-card-"+candidate.Attempt.ID {
					t.Fatal("mock confirmation did not stay local")
				}
			} else if checker.calls != 1 || tokens.calls != 1 || repo.observations[0].Paid {
				t.Fatal("demo delay credited a live card authorization")
			}
		})
	}
}

func TestCardConfirmationWorkerDisabledDemoAndExternalMockNeverAutoPay(t *testing.T) {
	for _, external := range []bool{false, true} {
		candidate := cardConfirmationCandidate()
		candidate.Profile.Environment = "mock"
		candidate.Attempt.Environment = "mock"
		repo := &cardConfirmationRepoFake{candidate: candidate}
		observation := cardSuccessObservation(candidate)
		observation.Paid = false
		observation.Status = "CREATED"
		checker := &cardCheckerFake{observation: observation}
		tokens := &confirmationTokensFake{}
		if _, err := NewCardConfirmationWorker(repo, checker, tokens, ConfirmationOptions{MockExternal: external}).ProcessOne(context.Background()); err != nil || len(repo.observations) != 1 || repo.observations[0].Paid {
			t.Fatalf("external=%v err=%v", external, err)
		}
		if external && (checker.calls != 1 || tokens.calls != 1) || !external && (checker.calls != 0 || tokens.calls != 0) {
			t.Fatal("wrong external mock path")
		}
	}
}

func TestCardConfirmationWorkerRejectsChangedIdentityAndIntermediatePaidFlags(t *testing.T) {
	for _, mutate := range []func(*ConfirmationCandidate, *CardObservation){
		func(c *ConfirmationCandidate, _ *CardObservation) { c.Attempt.Method = "sbp" },
		func(c *ConfirmationCandidate, _ *CardObservation) { c.Profile.CompanyID++ },
		func(c *ConfirmationCandidate, _ *CardObservation) { c.Attempt.OperationID = "" },
		func(_ *ConfirmationCandidate, o *CardObservation) { o.OperationID = "other-operation" },
		func(_ *ConfirmationCandidate, o *CardObservation) { o.CustomerCode = "other-customer" },
		func(_ *ConfirmationCandidate, o *CardObservation) { o.MerchantID = "other-merchant" },
		func(_ *ConfirmationCandidate, o *CardObservation) { o.AmountKopecks++ },
		func(_ *ConfirmationCandidate, o *CardObservation) { o.Status = "AUTHORIZED" },
	} {
		candidate := cardConfirmationCandidate()
		observation := cardSuccessObservation(candidate)
		mutate(&candidate, &observation)
		repo := &cardConfirmationRepoFake{candidate: candidate}
		checker := &cardCheckerFake{observation: observation}
		_, err := NewCardConfirmationWorker(repo, checker, &confirmationTokensFake{}, ConfirmationOptions{}).ProcessOne(context.Background())
		if err != nil || len(repo.observations) != 0 || len(repo.releases) != 1 {
			t.Fatalf("unsafe observation persisted: %+v %v", repo, err)
		}
	}
}

func TestCardConfirmationWorkerPersistsReleaseAfterCancellation(t *testing.T) {
	candidate := cardConfirmationCandidate()
	repo := &cardConfirmationRepoFake{candidate: candidate}
	ctx, cancel := context.WithCancel(context.Background())
	checker := &cardCheckerFake{err: context.Canceled, onCheck: func(context.Context) { cancel() }}
	worked, err := NewCardConfirmationWorker(repo, checker, &confirmationTokensFake{}, ConfirmationOptions{}).ProcessOne(ctx)
	if err != nil || !worked || len(repo.releases) != 1 || repo.releases[0] != "bank_confirmation_unavailable" || repo.saveContextErrors[0] != nil {
		t.Fatalf("worked=%v repo=%+v err=%v", worked, repo, err)
	}
}

func TestCardConfirmationWorkerMissingCredentialsNeverContactsBank(t *testing.T) {
	candidate := cardConfirmationCandidate()
	repo := &cardConfirmationRepoFake{candidate: candidate}
	checker := &cardCheckerFake{}
	_, err := NewCardConfirmationWorker(repo, checker, &confirmationTokensFake{err: errors.New("missing secret")}, ConfirmationOptions{}).ProcessOne(context.Background())
	if err != nil || checker.calls != 0 || len(repo.releases) != 1 || repo.releases[0] != "confirmation_credentials_unavailable" {
		t.Fatalf("repo=%+v err=%v", repo, err)
	}
}
