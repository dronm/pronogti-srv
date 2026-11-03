package goyclients

var authEndpoint = apiVersionEndpoint + "/auth"

type User struct {
	ID               int64    `json:"id"`
	UserToken        string `json:"user_token"`
	Name             string `json:"name"`
	Phone            string `json:"phone"`
	Login            string `json:"login"`
	Email            string `json:"Email"`
	Avatar           string `json:"avatar"`
	IsApproved       bool   `json:"is_approved"`
	IsEmailConfirmed bool   `json:"is_email_confirmed"`
}

type Auth struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

func (c *YClients) Auth(auth *Auth) error {
	c.User = User{}
	if err := c.SendRequest(authEndpoint, "POST", nil, auth, &c.User, nil); err != nil {
		return err
	}
	return nil
}
