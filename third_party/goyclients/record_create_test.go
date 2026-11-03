package goyclients

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

func createRecordFixture() CreateRecord {
	return CreateRecord{
		StaffID: 9, Services: []CreateRecordService{{ID: 17}},
		Client:       &CreateRecordClient{Phone: "79000000000", Name: "Test"},
		DateTime:     APIDate(time.Date(2026, 11, 3, 14, 30, 45, 0, time.FixedZone("UTC+05", 5*60*60))),
		SeanceLength: 1800, APIID: "checkout-reference",
	}
}

func TestRecordCreateSendsFullTimestampAndOptions(t *testing.T) {
	client := NewYClients("partner-token", &mockHTTPClient{doFunc: func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/api/v1/records/781898" {
			t.Fatalf("incorrect record request: %s %s", req.Method, req.URL)
		}
		if req.URL.Query().Get("include_consumables") != "1" || req.URL.Query().Get("include_finance_transactions") != "1" {
			t.Fatalf("record options missing: %s", req.URL.RawQuery)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(body, &data); err != nil {
			t.Fatal(err)
		}
		if string(data["datetime"]) != `"2026-11-03T14:30:45+05:00"` {
			t.Fatalf("record datetime lost time or timezone: %s", data["datetime"])
		}
		if string(data["staff_id"]) != "9" || string(data["api_id"]) != `"checkout-reference"` || len(data["client"]) == 0 || len(data["services"]) == 0 {
			t.Fatalf("record fields were lost: %s", body)
		}
		return newMockResponse(http.StatusOK, `{"success":true,"data":[{"id":123}],"meta":[]}`), nil
	}})
	records, err := client.NewRecordAPI(781898).CreateWithOptions(createRecordFixture(), &CreateRecordOptions{IncludeConsumables: 1, IncludeFinanceTransactions: 1})
	if err != nil || len(records) != 1 || records[0].ID != 123 {
		t.Fatalf("records=%+v error=%v", records, err)
	}
}

func TestGroupRecordCreateOmitsUnspecifiedDateTime(t *testing.T) {
	client := NewYClients("partner-token", &mockHTTPClient{doFunc: func(req *http.Request) (*http.Response, error) {
		var data map[string]json.RawMessage
		if err := json.NewDecoder(req.Body).Decode(&data); err != nil {
			t.Fatal(err)
		}
		if _, exists := data["datetime"]; exists {
			t.Fatal("group record sent a fabricated datetime")
		}
		if string(data["event_id"]) != "99" {
			t.Fatal("group record lost event ID")
		}
		return newMockResponse(http.StatusOK, `{"success":true,"data":[],"meta":[]}`), nil
	}})
	_, err := client.NewRecordAPI(781898).Create(CreateRecord{EventID: 99, Client: &CreateRecordClient{Phone: "79000000000"}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRecordCreateRetainsRequiredFieldValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CreateRecord)
	}{
		{"staff", func(r *CreateRecord) { r.StaffID = 0 }},
		{"services", func(r *CreateRecord) { r.Services = nil }},
		{"datetime", func(r *CreateRecord) { r.DateTime = APIDate(time.Time{}) }},
		{"duration", func(r *CreateRecord) { r.SeanceLength = 0 }},
		{"client", func(r *CreateRecord) { r.Client = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := NewYClients("partner-token", &mockHTTPClient{doFunc: func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid record made an HTTP request")
				return nil, nil
			}})
			record := createRecordFixture()
			tc.mutate(&record)
			if _, err := client.NewRecordAPI(781898).Create(record); err == nil {
				t.Fatal("invalid record accepted")
			}
		})
	}
}
