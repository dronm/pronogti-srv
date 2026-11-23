package fiscal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DriverConfig addresses a logical device in the goterminalfa HTTP service.
// BaseURL may use HTTP on a private device network; Token never appears in URLs.
type DriverConfig struct {
	BaseURL  string
	DeviceID string
	Token    string
	Timeout  time.Duration
}

// DriverResult preserves the driver's journal outcome, not a guessed receipt
// outcome. Response is present only when the server returned valid JSON.
type DriverResult struct {
	State     string
	Response  json.RawMessage
	ErrorCode string
}

// HTTPDriver never repeats a mutating request. An ambiguous Create result must
// be reconciled through Lookup with the same device and request identity.
type HTTPDriver struct{}

var driverID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

const maxDriverResponseBytes = 1024 * 1024

var errDriverUncertain = errors.New("receipt driver did not confirm an outcome; look up the original request ID")

func (HTTPDriver) Create(ctx context.Context, cfg DriverConfig, requestID string, params json.RawMessage) (DriverResult, error) {
	return submitDriverCommand(ctx, cfg, requestID, params, "createReceipt", validFiscalResult)
}

// CreateCorrection uses the driver's complete correction workflow, including
// conditional shift opening and cleanup. Each fiscal document owns a request ID.
func (HTTPDriver) CreateCorrection(ctx context.Context, cfg DriverConfig, requestID string, params json.RawMessage) (DriverResult, error) {
	return submitDriverCommand(ctx, cfg, requestID, params, "createCorrectionReceipt", validFiscalResult)
}

