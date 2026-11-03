package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/dronm/goyclients"
	"github.com/dronm/pronogti/internal/apperrors"
	"github.com/dronm/pronogti/internal/kiosk"
	"github.com/dronm/pronogti/internal/models"
	"github.com/dronm/webapp"
)

const findClientByPhoneKey = "findClientByPhoneKey"

// FindClientByPhone returns a client by his phone or error if not found.
func (s *YClientService) FindClientByPhone(ctx context.Context, compID int64, phone string) (goyclients.Client, error) {
	phone, err := kiosk.NormalizePhone(phone)
	if err != nil {
		return goyclients.Client{}, err
	}
	cacheKey := makeClientLookupCacheKey(compID, phone)
	valJSON, exists, valid := s.cache.Get(cacheKey)
	if exists && valid {
		var val goyclients.Client
		err := json.Unmarshal(valJSON, &val)
		if err == nil {
			cachedPhone, phoneErr := kiosk.NormalizePhone(val.Phone)
			if phoneErr == nil && cachedPhone == phone {
				return val, nil
			}
			s.cache.Invalidate(cacheKey)
		} else {
			slog.Error("decode cached client", "error", err)
		}
		//reconstruct cache
	}

	if err := s.yClient.auth(ctx); err != nil {
		return goyclients.Client{}, err
	}

	release, err := s.yClient.lockClient(ctx)
	if err != nil {
		return goyclients.Client{}, err
	}
	defer release()

	filterEntry := goyclients.ClientFilterEntry{
		Type: goyclients.ClientFilterTypeQuickSearch,
		State: goyclients.ClientFilterEntryState{
			Value: phone,
		},
	}

	api := s.yClient.Client.NewClientAPI(compID)
	filter := goyclients.ClientFilter{
		Fields:  []goyclients.ClientField{"id", "name", "phone", "email"},
		Filters: []goyclients.ClientFilterEntry{filterEntry},
	}

	clientModel, err := api.Get(filter)
	if err != nil {
		return goyclients.Client{}, err
	}

	matched, err := exactPhoneClient(clientModel, phone)
	if err != nil || matched.ID <= 0 {
		return matched, err
	}

	resJSON, err := json.Marshal(&matched)
	if err == nil {
		s.cache.Set(cacheKey, resJSON)
	} else {
		slog.Error("encode cached client", "error", err)
	}

	return matched, nil
}

// QuickSearch can return fuzzy matches. Only an exact canonical phone match can
// establish a customer session; ambiguous external records require resolution.
func exactPhoneClient(clients []*goyclients.Client, phone string) (goyclients.Client, error) {
	var matched goyclients.Client
	for _, candidate := range clients {
		if candidate == nil || candidate.ID <= 0 {
			continue
		}
		canonical, err := kiosk.NormalizePhone(candidate.Phone)
		if err != nil || canonical != phone {
			continue
		}
		if matched.ID > 0 && matched.ID != candidate.ID {
			return goyclients.Client{}, webapp.Conflict("multiple customer records use this phone; contact a staff member", nil)
		}
		matched = *candidate
	}
	return matched, nil
}

// UpdateClient modifies customer data in YClients.
func (s *YClientService) UpdateClient(ctx context.Context, compID int64, updateData models.ClientUpdateReq) error {
	if err := s.yClient.auth(ctx); err != nil {
		return err
	}

	release, err := s.yClient.lockClient(ctx)
	if err != nil {
		return err
	}
	defer release()

	newData := goyclients.UpdateClient{}
	if updateData.Model.Name != nil {
		newData.Name = *updateData.Model.Name
	}
	if updateData.Model.Email != nil {
		newData.Email = *updateData.Model.Email
	}
	if updateData.Model.BirthDate != nil {
		newData.BirthDate = updateData.Model.BirthDate.Format("2006-01-02")
	}

	api := s.yClient.Client.NewClientAPI(compID)
	if err := api.Update(updateData.Keys.ID, newData); err != nil {
		return fmt.Errorf("api.Update(): %w", err)
	}
	s.invalidateClientLookups(compID)

	filter := goyclients.ClientFilter{Fields: []goyclients.ClientField{"id", "name", "phone", "email"}}

	clientModel, err := api.Get(filter)
	if err != nil {
		return err
	}

	if len(clientModel) == 0 {
		return webapp.NewHTTPError(http.StatusNotFound, apperrors.ClientNotFound, "Клиент не найден", nil)
	}

	return nil
}

// AddClient adds a new client to YClients.
func (s *YClientService) AddClient(ctx context.Context, compID int64, model models.NewClient) (models.ClientKey, error) {
	if err := s.yClient.auth(ctx); err != nil {
		return models.ClientKey{}, err
	}

	release, err := s.yClient.lockClient(ctx)
	if err != nil {
		return models.ClientKey{}, err
	}
	defer release()

	newClient := goyclients.NewClient{Phone: model.Phone}
	if model.Name != nil {
		newClient.Name = *model.Name
	} else {
		newClient.Name = model.Phone
	}
	if model.Email != nil {
		newClient.Email = *model.Email
	}
	if model.BirthDate != nil {
		newClient.BirthDate = goyclients.APIDate(*model.BirthDate)
	}

	api := s.yClient.Client.NewClientAPI(compID)
	cl, err := api.Add(newClient)
	if err != nil {
		return models.ClientKey{}, fmt.Errorf("api.Add(): %w", err)
	}
	s.invalidateClientLookups(compID)

	return models.ClientKey{ID: cl.ID}, nil
}

func makeClientLookupCacheKey(compID int64, phone string) string {
	return fmt.Sprintf("%s:%d:%s", findClientByPhoneKey, compID, phone)
}

func (s *YClientService) invalidateClientLookups(compID int64) {
	prefix := makeClientLookupCacheKey(compID, "")
	for _, key := range s.cache.Keys() {
		if strings.HasPrefix(key, prefix) {
			s.cache.Invalidate(key)
		}
	}
}
