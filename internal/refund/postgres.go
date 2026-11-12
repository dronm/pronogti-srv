package refund

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/webapp"
	"github.com/google/uuid"
)

type PostgresRepository struct{ db ds.Provider }

func NewPostgresRepository(db ds.Provider) *PostgresRepository { return &PostgresRepository{db: db} }

const refundColumns = `r.id::text,r.payment_id::text,r.order_id::text,r.company_id,r.operator_id,
	r.amount_kopecks,r.lines,r.bank_state,r.fiscal_state,COALESCE(r.bank_reference,''),
	COALESCE(r.error_code,''),COALESCE(r.receipt_number,0),r.created_at,r.updated_at,r.receipt_payload`

func refundDest(refund *Refund, lines *[]byte) []any {
	return []any{&refund.ID, &refund.PaymentID, &refund.OrderID, &refund.CompanyID, &refund.OperatorID,
		&refund.AmountKopecks, lines, &refund.BankState, &refund.FiscalState, &refund.BankReference,
		&refund.ErrorCode, &refund.ReceiptNumber, &refund.CreatedAt, &refund.UpdatedAt, &refund.ReceiptPayload}
}

func readRefund(row ds.Row, extra ...any) (Refund, error) {
	var refund Refund
	var lines []byte
	if err := row.Scan(append(refundDest(&refund, &lines), extra...)...); err != nil {
		return Refund{}, err
	}
	if err := json.Unmarshal(lines, &refund.Lines); err != nil {
		return Refund{}, errors.New("invalid stored refund lines")
	}
	return refund, nil
}

func (r *PostgresRepository) Get(ctx context.Context, paymentID, refundID string) (Refund, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Refund{}, err
	}
	defer release()
	result, err := readRefund(conn.QueryRow(ctx, `SELECT `+refundColumns+`
		FROM public.payment_refunds r WHERE r.payment_id=$1 AND r.id=$2`, paymentID, refundID))
	if errors.Is(err, ds.ErrNoRows) {
		return Refund{}, webapp.NotFound("refund not found", nil)
	}
	if err != nil {
		return Refund{}, fmt.Errorf("read refund: %w", err)
	}
	return result, nil
}

func (r *PostgresRepository) LoadSale(ctx context.Context, paymentID string) (Sale, []Refund, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Sale{}, nil, err
	}
	defer release()
	sale, err := loadSale(ctx, conn, paymentID, false)
	if err != nil {
		return Sale{}, nil, err
	}
	previous, err := priorRefunds(ctx, conn, paymentID)
	return sale, previous, err
}

const refundSaleSQL = `SELECT p.id::text,p.order_id::text,p.company_id,p.payment_profile_id,p.method,p.environment,p.state,
	p.amount_kopecks,COALESCE(p.acquiring_operation_id,''),COALESCE(p.qrc_id,''),
	COALESCE(p.transaction_id,''),COALESCE(p.ref_transaction_id,''),p.profile_snapshot,
	COALESCE(profile.jwt_env,''),profile.jwt_ciphertext,o.state,o.items_snapshot,
	COALESCE(fr.state,''),COALESCE(fr.fiscal_snapshot,o.fiscal_snapshot),fr.request_payload
	FROM public.payments p
	JOIN public.payment_orders o ON o.id=p.order_id AND o.company_id=p.company_id
	JOIN public.payment_profiles profile ON profile.id=p.payment_profile_id
		AND profile.company_id=p.company_id AND profile.environment=p.environment
	LEFT JOIN public.fiscal_receipts fr ON fr.order_id=o.id AND fr.payment_id=p.id
		AND fr.company_id=p.company_id AND fr.environment=p.environment
	WHERE p.id=$1`

func loadSale(ctx context.Context, query ds.Querier, paymentID string, lockPayment bool) (Sale, error) {
	sql := refundSaleSQL
	if lockPayment {
		sql += " FOR UPDATE OF p"
	}
	var sale Sale
	var profileID int64
	var environment string
	var profile, fiscalProfile, items []byte
	bank := &sale.Bank
	err := query.QueryRow(ctx, sql, paymentID).Scan(
		&bank.PaymentID, &sale.OrderID, &sale.CompanyID, &profileID, &bank.Method, &environment,
		&sale.PaymentState, &bank.AmountKopecks, &bank.OperationID, &bank.QRCID,
		&bank.TransactionID, &bank.RefTransactionID, &profile, &bank.Profile.JWTEnv,
		&bank.Profile.JWTCiphertext, &sale.OrderState, &items, &sale.FiscalState, &fiscalProfile, &sale.SaleReceipt,
	)
	if errors.Is(err, ds.ErrNoRows) {
		return Sale{}, webapp.NotFound("payment not found", nil)
	}
	if err != nil {
		return Sale{}, fmt.Errorf("read refund sale: %w", err)
	}
	if err := json.Unmarshal(profile, &bank.Profile); err != nil || bank.Profile.ID != profileID ||
		bank.Profile.CompanyID != sale.CompanyID || bank.Profile.Environment != environment {
		return Sale{}, refundConflict("original payment registration is unavailable", "refund_bank_snapshot_unavailable")
	}
	if err := json.Unmarshal(fiscalProfile, &sale.FiscalProfile); err != nil || sale.FiscalProfile.CompanyID != sale.CompanyID {
		return Sale{}, refundConflict("original fiscal configuration is unavailable", "refund_fiscal_snapshot_unavailable")
	}
	if err := json.Unmarshal(items, &sale.Items); err != nil {
		return Sale{}, errors.New("invalid original sale item snapshot")
	}
	return sale, nil
}

