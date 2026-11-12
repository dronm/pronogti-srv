package refund

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/fiscal"
)

// PrepareReturnReceipt builds the return from the saved sale receipt before
// any bank call. A live refund requires a confirmed original fiscal document.
func PrepareReturnReceipt(profile fiscal.Profile, environment, saleState string, salePayload json.RawMessage, lines []Line, amountKopecks, originalPaidKopecks int64, allowLive bool) (json.RawMessage, error) {
	switch environment {
	case "production":
		if saleState != "issued" || profile.Mode != "live" || !allowLive {
			return nil, errors.New("refund requires an issued sale receipt and enabled live fiscalization")
		}
		if _, err := returnDriverConfig(profile); err != nil {
			return nil, err
		}
	case "mock", "sandbox":
		if saleState != "demo" {
			return nil, errors.New("test refund requires a completed demo sale receipt")
		}
	default:
		return nil, errors.New("unknown fiscal refund environment")
	}
	return buildReturnReceipt(salePayload, lines, amountKopecks, originalPaidKopecks)
}

type saleReceiptAmounts struct {
	CalculationSign int   `json:"calculationSign"`
	TotalKopecks    int64 `json:"totalKopecks"`
	Payment         struct {
		CashKopecks             int64 `json:"cashKopecks"`
		CashlessKopecks         int64 `json:"cashlessKopecks"`
		PrepaymentKopecks       int64 `json:"prepaymentKopecks"`
		CreditKopecks           int64 `json:"creditKopecks"`
		CounterProvisionKopecks int64 `json:"counterProvisionKopecks"`
	} `json:"payment"`
	Items []json.RawMessage `json:"items"`
}

type saleReceiptLine struct {
	Name          string `json:"name"`
	PriceKopecks  int64  `json:"priceKopecks"`
	Quantity      string `json:"quantity"`
	PaymentObject int    `json:"paymentObject"`
}

func buildReturnReceipt(salePayload json.RawMessage, lines []Line, amountKopecks, originalPaidKopecks int64) (json.RawMessage, error) {
	if amountKopecks <= 0 || originalPaidKopecks < amountKopecks || originalPaidKopecks > fiscal.MaxReceiptKopecks || len(salePayload) > fiscal.MaxReceiptPayloadBytes {
		return nil, errors.New("invalid fiscal return amount or original sale receipt size")
	}
	var sale saleReceiptAmounts
	if err := json.Unmarshal(salePayload, &sale); err != nil || sale.CalculationSign != 1 || sale.TotalKopecks != originalPaidKopecks ||
		sale.Payment.CashlessKopecks != originalPaidKopecks || sale.Payment.CashKopecks != 0 || sale.Payment.PrepaymentKopecks != 0 ||
		sale.Payment.CreditKopecks != 0 || sale.Payment.CounterProvisionKopecks != 0 {
		return nil, errors.New("original fiscal receipt does not match the cashless sale")
	}
	if len(lines) == 0 || len(lines) > len(sale.Items) || len(sale.Items) > 200 {
		return nil, errors.New("invalid fiscal return item count")
	}

	items := make([]map[string]json.RawMessage, 0, len(lines))
	used := make(map[int]bool, len(lines))
	var total int64
	for _, line := range lines {
		if line.LineIndex < 0 || line.LineIndex >= len(sale.Items) || used[line.LineIndex] {
			return nil, errors.New("invalid fiscal return item reference")
		}
		used[line.LineIndex] = true
		item, err := buildReturnReceiptLine(sale.Items[line.LineIndex], line)
		if err != nil {
			return nil, fmt.Errorf("fiscal return line %d: %w", line.LineIndex, err)
		}
		if line.AmountKopecks > amountKopecks-total {
			return nil, errors.New("fiscal return item totals exceed refund amount")
		}
		total += line.AmountKopecks
		items = append(items, item)
	}
	if total != amountKopecks {
		return nil, errors.New("fiscal return item totals differ from refund amount")
	}

	// Keep the original seller, tax, cashier, contact and print parameters.
	var receipt, payment map[string]json.RawMessage
	if err := json.Unmarshal(salePayload, &receipt); err != nil {
		return nil, errors.New("invalid original fiscal receipt")
	}
	if err := json.Unmarshal(receipt["payment"], &payment); err != nil || payment == nil {
		return nil, errors.New("invalid original fiscal payment")
	}
	amount := json.RawMessage(strconv.FormatInt(amountKopecks, 10))
	payment["cashlessKopecks"] = amount
	receipt["calculationSign"] = json.RawMessage("2")
	receipt["totalKopecks"] = amount
	var err error
	if receipt["items"], err = json.Marshal(items); err != nil {
		return nil, err
	}
	if receipt["payment"], err = json.Marshal(payment); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	if len(payload) > fiscal.MaxReceiptPayloadBytes {
		return nil, errors.New("fiscal return payload exceeds driver request size")
	}
	return payload, nil
}

