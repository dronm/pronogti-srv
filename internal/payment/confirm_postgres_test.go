package payment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dronm/ds/v4"
)

type confirmationPGRows struct {
	ds.Rows
	rows   []qrPGRow
	index  int
	closed bool
}

func (r *confirmationPGRows) Next() bool { return r.index < len(r.rows) }
func (r *confirmationPGRows) Scan(dest ...any) error {
	row := r.rows[r.index]
	r.index++
	return row.Scan(dest...)
}
func (r *confirmationPGRows) Err() error   { return nil }
func (r *confirmationPGRows) Close() error { r.closed = true; return nil }

type confirmationPGDB struct {
	ds.Provider
	rows                                  []qrPGRow
	lists                                 [][]qrPGRow
	queries, execs                        []qrPGCall
	commits, rollbacks, leases, maxLeases int
	failExec                              int
}
type confirmationPGConn struct {
	ds.Conn
	db *confirmationPGDB
}
type confirmationPGTx struct {
	ds.Tx
	db *confirmationPGDB
}

func (d *confirmationPGDB) GetPrimary(context.Context) (ds.PoolConn, ds.ServerID, error) {
	d.leases++
	if d.leases > d.maxLeases {
		d.maxLeases = d.leases
	}
	return qrPGLease{confirmationPGConn{db: d}}, "primary", nil
}
func (d *confirmationPGDB) Release(ds.PoolConn, ds.ServerID) { d.leases-- }
func (c confirmationPGConn) Begin(context.Context) (ds.Tx, error) {
	return confirmationPGTx{db: c.db}, nil
}
func (c confirmationPGConn) QueryRow(_ context.Context, sql string, args ...any) ds.Row {
	return c.db.row(sql, args)
}
func (t confirmationPGTx) QueryRow(_ context.Context, sql string, args ...any) ds.Row {
	return t.db.row(sql, args)
}
func (c confirmationPGConn) Query(_ context.Context, sql string, args ...any) (ds.Rows, error) {
	return c.db.list(sql, args)
}
func (t confirmationPGTx) Query(_ context.Context, sql string, args ...any) (ds.Rows, error) {
	return t.db.list(sql, args)
}
func (c confirmationPGConn) Exec(_ context.Context, sql string, args ...any) (ds.ExecResult, error) {
	return c.db.exec(sql, args)
}
func (t confirmationPGTx) Exec(_ context.Context, sql string, args ...any) (ds.ExecResult, error) {
	return t.db.exec(sql, args)
}
func (t confirmationPGTx) Commit(context.Context) error   { t.db.commits++; return nil }
func (t confirmationPGTx) Rollback(context.Context) error { t.db.rollbacks++; return nil }
func (d *confirmationPGDB) row(sql string, args []any) ds.Row {
	d.queries = append(d.queries, qrPGCall{sql, args})
	if len(d.rows) == 0 {
		return qrPGRow{err: errors.New("unexpected confirmation read")}
	}
	row := d.rows[0]
	d.rows = d.rows[1:]
	return row
}
func (d *confirmationPGDB) list(sql string, args []any) (ds.Rows, error) {
	d.queries = append(d.queries, qrPGCall{sql, args})
	if len(d.lists) == 0 {
		return nil, errors.New("unexpected confirmation list")
	}
	rows := d.lists[0]
	d.lists = d.lists[1:]
	return &confirmationPGRows{rows: rows}, nil
}
func (d *confirmationPGDB) exec(sql string, args []any) (ds.ExecResult, error) {
	d.execs = append(d.execs, qrPGCall{sql, args})
	if d.failExec == len(d.execs) {
		return nil, errors.New("unique bank reference violation")
	}
	return qrPGResult{}, nil
}

