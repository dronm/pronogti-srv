package goyclients

import (
	"encoding/json"
	"fmt"
)

// https://developers.yclients.com/ru/#tag/Finansovye-tranzakcii

type FinTransaction struct {
	ID             int64             `json:"id"`          // number ID транзакции
	DocumentID     int64             `json:"document_id"` // number ID документа
	Expense        *CatalogRef     `json:"expense"`     // Тип платежа
	Date           APIDateTime     `json:"date"`        // Дата создания транзакции
	Amount         float32         `json:"amount"`      // Сумма транзакции
	Comment        string          `json:"comment"`     // Комментарий
	Master         json.RawMessage `json:"master"`      // Сотрудник
	Supplier       json.RawMessage `json:"supplier"`    // Контрагент
	Account        *Account        `json:"account"`     // Касса
	Client         *CatalogRef     `json:"client"`      // Клиент
	LastChangeDate APIDateTime     `json:"last_change_date"`
	RecordID       int64             `json:"record_id"`      // Идентификатор записи
	VisitID        int64             `json:"visit_id"`       // Идентификатор визита
	SoldItemID     int64             `json:"sold_item_id"`   // Идентификатор проданного товара/услуги
	SoldItemType   string          `json:"sold_item_type"` // Тип сущности продаджи (товар/услуга)
}

type FinTransactionFilter struct {
	Page       int     `json:"page,omitempty"`        // Номер страницы, default 1
	Count      int     `json:"count,omitempty"`       // Количество клиентов на странице, default 50
	AccountID  int64     `json:"account_id,omitempty"`  // ID кассы, default =0
	SupplierID int64     `json:"supplier_id,omitempty"` // ID контрагента
	ClientID   int64     `json:"client_id,omitempty"`   // ID клиента
	UserID     int64     `json:"user_id,omitempty"`     // ID пользователя
	MasterID   int64     `json:"master_id,omitempty"`   // ID сотрудника
	Type       string  `json:"type,omitempty"`        // тип транзакции
	RealMoney  bool    `json:"real_money,omitempty"`  // транзакция реальными деньгами
	Deleted    bool    `json:"deleted,omitempty"`     // была ли удалена транзакция
	StartDate  APIDate `json:"start_date"`            // дата начала периода
	EndDate    APIDate `json:"end_date"`              // дата окончания периода
	BalanceIs  int     `json:"balance_is,omitempty"`  // 0 - любой баланс, 1 - положительный, 2 - оттрицательный
	DocumentID int64     `json:"document_id,omitempty"` // идентификатор документа
}

func (c *YClients) Transactions(companyID int64, filter FinTransactionFilter) ([]*FinTransaction, error) {
	resp := make([]*FinTransaction, 0)
	if err := c.SendRequest(fmt.Sprintf("%s/transactions/%d", apiVersionEndpoint, companyID), "GET", nil, filter, &resp, nil); err != nil {
		return nil, err
	}
	return resp, nil
}
