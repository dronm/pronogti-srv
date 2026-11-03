package payment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dronm/ds/v4"
)

// A claim is a short database lease, never a transaction held across bank I/O.
// Profiles may be disabled or credentials rotated after issuing a QR: neither
// operation cancels funds already in flight or changes its registration identity.
const claimConfirmationSQL = `WITH due AS (
	SELECT p.id FROM public.payments p
	WHERE p.method='sbp' AND p.state IN ('creating','pending','unknown','expired') AND p.qrc_id IS NOT NULL
	AND (p.next_check_at IS NULL OR p.next_check_at<=now())
	AND (p.confirmation_claimed_at IS NULL OR p.confirmation_claimed_at<now()-interval '10 minutes')
	ORDER BY COALESCE(p.next_check_at,p.created_at),p.id FOR UPDATE SKIP LOCKED LIMIT 1
), claimed AS (
	UPDATE public.payments p SET confirmation_claimed_at=clock_timestamp(),next_check_at=now()+interval '15 seconds'
	FROM due WHERE p.id=due.id RETURNING p.*
)
SELECT ` + attemptColumns + `,p.confirmation_claimed_at,COALESCE(profile.jwt_env,'') AS jwt_env,profile.jwt_ciphertext
	FROM claimed p JOIN public.payment_profiles profile ON profile.id=p.payment_profile_id`

func (r *PostgresRepository) ClaimConfirmation(ctx context.Context) (ConfirmationCandidate, error) {
	if err := r.ReplayPendingEvents(ctx); err != nil {
		return ConfirmationCandidate{}, err
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return ConfirmationCandidate{}, err
	}
	defer release()
	var item ConfirmationCandidate
	var jwtEnv string
	var ciphertext []byte
	dest := confirmationAttemptDest(&item.Attempt)
	dest = append(dest, &item.ClaimedAt, &jwtEnv, &ciphertext)
	if err = conn.QueryRow(ctx, claimConfirmationSQL).Scan(dest...); errors.Is(err, ds.ErrNoRows) {
		return ConfirmationCandidate{}, ErrNoConfirmation
	} else if err != nil {
		return ConfirmationCandidate{}, fmt.Errorf("claim bank confirmation: %w", err)
	}
	item.Profile, err = frozenConfirmationProfile(item.Attempt)
	if err != nil {
		return ConfirmationCandidate{}, err
	}
	item.Profile.JWTEnv, item.Profile.JWTCiphertext = jwtEnv, ciphertext
	return item, nil
}

const releaseConfirmationSQL = `UPDATE public.payments SET confirmation_claimed_at=NULL,
	next_check_at=now()+interval '15 seconds',last_checked_at=now(),
	error_details=jsonb_build_object('code',$3::text)
	WHERE id=$1 AND confirmation_claimed_at=$2 AND state<>'paid'`

func (r *PostgresRepository) ReleaseConfirmation(ctx context.Context, candidate ConfirmationCandidate, code string) error {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	// Zero affected rows also means a newer claim or a successful webhook won.
	// Neither outcome authorizes overwriting that newer state.
	_, err = conn.Exec(ctx, releaseConfirmationSQL, candidate.Attempt.ID, candidate.ClaimedAt, code)
	return err
}

type confirmationRecord struct {
	Attempt                         Attempt
	ClaimedAt                       *time.Time
	TransactionID, RefTransactionID string
}

const lockConfirmationOrderSQL = `SELECT o.id::text FROM public.payment_orders o
	JOIN public.payments p ON p.order_id=o.id WHERE p.id=$1 FOR UPDATE OF o`

func lockConfirmationRecord(ctx context.Context, tx ds.Tx, id string) (confirmationRecord, error) {
	// Reserve and settlement both acquire order before payment. This also
	// serializes different attempts for the same order and avoids lock inversion.
	var orderID string
	if err := tx.QueryRow(ctx, lockConfirmationOrderSQL, id).Scan(&orderID); err != nil {
		return confirmationRecord{}, err
	}
	var result confirmationRecord
	dest := confirmationAttemptDest(&result.Attempt)
	dest = append(dest, &result.ClaimedAt, &result.TransactionID, &result.RefTransactionID)
	err := tx.QueryRow(ctx, `SELECT `+attemptColumns+`,p.confirmation_claimed_at,
		COALESCE(p.transaction_id,''),COALESCE(p.ref_transaction_id,'')
		FROM public.payments p WHERE p.id=$1 FOR UPDATE OF p`, id).Scan(dest...)
	return result, err
}

