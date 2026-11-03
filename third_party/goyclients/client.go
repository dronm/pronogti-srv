package goyclients

import "fmt"

type ClientAPI struct {
	client    *YClients
	companyID int64
}

func (c *YClients) NewClientAPI(companyID int64) *ClientAPI {
	return &ClientAPI{
		client:    c,
		companyID: companyID,
	}
}

type NewClient struct {
	Name         string            `json:"name"`          // Имя клиента (обязательное поле)
	Surname      string            `json:"surname"`       // Фамилия клиента
	Patronymic   string            `json:"patronymic"`    // Отчество клиента
	Phone        string            `json:"phone"`         // Телефон клиента (обязательное поле)
	Email        string            `json:"email"`         // Email клиента
	SexID        int               `json:"sex_id"`        // Пол клиента (1 - мужской, 2 - женский, 0 - не известен)
	ImportanceID int               `json:"importance_id"` // Класс важности клиента (0 - нет, 1 - бронза, 2 - серебро, 3 - золото)
	Discount     int               `json:"discount"`      // Скидка клиента
	Card         string            `json:"card"`          // Номер карты клиента
	BirthDate    APIDate           `json:"birth_date"`    // Дата рождения клиента в формате yyyy-mm-dd
	Comment      string            `json:"comment"`       // Комментарий
	Spent        int               `json:"spent"`         // Сколько потратил средств в компании на момент добавления
	Balance      int               `json:"balance"`       // Баланс клиента
	SMSCheck     int               `json:"sms_check"`     // 1 - Поздравлять с Днем Рождения по SMS, 0 - не поздравлять
	SMSNot       int               `json:"sms_not"`       // 1 - Исключить клиента из SMS рассылок, 0 - не исключать
	Categories   map[string]string `json:"categories"`    // Массив идентификаторов категорий клиента
	CustomFields map[string]string `json:"custom_fields"` // Массив дополнительных полей клиента в виде пар "api-key": "value"
}

type AddedClient struct {
	ID           int64   `json:"id"`                   // Идентификатор клиента
	Name         string  `json:"name"`                 // Имя клиента (обязательное поле)
	Surname      string  `json:"surname,omitempty"`    // Фамилия клиента
	Patronymic   string  `json:"patronymic,omitempty"` // Отчество клиента
	Phone        string  `json:"phone"`                // Телефон клиента (обязательное поле)
	Email        string  `json:"email,omitempty"`      // Email клиента
	SexID        int     `json:"sex_id"`               // Пол клиента (1 - мужской, 2 - женский, 0 - не известен)
	ImportanceID int     `json:"importance_id"`        // Класс важности клиента (0 - нет, 1 - бронза, 2 - серебро, 3 - золото)
	Discount     int     `json:"discount"`             // Скидка клиента
	Card         string  `json:"card,omitempty"`       // Номер карты клиента
	BirthDate    APIDate `json:"birth_date"`           // Дата рождения клиента в формате yyyy-mm-dd
	Comment      string  `json:"comment,omitempty"`    // Комментарий
	Spent        int     `json:"spent,omitempty"`      // Сколько потратил средств в компании на момент добавления
	Balance      int     `json:"balance,omitempty"`    // Баланс клиента
	SMSCheck     int     `json:"sms_check"`            // 1 - Поздравлять с Днем Рождения по SMS, 0 - не поздравлять
	SMSNot       int     `json:"sms_not"`              // 1 - Исключить клиента из SMS рассылок, 0 - не исключать
}

func (c *ClientAPI) Add(client NewClient) (*AddedClient, error) {
	resp := &AddedClient{}
	if err := c.client.SendRequest(fmt.Sprintf("%s/clients/%d", apiVersionEndpoint, c.companyID), "POST", nil, &client, &resp, nil); err != nil {
		return nil, err
	}
	return resp, nil
}

