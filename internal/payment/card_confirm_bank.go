package payment

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dronm/paytochkabank"
)

// CardObservation records a read of the original acquiring operation. It never
// represents a browser redirect or the creation of a bank-hosted payment link.
type CardObservation struct {
	OperationID, Status, CustomerCode, MerchantID string
	AmountKopecks                                 int64
	Paid                                          bool
	PaymentType, PaymentID, TransactionID         string
	CheckoutURL                                   string
}

type CardChecker interface {
	Check(context.Context, ConfirmationCandidate, string) (CardObservation, error)
}

type SDKCardChecker struct{ MockExternal bool }

// Check performs only an authenticated GET. A missing operation ID after a lost
// creation response remains unknown until a verified callback recovers it.
func (c SDKCardChecker) Check(ctx context.Context, candidate ConfirmationCandidate, token string) (CardObservation, error) {
	a, p := candidate.Attempt, candidate.Profile
	if ctx == nil || !validCardConfirmationCandidate(candidate) || a.OperationID == "" {
		return CardObservation{}, errors.New("invalid card confirmation identity")
	}
	if p.Environment == "mock" && !c.MockExternal {
		return CardObservation{}, errors.New("built-in mock confirmation is managed by the server")
	}
	if !allowedBankEndpoint(p.Environment, p.APIBaseURL) || p.HTTPTimeoutSeconds < 1 || p.HTTPTimeoutSeconds > 300 {
		return CardObservation{}, errors.New("invalid card confirmation configuration")
	}
	client, err := paytochkabank.NewClient(token, paytochkabank.WithBaseURL(p.APIBaseURL), paytochkabank.WithHTTPClient(&http.Client{
		Timeout:       time.Duration(p.HTTPTimeoutSeconds) * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}))
	if err != nil {
		return CardObservation{}, errors.New("card confirmation credentials are unavailable")
	}
	result, err := client.Acquiring.GetPayment(ctx, a.OperationID)
	if err != nil || result == nil {
		return CardObservation{}, errors.New("card payment status could not be verified")
	}
	if result.PaymentLinkID != "" && result.PaymentLinkID != a.ID {
		return CardObservation{}, errors.New("card payment belongs to a different checkout attempt")
	}
	observation := CardObservation{
		OperationID: result.OperationID, Status: string(result.Status), CustomerCode: result.CustomerCode,
		MerchantID: result.MerchantID, AmountKopecks: int64(result.Amount),
		PaymentType: result.PaymentType, PaymentID: result.PaymentID, TransactionID: result.TransactionID,
		CheckoutURL: result.PaymentLink, Paid: result.Status == paytochkabank.AcquiringApproved,
	}
	if !sameCardObservationIdentity(candidate, observation) {
		return CardObservation{}, errors.New("card payment does not match its frozen registration")
	}
	if observation.CheckoutURL != "" && !validCardObservationURL(observation.CheckoutURL) {
		return CardObservation{}, errors.New("card payment link is invalid")
	}
	return observation, nil
}

func validCardConfirmationCandidate(candidate ConfirmationCandidate) bool {
	a, p := candidate.Attempt, candidate.Profile
	return a.Method == "card" && a.ID != "" && a.OrderID != "" && a.Currency == "RUB" && a.AmountKopecks > 0 &&
		p.ID > 0 && p.ID == a.PaymentProfileID && p.CompanyID == a.CompanyID && p.Environment == a.Environment &&
		p.CustomerCode != "" && p.AcquiringMerchantID != "" &&
		(p.Environment == "mock" || p.Environment == "sandbox" || p.Environment == "production")
}

func sameCardObservationIdentity(candidate ConfirmationCandidate, observation CardObservation) bool {
	a, p := candidate.Attempt, candidate.Profile
	return observation.OperationID != "" && (a.OperationID == "" || observation.OperationID == a.OperationID) &&
		observation.CustomerCode == p.CustomerCode && observation.MerchantID == p.AcquiringMerchantID &&
		observation.AmountKopecks == a.AmountKopecks && observation.Status != "" &&
		(observation.PaymentType == "" || observation.PaymentType == "card") &&
		(!observation.Paid || observation.Status == string(paytochkabank.AcquiringApproved))
}

func validCardObservationURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil &&
		len(value) <= 2083 && !strings.ContainsAny(value, "\r\n")
}

var _ CardChecker = SDKCardChecker{}
