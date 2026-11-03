package goyclients

import (
	"fmt"
	"net/url"
	"strconv"
)

// Компании
// Для работы с компаниями используются следующие методы

type CompanyAPI struct {
	client *YClients
}

func (c *YClients) NewCompanyAPI() *CompanyAPI {
	return &CompanyAPI{
		client: c,
	}
}

type Company struct {
	ID                 int64       `json:"id"`                   // "1050",
	Tile               string      `json:"title"`                // "Ля Визаж",
	ShortDescr         string      `json:"short_descr"`          // "Салон красоты",
	PublicTitle        string      `json:"public_title"`         // "La Visage Beauty",
	Logo               string      `json:"logo"`                 // "https://yclients.com/images/no-master.png",
	Active             int         `json:"active"`               // "1",
	Phone              string      `json:"phone"`                // "+7 495 509-24-46",
	Phones             []string    `json:"phones"`               // ["+7 495 509-24-46"],
	Email              string      `json:"email"`                // "info@example.com",
	CountryID          int         `json:"country_id"`           //: 1,
	Schedule           string      `json:"schedule"`             // "",
	Country            string      `json:"country"`              // "Россия",
	CityID             int         `json:"city_id"`              // 1,
	City               string      `json:"city"`                 // "Москва",
	TimezoneName       string      `json:"timezone_name"`        // "Europe/Moscow",
	Address            string      `json:"address"`              // "Талалихина, д. 1, к. 2",
	CoordinateLat      float64     `json:"coordinate_lat"`       // "55.735662",
	CoordinateLon      float64     `json:"coordinate_lon"`       // "37.678218",
	PhoneConfirmation  bool        `json:"phone_confirmation"`   // true,
	ActiveStaffCount   int         `json:"active_staff_count"`   // 2,
	NextSlot           APIDateTime `json:"next_slot"`            // "2023-03-23T10:10:00+0300",
	AppIos             string      `json:"app_ios"`              // "",
	AppAndroid         string      `json:"app_android"`          // "",
	CurrencyShortTitle string      `json:"currency_short_title"` // "р",
	GroupPriority      int         `json:"group_priority"`       // 900
}

type CompanyFilter struct {
	ID     int64 `json:"id,omitempty"`
	My     int   `json:"my,omitempty"`
	Active bool  `json:"active,omitempty"`
	CityID int64 `json:"city_id,omitempty"`
	Page   int   `json:"page,omitempty"`
	Count  int   `json:"count,omitempty"`
}

func (f CompanyFilter) ToURLValues() url.Values {
	values := url.Values{}

	if f.ID != 0 {
		values.Add("id", strconv.FormatInt(f.ID, 10))
	}
	if f.My != 0 {
		values.Add("my", strconv.Itoa(f.My))
	}
	if f.Active {
		values.Add("active", "1")
	}
	if f.CityID != 0 {
		values.Add("city_id", strconv.FormatInt(f.CityID, 10))
	}
	if f.Page != 0 {
		values.Add("page", strconv.Itoa(f.Page))
	}
	if f.Count != 0 {
		values.Add("count", strconv.Itoa(f.Count))
	}

	return values
}

// GetList retrieves companies collection.
func (c *CompanyAPI) GetList(filter *CompanyFilter) ([]*Company, error) {
	var values url.Values
	if filter != nil {
		values = filter.ToURLValues()
	}

	resp := make([]*Company, 0)
	if err := c.client.SendRequest(apiVersionEndpoint+"/companies", "GET", values, nil, &resp, nil); err != nil {
		return nil, err
	}

	return resp, nil
}

// GetAll retrieves all companies matching the filter across paged API responses.
func (c *CompanyAPI) GetAll(filter *CompanyFilter) ([]*Company, error) {
	pageSize := 100
	baseFilter := CompanyFilter{}
	if filter != nil {
		baseFilter = *filter
		if baseFilter.Count > 0 {
			pageSize = baseFilter.Count
		}
	}

	if pageSize <= 0 {
		pageSize = 100
	}

	companies := make([]*Company, 0)
	seen := make(map[int64]struct{})

	for page := 1; ; page++ {
		pageFilter := baseFilter
		pageFilter.Page = page
		pageFilter.Count = pageSize

		part, err := c.GetList(&pageFilter)
		if err != nil {
			return nil, err
		}

		newItems := 0
		for _, company := range part {
			if company == nil {
				continue
			}

			if company.ID == 0 {
				companies = append(companies, company)
				newItems++
				continue
			}

			if _, ok := seen[company.ID]; ok {
				continue
			}

			seen[company.ID] = struct{}{}
			companies = append(companies, company)
			newItems++
		}

		if len(part) < pageSize || newItems == 0 {
			break
		}
	}

	return companies, nil
}

// GetMyCompanies retrieves all companies available to the authenticated user.
func (c *CompanyAPI) GetMyCompanies() ([]*Company, error) {
	return c.GetAll(&CompanyFilter{
		My:    1,
		Count: 100,
	})
}

// Get retrieves one company data
func (c *CompanyAPI) Get(id int64) ([]*Company, error) {
	resp := make([]*Company, 0)
	if err := c.client.SendRequest(fmt.Sprintf("%s/company/%d", apiVersionEndpoint, id), "GET", nil, nil, &resp, nil); err != nil {
		return nil, err
	}
	return resp, nil
}

// missing methods: Delete, Update, Add