func priorRefunds(ctx context.Context, query ds.Querier, paymentID string) ([]Refund, error) {
	rows, err := query.Query(ctx, `SELECT `+refundColumns+` FROM public.payment_refunds r
		WHERE r.payment_id=$1 ORDER BY r.created_at,r.id`, paymentID)
	if err != nil {
		return nil, fmt.Errorf("read previous refunds: %w", err)
	}
	defer rows.Close()
	previous := make([]Refund, 0)
	for rows.Next() {
		item, err := readRefund(rows)
		if err != nil {
			return nil, fmt.Errorf("scan previous refunds: %w", err)
		}
		previous = append(previous, item)
	}
	return previous, rows.Err()
}

// Reserve takes locks in the same order as payment confirmation. That makes
// the previous-refunds check and the reservation one serialized operation.
func (r *PostgresRepository) Reserve(ctx context.Context, paymentID string, operatorID int64, key, hash string, prepare func(Sale, []Refund) (Refund, error)) (Refund, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Refund{}, err
	}
	defer release()
	var result Refund
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		var orderID string
		err := tx.QueryRow(ctx, `SELECT o.id::text FROM public.payment_orders o
			JOIN public.payments p ON p.order_id=o.id AND p.company_id=o.company_id
			WHERE p.id=$1 FOR UPDATE OF o`, paymentID).Scan(&orderID)
		if errors.Is(err, ds.ErrNoRows) {
			return webapp.NotFound("payment not found", nil)
		}
		if err != nil {
			return fmt.Errorf("lock refund checkout: %w", err)
		}

		var storedHash string
		existing, err := readRefund(tx.QueryRow(ctx, `SELECT `+refundColumns+`,r.request_hash
			FROM public.payment_refunds r WHERE r.payment_id=$1 AND r.idempotency_key=$2`, paymentID, key), &storedHash)
		if err == nil {
			if storedHash != hash {
				return refundConflict("Idempotency-Key was already used for a different refund", "refund_idempotency_conflict")
			}
			result = existing
			return nil
		}
		if !errors.Is(err, ds.ErrNoRows) {
			return fmt.Errorf("read existing refund: %w", err)
		}

		sale, err := loadSale(ctx, tx, paymentID, true)
		if err != nil {
			return err
		}
		previous, err := priorRefunds(ctx, tx, paymentID)
		if err != nil {
			return err
		}
		for _, item := range previous {
			if item.BankState != "rejected" && (item.BankState != "confirmed" ||
				(item.FiscalState != "issued" && item.FiscalState != "demo")) {
				return refundConflict("another refund for this payment needs completion", "refund_already_open")
			}
		}
		prepared, err := prepare(sale, previous)
		if err != nil {
			return err
		}
		lines, err := json.Marshal(prepared.Lines)
		if err != nil {
			return fmt.Errorf("encode refund lines: %w", err)
		}
		result, err = readRefund(tx.QueryRow(ctx, `INSERT INTO public.payment_refunds AS r (
			id,payment_id,order_id,company_id,environment,operator_id,idempotency_key,request_hash,
			amount_kopecks,lines,receipt_payload)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11::jsonb) RETURNING `+refundColumns,
			prepared.ID, paymentID, sale.OrderID, sale.CompanyID, sale.Bank.Profile.Environment, operatorID,
			key, hash, prepared.AmountKopecks, string(lines), string(prepared.ReceiptPayload)))
		if err != nil {
			return fmt.Errorf("reserve refund: %w", err)
		}
		return nil
	})
	return result, err
}

