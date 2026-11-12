package services

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/paytochkabank"
	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/pronogti/internal/payment"
	"github.com/dronm/webapp"
)

type PaymentProfileDiscoveryInput struct {
	ID           int64
	CustomerCode string
}

type PaymentProfileDiscoveryCandidate struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
}

type PaymentProfileDiscovery struct {
	Companies          []PaymentProfileDiscoveryCandidate `json:"companies"`
	CustomerCode       string                             `json:"customer_code,omitempty"`
	SBPMerchants       []PaymentProfileDiscoveryCandidate `json:"sbp_merchants"`
	AcquiringMerchants []PaymentProfileDiscoveryCandidate `json:"acquiring_merchants"`
	Issues             map[string]string                  `json:"issues"`
}

var discoveryBankBIC = regexp.MustCompile(`^[0-9]{9}$`)

func (s *PaymentProfileService) Discover(ctx context.Context, input PaymentProfileDiscoveryInput) (PaymentProfileDiscovery, error) {
	if err := s.requirePaymentProfileManagement(); err != nil {
		return PaymentProfileDiscovery{}, err
	}
	input.CustomerCode = strings.TrimSpace(input.CustomerCode)
	if input.ID <= 0 || input.CustomerCode != "" && !validDiscoveryCustomerCode(input.CustomerCode) {
		return PaymentProfileDiscovery{}, webapp.BadRequest("Некорректный платёжный профиль или код компании Точки.", nil)
	}
	profile, err := s.readPaymentProfileDiscovery(ctx, input.ID)
	if err != nil {
		return PaymentProfileDiscovery{}, err
	}
	if profile.Environment == string(models.PaymentProfileEnvironmentMock) {
		return PaymentProfileDiscovery{}, webapp.NewHTTPError(http.StatusConflict, "payment_discovery_mock_unsupported",
			"Справочники Точки доступны в песочнице или рабочей среде.", nil)
	}
	if !validAdminPaymentEndpoint(models.PaymentProfileEnvironment(profile.Environment), profile.APIBaseURL) ||
		profile.HTTPTimeoutSeconds < 1 || profile.HTTPTimeoutSeconds > 300 {
		return PaymentProfileDiscovery{}, webapp.NewHTTPError(http.StatusConflict, "payment_discovery_endpoint_invalid",
			"Проверьте сохранённый адрес API Точки и тайм-аут платёжного профиля.", nil)
	}
	token, err := paymentProfileTokens(ctx).Token(ctx, profile)
	if err != nil {
		return PaymentProfileDiscovery{}, webapp.NewHTTPError(http.StatusConflict, "payment_discovery_credential_unavailable",
			"Сохраните JWT Точки и проверьте доступность ключа шифрования на сервере.", nil)
	}
	client, err := paytochkabank.NewClient(token, paytochkabank.WithBaseURL(profile.APIBaseURL),
		paytochkabank.WithHTTPClient(&http.Client{Timeout: time.Duration(profile.HTTPTimeoutSeconds) * time.Second}))
	if err != nil {
		return PaymentProfileDiscovery{}, webapp.NewHTTPError(http.StatusConflict, "payment_discovery_client_invalid",
			"Проверьте сохранённые настройки подключения к Точке.", nil)
	}
	return discoverPaymentProfile(ctx, client, profile, input.CustomerCode)
}

func (s *PaymentProfileService) readPaymentProfileDiscovery(ctx context.Context, id int64) (payment.Profile, error) {
	conn, release, err := s.paymentProfileConn(ctx)
	if err != nil {
		return payment.Profile{}, err
	}
	defer release()
	var profile payment.Profile
	err = conn.QueryRow(ctx, `SELECT id,company_id,environment,api_base_url,http_timeout_seconds,
		COALESCE(customer_code,''),COALESCE(account_id,''),COALESCE(jwt_env,''),jwt_ciphertext
		FROM public.payment_profiles WHERE id=$1`, id).Scan(
		&profile.ID, &profile.CompanyID, &profile.Environment, &profile.APIBaseURL, &profile.HTTPTimeoutSeconds,
		&profile.CustomerCode, &profile.AccountID, &profile.JWTEnv, &profile.JWTCiphertext)
	if errors.Is(err, ds.ErrNoRows) {
		return payment.Profile{}, webapp.NotFound("payment profile not found", nil)
	}
	if err != nil {
		return payment.Profile{}, fmt.Errorf("read payment profile for discovery: %w", err)
	}
	return profile, nil
}

