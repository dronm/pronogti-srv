package fiscal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestReceiptBuyerContactIsExplicitAndPreservesPaymentTotal(t *testing.T) {
	order := receiptOrder()
	payload, err := BuildPayloadWithContact(order, DefaultProfile(order.Owner.CompanyID), "+79990000001")
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Total      int64 `json:"totalKopecks"`
		Additional struct {
			Contact string `json:"customerContact"`
		} `json:"additionalData"`
	}
	if err = json.Unmarshal(payload, &receipt); err != nil || receipt.Additional.Contact != "+79990000001" || receipt.Total != order.PayableKopecks {
		t.Fatalf("contact/amount payload=%s error=%v", payload, err)
	}
	other, err := BuildPayloadWithContact(order, DefaultProfile(order.Owner.CompanyID), "+79990000002")
	if err != nil || bytes.Contains(other, []byte("+79990000001")) {
		t.Fatal("another receipt retained the previous buyer contact")
	}
	for _, contact := range []string{strings.Repeat("x", 65), "buyer💅@example.test"} {
		if _, err = BuildPayloadWithContact(order, DefaultProfile(order.Owner.CompanyID), contact); err == nil {
			t.Fatalf("unsupported customer contact accepted: %q", contact)
		}
	}
}

func TestFiscalRecoveryBeforeDispatchResumesSameFrozenPayload(t *testing.T) {
	for _, saved := range []bool{false, true} {
		t.Run(map[bool]string{false: "before payload journal", true: "after payload journal"}[saved], func(t *testing.T) {
			job := liveJob()
			job.PreviousState = "processing"
			job.CustomerContact = "+79990000001"
			original, err := BuildPayloadWithContact(job.Order, job.Profile, job.CustomerContact)
			if err != nil {
				t.Fatal(err)
			}
			if saved {
				job.Payload = original
			}
			repository := &workerRepository{jobs: []Job{job}}
			driver := &workerDriver{result: DriverResult{State: "completed", Response: json.RawMessage(`{"status":"completed"}`)}}
			driver.onCall = func(context.Context) {
				if len(repository.dispatches) != 1 {
					t.Fatal("resumed receipt was not fenced before dispatch")
				}
			}
			if _, err := NewWorker(repository, driver, Options{AllowLive: true}).ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			onlyFinish(t, repository, "issued", "")
			if driver.creates != 1 || driver.lookups != 0 || driver.id != job.RequestID || !bytes.Equal(driver.payload, original) {
				t.Fatal("undispatched recovery changed payload/request identity or used lookup")
			}
			wantWrites := 1
			if saved {
				wantWrites = 0
			}
			if len(repository.saved) != wantWrites {
				t.Fatal("resumed receipt replaced the frozen payload")
			}
		})
	}
}

func TestFiscalDispatchFencingFailureCannotReachDriver(t *testing.T) {
	job := liveJob()
	claimLost := errors.New("claim no longer owned")
	repository := &workerRepository{jobs: []Job{job}, dispatchErr: claimLost}
	driver := &workerDriver{}
	worked, err := NewWorker(repository, driver, Options{AllowLive: true}).ProcessOne(context.Background())
	if !worked || !errors.Is(err, claimLost) || driver.creates+driver.lookups != 0 || len(repository.finished) != 0 {
		t.Fatalf("fencing failure wrote receipt: worked=%v error=%v", worked, err)
	}
}

func TestFiscalPendingRowWithDispatchMarkerIsLookupOnly(t *testing.T) {
	job := liveJob()
	started := job.Order.CreatedAt
	job.DispatchStartedAt = &started
	job.Payload = json.RawMessage(`{"frozen":true}`)
	repository := &workerRepository{jobs: []Job{job}}
	driver := &workerDriver{result: DriverResult{State: "not_found", ErrorCode: "not_found"}}
	if _, err := NewWorker(repository, driver, Options{AllowLive: true}).ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	onlyFinish(t, repository, "unknown", "not_found")
	if driver.creates != 0 || driver.lookups != 1 || len(repository.dispatches) != 0 {
		t.Fatal("a manually reset state bypassed dispatch uncertainty")
	}
}

func TestFiscalInvalidTokenFailsBeforeDispatchAndCanBeRepaired(t *testing.T) {
	job := liveJob()
	job.Profile.BearerTokenEnv = "PRONOGTI_FISCAL_INVALID_HEADER_TOKEN"
	t.Setenv(job.Profile.BearerTokenEnv, "bad\r\ntoken")
	repository := &workerRepository{jobs: []Job{job}}
	driver := &workerDriver{}
	if _, err := NewWorker(repository, driver, Options{AllowLive: true}).ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	onlyFinish(t, repository, "failed", "receipt_configuration_invalid")
	if len(repository.dispatches) != 0 || driver.creates+driver.lookups != 0 {
		t.Fatal("invalid token reached driver or marked dispatch")
	}
}

func TestFiscalWorkerRejectsMissingDependenciesOrContext(t *testing.T) {
	if worked, err := NewWorker(nil, nil, Options{}).ProcessOne(context.Background()); err == nil || worked {
		t.Fatal("missing repository did not fail")
	}
	if worked, err := NewWorker(&workerRepository{}, &workerDriver{}, Options{}).ProcessOne(nil); err == nil || worked {
		t.Fatal("nil context did not fail")
	}
	if err := NewWorker(&workerRepository{}, &workerDriver{}, Options{}).Run(nil); err == nil {
		t.Fatal("nil background context did not fail")
	}
}

func TestFiscalDriverCancelledBeforeDispatchIsDefiniteFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, input := range []context.Context{ctx, nil} {
		result, err := (HTTPDriver{}).Create(input, DriverConfig{}, "original-id", json.RawMessage(`{}`))
		if err == nil || result.State != "failed" {
			t.Fatalf("undispatched context failure became uncertain: result=%+v error=%v", result, err)
		}
	}
}
