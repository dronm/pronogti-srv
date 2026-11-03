package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/dronm/goyclients"
)

const staffListKey = "StaffListKey"

func (s *YClientService) FetchStaffList(ctx context.Context, compID int64) ([]goyclients.Staff, error) {
	cacheKey := makeStaffListCacheKey(compID)
	valJSON, exists, valid := s.cache.Get(cacheKey)
	if exists && valid {
		var val []goyclients.Staff
		err := json.Unmarshal(valJSON, &val)
		if err == nil {
			return val, nil //hit
		}
		slog.Error("decode cached staff", "error", err)
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

	// add filter: only working staff
	staff, err := s.yClient.Client.StaffAll(compID)
	if err != nil {
		return nil, err
	}
	result := []goyclients.Staff{}
	for _, p := range staff {
		if p == nil || p.Fired == 1 || p.Status == 1 || p.Hidden == 1 {
			continue
		}
		result = append(result, *p)
	}

	resJSON, err := json.Marshal(result)
	if err == nil {
		s.cache.Set(cacheKey, resJSON)
	} else {
		slog.Error("encode cached staff", "error", err)
	}

	return result, nil
}

func makeStaffListCacheKey(compID int64) string {
	return fmt.Sprintf("%s:%d", staffListKey, compID)
}
