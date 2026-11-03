package goyclients

import (
	"encoding/json"
	"fmt"
)

// Записи пользователя
// Для управлениями записями пользователя используются следующие методы

type UserRecordAPI struct {
	client    *YClients
	companyID int64
}

// NewUserRecordAPI creates a new UserRecordAPI instance
func (c *YClients) NewUserRecordAPI(companyID int64) *UserRecordAPI {
	return &UserRecordAPI{
		client:    c,
		companyID: companyID,
	}
}
type UserRecord struct {
	ID       int64 `json:"id"` // ID записи
	Services []struct {
		ID           int64     `json:"id"`                      // Идентификатор услуги
		Title        string  `json:"title"`                   // Название услуги
		Cost         float64 `json:"cost"`                    // Стоимость услуги
		PriceMin     float64 `json:"price_min"`               // Минимальная цена услуги
		PriceMax     float64 `json:"price_max"`               // Максимальная цена услуги
		Discount     float64 `json:"discount"`                // Скидка
		Amount       int     `json:"amount"`                  // Количество заказанных услуг
		SeanceLength int     `json:"seance_length,omitempty"` // Длительность оказания услуги в секундах (только если задан фильтр по сотруднику)
	} `json:"services"` // Список ID услуг записи
	Company struct {
		ID                         int64      `json:"id"`                             // Идентификатор компании
		Title                      string   `json:"title"`                          // Название компании
		CountryID                  int64      `json:"country_id"`                     // Идентификатор страны, в которой расположена компания
		Country                    string   `json:"country"`                        // Название страны компании
		CityID                     int64      `json:"city_id"`                        // Идентификатор города, в котором расположена компания
		City                       string   `json:"city"`                           // Название города компании
		Timezone                   string   `json:"timezone"`                       // Timezone компании
		Address                    string   `json:"address"`                        // Адрес, по которому расположена компания
		Phone                      string   `json:"phone"`                          // Основной номер телефона компании
		Phones                     []string `json:"phones"`                         // Все номера телефонов компании
		CoordinateLat              float64  `json:"coordinate_lat"`                 // Широта, на которой расположена компания
		CoordinateLng              float64  `json:"coordinate_lng"`                 // Долгота
		AllowDeleteRecord          bool     `json:"allow_delete_record"`            // Можно ли удалять запись
		AllowChangeRecord          bool     `json:"allow_change_record"`            // Можно ли переносить запись
		Site                       string   `json:"site"`                           // Сайт компании
		CurrencyShortTitle         string   `json:"currency_short_title"`           // Символ валюты
		AllowChangeRecordDelayStep int      `json:"allow_change_record_delay_step"` // Время, через которое можно переносить запись
		AllowDeleteRecordDelayStep int      `json:"allow_delete_record_delay_step"` // Время, через которое можно удалять запись
	} `json:"company"` // Параметры компании
	Staff struct {
		ID             int64             `json:"id"`             // Идентификатор сотрудника
		Name           string          `json:"name"`           // Имя сотрудника
		Specialization string          `json:"specialization"` // Специализация сотрудника
		Position       json.RawMessage `json:"position"`       // Должность сотрудника
		ShowRating     int             `json:"show_rating"`    // Показывать ли рейтинг сотрудника (1 - показывать, 0 - не показывать)
		Rating         float64         `json:"rating"`         // Рейтинг сотрудника
		VotesCount     int             `json:"votes_count"`    // Кол-во голосов, оценивших сотрудника
		CommentsCount  int             `json:"comments_count"` // Кол-во комментариев к сотруднику
		Avatar         string          `json:"avatar"`         // Путь к файлу аватарки сотрудника
	} `json:"staff"` // Параметры специалиста, которые был забронирован
	ClientsCount     int     `json:"clients_count"`               // Количество клиентов
	Date             string  `json:"date"`                        // Дата сеанса
	DateTime         string  `json:"datetime"`                    // Дата сеанса в формате
	CreateDate       string  `json:"create_date"`                 // Дата создания записи
	Length           int     `json:"length"`                      // Длительность сеанса
	Deleted          bool    `json:"deleted"`                     // Удалена ли запись (true если удалена)
	NotifyBySMS      *int    `json:"notify_by_sms,omitempty"`     // За какое кол-во часов отправить смс напоминание о записи (0 если не нужно напоминать)
	NotifyByEmail    *int    `json:"notify_by_email,omitempty"`   // За какое кол-во часов отправить email напоминание о записи (0 если не нужно напоминать)
	Comment          string  `json:"comment"`                     // Комментарий к записи
	MasterRequested  bool    `json:"master_requested"`            // Был ли указан определенный специалист при записи (false если был указан "не имеет значения")
	Online           bool    `json:"online"`                      // Запись онлайновая или нет (false если запись внес администратор)
	VisitAttendance  int     `json:"visit_attendance"`            // 2 - Пользователь подтвердил запись, 1 - Пользователь пришел, услуги оказаны, 0 - ожидание пользователя, -1 - пользователь не пришел на визит
	APIID            *string `json:"api_id,omitempty"`            // Внешний идентификатор записи
	LastChangeDate   *string `json:"last_change_date,omitempty"`  // Дата последнего редактирования записи
	Prepaid          *bool   `json:"prepaid,omitempty"`           // Доступна ли онлайн-оплата для записи
	PrepaidConfirmed *bool   `json:"prepaid_confirmed,omitempty"` // Статус online-оплаты
	ActivityID       *int    `json:"activity_id,omitempty"`       // ID чего групповой записи
}

func (c *UserRecordAPI) GetByID(recordID int64) ([]*UserRecord, error) {
	resp := make([]*UserRecord, 0)
	if err := c.client.SendRequest(fmt.Sprintf("%s/user/records/%d", apiVersionEndpoint, recordID), "GET", nil, nil, &resp, nil); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *UserRecordAPI) GetByHash(recordHash string) ([]*UserRecord, error) {
	resp := make([]*UserRecord, 0)
	if err := c.client.SendRequest(fmt.Sprintf("%s/user/records/%s", apiVersionEndpoint, recordHash), "GET", nil, nil, &resp, nil); err != nil {
		return nil, err
	}
	return resp, nil
}

// DeleteByID user should be authed
func (c *UserRecordAPI) DeleteByID(recordID int64) error {
	if err := c.client.SendRequest(fmt.Sprintf("%s/user/records/%d", apiVersionEndpoint, recordID), "DELETE", nil, nil, nil, nil); err != nil {
		return err
	}
	return nil
}

// DeleteByHash user can be unauthed
func (c *UserRecordAPI) DeleteByHash(recordHash string) error {
	if err := c.client.SendRequest(fmt.Sprintf("%s/user/records/%s", apiVersionEndpoint, recordHash), "DELETE", nil, nil, nil, nil); err != nil {
		return err
	}
	return nil
}