func discoverPaymentProfile(ctx context.Context, client *paytochkabank.Client, profile payment.Profile, requestedCode string) (PaymentProfileDiscovery, error) {
	result := PaymentProfileDiscovery{
		Companies:          []PaymentProfileDiscoveryCandidate{},
		SBPMerchants:       []PaymentProfileDiscoveryCandidate{},
		AcquiringMerchants: []PaymentProfileDiscoveryCandidate{},
		Issues:             make(map[string]string),
	}
	customers, err := client.ListCustomers(ctx)
	if err != nil {
		if paymentDiscoveryIssue(err) == "permission_denied" {
			return result, webapp.NewHTTPError(http.StatusBadGateway, "payment_discovery_customers_permission_denied",
				"Точка не предоставила доступ к списку компаний. Проверьте JWT и разрешение ReadCustomerData.", nil)
		}
		return result, webapp.NewHTTPError(http.StatusBadGateway, "payment_discovery_customers_failed",
			"Не удалось получить список компаний из Точки.", nil)
	}
	for _, customer := range customers {
		if customer.CustomerType != "Business" || !validDiscoveryCustomerCode(customer.CustomerCode) {
			continue
		}
		detail := strings.TrimSpace(customer.TaxCode)
		if detail != "" {
			detail = "ИНН " + detail
		}
		result.Companies = append(result.Companies, PaymentProfileDiscoveryCandidate{
			ID: customer.CustomerCode, Name: discoveryName(customer.ShortName, customer.FullName, customer.CustomerCode), Detail: detail,
		})
	}
	result.Companies = distinctDiscoveryCandidates(result.Companies)
	containsCode := func(code string) bool {
		return slices.ContainsFunc(result.Companies, func(candidate PaymentProfileDiscoveryCandidate) bool { return candidate.ID == code })
	}
	switch {
	case requestedCode != "":
		if !containsCode(requestedCode) {
			return result, webapp.BadRequest("Выбранная компания отсутствует в списке доступных компаний Точки.", nil)
		}
		result.CustomerCode = requestedCode
	case containsCode(profile.CustomerCode):
		result.CustomerCode = profile.CustomerCode
	case len(result.Companies) == 1:
		result.CustomerCode = result.Companies[0].ID
	case len(result.Companies) == 0:
		result.Issues["customer_code"] = "no_eligible_results"
		return result, nil
	default:
		result.Issues["customer_code"] = "selection_required"
		return result, nil
	}

	var wait sync.WaitGroup
	var sbpIssue, acquiringIssue string
	wait.Go(func() {
		var candidates []PaymentProfileDiscoveryCandidate
		candidates, sbpIssue = discoverSBPMerchants(ctx, client, profile, result.CustomerCode)
		result.SBPMerchants = append(result.SBPMerchants, candidates...)
	})
	wait.Go(func() {
		var candidates []PaymentProfileDiscoveryCandidate
		candidates, acquiringIssue = discoverAcquiringMerchants(ctx, client, result.CustomerCode)
		result.AcquiringMerchants = append(result.AcquiringMerchants, candidates...)
	})
	wait.Wait()
	if sbpIssue != "" {
		result.Issues["sbp_merchant_id"] = sbpIssue
	}
	if acquiringIssue != "" {
		result.Issues["acquiring_merchant_id"] = acquiringIssue
	}
	return result, nil
}

