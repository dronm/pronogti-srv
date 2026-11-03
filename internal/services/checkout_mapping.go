package services

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/webapp"
)

// JSON DTOs preserve the custom goyclients catalog's canonical good_id and
// permit both historical array and object representations of service staff.
type checkoutCatalogRow struct {
	ID       int64           `json:"id"`
	GoodID   int64           `json:"good_id"`
	Title    string          `json:"title"`
	Cost     json.Number     `json:"cost"`
	PriceMin json.Number     `json:"price_min"`
	PriceMax json.Number     `json:"price_max"`
	Staff    json.RawMessage `json:"staff"`
}

func checkoutRows(data any) ([]checkoutCatalogRow, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode authoritative catalog: %w", err)
	}
	var rows []checkoutCatalogRow
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode authoritative catalog: %w", err)
	}
	return rows, nil
}

func checkoutStaff(data []byte) (map[int64]string, error) {
	var rows []*struct {
		ID     int64           `json:"id"`
		Name   string          `json:"name"`
		Fired  json.RawMessage `json:"fired"`
		Status json.RawMessage `json:"status"`
		Hidden json.RawMessage `json:"hidden"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("decode authoritative staff: %w", err)
	}
	result := make(map[int64]string)
	for _, row := range rows {
		if row == nil || row.ID <= 0 || checkoutFlag(row.Fired) || checkoutFlag(row.Status) || checkoutFlag(row.Hidden) {
			continue
		}
		result[row.ID] = row.Name
	}
	return result, nil
}

func checkoutFlag(value json.RawMessage) bool {
	return string(value) == "1" || string(value) == "true"
}

func checkoutCatalogMatch(rows []checkoutCatalogRow, selection checkout.Selection) (checkoutCatalogRow, error) {
	var result checkoutCatalogRow
	found := false
	for _, row := range rows {
		id := row.ID
		if selection.Kind == checkout.KindGood && row.GoodID > 0 {
			id = row.GoodID
		}
		if id != selection.ID {
			continue
		}
		if selection.Kind == checkout.KindService && !checkoutServiceStaff(row.Staff, selection.StaffID) {
			continue
		}
		if strings.TrimSpace(row.Title) == "" {
			return checkoutCatalogRow{}, webapp.Conflict("selected item has no authoritative title", nil)
		}
		price, err := row.fixedPrice(selection.Kind)
		if err != nil {
			return checkoutCatalogRow{}, err
		}
		if found {
			previousPrice, err := result.fixedPrice(selection.Kind)
			if err != nil || previousPrice != price || result.Title != row.Title {
				return checkoutCatalogRow{}, webapp.Conflict("selected catalog item is ambiguous", nil)
			}
		}
		result, found = row, true
	}
	if !found {
		return checkoutCatalogRow{}, webapp.Conflict("selected item is unavailable for this company and staff", nil)
	}
	return result, nil
}

func checkoutServiceStaff(data []byte, staffID int64) bool {
	var rows []struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(data, &rows); err == nil {
		for _, row := range rows {
			if row.ID == staffID {
				return true
			}
		}
		return false
	}
	var row struct {
		ID int64 `json:"id"`
	}
	return json.Unmarshal(data, &row) == nil && row.ID == staffID
}

func (r checkoutCatalogRow) fixedPrice(kind string) (int64, error) {
	if kind == checkout.KindGood {
		price, err := checkout.ParseMoney(string(r.Cost))
		if err != nil || price <= 0 {
			return 0, webapp.Conflict("selected good has no valid fixed price", nil)
		}
		return price, nil
	}
	minimum, minErr := checkout.ParseMoney(string(r.PriceMin))
	maximum, maxErr := checkout.ParseMoney(string(r.PriceMax))
	if minErr != nil || maxErr != nil || minimum <= 0 || minimum != maximum {
		return 0, webapp.Conflict("selected service requires a confirmed fixed price", nil)
	}
	return minimum, nil
}

func checkoutVisit(draft models.PayDraft) (checkout.Visit, error) {
	result := checkout.Visit{VisitID: draft.VisitID, RecordID: draft.RecordID, Items: make([]checkout.VisitLine, 0, len(draft.Items))}
	for _, item := range draft.Items {
		amount, err := checkout.MoneyFromFloat(item.Amount)
		if err != nil || amount <= 0 {
			return checkout.Visit{}, webapp.Conflict("unpaid visit contains an invalid amount", nil)
		}
		price, err := checkout.MoneyFromFloat(item.Price)
		if err != nil || price < 0 {
			return checkout.Visit{}, webapp.Conflict("unpaid visit contains an invalid unit price", nil)
		}
		quantity, err := checkout.QuantityFromFloat(item.Quant)
		if err != nil {
			return checkout.Visit{}, webapp.Conflict("unpaid visit contains an unsupported quantity", nil)
		}
		result.Items = append(result.Items, checkout.VisitLine{
			Kind: item.ItemKind, ID: item.ID, LineID: item.LineID,
			StaffID: int64(item.Staff.ID), StaffName: item.Staff.Name, Title: item.Title,
			Quantity: quantity, UnitPriceKopecks: price, AmountKopecks: amount,
		})
	}
	return result, nil
}
