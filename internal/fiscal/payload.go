package fiscal

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/dronm/pronogti/internal/checkout"
	"golang.org/x/text/encoding/charmap"
)

// MaxReceiptKopecks is the UINT40 limit of the FFD 1.2 finalization command.
const MaxReceiptKopecks int64 = 1099511627775

// Leave room for the command envelope within the driver's default 64 KiB body limit.
const MaxReceiptPayloadBytes = 60 * 1024

// Profile is frozen on the checkout. It contains an environment variable name,
// never a bearer secret, so later receipt processing does not need a browser session.
type Profile struct {
	ID                *int64 `json:"id"`
	CompanyID         int64  `json:"company_id"`
	Name              string `json:"name"`
	Enabled           bool   `json:"enabled"`
	Mode              string `json:"mode"`
	DriverBaseURL     string `json:"driver_base_url"`
	DeviceID          string `json:"device_id"`
	BearerTokenEnv    string `json:"bearer_token_env"`
	TimeoutSeconds    int    `json:"timeout_seconds"`
	CashierName       string `json:"cashier_name"`
	CashierINN        string `json:"cashier_inn"`
	TaxSystem         int    `json:"tax_system"`
	ServiceVATRate    int    `json:"service_vat_rate"`
	GoodVATRate       int    `json:"good_vat_rate"`
	PaymentMethod     int    `json:"payment_method"`
	SettlementAddress string `json:"settlement_address"`
	SettlementPlace   string `json:"settlement_place"`
	InternetPayment   bool   `json:"internet_payment"`
	SuppressPrint     bool   `json:"suppress_print"`
}

func DefaultProfile(companyID int64) Profile {
	return Profile{
		CompanyID: companyID, Name: "Unconfigured demo", Enabled: true, Mode: "demo",
		TimeoutSeconds: 100, CashierName: "Cashier", TaxSystem: 1,
		ServiceVATRate: 6, GoodVATRate: 6, PaymentMethod: 4,
	}
}

var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidTaxSystem reports whether the code is allowed in an FFD 1.2 receipt.
func ValidTaxSystem(code int) bool {
	switch code {
	case 1, 2, 4, 16, 32:
		return true
	default:
		return false
	}
}

// ValidVATRate reports whether the code is a TerminalFA VAT rate.
func ValidVATRate(code int) bool {
	return code >= 1 && code <= 12
}

// ValidPaymentMethod reports whether the code is an FFD payment method.
func ValidPaymentMethod(code int) bool {
	return code >= 1 && code <= 7
}

// ValidTimeoutSeconds reports whether the driver HTTP timeout is supported.
func ValidTimeoutSeconds(seconds int) bool {
	return seconds >= 1 && seconds <= 300
}

// Validate checks the supported unmarked-receipt configuration. Tax codes are
// explicit operator choices; this package does not infer them from the date.
func (p Profile) Validate() error {
	if !p.Enabled {
		return errors.New("fiscal profile is disabled")
	}
	if p.Mode != "demo" && p.Mode != "live" {
		return errors.New("fiscal mode must be demo or live")
	}
	if !ValidTimeoutSeconds(p.TimeoutSeconds) {
		return errors.New("fiscal timeout must be between 1 and 300 seconds")
	}
	if p.Mode == "live" {
		u, err := url.Parse(p.DriverBaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" {
			return errors.New("fiscal driver URL must be an absolute HTTP or HTTPS URL without credentials, query or fragment")
		}
		if !deviceIDPattern.MatchString(p.DeviceID) {
			return errors.New("fiscal device ID is invalid")
		}
	}
	if p.BearerTokenEnv != "" && !envNamePattern.MatchString(p.BearerTokenEnv) {
		return errors.New("fiscal bearer token environment variable name is invalid")
	}
	if err := receiptText("cashier name", p.CashierName, 64, true); err != nil {
		return err
	}
	if p.CashierINN != "" {
		if len(p.CashierINN) != 12 || strings.Trim(p.CashierINN, "0123456789") != "" {
			return errors.New("fiscal cashier INN must have exactly twelve decimal digits")
		}
	}
	if !ValidTaxSystem(p.TaxSystem) {
		return errors.New("unsupported fiscal tax system")
	}
	if !ValidVATRate(p.ServiceVATRate) || !ValidVATRate(p.GoodVATRate) {
		return errors.New("fiscal VAT codes must be between 1 and 12")
	}
	if !ValidPaymentMethod(p.PaymentMethod) {
		return errors.New("fiscal payment method must be between 1 and 7")
	}
	if err := receiptText("settlement address", p.SettlementAddress, 256, false); err != nil {
		return err
	}
	if err := receiptText("settlement place", p.SettlementPlace, 256, p.InternetPayment); err != nil {
		return err
	}
	return nil
}

