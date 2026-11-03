package httpapi

import "github.com/dronm/pronogti/internal/models"

type companyRequest struct {
	CompanyID int64
}

type findClientRequest struct {
	CompanyID int64
	Phone     string
}

type payDraftRequest struct {
	CompanyID int64
	ClientID  int64
}

type clientGetAction uint8

const (
	clientGetFind clientGetAction = iota + 1
	clientGetPayDraft
)

type clientGetRequest struct {
	Action    clientGetAction
	CompanyID int64
	ClientID  int64
	Phone     string
}

type registerClientRequest struct {
	CompanyID int64
	Client    models.NewClient
}

type serviceListRequest struct {
	CompanyID  int64
	StaffID    *int64
	CategoryID *int64
	Term       string
}

type serviceCategoriesRequest struct {
	CompanyID int64
	StaffID   *int64
}

type goodsListRequest struct {
	CompanyID int64
	Term      string
}
