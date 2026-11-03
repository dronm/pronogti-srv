package salon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dronm/ds/v4"
	"github.com/dronm/pronogti/internal/models"
)

type testProvider struct {
	ds.Provider
	conn     *testConn
	released int
}

func (p *testProvider) GetPrimary(context.Context) (ds.PoolConn, ds.ServerID, error) {
	return testLease{p.conn}, "primary", nil
}
func (p *testProvider) Release(ds.PoolConn, ds.ServerID) { p.released++ }

type testLease struct{ conn ds.Conn }

func (l testLease) Conn() ds.Conn { return l.conn }
func (l testLease) Release()      {}

type testConn struct {
	ds.Conn
	query string
	args  []any
	row   testRow
	rows  *testRows
}

func (c *testConn) QueryRow(_ context.Context, query string, args ...any) ds.Row {
	c.query, c.args = query, args
	return c.row
}
func (c *testConn) Query(_ context.Context, query string, args ...any) (ds.Rows, error) {
	c.query, c.args = query, args
	return c.rows, nil
}

type testRow struct {
	entry Entry
	err   error
}

func (r testRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*int64)) = r.entry.CompanyID
	*(dest[1].(*string)) = r.entry.Slug
	*(dest[2].(*string)) = r.entry.Name
	*(dest[3].(*bool)) = r.entry.IsActive
	*(dest[4].(*int64)) = r.entry.SessionVersion
	return nil
}

type testRows struct {
	entries []Entry
	index   int
	err     error
	closed  bool
}

func (r *testRows) Next() bool {
	if r.index >= len(r.entries) {
		return false
	}
	r.index++
	return true
}
func (r *testRows) Scan(dest ...any) error {
	return (testRow{entry: r.entries[r.index-1]}).Scan(dest...)
}
func (r *testRows) Err() error   { return r.err }
func (r *testRows) Close() error { r.closed = true; return nil }

func fixtureEntry() Entry {
	return Entry{SalonIdentity: models.SalonIdentity{CompanyID: 781898, Slug: "company-781898", Name: "Salon"}, IsActive: true, SessionVersion: 3}
}

func TestSlugLookupUsesExactParameterAndPreservesRevocationFields(t *testing.T) {
	entry := fixtureEntry()
	entry.IsActive = false
	conn := &testConn{row: testRow{entry: entry}}
	provider := &testProvider{conn: conn}
	registry := NewPostgresRegistry(provider)
	item, err := registry.FindBySlug(context.Background(), entry.Slug)
	if err != nil || item != entry || provider.released != 1 {
		t.Fatalf("item=%+v releases=%d error=%v", item, provider.released, err)
	}
	if !strings.Contains(conn.query, "WHERE slug=$1") || len(conn.args) != 1 || conn.args[0] != entry.Slug {
		t.Fatal("slug must be matched as one exact parameter")
	}
	conn.row.err = ds.ErrNoRows
	if _, err := registry.FindByCompanyID(context.Background(), 999); !errors.Is(err, ErrSalonNotFound) {
		t.Fatalf("missing salon error=%v", err)
	}
	if provider.released != 2 {
		t.Fatal("missing-row lookup retained a database lease")
	}
}

func TestPublicListIsActiveOnlyAndReleasesRowsOnIterationError(t *testing.T) {
	sentinel := errors.New("query interrupted")
	rows := &testRows{entries: []Entry{fixtureEntry()}, err: sentinel}
	conn := &testConn{rows: rows}
	provider := &testProvider{conn: conn}
	items, err := NewPostgresRegistry(provider).List(context.Background())
	if !errors.Is(err, sentinel) || items != nil || !rows.closed || provider.released != 1 {
		t.Fatalf("items=%+v closed=%t releases=%d error=%v", items, rows.closed, provider.released, err)
	}
	if !strings.Contains(conn.query, "WHERE is_active=true") || !strings.Contains(conn.query, "ORDER BY sort_order, name, company_id") {
		t.Fatal("public directory must filter active salons and order them consistently")
	}
	rows = &testRows{}
	conn.rows = rows
	items, err = NewPostgresRegistry(provider).List(context.Background())
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("empty list=%+v error=%v", items, err)
	}
}

func TestInvalidSalonsNeverQueryDatabase(t *testing.T) {
	conn := &testConn{}
	registry := NewPostgresRegistry(&testProvider{conn: conn})
	for _, slug := range []string{"", "Salon", "../salon", "two--salons", "salon/1", strings.Repeat("a", 101)} {
		if _, err := registry.FindBySlug(context.Background(), slug); !errors.Is(err, ErrSalonNotFound) {
			t.Fatalf("accepted invalid public slug %q", slug)
		}
	}
	for _, item := range []struct {
		company int64
		slug    string
		name    string
	}{
		{0, "valid", "Name"},
		{1, "INVALID", "Name"},
		{1, "valid", "  "},
		{1, "valid", strings.Repeat("я", 201)},
	} {
		if _, err := registry.Upsert(context.Background(), item.company, item.slug, item.name); err == nil {
			t.Fatalf("accepted invalid salon %+v", item)
		}
	}
	if conn.query != "" {
		t.Fatal("invalid salon reached the database")
	}
}

func TestUpsertTrimsNameAndDisableUsesParameters(t *testing.T) {
	conn := &testConn{row: testRow{entry: fixtureEntry()}}
	registry := NewPostgresRegistry(&testProvider{conn: conn})
	if _, err := registry.Upsert(context.Background(), 781898, " pronogti ", " ПроНогти "); err != nil {
		t.Fatal(err)
	}
	if len(conn.args) != 3 || conn.args[0] != int64(781898) || conn.args[1] != "pronogti" || conn.args[2] != "ПроНогти" {
		t.Fatalf("upsert parameters=%+v", conn.args)
	}
	if err := registry.SetActive(context.Background(), 781898, false); err != nil || len(conn.args) != 2 || conn.args[1] != false {
		t.Fatalf("disable parameters=%+v error=%v", conn.args, err)
	}
	encoded, err := json.Marshal(fixtureEntry())
	if err != nil || strings.Contains(string(encoded), "session_version") || strings.Contains(string(encoded), "is_active") {
		t.Fatalf("public JSON=%s error=%v", encoded, err)
	}
}
