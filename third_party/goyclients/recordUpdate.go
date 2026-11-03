package goyclients

import (
	"fmt"
	"net/url"
	"strconv"
)

type UpdateRecordService struct {
	ID        int64   `json:"id"`
	FirstCost float64 `json:"first_cost,omitempty"`
	Discount  float64 `json:"discount,omitempty"`
	Cost      float64 `json:"cost,omitempty"`
}

type UpdateRecordClient struct {
	Phone string `json:"phone,omitempty"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

type UpdateRecord struct {
	StaffID int64                 `json:"staff_id,omitempty"`
	Services []UpdateRecordService `json:"services,omitempty"`
	Client   *UpdateRecordClient   `json:"client,omitempty"`

	SaveIfBusy bool `json:"save_if_busy,omitempty"`

	// ISO datetime, for example: 2026-09-21T23:00:00.000-05:00
	DateTime string `json:"datetime,omitempty"`

	// Appointment duration in seconds.
	SeanceLength int64 `json:"seance_length,omitempty"`

	SendSMS bool   `json:"send_sms,omitempty"`
	Comment string `json:"comment,omitempty"`

	SMSRemainHours   *float64 `json:"sms_remain_hours,omitempty"`
	EmailRemainHours *float64 `json:"email_remain_hours,omitempty"`

	// 2 - confirmed, 1 - came, 0 - waiting, -1 - did not come.
	// Pointer is intentional: zero is a meaningful value.
	Attendance *int `json:"attendance,omitempty"`

	APIID       string `json:"api_id,omitempty"`
	CustomColor string `json:"custom_color,omitempty"`

	// API expects array of record label IDs as strings.
	RecordLabels []string `json:"record_labels,omitempty"`

	// Seconds. Must be a multiple of 300. Max 3600.
	TechnicalBreakDuration *int64 `json:"technical_break_duration,omitempty"`

	// Custom appointment fields by custom field code.
	CustomFields map[string]any `json:"custom_fields,omitempty"`
}

type UpdatedRecord = Record

type RecordUpdateOptions struct {
	IncludeConsumables         int64
	IncludeFinanceTransactions int64
}

func (opts *RecordUpdateOptions) ToURLValues() url.Values {
	values := url.Values{}

	if opts == nil {
		return values
	}

	if opts.IncludeConsumables != 0 {
		values.Add("include_consumables", strconv.FormatInt(opts.IncludeConsumables, 10))
	}

	if opts.IncludeFinanceTransactions != 0 {
		values.Add("include_finance_transactions", strconv.FormatInt(opts.IncludeFinanceTransactions, 10))
	}

	return values
}

func (c *RecordAPI) Update(recordID int64, record UpdateRecord) (UpdatedRecord, error) {
	return c.UpdateWithOptions(recordID, record, nil)
}

func (c *RecordAPI) UpdateWithOptions(
	recordID int64,
	record UpdateRecord,
	opts *RecordUpdateOptions,
) (UpdatedRecord, error) {
	if recordID == 0 {
		return UpdatedRecord{}, fmt.Errorf("record id should be set")
	}

	resp := UpdatedRecord{}

	if err := c.client.SendRequest(
		fmt.Sprintf("%s/record/%d/%d", apiVersionEndpoint, c.companyID, recordID),
		"PUT",
		opts.ToURLValues(),
		record,
		&resp,
		nil,
	); err != nil {
		return UpdatedRecord{}, err
	}

	return resp, nil
}
