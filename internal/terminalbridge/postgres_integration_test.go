package terminalbridge

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/ds/v4/pgds"
	"github.com/dronm/pronogti/internal/checkout"
	"github.com/jackc/pgx/v5"
)

// Each test creates and drops its own database. An explicit loopback-only test
// connection is required; this never reads the application's config.json.
func bridgeIntegrationDatabase(t *testing.T) (ds.Provider, *pgx.Conn) {
	t.Helper()
	connection := os.Getenv("PRONOGTI_TEST_DATABASE_URL")
	if connection == "" {
		t.Skip("set PRONOGTI_TEST_DATABASE_URL to an isolated local PostgreSQL server")
	}
	configuration, err := pgx.ParseConfig(connection)
	if err != nil {
		t.Fatal("invalid PRONOGTI_TEST_DATABASE_URL")
	}
	if configuration.Host != "localhost" && configuration.Host != "127.0.0.1" && configuration.Host != "::1" {
		t.Fatal("terminal integration tests require a loopback PostgreSQL server")
	}
	ctx := context.Background()
	admin, err := pgx.ConnectConfig(ctx, configuration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	name := "terminal_bridge_test_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP DATABASE "+quoted+" WITH (FORCE)") })
	configuration.Database = name
	conn, err := pgx.ConnectConfig(ctx, configuration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	migrations, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	if err != nil || len(migrations) < 24 {
		t.Fatalf("terminal migration inventory: %d, %v", len(migrations), err)
	}
	for _, migration := range migrations {
		data, err := os.ReadFile(migration)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(data)); err != nil {
			t.Fatalf("migration %s: %v", filepath.Base(migration), err)
		}
	}
	poolURL := url.URL{Scheme: "postgres", User: url.UserPassword(configuration.User, configuration.Password), Host: net.JoinHostPort(configuration.Host, strconv.Itoa(int(configuration.Port))), Path: "/" + name, RawQuery: "sslmode=disable"}
	provider, err := pgds.New(&pgds.Config{PrimaryConnStr: poolURL.String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.(*pgds.Provider).Close() })
	return provider, conn
}

func bridgeIntegrationUUID(t *testing.T) string {
	t.Helper()
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		t.Fatal(err)
	}
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:])
}

func bridgeIntegrationOrder(t *testing.T, provider ds.Provider, owner checkout.Owner, key string) checkout.Order {
	t.Helper()
	now := time.Now().UTC()
	order := checkout.Order{
		ID: bridgeIntegrationUUID(t), Owner: owner,
		Items:           []checkout.Item{{Selection: checkout.Selection{Source: checkout.SourceCatalog, Kind: checkout.KindGood, ID: 7, StaffID: 9, Quantity: "1"}, Title: "Terminal integration good", UnitPriceKopecks: 12345, AmountKopecks: 12345}},
		SubtotalKopecks: 12345, PayableKopecks: 12345, Currency: "RUB", State: "prepared",
		IdempotencyKey: key, RequestHash: strings.Repeat("c", 64),
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(15 * time.Minute),
		YClientsSyncStatus: "not_started", CustomerContact: "79990000000",
	}
	stored, err := checkout.NewPostgresRepository(provider).Create(context.Background(), order)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func bridgeIntegrationJSON(t *testing.T, value any, target any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func bridgeIntegrationCounts(t *testing.T, conn *pgx.Conn, orderID string) (string, int, int) {
	t.Helper()
	var state string
	var receipts, settlements int
	err := conn.QueryRow(context.Background(), `SELECT o.state,
		(SELECT count(*) FROM public.fiscal_receipts WHERE order_id=o.id),
		(SELECT count(*) FROM public.yclients_jobs WHERE order_id=o.id)
		FROM public.payment_orders o WHERE o.id=$1`, orderID).Scan(&state, &receipts, &settlements)
	if err != nil {
		t.Fatal(err)
	}
	return state, receipts, settlements
}

func TestTerminalIntegrationMigrationProfileBoundary(t *testing.T) {
	_, conn := bridgeIntegrationDatabase(t)
	ctx := context.Background()
	var id int64
	if err := conn.QueryRow(ctx, `INSERT INTO public.payment_profiles
		(company_id,name,environment,api_base_url,terminals_enabled,enabled)
		VALUES(781898,'Terminal-only profile','mock','',true,true) RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("terminal-only profile required fake online credentials: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO public.payment_profiles
		(company_id,name,environment,api_base_url,enabled)
		VALUES(781898,'Invalid online profile','mock','https://bank.invalid',true)`); err == nil {
		t.Fatal("online profile no longer requires its online acquiring configuration")
	}
	if _, err := conn.Exec(ctx, `INSERT INTO public.payment_profiles
		(company_id,name,environment,api_base_url,enabled)
		VALUES(781898,'Invalid empty online endpoint','mock','',true)`); err == nil {
		t.Fatal("online profile accepted an empty bank URL")
	}
	var names []string
	rows, err := conn.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename IN
		('terminal_agents','payment_terminals','terminal_commands','terminal_agent_events','terminal_resolutions') ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(names) != 5 {
		t.Fatalf("terminal infrastructure incomplete: %v", names)
	}
	for _, index := range []string{"public.payment_terminals_enabled_physical_key", "public.terminal_commands_one_physical_idx"} {
		var definition string
		if err := conn.QueryRow(ctx, `SELECT pg_get_indexdef($1::regclass)`, index).Scan(&definition); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(definition, "(company_id, physical_id)") {
			t.Fatalf("physical lock is scoped to an agent alias instead of the original device: %s", definition)
		}
	}
}

func TestTerminalIntegrationMigrationCanRollbackAndReapplyBeforeUse(t *testing.T) {
	_, conn := bridgeIntegrationDatabase(t)
	ctx := context.Background()
	for _, name := range []string{"000024_terminal_payments.down.sql", "000024_terminal_payments.up.sql"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(data)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	var table *string
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.terminal_commands')::text`).Scan(&table); err != nil || table == nil {
		t.Fatal(table, err)
	}
}