func submitDriverCommand(ctx context.Context, cfg DriverConfig, requestID string, params json.RawMessage, command string, validData func(json.RawMessage) bool) (DriverResult, error) {
	if ctx == nil {
		return DriverResult{State: "failed", ErrorCode: "invalid_context"}, errors.New("receipt context is required")
	}
	if err := ctx.Err(); err != nil {
		return DriverResult{State: "failed", ErrorCode: "request_not_started"}, err
	}
	base, err := validateDriverConfig(cfg, requestID)
	if err != nil {
		return DriverResult{State: "failed", ErrorCode: "invalid_configuration"}, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(params, &object); err != nil || object == nil {
		return DriverResult{State: "failed", ErrorCode: "invalid_request"}, errors.New("receipt params must be a JSON object")
	}
	body, err := json.Marshal(struct {
		DeviceID  string          `json:"deviceId"`
		Command   string          `json:"cmd"`
		RequestID string          `json:"requestId"`
		Params    json.RawMessage `json:"params"`
	}{cfg.DeviceID, command, requestID, params})
	if err != nil {
		return DriverResult{State: "failed", ErrorCode: "invalid_request"}, errors.New("cannot encode receipt request")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/commands"
	return exchangeDriver(ctx, cfg, requestID, base, body, false, command, validData)
}

func (HTTPDriver) Lookup(ctx context.Context, cfg DriverConfig, requestID string) (DriverResult, error) {
	return lookupDriverReceipt(ctx, cfg, requestID, "createReceipt")
}

// LookupCorrection retrieves the original correction request without resending it.
func (HTTPDriver) LookupCorrection(ctx context.Context, cfg DriverConfig, requestID string) (DriverResult, error) {
	return lookupDriverReceipt(ctx, cfg, requestID, "createCorrectionReceipt")
}

func lookupDriverReceipt(ctx context.Context, cfg DriverConfig, requestID, command string) (DriverResult, error) {
	base, err := validateDriverConfig(cfg, requestID)
	if err != nil {
		return DriverResult{State: "unknown", ErrorCode: "invalid_configuration"}, err
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/requests"
	query := url.Values{"deviceId": {cfg.DeviceID}, "requestId": {requestID}}
	base.RawQuery = query.Encode()
	return exchangeDriver(ctx, cfg, requestID, base, nil, true, command, validFiscalResult)
}

func validateDriverConfig(cfg DriverConfig, requestID string) (*url.URL, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" ||
		base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Opaque != "" ||
		base.RawPath != "" || !validDriverToken(cfg.Token) || cfg.Timeout <= 0 ||
		!driverID.MatchString(cfg.DeviceID) || !driverID.MatchString(requestID) {
		return nil, errors.New("invalid receipt driver configuration or request identity")
	}
	return base, nil
}

func validDriverToken(token string) bool {
	for _, char := range token {
		if char < 33 || char > 126 {
			return false
		}
	}
	return true
}

func exchangeDriver(ctx context.Context, cfg DriverConfig, requestID string, endpoint *url.URL, body []byte, lookup bool, command string, validData func(json.RawMessage) bool) (DriverResult, error) {
	method := http.MethodPost
	if lookup {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return DriverResult{State: "unknown"}, errDriverUncertain
	}
	req.Header.Set("Accept", "application/json")
	if !lookup {
		req.Header.Set("Content-Type", "application/json")
	}
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	client := &http.Client{
		Timeout: cfg.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("receipt driver redirects are disabled")
		},
	}
	response, err := client.Do(req)
	if err != nil {
		return DriverResult{State: "unknown"}, errDriverUncertain
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxDriverResponseBytes+1))
	if err != nil || len(data) > maxDriverResponseBytes {
		return DriverResult{State: "unknown"}, errDriverUncertain
	}
	result := DriverResult{State: "unknown"}
	if json.Valid(data) {
		result.Response = append(json.RawMessage(nil), data...)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || len(result.Response) == 0 || !uniqueDriverJSON(data) {
		return result, errDriverUncertain
	}
	var envelope struct {
		DeviceID  string          `json:"deviceId"`
		Command   string          `json:"cmd"`
		RequestID string          `json:"requestId"`
		Status    string          `json:"status"`
		Data      json.RawMessage `json:"data"`
		Error     *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return result, errDriverUncertain
	}
	if envelope.Error != nil {
		result.ErrorCode = envelope.Error.Code
	}
	if envelope.DeviceID != cfg.DeviceID || envelope.RequestID != requestID {
		return result, errDriverUncertain
	}
	// Lookup's live journal states omit cmd. Saved execution responses contain it.
	if envelope.Command != command && !(lookup && envelope.Command == "" &&
		(envelope.Status == "unknown" || envelope.Status == "in_progress" || result.ErrorCode == "not_found")) {
		return result, errDriverUncertain
	}
	switch envelope.Status {
	case "completed":
		if response.StatusCode != http.StatusOK || envelope.Error != nil || !validData(envelope.Data) {
			return result, errDriverUncertain
		}
		result.State = "completed"
	case "failed":
		if lookup && envelope.Command == "" && response.StatusCode == http.StatusNotFound && result.ErrorCode == "not_found" {
			result.State = "not_found"
			return result, nil
		}
		// A conflict is about a prior claimed request and says nothing about its
		// fiscal outcome. HTTP 500 is likewise never evidence of non-execution.
		if response.StatusCode < 400 || response.StatusCode > 599 || response.StatusCode == http.StatusInternalServerError ||
			result.ErrorCode == "" || result.ErrorCode == "idempotency_conflict" {
			return result, errDriverUncertain
		}
		result.State = "failed"
	case "in_progress":
		if response.StatusCode != http.StatusConflict || result.ErrorCode != "request_in_progress" {
			return result, errDriverUncertain
		}
		result.State = "in_progress"
	case "unknown":
		if response.StatusCode != http.StatusConflict || result.ErrorCode != "outcome_unknown" {
			return result, errDriverUncertain
		}
	default:
		return result, errDriverUncertain
	}
	return result, nil
}

// Duplicate identity, status or fiscal-result fields are ambiguous even when
// encoding/json could select one of their values. Reject duplicates at any depth.
func uniqueDriverJSON(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var value func() error
	value = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, container := token.(json.Delim)
		if !container {
			return nil
		}
		if delimiter == '{' {
			seen := make(map[string]struct{})
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return errDriverUncertain
				}
				if _, duplicate := seen[name]; duplicate {
					return errDriverUncertain
				}
				seen[name] = struct{}{}
				if err := value(); err != nil {
					return err
				}
			}
		} else if delimiter == '[' {
			for decoder.More() {
				if err := value(); err != nil {
					return err
				}
			}
		} else {
			return errDriverUncertain
		}
		_, err = decoder.Token()
		return err
	}
	if err := value(); err != nil {
		return false
	}
	_, err := decoder.Token()
	return errors.Is(err, io.EOF)
}

func validFiscalResult(data json.RawMessage) bool {
	var result struct {
		ShiftOpenedByRequest *bool `json:"shiftOpenedByRequest"`
		Receipt              *struct {
			ReceiptNumber  *uint16 `json:"receiptNumber"`
			DocumentNumber *uint32 `json:"documentNumber"`
			FiscalSign     *uint32 `json:"fiscalSign"`
			DateTime       string  `json:"dateTime"`
			ShiftNumber    *uint16 `json:"shiftNumber"`
		} `json:"receipt"`
	}
	if err := json.Unmarshal(data, &result); err != nil || result.ShiftOpenedByRequest == nil || result.Receipt == nil {
		return false
	}
	receipt := result.Receipt
	_, err := time.Parse("2006-01-02T15:04", receipt.DateTime)
	return err == nil && receipt.ReceiptNumber != nil && receipt.DocumentNumber != nil && receipt.FiscalSign != nil && receipt.ShiftNumber != nil
}
