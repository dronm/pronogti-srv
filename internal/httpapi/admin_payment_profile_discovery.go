package httpapi

import (
	"net/http"

	"github.com/dronm/pronogti/internal/services"
	"github.com/dronm/webapp"
)

const paymentProfileDiscoveryRoute = "paymentProfile.discover"

func registerPaymentProfileDiscoveryRoutes(api *webapp.Group) {
	api.POST("/admin/payment-profiles/{id}/discover",
		webapp.WithName(paymentProfileDiscoveryRoute),
		webapp.WithPermission("paymentProfile.update"),
		webapp.WithService("PaymentProfile", "Discover"),
		webapp.WithBinder(bindPaymentProfileDiscovery),
	)
}

func bindPaymentProfileDiscovery(r *http.Request) (any, error) {
	id, err := paymentProfileCredentialID(r)
	if err != nil {
		return nil, err
	}
	var body struct {
		CustomerCode string `json:"customer_code"`
	}
	// This binder only parses the request; the router writes the binding error.
	if err := decodeAdminJSONLimit(nil, r, &body, 1024); err != nil {
		return nil, err
	}
	return services.PaymentProfileDiscoveryInput{ID: id, CustomerCode: body.CustomerCode}, nil
}
