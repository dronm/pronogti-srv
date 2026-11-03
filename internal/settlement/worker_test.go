package settlement

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type workerRepo struct {
	job                                                    Job
	available                                              bool
	savePlanError, errorBeforeDispatch, errorSavingOutcome error
	plans, dispatches, outcomes                            int
	finished                                               []string
	finishContexts                                         []error
}

func (r *workerRepo) Claim(context.Context) (Job, error) {
	if !r.available {
		return Job{}, ErrNoJob
	}
	job := r.job
	job.Operations = append([]Operation(nil), r.job.Operations...)
	return job, nil
}
func (r *workerRepo) SavePlan(_ context.Context, _ Job, operations []Operation) error {
	r.plans++
	if r.savePlanError != nil {
		return r.savePlanError
	}
	r.job.Operations = append([]Operation(nil), operations...)
	return nil
}
func (r *workerRepo) BeginDispatch(_ context.Context, _ Job, id string) error {
	if r.errorBeforeDispatch != nil {
		return r.errorBeforeDispatch
	}
	r.dispatches++
	for i := range r.job.Operations {
		if r.job.Operations[i].ID == id {
			r.job.Operations[i].State = "dispatched"
		}
	}
	return nil
}
func (r *workerRepo) SaveOutcome(_ context.Context, _ Job, operation Operation, outcome Outcome) error {
	r.outcomes++
	if r.errorSavingOutcome != nil {
		return r.errorSavingOutcome
	}
	for i := range r.job.Operations {
		if r.job.Operations[i].ID == operation.ID {
			r.job.Operations[i].State = outcome.State
			r.job.Operations[i].Result = outcome.Result
			r.job.Operations[i].ErrorCode = outcome.ErrorCode
		}
	}
	return nil
}
func (r *workerRepo) Finish(ctx context.Context, _ Job, state, code string) error {
	r.finished = append(r.finished, state)
	r.finishContexts = append(r.finishContexts, ctx.Err())
	return nil
}

type workerProvider struct {
	plans, prepares, writes, lookups int
	planError, prepareError          error
	execute, reconcile               Outcome
	onWrite                          func(context.Context)
}

func (p *workerProvider) Plan(context.Context, Job) ([]Operation, error) {
	p.plans++
	return []Operation{{ID: "op", Kind: KindServiceIncome, State: "pending", Request: json.RawMessage(`{"x":1}`)}}, p.planError
}
func (p *workerProvider) PrepareDispatch(context.Context, Job, Operation) error {
	p.prepares++
	return p.prepareError
}
func (p *workerProvider) Execute(ctx context.Context, _ Job, _ Operation) (Outcome, error) {
	p.writes++
	if p.onWrite != nil {
		p.onWrite(ctx)
	}
	if p.execute.State == "" {
		return succeeded(externalResult{TransactionID: 77}), nil
	}
	return p.execute, nil
}
func (p *workerProvider) Reconcile(context.Context, Job, Operation) (Outcome, error) {
	p.lookups++
	if p.reconcile.State == "" {
		return unknown("unconfirmed"), nil
	}
	return p.reconcile, nil
}

func TestWorkerDemoAndTestBanksNeverContactYClients(t *testing.T) {
	for _, testCase := range []struct {
		environment string
		allow       bool
		mode        string
	}{{"mock", true, "live"}, {"sandbox", true, "live"}, {"production", false, "live"}, {"production", true, "demo"}} {
		job := jobFixture()
		job.Environment = testCase.environment
		job.Config.Mode = testCase.mode
		repo := &workerRepo{job: job, available: true}
		provider := &workerProvider{}
		worked, err := NewWorker(repo, provider, Options{AllowLive: testCase.allow}).ProcessOne(context.Background())
		if !worked || err != nil || len(repo.finished) != 1 || repo.finished[0] != "demo" || provider.plans+provider.writes+provider.lookups+provider.prepares != 0 {
			t.Fatalf("demo invoked upstream: %+v %+v err=%v", repo, provider, err)
		}
	}
}

func TestWorkerJournalsDispatchBeforeMutationAndCompletes(t *testing.T) {
	repo := &workerRepo{job: jobFixture(), available: true}
	provider := &workerProvider{onWrite: func(context.Context) {
		if repo.dispatches != 1 || repo.plans != 1 || repo.job.Operations[0].State != "dispatched" {
			t.Fatal("financial write preceded durable dispatch marker")
		}
	}}
	worked, err := NewWorker(repo, provider, Options{AllowLive: true}).ProcessOne(context.Background())
	if !worked || err != nil || provider.writes != 1 || repo.job.Operations[0].State != "succeeded" || repo.finished[0] != "succeeded" {
		t.Fatalf("completion=%+v %+v err=%v", repo, provider, err)
	}
}

