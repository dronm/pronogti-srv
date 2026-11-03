package goyclients

import (
	"fmt"
	"net/url"
	"strconv"
	"time"
)

//
// https://developers.yclients.com/ru/#tag/Zapisi/operation/%D0%9F%D0%BE%D0%BB%D1%83%D1%87%D0%B8%D1%82%D1%8C%20%D1%81%D0%BF%D0%B8%D1%81%D0%BE%D0%BA%20%D0%B7%D0%B0%D0%BF%D0%B8%D1%81%D0%B5%D0%B9

type RecordAPI struct {
	client    *YClients
	companyID int64
}

func (c *YClients) NewRecordAPI(companyID int64) *RecordAPI {
	return &RecordAPI{
		client:    c,
		companyID: companyID,
	}
}

type RecordFilter struct {
	Page                       int     `json:"page,omitempty"`
	Count                      int     `json:"count,omitempty"`
	StaffID                    int64   `json:"staff_id,omitempty"`
	ClientID                   int64   `json:"client_id,omitempty"`
	CreatedUserID              int64   `json:"created_user_id,omitempty"`
	StartDate                  APIDate `json:"start_date"`
	EndDate                    APIDate `json:"end_date"`
	CStartDate                 APIDate `json:"c_start_date"`
	CEndDate                   APIDate `json:"c_end_date"`
	ChangedAfter               string  `json:"changed_after,omitempty"`
	ChangedBefore              string  `json:"changed_before,omitempty"`
	IncludeConsumables         int64   `json:"include_consumables,omitempty"`
	IncludeFinanceTransactions int64   `json:"include_finance_transactions,omitempty"`
	WithDeleted                *bool   `json:"with_deleted,omitempty"` // boolean version
}

func (rf *RecordFilter) ToURLValues() url.Values {
	values := url.Values{}

	// Add integer values if they are not zero
	if rf.Page != 0 {
		values.Add("page", strconv.Itoa(rf.Page))
	}
	if rf.Count != 0 {
		values.Add("count", strconv.Itoa(rf.Count))
	}
	if rf.StaffID != 0 {
		values.Add("staff_id", strconv.FormatInt(rf.StaffID, 10))
	}
	if rf.ClientID != 0 {
		values.Add("client_id", strconv.FormatInt(rf.ClientID, 10))
	}
	if rf.CreatedUserID != 0 {
		values.Add("created_user_id", strconv.FormatInt(rf.CreatedUserID, 10))
	}
	if rf.IncludeConsumables != 0 {
		values.Add("include_consumables", strconv.FormatInt(rf.IncludeConsumables, 10))
	}
	if rf.IncludeFinanceTransactions != 0 {
		values.Add("include_finance_transactions", strconv.FormatInt(rf.IncludeFinanceTransactions, 10))
	}

	// Add date values if they are not empty
	if !time.Time(rf.StartDate).IsZero() {
		values.Add("start_date", rf.StartDate.String())
	}
	if !time.Time(rf.EndDate).IsZero() {
		values.Add("end_date", rf.EndDate.String())
	}
	if !time.Time(rf.CStartDate).IsZero() {
		values.Add("c_start_date", rf.CStartDate.String())
	}
	if !time.Time(rf.CEndDate).IsZero() {
		values.Add("c_end_date", rf.CEndDate.String())
	}

	// Add string values if they are not empty
	if rf.ChangedAfter != "" {
		values.Add("changed_after", rf.ChangedAfter)
	}
	if rf.ChangedBefore != "" {
		values.Add("changed_before", rf.ChangedBefore)
	}

	// Add boolean value if it's not nil
	if rf.WithDeleted != nil {
		values.Add("with_deleted", strconv.FormatBool(*rf.WithDeleted))
	}

	return values
}

