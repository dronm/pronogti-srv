package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dronm/ds/v4"
)

const completeCardSQL = `UPDATE public.payments SET state=CASE WHEN state='paid' THEN 'paid' ELSE 'pending' END,
	acquiring_operation_id=$2,bank_payload=$3,bank_status=CASE WHEN state='paid' THEN bank_status ELSE $4 END,expires_at=LEAST(expires_at,$5),error_details=NULL
	WHERE id=$1 AND method='card' AND ((state IN ('creating','unknown') AND (acquiring_operation_id IS NULL OR acquiring_operation_id=$2)) OR (state IN ('pending','paid') AND acquiring_operation_id=$2))`

func (r *PostgresRepository) CompleteCard(ctx context.Context, id string, result CardResult) error {
	if result.OperationID == "" || result.CheckoutURL == "" || result.ExpiresAt.IsZero() {
		return errors.New("bank returned an incomplete card checkout")
	}
	return r.execOne(ctx, completeCardSQL, id, result.OperationID, result.CheckoutURL, result.Status, result.ExpiresAt)
}

const claimCardConfirmationSQL = `WITH due AS (
	SELECT p.id FROM public.payments p WHERE p.method='card' AND p.acquiring_operation_id IS NOT NULL
	AND p.state IN ('creating','pending','unknown','expired') AND (p.next_check_at IS NULL OR p.next_check_at<=now())
	AND (p.confirmation_claimed_at IS NULL OR p.confirmation_claimed_at<now()-interval '10 minutes')
	ORDER BY COALESCE(p.next_check_at,p.created_at),p.id FOR UPDATE SKIP LOCKED LIMIT 1
), claimed AS (
	UPDATE public.payments p SET confirmation_claimed_at=clock_timestamp(),next_check_at=now()+interval '15 seconds'
	FROM due WHERE p.id=due.id RETURNING p.*
)
SELECT ` + attemptColumns + `,p.confirmation_claimed_at,COALESCE(profile.jwt_env,''),profile.jwt_ciphertext
	FROM claimed p JOIN public.payment_profiles profile ON profile.id=p.payment_profile_id`

func (r *PostgresRepository) ClaimCardConfirmation(ctx context.Context) (ConfirmationCandidate, error) {
	if err := r.ReplayCardEvents(ctx); err != nil {
		return ConfirmationCandidate{}, err
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return ConfirmationCandidate{}, err
	}
	defer release()
	var c ConfirmationCandidate
	var env string
	var ciphertext []byte
	dest := append(confirmationAttemptDest(&c.Attempt), &c.ClaimedAt, &env, &ciphertext)
	err = conn.QueryRow(ctx, claimCardConfirmationSQL).Scan(dest...)
	if errors.Is(err, ds.ErrNoRows) {
		return ConfirmationCandidate{}, ErrNoConfirmation
	}
	if err != nil {
		return ConfirmationCandidate{}, fmt.Errorf("claim card confirmation: %w", err)
	}
	c.Profile, err = frozenConfirmationProfile(c.Attempt)
	if err != nil {
		return ConfirmationCandidate{}, err
	}
	c.Profile.JWTEnv, c.Profile.JWTCiphertext = env, ciphertext
	return c, nil
}

func sameCardProfile(a, b Profile) bool {
	return a.ID == b.ID && a.CompanyID == b.CompanyID && a.Environment == b.Environment && a.APIBaseURL == b.APIBaseURL && a.CustomerCode == b.CustomerCode && a.AcquiringMerchantID == b.AcquiringMerchantID
}

