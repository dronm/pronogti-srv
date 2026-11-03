package settlement

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/dronm/pronogti/internal/checkout"
)

// VerifyPaymentEvidence verifies the transaction IDs explicitly selected by an
// operator after inspecting YCLIENTS. The IDs assert attribution to this bank
// payment; amount equality alone never makes that decision. This method only
// reads the original sale and never sends another payment.
func (p *YClientsProvider) VerifyPaymentEvidence(ctx context.Context, job Job, operation Operation, transactionIDs []int64, reason string) (Outcome, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 4000 {
		return Outcome{}, errors.New("an operator review reason of at most 4000 bytes is required")
	}
	if operation.Kind != KindVisitPayment && operation.Kind != KindGoodsPayment {
		return Outcome{}, errors.New("operator payment evidence is only supported for account payments")
	}
	if operation.State != "unknown" && operation.State != "dispatched" {
		return Outcome{}, errors.New("only an uncertain dispatched account payment can be resolved")
	}
	if job.Order.State != "paid" || job.PaidAt.IsZero() || job.PaymentID == "" || job.Environment != "production" || job.Config.Mode != "live" {
		return Outcome{}, errors.New("operator evidence requires a confirmed production bank payment and live settlement")
	}
	if err := ValidateCheckout(job.Order, job.Config); err != nil {
		return Outcome{}, err
	}
	request, err := decodeOperation(job, operation)
	if err != nil {
		return Outcome{}, errors.New("the frozen settlement request is invalid")
	}
	if len(transactionIDs) == 0 || len(transactionIDs) > 500 {
		return Outcome{}, errors.New("explicit YCLIENTS payment transaction IDs are required")
	}
	baseline := make(map[int64]bool, len(request.BaselineTransactionIDs))
	for _, id := range request.BaselineTransactionIDs {
		baseline[id] = true
	}
	selected := make(map[int64]bool, len(transactionIDs))
	for _, id := range transactionIDs {
		if id <= 0 || selected[id] || baseline[id] {
			return Outcome{}, errors.New("payment evidence IDs must be positive, unique and absent from the original baseline")
		}
		selected[id] = true
	}
	documentID, err := resolveDocument(job, request)
	if err != nil {
		return Outcome{}, errors.New("the original settlement document is unavailable")
	}
	if p == nil || p.client == nil {
		return Outcome{}, errors.New("YCLIENTS settlement client is not initialized")
	}
	sale, err := p.client.NewSaleAPI(job.Config.CompanyID).GetContext(ctx, documentID)
	if err != nil {
		return Outcome{}, errors.New("YCLIENTS payment evidence could not be read")
	}
	verified := make(map[int64]bool, len(selected))
	var total int64
	for _, transaction := range sale.State.PaymentTransactions {
		if !selected[transaction.ID] {
			continue
		}
		if verified[transaction.ID] || transaction.DocumentID != documentID || transaction.AccountID != request.AccountID {
			return Outcome{}, errors.New("payment evidence has a repeated ID or a different document or account")
		}
		amount, err := kopecks(transaction.Amount)
		if err != nil || amount <= 0 || amount > checkout.MaxMoney-total {
			return Outcome{}, errors.New("payment evidence has an invalid transaction amount")
		}
		total += amount
		verified[transaction.ID] = true
	}
	if len(verified) != len(selected) || total != request.AmountKopecks {
		return Outcome{}, errors.New("all selected payment transactions must exist and total the exact confirmed amount")
	}
	result, err := json.Marshal(map[string]any{
		"document_id":     documentID,
		"transaction_ids": append([]int64(nil), transactionIDs...),
		"operator_evidence": map[string]any{
			"reason":      reason,
			"verified_at": time.Now().UTC(),
		},
	})
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{State: "succeeded", Result: result}, nil
}