type receiptItem struct {
	Name          string `json:"name"`
	PriceKopecks  int64  `json:"priceKopecks"`
	Quantity      string `json:"quantity"`
	VATRate       int    `json:"vatRate"`
	PaymentMethod int    `json:"paymentMethod"`
	PaymentObject int    `json:"paymentObject"`
}

type receiptAdditional struct {
	CashierName       string `json:"cashierName"`
	CashierINN        string `json:"cashierINN,omitempty"`
	CustomerContact   string `json:"customerContact,omitempty"`
	SettlementAddress string `json:"settlementAddress,omitempty"`
	SettlementPlace   string `json:"settlementPlace,omitempty"`
	InternetPayment   int    `json:"internetPayment"`
}

type receiptPayment struct {
	TaxSystem               int   `json:"taxSystem"`
	CashKopecks             int64 `json:"cashKopecks"`
	CashlessKopecks         int64 `json:"cashlessKopecks"`
	PrepaymentKopecks       int64 `json:"prepaymentKopecks"`
	CreditKopecks           int64 `json:"creditKopecks"`
	CounterProvisionKopecks int64 `json:"counterProvisionKopecks"`
}

type receiptPayload struct {
	CashierName          string            `json:"cashierName"`
	CashierINN           string            `json:"cashierInn,omitempty"`
	SuppressShiftPrint   *int              `json:"suppressShiftPrint,omitempty"`
	PrintSuppressionCode *int              `json:"printSuppressionCode,omitempty"`
	Items                []receiptItem     `json:"items"`
	AdditionalData       receiptAdditional `json:"additionalData"`
	Payment              receiptPayment    `json:"payment"`
	CalculationSign      int               `json:"calculationSign"`
	TotalKopecks         int64             `json:"totalKopecks"`
}

// BuildPayload maps the immutable checkout to createReceipt parameters. It
// rejects lines that cannot be represented by the driver's price and quantity
// fields, rather than changing their accounting meaning after payment.
func BuildPayload(order checkout.Order, profile Profile) (json.RawMessage, error) {
	return BuildPayloadWithContact(order, profile, order.CustomerContact)
}

