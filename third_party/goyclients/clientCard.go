package goyclients

import "fmt"

type LoyaltyCard struct {
	ID           int64              `json:"id"`
	Balance      float64            `json:"balance"`
	Points       int64              `json:"points"`
	PaidAmount   float64            `json:"paid_amount"`
	SoldAmount   float64            `json:"sold_amount"`
	VisitsCount  int64              `json:"visits_count"`
	Number       string             `json:"number"`
	TypeID       int64              `json:"type_id"`
	SalonGroupID int64              `json:"salon_group_id"`
	Type         LoyaltyCardType     `json:"type"`
	SalonGroup   LoyaltySalonGroup   `json:"salon_group"`
	Programs     []LoyaltyProgram    `json:"programs"`
}

type LoyaltyCardType struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	SalonGroupID int64  `json:"salon_group_id"`
}

type LoyaltySalonGroup struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

type LoyaltyProgram struct {
	ID            int64              `json:"id"`
	Title         string             `json:"title"`
	Value         float64            `json:"value"`
	LoyaltyTypeID int64              `json:"loyalty_type_id"`
	ItemTypeID    int64              `json:"item_type_id"`
	ValueUnitID   int64              `json:"value_unit_id"`
	GroupID       int64              `json:"group_id"`
	LoyaltyType   LoyaltyProgramType `json:"loyalty_type"`
	Rules         []LoyaltyRule      `json:"rules"`
}

type LoyaltyProgramType struct {
	ID             int64  `json:"id"`
	Title          string `json:"title"`
	IsDiscount     bool   `json:"is_discount"`
	IsCashback     bool   `json:"is_cashback"`
	IsStatic       bool   `json:"is_static"`
	IsAccumulative bool   `json:"is_accumulative"`
}

type LoyaltyRule struct {
	ID               int64   `json:"id"`
	LoyaltyProgramID int64   `json:"loyalty_program_id"`
	LoyaltyTypeID    int64   `json:"loyalty_type_id"`
	Value            float64 `json:"value"`
	Parameter        float64 `json:"parameter"`
}

func (c *ClientAPI) GetLoyaltyCards(clientID int64) ([]LoyaltyCard, error) {
	resp := make([]LoyaltyCard, 0)
	if err := c.client.SendRequest(fmt.Sprintf("%s/loyalty/client_cards/%d", apiVersionEndpoint, clientID), "GET", nil, nil, &resp, nil); err != nil {
		return nil, err
	}
	return resp, nil
}
