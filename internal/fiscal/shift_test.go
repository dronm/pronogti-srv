package fiscal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type shiftProfileFixture struct {
	profiles  []Profile
	snapshots map[string]Profile
}

func (f *shiftProfileFixture) ShiftProfile(_ context.Context, id int64) (Profile, error) {
	for _, p := range f.profiles {
		if p.ID != nil && *p.ID == id {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("not found")
}
func (f *shiftProfileFixture) KioskShiftProfile(ctx context.Context, _, _ int64) (Profile, error) {
	return f.ShiftProfile(ctx, 1)
}
func (f *shiftProfileFixture) LiveShiftProfiles(context.Context) ([]Profile, error) {
	return f.profiles, nil
}

func (f *shiftProfileFixture) ShiftSnapshot(_ context.Context, scope, key string, profile *Profile) (Profile, bool, error) {
	if f.snapshots == nil {
		f.snapshots = make(map[string]Profile)
	}
	saved, found := f.snapshots[scope+":"+key]
	if !found && profile != nil {
		saved = *profile
		found = true
		f.snapshots[scope+":"+key] = saved
	}
	return saved, found, nil
}

type shiftDriverFixture struct {
	calls      int
	requestIDs []string
	params     json.RawMessage
}

func (f *shiftDriverFixture) CloseShift(_ context.Context, _ DriverConfig, id string, params json.RawMessage) (ShiftResult, error) {
	f.calls++
	f.requestIDs = append(f.requestIDs, id)
	f.params = append([]byte(nil), params...)
	return ShiftResult{State: "already_closed", Message: "Already closed", RequestID: id}, nil
}

func shiftTestProfile(id int64) Profile {
	p := DefaultProfile(10)
	p.ID = &id
	p.Mode = "live"
	p.DriverBaseURL = "http://127.0.0.1:8190"
	p.DeviceID = "cashdesk-1"
	return p
}

func TestShiftServiceLiveGateAndIdempotency(t *testing.T) {
	profile := shiftTestProfile(1)
	driver := &shiftDriverFixture{}
	service := NewShiftService(&shiftProfileFixture{profiles: []Profile{profile}}, driver, false)
	if _, err := service.CloseProfile(context.Background(), 1, "operator-1"); err == nil || driver.calls != 0 {
		t.Fatal("live close bypassed deployment switch")
	}
	service.allowLive = true
	for range 2 {
		if _, err := service.CloseProfile(context.Background(), 1, "operator-1"); err != nil {
			t.Fatal(err)
		}
	}
	if driver.calls != 2 || driver.requestIDs[0] != driver.requestIDs[1] {
		t.Fatal("retry changed driver's journal identity")
	}
	if !strings.Contains(string(driver.params), `"cashierName":"Cashier"`) {
		t.Fatalf("cashier missing: %s", driver.params)
	}
	profile.Mode = "demo"
	if result, err := service.Close(context.Background(), profile, "demo-1"); err != nil || result.State != "demo" || driver.calls != 2 {
		t.Fatal("demo touched hardware")
	}
	profile.Mode = "live"
	profile.Enabled = false
	if _, err := service.Close(context.Background(), profile, "disabled"); err == nil || driver.calls != 2 {
		t.Fatal("disabled profile touched hardware")
	}
}

func TestShiftCloseAllDeduplicatesPhysicalDevice(t *testing.T) {
	one, two, three := shiftTestProfile(1), shiftTestProfile(2), shiftTestProfile(3)
	two.DriverBaseURL += "/"
	three.DeviceID = "cashdesk-2"
	driver := &shiftDriverFixture{}
	service := NewShiftService(&shiftProfileFixture{profiles: []Profile{one, two, three}}, driver, true)
	if err := service.CloseAll(context.Background(), "cron-invocation-1", nil); err != nil || driver.calls != 2 {
		t.Fatalf("dedup calls=%d err=%v", driver.calls, err)
	}
}

func TestShiftHTTPDriverPreservesDefiniteAndUncertainOutcomes(t *testing.T) {
	const identity = `"deviceId":"cashdesk-1","cmd":"closeShiftIfOpen","requestId":"close-1"`
	cases := []struct {
		name                string
		status              int
		body, want, message string
	}{
		{"closed", 200, `{` + identity + `,"status":"completed","data":{"alreadyClosed":false,"shiftNumber":7}}`, "closed", "Смена закрыта."},
		{"already closed", 200, `{` + identity + `,"status":"completed","data":{"alreadyClosed":true,"shiftNumber":7}}`, "already_closed", "уже закрыта"},
		{"hardware error", 502, `{` + identity + `,"status":"failed","error":{"code":"device_error","message":"Бумага закончилась (0x22)"}}`, "failed", "Бумага закончилась (0x22)"},
		{"unknown", 409, `{` + identity + `,"status":"unknown","error":{"code":"outcome_unknown","message":"response lost"}}`, "unknown", "response lost"},
		{"missing state", 200, `{` + identity + `,"status":"completed","data":{"shiftNumber":7}}`, "unknown", "тем же"},
		{"wrong identity", 200, `{"deviceId":"other","cmd":"closeShiftIfOpen","requestId":"close-1","status":"completed","data":{"alreadyClosed":false,"shiftNumber":7}}`, "unknown", "тем же"},
		{"server error", 500, `{` + identity + `,"status":"failed","error":{"code":"device_error","message":"lost"}}`, "unknown", "тем же"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/terminal/api/v1/commands" || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("driver identity/auth wrong")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			result, err := (HTTPDriver{}).CloseShift(context.Background(), testDriverConfig(server.URL+"/terminal"), "close-1", json.RawMessage(`{"cashierName":"Cashier"}`))
			if err != nil || calls != 1 || result.State != tt.want || !strings.Contains(result.Message, tt.message) {
				t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
			}
		})
	}
}

func TestShiftRetryFreezesDeviceTargetAndKioskAssignment(t *testing.T) {
	profile := shiftTestProfile(1)
	repository := &shiftProfileFixture{profiles: []Profile{profile}}
	driver := &shiftDriverFixture{}
	service := NewShiftService(repository, driver, true)
	if _, err := service.CloseKiosk(context.Background(), 20, 10, "pending-1"); err != nil {
		t.Fatal(err)
	}
	repository.profiles = nil // Current assignment removed; old request must still replay.
	if _, err := service.CloseKiosk(context.Background(), 20, 10, "pending-1"); err != nil {
		t.Fatal(err)
	}
	if len(driver.requestIDs) != 2 || driver.requestIDs[0] != driver.requestIDs[1] {
		t.Fatal("retry changed driver journal identity")
	}
	if _, err := service.CloseKiosk(context.Background(), 20, 10, "new-operation"); err == nil {
		t.Fatal("new operation ignored removed assignment")
	}
}

func TestShiftPreparationFailuresDoNotFreezeInvalidSettings(t *testing.T) {
	for _, name := range []string{"disabled", "invalid connection", "live disabled", "missing bearer"} {
		t.Run(name, func(t *testing.T) {
			valid := shiftTestProfile(1)
			invalid := valid
			driver := &shiftDriverFixture{}
			repository := &shiftProfileFixture{}
			service := NewShiftService(repository, driver, true)
			switch name {
			case "disabled":
				invalid.Enabled = false
			case "invalid connection":
				invalid.DriverBaseURL = "invalid-url"
			case "live disabled":
				service.allowLive = false
			case "missing bearer":
				invalid.BearerTokenEnv = "PRONOGTI_SHIFT_MISSING_TEST_TOKEN"
				t.Setenv(invalid.BearerTokenEnv, "")
			}
			repository.profiles = []Profile{invalid}
			if _, err := service.CloseProfile(context.Background(), 1, "correctable-1"); err == nil {
				t.Fatal("invalid preparation succeeded")
			}
			if len(repository.snapshots) != 0 || driver.calls != 0 {
				t.Fatal("invalid preparation was frozen or dispatched")
			}
			repository.profiles = []Profile{valid}
			service.allowLive = true
			if _, err := service.CloseProfile(context.Background(), 1, "correctable-1"); err != nil || driver.calls != 1 {
				t.Fatalf("same key could not recover after correction: %v", err)
			}
		})
	}
}
