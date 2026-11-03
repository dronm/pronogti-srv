package fiscal

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

// claimSQL atomically elects one worker. Undispatched claims can be recovered
// quickly because BeginDispatch fences the old owner. A potentially dispatched
// claim is looked up after a lease longer than the maximum HTTP timeout.
const claimSQL = `WITH candidate AS (
	SELECT r.order_id, r.state AS previous_state
	FROM public.fiscal_receipts r
	JOIN public.payment_orders o ON o.id=r.order_id AND o.company_id=r.company_id
	JOIN public.payments p ON p.id=r.payment_id AND p.order_id=o.id
	WHERE o.state='paid' AND p.state='paid' AND (
		r.state='pending'
		OR (r.state='unknown' AND r.claimed_at < now()-interval '30 seconds')
		OR (r.state='processing' AND r.dispatch_started_at IS NULL AND r.claimed_at < now()-interval '30 seconds')
		OR (r.state='processing' AND r.dispatch_started_at IS NOT NULL AND r.claimed_at < now()-interval '10 minutes')
	)
	ORDER BY r.created_at FOR UPDATE OF r SKIP LOCKED LIMIT 1
), claimed AS (
	UPDATE public.fiscal_receipts r SET state='processing',claimed_at=now(),updated_at=now()
	FROM candidate c WHERE r.order_id=c.order_id
	RETURNING r.*, c.previous_state
)
SELECT c.order_id::text,c.company_id,c.payment_id::text,c.environment,c.fiscal_snapshot,
	c.request_id,c.previous_state,c.request_payload,c.claimed_at,c.dispatch_started_at,o.customer_contact,
	o.items_snapshot,o.subtotal_kopecks,o.bonus_kopecks,o.payable_kopecks,o.currency,o.state
FROM claimed c JOIN public.payment_orders o ON o.id=c.order_id`

func (r *PostgresRepository) Claim(ctx context.Context) (Job, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Job{}, err
	}
	defer release()
	var job Job
	var profile, items []byte
	err = conn.QueryRow(ctx, claimSQL).Scan(&job.Order.ID, &job.Order.Owner.CompanyID, &job.PaymentID, &job.Environment, &profile,
		&job.RequestID, &job.PreviousState, &job.Payload, &job.ClaimedAt, &job.DispatchStartedAt, &job.CustomerContact, &items, &job.Order.SubtotalKopecks,
		&job.Order.BonusKopecks, &job.Order.PayableKopecks, &job.Order.Currency, &job.Order.State)
	if errors.Is(err, ds.ErrNoRows) {
		return Job{}, ErrNoJob
	}
	if err != nil {
		return Job{}, fmt.Errorf("claim receipt: %w", err)
	}
	if err = json.Unmarshal(profile, &job.Profile); err != nil {
		return Job{}, errors.New("invalid receipt configuration snapshot")
	}
	if err = json.Unmarshal(items, &job.Order.Items); err != nil {
		return Job{}, errors.New("invalid receipt item snapshot")
	}
	job.Order.CustomerContact = job.CustomerContact
	return job, nil
}

func (r *PostgresRepository) SavePayload(ctx context.Context, job Job, payload json.RawMessage) error {
	return r.update(ctx, `UPDATE public.fiscal_receipts SET request_payload=$3::jsonb,updated_at=now()
		WHERE order_id=$1 AND claimed_at=$2 AND state='processing'`, job.Order.ID, job.ClaimedAt, string(payload))
}

func (r *PostgresRepository) BeginDispatch(ctx context.Context, job Job) error {
	return r.update(ctx, `UPDATE public.fiscal_receipts SET dispatch_started_at=now(),updated_at=now()
		WHERE order_id=$1 AND claimed_at=$2 AND state='processing' AND dispatch_started_at IS NULL
		AND request_payload IS NOT NULL`, job.Order.ID, job.ClaimedAt)
}

// RetrySafe only repairs local failures that provably never started a driver
// request. Unknown or potentially dispatched outcomes require journal review;
// this method never changes their state, payload or request identity.
func (r *PostgresRepository) RetrySafe(ctx context.Context, orderID string) error {
	return r.update(ctx, `UPDATE public.fiscal_receipts r
		SET state='pending',claimed_at=NULL,completed_at=NULL,error_code=NULL,updated_at=now()
		FROM public.payment_orders o,public.payments p
		WHERE r.order_id=$1 AND r.state='failed' AND r.dispatch_started_at IS NULL
		AND o.id=r.order_id AND o.company_id=r.company_id AND o.state='paid'
		AND p.id=r.payment_id AND p.order_id=o.id AND p.company_id=o.company_id
		AND p.environment=r.environment AND p.state='paid'`, orderID)
}

func (r *PostgresRepository) Finish(ctx context.Context, job Job, state string, response json.RawMessage, code string) error {
	var encoded any
	if len(response) > 0 {
		encoded = string(response)
	}
	return r.update(ctx, `UPDATE public.fiscal_receipts SET state=$3,response_payload=$4::jsonb,error_code=NULLIF($5,''),
		completed_at=CASE WHEN $3 IN ('demo','issued','failed') THEN now() ELSE NULL END,updated_at=now()
		WHERE order_id=$1 AND claimed_at=$2 AND state='processing'`, job.Order.ID, job.ClaimedAt, state, encoded, code)
}

func (r *PostgresRepository) update(ctx context.Context, sql string, args ...any) error {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	result, err := conn.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("persist receipt outcome: %w", err)
	}
	if result.RowsAffected() != 1 {
		return errors.New("receipt claim is no longer owned by this worker")
	}
	return nil
}

func (r *PostgresRepository) connection(ctx context.Context) (ds.Conn, func(), error) {
	if r == nil || r.db == nil {
		return nil, nil, errors.New("receipt database is not configured")
	}
	lease, server, err := r.db.GetPrimary(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire receipt database: %w", err)
	}
	return lease.Conn(), func() { r.db.Release(lease, server) }, nil
}

// ValidateCheckout checks the frozen profile after checkout ownership has been
// verified by the checkout service. It never communicates with the driver.
func (r *PostgresRepository) ValidateCheckout(ctx context.Context, order checkout.Order) error {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	var snapshot []byte
	var customerContact string
	if err = conn.QueryRow(ctx, `SELECT fiscal_snapshot,customer_contact FROM public.payment_orders WHERE id=$1`, order.ID).Scan(&snapshot, &customerContact); err != nil {
		return fmt.Errorf("read checkout fiscal configuration: %w", err)
	}
	var profile Profile
	if err = json.Unmarshal(snapshot, &profile); err != nil {
		return errors.New("invalid checkout fiscal configuration")
	}
	if _, err = BuildPayloadWithContact(order, profile, customerContact); err != nil {
		return webapp.Conflict("checkout cannot be represented by the configured receipt device", map[string]any{"code": "fiscal_receipt_unsupported"})
	}
	return nil
}
