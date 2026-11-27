package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dronm/ds/v4"
)

type repositoryTestProvider struct {
	ds.Provider
	conn     *repositoryTestConn
	err      error
	releases int
}

func (p *repositoryTestProvider) GetPrimary(context.Context) (ds.PoolConn, ds.ServerID, error) {
	if p.err != nil {
		return nil, "", p.err
	}
	return repositoryTestLease{p.conn}, "primary", nil
}
func (p *repositoryTestProvider) Release(ds.PoolConn, ds.ServerID) { p.releases++ }

type repositoryTestLease struct{ conn ds.Conn }

func (l repositoryTestLease) Conn() ds.Conn { return l.conn }
func (l repositoryTestLease) Release()      {}

type repositoryTestConn struct {
	ds.Conn
	args  []any
	query string
	row   repositoryTestRow
}

func (c *repositoryTestConn) QueryRow(_ context.Context, query string, args ...any) ds.Row {
	c.query, c.args = query, args
	return c.row
}

type repositoryTestRow struct {
	order    Order
	snapshot []byte
	err      error
}

func (r repositoryTestRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	var kioskID int64
	if r.order.Owner.KioskID != nil {
		kioskID = *r.order.Owner.KioskID
	}
	snapshot := r.snapshot
	if snapshot == nil {
		var err error
		snapshot, err = json.Marshal(r.order.Items)
		if err != nil {
			return err
		}
	}
	values := []any{
		r.order.ID, r.order.Owner.CompanyID, kioskID, r.order.Owner.Mode,
		r.order.Owner.ClientID, r.order.Owner.ScopeHash, r.order.Owner.CustomerSessionHash,
		r.order.VisitID, r.order.RecordID, snapshot, r.order.SubtotalKopecks,
		r.order.BonusKopecks, r.order.PayableKopecks, r.order.Currency, r.order.State,
		r.order.IdempotencyKey, r.order.RequestHash, r.order.ExpiresAt, r.order.CreatedAt,
		r.order.UpdatedAt, r.order.YClientsSyncStatus, r.order.CustomerContact, r.order.CustomerName,
	}
	if len(values) != len(dest) {
		return errors.New("unexpected checkout projection")
	}
	for i, value := range values {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(value))
	}
	return nil
}

func postgresOrderFixture() Order {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	return Order{
		ID:              "ea9e4a39-7b73-4c5f-a1fd-5752c54135d6",
		Owner:           Owner{CompanyID: 781898, Mode: "public", ScopeHash: strings.Repeat("a", 64), CustomerSessionHash: strings.Repeat("b", 64)},
		Items:           []Item{{Selection: Selection{Source: SourceCatalog, Kind: KindGood, ID: 17, StaffID: 6, Quantity: "1.125"}, Title: "Exact checkout", StaffName: "Master", UnitPriceKopecks: 123456789012, AmountKopecks: 138888887639}},
		SubtotalKopecks: 138888887639, PayableKopecks: 138888887639,
		Currency: "RUB", State: StatePrepared, IdempotencyKey: "repeated-browser-action",
		RequestHash: strings.Repeat("c", 64), CreatedAt: now, UpdatedAt: now,
		ExpiresAt: now.Add(15 * time.Minute), YClientsSyncStatus: "not_started", CustomerContact: "79000000000",
	}
}

func TestPostgresRoundTripPreservesMoneyAndNullableIdentity(t *testing.T) {
	for _, device := range []bool{false, true} {
		t.Run(map[bool]string{false: "public guest", true: "registered kiosk"}[device], func(t *testing.T) {
			order := postgresOrderFixture()
			if device {
				id := int64(5)
				order.Owner.KioskID, order.Owner.Mode, order.Owner.ClientID = &id, "kiosk", 71
				order.VisitID, order.RecordID = 22, 33
				order.CustomerName = "Test Customer"
			}
			conn := &repositoryTestConn{row: repositoryTestRow{order: order}}
			provider := &repositoryTestProvider{conn: conn}
			got, err := NewPostgresRepository(provider).Create(context.Background(), order)
			if err != nil || !reflect.DeepEqual(got, order) || provider.releases != 1 {
				t.Fatalf("round trip=%+v releases=%d error=%v", got, provider.releases, err)
			}
			if len(conn.args) != 23 {
				t.Fatalf("unexpected insert argument count %d", len(conn.args))
			}
			if conn.args[22] != order.CustomerName {
				t.Fatal("checkout did not store the customer name")
			}
			if !device && (conn.args[2] != (*int64)(nil) || conn.args[4] != nil || conn.args[7] != nil || conn.args[8] != nil) {
				t.Fatal("guest checkout stored zero foreign identifiers instead of SQL NULL")
			}
			var items []Item
			if err := json.Unmarshal([]byte(conn.args[9].(string)), &items); err != nil || !reflect.DeepEqual(items, order.Items) {
				t.Fatalf("item snapshot lost money or quantity: %+v error=%v", items, err)
			}
		})
	}
}

