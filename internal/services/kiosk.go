package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/dronm/ds/v4"
	"github.com/dronm/modelbind"
	"github.com/dronm/pronogti/internal/kiosk"
	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/webapp"
	wmodels "github.com/dronm/webapp/models"
)

const kioskDetailSQL = `SELECT id,name,company_id,is_active,payment_profile_id,fiscal_profile_id,
	yclients_profile_id,session_version,created_at,updated_at FROM public.kiosks
	WHERE id=$1 AND archived_at IS NULL`

func (s *KioskService) Create(ctx context.Context, input modelbind.ModelInput[*models.Kiosk]) (map[string]any, error) {
	if err := s.requireManagement(); err != nil {
		return nil, err
	}
	if input.Model == nil {
		return nil, webapp.BadRequest("kiosk details are required", nil)
	}
	item := *input.Model
	item.Name = strings.TrimSpace(item.Name)
	if input.IsAbsent("is_active") {
		item.IsActive = true
	}
	token, err := kiosk.GenerateToken()
	if err != nil {
		return nil, err
	}
	conn, release, err := s.managementConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var id int64
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		if err := validateKioskConfiguration(ctx, tx, item, true); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `INSERT INTO public.kiosks
			(name,company_id,is_active,payment_profile_id,fiscal_profile_id,yclients_profile_id,token_hash)
			VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`, item.Name, item.CompanyID, item.IsActive,
			item.PaymentProfileID, item.FiscalProfileID, item.YclientsProfileID, kiosk.HashToken(token)).Scan(&id)
		if err != nil {
			return fmt.Errorf("insert kiosk: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "token": token}, nil
}

func (s *KioskService) Detail(ctx context.Context, id int64) (*models.Kiosk, error) {
	if err := s.requireManagement(); err != nil {
		return nil, err
	}
	if id <= 0 {
		return nil, webapp.BadRequest("kiosk id is required", nil)
	}
	conn, release, err := s.managementConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return fetchKioskConfiguration(ctx, conn, id, false)
}

func (s *KioskService) Update(ctx context.Context, input webapp.UpdateByKeysInput[*models.KioskKey, *models.Kiosk]) (wmodels.RowsAffectedResponse, error) {
	var response wmodels.RowsAffectedResponse
	if err := s.requireManagement(); err != nil {
		return response, err
	}
	if input.Keys == nil || input.Keys.ID <= 0 {
		return response, webapp.BadRequest("kiosk id is required", nil)
	}
	if input.Input.Model == nil {
		return response, webapp.BadRequest("kiosk details are required", nil)
	}
	conn, release, err := s.managementConnection(ctx)
	if err != nil {
		return response, err
	}
	defer release()
	id := input.Keys.ID
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		current, err := fetchKioskConfiguration(ctx, tx, id, true)
		if err != nil {
			return err
		}
		next := mergeKioskConfiguration(*current, input.Input)
		companyChanged := next.CompanyID != current.CompanyID
		if err := validateKioskConfiguration(ctx, tx, next, companyChanged); err != nil {
			return err
		}
		var newHash *string
		if companyChanged {
			// A previously provisioned device must never bind itself to a new salon.
			token, err := kiosk.GenerateToken()
			if err != nil {
				return err
			}
			digest := kiosk.HashToken(token)
			newHash = &digest
		}
		result, err := tx.Exec(ctx, `UPDATE public.kiosks SET
			name=$2,company_id=$3,is_active=$4,payment_profile_id=$5,fiscal_profile_id=$6,yclients_profile_id=$7,
			token_hash=COALESCE($8,token_hash) WHERE id=$1`, id, next.Name, next.CompanyID, next.IsActive,
			next.PaymentProfileID, next.FiscalProfileID, next.YclientsProfileID, newHash)
		if err != nil {
			return fmt.Errorf("update kiosk: %w", err)
		}
		response.RowsAffected = result.RowsAffected()
		return nil
	})
	if err != nil {
		return wmodels.RowsAffectedResponse{}, err
	}
	return response, nil
}

func (s *KioskService) Delete(ctx context.Context, id int64) (wmodels.RowsAffectedResponse, error) {
	if err := s.requireManagement(); err != nil {
		return wmodels.RowsAffectedResponse{}, err
	}
	if id <= 0 {
		return wmodels.RowsAffectedResponse{}, webapp.BadRequest("kiosk id is required", nil)
	}
	if err := kiosk.NewPostgresRegistry(s.DB).Archive(ctx, id); err != nil {
		return wmodels.RowsAffectedResponse{}, kioskManagementError(id, err)
	}
	return wmodels.RowsAffectedResponse{RowsAffected: 1}, nil
}

func (s *KioskService) RotateToken(ctx context.Context, id int64) (map[string]any, error) {
	if err := s.requireManagement(); err != nil {
		return nil, err
	}
	if id <= 0 {
		return nil, webapp.BadRequest("kiosk id is required", nil)
	}
	token, err := kiosk.NewPostgresRegistry(s.DB).Rotate(ctx, id)
	if err != nil {
		return nil, kioskManagementError(id, err)
	}
	return map[string]any{"id": id, "token": token}, nil
}

