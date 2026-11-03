package goyclients

import (
	"fmt"
)

// https://developers.yclients.com/ru/#tag/Sotrudniki/operation/%D0%9F%D0%BE%D0%BB%D1%83%D1%87%D0%B8%D1%82%D1%8C%20%D1%81%D0%BF%D0%B8%D1%81%D0%BE%D0%BA%20%D1%81%D0%BE%D1%82%D1%80%D1%83%D0%B4%D0%BD%D0%B8%D0%BA%D0%BE%D0%B2%20/%20%D0%BA%D0%BE%D0%BD%D0%BA%D1%80%D0%B5%D1%82%D0%BD%D0%BE%D0%B3%D0%BE%20%D1%81%D0%BE%D1%82%D1%80%D1%83%D0%B4%D0%BD%D0%B8%D0%BA%D0%B0

type StaffPosition struct {
	ID    int64    `json:"id"`
	Title string `json:"title"`
}

type Staff struct {
	ID             int64           `json:"id"`
	Name           string        `json:"name"`
	Specialization string        `json:"specialization"`
	Position       StaffPosition `json:"position"`
	Avatar         string        `json:"avatar"`
	AvatarBig      string        `json:"avatar_big"`
	Fired          int           `json:"fired"`  // 1 -fired
	Status         int           `json:"status"` // 1- deleted
	Hidden         int           `json:"hidden"` // 1- hidden for online subscribtion
	ShowRating     int           `json:"show_rating"`
	Rating         float32       `json:"rating"`
	VotesCount     int           `json:"votes_count"`
	CommentsCount  int           `json:"comments_count"`
}

func (c *YClients) StaffAll(companyID int64) ([]*Staff, error) {
	resp := make([]*Staff, 0)
	if err := c.SendRequest(fmt.Sprintf("%s/company/%d/staff", apiVersionEndpoint, companyID), "GET", nil, nil, &resp, nil); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *YClients) StaffByID(companyID, staffID int64) (*Staff, error) {
	resp := &Staff{}
	if err := c.SendRequest(fmt.Sprintf("%s/company/%d/staff/%d", apiVersionEndpoint, companyID, staffID), "GET", nil, nil, resp, nil); err != nil {
		return nil, err
	}
	return resp, nil
}