type Client struct {
	ID             int64             `json:"id"`               // Идентификатор клиента
	Name           string            `json:"name"`             // Имя клиента
	Surname        string            `json:"surname"`          // Фамилия клиента
	Patronymic     string            `json:"patronymic"`       // Отчество клиента
	Phone          string            `json:"phone"`            // Телефон клиента (представлен как число)
	Email          string            `json:"email"`            // Email клиента
	Categories     []string          `json:"categories"`       // Категории клиента (представлен как массив строк)
	Sex            string            `json:"sex"`              // Пол клиента
	Discount       int               `json:"discount"`         // Скидка клиента
	Importance     string            `json:"importance"`       // Класс важности
	Card           string            `json:"card"`             // Номер карты клиента
	BirthDate      APIDate           `json:"birth_date"`       // Дата рождения (представлена как timestamp)
	Comment        string            `json:"comment"`          // Комментарий
	SMSCheck       int               `json:"sms_check"`        // 1 - Поздравлять с Днем Рождения по SMS, 0 - не поздравлять
	SMSNot         int               `json:"sms_not"`          // 1 - Исключить клиента из SMS рассылок, 0 - не исключать
	Spent          int               `json:"spent"`            // Сколько потратил средств в компании
	Balance        int               `json:"balance"`          // Баланс клиента
	Visits         int               `json:"visits"`           // Количество посещений
	LastChangeDate string            `json:"last_change_date"` // Дата последнего изменения
	CustomFields   map[string]string `json:"custom_fields"`    // Дополнительные поля (представлены как массив строк)
}

// GetByID returns one client by its ID.
// Documentation: https://developers.yclients.com/ru/#tag/Klienty/operation/%D0%9F%D0%BE%D0%BB%D1%83%D1%87%D0%B8%D1%82%D1%8C%20%D0%BA%D0%BB%D0%B8%D0%B5%D0%BD%D1%82%D0%B0
func (c *ClientAPI) GetByID(id int64) (*Client, error) {
	resp := &Client{}
	if err := c.client.SendRequest(fmt.Sprintf("%s/client/%d/%d", apiVersionEndpoint, c.companyID, id), "GET", nil, nil, &resp, nil); err != nil {
		return nil, err
	}
	return resp, nil
}

type ClientOrderField string

const (
	ClientOrderFieldID             ClientOrderField = "id"
	ClientOrderFieldName           ClientOrderField = "name"
	ClientOrderFieldEmail          ClientOrderField = "email"
	ClientOrderFieldDiscount       ClientOrderField = "discount"
	ClientOrderFieldFirstVisitDate ClientOrderField = "first_visit_date"
	ClientOrderFieldLastVisitDate  ClientOrderField = "last_visit_date"
	ClientOrderFieldSoldAmount     ClientOrderField = "sold_amount"
	ClientOrderFieldVisitCount     ClientOrderField = "visits_count"
)

type ClientField string

const (
	ClientFieldID             ClientField = "id"
	ClientFieldName           ClientField = "name"
	ClientFieldEmail          ClientField = "email"
	ClientFieldDiscount       ClientField = "discount"
	ClientFieldFirstVisitDate ClientField = "first_visit_date"
	ClientFieldLastVisitDate  ClientField = "last_visit_date"
	ClientFieldSoldAmount     ClientField = "sold_amount"
	ClientFieldVisitCount     ClientField = "visits_count"
)

type ClientFilterType string

const (
	ClientFilterTypeID                 ClientFilterType = "id"                    // int
	ClientFilterTypeSoldAmount         ClientFilterType = "sold_amount"           // range: from: float to: float
	ClientFilterTypeQuickSearch        ClientFilterType = "quick_search"          // string
	ClientFilterTypeImportance         ClientFilterType = "importance"            // int
	ClientFilterTypeHasMobileApp       ClientFilterType = "has_mobile_app"        // bool
	ClientFilterTypeCategory           ClientFilterType = "category"              // int
	ClientFilterTypeHasPassTeamCard    ClientFilterType = "has_passteam_card"     // bool
	ClientFilterTypeHasPassTeamCardIDs ClientFilterType = "has_passteam_card_ids" // string
	ClientFilterTypeBirthday           ClientFilterType = "birth_day"             // range: from: Date, to: Date
	ClientFilterTypeGender             ClientFilterType = "gender"                // int
	ClientFilterTypeRecord             ClientFilterType = "record"                // ClientFilterRecord
)

