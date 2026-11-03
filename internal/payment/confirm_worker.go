package payment

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

var ErrNoConfirmation = errors.New("no payment confirmation candidate")

type ConfirmationRepository interface {
	ClaimConfirmation(context.Context) (ConfirmationCandidate, error)
	Observe(context.Context, ConfirmationCandidate, BankObservation) error
	ReleaseConfirmation(context.Context, ConfirmationCandidate, string) error
	Receive(context.Context, int64, VerifiedSBPNotification) error
}

type ConfirmationOptions struct {
	PollInterval time.Duration
	MockExternal bool
	// Zero disables automatic demo completion. It is never used for bank or
	// external-mock profiles, which require an independently observed result.
	MockAutoPayAfter time.Duration
}

type ConfirmationWorker struct {
	repository ConfirmationRepository
	checker    BankChecker
	tokens     TokenSource
	options    ConfirmationOptions
	now        func() time.Time
}

func NewConfirmationWorker(repository ConfirmationRepository, checker BankChecker, tokens TokenSource, options ConfirmationOptions) *ConfirmationWorker {
	if options.PollInterval <= 0 {
		options.PollInterval = 2 * time.Second
	}
	return &ConfirmationWorker{repository: repository, checker: checker, tokens: tokens, options: options, now: time.Now}
}

func (w *ConfirmationWorker) Run(ctx context.Context) error {
	if ctx == nil || w.repository == nil || w.checker == nil {
		return errors.New("payment confirmation worker is not configured")
	}
	ticker := time.NewTicker(w.options.PollInterval)
	defer ticker.Stop()
	for {
		worked, err := w.ProcessOne(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			slog.Error("payment confirmation iteration failed", "error", err)
		}
		if worked && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// ProcessOne runs independently from customer cookies or browser activity.
// Retrying its reads cannot register a second QR or charge a second payment.
func (w *ConfirmationWorker) ProcessOne(ctx context.Context) (bool, error) {
	if ctx == nil || w.repository == nil || w.checker == nil {
		return false, errors.New("payment confirmation worker is not configured")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	candidate, err := w.repository.ClaimConfirmation(ctx)
	if errors.Is(err, ErrNoConfirmation) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	release := func(code string) (bool, error) {
		saveCtx, cancel := persistenceContext(ctx)
		defer cancel()
		return true, w.repository.ReleaseConfirmation(saveCtx, candidate, code)
	}
	if !validConfirmationCandidate(candidate) {
		return release("invalid_confirmation_identity")
	}
	var observation BankObservation
	if candidate.Profile.Environment == "mock" && !w.options.MockExternal {
		observation = BankObservation{QRCID: candidate.Attempt.QRCID, Status: "MockWaiting"}
		if w.options.MockAutoPayAfter > 0 && !w.now().Before(candidate.Attempt.CreatedAt.Add(w.options.MockAutoPayAfter)) {
			observation.Paid = true
			observation.Status = "Accepted"
			observation.PaymentType = "sbpPayment"
			observation.TransactionID = "mock-" + candidate.Attempt.ID
			observation.RefTransactionID = observation.TransactionID
			observation.Payload, _ = json.Marshal(map[string]any{"source": "server_mock", "environment": "mock", "qrcId": candidate.Attempt.QRCID, "amount_kopecks": candidate.Attempt.AmountKopecks, "refTransactionId": observation.RefTransactionID})
		}
	} else {
		if w.tokens == nil {
			return release("confirmation_credentials_unavailable")
		}
		token, err := w.tokens.Token(ctx, candidate.Profile)
		if err != nil {
			return release("confirmation_credentials_unavailable")
		}
		deadline := time.Duration(candidate.Profile.HTTPTimeoutSeconds) * time.Second
		if deadline <= 0 || deadline > 300*time.Second {
			return release("invalid_confirmation_timeout")
		}
		checkCtx, cancel := context.WithTimeout(ctx, deadline)
		observation, err = w.checker.Check(checkCtx, candidate, token)
		cancel()
		if err != nil {
			return release("bank_confirmation_unavailable")
		}
	}
	if observation.Paid && (observation.QRCID != candidate.Attempt.QRCID || observation.Status != "Accepted" || observation.RefTransactionID == "") {
		return release("invalid_bank_confirmation")
	}
	saveCtx, cancel := persistenceContext(ctx)
	defer cancel()
	return true, w.repository.Observe(saveCtx, candidate, observation)
}

func validConfirmationCandidate(candidate ConfirmationCandidate) bool {
	a, p := candidate.Attempt, candidate.Profile
	return a.ID != "" && a.OrderID != "" && a.QRCID != "" && a.Currency == "RUB" && a.AmountKopecks > 0 &&
		p.ID > 0 && p.ID == a.PaymentProfileID && p.CompanyID == a.CompanyID && p.Environment == a.Environment &&
		(p.Environment == "mock" || p.Environment == "sandbox" || p.Environment == "production")
}