func (s *KioskService) requireManagement() error {
	if err := s.requireSession(); err != nil {
		return err
	}
	return s.requireDB()
}

func (s *KioskService) managementConnection(ctx context.Context) (ds.Conn, func(), error) {
	lease, server, err := s.DB.GetPrimary(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire kiosk database: %w", err)
	}
	return lease.Conn(), func() { s.DB.Release(lease, server) }, nil
}

func fetchKioskConfiguration(ctx context.Context, db ds.Querier, id int64, lock bool) (*models.Kiosk, error) {
	query := kioskDetailSQL
	if lock {
		query += ` FOR UPDATE`
	}
	var item models.Kiosk
	err := db.QueryRow(ctx, query, id).Scan(&item.ID, &item.Name, &item.CompanyID, &item.IsActive,
		&item.PaymentProfileID, &item.FiscalProfileID, &item.YclientsProfileID,
		&item.SessionVersion, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, ds.ErrNoRows) {
		return nil, kioskManagementError(id, kiosk.ErrKioskNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("read kiosk configuration: %w", err)
	}
	return &item, nil
}

func mergeKioskConfiguration(current models.Kiosk, input modelbind.ModelInput[*models.Kiosk]) models.Kiosk {
	patch := input.Model
	if input.IsPresent("name") {
		current.Name = strings.TrimSpace(patch.Name)
	}
	if input.IsPresent("company_id") && patch.CompanyID != current.CompanyID {
		current.CompanyID = patch.CompanyID
		current.PaymentProfileID = nil
		current.FiscalProfileID = nil
		current.YclientsProfileID = nil
	}
	if input.IsPresent("is_active") {
		current.IsActive = patch.IsActive
	}
	if input.IsPresent("payment_profile_id") {
		current.PaymentProfileID = patch.PaymentProfileID
	}
	if input.IsPresent("fiscal_profile_id") {
		current.FiscalProfileID = patch.FiscalProfileID
	}
	if input.IsPresent("yclients_profile_id") {
		current.YclientsProfileID = patch.YclientsProfileID
	}
	return current
}

func validateKioskConfiguration(ctx context.Context, tx ds.Tx, item models.Kiosk, checkSalon bool) error {
	if item.Name == "" || utf8.RuneCountInString(item.Name) > 200 {
		return invalidKioskFields(map[string]string{"name": "Введите название киоска длиной до 200 символов."})
	}
	if item.CompanyID <= 0 {
		return invalidKioskFields(map[string]string{"company_id": "Выберите салон."})
	}
	if checkSalon {
		var id int64
		err := tx.QueryRow(ctx, `SELECT company_id FROM public.salons WHERE company_id=$1 FOR KEY SHARE`, item.CompanyID).Scan(&id)
		if errors.Is(err, ds.ErrNoRows) {
			return invalidKioskFields(map[string]string{"company_id": "Выберите существующий салон."})
		}
		if err != nil {
			return fmt.Errorf("check kiosk salon: %w", err)
		}
	}
	var paymentValid, fiscalValid, yclientsValid bool
	err := tx.QueryRow(ctx, `SELECT
		$2::bigint IS NULL OR EXISTS (SELECT 1 FROM public.payment_profiles WHERE id=$2 AND company_id=$1),
		$3::bigint IS NULL OR EXISTS (SELECT 1 FROM public.fiscal_profiles WHERE id=$3 AND company_id=$1),
		$4::bigint IS NULL OR EXISTS (SELECT 1 FROM public.yclients_profiles WHERE id=$4 AND company_id=$1)`,
		item.CompanyID, item.PaymentProfileID, item.FiscalProfileID, item.YclientsProfileID).Scan(&paymentValid, &fiscalValid, &yclientsValid)
	if err != nil {
		return fmt.Errorf("check kiosk profiles: %w", err)
	}
	fieldErrors := map[string]string{}
	if !paymentValid {
		fieldErrors["payment_profile_id"] = "Выберите платёжный профиль этого салона."
	}
	if !fiscalValid {
		fieldErrors["fiscal_profile_id"] = "Выберите фискальный профиль этого салона."
	}
	if !yclientsValid {
		fieldErrors["yclients_profile_id"] = "Выберите профиль YClients этого салона."
	}
	if len(fieldErrors) > 0 {
		return invalidKioskFields(fieldErrors)
	}
	return nil
}

func invalidKioskFields(fields map[string]string) error {
	return webapp.BadRequest("Некорректные настройки киоска.", map[string]any{"field_errors": fields})
}

func kioskManagementError(id int64, err error) error {
	if errors.Is(err, kiosk.ErrKioskNotFound) {
		return webapp.NotFound("kiosk not found", map[string]any{"id": id})
	}
	return err
}
