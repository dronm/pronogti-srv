package services

import (
	"context"
	"fmt"
	"math/big"

	"github.com/dronm/pronogti/internal/checkout"
	"github.com/dronm/webapp"
)

// ValidatePayable rechecks existing unpaid visit lines immediately before a new
// QR registration. Catalog prices stay frozen by the unexpired checkout. This
// read cannot lock out independent payments made by salon staff in YClients.
func (p *CheckoutProvider) ValidatePayable(ctx context.Context, order checkout.Order) error {
	if order.VisitID == 0 {
		return nil
	}
	visit, err := p.LoadVisit(ctx, order.Owner.CompanyID, order.Owner.ClientID)
	if err != nil {
		return err
	}
	return validatePayableVisit(order, visit)
}

func validatePayableVisit(order checkout.Order, visit checkout.Visit) error {
	conflict := func() error {
		return webapp.Conflict("the unpaid visit has changed; prepare a new checkout", map[string]any{"code": "checkout_visit_changed"})
	}
	if visit.VisitID != order.VisitID || visit.RecordID != order.RecordID {
		return conflict()
	}
	lines := make(map[string]checkout.VisitLine)
	key := func(kind string, id, lineID, staffID int64) string {
		return fmt.Sprintf("%s:%d:%d:%d", kind, id, lineID, staffID)
	}
	for _, line := range visit.Items {
		id := key(line.Kind, line.ID, line.LineID, line.StaffID)
		if _, exists := lines[id]; exists {
			return conflict()
		}
		lines[id] = line
	}
	selectedVisitLines := 0
	for _, item := range order.Items {
		if item.Source != checkout.SourceVisit {
			continue
		}
		selectedVisitLines++
		line, exists := lines[key(item.Kind, item.ID, item.LineID, item.StaffID)]
		if !exists || line.AmountKopecks != item.AmountKopecks {
			return conflict()
		}
		left, okLeft := new(big.Rat).SetString(line.Quantity)
		right, okRight := new(big.Rat).SetString(item.Quantity)
		if !okLeft || !okRight || left.Cmp(right) != 0 {
			return conflict()
		}
	}
	// The documented account-payment endpoint allocates at document level. A
	// subset amount cannot guarantee that the customer's selected lines were paid.
	if selectedVisitLines == 0 || selectedVisitLines != len(visit.Items) {
		return conflict()
	}
	return nil
}
