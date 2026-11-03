package goyclients

type City struct {
	ID        int64    `json:"id"`
	CountryID int64    `json:"country_id"`
	Tile      string `json:"title"`
}

type CityFilter struct {
	ID        int64 `json:"id,omitempty"`
	CountryID int64 `json:"country_id"`
	CompanyID int64 `json:"company_id"`
}

func (c *YClients) Cities(filter *CityFilter) ([]*City, error) {
	resp := make([]*City, 0)
	if err := c.SendRequest(apiVersionEndpoint+"/cities", "GET", nil, filter, &resp, nil); err != nil {
		return nil, err
	}
	return resp, nil
}

