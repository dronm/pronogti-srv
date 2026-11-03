package payment

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/dronm/paytochkabank"
)

var bankOperationPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// SDKCardBank creates one-stage, card-only links hosted by the bank. It never
// accepts card details or asks the bank to issue a receipt: Terminal FA owns it.
type SDKCardBank struct{ MockExternal bool }

func (b SDKCardBank) CreateCard(ctx context.Context, request BankRequest) (CardResult, error) {
	if ctx == nil || !uuidPattern.MatchString(request.AttemptID) || !uuidPattern.MatchString(request.OrderID) || request.AmountKopecks <= 0 || request.TTLMinutes < 1 || request.TTLMinutes > 44640 || request.StartedAt.IsZero() {
		return CardResult{}, &BankError{Code: "bank_invalid_request"}
	}
	if ctx.Err() != nil {
		return CardResult{}, &BankError{Code: "bank_request_cancelled"}
	}
	if !request.Profile.CardsEnabled {
		return CardResult{}, &BankError{Code: "card_payment_unavailable"}
	}
	expires := request.StartedAt.Add(time.Duration(request.TTLMinutes) * time.Minute).UTC()
	if request.Profile.Environment == "mock" && !b.MockExternal {
		return CardResult{OperationID: "mock-card-" + request.AttemptID, CheckoutURL: "https://example.invalid/pronogti/mock-card/" + request.AttemptID, Status: "CREATED", ExpiresAt: expires}, nil
	}
	if !allowedBankEndpoint(request.Profile.Environment, request.Profile.APIBaseURL) || request.Profile.HTTPTimeoutSeconds < 1 || request.Profile.HTTPTimeoutSeconds > 300 || request.Profile.AcquiringMerchantID == "" {
		return CardResult{}, &BankError{Code: "bank_invalid_configuration"}
	}
	redirect, err := cardReturnURL(request.Profile.CardRedirectURL, request)
	if err != nil {
		return CardResult{}, err
	}
	failureRedirect, err := cardReturnURL(request.Profile.CardFailRedirectURL, request)
	if err != nil {
		return CardResult{}, err
	}
	client, err := paytochkabank.NewClient(request.Token, paytochkabank.WithBaseURL(request.Profile.APIBaseURL),
		paytochkabank.WithHTTPClient(&http.Client{
			Timeout:       time.Duration(request.Profile.HTTPTimeoutSeconds) * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}))
	if err != nil {
		return CardResult{}, &BankError{Code: "bank_invalid_configuration"}
	}
	result, err := client.Acquiring.CreateCardCheckout(ctx, paytochkabank.CreateCardCheckoutRequest{
		CustomerCode: request.Profile.CustomerCode, MerchantID: request.Profile.AcquiringMerchantID,
		Amount: paytochkabank.Money(request.AmountKopecks), Purpose: request.Purpose,
		TTLMinutes: request.TTLMinutes, PaymentLinkID: request.AttemptID,
		RedirectURL: redirect, FailRedirectURL: failureRedirect,
	})
	if err != nil {
		unknown := paytochkabank.IsOutcomeUnknown(err)
		code := "bank_rejected"
		if unknown {
			code = "bank_outcome_unknown"
		}
		return CardResult{}, &BankError{Code: code, Unknown: unknown}
	}
	if result == nil || result.Amount != paytochkabank.Money(request.AmountKopecks) || result.Purpose != request.Purpose ||
		result.MerchantID != "" && result.MerchantID != request.Profile.AcquiringMerchantID ||
		result.PaymentLinkID != "" && result.PaymentLinkID != request.AttemptID {
		return CardResult{}, &BankError{Code: "bank_invalid_response", Unknown: true}
	}
	for _, mode := range result.PaymentModes {
		if mode != "card" {
			return CardResult{}, &BankError{Code: "bank_invalid_response", Unknown: true}
		}
	}
	status := string(result.Status)
	if status == "" {
		status = "CREATED"
	}
	card := CardResult{OperationID: result.OperationID, CheckoutURL: result.PaymentLink, Status: status, ExpiresAt: expires}
	if !validCardResult(card, request.StartedAt, &expires) {
		return CardResult{}, &BankError{Code: "bank_invalid_response", Unknown: true}
	}
	return card, nil
}

// Return destinations are deployment settings, never browser request fields.
// The immutable IDs restore the existing attempt; a redirect is not payment proof.
func cardReturnURL(value string, request BankRequest) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || len(value) > 1900 {
		return "", &BankError{Code: "bank_invalid_configuration"}
	}
	loopback := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback && request.Profile.Environment != "production") {
		return "", &BankError{Code: "bank_invalid_configuration"}
	}
	query := u.Query()
	query.Set("checkout_id", request.OrderID)
	query.Set("payment_id", request.AttemptID)
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func validCardResult(result CardResult, now time.Time, limit *time.Time) bool {
	u, err := url.Parse(result.CheckoutURL)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil &&
		u.Opaque == "" && len(result.CheckoutURL) <= 2083 && bankOperationPattern.MatchString(result.OperationID) &&
		strings.TrimSpace(result.Status) != "" && result.ExpiresAt.After(now) && limit != nil && !result.ExpiresAt.After(*limit)
}

var _ CardBank = SDKCardBank{}
