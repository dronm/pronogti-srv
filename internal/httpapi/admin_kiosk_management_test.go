package httpapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dronm/pronogti/internal/kiosk"
)

func TestAdminKioskManagementPreservesCheckoutHistory(t *testing.T) {
	fixture := newGeneratedAdministratorIntegration(t)
	ctx := context.Background()
	const originalCompany int64 = 781898
	const newCompany int64 = 880020
	if _, err := fixture.conn.Exec(ctx, `
		INSERT INTO public.salons (company_id, slug, name)
		VALUES ($1, 'kiosk-management-destination', 'Kiosk management destination')`, newCompany); err != nil {
		t.Fatal(err)
	}
	profiles := make(map[int64]int64, 2)
	for _, company := range []int64{originalCompany, newCompany} {
		var id int64
		if err := fixture.conn.QueryRow(ctx, `
			INSERT INTO public.fiscal_profiles (company_id, name) VALUES ($1, 'Kiosk integration profile')
			RETURNING id`, company).Scan(&id); err != nil {
			t.Fatal(err)
		}
		profiles[company] = id
	}
	createBody := fmt.Sprintf(`{"name":"Managed kiosk","company_id":%d,"is_active":true,"fiscal_profile_id":%d}`, originalCompany, profiles[originalCompany])
	response := adminRequest(fixture.handler, http.MethodPost, "/admin/kiosks", createBody, fixture.cookie, fixture.nonce)
	var created struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &created) != nil || created.ID <= 0 || len(created.Token) != 64 {
		t.Fatalf("create did not return a kiosk setup credential, status %d", response.Code)
	}
	path := "/admin/kiosks/" + strconv.FormatInt(created.ID, 10)
	const orderID = "be9546ab-a451-4bd5-82f7-cba26b95d414"
	if _, err := fixture.conn.Exec(ctx, `
		INSERT INTO public.payment_orders
			(id, company_id, kiosk_id, entry_mode, scope_hash, customer_session_hash,
			 items_snapshot, subtotal_kopecks, payable_kopecks, idempotency_key, request_hash, expires_at)
		VALUES ($1, $2, $3, 'kiosk', repeat('a',64), repeat('b',64), '[{}]',
			100, 100, 'kiosk-admin-history', repeat('c',64), now() + interval '1 hour')`, orderID, originalCompany, created.ID); err != nil {
		t.Fatal(err)
	}

	invalidMove := fmt.Sprintf(`{"company_id":%d,"fiscal_profile_id":%d}`, newCompany, profiles[originalCompany])
	response = adminRequest(fixture.handler, http.MethodPatch, path, invalidMove, fixture.cookie, fixture.nonce)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"fiscal_profile_id"`) {
		t.Fatalf("cross-company profile assignment returned %d: %s", response.Code, response.Body)
	}
	move := fmt.Sprintf(`{"company_id":%d,"fiscal_profile_id":%d}`, newCompany, profiles[newCompany])
	response = adminRequest(fixture.handler, http.MethodPatch, path, move, fixture.cookie, fixture.nonce)
	if response.Code != http.StatusOK {
		t.Fatalf("valid kiosk reassignment returned %d: %s", response.Code, response.Body)
	}
	var company, profile, version int64
	var storedHash string
	if err := fixture.conn.QueryRow(ctx, `
		SELECT company_id, fiscal_profile_id, session_version, token_hash FROM public.kiosks WHERE id = $1`, created.ID).
		Scan(&company, &profile, &version, &storedHash); err != nil {
		t.Fatal(err)
	}
	if company != newCompany || profile != profiles[newCompany] || version != 2 || storedHash == kiosk.HashToken(created.Token) {
		t.Fatal("reassignment did not change the company/profile and revoke the old credential")
	}
	response = adminRequest(fixture.handler, http.MethodPatch, path, `{"is_active":false}`, fixture.cookie, fixture.nonce)
	if response.Code != http.StatusOK {
		t.Fatalf("disable returned %d: %s", response.Code, response.Body)
	}
	var active bool
	if err := fixture.conn.QueryRow(ctx, `SELECT is_active FROM public.kiosks WHERE id = $1`, created.ID).Scan(&active); err != nil || active {
		t.Fatalf("explicit false did not disable the kiosk: active=%v, error=%v", active, err)
	}

	response = adminRequest(fixture.handler, http.MethodDelete, path, "", fixture.cookie, fixture.nonce)
	if response.Code != http.StatusOK {
		t.Fatalf("archive with historical checkout returned %d: %s", response.Code, response.Body)
	}
	var historicalCompany, historicalKiosk int64
	if err := fixture.conn.QueryRow(ctx, `SELECT company_id, kiosk_id FROM public.payment_orders WHERE id = $1`, orderID).
		Scan(&historicalCompany, &historicalKiosk); err != nil {
		t.Fatal(err)
	}
	if historicalCompany != originalCompany || historicalKiosk != created.ID {
		t.Fatal("archive changed the original checkout's company or kiosk reference")
	}
	if response := adminRequest(fixture.handler, http.MethodGet, path, "", fixture.cookie, ""); response.Code != http.StatusNotFound {
		t.Fatalf("archived kiosk detail returned %d", response.Code)
	}
	response = adminRequest(fixture.handler, http.MethodGet, "/admin/kiosks", "", fixture.cookie, "")
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "Managed kiosk") {
		t.Fatalf("archived kiosk still listed or list failed, status %d", response.Code)
	}
}

func TestAdminKioskTokenRotation(t *testing.T) {
	fixture := newGeneratedAdministratorIntegration(t)
	ctx := context.Background()
	oldToken := strings.Repeat("a", 64)
	var id, version int64
	if err := fixture.conn.QueryRow(ctx, `
		INSERT INTO public.kiosks (company_id, name, token_hash)
		VALUES (781898, 'Admin token rotation', $1)
		RETURNING id, session_version`, kiosk.HashToken(oldToken)).Scan(&id, &version); err != nil {
		t.Fatal(err)
	}
	path := "/admin/kiosks/" + strconv.FormatInt(id, 10)
	rotate := path + "/token/rotate"

	if response := adminRequest(fixture.handler, http.MethodPost, rotate, `{}`, nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous token rotation returned %d: %s", response.Code, response.Body)
	}
	for _, nonce := range []string{"", "invalid"} {
		response := adminRequest(fixture.handler, http.MethodPost, rotate, `{}`, fixture.cookie, nonce)
		if response.Code != http.StatusForbidden {
			t.Fatalf("rotation without the current nonce returned %d: %s", response.Code, response.Body)
		}
	}
	if response := adminRequest(fixture.handler, http.MethodPost, "/admin/kiosks/0/token/rotate", `{}`, fixture.cookie, fixture.nonce); response.Code != http.StatusBadRequest {
		t.Fatalf("rotation with invalid ID returned %d: %s", response.Code, response.Body)
	}
	if response := adminRequest(fixture.handler, http.MethodGet, "/admin/permissions", "", fixture.cookie, ""); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"kiosk.rotateToken"`) {
		t.Fatalf("missing compiled kiosk rotation permission: %d %s", response.Code, response.Body)
	}

	response := adminRequest(fixture.handler, http.MethodPost, rotate, `{}`, fixture.cookie, fixture.nonce)
	var credential struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(response.Body.Bytes(), &credential) != nil {
		t.Fatalf("token rotation returned invalid response metadata: %d, cache %q", response.Code, response.Header().Get("Cache-Control"))
	}
	raw, err := hex.DecodeString(credential.Token)
	if err != nil || len(raw) != 32 || credential.ID != id || credential.Token == oldToken {
		t.Fatal("token rotation did not return a new 32-byte credential for this kiosk")
	}
	var storedHash string
	var updatedVersion int64
	if err := fixture.conn.QueryRow(ctx, `SELECT token_hash, session_version FROM public.kiosks WHERE id = $1`, id).Scan(&storedHash, &updatedVersion); err != nil {
		t.Fatal(err)
	}
	if storedHash != kiosk.HashToken(credential.Token) || updatedVersion != version+1 {
		t.Fatal("token rotation did not save only the new digest and invalidate the previous session version")
	}
	response = adminRequest(fixture.handler, http.MethodGet, path, "", fixture.cookie, "")
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), credential.Token) || strings.Contains(response.Body.String(), storedHash) {
		t.Fatalf("kiosk detail exposed credential material or failed with status %d", response.Code)
	}

	response = adminRequest(fixture.handler, http.MethodDelete, path, "", fixture.cookie, fixture.nonce)
	if response.Code != http.StatusOK {
		t.Fatalf("archive before token rotation returned %d: %s", response.Code, response.Body)
	}
	response = adminRequest(fixture.handler, http.MethodPost, rotate, `{}`, fixture.cookie, fixture.nonce)
	if response.Code != http.StatusNotFound {
		t.Fatalf("rotation for an archived kiosk returned status %d", response.Code)
	}
}
