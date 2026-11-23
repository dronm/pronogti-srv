package services

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/dronm/ds/v4"
	"github.com/dronm/modelbind"
	"github.com/dronm/pronogti/internal/fiscal"
	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/webapp"
	wmodels "github.com/dronm/webapp/models"
)

func (s *FiscalProfileService) Create(
	ctx context.Context,
	input modelbind.ModelInput[*models.FiscalProfile],
) (map[string]any, error) {
	if err := s.requireSession(); err != nil {
		return nil, err
	}
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	if err := validateFiscalProfileCodes(input); err != nil {
		return nil, err
	}
	if err := validateFiscalProfileConnection(ctx, models.FiscalProfile{}, input); err != nil {
		return nil, err
	}

	result, err := webapp.InsertModelInput(ctx, s.DB, input, nil)
	if err != nil {
		return nil, fmt.Errorf("insert fiscal profile: %w", err)
	}
	return result, nil
}

func (s *FiscalProfileService) Update(
	ctx context.Context,
	input webapp.UpdateByKeysInput[*models.FiscalProfileKey, *models.FiscalProfile],
) (wmodels.RowsAffectedResponse, error) {
	if err := s.requireSession(); err != nil {
		return wmodels.RowsAffectedResponse{}, err
	}
	if err := s.requireDB(); err != nil {
		return wmodels.RowsAffectedResponse{}, err
	}
	if input.Keys == nil {
		return wmodels.RowsAffectedResponse{}, webapp.BadRequest("fiscal profile key is required", nil)
	}
	if input.Keys.ID <= 0 {
		return wmodels.RowsAffectedResponse{}, webapp.BadRequest("fiscal profile id is required", nil)
	}
	if err := validateFiscalProfileCodes(input.Input); err != nil {
		return wmodels.RowsAffectedResponse{}, err
	}

	var rowsAffected int64
	var err error
	if touchesFiscalProfileConnection(input.Input) {
		rowsAffected, err = s.updateFiscalProfileConnection(ctx, input)
	} else {
		rowsAffected, err = webapp.UpdateModelInput(ctx, s.DB, input.Keys, input.Input, nil)
	}
	if err != nil {
		return wmodels.RowsAffectedResponse{}, fmt.Errorf("update fiscal profile: %w", err)
	}
	if rowsAffected == 0 {
		return wmodels.RowsAffectedResponse{}, webapp.NotFound(
			"fiscal profile not found",
			map[string]any{"id": input.Keys.ID},
		)
	}
	return wmodels.RowsAffectedResponse{RowsAffected: rowsAffected}, nil
}

type fiscalDriverConnectionsKey struct{}

func WithFiscalDriverConnections(ctx context.Context, connections []fiscal.DriverConnection) context.Context {
	return context.WithValue(ctx, fiscalDriverConnectionsKey{}, connections)
}

func touchesFiscalProfileConnection(input modelbind.ModelInput[*models.FiscalProfile]) bool {
	return input.IsPresent("driver_base_url") || input.IsPresent("bearer_token_env") ||
		input.IsPresent("mode") || input.IsPresent("device_id")
}

func validateFiscalProfileConnection(ctx context.Context, previous models.FiscalProfile, input modelbind.ModelInput[*models.FiscalProfile]) error {
	proposed := previous
	if input.IsPresent("driver_base_url") {
		proposed.DriverBaseURL = input.Model.DriverBaseURL
	}
	if input.IsPresent("bearer_token_env") {
		proposed.BearerTokenEnv = input.Model.BearerTokenEnv
	}
	if input.IsPresent("mode") {
		proposed.Mode = input.Model.Mode
	}
	if input.IsPresent("device_id") {
		proposed.DeviceID = input.Model.DeviceID
	}

	oldPair := fiscalDriverConnection(previous)
	pair := fiscalDriverConnection(proposed)
	fields := map[string]string{}
	if input.IsPresent("driver_base_url") && proposed.DriverBaseURL != nil && pair.BaseURL == "" {
		fields["driver_base_url"] = "Оставьте поле пустым или укажите допустимый адрес сервиса ККТ."
	}
	if input.IsPresent("bearer_token_env") && proposed.BearerTokenEnv != nil && pair.BearerTokenEnv == "" {
		fields["bearer_token_env"] = "Оставьте поле пустым или укажите имя переменной окружения."
	}
	if (pair != oldPair || proposed.Mode != previous.Mode) && pair.BaseURL == "" && pair.BearerTokenEnv != "" {
		fields["driver_base_url"] = "Укажите адрес сервиса ККТ для выбранной переменной окружения."
	}
	if proposed.Mode == "live" {
		if pair.BaseURL == "" {
			fields["driver_base_url"] = "Укажите адрес сервиса ККТ для рабочего режима."
		}
		if proposed.DeviceID == nil || *proposed.DeviceID == "" {
			fields["device_id"] = "Укажите устройство ККТ для рабочего режима."
		}
	}
	if len(fields) == 0 && pair != oldPair && pair != (fiscal.DriverConnection{}) {
		allowed, _ := ctx.Value(fiscalDriverConnectionsKey{}).([]fiscal.DriverConnection)
		if !slices.Contains(allowed, pair) {
			fields["driver_base_url"] = "Это подключение к ККТ не разрешено настройками сервера."
			fields["bearer_token_env"] = "Используйте имя переменной, разрешённое для этого адреса ККТ."
		}
	}
	if len(fields) > 0 {
		return webapp.BadRequest("Некорректные параметры подключения к ККТ.", map[string]any{
			"field_errors": fields,
		})
	}
	return nil
}

