package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/dronm/pronogti/internal/fiscal"
	"github.com/dronm/webapp"
)

type AdminKKT interface {
	Read(context.Context, int64, string) (fiscal.KKTResult, error)
	Run(context.Context, int64, string, string, *fiscal.FNRegistration) (fiscal.KKTResult, error)
}

func (h *adminHandler) registerKKT(mux *http.ServeMux) {
	if h.options.KKT == nil {
		return
	}
	const prefix = "/admin/fiscal-profiles/{id}/kkt/"
	for _, action := range []string{fiscal.KKTFormat, fiscal.KKTUnconfirmedCount, fiscal.KKTShiftStatus} {
		mux.HandleFunc("GET "+prefix+action, func(w http.ResponseWriter, r *http.Request) {
			h.kktOperation(w, r, action)
		})
	}
	for _, action := range []string{fiscal.KKTOpenShift, fiscal.KKTCloseShift, fiscal.KKTCloseFN, fiscal.KKTRegisterFN, fiscal.KKTRetry} {
		mux.HandleFunc("POST "+prefix+action, func(w http.ResponseWriter, r *http.Request) {
			h.kktOperation(w, r, action)
		})
	}
}

func (h *adminHandler) kktOperation(w http.ResponseWriter, r *http.Request, action string) {
	write := r.Method == http.MethodPost
	if _, err := administratorIdentity(h.service, r, write); err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		webapp.WriteServiceError(w, webapp.BadRequest("Некорректный идентификатор фискального профиля.", nil))
		return
	}
	var result fiscal.KKTResult
	if write {
		key, keyErr := shiftIdempotencyKey(r)
		if keyErr != nil {
			webapp.WriteServiceError(w, keyErr)
			return
		}
		var registration *fiscal.FNRegistration
		var body any = &struct{}{}
		if action == fiscal.KKTRegisterFN {
			registration = &fiscal.FNRegistration{}
			body = registration
		}
		if err := decodeAdminJSONLimit(w, r, body, 16*1024); err != nil {
			webapp.WriteServiceError(w, err)
			return
		}
		result, err = h.options.KKT.Run(r.Context(), id, action, key, registration)
	} else {
		result, err = h.options.KKT.Read(r.Context(), id, action)
	}
	if err != nil {
		webapp.WriteServiceError(w, err)
		return
	}
	_ = webapp.WriteJSON(w, http.StatusOK, result)
}