func confirmationPGCandidate() ConfirmationCandidate {
	c := confirmationCandidate()
	c.Profile.APIBaseURL = "https://enter.tochka.com/sandbox/v2"
	c.Attempt.ProfileSnapshot, _ = json.Marshal(c.Profile)
	return c
}
func confirmationPGRecord(c ConfirmationCandidate, state string, claimed *time.Time, ref string) qrPGRow {
	a := c.Attempt
	return qrPGRow{values: []any{a.ID, a.OrderID, a.CompanyID, a.PaymentProfileID, a.Environment, state, a.IdempotencyKey, a.RequestHash,
		a.QRCID, a.Payload, a.Image, a.ImageMediaType, a.ProfileSnapshot, a.AmountKopecks, a.Currency, a.ExpiresAt, a.CreatedAt, a.UpdatedAt, a.ErrorCode, "sbp", "",
		claimed, "", ref}}
}
func confirmationPGSuccess(c ConfirmationCandidate) BankObservation {
	return BankObservation{QRCID: c.Attempt.QRCID, Status: "Accepted", Paid: true, RefTransactionID: "bank-reference", PaymentType: "sbpPayment"}
}

func TestPostgresConfirmationAtomicPaidQueuesAndUnsignedPollAudit(t *testing.T) {
	c := confirmationPGCandidate()
	db := &confirmationPGDB{rows: []qrPGRow{{values: []any{c.Attempt.OrderID}}, confirmationPGRecord(c, "expired", &c.ClaimedAt, ""), {err: ds.ErrNoRows}}}
	err := NewPostgresRepository(db).Observe(context.Background(), c, confirmationPGSuccess(c))
	if err != nil || db.commits != 1 || db.rollbacks != 0 || db.leases != 0 {
		t.Fatalf("commit=%d rollback=%d leases=%d err=%v", db.commits, db.rollbacks, db.leases, err)
	}
	if len(db.execs) != 3 || !strings.Contains(db.execs[0].sql, "'reconciliation'") || !strings.Contains(db.execs[0].sql, "false") {
		t.Fatal("bank poll was not independently audited without claiming a webhook signature")
	}
	if db.execs[1].sql != settleConfirmedOrderSQL || db.execs[2].sql != settleConfirmedPaymentSQL {
		t.Fatal("order and payment were not transitioned in one ordered transaction")
	}
	if db.execs[2].args[3] != "bank-reference" {
		t.Fatal("provider reference missing")
	}
	if !strings.Contains(db.queries[0].sql, "FOR UPDATE OF o") || !strings.Contains(db.queries[1].sql, "FOR UPDATE OF p") {
		t.Fatal("confirmation inverted order and payment lock order")
	}
}

func TestPostgresConfirmationUniqueBankReferenceRollsBackWholeSettlement(t *testing.T) {
	c := confirmationPGCandidate()
	db := &confirmationPGDB{rows: []qrPGRow{{values: []any{c.Attempt.OrderID}}, confirmationPGRecord(c, "pending", &c.ClaimedAt, ""), {err: ds.ErrNoRows}}, failExec: 3}
	err := NewPostgresRepository(db).Observe(context.Background(), c, confirmationPGSuccess(c))
	if err == nil || db.commits != 0 || db.rollbacks != 1 {
		t.Fatalf("conflicting bank reference committed: %v", err)
	}
}

func TestPostgresConfirmationRejectsStaleClaimsAndChangedRegistration(t *testing.T) {
	for _, which := range []string{"stale claim", "different amount", "different frozen merchant", "different QR"} {
		t.Run(which, func(t *testing.T) {
			candidate := confirmationPGCandidate()
			current := candidate
			claim := candidate.ClaimedAt
			switch which {
			case "stale claim":
				claim = claim.Add(time.Second)
			case "different amount":
				current.Attempt.AmountKopecks++
			case "different frozen merchant":
				current.Profile.SBPMerchantID = "other"
				current.Attempt.ProfileSnapshot, _ = json.Marshal(current.Profile)
			case "different QR":
				current.Attempt.QRCID = "foreign"
			}
			db := &confirmationPGDB{rows: []qrPGRow{{values: []any{current.Attempt.OrderID}}, confirmationPGRecord(current, "pending", &claim, "")}}
			err := NewPostgresRepository(db).Observe(context.Background(), candidate, confirmationPGSuccess(candidate))
			if err == nil || len(db.execs) != 0 || db.commits != 0 || db.rollbacks != 1 {
				t.Fatalf("mismatched confirmation advanced: %v", err)
			}
		})
	}
}

