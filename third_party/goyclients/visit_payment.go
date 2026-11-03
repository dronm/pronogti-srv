package goyclients

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// PaymentDocumentID resolves the visit document for a particular appointment.
// The returned document ID is distinct from both visitID and recordID. Persist
// it with your checkout before submitting an externally confirmed payment.
func (c *VisitAPI) PaymentDocumentID(visitID, recordID int64) (int64, error) {
	return c.PaymentDocumentIDContext(context.Background(), visitID, recordID)
}

func (c *VisitAPI) PaymentDocumentIDContext(ctx context.Context, visitID, recordID int64) (int64, error) {
	if c == nil || c.client == nil || c.companyID <= 0 || visitID <= 0 || recordID <= 0 {
		return 0, errors.New("positive company, visit and record IDs and a client are required")
	}
	// Use a narrow read model so unrelated legacy phone, datetime or staff
	// representations cannot make an otherwise valid payment reference unreadable.
	var visit struct {
		Records []struct {
			ID        int64 `json:"id"`
			CompanyID int64 `json:"company_id"`
			Documents []struct {
				ID        int64 `json:"id"`
				TypeID    int64 `json:"type_id"`
				CompanyID int64 `json:"company_id"`
				VisitID   int64 `json:"visit_id"`
				RecordID  int64 `json:"record_id"`
			} `json:"documents"`
		} `json:"records"`
	}
	if err := c.client.SendRequestContext(ctx, fmt.Sprintf("%s/visits/%d", apiVersionEndpoint, visitID), http.MethodGet, nil, nil, &visit, nil); err != nil {
		return 0, err
	}
	var documentID int64
	matches := 0
	for _, record := range visit.Records {
		if record.ID != recordID {
			continue
		}
		matches++
		if record.CompanyID != c.companyID {
			return 0, errors.New("visit record belongs to another company")
		}
		for _, document := range record.Documents {
			if document.TypeID != 7 {
				continue
			}
			if document.ID <= 0 || document.CompanyID != c.companyID ||
				(document.VisitID != 0 && document.VisitID != visitID) ||
				(document.RecordID != 0 && document.RecordID != recordID) {
				return 0, errors.New("visit payment document has an inconsistent identity")
			}
			if documentID != 0 && documentID != document.ID {
				return 0, errors.New("visit record has multiple payment documents; select a document explicitly")
			}
			documentID = document.ID
		}
	}
	if matches != 1 || documentID == 0 {
		return 0, errors.New("a unique payment document was not found for this visit record")
	}
	return documentID, nil
}

// Pay records money already received by the caller, such as a confirmed SBP
// payment, against an existing visit's sale document. It never charges a bank,
// changes attendance, creates an appointment, or requests a fiscal receipt.
func (c *VisitAPI) Pay(visitID, recordID int64, payment AccountPayment) (Sale, error) {
	return c.PayContext(context.Background(), visitID, recordID, payment)
}

func (c *VisitAPI) PayContext(ctx context.Context, visitID, recordID int64, payment AccountPayment) (Sale, error) {
	if err := payment.Validate(); err != nil {
		return Sale{}, err
	}
	documentID, err := c.PaymentDocumentIDContext(ctx, visitID, recordID)
	if err != nil {
		return Sale{}, err
	}
	return c.client.NewSaleAPI(c.companyID).PayContext(ctx, documentID, payment)
}
