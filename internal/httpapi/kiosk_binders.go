package httpapi

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"net/mail"

	"github.com/dronm/pronogti/internal/kiosk"
	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/pronogti/internal/salon"
	"github.com/dronm/webapp"
)

type bindKioskRequest struct {
	Token string `json:"token"`
}
type startCustomerRequest struct {
	Phone string `json:"phone"`
	Guest bool   `json:"guest"`
}
type selectSalonRequest struct{ Slug string }

func selectSalonBinder(r *http.Request) (any, error) {
	if err := requireSessionJSON(r); err != nil {
		return nil, err
	}
	slug := r.PathValue("slug")
	if !salon.ValidSlug(slug) {
		return nil, webapp.BadRequest("salon slug should contain lowercase letters, digits, and single hyphens", nil)
	}
	body, err := decodeBody[*struct{}](r)
	if err != nil {
		return nil, err
	}
	if body == nil {
		return nil, webapp.BadRequest("salon selection requires a JSON object", nil)
	}
	return selectSalonRequest{Slug: slug}, nil
}

type customerRequest struct{ Nonce string }
type kioskRegisterRequest struct {
	Nonce  string
	Client models.NewClient
}
type kioskQRRequest struct {
	Nonce          string
	CheckoutID     string
	IdempotencyKey string
}

func bindKioskBinder(r *http.Request) (any, error) {
	if err := requireSessionJSON(r); err != nil {
		return nil, err
	}
	body, err := decodeBody[*bindKioskRequest](r)
	if err != nil {
		return nil, err
	}
	return *body, nil
}
func startCustomerBinder(r *http.Request) (any, error) {
	if err := requireSessionJSON(r); err != nil {
		return nil, err
	}
	body, err := decodeBody[*startCustomerRequest](r)
	if err != nil {
		return nil, err
	}
	return *body, nil
}

func requireSessionJSON(r *http.Request) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return webapp.BadRequest("session requests require application/json", nil)
	}
	if r.Body == nil {
		return webapp.BadRequest("session requests require a JSON object", nil)
	}
	const maxBodyBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		return webapp.BadRequest("read session request body", nil)
	}
	if len(body) > maxBodyBytes {
		return webapp.BadRequest("request body exceeds 1 MiB", nil)
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return webapp.BadRequest("session requests require a JSON object", nil)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return nil
}
func customerBinder(r *http.Request) (any, error) {
	return customerRequest{Nonce: r.Header.Get(kiosk.CustomerHeader)}, nil
}
func kioskServicesBinder(r *http.Request) (any, error) {
	staff, err := optionalQueryID(r, "staff")
	if err != nil {
		return nil, err
	}
	cat, err := optionalQueryID(r, "cat")
	return serviceListRequest{StaffID: staff, CategoryID: cat, Term: r.URL.Query().Get("term")}, err
}
func kioskCategoriesBinder(r *http.Request) (any, error) {
	staff, err := optionalQueryID(r, "staff")
	return serviceCategoriesRequest{StaffID: staff}, err
}
func kioskGoodsBinder(r *http.Request) (any, error) {
	return goodsListRequest{Term: r.URL.Query().Get("term")}, nil
}
func kioskRegisterBinder(r *http.Request) (any, error) {
	body, err := decodeBody[*newClientBody](r)
	if err != nil {
		return nil, err
	}
	if body.Email != nil && *body.Email != "" {
		address, err := mail.ParseAddress(*body.Email)
		if err != nil || address.Address != *body.Email {
			return nil, webapp.BadRequest("email should be a valid email address", nil)
		}
	}
	client := models.NewClient{Name: body.Name, Phone: body.Phone, Email: body.Email}
	if body.BirthDate != nil && *body.BirthDate != "" {
		date, err := parseBirthDate(*body.BirthDate)
		if err != nil {
			return nil, webapp.BadRequest("birth_date should be YYYY-MM-DD or an RFC3339 timestamp", nil)
		}
		client.BirthDate = &date
	}
	return kioskRegisterRequest{Nonce: r.Header.Get(kiosk.CustomerHeader), Client: client}, nil
}
func kioskQRBinder(r *http.Request) (any, error) {
	body, err := decodeCheckoutBody[struct {
		CheckoutID string `json:"checkout_id"`
	}](r)
	if err != nil {
		return nil, err
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return nil, webapp.BadRequest("Idempotency-Key is required for payment creation", nil)
	}
	return kioskQRRequest{Nonce: r.Header.Get(kiosk.CustomerHeader), CheckoutID: body.CheckoutID, IdempotencyKey: key}, nil
}
