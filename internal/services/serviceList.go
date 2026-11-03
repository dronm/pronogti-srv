package services

import (
	"context"
	"log/slog"
	"sort"
	"strings"

	"github.com/dronm/goyclients"
)

func (s *YClientService) FetchServiceList(
	ctx context.Context,
	compID int64,
	staffID *int64,
	catID *int64,
	searchTerm string,
) ([]*goyclients.Service, error) {
	if err := s.yClient.auth(ctx); err != nil {
		return nil, err
	}

	release, err := s.yClient.lockClient(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	api := s.yClient.Client.NewServiceAPI(compID)

	var filter *goyclients.ServiceFilter
	if staffID != nil {
		filter = &goyclients.ServiceFilter{
			StaffID: *staffID,
		}
	}

	if catID != nil {
		if filter == nil {
			filter = &goyclients.ServiceFilter{}
		}
		filter.CategoryID = *catID
	}
	if filter != nil {
		slog.Debug("fetch YClients services", "filter", *filter)
	}
	result, err := api.Get(nil, filter)
	if err != nil {
		return nil, err
	}

	return filterAndSortServices(result, searchTerm), nil
}

func filterAndSortServices(
	services []*goyclients.Service,
	searchTerm string,
) []*goyclients.Service {
	result := make([]*goyclients.Service, 0, len(services))

	searchWords := splitSearchWords(searchTerm)

	for _, service := range services {
		if service == nil {
			continue
		}

		if !serviceMatchesSearch(service, searchWords) {
			continue
		}

		result = append(result, service)
	}

	sort.SliceStable(result, func(i int, j int) bool {
		return strings.ToLower(result[i].Title) < strings.ToLower(result[j].Title)
	})

	return result
}

func splitSearchWords(searchTerm string) []string {
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

func serviceMatchesSearch(
	service *goyclients.Service,
	searchWords []string,
) bool {
	if len(searchWords) == 0 {
		return true
	}

	title := strings.ToLower(service.Title)

	for _, word := range searchWords {
		if !strings.Contains(title, word) {
			return false
		}
	}

	return true
}
