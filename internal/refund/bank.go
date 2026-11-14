package refund

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dronm/paytochkabank"
	"github.com/dronm/pronogti/internal/payment"
)

type BankPayment struct {
	Profile payment.Profile

	RefundID, PaymentID, Method string
	OperationID, QRCID          string
	TransactionID               string
	RefTransactionID            string
	AmountKopecks               int64
}

type BankObservation struct {
	State, ExternalID, Code string
}

type SDKBank struct {
	Tokens payment.TokenSource
}

var (
	refundAccountID  = regexp.MustCompile(`^([0-9]{20})/([0-9]{9})$`)
	sbpTransactionID = regexp.MustCompile(`^[a-zA-Z0-9]{32}$`)
)

// Validate verifies the frozen sale identifiers needed by the refund endpoint.
func (b SDKBank) Validate(sale BankPayment) error {
	if sale.Method == payment.MethodTerminalCard {
		return errors.New("physical terminal refunds are not supported by the internet-acquiring API")
	}
	profile := sale.Profile
	if sale.PaymentID == "" || sale.AmountKopecks <= 0 || profile.ID <= 0 || profile.CompanyID <= 0 {
		return errors.New("invalid refund payment identity")
	}
	if sale.Method != payment.MethodCard && sale.Method != payment.MethodSBP {
		return errors.New("payment method does not support refunds")
	}
	if profile.Environment == "mock" {
		return nil
	}
	if b.Tokens == nil || !payment.AllowedBankEndpoint(profile.Environment, profile.APIBaseURL) ||
		profile.HTTPTimeoutSeconds < 1 || profile.HTTPTimeoutSeconds > 300 {
		return errors.New("invalid bank refund configuration")
	}
	if sale.Method == payment.MethodCard {
		if strings.TrimSpace(sale.OperationID) == "" || profile.CustomerCode == "" || profile.AcquiringMerchantID == "" {
			return errors.New("card operation or acquiring identity is missing")
		}
		return nil
	}
	if strings.TrimSpace(sale.QRCID) == "" || !sbpTransactionID.MatchString(sale.TransactionID) {
		return errors.New("SBP refund requires a dynamic QR and its original NSPK transaction ID")
	}
	if !refundAccountID.MatchString(payment.TochkaAccountID(profile.AccountID)) {
		return errors.New("SBP refund requires the original settlement account and a valid bank BIC")
	}
	return nil
}

func (b SDKBank) validateAmount(sale BankPayment, amount int64) error {
	if err := b.Validate(sale); err != nil {
		return err
	}
	if amount <= 0 || amount > sale.AmountKopecks {
		return errors.New("invalid bank refund amount")
	}
	return nil
}

func (b SDKBank) client(ctx context.Context, sale BankPayment) (*paytochkabank.Client, error) {
	profile := sale.Profile
	token, err := b.Tokens.Token(ctx, profile)
	if err != nil {
		return nil, errors.New("bank refund credentials are unavailable")
	}
	client, err := paytochkabank.NewClient(token, paytochkabank.WithBaseURL(profile.APIBaseURL),
		paytochkabank.WithHTTPClient(&http.Client{Timeout: time.Duration(profile.HTTPTimeoutSeconds) * time.Second}))
	if err != nil {
		return nil, errors.New("bank refund client configuration is invalid")
	}
	return client, nil
}

// Prepare records the existing card refund order IDs before a POST can be attempted.
// The caller must persist this baseline under its exclusive refund claim.
func (b SDKBank) Prepare(ctx context.Context, sale BankPayment, amount int64) ([]string, error) {
	if err := b.validateAmount(sale, amount); err != nil {
		return nil, err
	}
	if sale.Profile.Environment == "mock" {
		return []string{}, nil
	}
	client, err := b.client(ctx, sale)
	if err != nil {
		return nil, err
	}
	if sale.Method == payment.MethodSBP {
		return []string{}, nil
	}
	original, err := readCardSale(ctx, client, sale)
	if err != nil {
		return nil, err
	}
	if original.Status != paytochkabank.AcquiringApproved {
		return nil, errors.New("the bank does not report the card payment as approved for refund")
	}
	refunds, alreadyRefunded, err := cardRefundOrders(original)
	if err != nil {
		return nil, err
	}
	if amount > sale.AmountKopecks-alreadyRefunded {
		return nil, errors.New("bank refund amount exceeds the original payment's remaining balance")
	}
	baseline := make([]string, 0, len(refunds))
	for id := range refunds {
		baseline = append(baseline, id)
	}
	sort.Strings(baseline)
	return baseline, nil
}