type Record struct {
	ID        int `json:"id"`         // ID записи
	CompanyID int `json:"company_id"` // Идентификатор компании
	StaffID   int `json:"staff_id"`   // Идентификатор сотрудника
	Services  []struct {
		ID          int     `json:"id"`
		Title       string  `json:"title"`
		Cost        float64 `json:"cost"`
		ManualCost  float64 `json:"manual_cost"`
		CostPerUnit float64 `json:"cost_per_unit"`
		Discount    float64 `json:"discount"`
		FirstCost   float64 `json:"first_cost"`
		Amount      float64 `json:"amount"`
	} `json:"services"` // Массив объектов с услугами в записи
	GoodsTransactions []struct {
		ID          int     `json:"id"`
		Title       string  `json:"title"`
		Amount      float64 `json:"amount"`
		CostPerUnit float64 `json:"cost_per_unit"`
		Price       float64 `json:"price"`
		Discount    float64 `json:"discount"`
	} `json:"goods_transactions"` // Массив товарных транзакций
	Staff struct {
		ID             int     `json:"id"`
		Name           string  `json:"name"`
		Specialization string  `json:"specialization"`
		Avatar         string  `json:"avatar"`
		AvatarBig      string  `json:"avatar_big"`
		Rating         float64 `json:"rating"`
		VotesCount     int     `json:"votes_count"`
	} `json:"staff"`
	StaffData              map[string]any `json:"staff_data"`               // Объект данных о сотруднике
	ClientData             map[string]any `json:"client_data"`              // Данные клиента (может быть пустым)
	Comer                  any            `json:"comer"`                    // Данные о посетителе (может быть null)
	ClientsCount           int            `json:"clients_count"`            // Количество клиентов (В индивидуальной записи всегда = 1)
	Date                   string         `json:"date"`                     // Дата сеанса
	Datetime               string         `json:"datetime"`                 // Дата сеанса в ISO
	CreateDate             string         `json:"create_date"`              // Дата создания сеанса
	TechnicalBreakDuration float64        `json:"technical_break_duration"` // Длительность технического перерыва
	Comment                string         `json:"comment"`                  // Комментарий к записи
	Online                 bool           `json:"online"`                   // (Только при чтении) Запись онлайновая или нет (false если запись внес администратор)
	VisitAttendance        int            `json:"visit_attendance"`         // Статус визита, 2 - Пользователь подтвердил запись, 1 - Пользователь пришел, услуги оказаны, 0 - ожидание пользователя, -1 - пользователь не пришел на визит
	Attendance             int            `json:"attendance"`               // Статус записи, 2 - Пользователь подтвердил запись, 1 - Пользователь пришел, услуги оказаны, 0 - ожидание пользователя, -1 - пользователь не пришел на визит
	Confirmed              int            `json:"confirmed"`                // Статус подтверждения записи, 0 - не подтверждена, 1 - подтверждена
	SeanceLength           int            `json:"seance_length"`            // Длительность сеанса
	Length                 int            `json:"length"`                   // Длительность сеанса
	SMSBefore              int            `json:"sms_before"`               // Отправить ли клиенту смс с напоминанием о записи (если клиент указан)
	SMSNow                 int            `json:"sms_now"`                  // Отправляли ли смс при добавлении записи
	SMSNowText             string         `json:"sms_now_text"`             // Текст смс
	EmailNow               int            `json:"email_now"`                // Отправлено ли уведомление по email
	Notified               int            `json:"notified"`                 // Флаг подтверждения записи администратором филиала, если клиент попросил подтвердить запись
	MasterRequest          int            `json:"master_request"`           // Был ли указан определенный специалист при записи (false если был указан "не имеет значения")
	APIID                  string         `json:"api_id"`                   // Внешний идентификатор записи
	FromURL                string         `json:"from_url"`                 // С какой страницы был совершён переход для оформления записи (сайт, приложение ВК и прочее)
	ReviewRequested        int            `json:"review_requested"`         // Флаг запроса у клиента отзыва о посещении
	VisitID                int            `json:"visit_id"`                 // Идентификатор визита
	CreatedUserID          int            `json:"created_user_id"`          // Идентификатор пользователя, создавшего запись
	Deleted                bool           `json:"deleted"`                  // (Только при чтении) Удалена ли запись (true если удалена)
	PaidFull               int            `json:"paid_full"`                // Флаг, оплачена ли запись полностью (1 - если оплачена полностью)
	Prepaid                bool           `json:"prepaid"`                  // Доступна ли онлайн-оплата
	PrepaidConfirmed       bool           `json:"prepaid_confirmed"`        // Статус онлайн-оплаты
	PaymentStatus          int            `json:"payment_status"`           // Статус автооплаты абонементом (0 - Визит не оплачен, 1 - Визит оплачен, 2 - визит оплачен абонементом автоматически, 3 - Визит не удалось оплатить автоматически, 4 - будет оплачен абонементом автоматически)
	LastChangeDate         string         `json:"last_change_date"`         // Дата последнего редактирования записи
	CustomColor            string         `json:"custom_color"`             // Цвет записи в журнале
	CustomFontColor        string         `json:"custom_font_color"`        // (Только при чтении) Цвет шрифта записи
	RecordLabels           []any          `json:"record_labels"`            // Категории записи
	ActivityID             int            `json:"activity_id"`              // Идентификатор группового события
	// CustomFields           map[string]any         `json:"custom_fields"`            // Дополнительные поля
	Documents        []any   `json:"documents"`          // Массив документов
	SMSRemainHours   float64 `json:"sms_remain_hours"`   // За сколько часов до начала визита отправить sms с напоминанием
	EmailRemainHours float64 `json:"email_remain_hours"` // За сколько часов до начала визита отправить email с напоминанием
	BookformID       int     `json:"bookform_id"`        // Идентификатор формы онлайн-записи
	RecordFrom       string  `json:"record_from"`        // Название формы онлайн-записи
	IsMobile         int     `json:"is_mobile"`          // Тип устройства, с которого записались (0 - запись, создана администратором, через web-версию или моб. приложение для администраторов, 1 - Мобильный браузер, виджет онлайн-записи, 2 - Десктопный браузер, виджет онлайн-записи)
}

// Get Получить список записей
func (c *RecordAPI) Get(filter *RecordFilter) ([]*Record, error) {
	resp := make([]*Record, 0)
	if err := c.client.SendRequest(fmt.Sprintf("%s/records/%d", apiVersionEndpoint, c.companyID), "GET", filter.ToURLValues(), nil, &resp, nil); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *RecordAPI) Delete(recordID int64) error {
	if err := c.client.SendRequest(fmt.Sprintf("%s/record/%d/%d", apiVersionEndpoint, c.companyID, recordID), "DELETE", nil, nil, nil, nil); err != nil {
		return err
	}
	return nil
}
