package goyclients

import "fmt"

// https://developers.yclients.com/ru/#tag/Polzovateli-kompanii/operation/%D0%9F%D0%BE%D0%BB%D1%83%D1%87%D0%B8%D1%82%D1%8C%20%D0%BF%D0%BE%D0%BB%D1%8C%D0%B7%D0%BE%D0%B2%D0%B0%D1%82%D0%B5%D0%BB%D0%B5%D0%B9%20%D0%BA%D0%BE%D0%BC%D0%BF%D0%B0%D0%BD%D0%B8%D0%B8

type CompanyUser struct {
	ID             int64    `json:"id"`               // Id пользователя
	Name           string `json:"name"`             // Имя пользователя
	Phone          string `json:"phone"`            // Телефон пользоватя
	Email          string `json:"email"`            // Почтовый адрес пользователя
	Information    string `json:"information"`      // Информация о пользователе
	IsApproved     bool   `json:"is_approved"`      // Принял ли пользователь приглашение к управлению филиалом
	IsNonDeletable bool   `json:"is_non_deletable"` // Является ли пользователь неудаляемым
	UserRoleSlug   string `json:"user_role_slug"`   // Роль пользователя в филиале (доступно только для типа бизнеса "Красота")
}

func (c *YClients) CompanyUsers(companyID int64) ([]*CompanyUser, error) {
	resp := make([]*CompanyUser, 0)
	if err := c.SendRequest(fmt.Sprintf("%s/company/%d/users", apiVersionEndpoint, companyID), "GET", nil, nil, &resp, nil); err != nil {
		return nil, err
	}
	return resp, nil
}
