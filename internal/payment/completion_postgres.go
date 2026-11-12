package payment

import (
	"context"
	"encoding/json"
	"errors"
	"time"

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
	var itemsSnapshot, receiptDetails []byte
	var totalKopecks int64
	var currency string
	err = conn.QueryRow(ctx, `SELECT o.state,o.yclients_sync_status,COALESCE(f.state,'not_started'),p.paid_at,
		COALESCE(p.ref_transaction_id,p.transaction_id,''),o.payable_kopecks,o.currency,
		CASE WHEN p.state='paid' AND f.payment_id=p.id AND f.state IN ('issued','demo') THEN o.items_snapshot END,
		CASE WHEN p.state='paid' AND f.payment_id=p.id AND f.state='issued' THEN f.response_payload #> '{data,receipt}' END,
		COALESCE(f.fiscal_snapshot->>'driver_base_url',''),COALESCE(f.fiscal_snapshot->>'device_id','')
		FROM public.payments p
		JOIN public.payment_orders o ON o.id=p.order_id
		LEFT JOIN public.fiscal_receipts f ON f.order_id=o.id
		WHERE p.id=$1 AND o.company_id=$2 AND COALESCE(o.kiosk_id,0)=$3 AND o.entry_mode=$4
		AND COALESCE(o.client_id,0)=$5 AND o.scope_hash=$6 AND o.customer_session_hash=$7`,
		id, owner.CompanyID, paymentKioskID(owner.KioskID), owner.Mode, owner.ClientID, owner.ScopeHash, owner.CustomerSessionHash).Scan(
		&result.OrderState, &result.YClientsSyncStatus, &result.FiscalStatus, &result.PaidAt, &result.BankReference,
		&totalKopecks, &currency, &itemsSnapshot, &receiptDetails, &result.receiptDriverBaseURL, &result.receiptDeviceID)
	if errors.Is(err, ds.ErrNoRows) {
		return Completion{}, paymentNotFound()
	}
	if err != nil {
		return Completion{}, err
	}
	result.Receipt = receiptView(result.FiscalStatus, itemsSnapshot, totalKopecks, currency, receiptDetails)
	return result, nil
}

func receiptView(state string, itemsSnapshot []byte, totalKopecks int64, currency string, receiptDetails []byte) *ReceiptView {
	if (state != "issued" && state != "demo") || len(itemsSnapshot) == 0 || totalKopecks <= 0 || currency == "" {
		return nil
	}
	var items []checkout.Item
	if err := json.Unmarshal(itemsSnapshot, &items); err != nil || len(items) == 0 {
		return nil
	}
	view := &ReceiptView{Kind: "demo", TotalKopecks: totalKopecks, Currency: currency, Items: make([]ReceiptItemView, 0, len(items))}
	for _, item := range items {
		if item.Title == "" || item.Quantity == "" || item.UnitPriceKopecks <= 0 || item.AmountKopecks <= 0 {
			return nil
		}
		view.Items = append(view.Items, ReceiptItemView{
			Name: item.Title, Quantity: item.Quantity, UnitPriceKopecks: item.UnitPriceKopecks, AmountKopecks: item.AmountKopecks,
		})
	}
	if state == "demo" {
		return view
	}
	view.Kind = "fiscal"
	var details struct {
		DateTime       string  `json:"dateTime"`
		FiscalSign     *uint32 `json:"fiscalSign"`
		DocumentNumber *uint32 `json:"documentNumber"`
		ReceiptNumber  *uint16 `json:"receiptNumber"`
		ShiftNumber    *uint16 `json:"shiftNumber"`
	}
	if json.Unmarshal(receiptDetails, &details) == nil {
		if _, err := time.Parse("2006-01-02T15:04", details.DateTime); err == nil {
			view.DateTime = details.DateTime
		}
		view.FiscalSign, view.DocumentNumber = details.FiscalSign, details.DocumentNumber
		view.ReceiptNumber, view.ShiftNumber = details.ReceiptNumber, details.ShiftNumber
	}
	return view
}
