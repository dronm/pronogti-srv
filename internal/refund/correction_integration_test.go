package refund

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/dronm/pronogti/internal/correction"
	"github.com/dronm/pronogti/internal/fiscal"
	"github.com/dronm/pronogti/internal/payment"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Like payment's integration fixture, this creates a separate database only
// when an explicit loopback-only test server is supplied. It never touches the
// configured application's database or a real bank/device.
func refundCorrectionDatabase(t *testing.T) (ds.Provider, *pgx.Conn) {
	t.Helper()
	value := os.Getenv("PRONOGTI_TEST_DATABASE_URL")
	if value == "" {
		t.Skip("set PRONOGTI_TEST_DATABASE_URL to an isolated local PostgreSQL server")
	}
	config, err := pgx.ParseConfig(value)
	if err != nil {
		t.Fatal("invalid PRONOGTI_TEST_DATABASE_URL")
	}
	if config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1" {
		t.Fatal("integration tests require a loopback PostgreSQL server")
	}
	ctx := context.Background()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	name := fmt.Sprintf("pronogti_refund_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP DATABASE "+quoted+" WITH (FORCE)") })
	config.Database = name
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	if err != nil || len(files) < 25 {
		t.Fatalf("migration inventory: %d, %v", len(files), err)
	}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(body)); err != nil {
			t.Fatalf("migration %s: %v", filepath.Base(file), err)
		}
	}
	poolURL := url.URL{Scheme: "postgres", User: url.UserPassword(config.User, config.Password), Host: net.JoinHostPort(config.Host, strconv.Itoa(int(config.Port))), Path: "/" + name, RawQuery: "sslmode=disable"}
	provider, err := pgds.New(&pgds.Config{PrimaryConnStr: poolURL.String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.(*pgds.Provider).Close() })
	return provider, conn
}

type refundCorrectionNoHardware struct{ t *testing.T }

func (d refundCorrectionNoHardware) Create(context.Context, fiscal.DriverConfig, string, json.RawMessage) (fiscal.DriverResult, error) {
	d.t.Fatal("demo fixture contacted fiscal hardware")
	return fiscal.DriverResult{}, nil
}

func (d refundCorrectionNoHardware) Lookup(context.Context, fiscal.DriverConfig, string) (fiscal.DriverResult, error) {
	d.t.Fatal("demo fixture contacted fiscal request journal")
	return fiscal.DriverResult{}, nil
}

type refundCorrectionFixture struct {
	provider    ds.Provider
	conn        *pgx.Conn
	order       checkout.Order
	paymentID   string
	operatorID  int64
	corrections *correction.Service
}

