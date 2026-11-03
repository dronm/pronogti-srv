package models

import (
	"github.com/dronm/goyclients"
)

type PayDraftItemKind = string

const (
	PayDraftItemKindService PayDraftItemKind = "service"
	PayDraftItemKindGood    PayDraftItemKind = "good"
)

type PayDraft struct {
	DraftID    string `json:"draft_id"`
	ClientID   int64  `json:"client_id"`
	VisitID    int64  `json:"visit_id"`
	RecordID   int64  `json:"record_id"`
	DocumentID int64  `json:"document_id"`

	Items []PayDraftItem `json:"items"`

	Total        float64 `json:"total"`
	BonusTotal   float64 `json:"bonus_total"`
	BonusCanPay  float64 `json:"bonus_can_pay"`
	BonusToApply float64 `json:"bonus_to_apply"`
}

type PayDraftItem struct {
	ID       int64            `json:"id"`      // service_id or good_id
	LineID   int64            `json:"line_id"` // goods_transaction_id for goods; usually 0 for services
	Staff    goyclients.Staff `json:"staff"`   // staff/master responsible for this line
	Title    string           `json:"title"`
	ItemKind PayDraftItemKind `json:"item_kind"`
	Quant    float64          `json:"quant"`
	Price    float64          `json:"price"`
	Amount   float64          `json:"amount"`
}
