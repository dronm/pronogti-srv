// Package goyclients implements integration with yclients.com
package goyclients

// https://developers.yclients.com/ru/#section/SDK

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

var (
	APIEndpoint = "https://api.yclients.com" ///api/v1
	IsDebug     bool
)

const (
	apiVersion         = "application/vnd.yclients.v2+json"
	apiVersionEndpoint = "/api/v1"
	defHTTPTimeout     = 10
)

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
)

type RespError struct {
	Message string `json:"message"`
}

type Response struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Meta    json.RawMessage `json:"meta"`
}

type YClients struct {
	PartnerToken string
	User         User
	HTTPClient   HTTPClient
}

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func NewYClients(partnerToken string, httpClient HTTPClient) *YClients {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defHTTPTimeout * time.Second}
	}
	return &YClients{
		PartnerToken: partnerToken,
		HTTPClient:   httpClient,
	}
}

func (c *YClients) SendRequest(cmdEndpoint string, rMethod string, urlParams url.Values, jsnParams, data, meta any) error {
	var reqBody io.Reader
	if jsnParams != nil {
		paramB, err := json.Marshal(jsnParams)
		if err != nil {
			return err
		}
		reqBody = bytes.NewBuffer(paramB)

		if IsDebug {
			fmt.Println("[DEBUG] requestBody:", string(paramB))
		}
	}

	// Parse URL
	u, err := url.Parse(APIEndpoint + cmdEndpoint)
	if err != nil {
		return fmt.Errorf("invalid base URL: %w", err)
	}

	// Merge with existing query parameters if any
	if urlParams != nil {
		query := u.Query()
		// Add all parameters from urlParams
		for key, values := range urlParams {
			for _, value := range values {
				query.Add(key, value)
			}
		}
		u.RawQuery = query.Encode()
	}

	if IsDebug {
		fmt.Println("[DEBUG] method:", rMethod)
		fmt.Println("[DEBUG] URL:", u.String())
	}

	req, err := http.NewRequest(rMethod, u.String(), reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	auth := fmt.Sprintf("Bearer %s", c.PartnerToken)
	if cmdEndpoint != authEndpoint && c.User.UserToken != "" {
		auth += fmt.Sprintf(", User %s", c.User.UserToken)
		if IsDebug {
			fmt.Println("[DEBUG] added user token to query")
		}
	}

	// if IsDebug {
	// 	fmt.Println("[DEBUG] auth:", auth)
	// }

	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", apiVersion)

	if c.HTTPClient == nil {
		return errors.New("HTTP client is not initialized")
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// if IsDebug {
	// 	fmt.Println("[DEBUG] http response:", string(body))
	// }

	r := Response{}
	if err := json.Unmarshal(body, &r); err != nil {
		return err
	}
	if !r.Success {
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return ErrUnauthorized
		case http.StatusForbidden:
			return ErrForbidden
		}

		rErr := RespError{}
		if err := json.Unmarshal(r.Meta, &rErr); err != nil {
			return err
		}
		if rErr.Message != "" {
			return fmt.Errorf("%s, status:%d", rErr.Message, resp.StatusCode)
		}
		return fmt.Errorf("unknown error, http: %d", resp.StatusCode)
	}

	if data != nil {
		if err := json.Unmarshal(r.Data, data); err != nil {
			return err
		}
	}
	if meta != nil {
		if err := json.Unmarshal(r.Meta, meta); err != nil {
			return err
		}
	}

	return nil
}
