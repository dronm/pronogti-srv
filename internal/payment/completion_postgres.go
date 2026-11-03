package payment

import (
	"context"
	"errors"

	"github.com/dronm/ds/v4"
	"github.com/dronm/pronogti/internal/checkout"
)

func (r *PostgresRepository) GetCompletion(ctx context.Context, owner checkout.Owner, id string) (Completion, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Completion{}, err
	}
	defer release()
	var result Completion
	err = conn.QueryRow(ctx, `SELECT o.state,o.yclients_sync_status,COALESCE(f.state,'not_started'),p.paid_at,
		COALESCE(p.ref_transaction_id,p.transaction_id,'') FROM public.payments p
		JOIN public.payment_orders o ON o.id=p.order_id
		LEFT JOIN public.fiscal_receipts f ON f.order_id=o.id
		WHERE p.id=$1 AND o.company_id=$2 AND COALESCE(o.kiosk_id,0)=$3 AND o.entry_mode=$4
		AND COALESCE(o.client_id,0)=$5 AND o.scope_hash=$6 AND o.customer_session_hash=$7`,
		id, owner.CompanyID, paymentKioskID(owner.KioskID), owner.Mode, owner.ClientID, owner.ScopeHash, owner.CustomerSessionHash).Scan(
		&result.OrderState, &result.YClientsSyncStatus, &result.FiscalStatus, &result.PaidAt, &result.BankReference)
	if errors.Is(err, ds.ErrNoRows) {
		return Completion{}, paymentNotFound()
	}
	return result, err
}
