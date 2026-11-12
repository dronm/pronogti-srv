package services

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dronm/ds/v4"
	"github.com/dronm/modelbind"
	"github.com/dronm/paytochkabank"
	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/pronogti/internal/payment"
	"github.com/dronm/webapp"
	wmodels "github.com/dronm/webapp/models"
)

type paymentProfileTokensContextKey struct{}

func WithPaymentProfileTokens(ctx context.Context, tokens *payment.EnvironmentTokens) context.Context {
	return context.WithValue(ctx, paymentProfileTokensContextKey{}, tokens)
}

func paymentProfileTokens(ctx context.Context) *payment.EnvironmentTokens {
	tokens, _ := ctx.Value(paymentProfileTokensContextKey{}).(*payment.EnvironmentTokens)
	return tokens
}

type SetPaymentProfileCredentialInput struct {
	ID  int64
	JWT string
}

type PaymentProfileCredentialStatus struct {
	Configured      bool `json:"configured"`
	HasPayments     bool `json:"has_payments"`
	HasCardPayments bool `json:"has_card_payments"`
}

type paymentProfileRecord struct {
	models.PaymentProfile
	credentialConfigured bool
}

func (s *PaymentProfileService) Create(
	ctx context.Context,
	input modelbind.ModelInput[*models.PaymentProfile],
) (map[string]any, error) {
	if err := s.requirePaymentProfileManagement(); err != nil {
		return nil, err
	}
	if input.Model == nil {
		return nil, webapp.BadRequest("payment profile is required", nil)
	}
	item := mergePaymentProfile(models.PaymentProfile{
		QrTtlMinutes:       15,
		HTTPTimeoutSeconds: 20,
		CardTtlMinutes:     15,
	}, input)
	item.CompanyID = input.Model.CompanyID
	item.Environment = input.Model.Environment
	if err := validatePaymentProfile(nil, item, false); err != nil {
		return nil, err
	}

	conn, release, err := s.paymentProfileConn(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	var id int64
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		var companyID int64
		err := tx.QueryRow(ctx, `SELECT company_id FROM public.salons WHERE company_id=$1 FOR KEY SHARE`, item.CompanyID).
			Scan(&companyID)
		if errors.Is(err, ds.ErrNoRows) {
			return paymentProfileFieldError(map[string]string{"company_id": "Выберите существующий салон."})
		}
		if err != nil {
			return fmt.Errorf("check payment profile salon: %w", err)
		}
		err = tx.QueryRow(ctx, `INSERT INTO public.payment_profiles
			(company_id,name,environment,api_base_url,client_id,customer_code,sbp_merchant_id,account_id,
			 qr_ttl_minutes,http_timeout_seconds,enabled,cards_enabled,acquiring_merchant_id,card_ttl_minutes,
			 card_redirect_url,card_fail_redirect_url)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) RETURNING id`,
			item.CompanyID, item.Name, item.Environment, item.APIBaseURL, item.ClientID, item.CustomerCode,
			item.SbpMerchantID, item.AccountID, item.QrTtlMinutes, item.HTTPTimeoutSeconds, item.Enabled,
			item.CardsEnabled, item.AcquiringMerchantID, item.CardTtlMinutes, item.CardRedirectURL,
			item.CardFailRedirectURL).Scan(&id)
		if err != nil {
			return fmt.Errorf("insert payment profile: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id}, nil
}

func (s *PaymentProfileService) Update(
	ctx context.Context,
	input webapp.UpdateByKeysInput[*models.PaymentProfileKey, *models.PaymentProfile],
) (wmodels.RowsAffectedResponse, error) {
	if err := s.requirePaymentProfileManagement(); err != nil {
		return wmodels.RowsAffectedResponse{}, err
	}
	if input.Keys == nil || input.Keys.ID <= 0 || input.Input.Model == nil {
		return wmodels.RowsAffectedResponse{}, webapp.BadRequest("payment profile is required", nil)
	}
	conn, release, err := s.paymentProfileConn(ctx)
	if err != nil {
		return wmodels.RowsAffectedResponse{}, err
	}
	defer release()

	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		current, err := lockPaymentProfile(ctx, tx, input.Keys.ID)
		if err != nil {
			return err
		}
		patch := input.Input
		if patch.IsPresent("company_id") && patch.Model.CompanyID != current.CompanyID ||
			patch.IsPresent("environment") && patch.Model.Environment != current.Environment {
			return webapp.Conflict("Компания и среда сохранённого профиля закреплены. Создайте другой профиль.", nil)
		}
		next := mergePaymentProfile(current.PaymentProfile, patch)
		if next.APIBaseURL != current.APIBaseURL && current.credentialConfigured {
			return webapp.Conflict("Для другого адреса API создайте новый платёжный профиль.", nil)
		}
		identityChanged := next.APIBaseURL != current.APIBaseURL ||
			!equalProfileText(next.CustomerCode, current.CustomerCode) ||
			!equalProfileText(next.SbpMerchantID, current.SbpMerchantID) ||
			!equalProfileText(next.AccountID, current.AccountID)
		cardIdentityChanged := !equalProfileText(next.AcquiringMerchantID, current.AcquiringMerchantID)
		if identityChanged || cardIdentityChanged {
			// Checkout reserves its payment while holding a shared profile lock.
			// Check usage after acquiring the exclusive lock in a new statement.
			usage, err := readPaymentProfileCredentialStatus(ctx, tx, current.ID)
			if err != nil {
				return err
			}
			if (identityChanged && usage.HasPayments) || (cardIdentityChanged && usage.HasCardPayments) {
				return webapp.Conflict("Банковские реквизиты использованного профиля закреплены. Создайте другой профиль.", nil)
			}
		}
		if err := validatePaymentProfile(&current.PaymentProfile, next, current.credentialConfigured); err != nil {
			return err
		}
		if next.Enabled && !current.Enabled {
			if err := verifyPaymentProfileCredential(ctx, tx, current.ID, current.CompanyID); err != nil {
				return err
			}
		}

		_, err = tx.Exec(ctx, `UPDATE public.payment_profiles SET
			name=$2, api_base_url=$3, client_id=$4, customer_code=$5, sbp_merchant_id=$6, account_id=$7,
			qr_ttl_minutes=$8, http_timeout_seconds=$9, enabled=$10, cards_enabled=$11,
			acquiring_merchant_id=$12, card_ttl_minutes=$13, card_redirect_url=$14, card_fail_redirect_url=$15
			WHERE id=$1`, current.ID, next.Name, next.APIBaseURL, next.ClientID, next.CustomerCode,
			next.SbpMerchantID, next.AccountID, next.QrTtlMinutes, next.HTTPTimeoutSeconds, next.Enabled,
			next.CardsEnabled, next.AcquiringMerchantID, next.CardTtlMinutes, next.CardRedirectURL,
			next.CardFailRedirectURL)
		if err != nil {
			return fmt.Errorf("update payment profile: %w", err)
		}
		return nil
	})
	if err != nil {
		return wmodels.RowsAffectedResponse{}, err
	}
	return wmodels.RowsAffectedResponse{RowsAffected: 1}, nil
}

func (s *PaymentProfileService) Delete(ctx context.Context, id int64) (wmodels.RowsAffectedResponse, error) {
	if err := s.requirePaymentProfileManagement(); err != nil {
		return wmodels.RowsAffectedResponse{}, err
	}
	if id <= 0 {
		return wmodels.RowsAffectedResponse{}, webapp.BadRequest("payment profile id is required", nil)
	}
	conn, release, err := s.paymentProfileConn(ctx)
	if err != nil {
		return wmodels.RowsAffectedResponse{}, err
	}
	defer release()
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		current, err := lockPaymentProfile(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Enabled {
			return webapp.Conflict("Отключите платёжный профиль перед удалением.", nil)
		}
		// Foreign keys protect both current assignments and historical records.
		_, err = tx.Exec(ctx, `DELETE FROM public.payment_profiles WHERE id=$1`, id)
		var state interface{ SQLState() string }
		if errors.As(err, &state) && state.SQLState() == "23503" {
			return webapp.Conflict("Профиль назначен салону или киоску либо связан с платежами. Его можно отключить.", nil)
		}
		if err != nil {
			return fmt.Errorf("delete payment profile: %w", err)
		}
		return nil
	})
	if err != nil {
		return wmodels.RowsAffectedResponse{}, err
	}
	return wmodels.RowsAffectedResponse{RowsAffected: 1}, nil
}

