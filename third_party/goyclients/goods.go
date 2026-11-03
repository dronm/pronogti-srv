package goyclients

import (
	"fmt"
	"net/url"
	"strconv"
)

type GoodsAPI struct {
	client    *YClients
	companyID int64
}

func (c *YClients) NewGoodsAPI(companyID int64) *GoodsAPI {
	return &GoodsAPI{
		client:    c,
		companyID: companyID,
	}
}

type Good struct {
	ID             int64   `json:"id,omitempty"`
	GoodID         int64   `json:"good_id"`
	Title          string  `json:"title"`
	Value          string  `json:"value"`
	Label          string  `json:"label"`
	Article        string  `json:"article"`
	Category       string  `json:"category"`
	CategoryID     int64   `json:"category_id"`
	SalonID        int64   `json:"salon_id"`
	Cost           float64 `json:"cost"`
	UnitID         int64   `json:"unit_id"`
	UnitShortTitle string  `json:"unit_short_title"`
}

func (g Good) ProductID() int64 {
	if g.GoodID != 0 {
		return g.GoodID
	}

	return g.ID
}

type GoodsFilter struct {
	Term          string `json:"term,omitempty"`
	Page          int    `json:"page,omitempty"`
	Count         int    `json:"count,omitempty"`
	CategoryID    int64  `json:"category_id,omitempty"`
	ChangedAfter  string `json:"changed_after,omitempty"`
	ChangedBefore string `json:"changed_before,omitempty"`
}

func (f GoodsFilter) ToURLValues() url.Values {
	values := url.Values{}

	if f.Term != "" {
		values.Add("term", f.Term)
	}
	if f.Page != 0 {
		values.Add("page", strconv.Itoa(f.Page))
	}
	if f.Count != 0 {
		values.Add("count", strconv.Itoa(f.Count))
	}
	if f.CategoryID != 0 {
		values.Add("category_id", strconv.FormatInt(f.CategoryID, 10))
	}
	if f.ChangedAfter != "" {
		values.Add("changed_after", f.ChangedAfter)
	}
	if f.ChangedBefore != "" {
		values.Add("changed_before", f.ChangedBefore)
	}

	return values
}

func (c *GoodsAPI) GetList(filter *GoodsFilter) ([]*Good, error) {
	if c.companyID == 0 {
		return nil, fmt.Errorf("company id should be set")
	}

	var values url.Values
	if filter != nil {
		values = filter.ToURLValues()
	}

	resp := make([]*Good, 0)
	if err := c.client.SendRequest(
		fmt.Sprintf("%s/goods/%d", apiVersionEndpoint, c.companyID),
		"GET",
		values,
		nil,
		&resp,
		nil,
	); err != nil {
		return nil, err
	}

	return resp, nil
}

func (c *GoodsAPI) GetByID(id int64) (*Good, error) {
	if c.companyID == 0 {
		return nil, fmt.Errorf("company id should be set")
	}
	if id == 0 {
		return nil, fmt.Errorf("good id should be set")
	}

	resp := &Good{}
	if err := c.client.SendRequest(
		fmt.Sprintf("%s/goods/%d/%d", apiVersionEndpoint, c.companyID, id),
		"GET",
		nil,
		nil,
		resp,
		nil,
	); err != nil {
		return nil, err
	}

	return resp, nil
}

func (c *GoodsAPI) GetAll(filter *GoodsFilter) ([]*Good, error) {
	pageSize := 100
	baseFilter := GoodsFilter{}
	if filter != nil {
		baseFilter = *filter
		if baseFilter.Count > 0 {
			pageSize = baseFilter.Count
		}
	}

	if pageSize <= 0 {
		pageSize = 100
	}

	goods := make([]*Good, 0)
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
		for _, good := range part {
			if good == nil {
				continue
			}

			productID := good.ProductID()
			if productID == 0 {
				goods = append(goods, good)
				newItems++
				continue
			}

			if _, ok := seen[productID]; ok {
				continue
			}

			seen[productID] = struct{}{}
			goods = append(goods, good)
			newItems++
		}

		if len(part) < pageSize || newItems == 0 {
			break
		}
	}

	return goods, nil
}
