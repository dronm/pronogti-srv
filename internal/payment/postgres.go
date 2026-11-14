package payment

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/webapp"
)

type PostgresRepository struct{ db ds.Provider }

func NewPostgresRepository(db ds.Provider) *PostgresRepository { return &PostgresRepository{db: db} }

func (r *PostgresRepository) GetOrder(ctx context.Context, id string) (checkout.Order, error) {
	return checkout.NewPostgresRepository(r.db).Get(ctx, id)
}

const attemptColumns = `p.id::text,p.order_id::text,p.company_id,p.payment_profile_id,
	p.environment,p.state,p.idempotency_key,p.request_hash,COALESCE(p.qrc_id,'') AS qrc_id,
	COALESCE(p.bank_payload,'') AS bank_payload,p.qr_image,COALESCE(p.qr_image_media_type,'') AS qr_image_media_type,
	p.profile_snapshot,p.amount_kopecks,p.currency,p.expires_at,p.created_at,p.updated_at,
	COALESCE(p.error_details->>'code','') AS error_code,p.method,COALESCE(p.acquiring_operation_id,'') AS acquiring_operation_id`

const lockOrderSQL = `SELECT company_id,COALESCE(kiosk_id,0),entry_mode,COALESCE(client_id,0),
	scope_hash,customer_session_hash,state,expires_at,payable_kopecks,currency
	FROM public.payment_orders WHERE id=$1 FOR UPDATE`

const profileForOrderSQL = `SELECT p.id,p.company_id,p.config_version,p.name,p.environment,p.api_base_url,
	COALESCE(p.jwt_env,'') AS jwt_env,p.jwt_ciphertext,COALESCE(p.client_id,'') AS client_id,COALESCE(p.customer_code,'') AS customer_code,
	COALESCE(p.sbp_merchant_id,'') AS sbp_merchant_id,COALESCE(p.account_id,'') AS account_id,p.qr_ttl_minutes,p.http_timeout_seconds,p.enabled,
	p.cards_enabled,COALESCE(p.acquiring_merchant_id,''),p.card_ttl_minutes,COALESCE(p.card_redirect_url,''),COALESCE(p.card_fail_redirect_url,'')
	FROM public.payment_profiles p WHERE p.company_id=$1 AND p.id=(
		SELECT COALESCE(k.payment_profile_id,s.default_payment_profile_id)
		FROM public.salons s LEFT JOIN public.kiosks k ON k.id=$2 AND k.company_id=s.company_id
		WHERE s.company_id=$1
	) FOR SHARE OF p`

const insertAttemptSQL = `INSERT INTO public.payments AS p (
	id,order_id,company_id,payment_profile_id,environment,amount_kopecks,currency,state,
	idempotency_key,request_hash,expires_at,profile_snapshot,created_at,updated_at,method
) VALUES ($1,$2,$3,$4,$5,$6,$7,'creating',$8,$9,$10,$11::jsonb,$12,$12,$13)
RETURNING ` + attemptColumns

// Reserve elects the sole sender while holding a short order-row lock. The bank
// request happens after commit. A second process returns the durable attempt,
// including an uncertain attempt, instead of making another state-changing call.
func (r *PostgresRepository) Reserve(ctx context.Context, owner checkout.Owner, orderID, key, requestHash string, now time.Time) (Reservation, error) {
	return r.reserve(ctx, owner, orderID, key, requestHash, now, "sbp")
}

func (r *PostgresRepository) ReserveCard(ctx context.Context, owner checkout.Owner, orderID, key, requestHash string, now time.Time) (Reservation, error) {
	return r.reserve(ctx, owner, orderID, key, requestHash, now, "card")
}

