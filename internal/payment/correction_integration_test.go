package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/correction"
	"github.com/dronm/pronogti/internal/fiscal"
	"github.com/jackc/pgx/v5"
)

type correctionIntegrationCall struct {
	RequestID string
	Receipt   correction.Receipt
}

// All bank and hardware interactions in these database tests use in-process
// adapters. A production environment exercises worker gates without contacting
// a bank, an OFD, or a physical fiscal device.
type correctionIntegrationDriver struct {
	t               *testing.T
	posts           []correctionIntegrationCall
	lookups         []string
	failReplacement bool
	unknownReversal bool
	lookupCompletes bool
	saleCalls       int
}

func correctionIntegrationResponse(requestID, command, status string, number int) json.RawMessage {
	response, _ := json.Marshal(map[string]any{
		"deviceId": "correction-test-kkt", "requestId": requestID, "cmd": command, "status": status,
		"data": map[string]any{"shiftOpenedByRequest": false, "receipt": map[string]any{
			"receiptNumber": number, "documentNumber": number, "fiscalSign": 123000 + number,
			"dateTime": time.Now().Add(-24 * time.Hour).UTC().Format("2006-01-02T15:04"), "shiftNumber": 2,
		}},
	})
	return response
}

func (d *correctionIntegrationDriver) Create(_ context.Context, _ fiscal.DriverConfig, requestID string, _ json.RawMessage) (fiscal.DriverResult, error) {
	d.saleCalls++
	return fiscal.DriverResult{State: "completed", Response: correctionIntegrationResponse(requestID, "createReceipt", "completed", 10)}, nil
}

func (d *correctionIntegrationDriver) Lookup(context.Context, fiscal.DriverConfig, string) (fiscal.DriverResult, error) {
	d.t.Fatal("original sale receipt unexpectedly required recovery")
	return fiscal.DriverResult{}, nil
}

func (d *correctionIntegrationDriver) CreateCorrection(_ context.Context, _ fiscal.DriverConfig, requestID string, payload json.RawMessage) (fiscal.DriverResult, error) {
	var receipt correction.Receipt
	if err := json.Unmarshal(payload, &receipt); err != nil {
		d.t.Fatal(err)
	}
	d.posts = append(d.posts, correctionIntegrationCall{RequestID: requestID, Receipt: receipt})
	if receipt.CalculationSign == 2 && d.unknownReversal {
		return fiscal.DriverResult{State: "unknown", ErrorCode: "outcome_unknown", Response: json.RawMessage(`{"status":"unknown","error":{"code":"outcome_unknown","message":"Simulated lost finalization response"}}`)}, nil
	}
	if receipt.CalculationSign == 1 && d.failReplacement {
		return fiscal.DriverResult{State: "failed", ErrorCode: "device_error", Response: json.RawMessage(`{"status":"failed","error":{"code":"device_error","message":"Simulated definite device rejection"}}`)}, nil
	}
	return fiscal.DriverResult{State: "completed", Response: correctionIntegrationResponse(requestID, "createCorrectionReceipt", "completed", 20+len(d.posts))}, nil
}

func (d *correctionIntegrationDriver) LookupCorrection(_ context.Context, _ fiscal.DriverConfig, requestID string) (fiscal.DriverResult, error) {
	d.lookups = append(d.lookups, requestID)
	if !d.lookupCompletes {
		return fiscal.DriverResult{State: "unknown", ErrorCode: "outcome_unknown", Response: json.RawMessage(`{"status":"unknown","error":{"code":"outcome_unknown"}}`)}, nil
	}
	return fiscal.DriverResult{State: "completed", Response: correctionIntegrationResponse(requestID, "createCorrectionReceipt", "completed", 21)}, nil
}