func TestPostgresConfirmationNeverDowngradesPaidOnOutOfOrderPoll(t *testing.T) {
	c := confirmationPGCandidate()
	for _, proof := range []BankObservation{{QRCID: c.Attempt.QRCID, Status: "NotFound"}, confirmationPGSuccess(c)} {
		db := &confirmationPGDB{rows: []qrPGRow{{values: []any{c.Attempt.OrderID}}, confirmationPGRecord(c, "paid", nil, "bank-reference")}}
		if err := NewPostgresRepository(db).Observe(context.Background(), c, proof); err != nil || len(db.execs) != 0 || db.commits != 1 {
			t.Fatalf("paid attempt changed on late observation: %v", err)
		}
	}
}

func TestPostgresConfirmationPendingReadRetainsOrderHold(t *testing.T) {
	c := confirmationPGCandidate()
	db := &confirmationPGDB{rows: []qrPGRow{{values: []any{c.Attempt.OrderID}}, confirmationPGRecord(c, "expired", &c.ClaimedAt, "")}}
	if err := NewPostgresRepository(db).Observe(context.Background(), c, BankObservation{QRCID: c.Attempt.QRCID, Status: "Rejected"}); err != nil {
		t.Fatal(err)
	}
	if len(db.execs) != 2 || strings.Contains(db.execs[1].sql, "state=") || strings.Contains(db.execs[1].sql, "payment_orders") {
		t.Fatal("individual rejected transfer released the QR/order")
	}
}

func TestPostgresDemoConfirmationIsRestrictedToMockRegisteredQR(t *testing.T) {
	for _, env := range []string{"sandbox", "production", "mock"} {
		c := confirmationPGCandidate()
		c.Profile.Environment = env
		c.Attempt.Environment = env
		c.Attempt.ProfileSnapshot, _ = json.Marshal(c.Profile)
		db := &confirmationPGDB{rows: []qrPGRow{{values: []any{c.Attempt.OrderID}}, confirmationPGRecord(c, "pending", nil, ""), {err: ds.ErrNoRows}}}
		err := NewPostgresRepository(db).ConfirmDemo(context.Background(), c.Attempt.ID)
		if env != "mock" {
			if err == nil || len(db.execs) != 0 {
				t.Fatal("demo command confirmed a real bank profile")
			}
		} else if err != nil || db.execs[2].args[3] != "mock-"+c.Attempt.ID {
			t.Fatalf("mock proof not deterministic: %v", err)
		}
	}
}

func TestPostgresClaimUsesFrozenIdentityAndRotatedCredentials(t *testing.T) {
	c := confirmationPGCandidate()
	row := confirmationPGRecord(c, "pending", nil, "")
	row.values = append(row.values[:21], c.ClaimedAt, "ROTATED_JWT", []byte("new-ciphertext"))
	db := &confirmationPGDB{lists: [][]qrPGRow{{}, {}}, rows: []qrPGRow{row}}
	claimed, err := NewPostgresRepository(db).ClaimConfirmation(context.Background())
	if err != nil || claimed.Profile.JWTEnv != "ROTATED_JWT" || claimed.Profile.SBPMerchantID != c.Profile.SBPMerchantID || db.leases != 0 || db.maxLeases != 1 {
		t.Fatalf("claim=%+v max leases=%d error=%v", claimed, db.maxLeases, err)
	}
	if strings.Contains(claimConfirmationSQL, "profile.enabled") {
		t.Fatal("disabled profiles cannot drop late bank confirmation")
	}
	if !strings.Contains(claimConfirmationSQL, "SKIP LOCKED") || !strings.Contains(claimConfirmationSQL, "qrc_id IS NOT NULL") {
		t.Fatal("claim can duplicate workers or retry unknown registration")
	}
}