func buildReturnReceiptLine(original json.RawMessage, line Line) (map[string]json.RawMessage, error) {
	var sold saleReceiptLine
	if err := json.Unmarshal(original, &sold); err != nil || sold.Name != line.Title ||
		sold.PriceKopecks != line.UnitPriceKopecks || sold.PriceKopecks <= 0 || sold.PriceKopecks > fiscal.MaxReceiptKopecks ||
		!(line.Kind == checkout.KindGood && sold.PaymentObject == 1 || line.Kind == checkout.KindService && sold.PaymentObject == 4) {
		return nil, errors.New("selected item differs from original fiscal receipt")
	}
	_, soldMilli, err := checkout.ParseQuantity(sold.Quantity)
	if err != nil {
		return nil, errors.New("original fiscal item quantity is invalid")
	}
	quantity, milli, err := checkout.ParseQuantity(line.Quantity)
	if err != nil || milli != line.QuantityMilli || milli > soldMilli {
		return nil, errors.New("return quantity differs from the selected sale line")
	}
	if line.AmountKopecks <= 0 || roundedLineAmount(sold.PriceKopecks, milli) != line.AmountKopecks {
		return nil, errors.New("return amount cannot be represented at the original fiscal unit price")
	}
	var item map[string]json.RawMessage
	if err := json.Unmarshal(original, &item); err != nil || item == nil {
		return nil, errors.New("invalid original fiscal item")
	}
	item["quantity"] = json.RawMessage(strconv.Quote(quantity))
	return item, nil
}

func returnDriverConfig(profile fiscal.Profile) (fiscal.DriverConfig, error) {
	if err := profile.Validate(); err != nil {
		return fiscal.DriverConfig{}, err
	}
	var token string
	if profile.BearerTokenEnv != "" {
		token = os.Getenv(profile.BearerTokenEnv)
		if token == "" || strings.IndexFunc(token, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
			return fiscal.DriverConfig{}, errors.New("refund receipt driver token is unavailable or invalid")
		}
	}
	return fiscal.DriverConfig{
		BaseURL: profile.DriverBaseURL, DeviceID: profile.DeviceID, Token: token,
		Timeout: time.Duration(profile.TimeoutSeconds) * time.Second,
	}, nil
}

// ProcessReturnReceipt runs after the bank refund is confirmed. The caller
// journals a dispatch marker before Create. A recovering attempt only reads
// the TerminalFA journal for its original request ID.
func ProcessReturnReceipt(ctx context.Context, driver fiscal.Driver, profile fiscal.Profile, environment, requestID string, payload json.RawMessage, allowLive, recovering bool) fiscal.DriverResult {
	if environment == "mock" || environment == "sandbox" {
		return fiscal.DriverResult{State: "demo", Response: json.RawMessage(`{"demo":true,"fiscal_document_issued":false}`)}
	}
	deferredState := "queued"
	if recovering {
		deferredState = "unknown"
	}
	if environment != "production" || profile.Mode != "live" || !allowLive {
		return fiscal.DriverResult{State: deferredState, ErrorCode: "live_refund_fiscalization_disabled"}
	}
	config, err := returnDriverConfig(profile)
	if err != nil || driver == nil {
		return fiscal.DriverResult{State: deferredState, ErrorCode: "refund_receipt_configuration_unavailable"}
	}
	var result fiscal.DriverResult
	if recovering {
		result, err = driver.Lookup(ctx, config, requestID)
	} else {
		result, err = driver.Create(ctx, config, requestID, payload)
	}
	if result.State == "failed" && (err == nil || !recovering) {
		if !recovering && result.ErrorCode == "request_not_started" && len(result.Response) == 0 {
			result.State = "queued"
		}
		return result
	}
	if err == nil && result.State == "completed" {
		result.State = "issued"
		return result
	}
	result.State = "unknown"
	if result.ErrorCode == "" {
		result.ErrorCode = "refund_receipt_outcome_unknown"
	}
	return result
}
