package services

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dronm/goyclients"
	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/webapp"
)

// CheckoutProvider reads authoritative prices directly from YClients. Catalog
// and staff reads deliberately bypass the display cache used by browsing pages.
type CheckoutProvider struct{ service *YClientService }

func NewCheckoutProvider(service *YClientService) *CheckoutProvider {
	return &CheckoutProvider{service: service}
}

func (p *CheckoutProvider) LoadVisit(ctx context.Context, companyID, clientID int64) (checkout.Visit, error) {
	draft, err := p.service.FetchPayDraft(ctx, companyID, clientID)
	if err != nil {
		return checkout.Visit{}, err
	}
	return checkoutVisit(draft)
}

func (p *CheckoutProvider) LoadCatalog(ctx context.Context, companyID int64, selections []checkout.Selection) ([]checkout.CatalogItem, error) {
	if len(selections) == 0 {
		return nil, nil
	}
	if err := p.service.yClient.auth(ctx); err != nil {
		return nil, err
	}
	release, err := p.service.yClient.lockClient(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	staff, err := p.service.yClient.Client.StaffAll(companyID)
	if err != nil {
		return nil, err
	}
	staffJSON, err := json.Marshal(staff)
	if err != nil {
		return nil, fmt.Errorf("encode authoritative staff: %w", err)
	}
	activeStaff, err := checkoutStaff(staffJSON)
	if err != nil {
		return nil, err
	}
	serviceRows := make(map[int64][]checkoutCatalogRow)
	var goodsRows []checkoutCatalogRow
	goodsLoaded := false
	result := make([]checkout.CatalogItem, 0, len(selections))
	for _, selection := range selections {
		if selection.Source != checkout.SourceCatalog {
			return nil, webapp.BadRequest("catalog source is required", nil)
		}
		staffName, exists := activeStaff[selection.StaffID]
		if !exists {
			return nil, webapp.Conflict("selected staff is unavailable", nil)
		}
		var row checkoutCatalogRow
		switch selection.Kind {
		case checkout.KindService:
			rows, loaded := serviceRows[selection.StaffID]
			if !loaded {
				api := p.service.yClient.Client.NewServiceAPI(companyID)
				items, err := api.Get(nil, &goyclients.ServiceFilter{StaffID: selection.StaffID})
				if err != nil {
					return nil, err
				}
				rows, err = checkoutRows(items)
				if err != nil {
					return nil, err
				}
				serviceRows[selection.StaffID] = rows
			}
			row, err = checkoutCatalogMatch(rows, selection)
		case checkout.KindGood:
			if !goodsLoaded {
				api := p.service.yClient.Client.NewGoodsAPI(companyID)
				items, fetchErr := api.GetAll(nil)
				if fetchErr != nil {
					return nil, fetchErr
				}
				goodsRows, err = checkoutRows(items)
				if err != nil {
					return nil, err
				}
				goodsLoaded = true
			}
			row, err = checkoutCatalogMatch(goodsRows, selection)
		default:
			return nil, webapp.BadRequest("unsupported checkout item kind", nil)
		}
		if err != nil {
			return nil, err
		}
		price, err := row.fixedPrice(selection.Kind)
		if err != nil {
			return nil, err
		}
		result = append(result, checkout.CatalogItem{
			Selection: selection, Title: row.Title, StaffName: staffName, UnitPriceKopecks: price,
		})
	}
	return result, nil
}
