package fiscal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dronm/ds/v4"
	"github.com/dronm/webapp"
)

func (r *PostgresRepository) ShiftProfile(ctx context.Context, id int64) (Profile, error) {
	return r.shiftProfile(ctx, `SELECT to_jsonb(p) FROM public.fiscal_profiles p WHERE p.id=$1`, id)
}

func (r *PostgresRepository) KioskShiftProfile(ctx context.Context, kioskID, companyID int64) (Profile, error) {
	return r.shiftProfile(ctx, `SELECT to_jsonb(p) FROM public.kiosks k
		LEFT JOIN public.salons s ON s.company_id=k.company_id
		JOIN public.fiscal_profiles p ON p.id=COALESCE(k.fiscal_profile_id,s.default_fiscal_profile_id) AND p.company_id=k.company_id
		WHERE k.id=$1 AND k.company_id=$2 AND k.is_active AND k.archived_at IS NULL`, kioskID, companyID)
}

func (r *PostgresRepository) shiftProfile(ctx context.Context, sql string, args ...any) (Profile, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Profile{}, err
	}
	defer release()
	var raw []byte
	if err := conn.QueryRow(ctx, sql, args...).Scan(&raw); err != nil {
		if errors.Is(err, ds.ErrNoRows) {
			return Profile{}, webapp.NotFound("Фискальный профиль не найден.", nil)
		}
		return Profile{}, fmt.Errorf("read shift fiscal profile: %w", err)
	}
	var profile Profile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return Profile{}, fmt.Errorf("decode shift fiscal profile: %w", err)
	}
	return profile, nil
}

func (r *PostgresRepository) LiveShiftProfiles(ctx context.Context) ([]Profile, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := conn.Query(ctx, `SELECT to_jsonb(p) FROM public.fiscal_profiles p WHERE p.enabled AND p.mode='live' ORDER BY p.id`)
	if err != nil {
		return nil, fmt.Errorf("list fiscal shift profiles: %w", err)
	}
	defer rows.Close()
	profiles := make([]Profile, 0)
	for rows.Next() {
		var raw []byte
		var profile Profile
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &profile); err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

// ShiftSnapshot freezes the device address and command settings on first use.
// Replays never resolve a newly assigned kiosk profile or edited connection.
func (r *PostgresRepository) ShiftSnapshot(ctx context.Context, scope, key string, profile *Profile) (Profile, bool, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Profile{}, false, err
	}
	defer release()
	digest := sha256.Sum256([]byte(scope + ":" + key))
	hash := hex.EncodeToString(digest[:])
	var raw []byte
	if profile == nil {
		err = conn.QueryRow(ctx, `SELECT profile_snapshot FROM public.fiscal_shift_requests WHERE request_hash=$1 AND scope=$2`, hash, scope).Scan(&raw)
	} else {
		encoded, marshalErr := json.Marshal(profile)
		if marshalErr != nil {
			return Profile{}, false, marshalErr
		}
		err = conn.QueryRow(ctx, `INSERT INTO public.fiscal_shift_requests(request_hash,scope,profile_snapshot)
			VALUES($1,$2,$3::jsonb) ON CONFLICT(request_hash) DO UPDATE SET request_hash=EXCLUDED.request_hash
			RETURNING profile_snapshot`, hash, scope, string(encoded)).Scan(&raw)
	}
	if errors.Is(err, ds.ErrNoRows) {
		return Profile{}, false, nil
	}
	if err != nil {
		return Profile{}, false, fmt.Errorf("persist fiscal shift request: %w", err)
	}
	var saved Profile
	if err := json.Unmarshal(raw, &saved); err != nil {
		return Profile{}, false, fmt.Errorf("decode fiscal shift request snapshot: %w", err)
	}
	return saved, true, nil
}
