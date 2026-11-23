package correction

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dronm/pronogti/internal/fiscal"
	"golang.org/x/text/encoding/charmap"
)

// DecodeReceipt refuses fields that this editor cannot preserve. In particular,
// a later extension must not silently remove additional fiscal attributes when
// reversing a previously issued receipt.
func DecodeReceipt(raw json.RawMessage) (Receipt, error) {
	if len(raw) == 0 || len(raw) > fiscal.MaxReceiptPayloadBytes {
		return Receipt{}, errors.New("Сохранённые данные чека отсутствуют или превышают допустимый размер.")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result *Receipt
	if err := decoder.Decode(&result); err != nil || result == nil {
		return Receipt{}, errors.New("Сохранённый чек содержит неподдерживаемые или некорректные реквизиты.")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Receipt{}, errors.New("Некорректный формат сохранённого чека.")
	}
	return *result, nil
}

// Build prepares both documents before either can be dispatched. The reversal
// retains the original fiscal contents; the replacement uses the edited data.
func Build(source Source, request CreateRequest, now time.Time, location *time.Location) (Preview, error) {
	if request.SourceVersion == "" || request.SourceVersion != source.Version {
		return Preview{}, errors.New("Исходный чек изменился. Обновите данные и повторите подготовку коррекции.")
	}
	if strings.TrimSpace(request.Reason) == "" || !utf8.ValidString(request.Reason) || utf8.RuneCountInString(request.Reason) > 2000 {
		return Preview{}, errors.New("Укажите причину коррекции длиной до 2000 символов.")
	}
	if request.Basis != "voluntary" && request.Basis != "prescription" {
		return Preview{}, errors.New("Выберите самостоятельную коррекцию или коррекцию по предписанию ФНС.")
	}
	prescription := strings.TrimSpace(request.PrescriptionNumber)
	if request.Basis == "voluntary" && prescription != "" {
		return Preview{}, errors.New("Номер предписания ФНС заполняется только для коррекции по предписанию.")
	}
	if request.Basis == "prescription" {
		if err := validateText("Номер предписания ФНС", prescription, 32, true); err != nil {
			return Preview{}, err
		}
	}
	if location == nil {
		location = time.Local
	}
	date, err := time.ParseInLocation("2006-01-02", request.CalculationDate, location)
	if err != nil || date.Format("2006-01-02") != request.CalculationDate || date.Unix() <= 0 || date.Unix() > math.MaxUint32 {
		return Preview{}, errors.New("Укажите корректную дату исходного расчёта в формате ГГГГ-ММ-ДД.")
	}
	if request.CalculationDate > now.In(location).Format("2006-01-02") {
		return Preview{}, errors.New("Дата исходного расчёта не может быть позже текущей даты ККТ.")
	}
	if source.Payload.CalculationSign != 1 || request.Receipt.CalculationSign != 1 {
		return Preview{}, errors.New("В этом разделе поддерживается исправление чеков с признаком «приход».")
	}
	if source.State != "demo" && (source.Identity.FiscalSign == 0 || source.Identity.DocumentNumber == 0) {
		return Preview{}, errors.New("У исходного чека отсутствуют подтверждённые фискальные реквизиты.")
	}

	reversal, replacement := cloneReceipt(source.Payload), cloneReceipt(request.Receipt)
	if err := validateReceipt(&reversal, false); err != nil {
		return Preview{}, fmt.Errorf("Невозможно воспроизвести исходный чек: %w", err)
	}
	if err := validateReceipt(&replacement, true); err != nil {
		return Preview{}, err
	}
	reference := ""
	if source.Identity.FiscalSign != 0 {
		reference = strconv.FormatUint(uint64(source.Identity.FiscalSign), 10)
	}
	if err := validateGeneratedFields(replacement.AdditionalData, date.Unix(), prescription, reference); err != nil {
		return Preview{}, err
	}
	if sameFiscalContents(reversal, replacement) && request.CalculationDate == source.CalculationDate {
		return Preview{}, errors.New("Исправленные реквизиты совпадают с исходным чеком. Укажите необходимые изменения.")
	}

	reversal.CalculationSign = 2
	for _, receipt := range []*Receipt{&reversal, &replacement} {
		receipt.AdditionalData.CorrectedCalculationDate = date.Unix()
		receipt.AdditionalData.PrescriptionNumber = prescription
		receipt.AdditionalData.AdditionalReceiptProperty = reference
		encoded, err := json.Marshal(receipt)
		if err != nil || len(encoded) > fiscal.MaxReceiptPayloadBytes {
			return Preview{}, errors.New("Чек коррекции превышает допустимый размер запроса к ККТ.")
		}
	}
	return Preview{SourceVersion: source.Version, Original: source.Identity, Reversal: reversal, Replacement: replacement}, nil
}

func validateGeneratedFields(additional AdditionalData, date int64, prescription, reference string) error {
	if additional.CorrectedCalculationDate != 0 && additional.CorrectedCalculationDate != date ||
		additional.PrescriptionNumber != "" && additional.PrescriptionNumber != prescription ||
		additional.AdditionalReceiptProperty != "" && additional.AdditionalReceiptProperty != reference {
		return errors.New("Дата, номер предписания и ссылка на исходный чек формируются сервером из параметров коррекции.")
	}
	return nil
}

func validateReceipt(receipt *Receipt, replacement bool) error {
	if receipt.TotalKopecks < 1 || receipt.TotalKopecks > fiscal.MaxReceiptKopecks {
		return errors.New("Итог чека должен быть положительной суммой в пределах лимита ККТ.")
	}
	if len(receipt.Items) < 1 || len(receipt.Items) > 200 {
		return errors.New("Чек должен содержать от 1 до 200 позиций.")
	}
	if err := validateText("Кассир", receipt.CashierName, 64, true); err != nil {
		return err
	}
	if err := validateINN(receipt.CashierINN); err != nil {
		return err
	}
	if receipt.SuppressShiftPrint != nil && *receipt.SuppressShiftPrint != 0 && *receipt.SuppressShiftPrint != 1 {
		return errors.New("Параметр печати отчёта открытия смены должен быть 0 или 1.")
	}
	if receipt.PrintSuppressionCode != nil && *receipt.PrintSuppressionCode != 41029 {
		return errors.New("Некорректный параметр отключения печати чека.")
	}
	additional := receipt.AdditionalData
	for _, field := range []struct {
		name, value string
		maximum     int
	}{
		{"Кассир в чеке", additional.CashierName, 64},
		{"Контакт покупателя", additional.CustomerContact, 64},
		{"Адрес расчётов", additional.SettlementAddress, 256},
		{"Место расчётов", additional.SettlementPlace, 256},
	} {
		if err := validateText(field.name, field.value, field.maximum, false); err != nil {
			return err
		}
	}
	if err := validateINN(additional.CashierINN); err != nil {
		return err
	}
	if additional.InternetPayment != 0 && additional.InternetPayment != 1 {
		return errors.New("Признак расчёта в интернете должен быть 0 или 1.")
	}
	if additional.InternetPayment == 1 && (strings.TrimSpace(additional.SettlementPlace) == "" || replacement && strings.TrimSpace(additional.CustomerContact) == "") {
		return errors.New("Для расчёта в интернете укажите адрес сайта и телефон или email покупателя.")
	}
	if !fiscal.ValidTaxSystem(receipt.Payment.TaxSystem) {
		return errors.New("Выберите допустимую систему налогообложения.")
	}
	var paymentTotal int64
	for _, amount := range []int64{
		receipt.Payment.CashKopecks, receipt.Payment.CashlessKopecks, receipt.Payment.PrepaymentKopecks,
		receipt.Payment.CreditKopecks, receipt.Payment.CounterProvisionKopecks,
	} {
		if amount < 0 || amount > receipt.TotalKopecks-paymentTotal {
			return errors.New("Суммы по способам оплаты должны быть неотрицательными и совпадать с итогом чека.")
		}
		paymentTotal += amount
	}
	if paymentTotal != receipt.TotalKopecks {
		return errors.New("Суммы по способам оплаты не совпадают с итогом чека.")
	}
	var itemTotal int64
	for index := range receipt.Items {
		item := &receipt.Items[index]
		if err := validateText("Наименование позиции", item.Name, 128, true); err != nil {
			return fmt.Errorf("Позиция %d: %w", index+1, err)
		}
		if item.PriceKopecks <= 0 || item.PriceKopecks > fiscal.MaxReceiptKopecks {
			return fmt.Errorf("Позиция %d: укажите положительную цену в пределах лимита ККТ.", index+1)
		}
		quantity, milli, err := normalizeQuantity(item.Quantity)
		if err != nil {
			return fmt.Errorf("Позиция %d: %w", index+1, err)
		}
		item.Quantity = quantity
		if !fiscal.ValidVATRate(item.VATRate) {
			return fmt.Errorf("Позиция %d: выберите допустимую ставку НДС.", index+1)
		}
		if !fiscal.ValidPaymentMethod(item.PaymentMethod) {
			return fmt.Errorf("Позиция %d: выберите допустимый признак способа расчёта.", index+1)
		}
		if item.PaymentObject != 1 && item.PaymentObject != 4 {
			return fmt.Errorf("Позиция %d: поддерживаются только немаркированные товары и услуги.", index+1)
		}
		if item.Measure != nil && !validMeasure(*item.Measure) {
			return fmt.Errorf("Позиция %d: выберите допустимую меру количества.", index+1)
		}
		amount := new(big.Int).Mul(big.NewInt(item.PriceKopecks), big.NewInt(milli))
		amount.Add(amount, big.NewInt(500))
		amount.Quo(amount, big.NewInt(1000))
		if !amount.IsInt64() || amount.Sign() <= 0 || amount.Int64() > receipt.TotalKopecks-itemTotal {
			return errors.New("Суммы позиций не совпадают с итогом чека или превышают лимит ККТ.")
		}
		itemTotal += amount.Int64()
	}
	if itemTotal != receipt.TotalKopecks {
		return errors.New("Суммы позиций не совпадают с итогом чека.")
	}
	return nil
}

func validateText(name, value string, maximum int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s: значение обязательно.", name)
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s: управляющие символы недопустимы.", name)
	}
	encoded, err := charmap.CodePage866.NewEncoder().String(value)
	if err != nil {
		return fmt.Errorf("%s: значение содержит символы, которые ККТ не поддерживает.", name)
	}
	if len(encoded) > maximum {
		return fmt.Errorf("%s: допускается не более %d символов в кодировке ККТ.", name, maximum)
	}
	return nil
}

