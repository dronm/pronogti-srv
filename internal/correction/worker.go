package correction

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/dronm/pronogti/internal/fiscal"
)

func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.options.PollInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		worked, err := s.ProcessOne(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("fiscal correction worker iteration failed", "error", err)
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
	return nil
}

// ProcessOne dispatches only after a durable marker. Recovery looks up that
// same request; it never guesses that a timeout or missing journal means retry.
func (s *Service) ProcessOne(ctx context.Context) (bool, error) {
	job, err := s.repo.Claim(ctx)
	if errors.Is(err, ErrNoJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	finish := func(state string, response json.RawMessage, code string) (bool, error) {
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return true, s.repo.Finish(saveCtx, job, state, response, code)
	}
	recovering := job.Document.DispatchStartedAt != nil || job.PreviousState == "unknown"
	if job.Profile.Mode == "demo" && job.Environment != "production" {
		return finish("demo", json.RawMessage(`{"demo":true,"fiscal_document_issued":false}`), "")
	}
	failureState := "failed"
	if recovering {
		failureState = "unknown"
	}
	if job.Environment != "production" || job.Profile.Mode != "live" || !s.options.AllowLive {
		return finish(failureState, localFailure("live_correction_disabled", "Рабочая фискализация отключена. Коррекция не отправлена."), "live_correction_disabled")
	}
	if s.driver == nil {
		return finish(failureState, localFailure("driver_unavailable", "Драйвер фискализации недоступен."), "driver_unavailable")
	}
	token := ""
	if job.Profile.BearerTokenEnv != "" {
		token = os.Getenv(job.Profile.BearerTokenEnv)
		if token == "" {
			return finish(failureState, localFailure("driver_token_unavailable", "Ключ доступа к драйверу недоступен."), "driver_token_unavailable")
		}
	}
	cfg := fiscal.DriverConfig{BaseURL: job.Profile.DriverBaseURL, DeviceID: job.Profile.DeviceID, Token: token, Timeout: time.Duration(job.Profile.TimeoutSeconds) * time.Second}
	var result fiscal.DriverResult
	if recovering {
		result, err = s.driver.LookupCorrection(ctx, cfg, job.Document.RequestID)
	} else {
		if err := s.repo.BeginDispatch(ctx, job); err != nil {
			return true, err
		}
		result, err = s.driver.CreateCorrection(ctx, cfg, job.Document.RequestID, job.Document.Payload)
	}
	if err != nil {
		if len(result.Response) == 0 {
			result.Response = localFailure("correction_transport_uncertain", "Не удалось подтвердить результат коррекции. Требуется проверка исходного запроса.")
		}
		if !recovering && result.State == "failed" {
			return finish("failed", result.Response, result.ErrorCode)
		}
		return finish("unknown", result.Response, "correction_transport_uncertain")
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

func localFailure(code, message string) json.RawMessage {
	encoded, _ := json.Marshal(map[string]any{"source": "backend", "error": map[string]string{"code": code, "message": message}})
	return encoded
}
