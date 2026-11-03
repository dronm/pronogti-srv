package goyclients

import "fmt"

type ServiceCategoryAPI struct {
	client    *YClients
	companyID int64
}

// NewServiceCategoryAPI creates a new NewServiceCategoryAPI instance
func (c *YClients) NewServiceCategoryAPI(companyID int64) *ServiceCategoryAPI {
	return &ServiceCategoryAPI{
		client:    c,
		companyID: companyID,
	}
}

type NewServiceCategory struct {
	Title  string  `json:"title"`
	APIID  string  `json:"api_id"`
	Weight int     `json:"weight"`
	Staff  []int64 `json:"staff"`
}

type ServiceCategory struct {
	ID             int64   `json:"id"`
	SalonServiceID int64   `json:"salon_service_id"`
	Title          string  `json:"title"`
	APIID          string  `json:"api_id"`
	Weight         int     `json:"weight"`
	Staff          []int64 `json:"staff"`
}

func (c *ServiceCategoryAPI) Add(service *NewServiceCategory) (*ServiceCategory, error) {
	resp := &ServiceCategory{}
	if err := c.client.SendRequest(fmt.Sprintf("%s/service_categories/%d", apiVersionEndpoint, c.companyID), "POST", nil, service, &resp, nil); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *ServiceCategoryAPI) GetByID(id int64) ([]*Service, error) {
	resp := make([]*Service, 0)
	if err := c.client.SendRequest(fmt.Sprintf("%s/service_category/%d/%d", apiVersionEndpoint, c.companyID, id), "GET", nil, nil, &resp, nil); err != nil {
		return nil, err
	}
	return resp, nil
}

type UpdateServiceCategory struct {
	Title  string  `json:"title"`
	APIID  string  `json:"api_id"`
	Weight int     `json:"weight"`
	Staff  []int64 `json:"staff"`
}

// Update modifies the existing service.
func (c *ServiceCategoryAPI) Update(id int64, service *UpdateServiceCategory) (*ServiceCategory, error) {
	resp := &ServiceCategory{}
	if err := c.client.SendRequest(fmt.Sprintf("%s/services/%d", apiVersionEndpoint, id), "PUT", nil, service, &resp, nil); err != nil {
		return nil, err
	}
	return resp, nil
}

type ServiceCategoryFilter struct {
	StaffID []int64 `json:"staff_id"`
}

// GetList retrieves all companies collection.
func (c *ServiceCategoryAPI) GetList(categoryID *int64, filter *ServiceCategoryFilter) ([]*ServiceCategory, error) {
	resp := make([]*ServiceCategory, 0)
	id := ""
	if categoryID != nil {
		id = fmt.Sprintf("%d", *categoryID)
	}
	if err := c.client.SendRequest(
		fmt.Sprintf("%s/company/%d/service_categories/%s", apiVersionEndpoint, c.companyID, id), "GET", nil, filter, &resp, nil); err != nil {
		return nil, err
	}
	return resp, nil
}