func newRefundCorrectionFixture(t *testing.T) refundCorrectionFixture {
	t.Helper()
	provider, conn := refundCorrectionDatabase(t)
	ctx := context.Background()
	var profileID, operatorID int64
	if err := conn.QueryRow(ctx, `INSERT INTO public.payment_profiles
		(company_id,name,environment,api_base_url,jwt_env,customer_code,sbp_merchant_id,account_id,enabled)
		VALUES(781898,'Refund correction profile','mock','https://bank.invalid','PRONOGTI_TEST_JWT','customer-test','merchant-test','40702810900000000001/044525104',true)
		RETURNING id`).Scan(&profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE public.salons SET default_payment_profile_id=$1 WHERE company_id=781898`, profileID); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `INSERT INTO public.users(name,role_id) VALUES('Refund correction operator','admin') RETURNING id::bigint`).Scan(&operatorID); err != nil {
		t.Fatal(err)
	}
	actor := checkout.Actor{CompanyID: 781898, Mode: "public", ClientID: 42, ScopeID: "refund-correction-scope", CustomerSessionID: "refund-correction-customer"}
	scope, customer := sha256.Sum256([]byte(actor.ScopeID)), sha256.Sum256([]byte(actor.CustomerSessionID))
	now := time.Now().UTC()
	order := checkout.Order{
		ID: uuid.NewString(), Owner: checkout.Owner{CompanyID: actor.CompanyID, Mode: actor.Mode, ClientID: actor.ClientID, ScopeHash: hex.EncodeToString(scope[:]), CustomerSessionHash: hex.EncodeToString(customer[:])},
		Items:           []checkout.Item{{Selection: checkout.Selection{Source: checkout.SourceCatalog, Kind: checkout.KindGood, ID: 7, StaffID: 9, Quantity: "1"}, Title: "Original good", UnitPriceKopecks: 12345, AmountKopecks: 12345}},
		SubtotalKopecks: 12345, PayableKopecks: 12345, Currency: "RUB", State: "prepared", IdempotencyKey: "refund-correction-checkout", RequestHash: strings.Repeat("a", 64),
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(15 * time.Minute), YClientsSyncStatus: "not_started", CustomerContact: "79990000000",
	}
	stored, err := checkout.NewPostgresRepository(provider).Create(ctx, order)
	if err != nil {
		t.Fatal(err)
	}
	payments := payment.NewPostgresRepository(provider)
	service := payment.NewService(payment.Dependencies{Repository: payments, Bank: payment.SDKBank{}}, payment.Options{})
	view, err := service.Init(ctx, actor, "qr:"+order.ID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := payments.ConfirmDemo(ctx, view.PaymentID); err != nil {
		t.Fatal(err)
	}
	worker := fiscal.NewWorker(fiscal.NewPostgresRepository(provider), refundCorrectionNoHardware{t}, fiscal.Options{})
	if worked, err := worker.ProcessOne(ctx); err != nil || !worked {
		t.Fatalf("original demo receipt: worked=%v error=%v", worked, err)
	}
	return refundCorrectionFixture{
		provider: provider, conn: conn, order: stored, paymentID: view.PaymentID, operatorID: operatorID,
		corrections: correction.NewService(correction.NewPostgresRepository(provider), nil, correction.Options{Location: time.UTC}),
	}
}

func (f refundCorrectionFixture) request(t *testing.T, title string, cash bool) correction.CreateRequest {
	t.Helper()
	quote, err := f.corrections.Quote(context.Background(), f.order.ID)
	if err != nil || !quote.Eligible {
		t.Fatalf("correction quote: %+v error=%v", quote, err)
	}
	receipt := quote.Source.Payload
	receipt.Items[0].Name, receipt.Items[0].VATRate = title, 5
	receipt.AdditionalData.CorrectedCalculationDate = 0
	receipt.AdditionalData.PrescriptionNumber = ""
	receipt.AdditionalData.AdditionalReceiptProperty = ""
	if cash {
		receipt.Payment.CashKopecks, receipt.Payment.CashlessKopecks = receipt.TotalKopecks, 0
	}
	return correction.CreateRequest{SourceVersion: quote.Source.Version, Reason: "Исправление реквизитов чека", Basis: "voluntary", CalculationDate: quote.Source.CalculationDate, Receipt: receipt}
}

func (f refundCorrectionFixture) complete(t *testing.T, id string) {
	t.Helper()
	for index := 0; index < 2; index++ {
		if worked, err := f.corrections.ProcessOne(context.Background()); err != nil || !worked {
			t.Fatalf("demo correction document %d: worked=%v error=%v", index, worked, err)
		}
	}
	finished, err := f.corrections.Get(context.Background(), f.order.ID, id)
	if err != nil || finished.State != "completed" {
		t.Fatalf("correction did not complete: %+v error=%v", finished, err)
	}
}

func TestRefundCorrectionIntegrationLoadsEffectiveReceiptAndReservesReturn(t *testing.T) {
	f := newRefundCorrectionFixture(t)
	ctx := context.Background()
	repository := NewPostgresRepository(f.provider)
	service := NewService(repository, SDKBank{}, nil, false)
	request := f.request(t, "Исправленный товар", false)
	created, err := f.corrections.Create(ctx, f.order.ID, f.operatorID, "correct-sale", request)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := service.Quote(ctx, f.paymentID)
	if err != nil || blocked.Eligible || blocked.Reason != "receipt_correction_incomplete" {
		t.Fatalf("unfinished correction did not block refund SQL: %+v error=%v", blocked, err)
	}
	selection := CreateRequest{Items: []Selection{{LineIndex: 0, Quantity: "1"}}}
	if _, err := service.Create(ctx, f.paymentID, f.operatorID, "blocked-refund", selection); err == nil {
		t.Fatal("refund Reserve allowed an unfinished fiscal correction")
	}
	f.complete(t, created.ID)
	sale, previous, err := repository.LoadSale(ctx, f.paymentID)
	if err != nil || len(previous) != 0 || sale.CorrectionBlockReason != "" || len(sale.Items) != 1 || sale.Items[0].Title != "Исправленный товар" || sale.Items[0].AmountKopecks != 12345 {
		t.Fatalf("effective fiscal sale not loaded: %+v previous=%+v error=%v", sale, previous, err)
	}
	var saved struct {
		Items []struct {
			VATRate int `json:"vatRate"`
		} `json:"items"`
	}
	if err := json.Unmarshal(sale.SaleReceipt, &saved); err != nil || len(saved.Items) != 1 || saved.Items[0].VATRate != 5 {
		t.Fatalf("refund loaded original fiscal VAT: %s error=%v", sale.SaleReceipt, err)
	}
	quote, err := service.Quote(ctx, f.paymentID)
	if err != nil || !quote.Eligible || len(quote.Items) != 1 || quote.Items[0].Title != "Исправленный товар" || quote.AvailableKopecks != 12345 {
		t.Fatalf("corrected refund quote: %+v error=%v", quote, err)
	}
	returned, err := service.Create(ctx, f.paymentID, f.operatorID, "corrected-refund", selection)
	if err != nil || returned.BankState != "queued" || returned.FiscalState != "waiting" || returned.AmountKopecks != 12345 {
		t.Fatalf("corrected return could not be reserved: %+v error=%v", returned, err)
	}
	replayed, err := service.Create(ctx, f.paymentID, f.operatorID, "corrected-refund", selection)
	if err != nil || replayed.ID != returned.ID {
		t.Fatalf("corrected refund replay changed identity: %+v error=%v", replayed, err)
	}
	var receipt struct {
		CalculationSign int `json:"calculationSign"`
		Items           []struct {
			Name    string `json:"name"`
			VATRate int    `json:"vatRate"`
		} `json:"items"`
		AdditionalData map[string]json.RawMessage `json:"additionalData"`
	}
	if err := json.Unmarshal(returned.ReceiptPayload, &receipt); err != nil || receipt.CalculationSign != 2 || len(receipt.Items) != 1 || receipt.Items[0].Name != "Исправленный товар" || receipt.Items[0].VATRate != 5 {
		t.Fatalf("reserved return lost corrected fiscal attributes: %s error=%v", returned.ReceiptPayload, err)
	}
	if len(receipt.AdditionalData["correctedCalculationDate"]) != 0 {
		t.Fatal("reserved normal return contains correction-only metadata")
	}
	blockedCorrection, err := f.corrections.Quote(ctx, f.order.ID)
	if err != nil || blockedCorrection.Eligible || !blockedCorrection.Source.HasRefunds {
		t.Fatalf("reserved refund did not block further correction: %+v error=%v", blockedCorrection, err)
	}
	var bankState, sourceName string
	if err := f.conn.QueryRow(ctx, `SELECT p.state,fr.request_payload#>>'{items,0,name}' FROM public.payments p JOIN public.fiscal_receipts fr ON fr.payment_id=p.id WHERE p.id=$1`, f.paymentID).Scan(&bankState, &sourceName); err != nil {
		t.Fatal(err)
	}
	if bankState != "paid" || sourceName != "Original good" {
		t.Fatalf("refund reservation changed the original payment/source: state=%s name=%s", bankState, sourceName)
	}
}