func TestPostgresIdempotentWinnerRemainsAuthoritative(t *testing.T) {
	winner := postgresOrderFixture()
	candidate := postgresOrderFixture()
	candidate.ID = "0755a8ee-a852-4c39-bf14-54fa3e58095e"
	candidate.RequestHash = strings.Repeat("d", 64)
	candidate.PayableKopecks = 1
	conn := &repositoryTestConn{row: repositoryTestRow{order: winner}}
	provider := &repositoryTestProvider{conn: conn}
	got, err := NewPostgresRepository(provider).Create(context.Background(), candidate)
	if err != nil || got.ID != winner.ID || got.RequestHash != winner.RequestHash || got.PayableKopecks != winner.PayableKopecks {
		t.Fatalf("existing winner replaced by candidate: %+v error=%v", got, err)
	}
	// The service must receive the winner's original hash and reject a different
	// body; database conflict handling must not return the caller's candidate.
	if provider.releases != 1 {
		t.Fatal("idempotent return retained the database lease")
	}
}

func TestPostgresFindByKeyBindsCompanyAndInteraction(t *testing.T) {
	order := postgresOrderFixture()
	conn := &repositoryTestConn{row: repositoryTestRow{order: order}}
	repository := NewPostgresRepository(&repositoryTestProvider{conn: conn})
	if _, err := repository.FindByKey(context.Background(), order.Owner, order.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	want := []any{order.Owner.CompanyID, order.Owner.CustomerSessionHash, order.IdempotencyKey}
	if !reflect.DeepEqual(conn.args, want) {
		t.Fatalf("lookup scope=%+v want=%+v", conn.args, want)
	}
	if _, err := repository.Get(context.Background(), order.ID); err != nil || !reflect.DeepEqual(conn.args, []any{order.ID}) {
		t.Fatalf("id lookup=%+v error=%v", conn.args, err)
	}
}

func TestPostgresReadFailuresReleaseLeaseAndFailClosed(t *testing.T) {
	for _, row := range []repositoryTestRow{
		{err: ds.ErrNoRows},
		{err: errors.New("connection lost")},
		{order: postgresOrderFixture(), snapshot: []byte("{\"not\":\"an item array\"}")},
		{order: postgresOrderFixture(), snapshot: []byte("[]")},
	} {
		conn := &repositoryTestConn{row: row}
		provider := &repositoryTestProvider{conn: conn}
		got, err := NewPostgresRepository(provider).Get(context.Background(), postgresOrderFixture().ID)
		if err == nil || got.ID != "" || provider.releases != 1 {
			t.Fatalf("failed read returned data or retained lease: %+v releases=%d error=%v", got, provider.releases, err)
		}
		if errors.Is(row.err, ds.ErrNoRows) && !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing checkout error=%v", err)
		}
		if row.err != nil && !errors.Is(row.err, ds.ErrNoRows) && !errors.Is(err, row.err) {
			t.Fatalf("database failure lost cause: %v", err)
		}
	}
	sentinel := errors.New("database unavailable")
	provider := &repositoryTestProvider{err: sentinel}
	if _, err := NewPostgresRepository(provider).Get(context.Background(), postgresOrderFixture().ID); !errors.Is(err, sentinel) || provider.releases != 0 {
		t.Fatalf("acquisition failure=%v releases=%d", err, provider.releases)
	}
	if _, err := NewPostgresRepository(nil).Get(context.Background(), postgresOrderFixture().ID); err == nil {
		t.Fatal("nil provider accepted")
	}
}