func (s *PaymentProfileService) CredentialStatus(ctx context.Context, id int64) (PaymentProfileCredentialStatus, error) {
	if err := s.requirePaymentProfileManagement(); err != nil {
		return PaymentProfileCredentialStatus{}, err
	}
	if id <= 0 {
		return PaymentProfileCredentialStatus{}, webapp.BadRequest("payment profile id is required", nil)
	}
	conn, release, err := s.paymentProfileConn(ctx)
	if err != nil {
		return PaymentProfileCredentialStatus{}, err
	}
	defer release()
	return readPaymentProfileCredentialStatus(ctx, conn, id)
}

func (s *PaymentProfileService) SetCredential(
	ctx context.Context,
	input SetPaymentProfileCredentialInput,
) (PaymentProfileCredentialStatus, error) {
	if err := s.requirePaymentProfileManagement(); err != nil {
		return PaymentProfileCredentialStatus{}, err
	}
	if input.ID <= 0 {
		return PaymentProfileCredentialStatus{}, webapp.BadRequest("payment profile id is required", nil)
	}
	conn, release, err := s.paymentProfileConn(ctx)
	if err != nil {
		return PaymentProfileCredentialStatus{}, err
	}
	defer release()

	var status PaymentProfileCredentialStatus
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		current, err := lockPaymentProfile(ctx, tx, input.ID)
		if err != nil {
			return err
		}
		if !validAdminPaymentEndpoint(current.Environment, current.APIBaseURL) {
			return webapp.Conflict("Токен можно сохранить только для профиля с разрешённым адресом API.", nil)
		}
		encrypted, err := paymentProfileTokens(ctx).Encrypt(
			payment.Profile{ID: current.ID, CompanyID: current.CompanyID}, input.JWT,
		)
		if errors.Is(err, payment.ErrInvalidPaymentToken) {
			return webapp.BadRequest("Введите токен Точки без пробелов.", nil)
		}
		if err != nil {
			return webapp.NewHTTPError(http.StatusServiceUnavailable, "payment_encryption_unavailable",
				"На сервере недоступен ключ шифрования платёжных профилей.", nil)
		}
		// Environment references take precedence in the runtime resolver.
		// Clear a legacy reference atomically when installing its replacement.
		if _, err := tx.Exec(ctx, `UPDATE public.payment_profiles SET jwt_ciphertext=$2,jwt_env=NULL WHERE id=$1`,
			current.ID, encrypted); err != nil {
			return fmt.Errorf("store payment credential: %w", err)
		}
		status, err = readPaymentProfileCredentialStatus(ctx, tx, current.ID)
		return err
	})
	if err != nil {
		return PaymentProfileCredentialStatus{}, err
	}
	return status, nil
}

