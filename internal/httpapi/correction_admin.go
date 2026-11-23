package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/dronm/pronogti/internal/adminauth"
	"github.com/dronm/pronogti/internal/correction"
	"github.com/dronm/webapp"
	"github.com/google/uuid"
)

type AdminCorrections interface {
	Quote(context.Context, string) (correction.Quote, error)
	Preview(context.Context, string, correction.CreateRequest) (correction.Preview, error)
	Create(context.Context, string, int64, string, correction.CreateRequest) (correction.Correction, error)
	Get(context.Context, string, string) (correction.Correction, error)
	List(context.Context) ([]correction.Correction, error)
	Resume(context.Context, string, string, int64, string) (correction.Correction, error)
}

func (h *adminHandler) registerCorrections(mux *http.ServeMux) {
	if h.options.Corrections == nil {
		return
	}
	mux.HandleFunc("GET /admin/fiscal-corrections", h.correctionList)
	mux.HandleFunc("GET /admin/payment-orders/{id}/corrections", h.correctionQuote)
	mux.HandleFunc("POST /admin/payment-orders/{id}/corrections/preview", h.correctionPreview)
	mux.HandleFunc("POST /admin/payment-orders/{id}/corrections", h.correctionCreate)
	mux.HandleFunc("GET /admin/payment-orders/{id}/corrections/{correction_id}", h.correctionGet)
	mux.HandleFunc("POST /admin/payment-orders/{id}/corrections/{correction_id}/resume", h.correctionResume)
}

func (h *adminHandler) correctionList(w http.ResponseWriter, r *http.Request) {
	if _, err := administratorIdentity(h.service, r, false); err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	result, err := h.options.Corrections.List(r.Context())
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	_ = webapp.WriteJSON(w, http.StatusOK, result)
}

func (h *adminHandler) correctionQuote(w http.ResponseWriter, r *http.Request) {
	_, orderID, ok := h.correctionOrder(w, r, false)
	if !ok {
		return
	}
	result, err := h.options.Corrections.Quote(r.Context(), orderID)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	_ = webapp.WriteJSON(w, http.StatusOK, result)
}

func (h *adminHandler) correctionPreview(w http.ResponseWriter, r *http.Request) {
	_, orderID, ok := h.correctionOrder(w, r, true)
	if !ok {
		return
	}
	var request correction.CreateRequest
	if err := decodeAdminJSONLimit(w, r, &request, 256<<10); err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	result, err := h.options.Corrections.Preview(r.Context(), orderID, request)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	_ = webapp.WriteJSON(w, http.StatusOK, result)
}

func (h *adminHandler) correctionCreate(w http.ResponseWriter, r *http.Request) {
	operator, orderID, ok := h.correctionOrder(w, r, true)
	if !ok {
		return
	}
	key, err := correctionKey(r)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	var request correction.CreateRequest
	if err := decodeAdminJSONLimit(w, r, &request, 256<<10); err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	result, err := h.options.Corrections.Create(r.Context(), orderID, operator.ID, key, request)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	w.Header().Set("Location", "corrections/"+result.ID)
	_ = webapp.WriteJSON(w, http.StatusAccepted, result)
}

func (h *adminHandler) correctionGet(w http.ResponseWriter, r *http.Request) {
	_, orderID, ok := h.correctionOrder(w, r, false)
	if !ok {
		return
	}
	correctionID, err := correctionPathID(r, "correction_id")
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	result, err := h.options.Corrections.Get(r.Context(), orderID, correctionID)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	_ = webapp.WriteJSON(w, http.StatusOK, result)
}

func (h *adminHandler) correctionResume(w http.ResponseWriter, r *http.Request) {
	operator, orderID, ok := h.correctionOrder(w, r, true)
	if !ok {
		return
	}
	correctionID, err := correctionPathID(r, "correction_id")
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	key, err := correctionKey(r)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	var body struct{}
	if err := decodeAdminJSON(w, r, &body); err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	result, err := h.options.Corrections.Resume(r.Context(), orderID, correctionID, operator.ID, key)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	_ = webapp.WriteJSON(w, http.StatusAccepted, result)
}

func (h *adminHandler) correctionOrder(w http.ResponseWriter, r *http.Request, mutate bool) (adminauth.User, string, bool) {
	operator, err := administratorIdentity(h.service, r, mutate)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return adminauth.User{}, "", false
	}
	orderID, err := correctionPathID(r, "id")
	if err != nil {
		webapp.WriteServiceError(w, err)
		return adminauth.User{}, "", false
	}
	return operator, orderID, true
}

func correctionPathID(r *http.Request, field string) (string, error) {
	value := r.PathValue(field)
	id, err := uuid.Parse(value)
	if len(value) != 36 || err != nil || id == uuid.Nil {
		return "", webapp.BadRequest("Некорректный идентификатор чека или коррекции.", map[string]any{"field": field})
	}
	return id.String(), nil
}

func correctionKey(r *http.Request) (string, error) {
	keys := r.Header.Values(refundIdempotencyHeader)
	if len(keys) != 1 || strings.TrimSpace(keys[0]) == "" || len(keys[0]) > 128 {
		return "", webapp.BadRequest("Требуется один ключ запроса Idempotency-Key длиной до 128 символов.", nil)
	}
	return keys[0], nil
}