func TestWorkerLostPersistenceAfterWriteRecoversWithoutAnotherMutation(t *testing.T) {
	repo := &workerRepo{job: jobFixture(), available: true, errorSavingOutcome: errors.New("commit response lost")}
	provider := &workerProvider{reconcile: succeeded(externalResult{TransactionID: 77})}
	worker := NewWorker(repo, provider, Options{AllowLive: true})
	if _, err := worker.ProcessOne(context.Background()); err == nil {
		t.Fatal("lost persistence was hidden")
	}
	if repo.job.Operations[0].State != "dispatched" || provider.writes != 1 {
		t.Fatal("upstream write was not journaled")
	}
	repo.errorSavingOutcome = nil
	if _, err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.writes != 1 || provider.lookups != 1 || repo.finished[0] != "succeeded" {
		t.Fatalf("duplicate write during recovery: %+v %+v", repo, provider)
	}
}

func TestWorkerUnknownNeverResubmitsAndCannotBecomeDemo(t *testing.T) {
	job := jobFixture()
	job.Operations = []Operation{{ID: "op", Kind: KindVisitPayment, State: "unknown", Request: json.RawMessage(`{}`)}}
	repo := &workerRepo{job: job, available: true}
	provider := &workerProvider{}
	if _, err := NewWorker(repo, provider, Options{AllowLive: true}).ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.lookups != 1 || provider.writes != 0 || repo.finished[0] != "unknown" {
		t.Fatal("unknown operation was resubmitted")
	}
	if _, err := NewWorker(repo, provider, Options{}).ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.finished[1] != "unknown" || provider.lookups != 1 {
		t.Fatal("disabled live mode erased an uncertain upstream result")
	}
}

func TestWorkerSafeReadFailureCanRetryBeforeDispatch(t *testing.T) {
	repo := &workerRepo{job: jobFixture(), available: true}
	provider := &workerProvider{prepareError: errors.New("auth unavailable")}
	worker := NewWorker(repo, provider, Options{AllowLive: true})
	if _, err := worker.ProcessOne(context.Background()); err == nil {
		t.Fatal("read failure not returned")
	}
	if repo.dispatches != 0 || provider.writes != 0 || repo.finished[0] != "pending" {
		t.Fatal("read failure was mistaken for a financial write")
	}
	provider.prepareError = nil
	if _, err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.writes != 1 || repo.finished[1] != "succeeded" {
		t.Fatal("safe pre-dispatch failure could not recover")
	}
}

func TestWorkerFailedDispatchFencePreventsUpstreamWrite(t *testing.T) {
	repo := &workerRepo{job: jobFixture(), available: true, errorBeforeDispatch: errors.New("claim no longer held")}
	provider := &workerProvider{}
	if _, err := NewWorker(repo, provider, Options{AllowLive: true}).ProcessOne(context.Background()); err == nil || provider.writes != 0 {
		t.Fatal("lost database claim permitted a write")
	}
}

func TestWorkerCancelledWritePersistsUncertaintyWithDetachedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	repo := &workerRepo{job: jobFixture(), available: true}
	provider := &workerProvider{execute: unknown("timeout"), onWrite: func(context.Context) { cancel() }}
	if _, err := NewWorker(repo, provider, Options{AllowLive: true}).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	if repo.finished[0] != "unknown" || repo.finishContexts[0] != nil || repo.job.Operations[0].State != "unknown" {
		t.Fatal("shutdown lost the uncertain financial outcome")
	}
}

func TestWorkerCompletedOperationsAreNotRepeated(t *testing.T) {
	job := jobFixture()
	job.Operations = []Operation{{ID: "done", State: "succeeded", Result: json.RawMessage(`{"transaction_id":77}`)}, {ID: "next", State: "pending"}}
	repo := &workerRepo{job: job, available: true}
	provider := &workerProvider{}
	if _, err := NewWorker(repo, provider, Options{AllowLive: true}).ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.plans != 0 || provider.writes != 1 || repo.dispatches != 1 {
		t.Fatal("completed settlement operation was repeated")
	}
}