// Submit performs one provider POST and reports only sanitized outcome data.
// It does not retry an uncertain provider write.
func (b SDKBank) Submit(ctx context.Context, sale BankPayment, amount int64, baseline []string) (BankObservation, error) {
	if err := b.validateAmount(sale, amount); err != nil {
		return BankObservation{State: "rejected", Code: "bank_refund_request_invalid"}, nil
	}
	if sale.Profile.Environment == "mock" {
		return mockBankRefund(sale), nil
	}
	if sale.Method == payment.MethodCard && baseline == nil {
		return BankObservation{State: "rejected", Code: "bank_refund_history_missing"}, nil
	}
	client, err := b.client(ctx, sale)
	if err != nil {
		return BankObservation{State: "rejected", Code: "bank_refund_client_unavailable"}, nil
	}
	if sale.Method == payment.MethodSBP {
		account := refundAccountID.FindStringSubmatch(payment.TochkaAccountID(sale.Profile.AccountID))
		result, err := client.SBP.Refund(ctx, paytochkabank.RefundSBPRequest{
			BankCode: account[2], AccountCode: account[1], QRCID: sale.QRCID,
			TrxID: sale.TransactionID, Amount: paytochkabank.Money(amount),
		})
		if err != nil {
			return refundSubmissionError(err), nil
		}
		return sbpRefundObservation(result), nil
	}

	result, err := client.Acquiring.Refund(ctx, sale.OperationID, paytochkabank.RefundAcquiringRequest{
		Amount: paytochkabank.Money(amount),
	})
	if err != nil {
		return refundSubmissionError(err), nil
	}
	if result.Amount != 0 && int64(result.Amount) != amount {
		return BankObservation{State: "unknown", Code: "bank_refund_response_mismatch"}, nil
	}
	if result.IsRefund == nil {
		return BankObservation{State: "unknown", Code: "bank_refund_outcome_unknown"}, nil
	}
	if !*result.IsRefund {
		if (result.Status != "" && result.Status != paytochkabank.AcquiringApproved) ||
			(result.OperationID != "" && result.OperationID != sale.OperationID) {
			return BankObservation{State: "unknown", Code: "bank_refund_response_mismatch"}, nil
		}
		return BankObservation{State: "rejected", Code: "bank_refund_rejected"}, nil
	}
	// A POST result can acknowledge processing. The GET refund history is the
	// authority for this card refund's completion and refund order identifier.
	observation := BankObservation{State: "pending"}
	if result.OperationID != sale.OperationID {
		observation.ExternalID = result.OperationID
	}
	return observation, nil
}

// Check reads provider evidence. For cards the caller must have durably recorded
// a positive POST acknowledgment before matching a refund against its baseline.
func (b SDKBank) Check(ctx context.Context, sale BankPayment, amount int64, baseline []string, externalID string) (BankObservation, error) {
	if err := b.validateAmount(sale, amount); err != nil {
		return BankObservation{}, err
	}
	if sale.Profile.Environment == "mock" {
		return mockBankRefund(sale), nil
	}
	if sale.Method == payment.MethodSBP && externalID == "" {
		return BankObservation{State: "unknown", Code: "bank_refund_reference_missing"}, nil
	}
	if sale.Method == payment.MethodCard && baseline == nil {
		return BankObservation{State: "unknown", Code: "bank_refund_history_missing"}, nil
	}
	client, err := b.client(ctx, sale)
	if err != nil {
		return BankObservation{}, err
	}
	if sale.Method == payment.MethodSBP {
		result, err := client.SBP.GetRefund(ctx, externalID)
		if err != nil {
			return BankObservation{}, errors.New("SBP refund status is unavailable")
		}
		if result.RequestID != externalID {
			return BankObservation{State: "unknown", Code: "bank_refund_response_mismatch"}, nil
		}
		return sbpRefundObservation(result), nil
	}
	original, err := readCardSale(ctx, client, sale)
	if err != nil {
		return BankObservation{}, err
	}
	return cardRefundObservation(original, amount, baseline, externalID), nil
}

