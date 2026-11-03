package payment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/dronm/paytochkabank"
)

// VerifiedSBPNotification is created only after authenticating the bank's JWT.
// It is still the receiver's responsibility to match the configured environment,
// frozen merchant/customer identity, amount and one specific payment attempt.
type VerifiedSBPNotification struct {
	EventKey         string
	CustomerCode     string
	MerchantID       string
	QRCID            string
	TransactionID    string
	RefTransactionID string
	PaymentType      string
	Purpose          string
	AmountKopecks    int64
	Payload          json.RawMessage
}

type WebhookReceiver func(context.Context, int64, VerifiedSBPNotification) error

// NewWebhookHandler belongs outside browser sessions, CORS and origin gates.
// The callback must journal verified unmatched events as well as matched ones:
// bank registration sends test events, and real notifications can precede the
// response that first persists qrcId. Only successful durable receipt gets 200.
func NewWebhookHandler(verifier *paytochkabank.WebhookVerifier, receive WebhookReceiver) http.Handler {
	return NewCombinedWebhookHandler(verifier, receive, nil)
}

func NewCombinedWebhookHandler(verifier *paytochkabank.WebhookVerifier, receive WebhookReceiver, receiveCard CardWebhookReceiver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if verifier == nil || (receive == nil && receiveCard == nil) {
			http.Error(w, "webhook is not configured", http.StatusServiceUnavailable)
			return
		}
		profileText := r.PathValue("profileID")
		generic := strings.TrimRight(r.URL.Path, "/") == "/payments/tochka/webhook"
		if profileText == "" && !generic {
			parts := strings.Split(strings.TrimRight(r.URL.Path, "/"), "/")
			profileText = parts[len(parts)-1]
		}
		var profileID int64
		var err error
		if !generic {
			profileID, err = strconv.ParseInt(profileText, 10, 64)
		}
		if err != nil || (!generic && profileID <= 0) {
			http.Error(w, "invalid payment profile", http.StatusBadRequest)
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "text/plain" {
			http.Error(w, "webhook requires text/plain", http.StatusUnsupportedMediaType)
			return
		}
		if r.Body == nil {
			http.Error(w, "webhook body is required", http.StatusBadRequest)
			return
		}
		const maxBody = 1 << 20
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		if err != nil || len(raw) == 0 {
			http.Error(w, "invalid webhook body", http.StatusBadRequest)
			return
		}
		if len(raw) > maxBody {
			http.Error(w, "webhook body is too large", http.StatusRequestEntityTooLarge)
			return
		}
		event, err := verifier.Verify(raw)
		if err != nil {
			http.Error(w, "invalid webhook signature or claims", http.StatusUnauthorized)
			return
		}
		if event.WebhookType == paytochkabank.WebhookAcquiringInternetPayment {
			if receiveCard == nil {
				w.WriteHeader(http.StatusOK)
				return
			}
			notification, decodeErr := verifiedCardNotification(event)
			if decodeErr != nil {
				http.Error(w, "invalid acquiring notification", http.StatusBadRequest)
				return
			}
			if err := receiveCard(r.Context(), profileID, notification); err != nil {
				http.Error(w, "payment notification could not be recorded", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		if event.WebhookType != paytochkabank.WebhookIncomingSBPPayment || receive == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		notification, err := verifiedNotification(event)
		if err != nil {
			http.Error(w, "invalid payment notification", http.StatusBadRequest)
			return
		}
		if err := receive(r.Context(), profileID, notification); err != nil {
			// Never return raw database errors, customer details, or the JWT.
			http.Error(w, "payment notification could not be recorded", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func verifiedNotification(event *paytochkabank.WebhookEvent) (VerifiedSBPNotification, error) {
	message, err := event.SBPPayment()
	if err != nil || message == nil || message.CustomerCode == "" || message.MerchantID == "" || message.QRCID == "" || message.Amount <= 0 {
		return VerifiedSBPNotification{}, errors.New("incoming SBP payment identity is incomplete")
	}
	digest := sha256.Sum256(event.Payload)
	return VerifiedSBPNotification{
		EventKey: hex.EncodeToString(digest[:]), CustomerCode: message.CustomerCode, MerchantID: message.MerchantID, QRCID: message.QRCID,
		TransactionID: message.OperationID, RefTransactionID: message.RefTransactionID, PaymentType: message.PaymentType,
		Purpose: message.Purpose, AmountKopecks: int64(message.Amount), Payload: append(json.RawMessage(nil), event.Payload...),
	}, nil
}
