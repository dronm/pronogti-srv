// Package fiscal registers receipts only for durable, confirmed payments.
package fiscal

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/dronm/pronogti/internal/checkout"
)

var ErrNoJob = errors.New("no receipt job available")

type Job struct {
	Order             checkout.Order
	PaymentID         string
	Environment       string
	Profile           Profile
	RequestID         string
	PreviousState     string
	Payload           json.RawMessage
	ClaimedAt         time.Time
	CustomerContact   string
	DispatchStartedAt *time.Time
}

type Repository interface {
	Claim(context.Context) (Job, error)
	SavePayload(context.Context, Job, json.RawMessage) error
	// BeginDispatch durably records that the following HTTP call may reach the
	// device service. Recovery may submit only when this marker was never set.
	BeginDispatch(context.Context, Job) error
	Finish(context.Context, Job, string, json.RawMessage, string) error
}

type Driver interface {
	Create(context.Context, DriverConfig, string, json.RawMessage) (DriverResult, error)
	Lookup(context.Context, DriverConfig, string) (DriverResult, error)
}

type Options struct {
	// AllowLive is a second, deployment-level opt-in. False forces new work to demo.
	AllowLive    bool
	PollInterval time.Duration
}

type Worker struct {
	repository Repository
	driver     Driver
	options    Options
}

func NewWorker(repository Repository, driver Driver, options Options) *Worker {
	if options.PollInterval <= 0 {
		options.PollInterval = 2 * time.Second
	}
	return &Worker{repository: repository, driver: driver, options: options}
}

func (w *Worker) Run(ctx context.Context) error {
	if ctx == nil || w == nil || w.repository == nil || w.driver == nil {
		return errors.New("receipt worker is not configured")
	}
	ticker := time.NewTicker(w.options.PollInterval)
	defer ticker.Stop()
	for {
		worked, err := w.ProcessOne(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			slog.Error("receipt worker iteration failed", "error", err)
		}
		if worked && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// ProcessOne never repeats a potentially dispatched state-changing request.
// Recovery looks up the original request ID unless durable evidence proves
// dispatch never began.
func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	if ctx == nil || w == nil || w.repository == nil || w.driver == nil {
		return false, errors.New("receipt worker is not configured")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	job, err := w.repository.Claim(ctx)
	if errors.Is(err, ErrNoJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	finish := func(state string, response json.RawMessage, code string) (bool, error) {
		// Preserve the journal even when shutdown interrupted the outgoing request.
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return true, w.repository.Finish(saveCtx, job, state, response, code)
	}
	// An unknown legacy row remains uncertain even if its dispatch marker is
	// absent. A processing row with no marker proves dispatch never began, so a
	// crashed worker may resume its frozen payload and original request ID.
	recovering := job.DispatchStartedAt != nil || job.PreviousState == "unknown"
	if recovering && (job.Profile.Mode == "demo" || job.Environment != "production") {
		return finish("demo", json.RawMessage(`{"demo":true,"fiscal_document_issued":false}`), "")
	}
	if recovering {
		// Changing deployment mode must not erase an uncertain physical outcome.
		if !w.options.AllowLive {
			return finish("unknown", fiscalFailureResponse("live_reconciliation_disabled", "Live receipt reconciliation is disabled in the backend configuration."), "live_reconciliation_disabled")
		}
		if len(job.Payload) == 0 {
			return finish("unknown", fiscalFailureResponse("request_not_journaled", "The original receipt request was not saved; reconcile the device journal before retrying."), "request_not_journaled")
		}
	} else if len(job.Payload) == 0 {
		payload, err := BuildPayloadWithContact(job.Order, job.Profile, job.CustomerContact)
		if err != nil {
			return finish("failed", fiscalFailureResponse("receipt_validation_failed", err.Error()), "receipt_validation_failed")
		}
		if err := w.repository.SavePayload(ctx, job, payload); err != nil {
			return true, err
		}
		job.Payload = payload
	}
	if !recovering && (job.Profile.Mode == "demo" || job.Environment != "production" || !w.options.AllowLive) {
		result, _ := json.Marshal(map[string]any{"demo": true, "fiscal_document_issued": false, "order_id": job.Order.ID, "total_kopecks": job.Order.PayableKopecks})
		return finish("demo", result, "")
	}
	token := ""
	if job.Profile.BearerTokenEnv != "" {
		var present bool
		token, present = os.LookupEnv(job.Profile.BearerTokenEnv)
		if !present || token == "" {
			if recovering {
				return finish("unknown", fiscalFailureResponse("receipt_token_unavailable", "The configured receipt driver bearer token is unavailable."), "receipt_token_unavailable")
			}
			return finish("failed", fiscalFailureResponse("receipt_token_unavailable", "The configured receipt driver bearer token is unavailable."), "receipt_token_unavailable")
		}
	}
	config := DriverConfig{BaseURL: job.Profile.DriverBaseURL, DeviceID: job.Profile.DeviceID, Token: token, Timeout: time.Duration(job.Profile.TimeoutSeconds) * time.Second}
	if _, err := validateDriverConfig(config, job.RequestID); err != nil {
		if recovering {
			return finish("unknown", fiscalFailureResponse("receipt_configuration_invalid", err.Error()), "receipt_configuration_invalid")
		}
		return finish("failed", fiscalFailureResponse("receipt_configuration_invalid", err.Error()), "receipt_configuration_invalid")
	}
	var result DriverResult
	if recovering {
		result, err = w.driver.Lookup(ctx, config, job.RequestID)
	} else {
		if err := w.repository.BeginDispatch(ctx, job); err != nil {
			return true, err
		}
		result, err = w.driver.Create(ctx, config, job.RequestID, job.Payload)
	}
	if err != nil {
		if len(result.Response) == 0 {
			code := result.ErrorCode
			if code == "" {
				code = "receipt_transport_uncertain"
			}
			result.Response = fiscalFailureResponse(code, err.Error())
		}
		if !recovering && result.State == "failed" {
			return finish("failed", result.Response, result.ErrorCode)
		}
		return finish("unknown", result.Response, "receipt_transport_uncertain")
	}
	switch result.State {
	case "completed":
		return finish("issued", result.Response, "")
	case "failed":
		return finish("failed", result.Response, result.ErrorCode)
	default:
		return finish("unknown", result.Response, result.ErrorCode)
	}
}

// Local failures have no device envelope. Persist their explanation in the same
// shape as a driver error so operator details do not stop at a machine code.
func fiscalFailureResponse(code, message string) json.RawMessage {
	response, _ := json.Marshal(map[string]any{
		"source": "backend",
		"error":  map[string]string{"code": code, "message": message},
	})
	return response
}
