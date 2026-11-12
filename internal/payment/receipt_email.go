package payment

import (
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/receiptmail"
	"github.com/dronm/webapp"
)

type ReceiptEmailDependencies struct {
	RegistrationNumber func(driverBaseURL, deviceID string) string
	Send               func(ctx context.Context, email, receiptURL, dateTime string) error
}

type receiptEmailRepository interface {
	ClaimReceiptEmail(context.Context, checkout.Owner, string) (bool, error)
}

var receiptRegistrationNumber = regexp.MustCompile(`^[0-9]{11,16}$`)

func (s *Service) sabyReceiptURL(completion Completion) string {
	mail, receipt := s.dependencies.ReceiptEmail, completion.Receipt
	if mail == nil || mail.Send == nil || mail.RegistrationNumber == nil || receipt == nil ||
		receipt.Kind != "fiscal" || receipt.FiscalSign == nil {
		return ""
	}
	if _, ok := s.dependencies.Repository.(receiptEmailRepository); !ok {
		return ""
	}
	number := mail.RegistrationNumber(completion.receiptDriverBaseURL, completion.receiptDeviceID)
	const fiscalDateLayout = "2006-01-02T15:04"
	date, err := time.Parse(fiscalDateLayout, receipt.DateTime)
	if err != nil || date.Format(fiscalDateLayout) != receipt.DateTime || !receiptRegistrationNumber.MatchString(number) {
		return ""
	}
	return "https://saby.ru/ofd/check/rec/" + number + "/" + date.Format("020106") + "/" +
		strconv.FormatUint(uint64(*receipt.FiscalSign), 10)
}

func (s *Service) SendReceiptEmail(ctx context.Context, actor checkout.Actor, id, address string) error {
	payment, err := s.Get(ctx, actor, id)
	if err != nil {
		return err
	}
	if payment.receiptEmailURL == "" {
		return paymentConflict("receipt email is not available for this payment", "receipt_email_unavailable")
	}
	email, err := receiptmail.NormalizeAddress(address)
	if err != nil {
		return webapp.BadRequest("a valid email address is required", map[string]any{"code": "receipt_email_invalid"})
	}
	repository := s.dependencies.Repository.(receiptEmailRepository)
	claimed, err := repository.ClaimReceiptEmail(ctx, ownerFor(actor), id)
	if err != nil {
		return err
	}
	if !claimed {
		return webapp.NewHTTPError(429, "too_many_requests", "receipt email send limit reached", nil)
	}
	if err := s.validateActor(ctx, actor); err != nil {
		return err
	}
	if err := s.dependencies.ReceiptEmail.Send(ctx, email, payment.receiptEmailURL, payment.Receipt.DateTime); err != nil {
		slog.Warn("receipt email delivery failed", "error", err)
		return webapp.NewHTTPError(502, "receipt_email_failed", "could not send the receipt email", nil)
	}
	return nil
}

// Reserve a send attempt atomically so simultaneous requests and restarts
// cannot bypass the cooldown or the per-receipt cap.
func (r *PostgresRepository) ClaimReceiptEmail(ctx context.Context, owner checkout.Owner, id string) (bool, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	result, err := conn.Exec(ctx, `UPDATE public.fiscal_receipts f
		SET email_attempts=f.email_attempts+1,email_attempted_at=now()
		FROM public.payments p,public.payment_orders o
		WHERE p.id=$1 AND f.payment_id=p.id AND f.order_id=p.order_id AND o.id=p.order_id
		AND p.state='paid' AND o.state='paid' AND p.environment='production' AND f.state='issued'
		AND o.company_id=$2 AND COALESCE(o.kiosk_id,0)=$3 AND o.entry_mode=$4
		AND COALESCE(o.client_id,0)=$5 AND o.scope_hash=$6 AND o.customer_session_hash=$7
		AND f.email_attempts<3 AND (f.email_attempted_at IS NULL OR f.email_attempted_at<=now()-interval '30 seconds')`,
		id, owner.CompanyID, paymentKioskID(owner.KioskID), owner.Mode, owner.ClientID, owner.ScopeHash, owner.CustomerSessionHash)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() == 1, nil
}