func discoverSBPMerchants(ctx context.Context, client *paytochkabank.Client, profile payment.Profile, customerCode string) ([]PaymentProfileDiscoveryCandidate, string) {
	bankBIC := payment.TochkaBankBIC
	if _, explicitBIC, ok := strings.Cut(profile.AccountID, "/"); ok && discoveryBankBIC.MatchString(explicitBIC) {
		bankBIC = explicitBIC
	}
	info, err := client.SBP.GetCustomerInfo(ctx, customerCode, bankBIC)
	if err != nil {
		var bankErr *paytochkabank.APIError
		if errors.As(err, &bankErr) && bankErr.StatusCode == http.StatusNotFound {
			return nil, "not_registered"
		}
		return nil, paymentDiscoveryIssue(err)
	}
	if info == nil || info.LegalID == "" {
		return nil, "not_registered"
	}
	if info.Status != "Active" {
		return nil, "no_eligible_results"
	}
	// Tochka's sandbox returns fixed examples. A live response must identify
	// the same customer and bank when it supplies those fields.
	if profile.Environment == string(models.PaymentProfileEnvironmentProduction) &&
		(info.CustomerCode != "" && info.CustomerCode != customerCode || info.BankCode != "" && info.BankCode != bankBIC) {
		return nil, "lookup_failed"
	}
	merchants, err := client.SBP.ListMerchants(ctx, info.LegalID)
	if err != nil {
		return nil, paymentDiscoveryIssue(err)
	}
	candidates := make([]PaymentProfileDiscoveryCandidate, 0, len(merchants))
	for _, merchant := range merchants {
		if merchant.Status != "Active" || !supportsDynamicDiscoveryQR(merchant.Capabilities) ||
			!validPaymentProfilePathPart(merchant.MerchantID) {
			continue
		}
		if profile.Environment == string(models.PaymentProfileEnvironmentProduction) &&
			merchant.LegalID != "" && merchant.LegalID != info.LegalID {
			continue
		}
		candidates = append(candidates, PaymentProfileDiscoveryCandidate{
			ID: merchant.MerchantID, Name: discoveryName(merchant.BrandName, merchant.MerchantID),
			Detail: strings.Trim(strings.Join([]string{strings.TrimSpace(merchant.City), strings.TrimSpace(merchant.Address)}, ", "), ", "),
		})
	}
	return discoveryMerchantResult(candidates)
}

func supportsDynamicDiscoveryQR(capabilities string) bool {
	switch capabilities {
	case "010", "011", "110", "111":
		return true
	default:
		return false
	}
}

func discoverAcquiringMerchants(ctx context.Context, client *paytochkabank.Client, customerCode string) ([]PaymentProfileDiscoveryCandidate, string) {
	retailers, err := client.Acquiring.ListRetailers(ctx, customerCode)
	if err != nil {
		return nil, paymentDiscoveryIssue(err)
	}
	candidates := make([]PaymentProfileDiscoveryCandidate, 0, len(retailers))
	for _, retailer := range retailers {
		if retailer.Status != "REG" || !retailer.IsActive || !slices.Contains(retailer.PaymentModes, "card") ||
			!paymentAcquiringMerchantID.MatchString(retailer.MerchantID) {
			continue
		}
		candidates = append(candidates, PaymentProfileDiscoveryCandidate{
			ID: retailer.MerchantID, Name: discoveryName(retailer.Name, retailer.MerchantID), Detail: strings.TrimSpace(retailer.URL),
		})
	}
	return discoveryMerchantResult(candidates)
}

func discoveryMerchantResult(candidates []PaymentProfileDiscoveryCandidate) ([]PaymentProfileDiscoveryCandidate, string) {
	candidates = distinctDiscoveryCandidates(candidates)
	switch len(candidates) {
	case 0:
		return candidates, "no_eligible_results"
	case 1:
		return candidates, ""
	default:
		return candidates, "selection_required"
	}
}

func distinctDiscoveryCandidates(candidates []PaymentProfileDiscoveryCandidate) []PaymentProfileDiscoveryCandidate {
	seen := make(map[string]struct{}, len(candidates))
	distinct := candidates[:0]
	for _, candidate := range candidates {
		if _, exists := seen[candidate.ID]; !exists {
			distinct = append(distinct, candidate)
			seen[candidate.ID] = struct{}{}
		}
	}
	return distinct
}

func discoveryName(values ...string) string {
	for _, value := range values {
		if value := strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func validDiscoveryCustomerCode(value string) bool {
	return len(value) <= 9 && validPaymentProfilePathPart(value)
}

func paymentDiscoveryIssue(err error) string {
	var bankErr *paytochkabank.APIError
	if errors.As(err, &bankErr) && (bankErr.StatusCode == http.StatusForbidden || bankErr.StatusCode == http.StatusUnauthorized) {
		return "permission_denied"
	}
	return "lookup_failed"
}
