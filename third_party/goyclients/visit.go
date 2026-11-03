package goyclients

import (
	"encoding/json"
	"fmt"
)

// Для работы с визитами

type VisitAPI struct {
	client    *YClients
	companyID int64
}

func (c *YClients) NewVisitAPI(companyID int64) *VisitAPI {
	return &VisitAPI{
		client:    c,
		companyID: companyID,
	}
}

// Visit is for VisitByID() response
type Visit struct {
	Attendance int         `json:"attendance"` // Статус визита
	Datetime   APIDateTime `json:"datetime"`
	Comment    int         `json:"comment"`
	Records    []struct {
		ID        int64 `json:"id"`
		CompanyID int64 `json:"company_id"`
		StaffID   int64 `json:"staff_id"`

		Services []struct {
			ID           int64    `json:"id"`
			Title        string `json:"title"`
			Amount       int    `json:"amount"`        // Количество заказанных услуг
			SeanceLength int    `json:"seance_length"` // Длительность оказания услуги в секундах (только если задан фильтр по сотрудникуДлительность оказания услуги в секундах (только если задан фильтр по сотруднику))
		} `json:"services"`

		Events            []json.RawMessage `json:"events"`
		GoodsTransactions []json.RawMessage `json:"goods_transactions"`

		Staff struct {
			ID             int64     `json:"id"`
			Name           string  `json:"name"`
			Specialization string  `json:"specialization"`
			Avatar         string  `json:"avatar"`
			AvatarBig      string  `json:"avatar_big"`
			ShowRating     int     `json:"show_rating"`
			Rating         float32 `json:"rating"`
			VotesCount     int     `json:"votes_count"`
			CommentsCount  int     `json:"comments_count"`
		}

		Client struct {
			ID                 int64    `json:"id"`                   // Идентификатор клиента
			Name               string `json:"name"`                 // Имя клиента
			Surname            string `json:"surname"`              // Фамилия клиента
			Patronymic         string `json:"patronymic"`           // Отчество клиента
			Phone              string `json:"phone"`                // Номер телефон клиента
			Card               string `json:"card"`                 // Номер карты клиента
			Email              string `json:"email"`                // Email адрес клиента
			SuccessVisitsCount int    `json:"success_visits_count"` // Количество визитов клиента, в статусе "Клиент пришел"
			FailVisitsCount    int    `json:"fail_visits_count"`    // Количество отмененных визитов клиента
		} `json:"client"`

		Date             APIDateTime `json:"date"`
		Datetime         APIDateTime `json:"datetime"`
		CreateDate       APIDateTime `json:"create_date"`
		Comment          string      `json:"comment"`
		Online           bool        `json:"online"`
		VisitAttendance  int         `json:"visit_attendance"` // Статус визита
		Attendance       int         `json:"attendance"`       // Статус записи в визите
		Confirmed        int         `json:"confirmed"`        // Подтверждена ли запись
		SeanceLength     int         `json:"seance_length"`    // Длительность визита
		Length           int         `json:"length"`           // Длительность услуги в визите
		SmsBefore        int         `json:"sms_before"`       // Отправить ли клиенту смс с напоминанием о записи (если клиент указан)
		SmsNow           int         `json:"sms_now"`          // Отправляли ли смс при добавлении записи
		SmsNowText       string      `json:"sms_now_text"`     // Текст смс
		EmailNow         int         `json:"email_now"`        // Отправлено ли уведомление по email
		Notified         int         `json:"notified"`         // Флаг подтверждения записи администратором филиала, если клиент попросил подтвердить запись
		MasterRequest    int         `json:"master_request"`   // Был ли указан определенный специалист при записи (false если был указан "не имеет значения")
		APIID            string      `json:"api_id"`           // Внешний идентификатор записи
		FromURL          string      `json:"from_url"`         // С какой страницы был совершён переход для оформления записи (сайт, приложение ВК и прочее)
		ReviewRequested  int         `json:"review_requested"` // Флаг запроса у клиента отзыва о посещении
		ActivityID       int         `json:"activity_id"`      // Идентификатор групповой записи
		SmsRemainHours   int         `json:"sms_remain_hours"`
		EmailRemainHours int         `json:"email_remain_hours"`
		IsMobile         int         `json:"is_mobile"`
		ShortLink        string      `json:"short_link"`     // visit picture with staff member, amount, address
		PaymentStatus    int         `json:"payment_status"` // 1- payed
	} `json:"records"`
}

func (c *VisitAPI) GetByID(id int64) (*Visit, error) {
	resp := &Visit{}
	if err := c.client.SendRequest(fmt.Sprintf("%s/visits/%d", apiVersionEndpoint, id), "GET", nil, nil, &resp, nil); err != nil {
		return nil, err
	}
	return resp, nil
}

// func (c *VisitAPI) GetDetails(id int) (*Visit, error) {
// 	resp := &Visit{}
// 	if err := c.client.SendRequest(fmt.Sprintf("%s/visits/%d", apiVersionEndpoint, id), "GET", nil, nil, &resp, nil); err != nil {
// 		return nil, err
// 	}
// 	return resp, nil
// }
