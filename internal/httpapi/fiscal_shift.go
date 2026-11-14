package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strconv"

	"github.com/dronm/pronogti/internal/fiscal"
	"github.com/dronm/webapp"
)

type FiscalShifts interface {
	CloseProfile(context.Context, int64, string) (fiscal.ShiftResult, error)
	CloseKiosk(context.Context, int64, int64, string) (fiscal.ShiftResult, error)
}

type ShiftDependencies struct {
	Service       FiscalShifts
	OperatorToken string
}

type closeShiftInput struct {
	OperatorToken  string `json:"operator_token"`
	IdempotencyKey string `json:"-"`
}

func shiftIdempotencyKey(r *http.Request) (string, error) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || !fiscal.ValidShiftKey(values[0]) {
		return "", webapp.BadRequest("a single Idempotency-Key of up to 128 bytes is required", nil)
	}
	return values[0], nil
}

func closeKioskShiftBinder(r *http.Request) (any, error) {
	input, err := decodeCheckoutBody[closeShiftInput](r)
	if err != nil {
		return nil, err
	}
	input.IdempotencyKey, err = shiftIdempotencyKey(r)
	return input, err
}

func (s *service) CloseKioskShift(ctx context.Context, input closeShiftInput) (fiscal.ShiftResult, error) {
	if !s.kioskOnly {
		return fiscal.ShiftResult{}, webapp.Forbidden("Требуется привязанный киоск.", nil)
	}
	scope, err := s.requireScope(ctx)
	if err != nil {
		return fiscal.ShiftResult{}, err
	}
	if scope.Kiosk == nil {
		return fiscal.ShiftResult{}, webapp.Forbidden("Требуется привязанный киоск.", nil)
	}
	if s.checkout == nil || s.checkout.Shifts == nil || s.checkout.Shifts.Service == nil || s.checkout.Shifts.OperatorToken == "" {
		return fiscal.ShiftResult{}, webapp.Forbidden("Закрытие смены с киоска не настроено.", nil)
	}
	expected, supplied := sha256.Sum256([]byte(s.checkout.Shifts.OperatorToken)), sha256.Sum256([]byte(input.OperatorToken))
	if subtle.ConstantTimeCompare(expected[:], supplied[:]) != 1 {
		return fiscal.ShiftResult{}, webapp.Forbidden("Неверный код оператора.", nil)
	}
	return s.checkout.Shifts.Service.CloseKiosk(ctx, scope.Kiosk.ID, scope.CompanyID, input.IdempotencyKey)
}

func (h *adminHandler) registerFiscalShifts(mux *http.ServeMux) {
	if h.options.Shifts == nil {
		return
	}
	mux.HandleFunc("POST /admin/fiscal-profiles/{id}/close-shift", h.closeFiscalShift)
}

func (h *adminHandler) closeFiscalShift(w http.ResponseWriter, r *http.Request) {
	if _, err := administratorIdentity(h.service, r, true); err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		webapp.WriteServiceError(w, webapp.BadRequest("invalid fiscal profile ID", nil))
		return
	}
	key, err := shiftIdempotencyKey(r)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	var body struct{}
	if err := decodeAdminJSON(w, r, &body); err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	result, err := h.options.Shifts.CloseProfile(r.Context(), id, key)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	_ = webapp.WriteJSON(w, http.StatusOK, result)
}
