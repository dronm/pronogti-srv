package httpapi

import "net/http"

// WithTochkaWebhook mounts the bank's independently authenticated callback
// outside the browser API's Redis session, origin and customer-scope middleware.
// The callback handler verifies the signed bank payload before processing it.
func WithTochkaWebhook(customerAPI, webhook http.Handler) http.Handler {
	if webhook == nil {
		return customerAPI
	}
	mux := http.NewServeMux()
	mux.Handle("/payments/tochka/webhook", webhook)
	mux.Handle("/payments/tochka/webhook/{profileID}", webhook)
	mux.Handle("/", customerAPI)
	return mux
}
