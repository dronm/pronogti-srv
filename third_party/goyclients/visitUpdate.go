package goyclients

import (
	"encoding/json"
	"fmt"
)

type VisitItemTypeID int64

const (
	VisitItemTypeService VisitItemTypeID = 1
	VisitItemTypeGood    VisitItemTypeID = 7
)

type UpdateVisit struct {
	Items []UpdateVisitItem `json:"items"`
}

type UpdateVisitItem struct {
	// Object ID inside the visit.
	// For services this is often 0.
	// For goods this is usually the goods transaction ID.
	ID int64 `json:"id"`

	// Product/service identifier.
	ItemID int64 `json:"item_id"`

	// Operation type identifier:
	// 1 - service
	// 7 - good
	ItemTypeID VisitItemTypeID `json:"item_type_id"`

	// Appointment/record ID.
	RecordID int64 `json:"record_id"`

	ItemTitle string `json:"item_title"`

	// Number of products/services.
	Amount float64 `json:"amount"`

	// Initial item price.
	FirstCost float64 `json:"first_cost"`

	// Manually changed item price.
	ManualCost float64 `json:"manual_cost"`

	// Discount amount/percent according to YCLIENTS visit item semantics.
	Discount float64 `json:"discount"`

	// Total cost of this line.
	Cost float64 `json:"cost"`

	// Team member ID.
	MasterID int64 `json:"master_id"`

	// Good/product ID. Set to 0 for services.
	GoodID int64 `json:"good_id"`

	// Service ID. Set to 0 for goods.
	ServiceID int64 `json:"service_id"`

	// Promotion/event ID. Usually 0 for normal services/goods.
	EventID int64 `json:"event_id"`

	IsService bool `json:"is_service"`
	IsEvent   bool `json:"is_event"`
	IsGood    bool `json:"is_good"`
}

type UpdatedVisit struct {
	PaymentTransactions            []VisitPaymentTransaction        `json:"payment_transactions"`
	LoyaltyTransactions            []VisitLoyaltyTransaction        `json:"loyalty_transactions"`
	KKMTransactionDetailsContainer VisitKKMTransactionDetails       `json:"kkm_transaction_details_container"`
	Items                          []UpdatedVisitItem               `json:"items"`
}

type VisitPaymentTransaction struct {
	ID                 int64           `json:"id"`
	DocumentID         int64           `json:"document_id"`
	Date               APIDateTime     `json:"date"`
	TypeID             int64           `json:"type_id"`
	ExpenseID          int64           `json:"expense_id"`
	AccountID          int64           `json:"account_id"`
	Amount             float64         `json:"amount"`
	ClientID           int64           `json:"client_id"`
	MasterID           int64           `json:"master_id"`
	SupplierID          int64           `json:"supplier_id"`
	Comment            string          `json:"comment"`
	ItemID             int64           `json:"item_id"`
	TargetTypeID       int64           `json:"target_type_id"`
	RecordID           int64           `json:"record_id"`
	GoodsTransactionID int64           `json:"goods_transaction_id"`
	Expense            json.RawMessage `json:"expense"`
	Account            json.RawMessage `json:"account"`
	Client             json.RawMessage `json:"client"`
	Master             json.RawMessage `json:"master"`
	Supplier           json.RawMessage `json:"supplier"`
}

type VisitLoyaltyTransaction struct {
	ID                 int64           `json:"id"`
	StatusID           int64           `json:"status_id"`
	Amount             float64         `json:"amount"`
	TypeID             int64           `json:"type_id"`
	ProgramID          int64           `json:"program_id"`
	CardID             int64           `json:"card_id"`
	SalonGroupID       int64           `json:"salon_group_id"`
	ItemID             int64           `json:"item_id"`
	ItemTypeID         int64           `json:"item_type_id"`
	ItemRecordID       int64           `json:"item_record_id"`
	GoodsTransactionID int64           `json:"goods_transaction_id"`
	IsDiscount         bool            `json:"is_discount"`
	IsLoyaltyWithdraw  bool            `json:"is_loyalty_withdraw"`
	Type               json.RawMessage `json:"type"`
}