func correctionIntegrationSale(t *testing.T, live bool) (ds.Provider, *pgx.Conn, checkout.Order, View, int64, *correctionIntegrationDriver) {
	t.Helper()
	provider, conn := completionTestDatabase(t)
	profileID := cardIntegrationProfile(t, conn, "https://bank.invalid")
	ctx := context.Background()
	if live {
		if _, err := conn.Exec(ctx, `UPDATE public.payment_profiles SET environment='production' WHERE id=$1`, profileID); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `UPDATE public.fiscal_profiles SET mode='live',driver_base_url='http://127.0.0.1:8190',device_id='correction-test-kkt' WHERE company_id=781898`); err != nil {
			t.Fatal(err)
		}
	}
	var operatorID int64
	if err := conn.QueryRow(ctx, `INSERT INTO public.users(name,role_id) VALUES('Correction operator','admin') RETURNING id::bigint`).Scan(&operatorID); err != nil {
		t.Fatal(err)
	}
	actor := cardIntegrationActor()
	order := completionOrder(t, provider, actor, "correction-checkout")
	repository := NewPostgresRepository(provider)
	service := NewService(Dependencies{Repository: repository, CardBank: cardIntegrationCreateApproved{}, Tokens: cardIntegrationTokens{}}, Options{})
	view, err := service.InitCard(ctx, actor, "card:"+order.ID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if live {
		worker := NewCardConfirmationWorker(repository, cardIntegrationApprovedChecker{}, cardIntegrationTokens{}, ConfirmationOptions{})
		if worked, err := worker.ProcessOne(ctx); err != nil || !worked {
			t.Fatalf("confirm paid fixture: worked=%v error=%v", worked, err)
		}
	} else if err := repository.ConfirmDemo(ctx, view.PaymentID); err != nil {
		t.Fatal(err)
	}
	driver := &correctionIntegrationDriver{t: t}
	worker := fiscal.NewWorker(fiscal.NewPostgresRepository(provider), driver, fiscal.Options{AllowLive: live})
	if worked, err := worker.ProcessOne(ctx); err != nil || !worked {
		t.Fatalf("issue original fixture receipt: worked=%v error=%v", worked, err)
	}
	if live && driver.saleCalls != 1 || !live && driver.saleCalls != 0 {
		t.Fatalf("wrong sale fixture dispatch count: %d", driver.saleCalls)
	}
	return provider, conn, order, view, operatorID, driver
}

func correctionIntegrationRequest(t *testing.T, quote correction.Quote, title string) correction.CreateRequest {
	t.Helper()
	encoded, err := json.Marshal(quote.Source.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var receipt correction.Receipt
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		t.Fatal(err)
	}
	receipt.Items[0].Name, receipt.Items[0].VATRate = title, 5
	receipt.AdditionalData.CorrectedCalculationDate = 0
	receipt.AdditionalData.PrescriptionNumber = ""
	receipt.AdditionalData.AdditionalReceiptProperty = ""
	return correction.CreateRequest{SourceVersion: quote.Source.Version, Reason: "Исправление наименования и ставки НДС", Basis: "voluntary", CalculationDate: quote.Source.CalculationDate, Receipt: receipt}
}

func correctionIntegrationProcess(t *testing.T, service *correction.Service) {
	t.Helper()
	if worked, err := service.ProcessOne(context.Background()); err != nil || !worked {
		t.Fatalf("process correction document: worked=%v error=%v", worked, err)
	}
}

