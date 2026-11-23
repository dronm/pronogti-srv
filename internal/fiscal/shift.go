package fiscal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dronm/webapp"
)

// ShiftResult is the operator-visible outcome. Unknown results must be retried
// with the same idempotency key so the driver's durable journal is consulted.
type ShiftResult struct {
	State          string          `json:"state"`
	Message        string          `json:"message"`
	ShiftNumber    *uint16         `json:"shift_number,omitempty"`
	RequestID      string          `json:"request_id"`
	DriverResponse json.RawMessage `json:"driver_response,omitempty"`
}

type ShiftProfiles interface {
	ShiftProfile(context.Context, int64) (Profile, error)
	KioskShiftProfile(context.Context, int64, int64) (Profile, error)
	LiveShiftProfiles(context.Context) ([]Profile, error)
	ShiftSnapshot(context.Context, string, string, *Profile) (Profile, bool, error)
}

type ShiftDriver interface {
	CloseShift(context.Context, DriverConfig, string, json.RawMessage) (ShiftResult, error)
}

type ShiftService struct {
	profiles  ShiftProfiles
	driver    ShiftDriver
	allowLive bool
}

func NewShiftService(profiles ShiftProfiles, driver ShiftDriver, allowLive bool) *ShiftService {
	return &ShiftService{profiles: profiles, driver: driver, allowLive: allowLive}
}

func (s *ShiftService) CloseProfile(ctx context.Context, id int64, key string) (ShiftResult, error) {
	if id <= 0 {
		return ShiftResult{}, webapp.BadRequest("invalid fiscal profile ID", nil)
	}
	return s.resolveClose(ctx, fmt.Sprintf("profile:%d", id), key, func() (Profile, error) { return s.profiles.ShiftProfile(ctx, id) })
}

func (s *ShiftService) CloseKiosk(ctx context.Context, kioskID, companyID int64, key string) (ShiftResult, error) {
	return s.resolveClose(ctx, fmt.Sprintf("kiosk:%d:%d", companyID, kioskID), key, func() (Profile, error) { return s.profiles.KioskShiftProfile(ctx, kioskID, companyID) })
}

func (s *ShiftService) resolveClose(ctx context.Context, scope, key string, load func() (Profile, error)) (ShiftResult, error) {
	if !ValidShiftKey(key) {
		return ShiftResult{}, webapp.BadRequest("a single Idempotency-Key of up to 128 bytes is required", nil)
	}
	profile, found, err := s.profiles.ShiftSnapshot(ctx, scope, key, nil)
	if err != nil {
		return ShiftResult{}, err
	}
	if !found {
		profile, err = load()
		if err != nil {
			return ShiftResult{}, err
		}
		if _, err = prepareKKTProfile(profile, "shift-preflight", s.allowLive); err != nil {
			return ShiftResult{}, err
		}
		profile, _, err = s.profiles.ShiftSnapshot(ctx, scope, key, &profile)
		if err != nil {
			return ShiftResult{}, err
		}
	}
	return s.closeSnapshot(ctx, profile, scope, key)
}

func (s *ShiftService) Close(ctx context.Context, profile Profile, key string) (ShiftResult, error) {
	if profile.ID == nil {
		return ShiftResult{}, errors.New("fiscal profile identity is missing")
	}
	return s.resolveClose(ctx, fmt.Sprintf("profile:%d", *profile.ID), key, func() (Profile, error) { return profile, nil })
}

func (s *ShiftService) closeSnapshot(ctx context.Context, profile Profile, scope, key string) (ShiftResult, error) {
	if !ValidShiftKey(key) {
		return ShiftResult{}, webapp.BadRequest("a single Idempotency-Key of up to 128 bytes is required", nil)
	}
	digest := sha256.Sum256([]byte(scope + ":" + key))
	requestID := "pronogti-close-" + hex.EncodeToString(digest[:])
	cfg, err := prepareKKTProfile(profile, requestID, s.allowLive)
	if err != nil {
		return ShiftResult{}, err
	}
	if profile.Mode == "demo" {
		return ShiftResult{State: "demo", Message: "Тестовый режим: команда закрытия смены в ККТ не отправлялась.", RequestID: requestID}, nil
	}
	params := map[string]any{"cashierName": profile.CashierName, "suppressPrint": 0}
	if profile.SuppressPrint {
		params["suppressPrint"] = 1
	}
	if profile.CashierINN != "" {
		params["cashierInn"] = profile.CashierINN
	}
	payload, err := json.Marshal(params)
	if err != nil {
		return ShiftResult{}, err
	}
	return s.driver.CloseShift(ctx, cfg, requestID, payload)
}

func prepareKKTProfile(profile Profile, requestID string, allowLive bool) (DriverConfig, error) {
	if !profile.Enabled {
		return DriverConfig{}, webapp.Conflict("Фискальный профиль отключён.", nil)
	}
	if profile.ID == nil || *profile.ID <= 0 {
		return DriverConfig{}, errors.New("fiscal profile identity is missing")
	}
	if profile.Mode == "demo" {
		return DriverConfig{}, nil
	}
	if !allowLive {
		return DriverConfig{}, webapp.Conflict("Операции с ККТ запрещены. Включите fiscal.allow_live в конфигурации сервера.", nil)
	}
	if err := profile.Validate(); err != nil {
		return DriverConfig{}, webapp.Conflict("Некорректный фискальный профиль: "+err.Error(), nil)
	}
	token := ""
	if profile.BearerTokenEnv != "" {
		token = os.Getenv(profile.BearerTokenEnv)
		if token == "" {
			return DriverConfig{}, webapp.Conflict("Недоступен токен подключения к ККТ.", nil)
		}
	}
	cfg := DriverConfig{BaseURL: profile.DriverBaseURL, DeviceID: profile.DeviceID, Token: token, Timeout: time.Duration(profile.TimeoutSeconds) * time.Second}
	if _, err := validateDriverConfig(cfg, requestID); err != nil {
		return DriverConfig{}, webapp.Conflict("Некорректное подключение к ККТ.", nil)
	}
	return cfg, nil
}

func ValidShiftKey(key string) bool {
	return strings.TrimSpace(key) != "" && len(key) <= 128 && !strings.ContainsAny(key, "\r\n")
}

// CloseAll selects enabled live profiles only and contacts each physical driver
// device once even if several profiles point at it. Every invocation uses a new
// key; HTTP retries within an invocation use its original key.
func (s *ShiftService) CloseAll(ctx context.Context, key string, emit func(Profile, ShiftResult, error)) error {
	if !s.allowLive {
		return errors.New("shift closing requires fiscal.allow_live=true")
	}
	profiles, err := s.profiles.LiveShiftProfiles(ctx)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	var failures []error
	for _, profile := range profiles {
		device := strings.TrimRight(profile.DriverBaseURL, "/") + "\x00" + profile.DeviceID
		if seen[device] {
			continue
		}
		seen[device] = true
		result, err := s.Close(ctx, profile, key)
		if emit != nil {
			emit(profile, result, err)
		}
		if err != nil {
			failures = append(failures, err)
		} else if result.State == "failed" || result.State == "unknown" {
			failures = append(failures, errors.New(result.Message))
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return errors.Join(failures...)
}
