package settlement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dronm/ds/v4"
	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/webapp"
)

type PostgresRepository struct{ db ds.Provider }

func NewPostgresRepository(db ds.Provider) *PostgresRepository { return &PostgresRepository{db: db} }

const claimJobSQL = `WITH candidate AS (
	SELECT j.order_id FROM public.yclients_jobs j
	JOIN public.payment_orders o ON o.id=j.order_id AND o.company_id=j.company_id
	JOIN public.payments p ON p.id=j.payment_id AND p.order_id=o.id
	WHERE o.state='paid' AND p.state='paid' AND (
		(j.state IN ('pending','unknown') AND (j.next_attempt_at IS NULL OR j.next_attempt_at<=now()))
		OR (j.state='processing' AND j.claimed_at<now()-interval '10 minutes')
	) ORDER BY j.created_at FOR UPDATE OF j SKIP LOCKED LIMIT 1
), claimed AS (
	UPDATE public.yclients_jobs j SET state='processing',claimed_at=now(),updated_at=now()
	FROM candidate c WHERE j.order_id=c.order_id RETURNING j.*
)
SELECT j.order_id::text,j.company_id,j.payment_id::text,j.environment,j.profile_snapshot,j.claimed_at,
	o.items_snapshot,o.subtotal_kopecks,o.bonus_kopecks,o.payable_kopecks,o.currency,o.state,
	COALESCE(o.client_id,0),COALESCE(o.visit_id,0),COALESCE(o.record_id,0),o.customer_contact,
	COALESCE(p.paid_at,p.updated_at),o.entry_mode,COALESCE(o.kiosk_id,0)
FROM claimed j JOIN public.payment_orders o ON o.id=j.order_id JOIN public.payments p ON p.id=j.payment_id`

func (r *PostgresRepository) Claim(ctx context.Context) (Job, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Job{}, err
	}
	defer release()
	return scanSettlementJob(ctx, conn, conn.QueryRow(ctx, claimJobSQL))
}

func scanSettlementJob(ctx context.Context, conn ds.Conn, row ds.Row) (Job, error) {
	var job Job
	var profile, items []byte
	var kioskID int64
	err := row.Scan(&job.Order.ID, &job.Order.Owner.CompanyID, &job.PaymentID, &job.Environment, &profile, &job.ClaimedAt,
		&items, &job.Order.SubtotalKopecks, &job.Order.BonusKopecks, &job.Order.PayableKopecks, &job.Order.Currency, &job.Order.State,
		&job.Order.Owner.ClientID, &job.Order.VisitID, &job.Order.RecordID, &job.CustomerContact, &job.PaidAt, &job.Order.Owner.Mode, &kioskID)
	if errors.Is(err, ds.ErrNoRows) {
		return Job{}, ErrNoJob
	}
	if err != nil {
		return Job{}, fmt.Errorf("claim YCLIENTS settlement: %w", err)
	}
	if err = json.Unmarshal(profile, &job.Config); err != nil {
		return Job{}, errors.New("invalid frozen YCLIENTS profile")
	}
	if err = json.Unmarshal(items, &job.Order.Items); err != nil {
		return Job{}, errors.New("invalid frozen YCLIENTS items")
	}
	job.Order.CustomerContact = job.CustomerContact
	if kioskID > 0 {
		job.Order.Owner.KioskID = &kioskID
	}
	rows, err := conn.Query(ctx, `SELECT id,kind,state,request,result,dispatched_at,COALESCE(error_code,'')
		FROM public.yclients_operations WHERE order_id=$1 ORDER BY sequence_no`, job.Order.ID)
	if err != nil {
		return Job{}, fmt.Errorf("read settlement operations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var op Operation
		if err = rows.Scan(&op.ID, &op.Kind, &op.State, &op.Request, &op.Result, &op.DispatchedAt, &op.ErrorCode); err != nil {
			return Job{}, err
		}
		job.Operations = append(job.Operations, op)
	}
	if err = rows.Err(); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (r *PostgresRepository) SavePlan(ctx context.Context, job Job, operations []Operation) error {
	if len(operations) == 0 || len(operations) > 500 {
		return errors.New("settlement plan has an invalid operation count")
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	return ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		if err := lockClaim(ctx, tx, job); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.yclients_operations WHERE order_id=$1`, job.Order.ID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("settlement plan already exists")
		}
		for index, op := range operations {
			if op.ID == "" || op.Kind == "" || op.State != "pending" || len(op.Request) == 0 || !json.Valid(op.Request) {
				return errors.New("invalid settlement plan operation")
			}
			_, err := tx.Exec(ctx, `INSERT INTO public.yclients_operations(order_id,id,sequence_no,kind,state,request)
				VALUES($1,$2,$3,$4,'pending',$5::jsonb)`, job.Order.ID, op.ID, index, op.Kind, string(op.Request))
			if err != nil {
				return fmt.Errorf("save settlement plan: %w", err)
			}
		}
		return nil
	})
}

func (r *PostgresRepository) BeginDispatch(ctx context.Context, job Job, id string) error {
	return r.mutate(ctx, job, func(ctx context.Context, tx ds.Tx) error {
		result, err := tx.Exec(ctx, `UPDATE public.yclients_operations SET state='dispatched',dispatched_at=now(),updated_at=now()
			WHERE order_id=$1 AND id=$2 AND state='pending' AND dispatched_at IS NULL`, job.Order.ID, id)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("settlement operation is already dispatched")
		}
		return nil
	})
}

func (r *PostgresRepository) SaveOutcome(ctx context.Context, job Job, op Operation, outcome Outcome) error {
	if outcome.State != "succeeded" && outcome.State != "unknown" && outcome.State != "failed" {
		return errors.New("invalid settlement outcome")
	}
	return r.mutate(ctx, job, func(ctx context.Context, tx ds.Tx) error {
		var value any
		if len(outcome.Result) > 0 {
			if !json.Valid(outcome.Result) {
				return errors.New("invalid settlement response")
			}
			value = string(outcome.Result)
		}
		result, err := tx.Exec(ctx, `UPDATE public.yclients_operations SET state=$3,result=$4::jsonb,error_code=NULLIF($5,''),updated_at=now()
			WHERE order_id=$1 AND id=$2 AND state IN ('dispatched','unknown') AND dispatched_at IS NOT NULL`, job.Order.ID, op.ID, outcome.State, value, outcome.ErrorCode)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("settlement outcome no longer belongs to an active operation")
		}
		return nil
	})
}

func (r *PostgresRepository) Finish(ctx context.Context, job Job, state, code string) error {
	allowed := map[string]bool{"pending": true, "succeeded": true, "demo": true, "unknown": true, "failed": true, "blocked": true}
	if !allowed[state] {
		return errors.New("invalid settlement job outcome")
	}
	return r.mutate(ctx, job, func(ctx context.Context, tx ds.Tx) error {
		if state == "succeeded" {
			var incomplete int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.yclients_operations WHERE order_id=$1 AND state<>'succeeded'`, job.Order.ID).Scan(&incomplete); err != nil {
				return err
			}
			if incomplete > 0 {
				return errors.New("settlement operations are not all confirmed")
			}
		}
		_, err := tx.Exec(ctx, `UPDATE public.yclients_jobs SET state=$3,error_code=NULLIF($4,''),
			next_attempt_at=CASE WHEN $3 IN ('pending','unknown') THEN now()+interval '30 seconds' ELSE NULL END,
			completed_at=CASE WHEN $3 IN ('succeeded','demo') THEN now() ELSE NULL END,updated_at=now()
			WHERE order_id=$1 AND claimed_at=$2 AND state='processing'`, job.Order.ID, job.ClaimedAt, state, code)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE public.payment_orders SET yclients_sync_status=$2 WHERE id=$1 AND state='paid'`, job.Order.ID, state)
		return err
	})
}

func (r *PostgresRepository) mutate(ctx context.Context, job Job, callback func(context.Context, ds.Tx) error) error {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	return ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		if err := lockClaim(ctx, tx, job); err != nil {
			return err
		}
		return callback(ctx, tx)
	})
}

func lockClaim(ctx context.Context, tx ds.Tx, job Job) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT order_id::text FROM public.yclients_jobs WHERE order_id=$1 AND claimed_at=$2 AND state='processing' FOR UPDATE`, job.Order.ID, job.ClaimedAt).Scan(&id)
	if errors.Is(err, ds.ErrNoRows) {
		return errors.New("settlement claim no longer belongs to this worker")
	}
	return err
}