// A stale submitting claim can perform only a provider lookup. Prepared claims
// and queued receipts are fenced as submitting before the transaction commits.
const claimRefundSQL = `WITH due AS (
	SELECT r.id, CASE
		WHEN r.bank_state='queued' THEN 'bank_prepare'
		WHEN r.bank_state='prepared' THEN 'bank_submit'
		WHEN r.bank_state IN ('submitting','pending','unknown') THEN 'bank_check'
		WHEN r.fiscal_state='queued' THEN 'fiscal_submit'
		ELSE 'fiscal_check'
	END AS step
	FROM public.payment_refunds r
	WHERE r.next_attempt_at<=now()
		AND (r.claim_token IS NULL OR r.claimed_at<now()-interval '6 minutes')
		AND (r.bank_state IN ('queued','prepared','submitting','pending','unknown')
			OR r.fiscal_state IN ('queued','submitting','unknown'))
	ORDER BY r.next_attempt_at,r.id
	FOR UPDATE OF r SKIP LOCKED LIMIT 1
)
UPDATE public.payment_refunds r SET
	bank_state=CASE WHEN d.step='bank_submit' THEN 'submitting' ELSE r.bank_state END,
	fiscal_state=CASE WHEN d.step='fiscal_submit' THEN 'submitting' ELSE r.fiscal_state END,
	claim_token=$1,claimed_at=now(),updated_at=now()
FROM due d WHERE r.id=d.id
RETURNING ` + refundColumns + `,r.bank_baseline,d.step`

func (r *PostgresRepository) ClaimNext(ctx context.Context) (*Job, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	token, err := uuid.NewRandom()
	if err != nil {
		return nil, fmt.Errorf("generate refund claim token: %w", err)
	}
	var result *Job
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		job := Job{ClaimToken: token.String()}
		var baseline []byte
		record, err := readRefund(tx.QueryRow(ctx, claimRefundSQL, job.ClaimToken), &baseline, &job.Step)
		if errors.Is(err, ds.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim refund: %w", err)
		}
		job.Refund = record
		if len(baseline) > 0 {
			if err := json.Unmarshal(baseline, &job.Baseline); err != nil {
				return errors.New("invalid stored bank refund baseline")
			}
		}
		job.Sale, err = loadSale(ctx, tx, record.PaymentID, false)
		if err != nil {
			return err
		}
		result = &job
		return nil
	})
	return result, err
}

func (r *PostgresRepository) Finish(ctx context.Context, job Job, outcome Outcome) error {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	var result ds.ExecResult
	switch job.Step {
	case "bank_prepare", "bank_submit", "bank_check":
		var baseline any
		if outcome.Baseline != nil {
			body, err := json.Marshal(outcome.Baseline)
			if err != nil {
				return fmt.Errorf("encode bank refund baseline: %w", err)
			}
			baseline = string(body)
		}
		result, err = conn.Exec(ctx, `UPDATE public.payment_refunds SET
			bank_state=$3,bank_reference=COALESCE(NULLIF($4,''),bank_reference),error_code=NULLIF($5,''),
			bank_baseline=COALESCE($6::jsonb,bank_baseline),
			fiscal_state=CASE WHEN $3='confirmed' THEN 'queued' ELSE fiscal_state END,
			next_attempt_at=now()+($7::bigint*interval '1 microsecond'),
			claim_token=NULL,claimed_at=NULL,updated_at=now()
			WHERE id=$1 AND claim_token=$2`,
			job.Refund.ID, job.ClaimToken, outcome.State, outcome.Reference, outcome.ErrorCode,
			baseline, refundRetryMicros(outcome, true))
	case "fiscal_submit", "fiscal_check":
		result, err = conn.Exec(ctx, `UPDATE public.payment_refunds SET
			fiscal_state=$3,receipt_number=NULLIF($4,0),error_code=NULLIF($5,''),
			next_attempt_at=now()+($6::bigint*interval '1 microsecond'),
			claim_token=NULL,claimed_at=NULL,updated_at=now()
			WHERE id=$1 AND claim_token=$2 AND bank_state='confirmed'`,
			job.Refund.ID, job.ClaimToken, outcome.State, outcome.ReceiptNumber, outcome.ErrorCode,
			refundRetryMicros(outcome, false))
	default:
		return errors.New("invalid refund step")
	}
	if err != nil {
		return fmt.Errorf("persist refund outcome: %w", err)
	}
	if result.RowsAffected() != 1 {
		return errors.New("refund claim is no longer owned by this worker")
	}
	return nil
}

func refundRetryMicros(outcome Outcome, bank bool) any {
	switch outcome.State {
	case "queued", "prepared":
		return max(outcome.RetryAfter, 0).Microseconds()
	case "confirmed":
		if bank {
			return int64(0)
		}
	case "pending", "unknown":
		if outcome.RetryAfter > 0 {
			return max(outcome.RetryAfter, time.Microsecond).Microseconds()
		}
	}
	return nil
}

func refundConflict(message, code string) error {
	return webapp.Conflict(message, map[string]any{"code": code})
}

func (r *PostgresRepository) connection(ctx context.Context) (ds.Conn, func(), error) {
	if r == nil || r.db == nil {
		return nil, nil, errors.New("refund database is not configured")
	}
	lease, server, err := r.db.GetPrimary(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire refund database: %w", err)
	}
	return lease.Conn(), func() { r.db.Release(lease, server) }, nil
}