func TestCorrectionIntegrationPairIdempotencyAndEffectiveSource(t *testing.T) {
	provider, conn, order, view, operatorID, driver := correctionIntegrationSale(t, false)
	ctx := context.Background()
	service := correction.NewService(correction.NewPostgresRepository(provider), driver, correction.Options{Location: time.UTC})
	quote, err := service.Quote(ctx, order.ID)
	if err != nil || !quote.Eligible || quote.Source.Version != "sale" {
		t.Fatalf("original correction quote: %+v error=%v", quote, err)
	}
	request := correctionIntegrationRequest(t, quote, "Исправленный товар")
	preview, err := service.Preview(ctx, order.ID, request)
	if err != nil || preview.Reversal.Items[0].Name != "Integration good" || preview.Reversal.Items[0].VATRate != 6 || preview.Reversal.CalculationSign != 2 ||
		preview.Replacement.Items[0].Name != "Исправленный товар" || preview.Replacement.Items[0].VATRate != 5 || preview.Replacement.CalculationSign != 1 {
		t.Fatalf("wrong reversal/replacement preview: %+v error=%v", preview, err)
	}
	created, err := service.Create(ctx, order.ID, operatorID, "correction-first", request)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for index := 0; index < 4; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			replayed, err := service.Create(ctx, order.ID, operatorID, "correction-first", request)
			if err != nil || replayed.ID != created.ID {
				t.Errorf("concurrent idempotent correction changed identity: %+v error=%v", replayed, err)
			}
		}()
	}
	group.Wait()
	correctionIntegrationProcess(t, service)
	correctionIntegrationProcess(t, service)
	completed, err := service.Get(ctx, order.ID, created.ID)
	if err != nil || completed.State != "completed" || len(completed.Documents) != 2 || completed.Documents[0].State != "demo" || completed.Documents[1].State != "demo" {
		t.Fatalf("demo correction pair not completed: %+v error=%v", completed, err)
	}
	if len(driver.posts) != 0 || len(driver.lookups) != 0 {
		t.Fatal("demo correction contacted hardware")
	}
	var count int
	var bankState, originalName, correctedName string
	var correctedVAT int
	if err := conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM public.fiscal_corrections WHERE order_id=$1),p.state,
		fr.request_payload#>>'{items,0,name}',c.replacement_payload#>>'{items,0,name}',(c.replacement_payload#>>'{items,0,vatRate}')::int
		FROM public.payments p JOIN public.fiscal_receipts fr ON fr.payment_id=p.id
		JOIN public.fiscal_corrections c ON c.payment_id=p.id WHERE p.id=$2`, order.ID, view.PaymentID).Scan(&count, &bankState, &originalName, &correctedName, &correctedVAT); err != nil {
		t.Fatal(err)
	}
	if count != 1 || bankState != "paid" || originalName != "Integration good" || correctedName != "Исправленный товар" || correctedVAT != 5 {
		t.Fatalf("correction changed bank/source or lost edited data: count=%d bank=%s source=%s corrected=%s VAT=%d", count, bankState, originalName, correctedName, correctedVAT)
	}
	next, err := service.Quote(ctx, order.ID)
	if err != nil || !next.Eligible || next.Source.Version != created.ID || next.Source.Payload.Items[0].Name != "Исправленный товар" || len(next.Corrections) != 1 {
		t.Fatalf("next quote did not use completed replacement: %+v error=%v", next, err)
	}
	if _, err := service.Create(ctx, order.ID, operatorID, "stale-source", request); err == nil {
		t.Fatal("stale original source could create another correction")
	}
	replayed, err := service.Create(ctx, order.ID, operatorID, "correction-first", request)
	if err != nil || replayed.ID != created.ID {
		t.Fatalf("completed original request could not replay: %+v error=%v", replayed, err)
	}
	secondRequest := correctionIntegrationRequest(t, next, "Повторно исправленный товар")
	second, err := service.Create(ctx, order.ID, operatorID, "correction-second", secondRequest)
	if err != nil || second.SourceVersion != created.ID {
		t.Fatalf("correction of latest replacement failed: %+v error=%v", second, err)
	}
	blocked, err := service.Quote(ctx, order.ID)
	if err != nil || blocked.Eligible || !blocked.Source.HasOpenCorrection {
		t.Fatalf("unfinished correction did not reserve the source: %+v error=%v", blocked, err)
	}
}

func TestCorrectionIntegrationReplacementResumeDoesNotRepeatReversal(t *testing.T) {
	provider, _, order, _, operatorID, driver := correctionIntegrationSale(t, true)
	driver.failReplacement = true
	ctx := context.Background()
	service := correction.NewService(correction.NewPostgresRepository(provider), driver, correction.Options{AllowLive: true, Location: time.UTC})
	quote, err := service.Quote(ctx, order.ID)
	if err != nil || !quote.Eligible {
		t.Fatalf("live quote: %+v error=%v", quote, err)
	}
	created, err := service.Create(ctx, order.ID, operatorID, "failed-replacement", correctionIntegrationRequest(t, quote, "Corrected item"))
	if err != nil {
		t.Fatal(err)
	}
	correctionIntegrationProcess(t, service)
	correctionIntegrationProcess(t, service)
	failed, err := service.Get(ctx, order.ID, created.ID)
	if err != nil || failed.State != "failed" || len(failed.Documents) != 2 || failed.Documents[0].State != "issued" || failed.Documents[1].State != "failed" {
		t.Fatalf("partially completed pair was not preserved: %+v error=%v", failed, err)
	}
	driver.failReplacement = false
	resumed, err := service.Resume(ctx, order.ID, created.ID, operatorID, "resume-replacement")
	if err != nil || len(resumed.Documents) != 3 {
		t.Fatalf("resume did not append an audited attempt: %+v error=%v", resumed, err)
	}
	replayed, err := service.Resume(ctx, order.ID, created.ID, operatorID, "resume-replacement")
	if err != nil || len(replayed.Documents) != 3 {
		t.Fatalf("resume key created another attempt: %+v error=%v", replayed, err)
	}
	correctionIntegrationProcess(t, service)
	completed, err := service.Get(ctx, order.ID, created.ID)
	if err != nil || completed.State != "completed" || len(completed.Documents) != 3 {
		t.Fatalf("replacement did not finish after resume: %+v error=%v", completed, err)
	}
	if len(driver.posts) != 3 || driver.posts[0].Receipt.CalculationSign != 2 || driver.posts[1].Receipt.CalculationSign != 1 || driver.posts[2].Receipt.CalculationSign != 1 || driver.posts[1].RequestID == driver.posts[2].RequestID {
		t.Fatalf("resume repeated a reversal or reused a failed request: %+v", driver.posts)
	}
	if len(driver.lookups) != 0 {
		t.Fatalf("definite failed attempts unexpectedly needed lookup: %+v", driver.lookups)
	}
}

func TestCorrectionIntegrationUnknownOutcomeOnlyLooksUpOriginalRequest(t *testing.T) {
	provider, conn, order, _, operatorID, driver := correctionIntegrationSale(t, true)
	driver.unknownReversal = true
	ctx := context.Background()
	service := correction.NewService(correction.NewPostgresRepository(provider), driver, correction.Options{AllowLive: true, Location: time.UTC})
	quote, err := service.Quote(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, order.ID, operatorID, "unknown-reversal", correctionIntegrationRequest(t, quote, "Corrected item"))
	if err != nil {
		t.Fatal(err)
	}
	correctionIntegrationProcess(t, service)
	unknown, err := service.Get(ctx, order.ID, created.ID)
	if err != nil || unknown.State != "unknown" || len(driver.posts) != 1 {
		t.Fatalf("uncertain reversal was not retained: %+v error=%v calls=%+v", unknown, err, driver.posts)
	}
	if _, err := service.Resume(ctx, order.ID, created.ID, operatorID, "unsafe-resume"); err == nil {
		t.Fatal("unknown outcome allowed a new mutating attempt")
	}
	makeDue := func() {
		t.Helper()
		if _, err := conn.Exec(ctx, `UPDATE public.fiscal_correction_documents SET next_attempt_at=now() WHERE correction_id=$1 AND state='unknown'`, created.ID); err != nil {
			t.Fatal(err)
		}
	}
	makeDue()
	correctionIntegrationProcess(t, service)
	if len(driver.posts) != 1 || len(driver.lookups) != 1 || driver.lookups[0] != driver.posts[0].RequestID {
		t.Fatalf("unknown outcome was reissued instead of looked up: posts=%+v lookups=%+v", driver.posts, driver.lookups)
	}
	driver.lookupCompletes = true
	makeDue()
	correctionIntegrationProcess(t, service)
	correctionIntegrationProcess(t, service)
	completed, err := service.Get(ctx, order.ID, created.ID)
	if err != nil || completed.State != "completed" || len(driver.posts) != 2 || driver.posts[1].Receipt.CalculationSign != 1 || len(driver.lookups) != 2 {
		t.Fatalf("reconciled reversal did not allow exactly one replacement: %+v error=%v posts=%+v lookups=%+v", completed, err, driver.posts, driver.lookups)
	}
	if driver.lookups[0] != driver.lookups[1] {
		t.Fatal(fmt.Sprintf("recovery changed request identity: %+v", driver.lookups))
	}
}
