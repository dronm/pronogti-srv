package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/dronm/goyclients"
)

const goodsListKey = "GoodsListKey"

func (s *YClientService) FetchGoodsList(
	ctx context.Context,
	compID int64,
	searchTerm string,
) ([]*goyclients.Good, error) {
	cacheKey := makeGoodsListCacheKey(compID)

	valJSON, exists, valid := s.cache.Get(cacheKey)
	if exists && valid {
		var val []*goyclients.Good

		err := json.Unmarshal(valJSON, &val)
		if err == nil {
			return filterAndSortGoods(val, searchTerm), nil
		}

		slog.Error("decode cached goods", "error", err)
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

	api := s.yClient.Client.NewGoodsAPI(compID)

	result, err := api.GetAll(nil)
	if err != nil {
		return nil, err
	}

	resJSON, err := json.Marshal(result)
	if err == nil {
		s.cache.Set(cacheKey, resJSON)
	} else {
		slog.Error("encode cached goods", "error", err)
	}

	return filterAndSortGoods(result, searchTerm), nil
}

func makeGoodsListCacheKey(compID int64) string {
	return fmt.Sprintf("%s:%d", goodsListKey, compID)
}

func filterAndSortGoods(
	goods []*goyclients.Good,
	searchTerm string,
) []*goyclients.Good {
	searchWords := splitGoodsSearchWords(searchTerm)

	result := make([]*goyclients.Good, 0, len(goods))

	for _, good := range goods {
		if good == nil {
			continue
		}

		if good.Cost <= 0 {
			continue
		}

		if !goodMatchesSearch(good, searchWords) {
			continue
		}

		result = append(result, good)
	}

	sort.SliceStable(result, func(i int, j int) bool {
		return strings.ToLower(result[i].Title) < strings.ToLower(result[j].Title)
	})

	return result
}

func splitGoodsSearchWords(searchTerm string) []string {
	parts := strings.Fields(strings.ToLower(strings.TrimSpace(searchTerm)))
	words := make([]string, 0, len(parts))

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		words = append(words, part)
	}

	return words
}

func goodMatchesSearch(
	good *goyclients.Good,
	searchWords []string,
) bool {
	if len(searchWords) == 0 {
		return true
	}

	title := strings.ToLower(good.Title)

	for _, word := range searchWords {
		if !strings.Contains(title, word) {
			return false
		}
	}

	return true
}