func (s *PaymentProfileService) requirePaymentProfileManagement() error {
	if err := s.requireSession(); err != nil {
		return err
	}
	return s.requireDB()
}

func (s *PaymentProfileService) paymentProfileConn(ctx context.Context) (ds.Conn, func(), error) {
	lease, server, err := s.DB.GetPrimary(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire payment profile database: %w", err)
	}
	return lease.Conn(), func() { s.DB.Release(lease, server) }, nil
}

func lockPaymentProfile(ctx context.Context, tx ds.Tx, id int64) (paymentProfileRecord, error) {
	var record paymentProfileRecord
	p := &record.PaymentProfile
	err := tx.QueryRow(ctx, `SELECT id,company_id,name,environment,api_base_url,client_id,
		customer_code,sbp_merchant_id,account_id,qr_ttl_minutes,http_timeout_seconds,enabled,cards_enabled,
		acquiring_merchant_id,card_ttl_minutes,card_redirect_url,card_fail_redirect_url,
		jwt_ciphertext IS NOT NULL OR jwt_env IS NOT NULL
		FROM public.payment_profiles WHERE id=$1 FOR UPDATE`, id).Scan(
		&p.ID, &p.CompanyID, &p.Name, &p.Environment, &p.APIBaseURL, &p.ClientID,
		&p.CustomerCode, &p.SbpMerchantID, &p.AccountID, &p.QrTtlMinutes, &p.HTTPTimeoutSeconds,
		&p.Enabled, &p.CardsEnabled, &p.AcquiringMerchantID, &p.CardTtlMinutes,
		&p.CardRedirectURL, &p.CardFailRedirectURL, &record.credentialConfigured)
	if errors.Is(err, ds.ErrNoRows) {
		return paymentProfileRecord{}, webapp.NotFound("payment profile not found", nil)
	}
	if err != nil {
		return paymentProfileRecord{}, fmt.Errorf("lock payment profile: %w", err)
	}
	return record, nil
}

func readPaymentProfileCredentialStatus(ctx context.Context, conn ds.Querier, id int64) (PaymentProfileCredentialStatus, error) {
	var status PaymentProfileCredentialStatus
	err := conn.QueryRow(ctx, `SELECT p.jwt_ciphertext IS NOT NULL OR p.jwt_env IS NOT NULL,
		EXISTS (SELECT 1 FROM public.payments WHERE payment_profile_id=p.id),
		EXISTS (SELECT 1 FROM public.payments WHERE payment_profile_id=p.id AND method='card')
		FROM public.payment_profiles p WHERE p.id=$1`, id).
		Scan(&status.Configured, &status.HasPayments, &status.HasCardPayments)
	if errors.Is(err, ds.ErrNoRows) {
		return PaymentProfileCredentialStatus{}, webapp.NotFound("payment profile not found", nil)
	}
	if err != nil {
		return PaymentProfileCredentialStatus{}, fmt.Errorf("read payment profile credential status: %w", err)
	}
	return status, nil
}

