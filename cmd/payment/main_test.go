package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type fakeOperations struct {
	status          paymentStatus
	confirmErr      error
	retryErr        error
	receiptRetryErr error
	confirms, reads int
	retries         int
	receiptRetries  int
}

func (f *fakeOperations) ConfirmDemo(context.Context, string) error {
	f.confirms++
	return f.confirmErr
}
func (f *fakeOperations) Status(context.Context, string) (paymentStatus, error) {
	f.reads++
	return f.status, nil
}

func (f *fakeOperations) RetrySettlement(context.Context, string) error {
	f.retries++
	return f.retryErr
}

func (f *fakeOperations) RetryReceipt(context.Context, string) error {
	f.receiptRetries++
	return f.receiptRetryErr
}

func TestDemoCommandReportsProductionRejectionWithoutReadingSuccess(t *testing.T) {
	ops := &fakeOperations{confirmErr: errors.New("only mock payments may be confirmed manually")}
	var out bytes.Buffer
	if err := execute(context.Background(), ops, "confirm-demo", "payment-id", &out); err == nil {
		t.Fatal("production rejection was ignored")
	}
	if ops.confirms != 1 || ops.reads != 0 || out.Len() != 0 {
		t.Fatal("failed confirmation reported success")
	}
}

func TestStatusCommandIsReadOnlyAndDoesNotExposeCredentials(t *testing.T) {
	ops := &fakeOperations{status: paymentStatus{PaymentID: "id", OrderID: "order", Environment: "mock", State: "paid", YClientsSyncStatus: "demo", FiscalStatus: "demo"}}
	var out bytes.Buffer
	if err := execute(context.Background(), ops, "status", "id", &out); err != nil {
		t.Fatal(err)
	}
	if ops.confirms != 0 || ops.retries != 0 || ops.receiptRetries != 0 || ops.reads != 1 || !strings.Contains(out.String(), `"state":"paid"`) {
		t.Fatalf("unexpected status output: %s", out.String())
	}
	if strings.Contains(out.String(), "jwt") || strings.Contains(out.String(), "token") {
		t.Fatal("status output exposed credentials")
	}
}

func TestReceiptRetryDoesNotTurnAnUncertainDeviceWriteIntoSuccess(t *testing.T) {
	ops := &fakeOperations{receiptRetryErr: errors.New("only a never-dispatched failed receipt can be retried safely")}
	var out bytes.Buffer
	if err := execute(context.Background(), ops, "retry-receipt", "payment-id", &out); err == nil {
		t.Fatal("uncertain receipt was retried")
	}
	if ops.receiptRetries != 1 || ops.confirms != 0 || ops.reads != 0 || out.Len() != 0 {
		t.Fatal("rejected receipt retry reported successful state")
	}
}

func TestSettlementRetryDoesNotTurnAnUncertainWriteIntoSuccess(t *testing.T) {
	ops := &fakeOperations{retryErr: errors.New("only a never-dispatched settlement can be retried safely")}
	var out bytes.Buffer
	if err := execute(context.Background(), ops, "retry-settlement", "payment-id", &out); err == nil {
		t.Fatal("uncertain write was retried")
	}
	if ops.retries != 1 || ops.confirms != 0 || ops.reads != 0 || out.Len() != 0 {
		t.Fatal("rejected retry reported successful state")
	}
}

func TestPaymentCommandRejectsInvalidArgumentsBeforeDatabaseConfiguration(t *testing.T) {
	for _, args := range [][]string{{}, {"mark-paid", "00000000-0000-4000-8000-000000000001"}, {"confirm-demo", "not-a-uuid"}, {"status"}} {
		if err := run(args, io.Discard, io.Discard); err == nil {
			t.Fatalf("invalid command accepted: %v", args)
		}
	}
}