func mockBankRefund(sale BankPayment) BankObservation {
	result := BankObservation{State: "confirmed"}
	if sale.RefundID != "" {
		result.ExternalID = "mock-refund-" + sale.RefundID
	}
	return result
}

func refundSubmissionError(err error) BankObservation {
	var validation *paytochkabank.ValidationError
	var bankError *paytochkabank.APIError
	switch {
	case errors.As(err, &validation) || errors.Is(err, paytochkabank.ErrUnsupportedPaymentMethod):
		return BankObservation{State: "rejected", Code: "bank_refund_request_invalid"}
	case errors.As(err, &bankError) && !bankError.OutcomeUnknown:
		switch bankError.StatusCode {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			return BankObservation{State: "rejected", Code: "bank_refund_rejected"}
		}
	}
	return BankObservation{State: "unknown", Code: "bank_refund_outcome_unknown"}
}

func sbpRefundObservation(result *paytochkabank.SBPRefund) BankObservation {
	switch result.Status {
	case paytochkabank.SBPRefundAccepted:
		return BankObservation{State: "confirmed", ExternalID: result.RequestID}
	case paytochkabank.SBPRefundRejected:
		return BankObservation{State: "rejected", ExternalID: result.RequestID, Code: "bank_refund_rejected"}
	default:
		return BankObservation{State: "pending", ExternalID: result.RequestID}
	}
}

func readCardSale(ctx context.Context, client *paytochkabank.Client, sale BankPayment) (*paytochkabank.AcquiringPayment, error) {
	original, err := client.Acquiring.GetPayment(ctx, sale.OperationID)
	if err != nil {
		return nil, errors.New("original card payment could not be verified")
	}
	if original.OperationID != sale.OperationID || int64(original.Amount) != sale.AmountKopecks ||
		original.CustomerCode != sale.Profile.CustomerCode || original.MerchantID != sale.Profile.AcquiringMerchantID ||
		(original.PaymentLinkID != "" && original.PaymentLinkID != sale.PaymentID) ||
		(original.PaymentType != "" && original.PaymentType != payment.MethodCard) {
		return nil, errors.New("original card payment does not match the recorded sale")
	}
	return original, nil
}

func cardRefundOrders(original *paytochkabank.AcquiringPayment) (map[string]int64, int64, error) {
	refunds := make(map[string]int64)
	var refunded int64
	for _, order := range original.Orders {
		if order.Type != "refund" {
			continue
		}
		amount := int64(order.Amount)
		if strings.TrimSpace(order.OrderID) == "" || amount <= 0 || amount > int64(original.Amount)-refunded {
			return nil, 0, errors.New("card refund history has incomplete or inconsistent amounts")
		}
		if _, exists := refunds[order.OrderID]; exists {
			return nil, 0, errors.New("card refund history contains duplicate bank identifiers")
		}
		refunds[order.OrderID] = amount
		refunded += amount
	}
	return refunds, refunded, nil
}

func cardRefundObservation(original *paytochkabank.AcquiringPayment, amount int64, baseline []string, externalID string) BankObservation {
	if original.Status != paytochkabank.AcquiringRefunded && original.Status != paytochkabank.AcquiringRefundedPartially {
		return BankObservation{State: "pending"}
	}
	refunds, total, err := cardRefundOrders(original)
	if err != nil || (original.Status == paytochkabank.AcquiringRefunded) != (total == int64(original.Amount)) {
		return BankObservation{State: "unknown", Code: "bank_refund_history_mismatch"}
	}
	for _, id := range baseline {
		if _, exists := refunds[id]; !exists {
			return BankObservation{State: "unknown", Code: "bank_refund_history_mismatch"}
		}
		delete(refunds, id)
	}
	if externalID != "" {
		refunded, exists := refunds[externalID]
		if !exists {
			return BankObservation{State: "pending"}
		}
		if refunded != amount {
			return BankObservation{State: "unknown", Code: "bank_refund_history_mismatch"}
		}
		return BankObservation{State: "confirmed", ExternalID: externalID}
	}
	if len(refunds) == 0 {
		return BankObservation{State: "pending"}
	}
	if len(refunds) == 1 {
		for id, refunded := range refunds {
			if refunded == amount {
				return BankObservation{State: "confirmed", ExternalID: id}
			}
		}
	}
	return BankObservation{State: "unknown", Code: "bank_refund_history_mismatch"}
}
