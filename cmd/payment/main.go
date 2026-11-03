// Command payment inspects payment state and confirms deliberately simulated
// payments. It never provides an unauthenticated browser "mark paid" endpoint.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/pronogti/internal/config"
	"github.com/dronm/pronogti/internal/fiscal"
	"github.com/dronm/pronogti/internal/payment"
	"github.com/dronm/pronogti/internal/settlement"
	"github.com/dronm/webapp/database"
)

type paymentStatus struct {
	PaymentID          string     `json:"payment_id"`
	OrderID            string     `json:"order_id"`
	CompanyID          int64      `json:"company_id"`
	Environment        string     `json:"environment"`
	State              string     `json:"state"`
	OrderState         string     `json:"order_state"`
	AmountKopecks      int64      `json:"amount_kopecks"`
	Currency           string     `json:"currency"`
	PaidAt             *time.Time `json:"paid_at"`
	BankReference      string     `json:"bank_reference,omitempty"`
	YClientsSyncStatus string     `json:"yclients_sync_status"`
	FiscalStatus       string     `json:"fiscal_status"`
}

type paymentOperations interface {
	ConfirmDemo(context.Context, string) error
	RetrySettlement(context.Context, string) error
	RetryReceipt(context.Context, string) error
	Status(context.Context, string) (paymentStatus, error)
}

type postgresOperations struct {
	db       ds.Provider
	verifier evidenceVerifier
}

func (p postgresOperations) ConfirmDemo(ctx context.Context, id string) error {
	return payment.NewPostgresRepository(p.db).ConfirmDemo(ctx, id)
}

func (p postgresOperations) RetrySettlement(ctx context.Context, id string) error {
	status, err := p.Status(ctx, id)
	if err != nil {
		return err
	}
	return settlement.NewPostgresRepository(p.db).RetrySafe(ctx, status.OrderID)
}

func (p postgresOperations) RetryReceipt(ctx context.Context, id string) error {
	status, err := p.Status(ctx, id)
	if err != nil {
		return err
	}
	if err := fiscal.NewPostgresRepository(p.db).RetrySafe(ctx, status.OrderID); err != nil {
		return fmt.Errorf("receipt retry requires a failed, never-dispatched receipt on a paid order: %w", err)
	}
	return nil
}

func (p postgresOperations) Status(ctx context.Context, id string) (paymentStatus, error) {
	lease, server, err := p.db.GetPrimary(ctx)
	if err != nil {
		return paymentStatus{}, fmt.Errorf("acquire payment database: %w", err)
	}
	defer p.db.Release(lease, server)
	var value paymentStatus
	err = lease.Conn().QueryRow(ctx, `SELECT p.id::text,p.order_id::text,p.company_id,p.environment,p.state,o.state,
		p.amount_kopecks,p.currency,p.paid_at,COALESCE(p.ref_transaction_id,''),o.yclients_sync_status,COALESCE(f.state,'not_started')
		FROM public.payments p JOIN public.payment_orders o ON o.id=p.order_id
		LEFT JOIN public.fiscal_receipts f ON f.order_id=o.id WHERE p.id=$1`, id).Scan(
		&value.PaymentID, &value.OrderID, &value.CompanyID, &value.Environment, &value.State, &value.OrderState,
		&value.AmountKopecks, &value.Currency, &value.PaidAt, &value.BankReference, &value.YClientsSyncStatus, &value.FiscalStatus)
	if errors.Is(err, ds.ErrNoRows) {
		return paymentStatus{}, errors.New("payment not found")
	}
	if err != nil {
		return paymentStatus{}, fmt.Errorf("read payment status: %w", err)
	}
	return value, nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var paymentIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func run(args []string, out, errOut io.Writer) error {
	global := flag.NewFlagSet("payment", flag.ContinueOnError)
	global.SetOutput(errOut)
	configPath := global.String("config", config.DefaultConfigPath, "configuration JSON path")
	if err := global.Parse(args); err != nil {
		return err
	}
	command, err := parsePaymentCommand(global.Args())
	if err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	dbConfig, err := cfg.Database.Resolve()
	if err != nil {
		return err
	}
	db, err := database.Open(dbConfig, nil)
	if err != nil {
		return fmt.Errorf("open payment database: %w", err)
	}
	defer db.Close()
	operations := postgresOperations{db: db}
	timeout := 15 * time.Second
	if command.action == "resolve-settlement" {
		operations.verifier, err = configuredEvidenceVerifier(cfg)
		if err != nil {
			return err
		}
		timeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if command.action == "resolve-settlement" {
		if err := operations.ResolveSettlement(ctx, command.id, command.resolution); err != nil {
			return err
		}
		return execute(ctx, operations, "status", command.id, out)
	}
	return execute(ctx, operations, command.action, command.id, out)
}

func execute(ctx context.Context, operations paymentOperations, action, id string, out io.Writer) error {
	switch action {
	case "confirm-demo":
		// The repository enforces mock environment in the same transaction that
		// performs confirmation, even if an operator supplied a production ID.
		if err := operations.ConfirmDemo(ctx, id); err != nil {
			return err
		}
	case "retry-settlement":
		// The database requeues only never-dispatched work. An uncertain write
		// must retain its original operation and reconcile with YClients.
		if err := operations.RetrySettlement(ctx, id); err != nil {
			return err
		}
	case "retry-receipt":
		// Failed validation before dispatch is safe to revisit. A sent fiscal
		// command is reconciled using its existing driver journal identity.
		if err := operations.RetryReceipt(ctx, id); err != nil {
			return err
		}
	}
	value, err := operations.Status(ctx, id)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(value)
}
