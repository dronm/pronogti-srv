package fiscal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

// CloseShift repeats only the same journaled driver operation. The driver owns
// the atomic check-and-close device lock and permanently records unknown results.
func (HTTPDriver) CloseShift(ctx context.Context, cfg DriverConfig, requestID string, params json.RawMessage) (ShiftResult, error) {
	base, err := validateDriverConfig(cfg, requestID)
	if err != nil {
		return ShiftResult{}, err
	}
	body, err := json.Marshal(struct {
		DeviceID  string          `json:"deviceId"`
		Command   string          `json:"cmd"`
		RequestID string          `json:"requestId"`
		Params    json.RawMessage `json:"params"`
	}{cfg.DeviceID, "closeShiftIfOpen", requestID, params})
	if err != nil {
		return ShiftResult{}, err
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/commands"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(body))
	if err != nil {
		return ShiftResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	client := &http.Client{Timeout: cfg.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("driver redirects disabled") }}
	result := ShiftResult{State: "unknown", Message: "ККТ не подтвердила результат. Повторите проверку с тем же идентификатором запроса; не создавайте новую операцию.", RequestID: requestID}
	response, err := client.Do(req)
	if err != nil {
		return result, nil
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxDriverResponseBytes+1))
	if err != nil || len(raw) > maxDriverResponseBytes || !json.Valid(raw) || !uniqueDriverJSON(raw) {
		return result, nil
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return result, nil
	}
	var envelope struct {
		DeviceID  string `json:"deviceId"`
		Command   string `json:"cmd"`
		RequestID string `json:"requestId"`
		Status    string `json:"status"`
		Data      struct {
			AlreadyClosed *bool   `json:"alreadyClosed"`
			ShiftNumber   *uint16 `json:"shiftNumber"`
		} `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.DeviceID != cfg.DeviceID || envelope.RequestID != requestID {
		return result, nil
	}
	if envelope.Command != "closeShiftIfOpen" && !(envelope.Command == "" && (envelope.Status == "unknown" || envelope.Status == "in_progress")) {
		return result, nil
	}
	result.DriverResponse = append(json.RawMessage(nil), raw...)
	if envelope.Status == "completed" && response.StatusCode == http.StatusOK && envelope.Error == nil && envelope.Data.AlreadyClosed != nil && envelope.Data.ShiftNumber != nil {
		result.State, result.ShiftNumber, result.Message = "closed", envelope.Data.ShiftNumber, "Смена закрыта."
		if *envelope.Data.AlreadyClosed {
			result.State, result.Message = "already_closed", "Смена уже закрыта. Команда закрытия не отправлялась."
		}
	} else if envelope.Status == "failed" && response.StatusCode >= 400 && response.StatusCode < 600 && response.StatusCode != 500 && envelope.Error != nil && envelope.Error.Code != "" && envelope.Error.Code != "idempotency_conflict" {
		result.State = "failed"
		result.Message = envelope.Error.Message
		if result.Message == "" {
			result.Message = envelope.Error.Code
		}
	} else if envelope.Error != nil && envelope.Error.Message != "" {
		result.Message += " " + envelope.Error.Message
	}
	return result, nil
}
