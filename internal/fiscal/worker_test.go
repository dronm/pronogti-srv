package fiscal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type workerRepository struct {
	jobs          []Job
	claimErr      error
	saveErr       error
	dispatchErr   error
	finishErr     error
	saved         []json.RawMessage
	dispatches    []Job
	finished      []workerFinish
	finishCtxErrs []error
}

type workerFinish struct {
	job      Job
	state    string
	response json.RawMessage
	code     string
}

func (r *workerRepository) Claim(context.Context) (Job, error) {
	if r.claimErr != nil {
		return Job{}, r.claimErr
	}
	if len(r.jobs) == 0 {
		return Job{}, ErrNoJob
	}
	job := r.jobs[0]
	r.jobs = r.jobs[1:]
	return job, nil
}

func (r *workerRepository) SavePayload(_ context.Context, _ Job, payload json.RawMessage) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.saved = append(r.saved, append(json.RawMessage(nil), payload...))
	return nil
}

func (r *workerRepository) BeginDispatch(_ context.Context, job Job) error {
	if r.dispatchErr != nil {
		return r.dispatchErr
	}
	r.dispatches = append(r.dispatches, job)
	return nil
}

func (r *workerRepository) Finish(ctx context.Context, job Job, state string, response json.RawMessage, code string) error {
	r.finishCtxErrs = append(r.finishCtxErrs, ctx.Err())
	r.finished = append(r.finished, workerFinish{job: job, state: state, response: append(json.RawMessage(nil), response...), code: code})
	return r.finishErr
}

type workerDriver struct {
	creates int
	lookups int
	config  DriverConfig
	id      string
	payload json.RawMessage
	result  DriverResult
	err     error
	onCall  func(context.Context)
}

func (d *workerDriver) Create(ctx context.Context, config DriverConfig, id string, payload json.RawMessage) (DriverResult, error) {
	d.creates++
	d.config, d.id, d.payload = config, id, append(json.RawMessage(nil), payload...)
	if d.onCall != nil {
		d.onCall(ctx)
	}
	return d.result, d.err
}

func (d *workerDriver) Lookup(ctx context.Context, config DriverConfig, id string) (DriverResult, error) {
	d.lookups++
	d.config, d.id = config, id
	if d.onCall != nil {
		d.onCall(ctx)
	}
	return d.result, d.err
}

func demoJob() Job {
	order := receiptOrder()
	order.ID = "4190fbde-23fd-4b2c-9cda-647d289908f8"
	return Job{Order: order, PaymentID: "payment-id", Environment: "production", Profile: DefaultProfile(781898), RequestID: "pronogti-sale-" + order.ID, PreviousState: "pending"}
}

func liveJob() Job {
	job := demoJob()
	job.Profile.Mode = "live"
	job.Profile.DriverBaseURL = "http://127.0.0.1:8080"
	job.Profile.DeviceID = "cashdesk-ffd12"
	return job
}

func onlyFinish(t *testing.T, repository *workerRepository, state, code string) workerFinish {
	t.Helper()
	if len(repository.finished) != 1 {
		t.Fatalf("expected one persisted outcome, got %d", len(repository.finished))
	}
	outcome := repository.finished[0]
	if outcome.state != state || outcome.code != code {
		t.Fatalf("unexpected persisted outcome: state=%q code=%q", outcome.state, outcome.code)
	}
	return outcome
}