func validateINN(value string) error {
	if value != "" && (len(value) != 12 || strings.Trim(value, "0123456789") != "") {
		return errors.New("ИНН кассира должен содержать 12 цифр.")
	}
	return nil
}

func normalizeQuantity(value string) (string, int64, error) {
	if len(value) == 0 || len(value) > 64 {
		return "", 0, errors.New("Укажите положительное количество с точностью до трёх знаков после запятой.")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 {
		return "", 0, errors.New("Некорректный формат количества.")
	}
	for _, part := range parts {
		if part == "" || strings.Trim(part, "0123456789") != "" {
			return "", 0, errors.New("Некорректный формат количества.")
		}
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = strings.TrimRight(parts[1], "0")
	}
	if len(fraction) > 3 {
		return "", 0, errors.New("Количество поддерживает не более трёх знаков после запятой.")
	}
	milli, err := strconv.ParseInt(parts[0]+fraction+strings.Repeat("0", 3-len(fraction)), 10, 64)
	if err != nil || milli <= 0 || milli > 1_000_000_000 {
		return "", 0, errors.New("Количество должно быть больше нуля и не больше 1000000.")
	}
	canonical := strconv.FormatInt(milli/1000, 10)
	if milli%1000 != 0 {
		canonical += "." + strings.TrimRight(fmt.Sprintf("%03d", milli%1000), "0")
	}
	return canonical, milli, nil
}

func validMeasure(value int) bool {
	switch value {
	case 0, 10, 11, 12, 20, 21, 22, 30, 31, 32, 40, 41, 42, 50, 51, 70, 71, 72, 73, 80, 81, 82, 83, 255:
		return true
	default:
		return false
	}
}

func cloneReceipt(value Receipt) Receipt {
	copyInt := func(value *int) *int {
		if value == nil {
			return nil
		}
		result := *value
		return &result
	}
	value.SuppressShiftPrint = copyInt(value.SuppressShiftPrint)
	value.PrintSuppressionCode = copyInt(value.PrintSuppressionCode)
	value.Items = append([]Item(nil), value.Items...)
	for index := range value.Items {
		value.Items[index].Measure = copyInt(value.Items[index].Measure)
	}
	return value
}

func sameFiscalContents(left, right Receipt) bool {
	for _, receipt := range []*Receipt{&left, &right} {
		receipt.AdditionalData.CorrectedCalculationDate = 0
		receipt.AdditionalData.PrescriptionNumber = ""
		receipt.AdditionalData.AdditionalReceiptProperty = ""
		// Printing and shift-opening flags do not correct a fiscal transaction.
		receipt.SuppressShiftPrint, receipt.PrintSuppressionCode = nil, nil
	}
	first, _ := json.Marshal(left)
	second, _ := json.Marshal(right)
	return bytes.Equal(first, second)
}
