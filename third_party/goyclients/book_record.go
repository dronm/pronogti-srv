package goyclients

import (
	"fmt"
	"time"
)

type BookRecord struct {
	Phone         string                  `json:"phone"`          // обязательное
	FullName      string                  `json:"fullname"`       // обязательное
	Email         string                  `json:"email"`          // обязательное
	Appointments  []BookRecordAppointment `json:"appointments"`   // обязательное
	Code          string                  `json:"code,omitempty"` // необязательное
	NotifyBySMS   int                     `json:"notify_by_sms,omitempty"`
	NotifyByEmail int                     `json:"notify_by_email,omitempty"`
	Comment       string                  `json:"comment,omitempty"`
	APIID         string                  `json:"api_id,omitempty"`
	CustomFields  map[string]any          `json:"custom_fields,omitempty"`
}

type BookRecordAppointment struct {
	ID       int64 `json:"id"` // required
	Services []struct {
		ID           int    `json:"id"`
		Title        string `json:"title"`
		Amount       int    `json:"amount"`        // Количество заказанных услуг
		SeanceLength int    `json:"seance_length"` // Длительность оказания услуги в секундах (только если задан фильтр по сотрудникуДлительность оказания услуги в секундах (только если задан фильтр по сотруднику))
	} `json:"services,omitempty"` // optional
	StaffID      int64          `json:"staff_id"`                // required (0 = any)
	DateTime     time.Time      `json:"datetime"`                // required (ISO8601)
	CustomFields map[string]any `json:"custom_fields,omitempty"` // optional
}

type AddedBookRecord struct {
	ID         int64  `json:"id"`
	RecordID   int64  `json:"record_id"`
	RecordHash string `json:"record_hash"`
}

func (c *YClients) AddBookRecord(companyID int64, record *BookRecord) ([]*AddedBookRecord, error) {
	resp := make([]*AddedBookRecord, 0)
	if err := c.SendRequest(fmt.Sprintf("%s/book_record/%d", apiVersionEndpoint, companyID), "POST", nil, record, &resp, nil); err != nil {
		return nil, err
	}
	return resp, nil
}

// BookedRecord is a response structure
type BookedRecord struct {
	ID       int64 `json:"id"`
	Services []struct {
		ID           int    `json:"id"`
		Title        string `json:"title"`
		Amount       int    `json:"amount"`        // Количество заказанных услуг
		SeanceLength int    `json:"seance_length"` // Длительность оказания услуги в секундах (только если задан фильтр по сотрудникуДлительность оказания услуги в секундах (только если задан фильтр по сотруднику))
	}
	Company struct {
		ID                         int64      `json:"id"`
		Title                      string   `json:"title"`
		CountryID                  int64      `json:"country_id"`
		Country                    string   `json:"country"`
		CityID                     int64      `json:"city_id"`
		City                       string   `json:"city"`
		Phone                      string   `json:"phone"`
		Phones                     []string `json:"phones"`
		Timezone                   string   `json:"timezone"`
		Address                    string   `json:"address"`
		CoordinateLat              float64  `json:"coordinate_lat"`
		CoordinateLon              float64  `json:"coordinate_lon"`
		AllowDeleteRecord          bool     `json:"allow_delete_record"`
		AllowChangeRecord          bool     `json:"allow_change_record"`
		Site                       string   `json:"site"`
		CurrencyShortTitle         string   `json:"currency_short_title"`
		AllowChangeRecordDelayStep int32    `json:"allow_change_record_delay_step"`
		AllowDeleteRecordDelayStep int32    `json:"allow_delete_record_delay_step"`
	}
	Staff            Staff
	ClientsCount     int     `json:"clients_count"`
	Date             string  `json:"date"`        // "2025-01-01 12:00:00", required
	DateTime         string  `json:"datetime"`    // ISO8601
	CreateDate       string  `json:"create_date"` // required
	Comment          string  `json:"comment,omitempty"`
	Deleted          bool    `json:"deleted,omitempty"`
	Attendance       int     `json:"attendance,omitempty"` // 2,1,0,-1
	Length           float64 `json:"length,omitempty"`     // duration in hours/minutes
	NotifyBySMS      int     `json:"notify_by_sms,omitempty"`
	NotifyByEmail    int     `json:"notify_by_email,omitempty"`
	MasterRequested  bool    `json:"master_requested,omitempty"`
	Online           bool    `json:"online,omitempty"`
	APIID            string  `json:"api_id,omitempty"`
	LastChangeDate   string  `json:"last_change_date,omitempty"`
	Prepaid          bool    `json:"prepaid,omitempty"`
	PrepaidConfirmed bool    `json:"prepaid_confirmed,omitempty"`
	ActivityID       int     `json:"activity_id,omitempty"`
}