func (r *PostgresRepository) CheckoutProfile(ctx context.Context, id string) (Profile, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Profile{}, err
	}
	defer release()
	var raw []byte
	if err := conn.QueryRow(ctx, `SELECT yclients_snapshot FROM public.payment_orders WHERE id=$1`, id).Scan(&raw); err != nil {
		return Profile{}, err
	}
	var profile Profile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return Profile{}, errors.New("invalid frozen settlement profile")
	}
	return profile, nil
}

func (r *PostgresRepository) ValidateCheckout(ctx context.Context, order checkout.Order) error {
	profile, err := r.CheckoutProfile(ctx, order.ID)
	if err != nil {
		return err
	}
	if err := ValidateCheckout(order, profile); err != nil {
		return webapp.Conflict("checkout cannot be synchronized with the configured YCLIENTS profile", map[string]any{"code": "yclients_settlement_unavailable"})
	}
	return nil
}

func (r *PostgresRepository) HeldPaidVisits(ctx context.Context, companyID, clientID int64) ([]VisitReference, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := conn.Query(ctx, `SELECT DISTINCT o.visit_id,o.record_id FROM public.payment_orders o
		JOIN public.payments p ON p.order_id=o.id AND p.state='paid' AND p.environment='production'
		WHERE o.company_id=$1 AND o.client_id=$2 AND o.visit_id IS NOT NULL AND o.state='paid' AND o.yclients_sync_status<>'succeeded'`, companyID, clientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []VisitReference
	for rows.Next() {
		var ref VisitReference
		if err := rows.Scan(&ref.VisitID, &ref.RecordID); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

// RetrySafe only requeues a job whose financial operations were never sent.
// Dispatched or unknown writes require reconciliation, never an automatic POST.
func (r *PostgresRepository) RetrySafe(ctx context.Context, id string) error {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	return ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		result, err := tx.Exec(ctx, `UPDATE public.yclients_jobs j SET state='pending',next_attempt_at=now(),error_code=NULL,updated_at=now()
			WHERE order_id=$1 AND state IN ('failed','blocked') AND NOT EXISTS(
				SELECT 1 FROM public.yclients_operations op WHERE op.order_id=j.order_id AND op.dispatched_at IS NOT NULL)`, id)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("only a never-dispatched settlement can be retried safely")
		}
		_, err = tx.Exec(ctx, `UPDATE public.payment_orders SET yclients_sync_status='pending' WHERE id=$1 AND state='paid'`, id)
		return err
	})
}

func (r *PostgresRepository) connection(ctx context.Context) (ds.Conn, func(), error) {
	if r == nil || r.db == nil {
		return nil, nil, errors.New("settlement database is not configured")
	}
	lease, server, err := r.db.GetPrimary(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire settlement database: %w", err)
	}
	return lease.Conn(), func() { r.db.Release(lease, server) }, nil
}
