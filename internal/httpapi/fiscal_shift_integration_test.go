package httpapi

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dronm/pronogti/internal/fiscal"
)

func TestFiscalShiftProfileLookupAndFrozenRequestReplay(t *testing.T) {
	provider, conn := adminIntegrationDatabase(t, nil)
	ctx := context.Background()
	repository := fiscal.NewPostgresRepository(provider)
	createProfile := func(name, device string) int64 {
		t.Helper()
		var id int64
		if err := conn.QueryRow(ctx, `INSERT INTO public.fiscal_profiles
			(company_id,name,mode,driver_base_url,device_id)
			VALUES (781898,$1,'live','http://127.0.0.1:8190',$2) RETURNING id`, name, device).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	defaultID := createProfile("Default shift profile", "cashdesk-default")
	overrideID := createProfile("Kiosk shift profile", "cashdesk-override")
	if _, err := conn.Exec(ctx, `UPDATE public.salons SET default_fiscal_profile_id=$1 WHERE company_id=781898`, defaultID); err != nil {
		t.Fatal(err)
	}
	var kioskID int64
	if err := conn.QueryRow(ctx, `INSERT INTO public.kiosks(name,company_id,token_hash)
		VALUES ('Shift kiosk',781898,$1) RETURNING id`, strings.Repeat("a", 64)).Scan(&kioskID); err != nil {
		t.Fatal(err)
	}
	profile, err := repository.KioskShiftProfile(ctx, kioskID, 781898)
	if err != nil || profile.ID == nil || *profile.ID != defaultID {
		t.Fatalf("salon default profile: %+v, %v", profile, err)
	}
	if _, err := conn.Exec(ctx, `UPDATE public.kiosks SET fiscal_profile_id=$2 WHERE id=$1`, kioskID, overrideID); err != nil {
		t.Fatal(err)
	}
	profile, err = repository.KioskShiftProfile(ctx, kioskID, 781898)
	if err != nil || profile.ID == nil || *profile.ID != overrideID {
		t.Fatalf("kiosk override profile: %+v, %v", profile, err)
	}
	scope := fmt.Sprintf("kiosk:781898:%d", kioskID)
	const key = "close-frozen-target"
	frozen, found, err := repository.ShiftSnapshot(ctx, scope, key, &profile)
	if err != nil || !found || frozen.DeviceID != "cashdesk-override" {
		t.Fatalf("persist snapshot: %+v, found=%v, error=%v", frozen, found, err)
	}
	if _, err := conn.Exec(ctx, `UPDATE public.fiscal_profiles SET device_id='edited-device' WHERE id=$1`, overrideID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE public.kiosks SET fiscal_profile_id=NULL WHERE id=$1`, kioskID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE public.salons SET default_fiscal_profile_id=NULL WHERE company_id=781898`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.KioskShiftProfile(ctx, kioskID, 781898); err == nil {
		t.Fatal("removed kiosk/default assignment still resolved a new request")
	}
	edited, err := repository.ShiftProfile(ctx, overrideID)
	if err != nil || edited.DeviceID != "edited-device" {
		t.Fatalf("edited profile lookup: %+v, %v", edited, err)
	}
	for _, candidate := range []*fiscal.Profile{nil, &edited} {
		replayed, found, err := repository.ShiftSnapshot(ctx, scope, key, candidate)
		if err != nil || !found || replayed.DeviceID != "cashdesk-override" || replayed.ID == nil || *replayed.ID != overrideID {
			t.Fatalf("replay target changed: %+v, found=%v, error=%v", replayed, found, err)
		}
	}
	if _, found, err := repository.ShiftSnapshot(ctx, "profile:other", key, nil); err != nil || found {
		t.Fatalf("request crossed scope: found=%v, error=%v", found, err)
	}
	if _, err := repository.KioskShiftProfile(ctx, kioskID, 123456); err == nil {
		t.Fatal("kiosk fiscal profile crossed company scope")
	}
}