// filter types
type (
	ClientFilterID = int

	ClientFilterSoldAmount struct {
		From float64 `json:"from"`
		To   float64 `json:"to"`
	}

	ClientFilterRecord struct {
		Staff           int64 `json:"staff"`
		Service         int64 `json:"service"`
		ServiceCategory int64 `json:"service_category"`
		Status          int   `json:"status"`
	}

	ClientFilterQuickSeach         = string
	ClientFilterImportance         = int
	ClientFilterHasMobileApp       = bool
	ClientFilterCategory           = int
	ClientFilterHasPassTeamCard    = bool
	ClientFilterHasPassTeamCardIDs int
	ClientFilterBirthday           struct {
		From APIDate `json:"from"`
		To   APIDate `json:"to"`
	}
	ClientFilterGender = int
)

type ClientFilterEntryState struct {
	Value any `json:"value"`
}

type ClientFilterEntry struct {
	Type  ClientFilterType       `json:"type"`
	State ClientFilterEntryState `json:"state"`
}

type ClientFilter struct {
	Page             int                 `json:"page"`                // Номер страницы
	PageSize         int                 `json:"page_size,omitempty"` // Количество выводимых строк на странице. Максимум 200. (По умолчанию 25)
	Fields           []ClientField       `json:"fields"`              // Поля, которые нужно вернуть в ответе
	OrderBy          ClientOrderField    `json:"order_by"`            // По какому полю сортировать
	OrderByDirection string              `json:"order_by_direction"`  // Как сортировать (по возрастанию / по убыванию)
	Operation        FilterOperation     `json:"operation,omitempty"` // Тип операции
	Filters          []ClientFilterEntry `json:"filters,omitempty"`
}

// Get returns client list. A filter can be used
func (c *ClientAPI) Get(filter ClientFilter) ([]*Client, error) {
	resp := make([]*Client, 0)
	if err := c.client.SendRequest(fmt.Sprintf("%s/company/%d/clients/search", apiVersionEndpoint, c.companyID), "POST", nil, filter, &resp, nil); err != nil {
		return nil, err
	}
	return resp, nil
}

type UpdateClient struct {
	Name         string         `json:"name" required:"true"`  // Имя клиента
	Surname      string         `json:"surname"`               // Фамилия клиента
	Patronymic   string         `json:"patronymic"`            // Отчество клиента
	Phone        string         `json:"phone" required:"true"` // Телефон клиента
	Email        string         `json:"email"`                 // Email клиента
	SexID        int            `json:"sex_id"`                // Пол клиента (1 - мужской, 2 - женский, 0 - не известен)
	ImportanceID int            `json:"importance_id"`         // Класс важности клиента (0 - нет, 1 - бронза, 2 - серебро, 3 - золото)
	Discount     int            `json:"discount"`              // Скидка клиента
	Card         string         `json:"card"`                  // Номер карты клиента
	BirthDate    string         `json:"birth_date"`            // Дата рождения клиента в формате yyyy-mm-dd
	Comment      string         `json:"comment"`               // Комментарий
	Spent        int            `json:"spent"`                 // Сколько потратил средств в компании на момент добавления
	Balance      int            `json:"balance"`               // Баланс клиента
	SMSCheck     int            `json:"sms_check"`             // 1 - Поздравлять с Днем Рождения по SMS, 0 - не поздравлять
	SMSNot       int            `json:"sms_not"`               // 1 - Исключить клиента из SMS рассылок, 0 - не исключать
	Labels       map[string]any `json:"labels"`                // Массив идентификаторов категорий клиента
	CustomFields map[string]any `json:"custom_fields"`         // Массив дополнительных полей клиента в виде пар "api-key": "value"
}

func (c *ClientAPI) Update(clientID int64, client UpdateClient) error {
	if err := c.client.SendRequest(fmt.Sprintf("%s/client/%d/%d", apiVersionEndpoint, c.companyID, clientID), "PUT", nil, client, nil, nil); err != nil {
		return err
	}
	return nil
}

func (c *ClientAPI) Delete(clientID int64) error {
	if err := c.client.SendRequest(fmt.Sprintf("%s/client/%d/%d", apiVersionEndpoint, c.companyID, clientID), "DELETE", nil, nil, nil, nil); err != nil {
		return err
	}
	return nil
}
