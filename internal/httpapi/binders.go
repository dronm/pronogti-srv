package httpapi

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/dronm/modelbind"
	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/webapp"
)

func positivePathID(r *http.Request, name string) (int64, error) {
	value, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || value <= 0 {
		return 0, webapp.BadRequest(name+" should be a positive integer", nil)
	}
	return value, nil
}

func optionalQueryID(r *http.Request, name string) (*int64, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, webapp.BadRequest(name+" should be an integer", nil)
	}
	return &parsed, nil
}

func companyBinder(r *http.Request) (any, error) {
	companyID, err := positivePathID(r, "companyID")
	return companyRequest{CompanyID: companyID}, err
}

func findClientBinder(r *http.Request) (any, error) {
	companyID, err := positivePathID(r, "companyID")
	if err != nil {
		return nil, err
	}
	// Preserve the phone normalization used by the old controller.
	phone := strings.Replace(r.PathValue("tel"), "+", "", 1)
	phone = strings.Replace(phone, "(", "", 1)
	phone = strings.Replace(phone, ")", "", 1)
	phone = strings.ReplaceAll(phone, "-", "")
	return findClientRequest{CompanyID: companyID, Phone: phone}, nil
}

func payDraftBinder(r *http.Request) (any, error) {
	companyID, err := positivePathID(r, "companyID")
	if err != nil {
		return nil, err
	}
	clientID, err := positivePathID(r, "clientID")
	return payDraftRequest{CompanyID: companyID, ClientID: clientID}, err
}

func clientGetBinder(r *http.Request) (any, error) {
	// The literal find segment takes precedence, including find/pay-draft.
	if r.PathValue("clientID") == "find" {
		r.SetPathValue("tel", r.PathValue("action"))
		bound, err := findClientBinder(r)
		if err != nil {
			return nil, err
		}
		input := bound.(findClientRequest)
		return clientGetRequest{
			Action: clientGetFind, CompanyID: input.CompanyID, Phone: input.Phone,
		}, nil
	}
	if r.PathValue("action") == "pay-draft" {
		bound, err := payDraftBinder(r)
		if err != nil {
			return nil, err
		}
		input := bound.(payDraftRequest)
		return clientGetRequest{
			Action: clientGetPayDraft, CompanyID: input.CompanyID, ClientID: input.ClientID,
		}, nil
	}
	return nil, webapp.NotFound("client endpoint not found", nil)
}

func serviceListBinder(r *http.Request) (any, error) {
	companyID, err := positivePathID(r, "companyID")
	if err != nil {
		return nil, err
	}
	staffID, err := optionalQueryID(r, "staff")
	if err != nil {
		return nil, err
	}
	categoryID, err := optionalQueryID(r, "cat")
	return serviceListRequest{
		CompanyID: companyID, StaffID: staffID, CategoryID: categoryID,
		Term: r.URL.Query().Get("term"),
	}, err
}

func serviceCategoriesBinder(r *http.Request) (any, error) {
	companyID, err := positivePathID(r, "companyID")
	if err != nil {
		return nil, err
	}
	staffID, err := optionalQueryID(r, "staff")
	return serviceCategoriesRequest{CompanyID: companyID, StaffID: staffID}, err
}

func goodsListBinder(r *http.Request) (any, error) {
	companyID, err := positivePathID(r, "companyID")
	return goodsListRequest{CompanyID: companyID, Term: r.URL.Query().Get("term")}, err
}

// A string date on the wire accepts both date-only form values and the RFC3339
// timestamps accepted by the existing JSON model, without changing domain types.
type newClientBody struct {
	Name      *string `json:"name"`
	Phone     string  `json:"phone" required:"true" min:"1"`
	Email     *string `json:"email"`
	BirthDate *string `json:"birth_date"`
}

func decodeBody[T any](r *http.Request) (T, error) {
	// modelbind treats an empty JSON document as an empty model. The previous
	// JSON binder rejected it, including on endpoints with optional fields only.
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType == "application/json" || mediaType == "text/json" {
		if r.Body == nil {
			var empty T
			return empty, webapp.BadRequest("request body is required", nil)
		}
		const maxBodyBytes = 1 << 20
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
		if err != nil {
			var empty T
			return empty, webapp.BadRequest("read request body", nil)
		}
		if len(bytes.TrimSpace(body)) == 0 {
			var empty T
			return empty, webapp.BadRequest("request body is required", nil)
		}
		if len(body) > maxBodyBytes {
			var empty T
			return empty, webapp.BadRequest("request body exceeds 1 MiB", nil)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	input, err := modelbind.DecodeRequestInput[T](r)
	if err != nil {
		var empty T
		return empty, err
	}
	if err := input.Validate(true); err != nil {
		var empty T
		return empty, err
	}
	return input.Model, nil
}

func registerClientBinder(r *http.Request) (any, error) {
	companyID, err := positivePathID(r, "companyID")
	if err != nil {
		return nil, err
	}
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
	return registerClientRequest{CompanyID: companyID, Client: client}, nil
}

func parseBirthDate(value string) (time.Time, error) {
	date, err := time.Parse("2006-01-02", value)
	if err == nil {
		return date, nil
	}
	return time.Parse(time.RFC3339Nano, value)
}
