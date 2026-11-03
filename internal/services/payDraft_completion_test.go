package services

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dronm/pronogti/internal/settlement"
)

func TestFetchPayDraftHidesOnlyLocallyPaidVisitRecord(t *testing.T) {
	client := newTestYClient(t, nil, testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/auth") {
			return authResponse(), nil
		}
		data := `[]`
		if strings.HasSuffix(request.URL.Path, "/clients/visits/search") {
			data = `{"records":[
				{"id":102,"visit_id":55,"date":"2026-11-03T12:00:00","services":[{"id":8,"title":"Paid locally","cost_to_pay":300,"paid_sum":0}]},
				{"id":101,"visit_id":55,"date":"2026-11-03T11:00:00","services":[{"id":7,"title":"Different record","cost_to_pay":200,"paid_sum":0}]},
				{"id":100,"visit_id":54,"date":"2026-11-03T10:00:00","services":[{"id":6,"title":"Older debt","cost_to_pay":100,"paid_sum":0}]}
			],"goods_transactions":[]}`
		} else if !strings.HasSuffix(request.URL.Path, "/loyalty/client_cards/42") {
			t.Fatalf("unexpected endpoint %s", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"success":true,"data":` + data + `}`))}, nil
	}))
	service := NewYClientService(client, nil)
	service.SetPaidVisitLookup(func(ctx context.Context, companyID, clientID int64) ([]settlement.VisitReference, error) {
		if companyID != 781898 || clientID != 42 {
			t.Fatal("paid debt lookup escaped company/client scope")
		}
		return []settlement.VisitReference{{VisitID: 55, RecordID: 102}}, nil
	})
	draft, err := service.FetchPayDraft(context.Background(), 781898, 42)
	if err != nil {
		t.Fatal(err)
	}
	if draft.VisitID != 55 || draft.RecordID != 101 || len(draft.Items) != 1 || draft.Items[0].Title != "Different record" || draft.Total != 200 {
		t.Fatalf("paid record leaked or another record was hidden: %+v", draft)
	}
}

func TestFetchPayDraftDoesNotOfferDebtWhenPaidLedgerIsUnavailable(t *testing.T) {
	want := errors.New("database unavailable")
	service := NewYClientService(newTestYClient(t, nil, testHTTPClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("YCLIENTS was called after the paid ledger failed")
		return nil, nil
	})), nil)
	service.SetPaidVisitLookup(func(context.Context, int64, int64) ([]settlement.VisitReference, error) { return nil, want })
	if _, err := service.FetchPayDraft(context.Background(), 781898, 42); !errors.Is(err, want) {
		t.Fatalf("paid ledger failure was hidden: %v", err)
	}
}
