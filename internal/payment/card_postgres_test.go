package payment

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/paytochkabank"
)

func cardPGRecord(c ConfirmationCandidate, state string, claimed *time.Time) qrPGRow {
	row := confirmationPGRecord(c, state, claimed, "")
	row.values[19], row.values[20] = "card", c.Attempt.OperationID
	return row
}

func cardPGCandidate() ConfirmationCandidate {
	c := confirmationPGCandidate()
	c.Attempt.Method = "card"
	c.Attempt.QRCID = ""
	c.Attempt.OperationID = "bank-card-operation"
	c.Profile.CardsEnabled = true
	c.Profile.AcquiringMerchantID = "200000000001056"
	c.Attempt.ProfileSnapshot, _ = json.Marshal(c.Profile)
	return c
}

func cardPGProof(c ConfirmationCandidate) CardObservation {
	return CardObservation{OperationID: c.Attempt.OperationID, Status: "APPROVED", Paid: true, CustomerCode: c.Profile.CustomerCode, MerchantID: c.Profile.AcquiringMerchantID, AmountKopecks: c.Attempt.AmountKopecks, PaymentType: "card", PaymentID: "payment-ref", TransactionID: "transaction-ref"}
}

func TestPostgresCardReserveCannotBypassQRHold(t *testing.T) {
	for _, state := range []string{"pending", "unknown", "paid", "expired"} {
		owner, now, order := qrPGFixture()
		order.values[6] = "payment_pending"
		previous := qrPGAttemptRow(now, state, "old-hash")
		rows := []qrPGRow{order, {err: ds.ErrNoRows}, previous}
		if state == "expired" {
			rows = []qrPGRow{order, {err: ds.ErrNoRows}, {err: ds.ErrNoRows}, previous}
		}
		tx := &qrPGTx{rows: rows}
		_, err := NewPostgresRepository(&qrPGProvider{tx: tx}).ReserveCard(context.Background(), owner, "order", "card-key", "newhash", now)
		qrPGRequireStatus(t, err, 409)
		if len(tx.execs) > 0 || tx.commits > 0 {
			t.Fatal("card checkout bypassed an existing QR hold")
		}
	}
}

func TestPostgresCardObserveAtomicPaidAndLeaseGuards(t *testing.T) {
	c := cardPGCandidate()
	db := &confirmationPGDB{rows: []qrPGRow{{values: []any{c.Attempt.OrderID}}, cardPGRecord(c, "expired", &c.ClaimedAt), {err: ds.ErrNoRows}}}
	if err := NewPostgresRepository(db).ObserveCard(context.Background(), c, cardPGProof(c)); err != nil {
		t.Fatal(err)
	}
	if db.commits != 1 || db.rollbacks != 0 || len(db.execs) != 3 || db.execs[1].sql != settleConfirmedOrderSQL || !strings.Contains(db.execs[2].sql, "state='paid'") {
		t.Fatal("card payment/order/jobs were not one transaction")
	}
	if db.execs[2].args[1] != "transaction-ref" || db.execs[2].args[2] != "payment-ref" {
		t.Fatal("native card references lost")
	}
	stale := c.ClaimedAt.Add(time.Second)
	db = &confirmationPGDB{rows: []qrPGRow{{values: []any{c.Attempt.OrderID}}, cardPGRecord(c, "pending", &stale)}}
	if err := NewPostgresRepository(db).ObserveCard(context.Background(), c, cardPGProof(c)); err == nil || len(db.execs) != 0 {
		t.Fatal("stale card worker wrote an outcome")
	}
}

func TestPostgresCardObserveRejectsMismatchedProofBeforeDatabase(t *testing.T) {
	c := cardPGCandidate()
	for _, change := range []func(*CardObservation){func(p *CardObservation) { p.AmountKopecks++ }, func(p *CardObservation) { p.CustomerCode = "other" }, func(p *CardObservation) { p.MerchantID = "other" }, func(p *CardObservation) { p.OperationID = "other" }, func(p *CardObservation) { p.Status = "AUTHORIZED" }, func(p *CardObservation) { p.PaymentType = "sbp" }} {
		proof := cardPGProof(c)
		change(&proof)
		if err := NewPostgresRepository(nil).ObserveCard(context.Background(), c, proof); err == nil {
			t.Fatal("mismatched card evidence accepted")
		}
	}
}

func TestPostgresCardPaidNeverRegressesAndLateCreatePreservesBankStatus(t *testing.T) {
	c := cardPGCandidate()
	db := &confirmationPGDB{rows: []qrPGRow{{values: []any{c.Attempt.OrderID}}, cardPGRecord(c, "paid", nil)}}
	proof := cardPGProof(c)
	proof.Paid = false
	proof.Status = "CREATED"
	if err := NewPostgresRepository(db).ObserveCard(context.Background(), c, proof); err != nil || len(db.execs) != 0 {
		t.Fatal("late bank state changed a paid checkout")
	}
	if !strings.Contains(completeCardSQL, "WHEN state='paid' THEN bank_status") {
		t.Fatal("late creation could downgrade the bank status")
	}
}

func TestCombinedWebhookAuthenticatesAndDispatchesAcquiringEvent(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	verifier, _ := paytochkabank.NewWebhookVerifier(&key.PublicKey)
	var count int
	handler := NewCombinedWebhookHandler(verifier, func(context.Context, int64, VerifiedSBPNotification) error {
		t.Fatal("acquiring event sent to SBP")
		return nil
	}, func(_ context.Context, id int64, n VerifiedCardNotification) error {
		count++
		if id != 0 || n.AmountKopecks != 12345 || n.OperationID != "bank-op" || n.PaymentLinkID != "attempt-id" || n.EventKey == "" {
			t.Fatal("card callback lost its correlation or amount")
		}
		return nil
	})
	for _, tamper := range []bool{false, true} {
		raw := signConfirmationWebhook(t, key, `{"webhookType":"acquiringInternetPayment","customerCode":"300000092","merchantId":"200000000001056","operationId":"bank-op","paymentLinkId":"attempt-id","paymentType":"card","status":"APPROVED","amount":123.45}`)
		if tamper {
			raw[len(raw)-10] ^= 1
		}
		r := httptest.NewRequest("POST", "/payments/tochka/webhook", strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "text/plain")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		want := 200
		if tamper {
			want = 401
		}
		if w.Code != want {
			t.Fatalf("status=%d want=%d", w.Code, want)
		}
	}
	if count != 1 {
		t.Fatal("unverified card callback reached the journal")
	}
}

func TestPostgresCardDemoCannotConfirmProduction(t *testing.T) {
	c := cardPGCandidate()
	c.Attempt.Environment = "production"
	c.Profile.Environment = "production"
	c.Attempt.ProfileSnapshot, _ = json.Marshal(c.Profile)
	db := &confirmationPGDB{rows: []qrPGRow{{values: []any{c.Attempt.OrderID}}, cardPGRecord(c, "pending", nil)}}
	if err := NewPostgresRepository(db).ConfirmDemo(context.Background(), c.Attempt.ID); err == nil || len(db.execs) > 0 {
		t.Fatal("production card payment accepted simulated confirmation")
	}
	if err := NewPostgresRepository(nil).CompleteCard(context.Background(), "id", CardResult{}); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatal("incomplete card response reached database")
	}
}
