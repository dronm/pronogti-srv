package settlement

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

type Worker struct {
	repository Repository
	provider   Provider
	options    Options
}

func NewWorker(repository Repository, provider Provider, options Options) *Worker {
	if options.PollInterval <= 0 {
		options.PollInterval = 2 * time.Second
	}
	return &Worker{repository: repository, provider: provider, options: options}
}

func (w *Worker) Run(ctx context.Context) error {
	if w.repository == nil || w.provider == nil {
		return errors.New("YCLIENTS settlement worker is not configured")
	}
	ticker := time.NewTicker(w.options.PollInterval)
	defer ticker.Stop()
	for {
		worked, err := w.ProcessOne(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			slog.Error("YCLIENTS settlement iteration failed", "error", err)
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

func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	job, err := w.repository.Claim(ctx)
	if errors.Is(err, ErrNoJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	finish := func(state, code string) (bool, error) {
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return true, w.repository.Finish(saveCtx, job, state, code)
	}
	// A real upstream operation that may have started is never relabeled demo.
	interrupted := false
	for _, operation := range job.Operations {
		if operation.State == "dispatched" || operation.State == "unknown" {
			interrupted = true
		}
	}
	if err := ValidateCheckout(job.Order, job.Config); err != nil {
		if interrupted {
			return finish("unknown", "settlement_configuration_invalid")
		}
		return finish("failed", "settlement_configuration_invalid")
	}
	if !w.options.AllowLive || job.Environment != "production" || job.Config.Mode != "live" {
		if interrupted {
			return finish("unknown", "live_reconciliation_disabled")
		}
		return finish("demo", "")
	}
	if len(job.Operations) == 0 {
		operations, err := w.provider.Plan(ctx, job)
		if err != nil {
			worked, saveErr := finish("pending", "settlement_plan_unavailable")
			if saveErr != nil {
				return worked, saveErr
			}
			return worked, errors.New("YCLIENTS settlement plan is unavailable")
		}
		if len(operations) == 0 {
			return finish("failed", "settlement_plan_empty")
		}
		if err := w.repository.SavePlan(ctx, job, operations); err != nil {
			return true, err
		}
		job.Operations = operations
	}
	for index, operation := range job.Operations {
		if operation.State == "succeeded" {
			continue
		}
		if operation.State == "failed" {
			return finish("failed", operation.ErrorCode)
		}
		var outcome Outcome
		var executeErr error
		if operation.State == "dispatched" || operation.State == "unknown" {
			outcome, executeErr = w.provider.Reconcile(ctx, job, operation)
		} else {
			if operation.State != "pending" {
				return finish("failed", "settlement_operation_state_invalid")
			}
			if preparer, ok := w.provider.(interface {
				PrepareDispatch(context.Context, Job, Operation) error
			}); ok {
				if err := preparer.PrepareDispatch(ctx, job, operation); err != nil {
					worked, saveErr := finish("pending", "settlement_dispatch_not_started")
					if saveErr != nil {
						return worked, saveErr
					}
					return worked, errors.New("YCLIENTS settlement is unavailable before dispatch")
				}
			}
			if err := ctx.Err(); err != nil {
				return finish("pending", "worker_interrupted_before_dispatch")
			}
			if err := w.repository.BeginDispatch(ctx, job, operation.ID); err != nil {
				return true, err
			}
			operation.State = "dispatched"
			job.Operations[index] = operation
			outcome, executeErr = w.provider.Execute(ctx, job, operation)
		}
		if executeErr != nil {
			outcome = Outcome{State: "unknown", ErrorCode: "settlement_outcome_unknown"}
		}
		if outcome.State != "succeeded" && outcome.State != "failed" && outcome.State != "unknown" {
			outcome = Outcome{State: "unknown", ErrorCode: "settlement_response_invalid"}
		}
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err = w.repository.SaveOutcome(saveCtx, job, operation, outcome)
		cancel()
		if err != nil {
			return true, err
		}
		operation.State, operation.Result, operation.ErrorCode = outcome.State, outcome.Result, outcome.ErrorCode
		job.Operations[index] = operation
		if outcome.State != "succeeded" {
			return finish(outcome.State, outcome.ErrorCode)
		}
	}
	return finish("succeeded", "")
}