func TestWorkerDemoNeverContactsDriverOrRequiresToken(t *testing.T) {
	job := demoJob()
	job.Profile.BearerTokenEnv = "PRONOGTI_FISCAL_TEST_MISSING_DEMO_TOKEN"
	t.Setenv(job.Profile.BearerTokenEnv, "")
	repository := &workerRepository{jobs: []Job{job}}
	driver := &workerDriver{}
	worker := NewWorker(repository, driver, Options{})
	worked, err := worker.ProcessOne(context.Background())
	if err != nil || !worked {
		t.Fatalf("process demo: worked=%v err=%v", worked, err)
	}
	outcome := onlyFinish(t, repository, "demo", "")
	if driver.creates != 0 || driver.lookups != 0 || len(repository.saved) != 1 {
		t.Fatal("demo must journal its payload without consulting the receipt driver")
	}
	var result struct {
		Demo                 bool  `json:"demo"`
		FiscalDocumentIssued bool  `json:"fiscal_document_issued"`
		TotalKopecks         int64 `json:"total_kopecks"`
	}
	if err := json.Unmarshal(outcome.response, &result); err != nil || !result.Demo || result.FiscalDocumentIssued || result.TotalKopecks != job.Order.PayableKopecks {
		t.Fatalf("unexpected simulated result: %s (%v)", outcome.response, err)
	}
}

func TestWorkerTestPaymentsAndGlobalOptOutForceDemo(t *testing.T) {
	for _, tc := range []struct {
		name        string
		environment string
		allowLive   bool
	}{
		{"mock", "mock", true},
		{"sandbox", "sandbox", true},
		{"deployment demo", "production", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := liveJob()
			job.Environment = tc.environment
			job.Profile.BearerTokenEnv = "PRONOGTI_FISCAL_TEST_MISSING_FORCED_DEMO_TOKEN"
			t.Setenv(job.Profile.BearerTokenEnv, "")
			repository := &workerRepository{jobs: []Job{job}}
			driver := &workerDriver{}
			if worked, err := NewWorker(repository, driver, Options{AllowLive: tc.allowLive}).ProcessOne(context.Background()); err != nil || !worked {
				t.Fatalf("process forced demo: %v %v", worked, err)
			}
			onlyFinish(t, repository, "demo", "")
			if driver.creates+driver.lookups != 0 {
				t.Fatal("forced demo contacted the receipt driver")
			}
		})
	}
}

func TestWorkerLiveJournalsPayloadBeforeSingleCreate(t *testing.T) {
	job := liveJob()
	job.Profile.BearerTokenEnv = "PRONOGTI_FISCAL_TEST_LIVE_TOKEN"
	t.Setenv(job.Profile.BearerTokenEnv, "test-token")
	repository := &workerRepository{jobs: []Job{job}}
	response := json.RawMessage(`{"status":"completed","data":{"receipt":{"documentNumber":123}}}`)
	driver := &workerDriver{result: DriverResult{State: "completed", Response: response}}
	driver.onCall = func(context.Context) {
		if len(repository.saved) != 1 {
			t.Fatal("receipt was submitted before its payload was persisted")
		}
		if len(repository.dispatches) != 1 {
			t.Fatal("receipt was submitted before its dispatch marker was persisted")
		}
	}
	worker := NewWorker(repository, driver, Options{AllowLive: true})
	if worked, err := worker.ProcessOne(context.Background()); err != nil || !worked {
		t.Fatalf("process live: %v %v", worked, err)
	}
	outcome := onlyFinish(t, repository, "issued", "")
	if driver.creates != 1 || driver.lookups != 0 || driver.id != job.RequestID || !bytes.Equal(driver.payload, repository.saved[0]) || !bytes.Equal(outcome.response, response) {
		t.Fatal("live receipt identity/payload/outcome changed")
	}
	if driver.config.Token != "test-token" || driver.config.BaseURL != job.Profile.DriverBaseURL || driver.config.DeviceID != job.Profile.DeviceID || driver.config.Timeout != 100*time.Second {
		t.Fatal("live device configuration was not passed correctly")
	}
	if worked, err := worker.ProcessOne(context.Background()); err != nil || worked || driver.creates != 1 {
		t.Fatalf("completed job must not create again: %v %v", worked, err)
	}
}

