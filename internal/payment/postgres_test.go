package payment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/webapp"
)

type qrPGRow struct {
	values []any
	err    error
}

func (r qrPGRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("unexpected payment projection")
	}
	for i, value := range r.values {
		target := reflect.ValueOf(dest[i]).Elem()
		if value == nil {
			target.SetZero()
		} else {
			target.Set(reflect.ValueOf(value))
		}
	}
	return nil
}

type qrPGCall struct {
	sql  string
	args []any
}
type qrPGResult struct{}

func (qrPGResult) RowsAffected() int64 { return 1 }

type qrPGTx struct {
	ds.Tx
	rows               []qrPGRow
	queries, execs     []qrPGCall
	commits, rollbacks int
}

func (t *qrPGTx) QueryRow(_ context.Context, sql string, args ...any) ds.Row {
	t.queries = append(t.queries, qrPGCall{sql, args})
	if len(t.rows) == 0 {
		return qrPGRow{err: errors.New("unexpected database read")}
	}
	row := t.rows[0]
	t.rows = t.rows[1:]
	return row
}
func (t *qrPGTx) Exec(_ context.Context, sql string, args ...any) (ds.ExecResult, error) {
	t.execs = append(t.execs, qrPGCall{sql, args})
	return qrPGResult{}, nil
}
func (t *qrPGTx) Commit(context.Context) error   { t.commits++; return nil }
func (t *qrPGTx) Rollback(context.Context) error { t.rollbacks++; return nil }

type qrPGConn struct {
	ds.Conn
	tx *qrPGTx
}

func (c qrPGConn) Begin(context.Context) (ds.Tx, error) { return c.tx, nil }

type qrPGLease struct{ conn ds.Conn }

func (l qrPGLease) Conn() ds.Conn { return l.conn }
func (l qrPGLease) Release()      {}

type qrPGProvider struct {
	ds.Provider
	tx       *qrPGTx
	releases int
}

func (p *qrPGProvider) GetPrimary(context.Context) (ds.PoolConn, ds.ServerID, error) {
	return qrPGLease{qrPGConn{tx: p.tx}}, "primary", nil
}
func (p *qrPGProvider) Release(ds.PoolConn, ds.ServerID) { p.releases++ }

func qrPGFixture() (checkout.Owner, time.Time, qrPGRow) {
	owner := checkout.Owner{CompanyID: 781898, Mode: "public", ClientID: 71, ScopeHash: strings.Repeat("a", 64), CustomerSessionHash: strings.Repeat("b", 64)}
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	row := qrPGRow{values: []any{owner.CompanyID, int64(0), owner.Mode, owner.ClientID, owner.ScopeHash, owner.CustomerSessionHash, "prepared", now.Add(14*time.Minute + 50*time.Second), int64(12345), "RUB"}}
	return owner, now, row
}
func qrPGAttemptRow(now time.Time, state, hash string) qrPGRow {
	exp := now.Add(14 * time.Minute)
	return qrPGRow{values: []any{"10000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000001", int64(781898), int64(7), "mock", state, "old-key", hash, "qrc-id", "https://example.invalid/qr", []byte{1}, "image/png", json.RawMessage(`{"id":7}`), int64(12345), "RUB", &exp, now, now, "", "sbp", ""}}
}
func qrPGRequireStatus(t *testing.T, err error, status int) {
	t.Helper()
	if err == nil {
		t.Fatal("expected service error")
	}
	w := httptest.NewRecorder()
	webapp.WriteServiceError(w, err)
	if w.Code != status {
		t.Fatalf("status=%d error=%v", w.Code, err)
	}
}

func TestPostgresReserveFreezesProfileAndElectsSender(t *testing.T) {
	owner, now, order := qrPGFixture()
	profile := qrPGRow{values: []any{int64(7), int64(781898), int64(3), "Mock", "mock", "http://127.0.0.1:8099", "TOKEN_ENV", []byte("ciphertext"), "client", "customer", "merchant", "account", 15, 20, true, false, "", 15, "", ""}}
	tx := &qrPGTx{rows: []qrPGRow{order, {err: ds.ErrNoRows}, {err: ds.ErrNoRows}, profile, qrPGAttemptRow(now, "creating", "request-hash")}}
	p := &qrPGProvider{tx: tx}
	result, err := NewPostgresRepository(p).Reserve(context.Background(), owner, "00000000-0000-4000-8000-000000000001", "new-key", "request-hash", now)
	if err != nil || !result.Created || tx.commits != 1 || tx.rollbacks != 0 || p.releases != 1 {
		t.Fatalf("created=%v commit=%d rollback=%d err=%v", result.Created, tx.commits, tx.rollbacks, err)
	}
	if result.Profile.JWTEnv != "TOKEN_ENV" || string(result.Profile.JWTCiphertext) != "ciphertext" {
		t.Fatal("sender did not receive the resolved profile credentials")
	}
	insert := tx.queries[len(tx.queries)-1]
	if insert.args[5] != int64(12345) || insert.args[6] != "RUB" || insert.args[7] != "new-key" {
		t.Fatalf("browser values replaced checkout amount: %+v", insert.args)
	}
	if !insert.args[9].(time.Time).Equal(now.Add(14*time.Minute + 50*time.Second)) {
		t.Fatal("creating QR attempt did not preserve the quote's outer deadline")
	}
	var snapshot map[string]any
	if err = json.Unmarshal([]byte(insert.args[10].(string)), &snapshot); err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot["jwt_env"]; ok {
		t.Fatal("snapshot contains a credential reference")
	}
	if _, ok := snapshot["jwt_ciphertext"]; ok {
		t.Fatal("snapshot contains credential bytes")
	}
	if snapshot["name"] != "Mock" || snapshot["config_version"] != float64(3) {
		t.Fatal("bank configuration was not frozen")
	}
	if len(tx.execs) != 1 || !strings.Contains(tx.execs[0].sql, "payment_pending") {
		t.Fatal("checkout was not reserved before bank dispatch")
	}
}

