package services

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/dronm/goyclients"
)

const fetchCompaniesCacheKey = "FetchCompaniesKey"

func (s *YClientService) FetchCompanies(ctx context.Context) ([]*goyclients.Company, error) {
	valJSON, exists, valid := s.cache.Get(fetchCompaniesCacheKey)
	if exists && valid {
		var val []*goyclients.Company
		err := json.Unmarshal(valJSON, &val)
		if err == nil {
			return val, nil
		}
		slog.Error("decode cached companies", "error", err)
		//reconstruct cache
	}

	if err := s.yClient.auth(ctx); err != nil {
		return nil, err
	}

	release, err := s.yClient.lockClient(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	api := s.yClient.Client.NewCompanyAPI()
	filter := goyclients.CompanyFilter{
		My:     1,
		Active: true,
	}

	companies, err := api.GetList(&filter)
	if err != nil {
		return nil, err
	}

	resJSON, err := json.Marshal(companies)
	if err == nil {
		s.cache.Set(fetchCompaniesCacheKey, resJSON)
	} else {
		slog.Error("encode cached companies", "error", err)
	}

	return companies, nil
}
