package goyclients

import (
	"context"
	"fmt"
	"net/http"
)

// FinancialAccount is a YClients cashdesk/account, not a bank account. Type is
// 0 for cash and 1 for cashless payments, according to the accounts endpoint.
type FinancialAccount struct {
	ID      int64  `json:"id"`
	Title   string `json:"title"`
	Type    int    `json:"type"`
	Comment string `json:"comment"`
}

func (c *YClients) Accounts(companyID int64) ([]FinancialAccount, error) {
	return c.AccountsContext(context.Background(), companyID)
}

func (c *YClients) AccountsContext(ctx context.Context, companyID int64) ([]FinancialAccount, error) {
	if companyID <= 0 {
		return nil, fmt.Errorf("company id must be positive")
	}
	var accounts []FinancialAccount
	if err := c.SendRequestContext(ctx, fmt.Sprintf("%s/accounts/%d", apiVersionEndpoint, companyID),
		http.MethodGet, nil, nil, &accounts, nil); err != nil {
		return nil, err
	}
	return accounts, nil
}
