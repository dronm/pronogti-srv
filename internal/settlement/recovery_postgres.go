package settlement

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/dronm/ds/v4"
)

// LoadJob is an operator read, never a browser endpoint. Evidence is checked
// against the immutable operation before ResolveOperation changes local state.
func (r *PostgresRepository) LoadJob(ctx context.Context, id string) (Job, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Job{}, err
	}
	defer release()
	return scanSettlementJob(ctx, conn, conn.QueryRow(ctx, `SELECT j.order_id::text,j.company_id,j.payment_id::text,j.environment,j.profile_snapshot,COALESCE(j.claimed_at,j.created_at),
		o.items_snapshot,o.subtotal_kopecks,o.bonus_kopecks,o.payable_kopecks,o.currency,o.state,
		COALESCE(o.client_id,0),COALESCE(o.visit_id,0),COALESCE(o.record_id,0),o.customer_contact,
		COALESCE(p.paid_at,p.updated_at),o.entry_mode,COALESCE(o.kiosk_id,0)
		FROM public.yclients_jobs j JOIN public.payment_orders o ON o.id=j.order_id
		JOIN public.payments p ON p.id=j.payment_id WHERE j.order_id=$1 AND o.state='paid' AND p.state='paid'`, id))
}

func (r *PostgresRepository) ResolveOperation(ctx context.Context, orderID, operationID string, outcome Outcome) error {
	if outcome.State != "succeeded" || !json.Valid(outcome.Result) {
		return errors.New("verified successful YCLIENTS evidence is required")
	}
	var proof struct {
		DocumentID       int64   `json:"document_id"`
		TransactionIDs   []int64 `json:"transaction_ids"`
		OperatorEvidence struct {
			Reason     string    `json:"reason"`
			VerifiedAt time.Time `json:"verified_at"`
		} `json:"operator_evidence"`
	}
	if json.Unmarshal(outcome.Result, &proof) != nil || proof.DocumentID <= 0 || len(proof.TransactionIDs) == 0 || len(proof.TransactionIDs) > 500 || strings.TrimSpace(proof.OperatorEvidence.Reason) == "" || len(proof.OperatorEvidence.Reason) > 4000 || proof.OperatorEvidence.VerifiedAt.IsZero() {
		return errors.New("auditable operator evidence is required")
	}
	seen := make(map[int64]bool, len(proof.TransactionIDs))
	for _, id := range proof.TransactionIDs {
		if id <= 0 || seen[id] {
			return errors.New("verified transaction IDs must be positive and unique")
		}
		seen[id] = true
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	return ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `SELECT j.order_id::text FROM public.yclients_jobs j
			JOIN public.payment_orders o ON o.id=j.order_id JOIN public.payments p ON p.id=j.payment_id
			WHERE j.order_id=$1 AND o.state='paid' AND p.state='paid' AND (j.state<>'processing' OR j.claimed_at<now()-interval '10 minutes') FOR UPDATE OF j`, orderID).Scan(&id)
		if errors.Is(err, ds.ErrNoRows) {
			return errors.New("settlement is not paid or is being processed; inspect it before resolving")
		}
		if err != nil {
			return err
		}
		result, err := tx.Exec(ctx, `UPDATE public.yclients_operations SET state='succeeded',result=$3::jsonb,error_code=NULL,updated_at=now()
			WHERE order_id=$1 AND id=$2 AND kind IN ('visit_payment','goods_payment') AND state IN ('unknown','dispatched') AND dispatched_at IS NOT NULL`, orderID, operationID, string(outcome.Result))
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("operation is not an unresolved dispatched account payment")
		}
		if _, err = tx.Exec(ctx, `UPDATE public.yclients_jobs SET state='pending',claimed_at=NULL,next_attempt_at=now(),error_code=NULL,updated_at=now() WHERE order_id=$1`, orderID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE public.payment_orders SET yclients_sync_status='pending' WHERE id=$1 AND state='paid'`, orderID)
		return err
	})
}