type VisitKKMTransactionDetails struct {
	LastOperationType int64                   `json:"last_operation_type"`
	Transactions      []VisitKKMTransaction   `json:"transactions"`
}

type VisitKKMTransaction struct {
	ID           int64           `json:"id"`
	PrintDate    APIDateTime     `json:"print_date"`
	PrintedCount int64           `json:"printed_count"`
	Sum          float64         `json:"sum"`
	Type         json.RawMessage `json:"type"`
	Status       json.RawMessage `json:"status"`
	Document     json.RawMessage `json:"document"`
	Cashier      json.RawMessage `json:"cashier"`
}

type UpdatedVisitItem struct {
	ID         int64           `json:"id"`
	ItemID     int64           `json:"item_id"`
	ItemTypeID VisitItemTypeID `json:"item_type_id"`
	RecordID   int64           `json:"record_id"`
	ItemTitle  string          `json:"item_title"`
	Amount     float64         `json:"amount"`
	FirstCost  float64         `json:"first_cost"`
	ManualCost float64         `json:"manual_cost"`
	Discount   float64         `json:"discount"`
	Cost       float64         `json:"cost"`
	MasterID   int64           `json:"master_id"`
	GoodID     int64           `json:"good_id"`
	ServiceID  int64           `json:"service_id"`
	EventID    int64           `json:"event_id"`
	IsService  bool            `json:"is_service"`
	IsEvent    bool            `json:"is_event"`
	IsGood     bool            `json:"is_good"`
}

func NewUpdateVisitServiceItem(
	recordID int64,
	serviceID int64,
	masterID int64,
	title string,
	amount float64,
	price float64,
	total float64,
) UpdateVisitItem {
	return UpdateVisitItem{
		ID:         0,
		ItemID:     serviceID,
		ItemTypeID: VisitItemTypeService,
		RecordID:   recordID,
		ItemTitle:  title,
		Amount:     amount,
		FirstCost:  price,
		ManualCost: price,
		Discount:   0,
		Cost:       total,
		MasterID:   masterID,
		GoodID:     0,
		ServiceID:  serviceID,
		EventID:    0,
		IsService:  true,
		IsEvent:    false,
		IsGood:     false,
	}
}

func NewUpdateVisitGoodItem(
	recordID int64,
	goodsTransactionID int64,
	goodID int64,
	masterID int64,
	title string,
	amount float64,
	price float64,
	total float64,
) UpdateVisitItem {
	return UpdateVisitItem{
		ID:         goodsTransactionID,
		ItemID:     goodID,
		ItemTypeID: VisitItemTypeGood,
		RecordID:   recordID,
		ItemTitle:  title,
		Amount:     amount,
		FirstCost:  price,
		ManualCost: price,
		Discount:   0,
		Cost:       total,
		MasterID:   masterID,
		GoodID:     goodID,
		ServiceID:  0,
		EventID:    0,
		IsService:  false,
		IsEvent:    false,
		IsGood:     true,
	}
}

func (c *VisitAPI) Update(
	visitID int64,
	recordID int64,
	visit UpdateVisit,
) (UpdatedVisit, error) {
	if visitID == 0 {
		return UpdatedVisit{}, fmt.Errorf("visit id should be set")
	}

	if recordID == 0 {
		return UpdatedVisit{}, fmt.Errorf("record id should be set")
	}

	if len(visit.Items) == 0 {
		return UpdatedVisit{}, fmt.Errorf("visit items should not be empty")
	}

	resp := UpdatedVisit{}

	if err := c.client.SendRequest(
		fmt.Sprintf("%s/visits/%d/%d", apiVersionEndpoint, visitID, recordID),
		"PUT",
		nil,
		visit,
		&resp,
		nil,
	); err != nil {
		return UpdatedVisit{}, err
	}

	return resp, nil
}