// BookRecordByID reads existing book record
// Получить запись на сеанс
func (c *YClients) BookRecordByID(companyID int64, recordID int64) (*BookedRecord, error) {
	resp := BookedRecord{}
	if err := c.SendRequest(fmt.Sprintf("%s/book_record/%d/%d", apiVersionEndpoint, companyID, recordID), "GET", nil, nil, &resp, nil); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *YClients) BookRecordByHash(companyID int64, recordHash string) (*BookedRecord, error) {
	resp := BookedRecord{}
	if err := c.SendRequest(fmt.Sprintf("%s/book_record/%d/%s", apiVersionEndpoint, companyID, recordHash), "GET", nil, nil, &resp, nil); err != nil {
		return nil, err
	}
	return &resp, nil
}

type AddedRecord struct {
	ID         int64    `json:"id"`          // ID записи переданный в запросе в appontments
	RecordID   int64    `json:"record_id"`   // ID записи в системе yclients. Используется для удаления записи
	RecordHash string `json:"record_hash"` // HASH записи в системе yclients. Используется для удаления записи неавторизованным пользователем сразу после бронирования
}

type NewBookRecord struct {
	Phone        string `json:"phone"`    // Телефон клиента (например 79161502239)
	FullName     string `json:"fullname"` // Имя клиента
	Email        string `json:"email"`    // Почтовый адрес клиента
	Appointments []struct {
		ID           int64               `json:"id"`
		Services     []int64             `json:"services,omitempty"`
		StaffID      int64               `json:"staff_id"`
		DateTime     time.Time         `json:"datetime"`
		CustomFields map[string]string `json:"custom_fields,omitempty"`
	} `json:"appointments"` // Параметры записи (услуги, специалисты...)
	Code          *string           `json:"code,omitempty"`            // Код подтверждения номера телефона, высланный по СМС (нужен только в случае необходимости подтверждения номера)
	NotifyBySMS   *int              `json:"notify_by_sms,omitempty"`   // За какое кол-во часов отправить смс напоминание о записи (0 если не нужно напоминать)
	NotifyByEmail *int              `json:"notify_by_email,omitempty"` // За какое кол-во часов отправить email напоминание о записи (0 если не нужно напоминать)
	Comment       *string           `json:"comment,omitempty"`         // Комментарий к записи
	APIID         *string           `json:"api_id,omitempty"`          // Внешний идентификатор записи
	CustomFields  map[string]string `json:"custom_fields,omitempty"`   // Значения дополнительных полей карточки клиента, которые сохраняются вместе с записью
}

func (c *YClients) BookRecordAdd(companyID int64, bookRecord *NewBookRecord) (*AddedRecord, error) {
	resp := AddedRecord{}
	if err := c.SendRequest(fmt.Sprintf("%s/book_record/%d", apiVersionEndpoint, companyID), "POST", nil, bookRecord, &resp, nil); err != nil {
		return nil, err
	}
	return &resp, nil
}

type BookRecordNewDate struct {
	Datetime time.Time `json:"datetime"`
	Comment  string    `json:"comment"`
}

func (c *YClients) BookRecordPostpone(companyID int64, bookRecordID int64, newDate BookRecordNewDate) (*BookRecord, error) {
	resp := BookRecord{}
	if err := c.SendRequest(fmt.Sprintf("%s/book_record/%d", apiVersionEndpoint, companyID), "PUT", nil, newDate, &resp, nil); err != nil {
		return nil, err
	}
	return &resp, nil
}
