//go:build integration

package goyclients

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	testClient *YClients
)

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		_ = os.Setenv(key, value)
	}

	return scanner.Err()
}

func parseDateFromEnv(t *testing.T, envName string) APIDate {
	filterDateFromStr := os.Getenv(envName)
	d, err := time.Parse("2006-01-02", filterDateFromStr)
	if err != nil {
		t.Fatalf("time.Parse(): %v", err)
	}
	return APIDate(d)
}

func TestMain(m *testing.M) {
	if err := loadDotEnv(".env"); err != nil && !os.IsNotExist(err) {
		fmt.Printf("loadDotEnv():%v\n", err)
		os.Exit(m.Run())
	}

	partnerToken := os.Getenv("YCLIENTS_PARTNER_TOKEN")
	if partnerToken == "" {
		os.Exit(m.Run())
	}

	IsDebug = true
	candidate := NewYClients(
		partnerToken,
		&http.Client{Timeout: 10 * time.Second},
	)

	login := os.Getenv("LOGIN")
	if login == "" {
		fmt.Printf("LOGIN not set in .env\n")
		os.Exit(m.Run())
	}
	password := os.Getenv("PASSWORD")
	if password == "" {
		fmt.Printf("PASSWORD not set in .env\n")
		os.Exit(m.Run())
	}

	auth := Auth{Login: login, Password: password}
	err := candidate.Auth(&auth)
	if err != nil {
		fmt.Printf("auth():%v\n", err)
		os.Exit(m.Run())
	}

	if candidate.User.UserToken == "" {
		fmt.Printf("user token not received\n")
		os.Exit(m.Run())
	}

	testClient = candidate
	os.Exit(m.Run())
}

func requireIntegrationClient(t *testing.T) {
	t.Helper()
	if testClient == nil {
		t.Skip("integration credentials were not configured or authentication did not succeed")
	}
}

func TestIntegration_MyCompanies(t *testing.T) {
	requireIntegrationClient(t)
	companyAPI := testClient.NewCompanyAPI()

	filter := CompanyFilter{My: 1}
	companies, err := companyAPI.GetList(&filter)
	if err != nil {
		t.Fatalf("real API error: %v", err)
	}

	if len(companies) == 0 {
		t.Fatal("expected at least one my company")
	}

	t.Logf("received %d my companies", len(companies))
	t.Logf("companies:%v", companies)
}

func TestIntegration_CompaniesList(t *testing.T) {
	requireIntegrationClient(t)
	companyAPI := testClient.NewCompanyAPI()

	companies, err := companyAPI.GetList(nil)
	if err != nil {
		t.Fatalf("real API error: %v", err)
	}

	if len(companies) == 0 {
		t.Fatal("expected at least one company")
	}

	t.Logf("received %d companies", len(companies))
}

