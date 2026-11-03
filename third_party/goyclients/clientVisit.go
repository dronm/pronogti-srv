package goyclients

import "fmt"

type ClientVisitPaymentStatus string

const (
	ClientVisitPaymentStatusNotPaid     ClientVisitPaymentStatus = "not_paid"
	ClientVisitPaymentStatusPaidNotFull ClientVisitPaymentStatus = "paid_not_full"
	ClientVisitPaymentStatusPaidFull    ClientVisitPaymentStatus = "paid_full"
	ClientVisitPaymentStatusPaidOver    ClientVisitPaymentStatus = "paid_over"
)

type ClientVisitAttendance int

const (
	ClientVisitAttendanceNotCome   ClientVisitAttendance = -1
	ClientVisitAttendanceWaiting   ClientVisitAttendance = 0
	ClientVisitAttendanceArrived   ClientVisitAttendance = 1
	ClientVisitAttendanceConfirmed ClientVisitAttendance = 2
)

type ClientVisitGoodsTransaction struct {
	ID       int64             `json:"id"`
	Comment  string            `json:"comment"`
	Date     string            `json:"date"`
	VisitID  int64             `json:"visit_id"`
	RecordID int64             `json:"record_id"`
	Goods    []ClientVisitGood `json:"goods"`
	Staff    Staff             `json:"staff"`
	Company  Company           `json:"company"`
}

type ClientVisitGood struct {
	ID              int64                    `json:"id"`
	Title           string                   `json:"title"`
	Amount          float64                  `json:"amount"`
	Unit            string                   `json:"unit"`
	CostPerUnit     float64                  `json:"cost_per_unit"`
	FirstCost       float64                  `json:"first_cost"`
	DiscountPercent float64                  `json:"discount_percent"`
	CostToPay       float64                  `json:"cost_to_pay"`
	PaidSum         float64                  `json:"paid_sum"`
	PaymentStatus   ClientVisitPaymentStatus `json:"payment_status"`
}
type ClientVisitService struct {
	ID              int64                          `json:"id"`
	Title           string                         `json:"title"`
	FirstCost       float64                        `json:"first_cost"`
	DiscountPercent float64                        `json:"discount_percent"`
	CostToPay       float64                        `json:"cost_to_pay"`
	PaidSum         float64                        `json:"paid_sum"`
	PaymentStatus   ClientVisitPaymentStatus       `json:"payment_status"`
	Consumables     []ClientVisitServiceConsumable `json:"consumables"`
}

type ClientVisitServiceConsumable struct {
	Title       string  `json:"title"`
	Amount      float64 `json:"amount"`
	CostPerUnit float64 `json:"cost_per_unit"`
	Unit        string  `json:"unit"`
}

type ClientVisitStaff struct {
	ID             int64          `json:"id"`
	Name           string         `json:"name"`
	CompanyID      int64          `json:"company_id"`
	Specialization string         `json:"specialization"`
	Avatar         string         `json:"avatar"`
	AvatarBig      string         `json:"avatar_big"`
	Position       *StaffPosition `json:"position,omitempty"`
}

type ClientVisitRecord struct {
	ID         int64                `json:"id"`
	Comment    string               `json:"comment"`
	Date       string               `json:"date"`
	VisitID    int64                `json:"visit_id"`
	Attendance int64                `json:"attendance"`
	Services   []ClientVisitService `json:"services"`
	Staff      ClientVisitStaff     `json:"staff"`
	Company    Company              `json:"company"`
	Tips       ClientVisitTips      `json:"tips"`
	Comer      *ClientVisitComer    `json:"comer,omitempty"`
}

type ClientVisitTips struct {
	HasTips bool     `json:"has_tips"`
	Sum     *float64 `json:"sum"`
}

type ClientVisitComer struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Slug  string `json:"slug"`
}
type ClientVisitHistory struct {
	Records           []ClientVisitRecord           `json:"records"`
	GoodsTransactions []ClientVisitGoodsTransaction `json:"goods_transactions"`
}

type ClientVisitFilter struct {
	ClientID        *int64                     `json:"client_id,omitempty"`
	ClientPhone     *string                    `json:"client_phone,omitempty"`
	From            *APIDate                   `json:"from,omitempty"`
	To              *APIDate                   `json:"to,omitempty"`
	PaymentStatuses []ClientVisitPaymentStatus `json:"payment_statuses,omitempty"`
	Attendance      *ClientVisitAttendance     `json:"attendance,omitempty"`
}

func (c *ClientAPI) GetVisits(filter ClientVisitFilter) (ClientVisitHistory, error) {
	if filter.ClientID == nil && filter.ClientPhone == nil {
		return ClientVisitHistory{}, fmt.Errorf("client id or phone should be set")
	}
	resp := ClientVisitHistory{}
	if err := c.client.SendRequest(fmt.Sprintf("%s/company/%d/clients/visits/search", apiVersionEndpoint, c.companyID), "POST", nil, filter, &resp, nil); err != nil {
		return ClientVisitHistory{}, err
	}
	return resp, nil
}
