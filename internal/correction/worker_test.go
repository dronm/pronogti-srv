package correction

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dronm/pronogti/internal/fiscal"
)

type correctionFixture struct {
	source           Source
	history          []Correction
	job              Job
	claimError       error
	beginError       error
	begins           int
	finishedState    string
	finishedResponse json.RawMessage
	reserved         bool
}

func (f *correctionFixture) Load(context.Context, string) (Source, []Correction, error) {
	return f.source, f.history, nil
}
func (f *correctionFixture) Reserve(_ context.Context, _ string, _ int64, _ string, _ string, prepare func(Source) (Preview, error), _ CreateRequest) (Correction, error) {
	_, err := prepare(f.source)
	if err != nil {
		return Correction{}, err
	}
	f.reserved = true
	return Correction{ID: "reserved"}, nil
}
func (f *correctionFixture) Get(context.Context, string, string) (Correction, error) {
	return Correction{}, nil
}
func (f *correctionFixture) List(context.Context) ([]Correction, error) { return nil, nil }
func (f *correctionFixture) Resume(context.Context, string, string, int64, string) (Correction, error) {
	return Correction{}, nil
}
func (f *correctionFixture) Claim(context.Context) (Job, error) { return f.job, f.claimError }
func (f *correctionFixture) BeginDispatch(context.Context, Job) error {
	f.begins++
	return f.beginError
}
func (f *correctionFixture) Finish(_ context.Context, _ Job, state string, response json.RawMessage, _ string) error {
	f.finishedState = state
	f.finishedResponse = response
	return nil
}

type correctionDriverFixture struct {
	creates   int
	lookups   int
	result    fiscal.DriverResult
	err       error
	requestID string
}

func (f *correctionDriverFixture) CreateCorrection(_ context.Context, _ fiscal.DriverConfig, id string, _ json.RawMessage) (fiscal.DriverResult, error) {
	f.creates++
	f.requestID = id
	return f.result, f.err
}
func (f *correctionDriverFixture) LookupCorrection(_ context.Context, _ fiscal.DriverConfig, id string) (fiscal.DriverResult, error) {
	f.lookups++
	f.requestID = id
	return f.result, f.err
}

func workerJob() Job {
	return Job{CorrectionID: "correction", Environment: "production", Profile: fiscal.Profile{Mode: "live", DriverBaseURL: "http://127.0.0.1:8190", DeviceID: "kkt-1", TimeoutSeconds: 100},
		Document: Document{ID: "document", Role: "reversal", Attempt: 1, RequestID: "original-stable-request", Payload: json.RawMessage(`{"totalKopecks":10000}`)}}
}

func TestWorkerPersistsDispatchBeforeCreatingAndUsesCorrectionCommand(t *testing.T) {
	repo := &correctionFixture{job: workerJob()}
	driver := &correctionDriverFixture{result: fiscal.DriverResult{State: "completed", Response: json.RawMessage(`{"status":"completed"}`)}}
	svc := NewService(repo, driver, Options{AllowLive: true})
	worked, err := svc.ProcessOne(context.Background())
	if err != nil || !worked || repo.begins != 1 || driver.creates != 1 || driver.lookups != 0 || repo.finishedState != "issued" || driver.requestID != repo.job.Document.RequestID {
		t.Fatalf("unexpected dispatch: repo=%+v driver=%+v worked=%v err=%v", repo, driver, worked, err)
	}
	repo.beginError = errors.New("lost lease")
	_, err = svc.ProcessOne(context.Background())
	if err == nil || driver.creates != 1 {
		t.Fatal("a lost dispatch fence must prevent physical submission")
	}
}

func TestWorkerRecoveryNeverCreatesAnotherCorrection(t *testing.T) {
	for _, previous := range []string{"processing", "unknown"} {
		t.Run(previous, func(t *testing.T) {
			job := workerJob()
			job.PreviousState = previous
			if previous == "processing" {
				now := time.Now()
				job.Document.DispatchStartedAt = &now
			}
			repo := &correctionFixture{job: job}
			driver := &correctionDriverFixture{result: fiscal.DriverResult{State: "not_found"}}
			_, err := NewService(repo, driver, Options{AllowLive: true}).ProcessOne(context.Background())
			if err != nil || repo.begins != 0 || driver.creates != 0 || driver.lookups != 1 || repo.finishedState != "unknown" || driver.requestID != job.Document.RequestID {
				t.Fatalf("unsafe recovery: repo=%+v driver=%+v err=%v", repo, driver, err)
			}
		})
	}
}

func TestWorkerPreservesDefiniteFailureVersusAmbiguity(t *testing.T) {
	for _, test := range []struct {
		name   string
		result fiscal.DriverResult
		err    error
		want   string
	}{
		{"definite rejection", fiscal.DriverResult{State: "failed", ErrorCode: "device_error"}, nil, "failed"},
		{"lost final reply", fiscal.DriverResult{State: "unknown"}, errors.New("timeout"), "unknown"},
		{"no dispatch", fiscal.DriverResult{State: "failed", ErrorCode: "request_not_started"}, errors.New("canceled"), "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &correctionFixture{job: workerJob()}
			driver := &correctionDriverFixture{result: test.result, err: test.err}
			_, err := NewService(repo, driver, Options{AllowLive: true}).ProcessOne(context.Background())
			if err != nil || repo.finishedState != test.want {
				t.Fatalf("state=%s err=%v", repo.finishedState, err)
			}
		})
	}
}

func TestWorkerDemoAndLiveGateCannotAccidentallyUseHardware(t *testing.T) {
	for _, test := range []struct {
		name                    string
		demo, allow, recovering bool
		want                    string
	}{
		{"demo", true, true, false, "demo"}, {"live disabled", false, false, false, "failed"}, {"disabled reconciliation", false, false, true, "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := workerJob()
			if test.demo {
				job.Profile.Mode = "demo"
				job.Environment = "mock"
			}
			if test.recovering {
				job.PreviousState = "unknown"
			}
			repo := &correctionFixture{job: job}
			driver := &correctionDriverFixture{}
			_, err := NewService(repo, driver, Options{AllowLive: test.allow}).ProcessOne(context.Background())
			if err != nil || driver.creates != 0 || driver.lookups != 0 || repo.begins != 0 || repo.finishedState != test.want {
				t.Fatalf("unexpected hardware call or outcome: %+v %+v %v", repo, driver, err)
			}
		})
	}
}
