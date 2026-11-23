package fiscal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

type kktProfilesFixture struct {
	profile Profile
	saved   map[string]KKTRequest
}

func (f *kktProfilesFixture) ShiftProfile(context.Context, int64) (Profile, error) {
	return f.profile, nil
}
func (f *kktProfilesFixture) KKTSnapshot(_ context.Context, scope, key string, initial *KKTRequest) (KKTRequest, bool, error) {
	k := scope + ":" + key
	if _, ok := f.saved[k]; !ok && initial != nil {
		f.saved[k] = *initial
	}
	val, ok := f.saved[k]
	return val, ok, nil
}

func TestKKTStatusAndOperationWireContracts(t *testing.T) {
	type command struct {
		DeviceID string          `json:"deviceId"`
		Name     string          `json:"cmd"`
		ID       string          `json:"requestId"`
		Params   json.RawMessage `json:"params"`
	}
	ctx := context.Background()
	var requests []command
	var data string
	var truncated bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req command
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		requests = append(requests, req)
		if r.URL.Path != "/api/v1/commands" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("wrong driver request")
		}
		w.Header().Set("Content-Type", "application/json")
		if truncated {
			fmt.Fprint(w, "{")
			return
		}
		fmt.Fprintf(w, `{"deviceId":%q,"cmd":%q,"requestId":%q,"status":"completed","data":%s}`, req.DeviceID, req.Name, req.ID, data)
	}))
	defer server.Close()
	profile := shiftTestProfile(4)
	profile.DriverBaseURL, profile.BearerTokenEnv = server.URL, "PRONOGTI_TEST_KKT_TOKEN"
	profile.SettlementAddress, profile.SettlementPlace = "Адрес", "Место"
	t.Setenv(profile.BearerTokenEnv, "test-secret")
	store := &kktProfilesFixture{profile: profile, saved: map[string]KKTRequest{}}
	service := NewKKTService(store, true)

	for _, tc := range []struct{ action, name, data string }{
		{KKTFormat, "getFiscalStorageFormat", `{"ffdVersion":0,"supportedFfdVersion":4}`},
		{KKTUnconfirmedCount, "getUnconfirmedDocumentCount", `{"count":0}`},
		{KKTUnconfirmedCount, "getUnconfirmedDocumentCount", `{"count":65535}`},
		{KKTShiftStatus, "getShiftParameters", `{"shiftState":0,"shiftNumber":0,"receiptNumber":0}`},
	} {
		data = tc.data
		for range 2 {
			out, err := service.Read(ctx, 4, tc.action)
			req := requests[len(requests)-1]
			var want map[string]uint32
			if err := json.Unmarshal([]byte(tc.data), &want); err != nil {
				t.Fatal(err)
			}
			if err != nil || out.State != "completed" || !reflect.DeepEqual(out.Data, want) || req.Name != tc.name || string(req.Params) != "{}" {
				t.Fatalf("%s: %+v %v", tc.action, out, err)
			}
		}
		if requests[len(requests)-2].ID == requests[len(requests)-1].ID || len(store.saved) != 0 {
			t.Fatal("status read was cached or journaled")
		}
	}
	for _, invalid := range []string{`{}`, `{"count":null}`, `{"count":65536}`, `{"count":-1}`, `{"count":"5"}`} {
		data = invalid
		out, err := service.Read(ctx, 4, KKTUnconfirmedCount)
		if err != nil || out.State != "failed" || out.Data != nil {
			t.Fatalf("invalid count %s: %+v %v", invalid, out, err)
		}
	}
	for _, tc := range []struct {
		action string
		steps  []string
	}{
		{KKTOpenShift, []string{"startOpenShift", "sendCashierData", "completeOpenShift"}},
		{KKTCloseShift, []string{"startCloseShift", "sendCashierData", "completeCloseShift"}},
		{KKTCloseFN, []string{"beginFiscalStorageClosure", "sendFiscalStorageClosureData", "finalizeFiscalStorageClosure"}},
	} {
		data = fmt.Sprintf(`{"results":[{"cmd":%q,"data":{"documentNumber":15,"fiscalSign":123,"shiftNumber":4,"warningFlags":0}}]}`, tc.steps[2])
		out, err := service.Run(ctx, 4, tc.action, tc.action, nil)
		req := requests[len(requests)-1]
		if err != nil || out.State != "completed" || req.Name != "executeOperation" || out.Data["documentNumber"] != 15 {
			t.Fatalf("%s: %+v %v", tc.action, out, err)
		}
		var params struct {
			Commands []kktStep `json:"commands"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, step := range params.Commands {
			names = append(names, step.Command)
		}
		if !reflect.DeepEqual(names, tc.steps) {
			t.Fatalf("steps: %v", names)
		}
		if tc.action == KKTCloseFN && params.Commands[1].Params["documentPlace"] != "Место" {
			t.Fatal("missing closure data")
		}
		if tc.action != KKTCloseFN && params.Commands[1].Params["cashierName"] != profile.CashierName {
			t.Fatal("missing cashier")
		}
	}
	reportType, reason := uint8(1), uint32(1<<31)
	registration := &FNRegistration{ReportType: &reportType, TaxationSystems: 3, ReregistrationReason: &reason, RegistrationNumber: "0000000001046446", UserInn: "7707083893", UserName: "Тест"}
	truncated = true
	first, err := service.Run(ctx, 4, KKTRegisterFN, "reg-attempt", registration)
	if err != nil || first.State != "unknown" {
		t.Fatalf("ambiguous write: %+v %v", first, err)
	}
	req := requests[len(requests)-1]
	if req.Name != "registerKkt" {
		t.Fatal("registration must be a top-level command")
	}
	store.profile.DriverBaseURL = "http://invalid.invalid"
	truncated = false
	data = `{"documentNumber":20,"fiscalSign":54321}`
	second, err := service.Run(ctx, 4, KKTRetry, "reg-attempt", nil)
	last := requests[len(requests)-1]
	if err != nil || second.State != "completed" || second.RequestID != first.RequestID || last.ID != req.ID || string(last.Params) != string(req.Params) {
		t.Fatalf("original request not recovered: %+v %v", second, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(last.Params, &payload); err != nil || payload["reregistrationReason"] != float64(2147483648) || payload["registrationNumber"] != registration.RegistrationNumber || payload["operatingMode"] != nil {
		t.Fatalf("registration data %s", last.Params)
	}
	count := len(requests)
	if _, err := service.Run(ctx, 4, KKTCloseFN, "reg-attempt", nil); err == nil || len(requests) != count {
		t.Fatal("a different command reused an existing key")
	}
	if _, err := service.Run(ctx, 4, KKTRetry, "never-submitted", nil); err == nil || len(requests) != count {
		t.Fatal("retry created a new intent")
	}
}

func TestKKTInvalidOrDisabledRequestsNeverDispatch(t *testing.T) {
	profile := shiftTestProfile(1)
	repo := &kktProfilesFixture{profile: profile, saved: map[string]KKTRequest{}}
	s := NewKKTService(repo, false)
	ctx := context.Background()
	if _, err := s.Run(ctx, 1, KKTCloseFN, "disabled", nil); err == nil {
		t.Fatal("allow_live bypassed")
	}
	repo.profile.Mode = "demo"
	result, err := s.Run(ctx, 1, KKTCloseFN, "demo", nil)
	if err != nil || result.State != "demo" {
		t.Fatal(result, err)
	}
	repo.profile.Enabled = false
	if _, err := s.Read(ctx, 1, KKTFormat); err == nil {
		t.Fatal("disabled profile was accepted")
	}
	for _, action := range []string{"executeOperation", KKTRegisterFN, KKTShiftStatus} {
		if _, err := s.Run(ctx, 1, action, "unused", nil); err == nil {
			t.Fatal("accepted", action)
		}
	}
	if kktOutput(KKTCloseFN, "finalizeFiscalStorageClosure", []byte(`{"results":[{"cmd":"finalizeSettlementReport","data":{"fiscalSign":1,"documentNumber":2}}]}`)) != nil {
		t.Fatal("accepted the wrong 0x28 report")
	}
	if kktOutput(KKTShiftStatus, "", []byte(`{"shiftNumber":null}`)) != nil {
		t.Fatal("invalid status treated as success")
	}
}