func TestPostgresVerifiedWebhookProfileMismatchJournalsBeforeIgnoring(t *testing.T) {
	message := VerifiedSBPNotification{EventKey: "event", CustomerCode: "other-customer", MerchantID: "merchant-a", QRCID: "qrc", AmountKopecks: 100, Payload: json.RawMessage(`{"webhookType":"incomingSbpPayment"}`)}
	snapshot, _ := json.Marshal(message)
	db := &confirmationPGDB{rows: []qrPGRow{{values: []any{"production"}}, {values: []any{int64(11)}},
		{values: []any{int64(4), "production", "pending", json.RawMessage(snapshot)}}, {values: []any{int64(781898), "production", "customer-a", "merchant-a"}}}}
	if err := NewPostgresRepository(db).Receive(context.Background(), 4, message); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(db.queries[1].sql, "INSERT INTO public.payment_events") || len(db.execs) != 1 || db.execs[0].args[1] != "callback_profile_identity_mismatch" || db.maxLeases != 1 {
		t.Fatal("unmatched signed callback was not independently journaled and safely ignored")
	}
}

func TestPostgresVerifiedWebhookUsesExactMoneyAndOperationOnlyProof(t *testing.T) {
	for _, wrongAmount := range []bool{false, true} {
		c := confirmationPGCandidate()
		message := VerifiedSBPNotification{EventKey: "event", CustomerCode: c.Profile.CustomerCode, MerchantID: c.Profile.SBPMerchantID, QRCID: c.Attempt.QRCID, AmountKopecks: c.Attempt.AmountKopecks, TransactionID: "dr-operation", PaymentType: "drPayment"}
		if wrongAmount {
			message.AmountKopecks++
		}
		snapshot, _ := json.Marshal(message)
		db := &confirmationPGDB{rows: []qrPGRow{{values: []any{int64(4), "sandbox", "pending", json.RawMessage(snapshot)}},
			{values: []any{c.Attempt.CompanyID, "sandbox", c.Profile.CustomerCode, c.Profile.SBPMerchantID}},
			{values: []any{c.Attempt.OrderID}}, confirmationPGRecord(c, "pending", nil, ""), {err: ds.ErrNoRows}}, lists: [][]qrPGRow{{{values: []any{c.Attempt.ID}}}}}
		if err := NewPostgresRepository(db).applyVerifiedEvent(context.Background(), 11); err != nil {
			t.Fatal(err)
		}
		if wrongAmount {
			if len(db.execs) != 1 || db.execs[0].args[1] != "payment_notification_mismatch" {
				t.Fatal("signed wrong amount was applied")
			}
		} else if len(db.execs) != 3 || db.execs[1].args[2] != "dr-operation" || db.execs[1].args[3] != "" {
			t.Fatal("digital-ruble operation proof invented a refund reference or failed to settle")
		}
	}
}

func TestPostgresGenericWebhookUnmatchedTestsRemainDurableAndFair(t *testing.T) {
	message := VerifiedSBPNotification{EventKey: "event", CustomerCode: "test", MerchantID: "test", QRCID: "bank-registration-test", AmountKopecks: 100, Payload: json.RawMessage(`{"webhookType":"incomingSbpPayment"}`)}
	snapshot, _ := json.Marshal(message)
	db := &confirmationPGDB{rows: []qrPGRow{{values: []any{json.RawMessage(snapshot)}}}, lists: [][]qrPGRow{{}}}
	if err := NewPostgresRepository(db).ReceiveAny(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if len(db.execs) != 1 || !strings.Contains(db.execs[0].sql, "bank_webhook_inbox") || db.maxLeases != 1 || db.leases != 0 {
		t.Fatal("generic unmatched callback was lost or nested database leases were held")
	}
	if !strings.Contains(db.queries[0].sql, "30 seconds") || !strings.Contains(genericNotificationMatchesSQL, "profile_snapshot") {
		t.Fatal("generic callback guessed environment or starves replay")
	}
}

func TestPostgresFailAfterVerifiedPaymentDoesNotDowngrade(t *testing.T) {
	tx := &qrPGTx{rows: []qrPGRow{{values: []any{"paid"}}}}
	if err := NewPostgresRepository(&qrPGProvider{tx: tx}).Fail(context.Background(), "attempt", true, "network_timeout"); err != nil || len(tx.execs) != 0 || tx.commits != 1 {
		t.Fatalf("late creation failure replaced payment: %v", err)
	}
}
