package payment

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/dronm/paytochkabank"
	qrcode "github.com/skip2/go-qrcode"
)

// BankError deliberately excludes bank response bodies, URLs and credentials.
// Unknown means the bank may have registered a QR and must not be called again
// automatically for this attempt.
type BankError struct {
	Code    string
	Unknown bool
}

func (e *BankError) Error() string {
	return "bank payment registration failed: " + e.Code
}

// SDKBank uses the uploaded paytochkabank client for real or external mock HTTP.
// Mock profiles are self-contained by default; opting into external mock HTTP
// accepts only a loopback URL and never makes a mock QR into a production pay.
type SDKBank struct {
	MockExternal bool
}

func (b SDKBank) Create(ctx context.Context, request BankRequest) (QRResult, error) {
	if ctx == nil || request.AmountKopecks <= 0 || request.TTLMinutes < 1 || request.TTLMinutes > 129600 || request.StartedAt.IsZero() {
		return QRResult{}, &BankError{Code: "bank_invalid_request"}
	}
	if err := ctx.Err(); err != nil {
		return QRResult{}, &BankError{Code: "bank_request_cancelled"}
	}
	if request.Profile.Environment == "mock" && !b.MockExternal {
		if request.AttemptID == "" || strings.ContainsAny(request.AttemptID, "/?#%\\") || strings.IndexFunc(request.AttemptID, unicode.IsSpace) >= 0 {
			return QRResult{}, &BankError{Code: "bank_invalid_request"}
		}
		return renderQR("mock-"+request.AttemptID, "https://example.invalid/pronogti/mock/"+request.AttemptID, request)
	}
	if !allowedBankEndpoint(request.Profile.Environment, request.Profile.APIBaseURL) || request.Profile.HTTPTimeoutSeconds < 1 || request.Profile.HTTPTimeoutSeconds > 300 {
		return QRResult{}, &BankError{Code: "bank_invalid_configuration"}
	}
	client, err := paytochkabank.NewClient(request.Token,
		paytochkabank.WithBaseURL(request.Profile.APIBaseURL),
		paytochkabank.WithHTTPClient(&http.Client{Timeout: time.Duration(request.Profile.HTTPTimeoutSeconds) * time.Second}),
	)
	if err != nil {
		return QRResult{}, &BankError{Code: "bank_invalid_configuration"}
	}
	result, err := client.SBP.CreateDynamicQR(ctx, paytochkabank.CreateDynamicQRRequest{
		MerchantID: request.Profile.SBPMerchantID, AccountID: request.Profile.AccountID,
		Amount: paytochkabank.Money(request.AmountKopecks), PaymentPurpose: request.Purpose,
		TTLMinutes: request.TTLMinutes, SourceName: "pronogti",
		ImageParams: &paytochkabank.QRImageParams{Width: 512, Height: 512, MediaType: "image/png"},
	})
	if err != nil {
		code := "bank_rejected"
		if paytochkabank.IsOutcomeUnknown(err) {
			code = "bank_outcome_unknown"
		}
		return QRResult{}, &BankError{Code: code, Unknown: paytochkabank.IsOutcomeUnknown(err)}
	}
	if result == nil {
		return QRResult{}, &BankError{Code: "bank_invalid_response", Unknown: true}
	}
	// The bank's image is optional and may be SVG. Generate PNG from the returned
	// payment payload so no bank-provided markup enters the customer UI.
	return renderQR(result.QRCID, result.Payload, request)
}

func renderQR(qrcID, payload string, request BankRequest) (QRResult, error) {
	u, err := url.Parse(payload)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || len(payload) > 2083 ||
		qrcID == "" || len(qrcID) > 128 || strings.ContainsAny(qrcID, "/\\?#%") ||
		strings.IndexFunc(qrcID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return QRResult{}, &BankError{Code: "bank_invalid_response", Unknown: true}
	}
	image, err := qrcode.Encode(payload, qrcode.Medium, 512)
	if err != nil {
		return QRResult{}, &BankError{Code: "bank_qr_render_failed", Unknown: true}
	}
	return QRResult{
		QRCID: qrcID, Payload: payload, Image: image, ImageMediaType: "image/png",
		ExpiresAt: request.StartedAt.Add(time.Duration(request.TTLMinutes) * time.Minute).UTC(),
	}, nil
}

func allowedBankEndpoint(environment, value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	loopback := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	switch environment {
	case "mock":
		return loopback
	case "sandbox":
		return loopback || (u.Scheme == "https" && (u.Host == "enter.tochka.com" || u.Host == "enter.tochka.com:443") && strings.TrimRight(u.Path, "/") == "/sandbox/v2")
	case "production":
		return u.Scheme == "https"
	default:
		return false
	}
}

// Keep compile-time interface checking beside the implementation.
var _ Bank = SDKBank{}