func TestWorkerRecoveryUsesLookupWithOriginalIdentity(t *testing.T) {
	for _, previousState := range []string{"unknown", "processing"} {
		t.Run(previousState, func(t *testing.T) {
			job := liveJob()
			job.PreviousState = previousState
			started := time.Now()
			job.DispatchStartedAt = &started
			job.Payload = json.RawMessage(`{"original":true}`)
			repository := &workerRepository{jobs: []Job{job}}
			driver := &workerDriver{result: DriverResult{State: "completed", Response: json.RawMessage(`{"status":"completed"}`)}}
			if _, err := NewWorker(repository, driver, Options{AllowLive: true}).ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			onlyFinish(t, repository, "issued", "")
			if driver.lookups != 1 || driver.creates != 0 || driver.id != job.RequestID || len(repository.saved) != 0 {
				t.Fatal("recovery must look up the original journal entry without replacing or submitting its payload")
			}
		})
	}
}

func TestWorkerUnknownLookupNeverResubmits(t *testing.T) {
	for _, state := range []string{"not_found", "unknown", "in_progress"} {
		t.Run(state, func(t *testing.T) {
			job := liveJob()
			job.PreviousState, job.Payload = "unknown", json.RawMessage(`{"original":true}`)
			repository := &workerRepository{jobs: []Job{job}}
			driver := &workerDriver{result: DriverResult{State: state, ErrorCode: state}}
			if _, err := NewWorker(repository, driver, Options{AllowLive: true}).ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			onlyFinish(t, repository, "unknown", state)
			if driver.creates != 0 || driver.lookups != 1 {
				t.Fatal("uncertain receipt lookup must never submit a replacement receipt")
			}
		})
	}
}

func TestWorkerRecoveryPreservesUncertaintyWhenLiveDisabled(t *testing.T) {
	job := liveJob()
	job.PreviousState, job.Payload = "unknown", json.RawMessage(`{"original":true}`)
	repository := &workerRepository{jobs: []Job{job}}
	driver := &workerDriver{}
	if _, err := NewWorker(repository, driver, Options{}).ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	onlyFinish(t, repository, "unknown", "live_reconciliation_disabled")
	if driver.creates+driver.lookups != 0 {
		t.Fatal("global opt-out must also prevent reconciliation network access")
	}
}

func TestWorkerRecoveryWithoutPayloadCannotSubmit(t *testing.T) {
	job := liveJob()
	job.PreviousState = "processing"
	started := time.Now()
	job.DispatchStartedAt = &started
	repository := &workerRepository{jobs: []Job{job}}
	driver := &workerDriver{}
	if _, err := NewWorker(repository, driver, Options{AllowLive: true}).ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	onlyFinish(t, repository, "unknown", "request_not_journaled")
	if driver.creates+driver.lookups != 0 || len(repository.saved) != 0 {
		t.Fatal("missing original journal must not produce a replacement request")
	}
}

func TestWorkerInvalidPayloadFailsBeforeDriver(t *testing.T) {
	job := liveJob()
	job.Order.Items[0].AmountKopecks--
	repository := &workerRepository{jobs: []Job{job}}
	driver := &workerDriver{}
	if _, err := NewWorker(repository, driver, Options{AllowLive: true}).ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	onlyFinish(t, repository, "failed", "receipt_validation_failed")
	if driver.creates+driver.lookups != 0 || len(repository.saved) != 0 {
		t.Fatal("invalid fiscal payload reached external I/O")
	}
}

func TestWorkerSaveFailureStopsBeforeDriver(t *testing.T) {
	job := liveJob()
	writeErr := errors.New("database unavailable")
	repository := &workerRepository{jobs: []Job{job}, saveErr: writeErr}
	driver := &workerDriver{}
	worked, err := NewWorker(repository, driver, Options{AllowLive: true}).ProcessOne(context.Background())
	if !worked || !errors.Is(err, writeErr) || driver.creates+driver.lookups != 0 || len(repository.finished) != 0 {
		t.Fatalf("unpersisted payload must stop processing: worked=%v err=%v", worked, err)
	}
}