func (r *PostgresRepository) reserve(ctx context.Context, owner checkout.Owner, orderID, key, requestHash string, now time.Time, method string) (Reservation, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Reservation{}, err
	}
	defer release()
	var result Reservation
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		var actual checkout.Owner
		var kioskID int64
		var state, currency string
		var orderExpires time.Time
		var amount int64
		err := tx.QueryRow(ctx, lockOrderSQL, orderID).Scan(&actual.CompanyID, &kioskID, &actual.Mode, &actual.ClientID,
			&actual.ScopeHash, &actual.CustomerSessionHash, &state, &orderExpires, &amount, &currency)
		if errors.Is(err, ds.ErrNoRows) {
			return paymentNotFound()
		}
		if err != nil {
			return fmt.Errorf("lock payment checkout: %w", err)
		}
		if kioskID > 0 {
			actual.KioskID = &kioskID
		}
		if !samePaymentOwner(owner, actual) {
			return paymentNotFound()
		}

		previous, err := readAttempt(tx.QueryRow(ctx, `SELECT `+attemptColumns+` FROM public.payments p WHERE p.order_id=$1 AND p.idempotency_key=$2`, orderID, key))
		if err == nil {
			if methodFor(previous) != method {
				return paymentConflict("checkout already has another payment method", "payment_method_conflict")
			}
			if previous.RequestHash != requestHash {
				return paymentConflict("Idempotency-Key was already used for another QR request", "payment_idempotency_conflict")
			}
			result.Attempt = previous
			return nil
		}
		if !errors.Is(err, ds.ErrNoRows) {
			return fmt.Errorf("find QR idempotency record: %w", err)
		}

		// A new browser key cannot bypass a known or uncertain active attempt.
		previous, err = readAttempt(tx.QueryRow(ctx, `SELECT `+attemptColumns+` FROM public.payments p
			WHERE p.order_id=$1 AND p.state IN ('creating','pending','unknown','paid')
			ORDER BY CASE WHEN p.state='paid' THEN 0 ELSE 1 END,p.created_at DESC LIMIT 1`, orderID))
		if err == nil {
			if methodFor(previous) != method {
				return paymentConflict("checkout already has another payment method", "payment_method_conflict")
			}
			result.Attempt = previous
			return nil
		}
		if !errors.Is(err, ds.ErrNoRows) {
			return fmt.Errorf("find existing QR attempt: %w", err)
		}
		if state == "payment_pending" {
			// Preserve an expired attempt for reloads with a new browser key. The
			// order hold remains until bank reconciliation establishes its outcome.
			previous, err = readAttempt(tx.QueryRow(ctx, `SELECT `+attemptColumns+` FROM public.payments p
				WHERE p.order_id=$1 AND p.state='expired' ORDER BY p.created_at DESC LIMIT 1`, orderID))
			if err == nil {
				if methodFor(previous) != method {
					return paymentConflict("checkout already has another payment method", "payment_method_conflict")
				}
				result.Attempt = previous
				return nil
			}
			if !errors.Is(err, ds.ErrNoRows) {
				return fmt.Errorf("read expired QR attempt: %w", err)
			}
		}
		if state != "prepared" {
			return paymentConflict("checkout already has a payment attempt requiring reconciliation", "checkout_not_prepared")
		}
		minutesRemaining := int(orderExpires.Sub(now) / time.Minute)
		if minutesRemaining < 1 {
			return paymentConflict("checkout has expired or has insufficient QR lifetime; prepare it again", "checkout_expired")
		}

		var profile Profile
		err = tx.QueryRow(ctx, profileForOrderSQL, actual.CompanyID, actual.KioskID).Scan(&profile.ID, &profile.CompanyID, &profile.ConfigVersion,
			&profile.Name, &profile.Environment, &profile.APIBaseURL, &profile.JWTEnv, &profile.JWTCiphertext, &profile.ClientID,
			&profile.CustomerCode, &profile.SBPMerchantID, &profile.AccountID, &profile.QRTTLMinutes, &profile.HTTPTimeoutSeconds, &profile.Enabled,
			&profile.CardsEnabled, &profile.AcquiringMerchantID, &profile.CardTTLMinutes, &profile.CardRedirectURL, &profile.CardFailRedirectURL)
		if errors.Is(err, ds.ErrNoRows) || err == nil && !profile.Enabled {
			return paymentConflict("payment profile is not configured or enabled for this salon", "payment_profile_unavailable")
		}
		if err != nil {
			return fmt.Errorf("read QR payment profile: %w", err)
		}
		if method == "card" && (!profile.CardsEnabled || profile.AcquiringMerchantID == "" || profile.CardTTLMinutes < 1 || profile.CardTTLMinutes > 44640) {
			return paymentConflict("card payment profile is not configured", "payment_profile_unavailable")
		}
		if method == "sbp" && (profile.SBPMerchantID == "" || profile.AccountID == "") {
			return paymentConflict("SBP payment profile is not configured", "payment_profile_unavailable")
		}
		if profile.QRTTLMinutes < 1 || profile.HTTPTimeoutSeconds < 1 {
			return paymentConflict("payment profile contains invalid timing settings", "payment_profile_unavailable")
		}
		// A creating attempt carries the quote's outer deadline. The sender
		// computes the final integer TTL immediately before dispatch, so a
		// configured one-minute QR does not lose its lifetime to DB latency.
		expires := orderExpires
		snapshot, err := json.Marshal(profile)
		if err != nil {
			return fmt.Errorf("encode QR profile snapshot: %w", err)
		}
		id, err := newAttemptID()
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE public.payment_orders SET state='payment_pending' WHERE id=$1 AND state='prepared'`, orderID); err != nil {
			var pg interface{ SQLState() string }
			if errors.As(err, &pg) && pg.SQLState() == "23505" {
				return paymentConflict("another checkout for this visit already has a payment in progress", "visit_payment_in_progress")
			}
			return fmt.Errorf("reserve checkout for QR: %w", err)
		}
		attempt, err := readAttempt(tx.QueryRow(ctx, insertAttemptSQL, id, orderID, actual.CompanyID, profile.ID, profile.Environment,
			amount, currency, key, requestHash, expires, string(snapshot), now, method))
		if err != nil {
			return fmt.Errorf("create QR payment attempt: %w", err)
		}
		result = Reservation{Attempt: attempt, Profile: profile, Created: true}
		return nil
	})
	return result, err
}

func (r *PostgresRepository) Get(ctx context.Context, owner checkout.Owner, id string) (Attempt, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Attempt{}, err
	}
	defer release()
	item, err := readAttempt(conn.QueryRow(ctx, `SELECT `+attemptColumns+` FROM public.payments p
		JOIN public.payment_orders o ON o.id=p.order_id
		WHERE p.id=$1 AND o.company_id=$2 AND COALESCE(o.kiosk_id,0)=$3 AND o.entry_mode=$4
		AND COALESCE(o.client_id,0)=$5 AND o.scope_hash=$6 AND o.customer_session_hash=$7`,
		id, owner.CompanyID, paymentKioskID(owner.KioskID), owner.Mode, owner.ClientID, owner.ScopeHash, owner.CustomerSessionHash))
	if errors.Is(err, ds.ErrNoRows) {
		return Attempt{}, paymentNotFound()
	}
	if err != nil {
		return Attempt{}, fmt.Errorf("read QR payment attempt: %w", err)
	}
	return item, nil
}

const completeAttemptSQL = `UPDATE public.payments SET state=CASE WHEN state='paid' THEN 'paid' ELSE 'pending' END,qrc_id=$2,bank_payload=$3,
	qr_image=$4,qr_image_media_type=$5,expires_at=LEAST(expires_at,$6),error_details=NULL
	WHERE id=$1 AND method='sbp' AND ((state IN ('creating','unknown') AND qrc_id IS NULL) OR (state='paid' AND qrc_id=$2))`

func (r *PostgresRepository) Complete(ctx context.Context, id string, result QRResult) error {
	if result.QRCID == "" || result.Payload == "" || len(result.Image) == 0 || result.ImageMediaType != "image/png" || result.ExpiresAt.IsZero() {
		return errors.New("bank returned an incomplete QR result")
	}
	return r.execOne(ctx, completeAttemptSQL, id, result.QRCID, result.Payload, result.Image, result.ImageMediaType, result.ExpiresAt)
}

const failAttemptSQL = `UPDATE public.payments SET state=$2,error_details=jsonb_build_object('code',$3::text)
	WHERE id=$1 AND state IN ('creating','unknown') AND qrc_id IS NULL AND acquiring_operation_id IS NULL`

func (r *PostgresRepository) Fail(ctx context.Context, id string, unknown bool, code string) error {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	return ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		// Match Reserve and bank confirmation's order-before-payment lock order.
		// A signed callback can establish payment before QR creation returns.
		var existing string
		if err := tx.QueryRow(ctx, `SELECT p.state FROM public.payment_orders o
			JOIN public.payments p ON p.order_id=o.id WHERE p.id=$1 FOR UPDATE OF o`, id).Scan(&existing); err != nil {
			return err
		}
		if existing == "paid" {
			return nil
		}
		state := "failed"
		if unknown {
			state = "unknown"
		}
		result, err := tx.Exec(ctx, failAttemptSQL, id, state, code)
		if err != nil {
			return fmt.Errorf("persist QR failure: %w", err)
		}
		if result.RowsAffected() != 1 {
			return errors.New("QR attempt is no longer awaiting creation")
		}
		if !unknown {
			// Definite rejection before a usable QR exists permits a fresh key on
			// this order. Unknown and locally expired outcomes never release it.
			_, err = tx.Exec(ctx, `UPDATE public.payment_orders o SET state=CASE WHEN o.expires_at>now() THEN 'prepared' ELSE 'expired' END
				FROM public.payments p WHERE p.id=$1 AND o.id=p.order_id AND o.state='payment_pending'
				AND NOT EXISTS (SELECT 1 FROM public.payments active WHERE active.order_id=o.id AND active.state IN ('creating','pending','unknown','paid'))`, id)
			if err != nil {
				return fmt.Errorf("release rejected QR checkout: %w", err)
			}
		}
		return nil
	})
}

const expireAttemptSQL = `UPDATE public.payments SET state='expired'
	WHERE id=$1 AND method IN ('sbp','card') AND state='pending' AND (qrc_id IS NOT NULL OR acquiring_operation_id IS NOT NULL) AND expires_at<=$2`

func (r *PostgresRepository) UpdateExpired(ctx context.Context, id string, now time.Time) (Attempt, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Attempt{}, err
	}
	defer release()
	if _, err = conn.Exec(ctx, expireAttemptSQL, id, now); err != nil {
		return Attempt{}, fmt.Errorf("expire QR display: %w", err)
	}
	// The order remains payment_pending. A local timer cannot prove that a bank
	// QR can no longer accept funds and therefore cannot authorize another charge.
	return readAttempt(conn.QueryRow(ctx, `SELECT `+attemptColumns+` FROM public.payments p WHERE p.id=$1`, id))
}

const abandonAttemptSQL = `UPDATE public.payments SET state='unknown',error_details='{"code":"qr_creation_interrupted"}'::jsonb
	WHERE id=$1 AND method IN ('sbp','card') AND state='creating'
	AND created_at <= $2::timestamptz - make_interval(secs => COALESCE((profile_snapshot->>'http_timeout_seconds')::integer,300)+30)`

func (r *PostgresRepository) AbandonStale(ctx context.Context, id string, now time.Time) (Attempt, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Attempt{}, err
	}
	defer release()
	if _, err = conn.Exec(ctx, abandonAttemptSQL, id, now); err != nil {
		return Attempt{}, fmt.Errorf("mark interrupted QR creation: %w", err)
	}
	return readAttempt(conn.QueryRow(ctx, `SELECT `+attemptColumns+` FROM public.payments p WHERE p.id=$1`, id))
}

func readAttempt(row ds.Row) (Attempt, error) {
	var item Attempt
	err := row.Scan(&item.ID, &item.OrderID, &item.CompanyID, &item.PaymentProfileID, &item.Environment, &item.State,
		&item.IdempotencyKey, &item.RequestHash, &item.QRCID, &item.Payload, &item.Image, &item.ImageMediaType, &item.ProfileSnapshot,
		&item.AmountKopecks, &item.Currency, &item.ExpiresAt, &item.CreatedAt, &item.UpdatedAt, &item.ErrorCode, &item.Method, &item.OperationID)
	return item, err
}

func (r *PostgresRepository) execOne(ctx context.Context, sql string, args ...any) error {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	result, err := conn.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("persist QR result: %w", err)
	}
	if result.RowsAffected() != 1 {
		return errors.New("QR attempt is no longer awaiting creation")
	}
	return nil
}

func (r *PostgresRepository) connection(ctx context.Context) (ds.Conn, func(), error) {
	if r == nil || r.db == nil {
		return nil, nil, errors.New("payment database is not configured")
	}
	lease, server, err := r.db.GetPrimary(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire payment database: %w", err)
	}
	return lease.Conn(), func() { r.db.Release(lease, server) }, nil
}

func newAttemptID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate payment identity: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[:4], raw[4:6], raw[6:8], raw[8:10], raw[10:]), nil
}

func samePaymentOwner(a, b checkout.Owner) bool {
	return a.CompanyID == b.CompanyID && paymentKioskID(a.KioskID) == paymentKioskID(b.KioskID) && a.Mode == b.Mode && a.ClientID == b.ClientID && a.ScopeHash == b.ScopeHash && a.CustomerSessionHash == b.CustomerSessionHash
}
func paymentKioskID(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}
func paymentNotFound() error { return webapp.NotFound("payment checkout or attempt not found", nil) }
func paymentConflict(message, code string) error {
	return webapp.Conflict(message, map[string]any{"code": code})
}
