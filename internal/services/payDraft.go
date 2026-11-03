package services

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/dronm/goyclients"
	"github.com/dronm/pronogti/internal/models"
)

// FetchPayDraft returns the latest client visit that still has unpaid services/goods,
// plus current loyalty bonus information.
func (s *YClientService) FetchPayDraft(
	ctx context.Context,
	compID,
	clientID int64,
) (models.PayDraft, error) {
	if clientID == 0 {
		return models.PayDraft{}, fmt.Errorf("client id should be set")
	}
	heldVisits := make(map[[2]int64]bool)
	if s.paidVisitLookup != nil {
		paid, err := s.paidVisitLookup(ctx, compID, clientID)
		if err != nil {
			return models.PayDraft{}, fmt.Errorf("check locally confirmed visit payments: %w", err)
		}
		for _, visit := range paid {
			heldVisits[[2]int64{visit.VisitID, visit.RecordID}] = true
		}
	}

	if err := s.yClient.auth(ctx); err != nil {
		return models.PayDraft{}, err
	}

	release, err := s.yClient.lockClient(ctx)
	if err != nil {
		return models.PayDraft{}, err
	}
	defer release()

	now := time.Now()
	from := goyclients.APIDate(now.AddDate(0, 0, -30))
	to := goyclients.APIDate(now.AddDate(0, 0, 1))

	filter := goyclients.ClientVisitFilter{
		ClientID: &clientID,
		From:     &from,
		To:       &to,
		PaymentStatuses: []goyclients.ClientVisitPaymentStatus{
			goyclients.ClientVisitPaymentStatusNotPaid,
			goyclients.ClientVisitPaymentStatusPaidNotFull,
		},
	}

	api := s.yClient.Client.NewClientAPI(compID)

	visits, err := api.GetVisits(filter)
	if err != nil {
		return models.PayDraft{}, err
	}

	loyaltyCards, err := api.GetLoyaltyCards(clientID)
	if err != nil {
		return models.PayDraft{}, err
	}

	base := models.PayDraft{
		ClientID:     clientID,
		Items:        make([]models.PayDraftItem, 0),
		BonusToApply: 0,
		BonusCanPay:  0,
		Total:        0,
	}

	for _, card := range loyaltyCards {
		base.BonusTotal += card.Balance
	}

	base.BonusTotal = roundMoney(base.BonusTotal)
	base.BonusCanPay = base.BonusTotal

	sort.SliceStable(visits.Records, func(i int, j int) bool {
		if visits.Records[i].Date == visits.Records[j].Date {
			return visits.Records[i].ID > visits.Records[j].ID
		}

		return visits.Records[i].Date > visits.Records[j].Date
	})

	for _, rec := range visits.Records {
		if heldVisits[[2]int64{rec.VisitID, rec.ID}] {
			continue
		}
		candidate := base
		candidate.DraftID = fmt.Sprintf("yclients:%d:%d:%d", clientID, rec.VisitID, rec.ID)
		candidate.VisitID = rec.VisitID
		candidate.RecordID = rec.ID
		candidate.Items = make([]models.PayDraftItem, 0, len(rec.Services))
		candidate.Total = 0
		candidate.BonusToApply = 0
		candidate.BonusCanPay = 0

		for _, service := range rec.Services {
			unpaidAmount := roundMoney(service.CostToPay - service.PaidSum)
			if unpaidAmount <= 0 {
				continue
			}

			item := models.PayDraftItem{
				ID:       service.ID,
				LineID:   0,
				Staff:    staffFromClientVisitStaff(rec.Staff),
				Title:    service.Title,
				ItemKind: models.PayDraftItemKindService,
				Quant:    1,
				Price:    unpaidAmount,
				Amount:   unpaidAmount,
			}

			candidate.Total += item.Amount
			candidate.Items = append(candidate.Items, item)
		}

		for _, goodsTransaction := range visits.GoodsTransactions {
			if goodsTransaction.RecordID != rec.ID {
				continue
			}

			master := goodsTransaction.Staff
			if master.ID == 0 {
				master = staffFromClientVisitStaff(rec.Staff)
			}

			for _, good := range goodsTransaction.Goods {
				unpaidAmount := roundMoney(good.CostToPay - good.PaidSum)
				if unpaidAmount <= 0 {
					continue
				}

				quant := good.Amount
				if quant <= 0 {
					quant = 1
				}

				price := roundMoney(unpaidAmount / quant)

				item := models.PayDraftItem{
					ID:       good.ID,
					LineID:   goodsTransaction.ID,
					Staff:    master,
					Title:    good.Title,
					ItemKind: models.PayDraftItemKindGood,
					Quant:    quant,
					Price:    price,
					Amount:   unpaidAmount,
				}

				candidate.Total += item.Amount
				candidate.Items = append(candidate.Items, item)
			}
		}

		candidate.Total = roundMoney(candidate.Total)
		candidate.BonusTotal = base.BonusTotal
		candidate.BonusToApply = 0

		if len(candidate.Items) > 0 {
			return candidate, nil
		}
	}

	return base, nil
}

func staffFromClientVisitStaff(staff goyclients.ClientVisitStaff) goyclients.Staff {
	res := goyclients.Staff{
		ID:             staff.ID,
		Name:           staff.Name,
		Specialization: staff.Specialization,
		Avatar:         staff.Avatar,
		AvatarBig:      staff.AvatarBig,
	}

	if staff.Position != nil {
		res.Position = *staff.Position
	}

	return res
}

func roundMoney(value float64) float64 {
	return math.Round(value*100) / 100
}
