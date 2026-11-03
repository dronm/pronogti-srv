package goyclients

import (
	"fmt"
	"net/url"
	"strconv"
)

// https://developers.yclients.com/ru/#tag/Uslugi/operation/%D0%A1%D0%BE%D0%B7%D0%B4%D0%B0%D1%82%D1%8C%20%D1%83%D1%81%D0%BB%D1%83%D0%B3%D1%83

type ServiceAPI struct {
	client    *YClients
	companyID int64
}

func (c *YClients) NewServiceAPI(companyID int64) *ServiceAPI {
	return &ServiceAPI{
		client:    c,
		companyID: companyID,
	}
}

type Service struct {
	ID           int64   `json:"id"`
	CategoryID   int64   `json:"category_id"`
	Title        string  `json:"title"`
	PriceMin     float64 `json:"price_min"`
	PriceMax     float64 `json:"price_max"`
	Duration     int     `json:"duration"`
	ServiceType  int     `json:"service_type"`
	Comment      string  `json:"comment"`
	APIServiceID int64   `json:"api_service_id"`
	Weight       float64 `json:"weight"`
	Staff        []struct {
		ID           int64 `json:"id"`
		SeanceLength int   `json:"seance_length"`
	} `json:"staff"`
}

type ServiceFilter struct {
	StaffID    int64
	CategoryID int64
}

func (filter *ServiceFilter) ToURLValues() url.Values {
	values := url.Values{}

	if filter == nil {
		return values;
	}

	if filter.StaffID != 0 {
		values.Add("staff_id", strconv.FormatInt(filter.StaffID, 10))
	}

	if filter.CategoryID != 0 {
		values.Add("category_id", strconv.FormatInt(filter.CategoryID, 10))
	}

	return values
}

// Get returns a list of services or one specific service.
func (c *ServiceAPI) Get(id *int64, filter *ServiceFilter) ([]*Service, error) {
	resp := make([]*Service, 0)

	endpoint := fmt.Sprintf("%s/company/%d/services", apiVersionEndpoint, c.companyID)
	if id != nil {
		endpoint = fmt.Sprintf("%s/company/%d/services/%d", apiVersionEndpoint, c.companyID, *id)
	}

	if err := c.client.SendRequest(
		endpoint,
		"GET",
		filter.ToURLValues(),
		nil,
		&resp,
		nil,
	); err != nil {
		return nil, err
	}

	return resp, nil
}