func confirmationAttemptDest(a *Attempt) []any {
	return []any{&a.ID, &a.OrderID, &a.CompanyID, &a.PaymentProfileID, &a.Environment, &a.State,
		&a.IdempotencyKey, &a.RequestHash, &a.QRCID, &a.Payload, &a.Image, &a.ImageMediaType,
		&a.ProfileSnapshot, &a.AmountKopecks, &a.Currency, &a.ExpiresAt, &a.CreatedAt, &a.UpdatedAt, &a.ErrorCode, &a.Method, &a.OperationID}
}

func frozenConfirmationProfile(a Attempt) (Profile, error) {
	var p Profile
	if err := json.Unmarshal(a.ProfileSnapshot, &p); err != nil || p.ID != a.PaymentProfileID || p.CompanyID != a.CompanyID || p.Environment != a.Environment {
		return Profile{}, errors.New("bank confirmation registration snapshot is invalid")
	}
	return p, nil
}

func sameConfirmationRegistration(a, b Attempt) bool {
	return a.ID == b.ID && a.OrderID == b.OrderID && a.CompanyID == b.CompanyID &&
		a.PaymentProfileID == b.PaymentProfileID && a.Environment == b.Environment &&
		a.QRCID != "" && a.QRCID == b.QRCID && a.AmountKopecks > 0 && a.AmountKopecks == b.AmountKopecks &&
		a.Currency == "RUB" && a.Currency == b.Currency
}

func sameConfirmationProfile(a, b Profile) bool {
	return a.ID == b.ID && a.CompanyID == b.CompanyID && a.Environment == b.Environment &&
		a.APIBaseURL == b.APIBaseURL && a.CustomerCode == b.CustomerCode &&
		a.SBPMerchantID == b.SBPMerchantID && a.AccountID == b.AccountID
}

func (r *PostgresRepository) Observe(ctx context.Context, candidate ConfirmationCandidate, observation BankObservation) error {
	if !validConfirmationCandidate(candidate) || observation.QRCID != candidate.Attempt.QRCID ||
		(observation.Paid && (observation.Status != "Accepted" || observation.RefTransactionID == "")) {
		return errors.New("invalid bank confirmation proof")
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	return ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		current, err := lockConfirmationRecord(ctx, tx, candidate.Attempt.ID)
		if err != nil {
			return err
		}
		profile, err := frozenConfirmationProfile(current.Attempt)
		if err != nil || !sameConfirmationRegistration(candidate.Attempt, current.Attempt) || !sameConfirmationProfile(profile, candidate.Profile) {
			return errors.New("bank confirmation does not match frozen registration")
		}
		if current.Attempt.State == "paid" {
			if observation.Paid {
				return settleConfirmedPayment(ctx, tx, current, observation)
			}
			return nil
		}
		if current.ClaimedAt == nil || !current.ClaimedAt.Equal(candidate.ClaimedAt) {
			return errors.New("bank confirmation claim is no longer current")
		}
		if err := auditObservation(ctx, tx, current.Attempt, observation); err != nil {
			return err
		}
		if observation.Paid {
			return settleConfirmedPayment(ctx, tx, current, observation)
		}
		_, err = tx.Exec(ctx, `UPDATE public.payments SET bank_status=$2,last_checked_at=now(),
			next_check_at=now()+interval '15 seconds',confirmation_claimed_at=NULL,error_details=NULL
			WHERE id=$1 AND state<>'paid'`, current.Attempt.ID, observation.Status)
		return err
	})
}

const settleConfirmedOrderSQL = `UPDATE public.payment_orders SET state='paid',
	yclients_sync_status=CASE WHEN yclients_sync_status='not_started' THEN 'pending' ELSE yclients_sync_status END
	WHERE id=$1 AND company_id=$2`

const settleConfirmedPaymentSQL = `UPDATE public.payments SET state='paid',bank_status='Accepted',
	qrc_id=$2,transaction_id=COALESCE(transaction_id,NULLIF($3,'')),
	ref_transaction_id=COALESCE(ref_transaction_id,NULLIF($4,'')),paid_at=COALESCE(paid_at,now()),
	last_checked_at=now(),next_check_at=NULL,confirmation_claimed_at=NULL,error_details=NULL
	WHERE id=$1 AND state<>'paid'`

