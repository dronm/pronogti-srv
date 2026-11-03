package payment

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

type CardConfirmationRepository interface {
	ClaimCardConfirmation(context.Context) (ConfirmationCandidate, error)
	ObserveCard(context.Context, ConfirmationCandidate, CardObservation) error
	ReleaseConfirmation(context.Context, ConfirmationCandidate, string) error
}

type CardConfirmationWorker struct {
	repository CardConfirmationRepository
	checker    CardChecker
	tokens     TokenSource
	options    ConfirmationOptions
	now        func() time.Time
}

func NewCardConfirmationWorker(repository CardConfirmationRepository, checker CardChecker, tokens TokenSource, options ConfirmationOptions) *CardConfirmationWorker {
	if options.PollInterval <= 0 {
		options.PollInterval = 2 * time.Second
	}
	return &CardConfirmationWorker{repository: repository, checker: checker, tokens: tokens, options: options, now: time.Now}
}

func (w *CardConfirmationWorker) Run(ctx context.Context) error {
	if ctx == nil || w == nil || w.repository == nil || w.checker == nil {
		return errors.New("card confirmation worker is not configured")
	}
	ticker := time.NewTicker(w.options.PollInterval)
	defer ticker.Stop()
	for {
		worked, err := w.ProcessOne(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			slog.Error("card confirmation iteration failed", "error", err)
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

// ProcessOne is independent of customer sessions. Only this worker's verified
// bank read (or deliberate built-in demo) can confirm an acquiring payment.
func (w *CardConfirmationWorker) ProcessOne(ctx context.Context) (bool, error) {
	if ctx == nil || w == nil || w.repository == nil || w.checker == nil {
		return false, errors.New("card confirmation worker is not configured")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	candidate, err := w.repository.ClaimCardConfirmation(ctx)
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
	if !validCardConfirmationCandidate(candidate) || candidate.Attempt.OperationID == "" {
		return release("invalid_card_confirmation_identity")
	}
	var observation CardObservation
	if candidate.Profile.Environment == "mock" && !w.options.MockExternal {
		observation = CardObservation{
			OperationID: candidate.Attempt.OperationID, Status: "CREATED", CustomerCode: candidate.Profile.CustomerCode,
			MerchantID: candidate.Profile.AcquiringMerchantID, AmountKopecks: candidate.Attempt.AmountKopecks, PaymentType: "card",
		}
		if w.options.MockAutoPayAfter > 0 && !w.now().Before(candidate.Attempt.CreatedAt.Add(w.options.MockAutoPayAfter)) {
			observation.Paid = true
			observation.Status = "APPROVED"
			observation.TransactionID = "mock-card-" + candidate.Attempt.ID
			observation.PaymentID = observation.TransactionID
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
	if !sameCardObservationIdentity(candidate, observation) {
		return release("invalid_card_bank_confirmation")
	}
	saveCtx, cancel := persistenceContext(ctx)
	defer cancel()
	return true, w.repository.ObserveCard(saveCtx, candidate, observation)
}
