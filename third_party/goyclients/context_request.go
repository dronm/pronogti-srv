package goyclients

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const maxContextResponseBytes int64 = 8 * 1024 * 1024

// APIError means YClients explicitly rejected a request. Its status remains
// available to callers, while Is preserves the existing authorization errors.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e == nil {
		return "YClients API error"
	}
	return fmt.Sprintf("YClients API rejected request (HTTP %d): %s", e.StatusCode, e.Message)
}

func (e *APIError) Is(target error) bool {
	return e != nil && (e.StatusCode == http.StatusUnauthorized && target == ErrUnauthorized ||
		e.StatusCode == http.StatusForbidden && target == ErrForbidden)
}

// OutcomeUnknownError means a mutating request may have reached YClients. It is
// not permission to repeat the operation; reconcile the original payment first.
type OutcomeUnknownError struct {
	Cause error
}

func (e *OutcomeUnknownError) Error() string {
	return "YClients request outcome is unknown; reconcile the original operation before retrying"
}

func (e *OutcomeUnknownError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func IsOutcomeUnknown(err error) bool {
	var unknown *OutcomeUnknownError
	return errors.As(err, &unknown)
}

type contextRequestError struct {
	message string
	cause   error
}

func (e *contextRequestError) Error() string { return e.message }
func (e *contextRequestError) Unwrap() error { return e.cause }

func contextResponseFailure(mutating bool, message string, cause error) error {
	wrapped := &contextRequestError{message: message, cause: cause}
	if mutating {
		return &OutcomeUnknownError{Cause: wrapped}
	}
	return wrapped
}

// SendRequestContext is the additive, cancellable transport for financial
// operations. It never retries a request, never logs bodies or credentials, and
// conservatively identifies ambiguous writes. Legacy SendRequest is unchanged.
func (c *YClients) SendRequestContext(ctx context.Context, cmdEndpoint, rMethod string, urlParams url.Values, jsnParams, data, meta any) error {
	if ctx == nil {
		return errors.New("YClients request context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.HTTPClient == nil {
		return errors.New("YClients HTTP client is not initialized")
	}
	if strings.ContainsAny(c.PartnerToken+c.User.UserToken, "\r\n") {
		return errors.New("invalid YClients authentication configuration")
	}
	var body io.Reader
	if jsnParams != nil {
		encoded, err := json.Marshal(jsnParams)
		if err != nil {
			return &contextRequestError{message: "cannot encode YClients request", cause: err}
		}
		body = bytes.NewReader(encoded)
	}
	endpoint, err := url.Parse(strings.TrimRight(APIEndpoint, "/") + "/" + strings.TrimLeft(cmdEndpoint, "/"))
	if err != nil || endpoint == nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.Fragment != "" {
		return errors.New("invalid YClients endpoint configuration")
	}
	query := endpoint.Query()
	for key, values := range urlParams {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, rMethod, endpoint.String(), body)
	if err != nil {
		return errors.New("cannot construct YClients request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", apiVersion)
	auth := "Bearer " + c.PartnerToken
	if cmdEndpoint != authEndpoint && c.User.UserToken != "" {
		auth += ", User " + c.User.UserToken
	}
	request.Header.Set("Authorization", auth)
	if err := ctx.Err(); err != nil {
		return err
	}
	client := c.HTTPClient
	if standard, ok := client.(*http.Client); ok {
		// Avoid a redirect changing POST to GET or forwarding credentials to a
		// different origin. Copy rather than mutate the caller's shared client.
		copy := *standard
		copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client = &copy
	}
	mutating := rMethod != http.MethodGet && rMethod != http.MethodHead && rMethod != http.MethodOptions
	response, err := client.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return contextResponseFailure(mutating, "YClients request failed", err)
	}
	if response == nil {
		return contextResponseFailure(mutating, "YClients response is missing", nil)
	}
	if response.Body != nil {
		defer response.Body.Close()
	}
	// Explicit client errors are definite rejections, including authentication
	// failures whose response bodies were replaced by a proxy. 408 is uncertain.
	definiteClientError := response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != http.StatusRequestTimeout
	if response.StatusCode >= 500 || response.StatusCode == http.StatusRequestTimeout {
		cause := &APIError{StatusCode: response.StatusCode, Message: "server did not confirm the operation"}
		return contextResponseFailure(mutating, "YClients server did not confirm the operation", cause)
	}
	if response.Body == nil {
		if definiteClientError {
			return &APIError{StatusCode: response.StatusCode, Message: "request rejected"}
		}
		return contextResponseFailure(mutating, "YClients response body is missing", nil)
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, maxContextResponseBytes+1))
	if definiteClientError {
		message := "request rejected"
		if err == nil && int64(len(encoded)) <= maxContextResponseBytes {
			message = c.contextAPIMessage(encoded)
		}
		return &APIError{StatusCode: response.StatusCode, Message: message}
	}
	if err != nil {
		return contextResponseFailure(mutating, "cannot read YClients response", err)
	}
	if int64(len(encoded)) > maxContextResponseBytes {
		return contextResponseFailure(mutating, "YClients response exceeds the supported size", nil)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return contextResponseFailure(mutating, "unexpected YClients HTTP status", &APIError{StatusCode: response.StatusCode, Message: "unexpected HTTP status"})
	}
	var envelope struct {
		Success *bool           `json:"success"`
		Data    json.RawMessage `json:"data"`
		Meta    json.RawMessage `json:"meta"`
	}
	if err = json.Unmarshal(encoded, &envelope); err != nil || envelope.Success == nil {
		return contextResponseFailure(mutating, "invalid YClients response envelope", err)
	}
	if !*envelope.Success {
		var details map[string]json.RawMessage
		if err = json.Unmarshal(envelope.Meta, &details); err != nil || details == nil {
			return contextResponseFailure(mutating, "YClients rejection details are missing", err)
		}
		return &APIError{StatusCode: response.StatusCode, Message: c.contextAPIMessage(encoded)}
	}
	if data != nil {
		if len(envelope.Data) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Data), []byte("null")) {
			return contextResponseFailure(mutating, "YClients response data is missing", nil)
		}
		if err = json.Unmarshal(envelope.Data, data); err != nil {
			return contextResponseFailure(mutating, "cannot decode YClients response data", err)
		}
	}
	if meta != nil {
		if len(envelope.Meta) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Meta), []byte("null")) {
			return contextResponseFailure(mutating, "YClients response metadata is missing", nil)
		}
		if err = json.Unmarshal(envelope.Meta, meta); err != nil {
			return contextResponseFailure(mutating, "cannot decode YClients response metadata", err)
		}
	}
	return nil
}

var contextMessageURL = regexp.MustCompile(`https?://[^\s]+`)

func (c *YClients) contextAPIMessage(body []byte) string {
	var envelope struct {
		Meta struct {
			Message string `json:"message"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || strings.TrimSpace(envelope.Meta.Message) == "" {
		return "request rejected"
	}
	message := envelope.Meta.Message
	for _, secret := range []string{c.PartnerToken, c.User.UserToken} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	message = contextMessageURL.ReplaceAllString(message, "[redacted URL]")
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 1024 {
		message = message[:1024] + "..."
	}
	return message
}
