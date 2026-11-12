package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestAdminFiscalDriverConnections(t *testing.T) {
	t.Setenv(fiscalDriverConnectionsEnv, `[
		{"base_url":"http://127.0.0.1:8090"},
		{"base_url":"https://driver.example.test","bearer_token_env":"TERMINALFA_TOKEN"}
	]`)
	fixture := newGeneratedAdministratorIntegration(t)
	ctx := context.Background()

	const create = `{"company_id":781898,"name":"Connection test","enabled":true,"mode":"demo",
		"timeout_seconds":100,"cashier_name":"Cashier","tax_system":1,"service_vat_rate":6,
		"good_vat_rate":6,"payment_method":4,"internet_payment":false,"suppress_print":false,
		"driver_base_url":"http://127.0.0.1:8090","device_id":"cashdesk-1"}`
	response := adminRequest(fixture.handler, http.MethodPost, "/admin/fiscal-profiles", create, fixture.cookie, fixture.nonce)
	var created struct {
		ID int64 `json:"id"`
	}
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &created) != nil || created.ID <= 0 {
		t.Fatalf("approved create: %d %s", response.Code, response.Body)
	}
	unapproved := strings.Replace(create, "http://127.0.0.1:8090", "http://unknown.example.test", 1)
	response = adminRequest(fixture.handler, http.MethodPost, "/admin/fiscal-profiles", unapproved, fixture.cookie, fixture.nonce)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "driver_base_url") {
		t.Fatalf("unapproved create: %d %s", response.Code, response.Body)
	}

	patch := func(id int64, body string, expected int, field string) {
		t.Helper()
		path := "/admin/fiscal-profiles/" + strconv.FormatInt(id, 10)
		response := adminRequest(fixture.handler, http.MethodPatch, path, body, fixture.cookie, fixture.nonce)
		if response.Code != expected || field != "" && !strings.Contains(response.Body.String(), field) {
			t.Fatalf("patch %s: got %d %s, want %d", body, response.Code, response.Body, expected)
		}
	}
	assertPair := func(id int64, wantURL, wantEnv string) {
		t.Helper()
		var baseURL, tokenEnv string
		err := fixture.conn.QueryRow(ctx, `SELECT coalesce(driver_base_url, ''), coalesce(bearer_token_env, '')
			FROM public.fiscal_profiles WHERE id = $1`, id).Scan(&baseURL, &tokenEnv)
		if err != nil || baseURL != wantURL || tokenEnv != wantEnv {
			t.Fatalf("fiscal profile %d connection = (%q,%q), want (%q,%q): %v", id, baseURL, tokenEnv, wantURL, wantEnv, err)
		}
	}

	patch(created.ID, `{"bearer_token_env":"TERMINALFA_TOKEN"}`, http.StatusBadRequest, "bearer_token_env")
	patch(created.ID, `{"driver_base_url":"https://driver.example.test"}`, http.StatusBadRequest, "driver_base_url")
	assertPair(created.ID, "http://127.0.0.1:8090", "")
	patch(created.ID, `{"driver_base_url":"https://driver.example.test","bearer_token_env":"TERMINALFA_TOKEN","mode":"live"}`, http.StatusOK, "")
	assertPair(created.ID, "https://driver.example.test", "TERMINALFA_TOKEN")
	patch(created.ID, `{"driver_base_url":null,"bearer_token_env":null}`, http.StatusBadRequest, "driver_base_url")
	patch(created.ID, `{"device_id":null}`, http.StatusBadRequest, "device_id")
	patch(created.ID, `{"mode":"demo","driver_base_url":null,"bearer_token_env":null}`, http.StatusOK, "")
	assertPair(created.ID, "", "")

	var legacy int64
	if err := fixture.conn.QueryRow(ctx, `INSERT INTO public.fiscal_profiles
		(company_id, name, driver_base_url, bearer_token_env) VALUES (781898, $1, $2, $3) RETURNING id`,
		fmt.Sprintf("Legacy driver %d", created.ID), "http://legacy.example.test", "LEGACY_TOKEN").Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	patch(legacy, `{"name":"Renamed legacy driver","driver_base_url":"http://legacy.example.test"}`, http.StatusOK, "")
	patch(legacy, `{"bearer_token_env":"TERMINALFA_TOKEN"}`, http.StatusBadRequest, "bearer_token_env")
	assertPair(legacy, "http://legacy.example.test", "LEGACY_TOKEN")
}
