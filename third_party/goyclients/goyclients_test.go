package goyclients

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

type mockHTTPClient struct {
	doFunc func(req *http.Request) (*http.Response, error)
}

func (m *mockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return m.doFunc(req)
}

func newMockResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     make(http.Header),
	}
}

func TestSendRequest_Success(t *testing.T) {
	mockClient := &mockHTTPClient{
		doFunc: func(req *http.Request) (*http.Response, error) {
			// basic request validation
			if req.Method != http.MethodGet {
				t.Fatalf("unexpected method: %s", req.Method)
			}

			if req.Header.Get("Authorization") == "" {
				t.Fatal("Authorization header is missing")
			}

			body := `{
				"success": true,
				"data": { "id": 123 },
				"meta": {}
			}`

			return newMockResponse(http.StatusOK, body), nil
		},
	}

	yc := NewYClients("partner-token", mockClient)

	var data struct {
		ID int `json:"id"`
	}

	err := yc.SendRequest(
		"test",
		http.MethodGet,
		nil,
		nil,
		&data,
		nil,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if data.ID != 123 {
		t.Fatalf("unexpected data value: %d", data.ID)
	}
}