func TestPostgresReserveSameKeyAndOtherKeyReuseExistingAttempt(t *testing.T) {
	for _, keyMatch := range []bool{true, false} {
		t.Run(map[bool]string{true: "same key", false: "new browser key"}[keyMatch], func(t *testing.T) {
			owner, now, order := qrPGFixture()
			order.values[6] = "payment_pending"
			rows := []qrPGRow{order}
			if !keyMatch {
				rows = append(rows, qrPGRow{err: ds.ErrNoRows})
			}
			rows = append(rows, qrPGAttemptRow(now, "unknown", "request-hash"))
			tx := &qrPGTx{rows: rows}
			p := &qrPGProvider{tx: tx}
			res, err := NewPostgresRepository(p).Reserve(context.Background(), owner, "order-id", "key", "request-hash", now)
			if err != nil || res.Created || res.Attempt.State != "unknown" || len(tx.execs) != 0 || tx.commits != 1 {
				t.Fatalf("reservation=%+v err=%v", res, err)
			}
		})
	}
}

func TestPostgresReserveChecksOwnerAndIdempotency(t *testing.T) {
	owner, now, order := qrPGFixture()
	tx := &qrPGTx{rows: []qrPGRow{order}}
	foreign := owner
	foreign.CustomerSessionHash = strings.Repeat("c", 64)
	_, err := NewPostgresRepository(&qrPGProvider{tx: tx}).Reserve(context.Background(), foreign, "order", "key", "hash", now)
	qrPGRequireStatus(t, err, 404)
	if tx.rollbacks != 1 || len(tx.queries) != 1 || len(tx.execs) != 0 {
		t.Fatal("foreign actor advanced payment reservation")
	}
	tx = &qrPGTx{rows: []qrPGRow{order, qrPGAttemptRow(now, "creating", "different-hash")}}
	_, err = NewPostgresRepository(&qrPGProvider{tx: tx}).Reserve(context.Background(), owner, "order", "key", "hash", now)
	qrPGRequireStatus(t, err, 409)
	if len(tx.execs) != 0 || tx.rollbacks != 1 {
		t.Fatal("idempotency conflict created another payment")
	}
}

func TestPostgresReserveDoesNotReleaseExpiredOrUncertainCheckout(t *testing.T) {
	owner, now, order := qrPGFixture()
	order.values[6] = "payment_pending"
	tx := &qrPGTx{rows: []qrPGRow{order, {err: ds.ErrNoRows}, {err: ds.ErrNoRows}, {err: ds.ErrNoRows}}}
	_, err := NewPostgresRepository(&qrPGProvider{tx: tx}).Reserve(context.Background(), owner, "order", "new-key", "hash", now)
	qrPGRequireStatus(t, err, 409)
	if len(tx.execs) != 0 {
		t.Fatal("locally expired checkout was released for another bank call")
	}
}

func TestPostgresReserveReloadReusesExpiredAttemptWithoutNewCharge(t *testing.T) {
	owner, now, order := qrPGFixture()
	order.values[6] = "payment_pending"
	tx := &qrPGTx{rows: []qrPGRow{order, {err: ds.ErrNoRows}, {err: ds.ErrNoRows}, qrPGAttemptRow(now, "expired", "hash")}}
	res, err := NewPostgresRepository(&qrPGProvider{tx: tx}).Reserve(context.Background(), owner, "order", "new-browser-key", "hash", now.Add(time.Hour))
	if err != nil || res.Created || res.Attempt.State != "expired" || len(tx.execs) != 0 {
		t.Fatalf("expired replay: %+v error=%v", res, err)
	}
}

func TestPostgresReserveRequiresUsableQuoteAndProfile(t *testing.T) {
	for _, which := range []string{"expired", "disabled profile"} {
		t.Run(which, func(t *testing.T) {
			owner, now, order := qrPGFixture()
			rows := []qrPGRow{order, {err: ds.ErrNoRows}, {err: ds.ErrNoRows}}
			if which == "expired" {
				order.values[7] = now.Add(59 * time.Second)
			} else {
				rows = append(rows, qrPGRow{err: ds.ErrNoRows})
			}
			tx := &qrPGTx{rows: rows}
			_, err := NewPostgresRepository(&qrPGProvider{tx: tx}).Reserve(context.Background(), owner, "order", "key", "hash", now)
			qrPGRequireStatus(t, err, 409)
			if len(tx.execs) != 0 {
				t.Fatal("unusable checkout reached bank reservation")
			}
		})
	}
}

func TestPostgresFailureOnlyReleasesDefiniteRejection(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "unknown"}[unknown], func(t *testing.T) {
			tx := &qrPGTx{rows: []qrPGRow{{values: []any{"creating"}}}}
			err := NewPostgresRepository(&qrPGProvider{tx: tx}).Fail(context.Background(), "attempt", unknown, "bank_error")
			if err != nil {
				t.Fatal(err)
			}
			expected := 2
			state := "failed"
			if unknown {
				expected = 1
				state = "unknown"
			}
			if len(tx.execs) != expected || tx.execs[0].args[1] != state {
				t.Fatalf("failure outcome=%+v", tx.execs)
			}
		})
	}
}

func TestPostgresCompleteRejectsIncompleteBankResponseBeforeWrite(t *testing.T) {
	err := NewPostgresRepository(nil).Complete(context.Background(), "id", QRResult{QRCID: "qrc"})
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("response validation=%v", err)
	}
}
