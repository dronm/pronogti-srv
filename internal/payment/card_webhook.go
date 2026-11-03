package payment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dronm/ds/v4"
	"github.com/dronm/paytochkabank"
)

type VerifiedCardNotification struct {
	EventKey, CustomerCode, MerchantID, OperationID, PaymentLinkID, PaymentType, Status string
	AmountKopecks                                                                       int64
	Payload                                                                             json.RawMessage
}

type CardWebhookReceiver func(context.Context, int64, VerifiedCardNotification) error

func verifiedCardNotification(event *paytochkabank.WebhookEvent) (VerifiedCardNotification, error) {
	message, err := event.AcquiringPayment()
	if err != nil || message == nil || message.CustomerCode == "" || message.MerchantID == "" || message.OperationID == "" || message.Amount <= 0 || message.Status == "" {
		return VerifiedCardNotification{}, errors.New("acquiring payment identity is incomplete")
	}
	digest := sha256.Sum256(event.Payload)
	return VerifiedCardNotification{EventKey: hex.EncodeToString(digest[:]), CustomerCode: message.CustomerCode, MerchantID: message.MerchantID, OperationID: message.OperationID, PaymentLinkID: message.PaymentLinkID, PaymentType: message.PaymentType, Status: string(message.Status), AmountKopecks: int64(message.Amount), Payload: append(json.RawMessage(nil), event.Payload...)}, nil
}

// ReceiveCard journals signed events before attempting correlation. A callback
// can recover the bank operation ID, but only an authenticated detail read may
// mark the payment paid. Browser redirects never enter this path.
func (r *PostgresRepository) ReceiveCard(ctx context.Context, profileID int64, message VerifiedCardNotification) error {
	if profileID < 0 || message.EventKey == "" || len(message.EventKey) > 128 || message.CustomerCode == "" || message.MerchantID == "" || message.OperationID == "" || len(message.OperationID) > 128 || (message.PaymentType != "" && message.PaymentType != "card") || message.AmountKopecks <= 0 || !json.Valid(message.Payload) {
		return errors.New("invalid verified acquiring notification")
	}
	snapshot, err := json.Marshal(message)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%d:%s", profileID, message.EventKey)
	var profile any
	if profileID > 0 {
		profile = profileID
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `INSERT INTO public.bank_card_webhook_inbox(event_key,payment_profile_id,payload,matching_snapshot)
		VALUES($1,$2,$3::jsonb,$4::jsonb) ON CONFLICT(event_key) DO NOTHING`, key, profile, string(message.Payload), string(snapshot))
	release()
	if err != nil {
		return err
	}
	return r.applyCardEvent(ctx, key)
}

func (r *PostgresRepository) ReplayCardEvents(ctx context.Context) error {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	rows, err := conn.Query(ctx, `WITH due AS (
		SELECT event_key FROM public.bank_card_webhook_inbox WHERE state='pending' AND next_matching_at<=now()
		ORDER BY next_matching_at,received_at FOR UPDATE SKIP LOCKED LIMIT 10
	) UPDATE public.bank_card_webhook_inbox i SET next_matching_at=now()+interval '30 seconds'
	FROM due WHERE i.event_key=due.event_key RETURNING i.event_key`)
	if err != nil {
		release()
		return err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			break
		}
		keys = append(keys, key)
	}
	if err == nil {
		err = rows.Err()
	}
	closeErr := rows.Close()
	release()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, key := range keys {
		if err = r.applyCardEvent(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

const cardNotificationMatchesSQL = `SELECT p.id::text FROM public.payments p
	WHERE p.method='card' AND ($1::bigint IS NULL OR p.payment_profile_id=$1)
	AND p.profile_snapshot->>'customer_code'=$2 AND p.profile_snapshot->>'acquiring_merchant_id'=$3
	AND p.amount_kopecks=$4 AND p.currency='RUB' AND ($6='' OR p.id::text=$6)
	AND (p.acquiring_operation_id=$5 OR (p.acquiring_operation_id IS NULL AND p.state IN ('creating','unknown') AND p.id::text=$6))
	ORDER BY p.id LIMIT 2`

func (r *PostgresRepository) applyCardEvent(ctx context.Context, key string) error {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	return ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		var snapshot json.RawMessage
		var profileID *int64
		err := tx.QueryRow(ctx, `SELECT matching_snapshot,payment_profile_id FROM public.bank_card_webhook_inbox WHERE event_key=$1 AND state='pending' FOR UPDATE`, key).Scan(&snapshot, &profileID)
		if errors.Is(err, ds.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var message VerifiedCardNotification
		if err = json.Unmarshal(snapshot, &message); err != nil {
			return err
		}
		ids, err := notificationIDs(ctx, tx, cardNotificationMatchesSQL, profileID, message.CustomerCode, message.MerchantID, message.AmountKopecks, message.OperationID, message.PaymentLinkID)
		if err != nil {
			return err
		}
		if len(ids) != 1 {
			_, err = tx.Exec(ctx, `UPDATE public.bank_card_webhook_inbox SET next_matching_at=now()+interval '30 seconds',processing_error='payment_notification_unmatched' WHERE event_key=$1`, key)
			return err
		}
		current, err := lockConfirmationRecord(ctx, tx, ids[0])
		if err != nil {
			return err
		}
		a := current.Attempt
		profile, err := frozenConfirmationProfile(a)
		if err != nil {
			return err
		}
		if methodFor(a) != "card" || profile.CustomerCode != message.CustomerCode || profile.AcquiringMerchantID != message.MerchantID || a.AmountKopecks != message.AmountKopecks || a.Currency != "RUB" || (a.OperationID != "" && a.OperationID != message.OperationID) || (profileID != nil && *profileID != a.PaymentProfileID) {
			return errors.New("acquiring notification registration changed")
		}
		if a.OperationID == "" && message.PaymentLinkID != a.ID {
			return errors.New("acquiring notification lacks the original attempt reference")
		}
		_, err = tx.Exec(ctx, `UPDATE public.payments SET acquiring_operation_id=COALESCE(acquiring_operation_id,$2),next_check_at=CASE WHEN state='paid' THEN NULL ELSE now() END
			WHERE id=$1 AND method='card' AND (acquiring_operation_id IS NULL OR acquiring_operation_id=$2)`, a.ID, message.OperationID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO public.payment_events(environment,payment_id,payment_profile_id,event_type,source,deduplication_key,signature_verified,payload,matching_snapshot,processing_state,processed_at)
			VALUES($1,$2,$3,'acquiringInternetPayment','webhook',$4,true,$5::jsonb,$6::jsonb,'processed',now())
			ON CONFLICT(provider,environment,deduplication_key) DO NOTHING`, a.Environment, a.ID, a.PaymentProfileID, "card-webhook:"+key, string(message.Payload), string(snapshot))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE public.bank_card_webhook_inbox SET state='processed',processing_error=NULL WHERE event_key=$1`, key)
		return err
	})
}