func fiscalDriverConnection(profile models.FiscalProfile) fiscal.DriverConnection {
	var pair fiscal.DriverConnection
	if profile.DriverBaseURL != nil {
		pair.BaseURL = *profile.DriverBaseURL
	}
	if profile.BearerTokenEnv != nil {
		pair.BearerTokenEnv = *profile.BearerTokenEnv
	}
	return pair
}

func (s *FiscalProfileService) updateFiscalProfileConnection(ctx context.Context, input webapp.UpdateByKeysInput[*models.FiscalProfileKey, *models.FiscalProfile]) (int64, error) {
	lease, server, err := s.DB.GetPrimary(ctx)
	if err != nil {
		return 0, err
	}
	defer s.DB.Release(lease, server)

	var rowsAffected int64
	err = ds.WithTx(ctx, lease.Conn(), func(ctx context.Context, tx ds.Tx) error {
		var previous models.FiscalProfile
		// PATCH omits unchanged fields. Serialize the merge and write so two
		// edits cannot validate separately and commit a mixed URL/secret pair.
		err := tx.QueryRow(ctx, `SELECT driver_base_url, bearer_token_env, mode, device_id
			FROM public.fiscal_profiles WHERE id = $1 FOR UPDATE`, input.Keys.ID).
			Scan(&previous.DriverBaseURL, &previous.BearerTokenEnv, &previous.Mode, &previous.DeviceID)
		if errors.Is(err, ds.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := validateFiscalProfileConnection(ctx, previous, input.Input); err != nil {
			return err
		}
		rowsAffected, err = webapp.UpdateModelInputWithConn(ctx, fiscalUpdateConn{Tx: tx}, input.Keys, input.Input, nil)
		return err
	})
	return rowsAffected, err
}

// webapp accepts a connection for a single model write. Keep its database
// operations in the transaction that locked and validated the profile.
type fiscalUpdateConn struct{ ds.Tx }

func (fiscalUpdateConn) Prepare(context.Context, string, string) (ds.PreparedStatement, error) {
	return nil, errors.New("preparation is unavailable in the fiscal profile update transaction")
}

func (fiscalUpdateConn) Begin(context.Context) (ds.Tx, error) {
	return nil, errors.New("nested fiscal profile update transaction is unavailable")
}

func validateFiscalProfileCodes(input modelbind.ModelInput[*models.FiscalProfile]) error {
	p := input.Model
	if p == nil {
		return webapp.BadRequest("fiscal profile is required", nil)
	}

	// modelbind's enum annotation applies to strings. Keep the existing
	// numeric API and validate membership only for fields present in the input.
	fields := map[string]string{}
	if input.IsPresent("timeout_seconds") && !fiscal.ValidTimeoutSeconds(p.TimeoutSeconds) {
		fields["timeout_seconds"] = "Укажите тайм-аут от 1 до 300 секунд."
	}
	if input.IsPresent("tax_system") && !fiscal.ValidTaxSystem(p.TaxSystem) {
		fields["tax_system"] = "Выберите допустимую систему налогообложения."
	}
	if input.IsPresent("service_vat_rate") && !fiscal.ValidVATRate(p.ServiceVatRate) {
		fields["service_vat_rate"] = "Выберите допустимую ставку НДС услуг."
	}
	if input.IsPresent("good_vat_rate") && !fiscal.ValidVATRate(p.GoodVatRate) {
		fields["good_vat_rate"] = "Выберите допустимую ставку НДС товаров."
	}
	if input.IsPresent("payment_method") && !fiscal.ValidPaymentMethod(p.PaymentMethod) {
		fields["payment_method"] = "Выберите допустимый признак способа расчёта."
	}
	if input.IsPresent("time_zone") && p.TimeZone != nil && !fiscal.ValidReceiptTimeZone(*p.TimeZone) {
		fields["time_zone"] = "Выберите часовую зону от 1 до 11."
	}
	if input.IsPresent("automatic_device_number") && p.AutomaticDeviceNumber != nil && !fiscal.ValidAutomaticDeviceNumber(*p.AutomaticDeviceNumber) {
		fields["automatic_device_number"] = "Укажите номер автомата длиной до 20 символов в кодировке ККТ или оставьте поле пустым."
	}
	if len(fields) > 0 {
		return webapp.BadRequest("Некорректные параметры фискального профиля.", map[string]any{
			"field_errors": fields,
		})
	}
	return nil
}
