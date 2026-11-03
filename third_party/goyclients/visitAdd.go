package goyclients

import (
	"fmt"
	"net/url"
	"strconv"
	"time"
)

type CreateRecordService struct {
	ID        int64   `json:"id"`
	FirstCost float64 `json:"first_cost,omitempty"`
	Discount  float64 `json:"discount,omitempty"`
	Cost      float64 `json:"cost,omitempty"`
}

type CreateRecordClient struct {
	Phone string `json:"phone,omitempty"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

type CreateRecord struct {
	StaffID  int64                 `json:"staff_id,omitempty"`
	Services []CreateRecordService `json:"services,omitempty"`
	Client   *CreateRecordClient   `json:"client,omitempty"`

	SaveIfBusy bool `json:"save_if_busy,omitempty"`

	// ISO datetime, for example: 2026-05-12T14:30:00+03:00
	DateTime APIDate `json:"datetime,omitempty"`

	// Appointment duration in seconds.
	SeanceLength int64 `json:"seance_length,omitempty"`

	SendSMS bool   `json:"send_sms,omitempty"`
	Comment string `json:"comment,omitempty"`

	SMSRemainHours   *float64 `json:"sms_remain_hours,omitempty"`
	EmailRemainHours *float64 `json:"email_remain_hours,omitempty"`

	// 2 - confirmed, 1 - came, 0 - waiting, -1 - did not come.
	Attendance *int `json:"attendance,omitempty"`

	APIID       string `json:"api_id,omitempty"`
	CustomColor string `json:"custom_color,omitempty"`

	RecordLabels []string `json:"record_labels,omitempty"`

	// Technical break duration in seconds.
	// Must be a multiple of 300. Max 3600.
	TechnicalBreakDuration *int64 `json:"technical_break_duration,omitempty"`

	CustomFields map[string]any `json:"custom_fields,omitempty"`

	// Required only for group event appointment creation.
	EventID int64 `json:"event_id,omitempty"`
}

type CreateRecordOptions struct {
	IncludeConsumables         int64
	IncludeFinanceTransactions int64
}

func (opts *CreateRecordOptions) ToURLValues() url.Values {
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

type CreatedRecords []*Record

func (c *RecordAPI) Create(record CreateRecord) (CreatedRecords, error) {
	return c.CreateWithOptions(record, nil)
}

func (c *RecordAPI) CreateWithOptions(
	record CreateRecord,
	opts *CreateRecordOptions,
) (CreatedRecords, error) {
	if record.EventID == 0 {
		if record.StaffID == 0 {
			return nil, fmt.Errorf("staff id should be set")
		}

		if len(record.Services) == 0 {
			return nil, fmt.Errorf("services should not be empty")
		}

		emptyDate := APIDate(time.Time{})
		if record.DateTime == emptyDate {
			return nil, fmt.Errorf("datetime should be set")
		}

		if record.SeanceLength == 0 {
			return nil, fmt.Errorf("seance length should be set")
		}
	}

	if record.Client == nil {
		return nil, fmt.Errorf("client should be set")
	}

	resp := make(CreatedRecords, 0)
	// Keep the exported APIDate field for source compatibility, but serialize
	// this endpoint's datetime as a full timestamp. APIDate is a date-only type
	// and its pointer marshaler is not used when record is passed by value.
	wire := struct {
		CreateRecord
		DateTime string `json:"datetime,omitempty"`
	}{CreateRecord: record}
	if at := time.Time(record.DateTime); !at.IsZero() {
		wire.DateTime = at.Format(time.RFC3339)
	}

	if err := c.client.SendRequest(
		fmt.Sprintf("%s/records/%d", apiVersionEndpoint, c.companyID),
		"POST",
		opts.ToURLValues(),
		wire,
		&resp,
		nil,
	); err != nil {
		return nil, err
	}

	return resp, nil
}
