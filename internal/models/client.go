package models

import "time"

type ClientKey struct {
	ID int64 `json:"id"`
}

type Client struct {
	Name      *string    `json:"name"`
	Tel       *string    `json:"tel"`
	Email     *string    `json:"email"`
	BirthDate *time.Time `json:"birth_date"`
}

type ClientUpdateReq struct {
	Keys  ClientKey `json:"keys"`
	Model Client    `json:"model"`
}

type NewClient struct {
	Name      *string    `form:"name" json:"name"`
	Phone     string     `form:"phone" json:"phone" required:"true" min:"1"`
	Email     *string    `form:"email" json:"email"`
	BirthDate *time.Time `form:"birth_date" json:"birth_date"`
}