func verifyPaymentProfileCredential(ctx context.Context, tx ds.Tx, id, companyID int64) error {
	profile := payment.Profile{ID: id, CompanyID: companyID}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(jwt_env, ''),jwt_ciphertext FROM public.payment_profiles WHERE id=$1`, id).
		Scan(&profile.JWTEnv, &profile.JWTCiphertext); err != nil {
		return fmt.Errorf("read payment credential for activation: %w", err)
	}
	if _, err := paymentProfileTokens(ctx).Token(ctx, profile); err != nil {
		return paymentProfileFieldError(map[string]string{"enabled": "Токен недоступен. Проверьте настройки сервера или сохраните токен заново."})
	}
	return nil
}

func mergePaymentProfile(item models.PaymentProfile, input modelbind.ModelInput[*models.PaymentProfile]) models.PaymentProfile {
	patch := input.Model
	if input.IsPresent("name") {
		item.Name = strings.TrimSpace(patch.Name)
	}
	if input.IsPresent("api_base_url") {
		item.APIBaseURL = strings.TrimSpace(patch.APIBaseURL)
	}
	if input.IsPresent("client_id") {
		item.ClientID = normalizedProfileText(patch.ClientID)
	}
	if input.IsPresent("customer_code") {
		item.CustomerCode = normalizedProfileText(patch.CustomerCode)
	}
	if input.IsPresent("sbp_merchant_id") {
		item.SbpMerchantID = normalizedProfileText(patch.SbpMerchantID)
	}
	if input.IsPresent("account_id") {
		account := normalizedProfileText(patch.AccountID)
		if account == nil || payment.TochkaAccountID(profileText(account)) != payment.TochkaAccountID(profileText(item.AccountID)) {
			item.AccountID = account
		}
	}
	if input.IsPresent("qr_ttl_minutes") {
		item.QrTtlMinutes = patch.QrTtlMinutes
	}
	if input.IsPresent("http_timeout_seconds") {
		item.HTTPTimeoutSeconds = patch.HTTPTimeoutSeconds
	}
	if input.IsPresent("enabled") {
		item.Enabled = patch.Enabled
	}
	if input.IsPresent("cards_enabled") {
		item.CardsEnabled = patch.CardsEnabled
	}
	if input.IsPresent("acquiring_merchant_id") {
		item.AcquiringMerchantID = normalizedProfileText(patch.AcquiringMerchantID)
	}
	if input.IsPresent("card_ttl_minutes") {
		item.CardTtlMinutes = patch.CardTtlMinutes
	}
	if input.IsPresent("card_redirect_url") {
		item.CardRedirectURL = normalizedProfileText(patch.CardRedirectURL)
	}
	if input.IsPresent("card_fail_redirect_url") {
		item.CardFailRedirectURL = normalizedProfileText(patch.CardFailRedirectURL)
	}
	return item
}

func profileText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func equalProfileText(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func normalizedProfileText(value *string) *string {
	trimmed := strings.TrimSpace(profileText(value))
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

var (
	paymentAcquiringMerchantID = regexp.MustCompile(`^[0-9]{15}$`)
	paymentAccountNumber       = regexp.MustCompile(`^[0-9]{20}$`)
)

func validatePaymentProfile(previous *models.PaymentProfile, item models.PaymentProfile, credentialConfigured bool) error {
	fields := map[string]string{}
	if item.CompanyID <= 0 {
		fields["company_id"] = "Выберите салон."
	}
	if strings.TrimSpace(item.Name) == "" || utf8.RuneCountInString(item.Name) > 200 {
		fields["name"] = "Укажите название профиля длиной до 200 символов."
	}
	if !item.Environment.IsValid() {
		fields["environment"] = "Выберите среду."
	}
	activating := item.Enabled && (previous == nil || !previous.Enabled)
	if (previous == nil || previous.APIBaseURL != item.APIBaseURL || activating) &&
		!validAdminPaymentEndpoint(item.Environment, item.APIBaseURL) {
		fields["api_base_url"] = "Укажите разрешённый адрес API для выбранной среды."
	}
	if item.QrTtlMinutes < 1 || item.QrTtlMinutes > 129600 {
		fields["qr_ttl_minutes"] = "Укажите срок действия QR от 1 до 129600 минут."
	}
	if item.HTTPTimeoutSeconds < 1 || item.HTTPTimeoutSeconds > 300 {
		fields["http_timeout_seconds"] = "Укажите тайм-аут от 1 до 300 секунд."
	}
	if item.CardTtlMinutes < 1 || item.CardTtlMinutes > 44640 {
		fields["card_ttl_minutes"] = "Укажите срок действия ссылки от 1 до 44640 минут."
	}
	acquirer := profileText(item.AcquiringMerchantID)
	if (item.CardsEnabled || acquirer != "") && !paymentAcquiringMerchantID.MatchString(acquirer) {
		fields["acquiring_merchant_id"] = "Укажите 15-значный идентификатор эквайринга."
	}
	if item.Enabled {
		if !credentialConfigured {
			fields["enabled"] = "Сначала сохраните токен Точки, затем включите профиль."
		}
		if strings.TrimSpace(profileText(item.CustomerCode)) == "" {
			fields["customer_code"] = "Укажите код клиента Точки."
		}
		sbpChanged := previous == nil ||
			!equalProfileText(item.SbpMerchantID, previous.SbpMerchantID) || !equalProfileText(item.AccountID, previous.AccountID)
		if !item.CardsEnabled || (activating || sbpChanged) && (item.SbpMerchantID != nil || item.AccountID != nil) {
			if strings.TrimSpace(profileText(item.SbpMerchantID)) == "" {
				fields["sbp_merchant_id"] = "Укажите идентификатор СБП."
			}
			if strings.TrimSpace(profileText(item.AccountID)) == "" {
				fields["account_id"] = "Укажите 20-значный номер счёта."
			}
		}
	}
	if account := profileText(item.AccountID); account != "" && (previous == nil || account != profileText(previous.AccountID) || activating) {
		if !validPaymentProfileAccount(account) {
			fields["account_id"] = "Укажите 20-значный номер счёта или счёт и БИК через косую черту."
		}
	}
	if merchant := profileText(item.SbpMerchantID); merchant != "" &&
		(previous == nil || merchant != profileText(previous.SbpMerchantID) || activating) &&
		!validPaymentProfilePathPart(merchant) {
		fields["sbp_merchant_id"] = "Укажите допустимый идентификатор СБП."
	}
	validatePaymentProfileReturnURLs(fields, previous, item)
	if len(fields) != 0 {
		return paymentProfileFieldError(fields)
	}
	return nil
}

func validPaymentProfileAccount(value string) bool {
	number, bic, explicitBIC := strings.Cut(value, "/")
	if !explicitBIC {
		return paymentAccountNumber.MatchString(number)
	}
	return validPaymentProfilePathPart(number) && validPaymentProfilePathPart(bic)
}

func validPaymentProfilePathPart(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "/\\?#%") &&
		strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func validAdminPaymentEndpoint(environment models.PaymentProfileEnvironment, value string) bool {
	if environment == models.PaymentProfileEnvironmentProduction {
		return value == paytochkabank.ProductionURL
	}
	return payment.AllowedBankEndpoint(string(environment), value)
}

func validatePaymentProfileReturnURLs(fields map[string]string, previous *models.PaymentProfile, item models.PaymentProfile) {
	required := item.Enabled && item.CardsEnabled && item.Environment != models.PaymentProfileEnvironmentMock
	newCardUse := required && (previous == nil || !previous.Enabled || !previous.CardsEnabled)
	var previousSuccess, previousFailure string
	if previous != nil {
		previousSuccess, previousFailure = profileText(previous.CardRedirectURL), profileText(previous.CardFailRedirectURL)
	}
	for _, setting := range []struct {
		field, value, previous string
	}{
		{"card_redirect_url", profileText(item.CardRedirectURL), previousSuccess},
		{"card_fail_redirect_url", profileText(item.CardFailRedirectURL), previousFailure},
	} {
		if setting.value == setting.previous && !newCardUse {
			continue
		}
		if (setting.value != "" || required) && !validAdminCardReturnURL(item.Environment, setting.value) {
			fields[setting.field] = "Укажите допустимый URL возврата для карточного платежа."
		}
	}
}

func validAdminCardReturnURL(environment models.PaymentProfileEnvironment, value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" ||
		len(value) > 1900 || strings.ContainsAny(value, "\\#") ||
		strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	return u.Scheme == "http" && environment != models.PaymentProfileEnvironmentProduction &&
		(u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")
}

func paymentProfileFieldError(fields map[string]string) error {
	return webapp.BadRequest("Некорректные настройки платёжного профиля.", map[string]any{"field_errors": fields})
}