func (r *PostgresRepository) ObserveCard(ctx context.Context, c ConfirmationCandidate, proof CardObservation) error {
	a := c.Attempt
	if methodFor(a) != "card" || a.OperationID == "" || proof.OperationID != a.OperationID || proof.CustomerCode != c.Profile.CustomerCode || proof.MerchantID != c.Profile.AcquiringMerchantID || proof.AmountKopecks != a.AmountKopecks || a.Currency != "RUB" || (proof.PaymentType != "" && proof.PaymentType != "card") || (proof.Paid && proof.Status != "APPROVED") {
		return errors.New("card confirmation proof does not match its registration")
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	return ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		current, err := lockConfirmationRecord(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		actual := current.Attempt
		profile, err := frozenConfirmationProfile(actual)
		if err != nil || methodFor(actual) != "card" || actual.OperationID != a.OperationID || actual.AmountKopecks != a.AmountKopecks || actual.OrderID != a.OrderID || actual.CompanyID != a.CompanyID || !sameCardProfile(profile, c.Profile) {
			return errors.New("card confirmation registration changed")
		}
		if actual.State == "paid" {
			return nil
		}
		if current.ClaimedAt == nil || !current.ClaimedAt.Equal(c.ClaimedAt) {
			return errors.New("card confirmation claim is no longer current")
		}
		payload, _ := json.Marshal(map[string]any{"payment_id": a.ID, "operation_id": proof.OperationID, "status": proof.Status, "amount_kopecks": proof.AmountKopecks, "customer_code": proof.CustomerCode, "merchant_id": proof.MerchantID})
		_, err = tx.Exec(ctx, `INSERT INTO public.payment_events(environment,payment_id,payment_profile_id,event_type,source,deduplication_key,signature_verified,payload,processing_state,processed_at)
			VALUES($1,$2,$3,'acquiringInternetPayment','reconciliation',$4,false,$5::jsonb,'processed',now()) ON CONFLICT(provider,environment,deduplication_key) DO NOTHING`, a.Environment, a.ID, a.PaymentProfileID, "card-poll:"+a.ID+":"+proof.Status, string(payload))
		if err != nil {
			return err
		}
		if !proof.Paid {
			_, err = tx.Exec(ctx, `UPDATE public.payments SET bank_status=$2,last_checked_at=now(),next_check_at=now()+interval '15 seconds',confirmation_claimed_at=NULL,error_details=NULL WHERE id=$1 AND state<>'paid'`, a.ID, proof.Status)
			return err
		}
		return settleCardPayment(ctx, tx, current, proof)
	})
}

func settleCardPayment(ctx context.Context, tx ds.Tx, current confirmationRecord, proof CardObservation) error {
	a := current.Attempt
	var other string
	err := tx.QueryRow(ctx, `SELECT id::text FROM public.payments WHERE order_id=$1 AND state='paid' AND id<>$2 LIMIT 1`, a.OrderID, a.ID).Scan(&other)
	if err == nil {
		return errors.New("checkout already has a different successful payment")
	}
	if !errors.Is(err, ds.ErrNoRows) {
		return err
	}
	if _, err = tx.Exec(ctx, settleConfirmedOrderSQL, a.OrderID, a.CompanyID); err != nil {
		return err
	}
	// The acquiring operation ID is the native stable reference. Direct SBP
	// refund references are deliberately not invented for card payments.
	_, err = tx.Exec(ctx, `UPDATE public.payments SET state='paid',bank_status='APPROVED',transaction_id=COALESCE(NULLIF($2,''),acquiring_operation_id),
			ref_transaction_id=COALESCE(NULLIF($3,''),acquiring_operation_id),paid_at=COALESCE(paid_at,now()),last_checked_at=now(),next_check_at=NULL,confirmation_claimed_at=NULL,error_details=NULL
			WHERE id=$1 AND method='card' AND state<>'paid'`, a.ID, proof.TransactionID, proof.PaymentID)
	return err
}

func confirmMockCard(ctx context.Context, tx ds.Tx, current confirmationRecord) error {
	a := current.Attempt
	if a.Environment != "mock" || methodFor(a) != "card" || a.OperationID == "" || a.AmountKopecks <= 0 || a.Currency != "RUB" {
		return errors.New("demo confirmation requires a registered mock card checkout")
	}
	profile, err := frozenConfirmationProfile(a)
	if err != nil || profile.Environment != "mock" {
		return errors.New("invalid mock card registration")
	}
	if a.State == "paid" {
		return nil
	}
	proof := CardObservation{OperationID: a.OperationID, Status: "APPROVED", Paid: true, CustomerCode: profile.CustomerCode, MerchantID: profile.AcquiringMerchantID, AmountKopecks: a.AmountKopecks, PaymentType: "card"}
	payload, _ := json.Marshal(map[string]any{"source": "server_mock", "operation_id": a.OperationID, "payment_id": a.ID, "status": "APPROVED"})
	if _, err = tx.Exec(ctx, `INSERT INTO public.payment_events(environment,payment_id,payment_profile_id,event_type,source,deduplication_key,signature_verified,payload,processing_state,processed_at)
		VALUES($1,$2,$3,'acquiringInternetPayment','reconciliation',$4,false,$5::jsonb,'processed',now()) ON CONFLICT(provider,environment,deduplication_key) DO NOTHING`, a.Environment, a.ID, a.PaymentProfileID, "card-demo:"+a.ID, string(payload)); err != nil {
		return err
	}
	return settleCardPayment(ctx, tx, current, proof)
}
