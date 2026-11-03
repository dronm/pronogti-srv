package payment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/dronm/paytochkabank"
)

// ConfirmationCandidate combines immutable registration identity with current
// credentials for the same profile. A disabled profile still needs reconciliation.
type ConfirmationCandidate struct {
	Attempt   Attempt
	Profile   Profile
	ClaimedAt time.Time
}

// BankObservation is evidence about an already registered fixed-amount QR.
// GetPayments does not return money/merchant fields: its authenticated query is
// scoped to the frozen customer and QR identity. Signed webhooks carry the exact
// amount and merchant, which are checked independently before application.
type BankObservation struct {
	QRCID            string
	Status           string
	Paid             bool
	TransactionID    string
	RefTransactionID string
	PaymentType      string
	Payload          json.RawMessage
}

type BankChecker interface {
	Check(context.Context, ConfirmationCandidate, string) (BankObservation, error)
}

type SDKChecker struct{ MockExternal bool }

// Check only reads the bank; it never recreates a QR, interprets "Confirmed" as
// credit, or considers a failed payment attempt proof that its QR cannot be paid.
func (c SDKChecker) Check(ctx context.Context, candidate ConfirmationCandidate, token string) (BankObservation, error) {
	p, a := candidate.Profile, candidate.Attempt
	if ctx == nil || a.QRCID == "" || p.CustomerCode == "" || p.ID != a.PaymentProfileID || p.CompanyID != a.CompanyID || p.Environment != a.Environment {
		return BankObservation{}, errors.New("invalid bank confirmation identity")
	}
	if p.Environment == "mock" && !c.MockExternal {
		return BankObservation{}, errors.New("built-in mock confirmation is managed by the server")
	}
	if !allowedBankEndpoint(p.Environment, p.APIBaseURL) || p.HTTPTimeoutSeconds < 1 || p.HTTPTimeoutSeconds > 300 {
		return BankObservation{}, errors.New("invalid bank confirmation configuration")
	}
	client, err := paytochkabank.NewClient(token, paytochkabank.WithBaseURL(p.APIBaseURL), paytochkabank.WithHTTPClient(&http.Client{Timeout: time.Duration(p.HTTPTimeoutSeconds) * time.Second}))
	if err != nil {
		return BankObservation{}, errors.New("bank confirmation credentials are unavailable")
	}
	start := a.CreatedAt.UTC().AddDate(0, 0, -1).Format("2006-01-02")
	end := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	observation := BankObservation{QRCID: a.QRCID, Status: "NotFound"}
	var accepted *paytochkabank.SBPPayment
	const maxPages = 100
	for page := 1; page <= maxPages; page++ {
		response, err := client.SBP.GetPayments(ctx, paytochkabank.GetSBPPaymentsRequest{CustomerCode: p.CustomerCode, QRCID: a.QRCID, FromDate: start, ToDate: end, Page: page, PerPage: 1000})
		if err != nil || response == nil {
			return BankObservation{}, errors.New("bank payment status could not be verified")
		}
		if response.TotalPages > maxPages {
			return BankObservation{}, errors.New("bank payment history exceeds the supported confirmation window")
		}
		for _, payment := range response.Payments {
			if payment.QRCID != a.QRCID {
				continue
			}
			if observation.Status == "NotFound" {
				observation.Status = string(payment.Status)
			}
			if payment.Status != paytochkabank.SBPPaymentAccepted {
				continue
			}
			if accepted != nil && accepted.RefTransactionID != "" && payment.RefTransactionID != "" && accepted.RefTransactionID != payment.RefTransactionID {
				return BankObservation{}, errors.New("bank returned conflicting successful payments for a one-use QR")
			}
			copy := payment
			if accepted == nil || copy.RefTransactionID != "" {
				accepted = &copy
			}
		}
		if response.TotalPages <= page {
			break
		}
	}
	if accepted != nil {
		observation.QRCID = accepted.QRCID
		observation.Status = string(accepted.Status)
		// A successful row without its stable bank reference cannot yet be
		// durably identified for settlement/refunds. Retry the read for it.
		observation.Paid = accepted.RefTransactionID != ""
		observation.RefTransactionID = accepted.RefTransactionID
		observation.PaymentType = "sbpPayment"
		observation.Payload, _ = json.Marshal(map[string]any{"source": "bank_poll", "customerCode": p.CustomerCode, "qrcId": a.QRCID, "status": accepted.Status, "refTransactionId": accepted.RefTransactionID})
	}
	return observation, nil
}

var _ BankChecker = SDKChecker{}
