package terminalbridge

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/dronm/ds/v4"
)

// ExpireUnclaimed releases only commands provably never handed to an agent.
// Previously leased commands are never failed by a server clock or lost lease.
func (r *Repository) ExpireUnclaimed(ctx context.Context) (int, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	rows, err := conn.Query(ctx, `SELECT id::text,order_id::text FROM public.terminal_commands WHERE state='queued' AND first_claimed_at IS NULL AND expires_at<=now() ORDER BY expires_at,id LIMIT 100`)
	if err != nil {
		return 0, err
	}
	type candidate struct{ id, orderID string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.orderID); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, c := range candidates {
		changed := false
		if err := ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
			var locked string
			if err := tx.QueryRow(ctx, `SELECT id::text FROM public.payment_orders WHERE id=$1 FOR UPDATE`, c.orderID).Scan(&locked); err != nil {
				return err
			}
			command, err := scanCommand(tx.QueryRow(ctx, `SELECT `+commandColumns+` FROM public.terminal_commands c WHERE c.id=$1 FOR UPDATE`, c.id))
			if err != nil {
				return err
			}
			if command.State != "queued" || command.FirstClaimedAt != nil || command.ExpiresAt.After(time.Now()) {
				return nil
			}
			if _, err := tx.Exec(ctx, `UPDATE public.terminal_commands SET state='expired',error_code='command_expired_before_delivery',lease_until=NULL WHERE id=$1`, command.CommandID); err != nil {
				return err
			}
			if err := failPayment(ctx, tx, command, "command_expired_before_delivery", "expired"); err != nil {
				return err
			}
			changed = true
			return nil
		}); err != nil {
			return count, err
		}
		if changed {
			count++
		}
	}
	return count, nil
}

// ExpirePayment is used after HTTP ownership checks so an offline agent cannot
// strand a checkout that has provably never been delivered to a device.
func (r *Repository) ExpirePayment(ctx context.Context, paymentID string) (bool, error) {
	if !uuidPattern.MatchString(paymentID) {
		return false, invalid("valid payment ID is required")
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	var id, orderID string
	err = conn.QueryRow(ctx, `SELECT id::text,order_id::text FROM public.terminal_commands WHERE payment_id=$1 AND state='queued' AND first_claimed_at IS NULL AND expires_at<=now()`, paymentID).Scan(&id, &orderID)
	if errors.Is(err, ds.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	changed := false
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		var locked string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM public.payment_orders WHERE id=$1 FOR UPDATE`, orderID).Scan(&locked); err != nil {
			return err
		}
		command, err := scanCommand(tx.QueryRow(ctx, `SELECT `+commandColumns+` FROM public.terminal_commands c WHERE c.id=$1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if command.State != "queued" || command.FirstClaimedAt != nil || command.ExpiresAt.After(time.Now()) {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE public.terminal_commands SET state='expired',error_code='command_expired_before_delivery',lease_until=NULL WHERE id=$1`, id); err != nil {
			return err
		}
		if err := failPayment(ctx, tx, command, "command_expired_before_delivery", "expired"); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

// RunExpiry performs only durable housekeeping; it never opens a device or
// infers nonpayment for a leased/dispatched/uncertain command.
func (r *Repository) RunExpiry(ctx context.Context) error {
	var lastError time.Time
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		check, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := r.ExpireUnclaimed(check)
		cancel()
		if err != nil && ctx.Err() == nil && time.Since(lastError) >= time.Minute {
			slog.ErrorContext(ctx, "terminal command expiry failed", "error", err)
			lastError = time.Now()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
