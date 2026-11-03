package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/dronm/pronogti/internal/cache"
	"github.com/dronm/pronogti/internal/config"
	"github.com/dronm/pronogti/internal/services"
	"github.com/dronm/pronogti/internal/settlement"
)

type settlementResolution struct {
	operationID    string
	transactionIDs []int64
	reason         string
}

type paymentCommand struct {
	action, id string
	resolution settlementResolution
}

const paymentUsage = "usage: payment -config config.json {status|confirm-demo|retry-settlement|retry-receipt} PAYMENT_UUID; or resolve-settlement PAYMENT_UUID OPERATION_ID TRANSACTION_ID[,ID...] 'review reason'"

var operationIDPattern = regexp.MustCompile(`^(visit-payment|goods-payment|goods-create|service-[1-9][0-9]*)$`)

func parsePaymentCommand(args []string) (paymentCommand, error) {
	if len(args) < 2 || !paymentIDPattern.MatchString(args[1]) {
		return paymentCommand{}, errors.New(paymentUsage)
	}
	command := paymentCommand{action: args[0], id: args[1]}
	switch command.action {
	case "status", "confirm-demo", "retry-settlement", "retry-receipt":
		if len(args) != 2 {
			return paymentCommand{}, errors.New(paymentUsage)
		}
	case "resolve-settlement":
		if len(args) != 5 || !operationIDPattern.MatchString(args[2]) {
			return paymentCommand{}, errors.New(paymentUsage)
		}
		command.resolution = settlementResolution{operationID: args[2], reason: strings.TrimSpace(args[4])}
		if command.resolution.reason == "" || len(command.resolution.reason) > 4000 {
			return paymentCommand{}, errors.New("resolve-settlement requires a review reason of 1..4000 bytes")
		}
		ids := strings.Split(args[3], ",")
		if len(ids) > 500 {
			return paymentCommand{}, errors.New("resolve-settlement accepts at most 500 transaction IDs")
		}
		seen := make(map[int64]bool, len(ids))
		for _, text := range ids {
			id, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
			if err != nil || id <= 0 || seen[id] {
				return paymentCommand{}, errors.New("resolve-settlement transaction IDs must be positive unique integers")
			}
			seen[id] = true
			command.resolution.transactionIDs = append(command.resolution.transactionIDs, id)
		}
	default:
		return paymentCommand{}, errors.New(paymentUsage)
	}
	return command, nil
}

type evidenceVerifier interface {
	VerifyPaymentEvidence(context.Context, settlement.Job, settlement.Operation, []int64, string) (settlement.Outcome, error)
}

type resolutionRepository interface {
	LoadJob(context.Context, string) (settlement.Job, error)
	ResolveOperation(context.Context, string, string, settlement.Outcome) error
}

func configuredEvidenceVerifier(cfg *config.Config) (evidenceVerifier, error) {
	timeout, err := cfg.ClientTimeout()
	if err != nil {
		return nil, err
	}
	ttl, err := cfg.CacheTTLDuration()
	if err != nil {
		return nil, err
	}
	client, err := services.NewYClient(cfg.YClients, false, cfg.SaveYClientsToken, &http.Client{Timeout: timeout})
	if err != nil {
		return nil, err
	}
	return services.NewSettlementProvider(services.NewYClientService(client, cache.New(ttl))), nil
}

func (p postgresOperations) ResolveSettlement(ctx context.Context, id string, resolution settlementResolution) error {
	status, err := p.Status(ctx, id)
	if err != nil {
		return err
	}
	return resolveSettlement(ctx, settlement.NewPostgresRepository(p.db), p.verifier, status, resolution)
}

// resolveSettlement verifies operator-attributed external transactions by reading
// YClients. Only the resulting audit evidence is saved locally; no financial
// request or new payment is sent to YClients or the bank.
func resolveSettlement(ctx context.Context, repository resolutionRepository, verifier evidenceVerifier, status paymentStatus, resolution settlementResolution) error {
	if status.State != "paid" || status.OrderState != "paid" || status.Environment != "production" {
		return errors.New("settlement evidence requires a confirmed production payment and paid order")
	}
	if verifier == nil {
		return errors.New("settlement evidence verifier is not configured")
	}
	job, err := repository.LoadJob(ctx, status.OrderID)
	if err != nil {
		return err
	}
	if job.PaymentID != status.PaymentID || job.Order.ID != status.OrderID {
		return errors.New("settlement job does not belong to the selected bank payment")
	}
	var operation *settlement.Operation
	for index := range job.Operations {
		if job.Operations[index].ID == resolution.operationID {
			operation = &job.Operations[index]
			break
		}
	}
	if operation == nil {
		return errors.New("settlement operation not found on this order")
	}
	if (operation.Kind != settlement.KindVisitPayment && operation.Kind != settlement.KindGoodsPayment) || (operation.State != "unknown" && operation.State != "dispatched") {
		return errors.New("only an uncertain dispatched visit or goods payment accepts operator transaction evidence")
	}
	outcome, err := verifier.VerifyPaymentEvidence(ctx, job, *operation, resolution.transactionIDs, resolution.reason)
	if err != nil {
		return fmt.Errorf("settlement evidence was not accepted: %w", err)
	}
	if outcome.State != "succeeded" || len(outcome.Result) == 0 {
		return errors.New("settlement evidence verifier did not establish a successful outcome")
	}
	return repository.ResolveOperation(ctx, status.OrderID, operation.ID, outcome)
}