func settleConfirmedPayment(ctx context.Context, tx ds.Tx, current confirmationRecord, proof BankObservation) error {
	a := current.Attempt
	if proof.QRCID == "" || proof.Status != "Accepted" || !proof.Paid || a.AmountKopecks <= 0 || a.Currency != "RUB" {
		return errors.New("incomplete successful bank payment proof")
	}
	if a.QRCID != "" && a.QRCID != proof.QRCID {
		return errors.New("bank payment QR does not match registration")
	}
	if a.State == "paid" {
		if current.RefTransactionID != "" && proof.RefTransactionID != "" && current.RefTransactionID != proof.RefTransactionID {
			return errors.New("conflicting bank reference for paid QR")
		}
		if current.TransactionID != "" && proof.TransactionID != "" && current.TransactionID != proof.TransactionID {
			return errors.New("conflicting bank operation for paid QR")
		}
		if current.TransactionID == "" && proof.TransactionID != "" || current.RefTransactionID == "" && proof.RefTransactionID != "" {
			// Polling can confirm before the signed webhook provides operationId,
			// and a digital-ruble webhook can precede a later stable reference.
			// Fill missing metadata without repeating the paid transition/jobs.
			_, err := tx.Exec(ctx, `UPDATE public.payments SET transaction_id=COALESCE(transaction_id,NULLIF($2,'')),
				ref_transaction_id=COALESCE(ref_transaction_id,NULLIF($3,'')) WHERE id=$1 AND state='paid'`, a.ID, proof.TransactionID, proof.RefTransactionID)
			return err
		}
		return nil
	}
	// This check is under the order lock; a second paid attempt cannot enqueue
	// another receipt or silently attribute two transfers to one order.
	var other string
	err := tx.QueryRow(ctx, `SELECT id::text FROM public.payments WHERE order_id=$1 AND state='paid' AND id<>$2 LIMIT 1`, a.OrderID, a.ID).Scan(&other)
	if err == nil {
		return errors.New("checkout already has a different successful payment")
	}
	if !errors.Is(err, ds.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(ctx, settleConfirmedOrderSQL, a.OrderID, a.CompanyID); err != nil {
		return fmt.Errorf("confirm paid checkout: %w", err)
	}
	// The paid transition and both durable follow-up queues commit together.
	// The unique profile/bank-reference index rejects cross-order attribution.
	if _, err := tx.Exec(ctx, settleConfirmedPaymentSQL, a.ID, proof.QRCID, proof.TransactionID, proof.RefTransactionID); err != nil {
		return fmt.Errorf("confirm bank payment: %w", err)
	}
	return nil
}

func auditObservation(ctx context.Context, tx ds.Tx, a Attempt, proof BankObservation) error {
	// Never persist arbitrary transport bodies or credentials. The authenticated
	// registration identity and normalized result are sufficient for the audit.
	payload, err := json.Marshal(struct {
		PaymentID     string `json:"payment_id"`
		ProfileID     int64  `json:"payment_profile_id"`
		QRCID         string `json:"qrc_id"`
		Status        string `json:"status"`
		Paid          bool   `json:"paid"`
		TransactionID string `json:"transaction_id,omitempty"`
		Reference     string `json:"ref_transaction_id,omitempty"`
		PaymentType   string `json:"payment_type,omitempty"`
	}{a.ID, a.PaymentProfileID, proof.QRCID, proof.Status, proof.Paid, proof.TransactionID, proof.RefTransactionID, proof.PaymentType})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	_, err = tx.Exec(ctx, `INSERT INTO public.payment_events
		(environment,payment_id,payment_profile_id,event_type,source,deduplication_key,signature_verified,payload,processing_state,processed_at)
		VALUES($1,$2,$3,'sbpPaymentStatus','reconciliation',$4,false,$5::jsonb,'processed',now())
		ON CONFLICT(provider,environment,deduplication_key) DO NOTHING`,
		a.Environment, a.ID, a.PaymentProfileID, "poll:"+hex.EncodeToString(digest[:]), string(payload))
	return err
}

// Receive first commits the signed notification to a durable inbox. A matching
// failure never loses that journal, and duplicate delivery reuses the same row.
func (r *PostgresRepository) Receive(ctx context.Context, profileID int64, message VerifiedSBPNotification) error {
	if profileID <= 0 || message.EventKey == "" || len(message.EventKey) > 128 || message.QRCID == "" ||
		message.CustomerCode == "" || message.MerchantID == "" || message.AmountKopecks <= 0 || !json.Valid(message.Payload) {
		return errors.New("invalid verified bank notification")
	}
	snapshot, err := json.Marshal(message)
	if err != nil {
		return err
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	var environment string
	err = conn.QueryRow(ctx, `SELECT environment FROM public.payment_profiles WHERE id=$1`, profileID).Scan(&environment)
	if err != nil {
		release()
		return fmt.Errorf("resolve callback payment profile: %w", err)
	}
	var eventID int64
	err = conn.QueryRow(ctx, `INSERT INTO public.payment_events
		(environment,payment_profile_id,event_type,source,deduplication_key,signature_verified,payload,matching_snapshot)
		VALUES($1,$2,'incomingSbpPayment','webhook',$3,true,$4::jsonb,$5::jsonb)
		ON CONFLICT(provider,environment,deduplication_key) DO UPDATE SET deduplication_key=EXCLUDED.deduplication_key
		RETURNING id`, environment, profileID, fmt.Sprintf("profile:%d:%s", profileID, message.EventKey), string(message.Payload), string(snapshot)).Scan(&eventID)
	release()
	if err != nil {
		return fmt.Errorf("journal bank notification: %w", err)
	}
	return r.applyVerifiedEvent(ctx, eventID)
}

// ReplayPendingEvents also handles a notification received before registration
// persisted its qrcId. Each application has its own short connection and txn;
// unmatched events are rescheduled so bank registration test events cannot starve
// later real payments.
func (r *PostgresRepository) ReplayPendingEvents(ctx context.Context) error {
	if err := r.ReplayGenericEvents(ctx); err != nil {
		return err
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	rows, err := conn.Query(ctx, `SELECT id FROM public.payment_events WHERE source='webhook'
		AND signature_verified AND processing_state='pending' AND matching_snapshot IS NOT NULL
		AND next_matching_at<=now() ORDER BY next_matching_at,id LIMIT 10`)
	if err != nil {
		release()
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
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
	for _, id := range ids {
		if err := r.applyVerifiedEvent(ctx, id); err != nil {
			// Keep failed matching durable and fair. A duplicated bank reference
			// must not roll back its inbox or monopolize every worker iteration.
			saveCtx, cancel := persistenceContext(ctx)
			conn, release, acquireErr := r.connection(saveCtx)
			if acquireErr == nil {
				_ = postponeEvent(saveCtx, conn, id, "payment_matching_failed")
				release()
			}
			cancel()
			return err
		}
	}
	return nil
}

// ReceiveAny supports one bank callback URL for an API client shared by several
// payment profiles. Its first journal deliberately has no guessed environment:
// only an unambiguous frozen QR registration may supply that identity.
func (r *PostgresRepository) ReceiveAny(ctx context.Context, message VerifiedSBPNotification) error {
	if message.EventKey == "" || len(message.EventKey) > 128 || message.QRCID == "" || message.CustomerCode == "" || message.MerchantID == "" || message.AmountKopecks <= 0 || !json.Valid(message.Payload) {
		return errors.New("invalid verified bank notification")
	}
	snapshot, err := json.Marshal(message)
	if err != nil {
		return err
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `INSERT INTO public.bank_webhook_inbox(event_key,payload,matching_snapshot)
		VALUES($1,$2::jsonb,$3::jsonb) ON CONFLICT(event_key) DO NOTHING`, message.EventKey, string(message.Payload), string(snapshot))
	release()
	if err != nil {
		return fmt.Errorf("journal generic bank notification: %w", err)
	}
	return r.applyGenericEvent(ctx, message.EventKey)
}

func (r *PostgresRepository) ReplayGenericEvents(ctx context.Context) error {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	rows, err := conn.Query(ctx, `SELECT event_key FROM public.bank_webhook_inbox
		WHERE state='pending' AND next_matching_at<=now() ORDER BY next_matching_at,event_key LIMIT 10`)
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
		if err := r.applyGenericEvent(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

const genericNotificationMatchesSQL = `SELECT p.payment_profile_id FROM public.payments p
	WHERE p.method='sbp' AND p.profile_snapshot->>'customer_code'=$1 AND p.profile_snapshot->>'sbp_merchant_id'=$2
	AND p.amount_kopecks=$3 AND p.currency='RUB'
	AND (p.qrc_id=$4 OR (p.qrc_id IS NULL AND p.state IN ('creating','unknown') AND $5='Checkout '||p.order_id::text))
	ORDER BY p.id LIMIT 2`

func (r *PostgresRepository) applyGenericEvent(ctx context.Context, key string) error {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	var snapshot json.RawMessage
	err = conn.QueryRow(ctx, `UPDATE public.bank_webhook_inbox SET next_matching_at=now()+interval '30 seconds'
		WHERE event_key=$1 AND state='pending' RETURNING matching_snapshot`, key).Scan(&snapshot)
	if errors.Is(err, ds.ErrNoRows) {
		release()
		return nil
	}
	if err != nil {
		release()
		return err
	}
	var message VerifiedSBPNotification
	if err = json.Unmarshal(snapshot, &message); err != nil {
		release()
		return err
	}
	rows, err := conn.Query(ctx, genericNotificationMatchesSQL, message.CustomerCode, message.MerchantID, message.AmountKopecks, message.QRCID, message.Purpose)
	if err != nil {
		release()
		return err
	}
	var profiles []int64
	for rows.Next() {
		var profile int64
		if err = rows.Scan(&profile); err != nil {
			break
		}
		profiles = append(profiles, profile)
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
	if len(profiles) != 1 {
		return nil
	} // Signed registration test or ambiguous fixture.
	// No connection or transaction is retained across the per-profile journal.
	if err = r.Receive(ctx, profiles[0], message); err != nil {
		return err
	}
	conn, release, err = r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	_, err = conn.Exec(ctx, `UPDATE public.bank_webhook_inbox SET state='processed',processing_error=NULL WHERE event_key=$1`, key)
	return err
}

func (r *PostgresRepository) applyVerifiedEvent(ctx context.Context, eventID int64) error {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	return ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		var profileID int64
		var environment, state string
		var snapshot json.RawMessage
		err := tx.QueryRow(ctx, `SELECT payment_profile_id,environment,processing_state,matching_snapshot
			FROM public.payment_events WHERE id=$1 AND source='webhook' AND signature_verified FOR UPDATE`, eventID).Scan(&profileID, &environment, &state, &snapshot)
		if err != nil {
			return err
		}
		if state == "processed" || state == "ignored" {
			return nil
		}
		var message VerifiedSBPNotification
		if err := json.Unmarshal(snapshot, &message); err != nil {
			return finishIgnoredEvent(ctx, tx, eventID, "invalid_matching_snapshot")
		}
		var companyID int64
		var actualEnv, customer, merchant string
		err = tx.QueryRow(ctx, `SELECT company_id,environment,COALESCE(customer_code,''),COALESCE(sbp_merchant_id,'')
			FROM public.payment_profiles WHERE id=$1`, profileID).Scan(&companyID, &actualEnv, &customer, &merchant)
		if err != nil {
			return err
		}
		if actualEnv != environment || customer != message.CustomerCode || merchant != message.MerchantID {
			return finishIgnoredEvent(ctx, tx, eventID, "callback_profile_identity_mismatch")
		}
		id, err := notificationAttemptID(ctx, tx, profileID, environment, message)
		if err != nil {
			return err
		}
		if id == "" {
			return postponeEvent(ctx, tx, eventID, "payment_registration_not_found")
		}
		current, err := lockConfirmationRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		a := current.Attempt
		profile, err := frozenConfirmationProfile(a)
		if err != nil {
			return finishIgnoredEvent(ctx, tx, eventID, "invalid_registration_snapshot")
		}
		if a.PaymentProfileID != profileID || a.CompanyID != companyID || a.Environment != environment || a.Currency != "RUB" || a.AmountKopecks != message.AmountKopecks ||
			profile.CustomerCode != message.CustomerCode || profile.SBPMerchantID != message.MerchantID || (a.QRCID != "" && a.QRCID != message.QRCID) {
			return finishIgnoredEvent(ctx, tx, eventID, "payment_notification_mismatch")
		}
		if a.QRCID == "" && ((a.State != "creating" && a.State != "unknown") || message.Purpose != "Checkout "+a.OrderID) {
			return finishIgnoredEvent(ctx, tx, eventID, "payment_registration_correlation_changed")
		}
		if message.RefTransactionID == "" && message.TransactionID == "" {
			return postponeEvent(ctx, tx, eventID, "bank_reference_not_available")
		}
		proof := BankObservation{QRCID: message.QRCID, Status: "Accepted", Paid: true,
			TransactionID: message.TransactionID, RefTransactionID: message.RefTransactionID, PaymentType: message.PaymentType}
		if err := settleConfirmedPayment(ctx, tx, current, proof); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE public.payment_events SET payment_id=$2,processing_state='processed',processed_at=now(),processing_error=NULL WHERE id=$1`, eventID, a.ID)
		return err
	})
}

func notificationAttemptID(ctx context.Context, tx ds.Tx, profileID int64, environment string, message VerifiedSBPNotification) (string, error) {
	ids, err := notificationIDs(ctx, tx, `SELECT id::text FROM public.payments
		WHERE method='sbp' AND payment_profile_id=$1 AND environment=$2 AND qrc_id=$3 ORDER BY id LIMIT 2`, profileID, environment, message.QRCID)
	if err != nil {
		return "", err
	}
	if len(ids) == 1 {
		return ids[0], nil
	}
	if len(ids) > 1 {
		return "", nil
	} // Ambiguous sandbox fixtures never select by amount.
	orderID := strings.TrimPrefix(message.Purpose, "Checkout ")
	if message.Purpose != "Checkout "+orderID || !confirmationUUID(orderID) {
		return "", nil
	}
	ids, err = notificationIDs(ctx, tx, `SELECT id::text FROM public.payments
		WHERE method='sbp' AND payment_profile_id=$1 AND environment=$2 AND order_id=$3 AND amount_kopecks=$4
		AND currency='RUB' AND state IN ('creating','unknown') AND qrc_id IS NULL ORDER BY id LIMIT 2`, profileID, environment, orderID, message.AmountKopecks)
	if err != nil {
		return "", err
	}
	if len(ids) == 1 {
		return ids[0], nil
	}
	return "", nil
}

func notificationIDs(ctx context.Context, tx ds.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func confirmationUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil
}

func finishIgnoredEvent(ctx context.Context, tx ds.Tx, id int64, code string) error {
	_, err := tx.Exec(ctx, `UPDATE public.payment_events SET processing_state='ignored',processed_at=now(),processing_error=$2 WHERE id=$1`, id, code)
	return err
}

type confirmationExecer interface {
	Exec(context.Context, string, ...any) (ds.ExecResult, error)
}

func postponeEvent(ctx context.Context, tx confirmationExecer, id int64, code string) error {
	_, err := tx.Exec(ctx, `UPDATE public.payment_events SET next_matching_at=now()+interval '1 minute',processing_error=$2 WHERE id=$1 AND processing_state='pending'`, id, code)
	return err
}

// GetAdminPayment is for privileged operator commands, never a customer route.
func (r *PostgresRepository) GetAdminPayment(ctx context.Context, id string) (Attempt, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Attempt{}, err
	}
	defer release()
	return readAttempt(conn.QueryRow(ctx, `SELECT `+attemptColumns+` FROM public.payments p WHERE p.id=$1`, id))
}

// ConfirmDemo is an explicit operator/server action and cannot confirm a sandbox
// or production profile, regardless of the configured bank endpoint.
func (r *PostgresRepository) ConfirmDemo(ctx context.Context, id string) error {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	return ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		current, err := lockConfirmationRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		a := current.Attempt
		if methodFor(a) == "card" {
			return confirmMockCard(ctx, tx, current)
		}
		if a.Environment != "mock" || a.QRCID == "" || a.AmountKopecks <= 0 || a.Currency != "RUB" {
			return errors.New("demo confirmation requires a registered mock QR")
		}
		profile, err := frozenConfirmationProfile(a)
		if err != nil || profile.Environment != "mock" {
			return errors.New("invalid mock registration")
		}
		proof := BankObservation{QRCID: a.QRCID, Status: "Accepted", Paid: true, TransactionID: "mock-" + a.ID, RefTransactionID: "mock-" + a.ID, PaymentType: "sbpPayment"}
		if err := auditObservation(ctx, tx, a, proof); err != nil {
			return err
		}
		return settleConfirmedPayment(ctx, tx, current, proof)
	})
}

var _ ConfirmationRepository = (*PostgresRepository)(nil)
