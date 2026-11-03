package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/dronm/goyclients"
)

const serviceCatListKey = "ServiceListKey"

func (s *YClientService) FetchServiceCatList(
	ctx context.Context,
	compID int64,
	staffID *int64,
) ([]*goyclients.ServiceCategory, error) {
	cacheKey := makeServiceCatListCacheKey(compID, staffID)

	valJSON, exists, valid := s.cache.Get(cacheKey)
	if exists && valid {
		var val []*goyclients.ServiceCategory

		err := json.Unmarshal(valJSON, &val)
		if err == nil {
			return val, nil
		}

		slog.Error("decode cached service categories", "error", err)
		// Reconstruct cache below.
	}

	if err := s.yClient.auth(ctx); err != nil {
		return nil, err
	}

	release, err := s.yClient.lockClient(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	api := s.yClient.Client.NewServiceCategoryAPI(compID)

	var filter *goyclients.ServiceCategoryFilter
	if staffID != nil {
		filter = &goyclients.ServiceCategoryFilter{
			StaffID: []int64{*staffID},
		}
	}

	result, err := api.GetList(nil, filter)
	if err != nil {
		return nil, err
	}

	resJSON, err := json.Marshal(result)
	if err == nil {
		s.cache.Set(cacheKey, resJSON)
	} else {
		slog.Error("encode cached service categories", "error", err)
	}

	return result, nil
}

func makeServiceCatListCacheKey(compID int64, staffID *int64) string {
	if staffID == nil {
		return fmt.Sprintf("%s:%d:staff:all", serviceCatListKey, compID)
	}

	return fmt.Sprintf("%s:%d:staff:%d", serviceCatListKey, compID, *staffID)
}
