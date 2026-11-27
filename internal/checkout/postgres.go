package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dronm/ds/v4"
)

// PostgresRepository stores append-only checkout pricing snapshots. It does not
// create payment attempts or contact a payment provider.
type PostgresRepository struct{ db ds.Provider }

func NewPostgresRepository(db ds.Provider) *PostgresRepository {
	return &PostgresRepository{db: db}
}

const orderColumns = `id::text, company_id, COALESCE(kiosk_id,0), entry_mode,
	COALESCE(client_id,0), scope_hash, customer_session_hash,
	COALESCE(visit_id,0), COALESCE(record_id,0), items_snapshot,
	subtotal_kopecks, bonus_kopecks, payable_kopecks, currency, state,
	idempotency_key, request_hash, expires_at, created_at, updated_at, yclients_sync_status,
	customer_contact, customer_name`

func (r *PostgresRepository) FindByKey(ctx context.Context, owner Owner, key string) (Order, error) {
	return r.find(ctx, `SELECT `+orderColumns+` FROM public.payment_orders
		WHERE company_id=$1 AND customer_session_hash=$2 AND idempotency_key=$3`,
		owner.CompanyID, owner.CustomerSessionHash, key)
}

func (r *PostgresRepository) Get(ctx context.Context, id string) (Order, error) {
	return r.find(ctx, `SELECT `+orderColumns+` FROM public.payment_orders WHERE id=$1`, id)
}

func (r *PostgresRepository) Create(ctx context.Context, order Order) (Order, error) {
	items, err := json.Marshal(order.Items)
	if err != nil {
		return Order{}, fmt.Errorf("encode checkout items: %w", err)
	}
	// The unique owner/key constraint elects a single winner even if requests
	// reach separate application processes. The no-op update returns that winner
	// without replacing its owner, amount, item snapshot, hash, or expiry.
	return r.find(ctx, `INSERT INTO public.payment_orders (
		id,company_id,kiosk_id,entry_mode,client_id,scope_hash,customer_session_hash,
		visit_id,record_id,items_snapshot,subtotal_kopecks,bonus_kopecks,payable_kopecks,
		currency,state,idempotency_key,request_hash,expires_at,created_at,updated_at,yclients_sync_status,
		customer_contact,customer_name
	) VALUES (
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23
	) ON CONFLICT (company_id,customer_session_hash,idempotency_key)
		DO UPDATE SET idempotency_key=payment_orders.idempotency_key
	RETURNING `+orderColumns,
		order.ID, order.Owner.CompanyID, order.Owner.KioskID, order.Owner.Mode,
		nullableID(order.Owner.ClientID), order.Owner.ScopeHash, order.Owner.CustomerSessionHash,
		nullableID(order.VisitID), nullableID(order.RecordID), string(items),
		order.SubtotalKopecks, order.BonusKopecks, order.PayableKopecks,
		order.Currency, order.State, order.IdempotencyKey, order.RequestHash,
		order.ExpiresAt, order.CreatedAt, order.UpdatedAt, order.YClientsSyncStatus,
		order.CustomerContact, order.CustomerName)
}

func (r *PostgresRepository) find(ctx context.Context, query string, args ...any) (Order, error) {
	if r == nil || r.db == nil {
		return Order{}, errors.New("checkout database is not configured")
	}
	lease, server, err := r.db.GetPrimary(ctx)
	if err != nil {
		return Order{}, fmt.Errorf("acquire checkout database: %w", err)
	}
	defer r.db.Release(lease, server)
	var item Order
	var kioskID int64
	var snapshot []byte
	err = lease.Conn().QueryRow(ctx, query, args...).Scan(
		&item.ID, &item.Owner.CompanyID, &kioskID, &item.Owner.Mode,
		&item.Owner.ClientID, &item.Owner.ScopeHash, &item.Owner.CustomerSessionHash,
		&item.VisitID, &item.RecordID, &snapshot, &item.SubtotalKopecks,
		&item.BonusKopecks, &item.PayableKopecks, &item.Currency, &item.State,
		&item.IdempotencyKey, &item.RequestHash, &item.ExpiresAt, &item.CreatedAt,
		&item.UpdatedAt, &item.YClientsSyncStatus, &item.CustomerContact, &item.CustomerName,
	)
	if errors.Is(err, ds.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, fmt.Errorf("read checkout: %w", err)
	}
	if kioskID > 0 {
		item.Owner.KioskID = &kioskID
	}
	if err := json.Unmarshal(snapshot, &item.Items); err != nil {
		return Order{}, fmt.Errorf("decode checkout snapshot: %w", err)
	}
	if len(item.Items) == 0 {
		return Order{}, errors.New("checkout snapshot contains no items")
	}
	return item, nil
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