func TestIntegration_City(t *testing.T) {
	requireIntegrationClient(t)
	data, err := testClient.Cities(nil)
	if err != nil {
		t.Fatalf("real API error: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("expected at least one city")
	}

	t.Logf("received %d cities", len(data))
}

func TestIntegration_StaffAll(t *testing.T) {
	requireIntegrationClient(t)
	compIDStr := os.Getenv("COMPANY_ID")
	compID, err := strconv.ParseInt(compIDStr, 10, 64)
	if err != nil {
		t.Fatalf("can not convert compony ID to int")
	}
	data, err := testClient.StaffAll(compID)
	if err != nil {
		t.Fatalf("real API error: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("expected at least one staff person")
	}

	t.Logf("received %d staff", len(data))
}

func TestIntegration_StaffOne(t *testing.T) {
	requireIntegrationClient(t)
	compIDStr := os.Getenv("COMPANY_ID")
	compID, err := strconv.ParseInt(compIDStr, 10, 64)
	if err != nil {
		t.Fatalf("can not convert compony ID to int")
	}
	staffIDStr := os.Getenv("STAFF_ID")
	staffID, err := strconv.ParseInt(staffIDStr, 10, 64)
	if err != nil {
		t.Fatalf("can not convert staff ID to int")
	}
	data, err := testClient.StaffByID(compID, staffID)
	if err != nil {
		t.Fatalf("real API error: %v", err)
	}

	if data == nil {
		t.Fatal("expected person data, got none")
	}
}

func TestIntegration_FinTransaction(t *testing.T) {
	requireIntegrationClient(t)
	compIDStr := os.Getenv("COMPANY_ID")
	compID, err := strconv.ParseInt(compIDStr, 10, 64)
	if err != nil {
		t.Fatalf("can not convert compony ID to int")
	}

	filterDateFrom := parseDateFromEnv(t, "DATE_START")
	filterDateTo := parseDateFromEnv(t, "DATE_END")
	filter := FinTransactionFilter{
		StartDate: filterDateFrom,
		EndDate:   filterDateTo,
	}

	data, err := testClient.Transactions(compID, filter)
	if err != nil {
		t.Fatalf("real API error: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("expected at least one fin transaction, got none")
	}

	t.Logf("received %d transactions", len(data))
}

func TestIntegration_CompanyUsers(t *testing.T) {
	requireIntegrationClient(t)
	compIDStr := os.Getenv("COMPANY_ID")
	compID, err := strconv.ParseInt(compIDStr, 10, 64)
	if err != nil {
		t.Fatalf("can not convert compony ID to int")
	}

	data, err := testClient.CompanyUsers(compID)
	if err != nil {
		t.Fatalf("real API error: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("expected at least one user, got none")
	}

	t.Logf("received %d users", len(data))
}

func TestIntegration_GetServices(t *testing.T) {
	requireIntegrationClient(t)
	compIDStr := os.Getenv("COMPANY_ID")
	compID, err := strconv.ParseInt(compIDStr, 10, 64)
	if err != nil {
		t.Fatalf("can not convert compony ID to int")
	}
	serviceAPI := testClient.NewServiceAPI(compID)
	data, err := serviceAPI.Get(nil, nil)
	if err != nil {
		t.Fatalf("real API error: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("expected at least one record, got none")
	}

	t.Logf("received %d records", len(data))
}

func TestIntegration_GetServiceCategories(t *testing.T) {
	requireIntegrationClient(t)
	compIDStr := os.Getenv("COMPANY_ID")
	compID, err := strconv.ParseInt(compIDStr, 10, 64)
	if err != nil {
		t.Fatalf("can not convert compony ID to int")
	}
	serviceAPI := testClient.NewServiceCategoryAPI(compID)
	data, err := serviceAPI.GetList(nil, nil)
	if err != nil {
		t.Fatalf("real API error: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("expected at least one record, got none")
	}

	t.Logf("received %d records", len(data))
}

func TestIntegration_GetClients(t *testing.T) {
	requireIntegrationClient(t)
	compIDStr := os.Getenv("COMPANY_ID")
	compID, err := strconv.ParseInt(compIDStr, 10, 64)
	if err != nil {
		t.Fatalf("can not convert compony ID to int")
	}
	api := testClient.NewClientAPI(compID)
	data, err := api.Get(ClientFilter{
		Page:             1,
		PageSize:         5,
		Fields:           []ClientField{"id", "name", "phone"},
		OrderBy:          "name",
		OrderByDirection: "asc",
	})
	if err != nil {
		t.Fatalf("real API error: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("expected at least one record, got none")
	}

	t.Logf("received %d records", len(data))
}

func TestIntegration_GetClientByID(t *testing.T) {
	requireIntegrationClient(t)
	compIDStr := os.Getenv("COMPANY_ID")
	compID, err := strconv.ParseInt(compIDStr, 10, 64)
	if err != nil {
		t.Fatalf("can not convert compony ID to int")
	}

	clIDStr := os.Getenv("CLIENT_ID")
	clID, err := strconv.ParseInt(clIDStr, 10, 64)
	if err != nil {
		t.Fatalf("can not convert client ID to int")
	}
	api := testClient.NewClientAPI(compID)
	data, err := api.GetByID(clID)
	if err != nil {
		t.Fatalf("real API error: %v", err)
	}

	if data == nil {
		t.Fatal("expected to return data, got none")
	}
}

func TestIntegration_GetClientByPhone(t *testing.T) {
	requireIntegrationClient(t)
	compIDStr := os.Getenv("COMPANY_ID")
	compID, err := strconv.ParseInt(compIDStr, 10, 64)
	if err != nil {
		t.Fatalf("can not convert compony ID to int")
	}

	clPhone := os.Getenv("CLIENT_PHONE")
	filterEntry := ClientFilterEntry{
		Type: ClientFilterTypeQuickSearch,
		State: ClientFilterEntryState{
			Value: clPhone,
		},
	}

	api := testClient.NewClientAPI(compID)
	filter := ClientFilter{
		Fields:  []ClientField{ClientFieldName, ClientFieldEmail},
		Filters: []ClientFilterEntry{filterEntry},
	}
	data, err := api.Get(filter)
	if err != nil {
		t.Fatalf("real API error: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("expected to return data, got none")
	}
}

func TestIntegration_GetRecords(t *testing.T) {
	requireIntegrationClient(t)
	compIDStr := os.Getenv("COMPANY_ID")
	compID, err := strconv.ParseInt(compIDStr, 10, 64)
	if err != nil {
		t.Fatalf("can not convert compony ID to int")
	}

	clIDStr := os.Getenv("CLIENT_ID")
	clID, err := strconv.ParseInt(clIDStr, 10, 64)
	if err != nil {
		t.Fatalf("can not convert client ID to int")
	}

	//filterDateFrom := parseDateFromEnv(t, "DATE_START")
	//filterDateTo := parseDateFromEnv(t, "DATE_END")

	api := testClient.NewRecordAPI(compID)
	data, err := api.Get(&RecordFilter{
		Page:     1,
		Count:    5,
		ClientID: clID,
		//StartDate: filterDateFrom,
		//EndDate: filterDateTo,
	})
	// data, err := api.Get(nil)
	if err != nil {
		t.Fatalf("real API error: %v", err)
	}

	// if len(data) == 0 {
	// 	t.Fatal("expected at least one record, got none")
	// }

	t.Logf("received %d records", len(data))
}

func TestIntegration_GetVisit(t *testing.T) {
	requireIntegrationClient(t)
	compIDStr := os.Getenv("COMPANY_ID")
	compID, err := strconv.ParseInt(compIDStr, 10, 64)
	if err != nil {
		t.Fatalf("can not convert compony ID to int")
	}

	vIDStr := os.Getenv("VISIT_ID")
	vID, err := strconv.ParseInt(vIDStr, 10, 64)
	if err != nil {
		t.Fatalf("can not convert visit ID to int")
	}

	api := testClient.NewVisitAPI(compID)
	data, err := api.GetByID(vID)
	if err != nil {
		t.Fatalf("real API error: %v", err)
	}

	if data == nil {
		t.Fatal("expected visit, got none")
	}
}

func TestIntegration_GetClientVisits(t *testing.T) {
	requireIntegrationClient(t)
	compIDStr := os.Getenv("COMPANY_ID")
	compID, err := strconv.ParseInt(compIDStr, 10, 64)
	if err != nil {
		t.Fatalf("can not convert compony ID to int")
	}
	phone := os.Getenv("CLIENT_PHONE")
	filterDateFrom := parseDateFromEnv(t, "DATE_START")
	filterDateTo := parseDateFromEnv(t, "DATE_END")
	filter := ClientVisitFilter{
		From:        &filterDateFrom,
		To:          &filterDateTo,
		ClientPhone: &phone,
		//PaymentStatuses: []ClientVisitPaymentStatus{"not_paid", "paid_not_full"},
	}
	srv := testClient.NewClientAPI(compID)
	data, err := srv.GetVisits(filter)
	if err != nil {
		t.Fatalf("real API error: %v", err)
	}

	if len(data.Records) == 0 {
		t.Fatal("expected at least one record, got none")
	}

	t.Logf("received %d records", len(data.Records))
}

func TestIntegration_GetClientLoyaltyCards(t *testing.T) {
	requireIntegrationClient(t)
	compIDStr := os.Getenv("COMPANY_ID")
	compID, err := strconv.ParseInt(compIDStr, 10, 64)
	if err != nil {
		t.Fatalf("can not convert compony ID to int")
	}

	clientIDStr := os.Getenv("CLIENT_ID")
	clientID, err := strconv.ParseInt(clientIDStr, 10, 64)
	if err != nil {
		t.Fatalf("can not convert client ID to int")
	}
	srv := testClient.NewClientAPI(compID)
	data, err := srv.GetLoyaltyCards(clientID)
	if err != nil {
		t.Fatalf("real API error: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("expected at least one record, got none")
	}

	t.Logf("received %d records", len(data))
	t.Logf("balance %f", data[0].Balance)
}

func TestIntegration_GetGoodsAll(t *testing.T) {
	requireIntegrationClient(t)
	compIDStr := os.Getenv("COMPANY_ID")
	compID, err := strconv.ParseInt(compIDStr, 10, 64)
	if err != nil {
		t.Fatalf("can not convert compony ID to int")
	}

	srv := testClient.NewGoodsAPI(compID)
	// filter := GoodsFilter{Term: "гель"}
	data, err := srv.GetAll(nil)
	if err != nil {
		t.Fatalf("real API error: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("expected at least one record, got none")
	}

	t.Logf("received %d records", len(data))
}