func TestRefundCorrectionIntegrationRejectsChangedPaymentAllocation(t *testing.T) {
	f := newRefundCorrectionFixture(t)
	ctx := context.Background()
	first, err := f.corrections.Create(ctx, f.order.ID, f.operatorID, "correct-name", f.request(t, "First correction", false))
	if err != nil {
		t.Fatal(err)
	}
	f.complete(t, first.ID)
	second, err := f.corrections.Create(ctx, f.order.ID, f.operatorID, "correct-payment", f.request(t, "Latest correction", true))
	if err != nil {
		t.Fatal(err)
	}
	f.complete(t, second.ID)
	repository := NewPostgresRepository(f.provider)
	sale, _, err := repository.LoadSale(ctx, f.paymentID)
	if err != nil || sale.CorrectionBlockReason != "corrected_receipt_refund_unsupported" {
		t.Fatalf("latest completed cash correction not used: %+v error=%v", sale, err)
	}
	service := NewService(repository, SDKBank{}, nil, false)
	quote, err := service.Quote(ctx, f.paymentID)
	if err != nil || quote.Eligible || quote.Reason != "corrected_receipt_refund_unsupported" {
		t.Fatalf("automatic refund accepted changed payment meaning: %+v error=%v", quote, err)
	}
	if _, err := service.Create(ctx, f.paymentID, f.operatorID, "unsafe-return", CreateRequest{Items: []Selection{{LineIndex: 0, Quantity: "1"}}}); err == nil {
		t.Fatal("refund Reserve accepted a cash correction against a cashless bank payment")
	}
	var count int
	if err := f.conn.QueryRow(ctx, `SELECT count(*) FROM public.payment_refunds WHERE payment_id=$1`, f.paymentID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unsupported correction reserved a bank refund: count=%d error=%v", count, err)
	}
}
