package correction

// Receipt uses the goterminalfa createCorrectionReceipt parameter names. The
// backend, rather than the browser, fills its correction-specific attributes.
type Receipt struct {
	CashierName          string         `json:"cashierName"`
	CashierINN           string         `json:"cashierInn,omitempty"`
	SuppressShiftPrint   *int           `json:"suppressShiftPrint,omitempty"`
	PrintSuppressionCode *int           `json:"printSuppressionCode,omitempty"`
	Items                []Item         `json:"items"`
	AdditionalData       AdditionalData `json:"additionalData"`
	Payment              Payment        `json:"payment"`
	CalculationSign      int            `json:"calculationSign"`
	TotalKopecks         int64          `json:"totalKopecks"`
}

type Item struct {
	Name          string `json:"name"`
	PriceKopecks  int64  `json:"priceKopecks"`
	Quantity      string `json:"quantity"`
	VATRate       int    `json:"vatRate"`
	PaymentMethod int    `json:"paymentMethod"`
	PaymentObject int    `json:"paymentObject"`
	Measure       *int   `json:"measure,omitempty"`
}

type AdditionalData struct {
	CashierName               string `json:"cashierName,omitempty"`
	CashierINN                string `json:"cashierINN,omitempty"`
	CustomerContact           string `json:"customerContact,omitempty"`
	SettlementAddress         string `json:"settlementAddress,omitempty"`
	SettlementPlace           string `json:"settlementPlace,omitempty"`
	InternetPayment           int    `json:"internetPayment"`
	CorrectedCalculationDate  int64  `json:"correctedCalculationDate,omitempty"`
	PrescriptionNumber        string `json:"prescriptionNumber,omitempty"`
	AdditionalReceiptProperty string `json:"additionalReceiptProperty,omitempty"`
}

type Payment struct {
	TaxSystem               int   `json:"taxSystem"`
	CashKopecks             int64 `json:"cashKopecks"`
	CashlessKopecks         int64 `json:"cashlessKopecks"`
	PrepaymentKopecks       int64 `json:"prepaymentKopecks"`
	CreditKopecks           int64 `json:"creditKopecks"`
	CounterProvisionKopecks int64 `json:"counterProvisionKopecks"`
}

type CreateRequest struct {
	SourceVersion      string  `json:"source_version"`
	Reason             string  `json:"reason"`
	Basis              string  `json:"basis"`
	PrescriptionNumber string  `json:"prescription_number"`
	CalculationDate    string  `json:"calculation_date"`
	Receipt            Receipt `json:"receipt"`
}

type FiscalIdentity struct {
	DocumentNumber uint32 `json:"document_number"`
	FiscalSign     uint32 `json:"fiscal_sign"`
	DateTime       string `json:"date_time"`
	ReceiptNumber  uint16 `json:"receipt_number"`
	ShiftNumber    uint16 `json:"shift_number"`
}

type Preview struct {
	SourceVersion string         `json:"source_version"`
	Original      FiscalIdentity `json:"original"`
	Reversal      Receipt        `json:"reversal"`
	Replacement   Receipt        `json:"replacement"`
}