func TestWorkerCancelledTransportStillPersistsUnknownOutcome(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repository := &workerRepository{jobs: []Job{liveJob()}}
	driver := &workerDriver{err: context.Canceled, onCall: func(context.Context) { cancel() }}
	if worked, err := NewWorker(repository, driver, Options{AllowLive: true}).ProcessOne(ctx); err != nil || !worked {
		t.Fatalf("cancelled transport: %v %v", worked, err)
	}
	onlyFinish(t, repository, "unknown", "receipt_transport_uncertain")
	if len(repository.finishCtxErrs) != 1 || repository.finishCtxErrs[0] != nil {
		t.Fatal("request cancellation prevented journaling the uncertain fiscal outcome")
	}
	if driver.creates != 1 || driver.lookups != 0 {
		t.Fatal("cancelled transport must not trigger automatic retry")
	}
}

func TestWorkerRecoveredDemoClaimTerminatesWithoutNetwork(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mode        string
		environment string
	}{
		{"demo profile", "demo", "production"},
		{"mock payment", "live", "mock"},
		{"sandbox payment", "live", "sandbox"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := liveJob()
			job.Profile.Mode, job.Environment, job.PreviousState = tc.mode, tc.environment, "processing"
			repository := &workerRepository{jobs: []Job{job}}
			driver := &workerDriver{}
			if _, err := NewWorker(repository, driver, Options{AllowLive: true}).ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			onlyFinish(t, repository, "demo", "")
			if driver.creates+driver.lookups != 0 {
				t.Fatal("interrupted demo claim unexpectedly contacted a receipt device")
			}
		})
	}
}

func TestWorkerMissingTokenPreservesRecoveryUncertainty(t *testing.T) {
	for _, previousState := range []string{"pending", "unknown"} {
		t.Run(previousState, func(t *testing.T) {
			job := liveJob()
			job.PreviousState = previousState
			job.Profile.BearerTokenEnv = "PRONOGTI_FISCAL_TEST_UNAVAILABLE_TOKEN"
			t.Setenv(job.Profile.BearerTokenEnv, "")
			if previousState != "pending" {
				job.Payload = json.RawMessage(`{"original":true}`)
			}
			repository := &workerRepository{jobs: []Job{job}}
			driver := &workerDriver{}
			if _, err := NewWorker(repository, driver, Options{AllowLive: true}).ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			wantState := "failed"
			if previousState != "pending" {
				wantState = "unknown"
			}
			onlyFinish(t, repository, wantState, "receipt_token_unavailable")
			if driver.creates+driver.lookups != 0 {
				t.Fatal("missing required token must block receipt driver access")
			}
		})
	}
}

func TestWorkerDefiniteDriverFailureAndRepositoryErrors(t *testing.T) {
	job := liveJob()
	repository := &workerRepository{jobs: []Job{job}}
	driver := &workerDriver{result: DriverResult{State: "failed", ErrorCode: "device_error", Response: json.RawMessage(`{"status":"failed"}`)}}
	if _, err := NewWorker(repository, driver, Options{AllowLive: true}).ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	onlyFinish(t, repository, "failed", "device_error")
	claimErr := errors.New("claim failed")
	if worked, err := NewWorker(&workerRepository{claimErr: claimErr}, driver, Options{}).ProcessOne(context.Background()); worked || !errors.Is(err, claimErr) {
		t.Fatalf("claim failure was hidden: %v %v", worked, err)
	}
	finishErr := errors.New("finish failed")
	repository = &workerRepository{jobs: []Job{demoJob()}, finishErr: finishErr}
	if worked, err := NewWorker(repository, driver, Options{}).ProcessOne(context.Background()); !worked || !errors.Is(err, finishErr) {
		t.Fatalf("outcome persistence failure was hidden: %v %v", worked, err)
	}
}
