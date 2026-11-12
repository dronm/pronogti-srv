package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/dronm/pronogti/internal/adminauth"
	"github.com/dronm/pronogti/internal/refund"
	"github.com/dronm/webapp"
	"github.com/google/uuid"
)

const refundIdempotencyHeader = "Idempotency-Key"

type AdminRefunds interface {
	Quote(context.Context, string) (refund.Quote, error)
	Create(context.Context, string, int64, string, refund.CreateRequest) (refund.Refund, error)
	Get(context.Context, string, string) (refund.Refund, error)
}

func (h *adminHandler) registerRefunds(mux *http.ServeMux) {
	if h.options.Refunds == nil {
		return
	}
	mux.HandleFunc("GET /admin/payments/{id}/refunds", h.refundQuote)
	mux.HandleFunc("POST /admin/payments/{id}/refunds", h.refundCreate)
	mux.HandleFunc("GET /admin/payments/{id}/refunds/{refund_id}", h.refundGet)
}

func (h *adminHandler) refundQuote(w http.ResponseWriter, r *http.Request) {
	_, paymentID, ok := h.refundPayment(w, r, false)
	if !ok {
		return
	}
	value, err := h.options.Refunds.Quote(r.Context(), paymentID)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	_ = webapp.WriteJSON(w, http.StatusOK, value)
}

func (h *adminHandler) refundCreate(w http.ResponseWriter, r *http.Request) {
	operator, paymentID, ok := h.refundPayment(w, r, true)
	if !ok {
		return
	}
	keys := r.Header.Values(refundIdempotencyHeader)
	if len(keys) != 1 || strings.TrimSpace(keys[0]) == "" || len(keys[0]) > 128 {
		webapp.WriteServiceError(w, webapp.BadRequest("a single Idempotency-Key of up to 128 bytes is required", nil))
		return
	}
	var body struct {
		Items []struct {
			LineIndex *int   `json:"line_index"`
			Quantity  string `json:"quantity"`
		} `json:"items"`
	}
	if err := decodeAdminJSONLimit(w, r, &body, 32<<10); err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	if len(body.Items) == 0 || len(body.Items) > 200 {
		webapp.WriteServiceError(w, webapp.BadRequest("select between 1 and 200 refund items", nil))
		return
	}
	request := refund.CreateRequest{Items: make([]refund.Selection, len(body.Items))}
	for index, item := range body.Items {
		if item.LineIndex == nil || item.Quantity == "" {
			webapp.WriteServiceError(w, webapp.BadRequest("refund items require line_index and quantity", map[string]any{"index": index}))
			return
		}
		request.Items[index] = refund.Selection{LineIndex: *item.LineIndex, Quantity: item.Quantity}
	}
	value, err := h.options.Refunds.Create(r.Context(), paymentID, operator.ID, keys[0], request)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	// A relative location preserves an API prefix added by a reverse proxy.
	w.Header().Set("Location", "refunds/"+value.ID)
	_ = webapp.WriteJSON(w, http.StatusAccepted, value)
}

func (h *adminHandler) refundGet(w http.ResponseWriter, r *http.Request) {
	_, paymentID, ok := h.refundPayment(w, r, false)
	if !ok {
		return
	}
	refundID, err := refundPathID(r, "refund_id")
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	value, err := h.options.Refunds.Get(r.Context(), paymentID, refundID)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	_ = webapp.WriteJSON(w, http.StatusOK, value)
}

func (h *adminHandler) refundPayment(w http.ResponseWriter, r *http.Request, mutate bool) (adminauth.User, string, bool) {
	operator, err := administratorIdentity(h.service, r, mutate)
	if err != nil {
		webapp.WriteServiceError(w, err)
		return adminauth.User{}, "", false
	}
	paymentID, err := refundPathID(r, "id")
	if err != nil {
		webapp.WriteServiceError(w, err)
		return adminauth.User{}, "", false
	}
	return operator, paymentID, true
}

func refundPathID(r *http.Request, field string) (string, error) {
	value := r.PathValue(field)
	id, err := uuid.Parse(value)
	if len(value) != 36 || err != nil || id == uuid.Nil {
		return "", webapp.BadRequest("invalid refund route identifier", map[string]any{"field": field})
	}
	return id.String(), nil
}
