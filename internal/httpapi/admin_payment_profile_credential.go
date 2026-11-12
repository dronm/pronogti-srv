package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/dronm/pronogti/internal/payment"
	"github.com/dronm/pronogti/internal/services"
	"github.com/dronm/webapp"
)

const paymentProfileCredentialWriteRoute = "paymentProfile.credential.set"
const maxPaymentProfileCredentialBody = 16 << 10
const maxPaymentProfileCredentialJWT = 8192

var paymentProfileCredentialTokens atomic.Pointer[payment.EnvironmentTokens]

// ConfigurePaymentProfileCredentials supplies the token store before the
// administrator router is built. Each router captures its own store.
func ConfigurePaymentProfileCredentials(tokens *payment.EnvironmentTokens) {
	paymentProfileCredentialTokens.Store(tokens)
}

func paymentProfileCredentialMiddleware(tokens *payment.EnvironmentTokens) webapp.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(services.WithPaymentProfileTokens(r.Context(), tokens)))
		})
	}
}

func registerPaymentProfileCredentialRoutes(api *webapp.Group) {
	const path = "/admin/payment-profiles/{id}/credential"
	api.GET(path,
		webapp.WithName("paymentProfile.credential.status"),
		webapp.WithPermission("paymentProfile.detail"),
		webapp.WithService("PaymentProfile", "CredentialStatus"),
		webapp.WithBinder(func(r *http.Request) (any, error) {
			return paymentProfileCredentialID(r)
		}),
	)
	api.POST(path,
		webapp.WithName(paymentProfileCredentialWriteRoute),
		webapp.WithPermission("paymentProfile.update"),
		webapp.WithService("PaymentProfile", "SetCredential"),
		webapp.WithBinder(bindPaymentProfileCredential),
	)
}

func paymentProfileCredentialID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, webapp.BadRequest("invalid payment profile ID", nil)
	}
	return id, nil
}

func bindPaymentProfileCredential(r *http.Request) (any, error) {
	id, err := paymentProfileCredentialID(r)
	if err != nil {
		return nil, err
	}
	invalid := webapp.BadRequest("expected application/json with one nonempty jwt field", nil)
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || r.Body == nil {
		return nil, invalid
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxPaymentProfileCredentialBody+1))
	if err != nil || len(data) > maxPaymentProfileCredentialBody {
		return nil, invalid
	}
	defer clear(data)
	decoder := json.NewDecoder(bytes.NewReader(data))
	if open, err := decoder.Token(); err != nil || open != json.Delim('{') {
		return nil, invalid
	}
	if key, err := decoder.Token(); err != nil || key != "jwt" {
		return nil, invalid
	}
	var jwt string
	if err := decoder.Decode(&jwt); err != nil || len(jwt) == 0 || len(jwt) > maxPaymentProfileCredentialJWT {
		return nil, invalid
	}
	if close, err := decoder.Token(); err != nil || close != json.Delim('}') {
		return nil, invalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, invalid
	}
	return services.SetPaymentProfileCredentialInput{ID: id, JWT: jwt}, nil
}