// BuildPayloadWithContact includes the buyer contact frozen by the backend at
// checkout. It is never read from the browser during receipt dispatch or from
// a later customer's session on the same kiosk.
func BuildPayloadWithContact(order checkout.Order, profile Profile, customerContact string) (json.RawMessage, error) {
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	if err := receiptText("customer contact", customerContact, 64, false); err != nil {
		return nil, err
	}
	if profile.CompanyID != order.Owner.CompanyID {
		return nil, errors.New("fiscal profile belongs to another company")
	}
	if order.Currency != "RUB" || order.BonusKopecks != 0 || order.PayableKopecks != order.SubtotalKopecks {
		return nil, errors.New("receipt requires an undiscounted RUB checkout")
	}
	if order.PayableKopecks < 1 || order.PayableKopecks > MaxReceiptKopecks {
		return nil, errors.New("checkout total exceeds fiscal receipt limits")
	}
	if len(order.Items) < 1 || len(order.Items) > 200 {
		return nil, errors.New("fiscal receipt requires between 1 and 200 items")
	}
	p := receiptPayload{
		CashierName: profile.CashierName, CashierINN: profile.CashierINN,
		AdditionalData:  receiptAdditional{CashierName: profile.CashierName, CashierINN: profile.CashierINN, CustomerContact: customerContact, SettlementAddress: profile.SettlementAddress, SettlementPlace: profile.SettlementPlace},
		Payment:         receiptPayment{TaxSystem: profile.TaxSystem, CashlessKopecks: order.PayableKopecks},
		CalculationSign: 1, TotalKopecks: order.PayableKopecks,
		Items: make([]receiptItem, 0, len(order.Items)),
	}
	if profile.InternetPayment {
		p.AdditionalData.InternetPayment = 1
	}
	if profile.SuppressPrint {
		one, suppress := 1, 41029
		p.SuppressShiftPrint, p.PrintSuppressionCode = &one, &suppress
	}
	var sum int64
	for i, item := range order.Items {
		if err := receiptText("item name", item.Title, 128, true); err != nil {
			return nil, fmt.Errorf("receipt item %d: %w", i+1, err)
		}
		if item.UnitPriceKopecks <= 0 || item.UnitPriceKopecks > MaxReceiptKopecks || item.AmountKopecks <= 0 || item.AmountKopecks > MaxReceiptKopecks {
			return nil, fmt.Errorf("receipt item %d exceeds fiscal price limits", i+1)
		}
		quantity, milli, err := receiptQuantity(item.Quantity)
		if err != nil {
			return nil, fmt.Errorf("receipt item %d: %w", i+1, err)
		}
		amount := new(big.Int).Mul(big.NewInt(item.UnitPriceKopecks), big.NewInt(milli))
		amount.Add(amount, big.NewInt(500))
		amount.Quo(amount, big.NewInt(1000))
		if !amount.IsInt64() || amount.Int64() != item.AmountKopecks {
			return nil, fmt.Errorf("receipt item %d cannot represent its outstanding amount using the recorded price and quantity", i+1)
		}
		if item.AmountKopecks > MaxReceiptKopecks-sum {
			return nil, errors.New("receipt items exceed fiscal total limits")
		}
		sum += item.AmountKopecks
		object, vat := 0, profile.GoodVATRate
		switch item.Kind {
		case checkout.KindGood:
			object = 1
		case checkout.KindService:
			object, vat = 4, profile.ServiceVATRate
		default:
			return nil, errors.New("unsupported fiscal item kind")
		}
		p.Items = append(p.Items, receiptItem{Name: item.Title, PriceKopecks: item.UnitPriceKopecks, Quantity: quantity, VATRate: vat, PaymentMethod: profile.PaymentMethod, PaymentObject: object})
	}
	if sum != order.PayableKopecks {
		return nil, errors.New("receipt item totals do not match the checkout")
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxReceiptPayloadBytes {
		return nil, errors.New("receipt payload exceeds the supported driver request size")
	}
	return encoded, nil
}

func receiptText(field, value string, maximum int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("fiscal %s is required", field)
	}
	encoded, err := charmap.CodePage866.NewEncoder().String(value)
	if err != nil {
		return fmt.Errorf("fiscal %s contains characters unsupported by CP866", field)
	}
	if len(encoded) > maximum {
		return fmt.Errorf("fiscal %s exceeds %d CP866 bytes", field, maximum)
	}
	return nil
}

func receiptQuantity(value string) (string, int64, error) {
	if len(value) == 0 || len(value) > 64 {
		return "", 0, errors.New("invalid fiscal item quantity")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 {
		return "", 0, errors.New("invalid fiscal item quantity")
	}
	for _, part := range parts {
		if part == "" || strings.Trim(part, "0123456789") != "" {
			return "", 0, errors.New("invalid fiscal item quantity")
		}
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = strings.TrimRight(parts[1], "0")
	}
	if len(fraction) > 3 {
		return "", 0, errors.New("fiscal item quantity supports at most three decimal places")
	}
	milli, err := strconv.ParseInt(parts[0]+fraction+strings.Repeat("0", 3-len(fraction)), 10, 64)
	if err != nil || milli <= 0 || milli > 1000000000 {
		return "", 0, errors.New("fiscal item quantity must be positive and no greater than 1000000")
	}
	canonical := strconv.FormatInt(milli/1000, 10)
	if milli%1000 != 0 {
		canonical += "." + strings.TrimRight(fmt.Sprintf("%03d", milli%1000), "0")
	}
	return canonical, milli, nil
}
