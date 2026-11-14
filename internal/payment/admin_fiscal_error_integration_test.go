package payment

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dronm/pronogti/internal/checkout"
)

func TestAdminFiscalErrorProjectionRetainsHistoricalDriverMessages(t *testing.T) {
	provider, conn := completionTestDatabase(t)
	completionProfile(t, conn, 781898, "mock")
	ctx := context.Background()
	actor := checkout.Actor{CompanyID: 781898, Mode: "public", ClientID: 42, ScopeID: "error-scope", CustomerSessionID: "error-customer"}
	order := completionOrder(t, provider, actor, "admin-fiscal-error")
	repository := NewPostgresRepository(provider)
	service := NewService(Dependencies{Repository: repository, Bank: SDKBank{}}, Options{})
	view, err := service.Init(ctx, actor, "qr:"+order.ID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfirmDemo(ctx, view.PaymentID); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name, response, message string
	}{
		{"device failure", `{"status":"failed","error":{"code":"device_error","message":"Device rejected command 0x24"},"data":{"failure":{"code":"device_error","message":"Device rejected command 0x24"}}}`, "Device rejected command 0x24"},
		{"unknown with underlying failure", `{"status":"unknown","error":{"code":"outcome_unknown","message":"Review required"},"data":{"failure":{"code":"device_error","message":"Device rejected command 0x24"},"cleanup":{"error":{"message":"Cancel failed"}}}}`, "Review required\nDevice rejected command 0x24\nCancel failed"},
		{"nested failure only", `{"data":{"failure":{"message":"Full nested message"}}}`, "Full nested message"},
		{"no message", `{"error":{"code":"device_error"}}`, ""},
	} {
		t.Run(item.name, func(t *testing.T) {
			if _, err := conn.Exec(ctx, `UPDATE public.fiscal_receipts SET state='failed',error_code='device_error',response_payload=$2::jsonb WHERE order_id=$1`, order.ID, item.response); err != nil {
				t.Fatal(err)
			}
			var code string
			var message *string
			var response map[string]any
			if err := conn.QueryRow(ctx, `SELECT fiscal_error_code,fiscal_error_message,fiscal_response_payload FROM public.admin_payment_orders_detail WHERE id=$1`, order.ID).Scan(&code, &message, &response); err != nil {
				t.Fatal(err)
			}
			if code != "device_error" || item.message != "" && (message == nil || *message != item.message) || item.message == "" && message != nil {
				t.Fatalf("code=%q message=%v, want %q", code, message, item.message)
			}
			var original map[string]any
			if err := json.Unmarshal([]byte(item.response), &original); err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(response)
			want, _ := json.Marshal(original)
			if string(got) != string(want) {
				t.Fatalf("driver details changed: %s, want %s", got, want)
			}
		})
	}
}
