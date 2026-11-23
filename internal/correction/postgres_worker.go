package correction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dronm/ds/v4"
	"github.com/google/uuid"
)

const claimCorrectionSQL = `WITH candidate AS (
	SELECT d.id,d.state AS previous_state
	FROM public.fiscal_correction_documents d JOIN public.fiscal_corrections c ON c.id=d.correction_id
	WHERE c.state IN ('queued','processing','unknown')
		AND NOT EXISTS(SELECT 1 FROM public.fiscal_correction_documents newer
			WHERE newer.correction_id=d.correction_id AND newer.role=d.role AND newer.attempt>d.attempt)
		AND (d.role='reversal' OR EXISTS(
			SELECT 1 FROM public.fiscal_correction_documents rev WHERE rev.correction_id=d.correction_id AND rev.role='reversal'
			AND rev.state IN ('issued','demo') AND NOT EXISTS(SELECT 1 FROM public.fiscal_correction_documents revnew
				WHERE revnew.correction_id=rev.correction_id AND revnew.role='reversal' AND revnew.attempt>rev.attempt)))
		AND ((d.state IN ('pending','unknown') AND d.next_attempt_at<=now())
			OR (d.state='processing' AND d.dispatch_started_at IS NULL AND d.claimed_at<now()-interval '30 seconds')
			OR (d.state='processing' AND d.dispatch_started_at IS NOT NULL AND d.claimed_at<now()-interval '10 minutes'))
	ORDER BY d.created_at,d.id FOR UPDATE OF d SKIP LOCKED LIMIT 1
), claimed AS (
	UPDATE public.fiscal_correction_documents d SET state='processing',claim_token=$1,claimed_at=now(),updated_at=now()
	FROM candidate c WHERE d.id=c.id RETURNING d.*,c.previous_state
)
SELECT ` + documentColumns + `,d.correction_id::text,d.previous_state,c.environment,c.fiscal_snapshot
FROM claimed d JOIN public.fiscal_corrections c ON c.id=d.correction_id`

func (r *PostgresRepository) Claim(ctx context.Context) (Job, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Job{}, err
	}
	defer release()
	job := Job{ClaimToken: uuid.NewString()}
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		var profile []byte
		doc, err := readDocument(tx.QueryRow(ctx, claimCorrectionSQL, job.ClaimToken), &job.CorrectionID, &job.PreviousState, &job.Environment, &profile)
		if errors.Is(err, ds.ErrNoRows) {
			return ErrNoJob
		}
		if err != nil {
			return fmt.Errorf("claim correction document: %w", err)
		}
		job.Document = doc
		if err := json.Unmarshal(profile, &job.Profile); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE public.fiscal_corrections SET state='processing',updated_at=now() WHERE id=$1`, job.CorrectionID)
		return err
	})
	return job, err
}

func (r *PostgresRepository) BeginDispatch(ctx context.Context, job Job) error {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	result, err := conn.Exec(ctx, `UPDATE public.fiscal_correction_documents SET dispatch_started_at=now(),updated_at=now()
		WHERE id=$1 AND state='processing' AND claim_token=$2 AND dispatch_started_at IS NULL`, job.Document.ID, job.ClaimToken)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errors.New("correction document claim no longer owned")
	}
	return nil
}

func (r *PostgresRepository) Finish(ctx context.Context, job Job, state string, response json.RawMessage, code string) error {
	switch state {
	case "issued", "demo", "failed", "unknown":
	default:
		return errors.New("invalid correction outcome")
	}
	if state == "issued" {
		if _, err := responseIdentity(response); err != nil {
			return err
		}
	}
	conn, release, err := r.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	return ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		var encoded any
		if len(response) > 0 {
			encoded = string(response)
		}
		result, err := tx.Exec(ctx, `UPDATE public.fiscal_correction_documents SET state=$3,response_payload=$4::jsonb,initial_response_payload=COALESCE(initial_response_payload,$4::jsonb),error_code=NULLIF($5,''),
			claim_token=NULL,claimed_at=NULL,next_attempt_at=CASE WHEN $3='unknown' THEN now()+interval '30 seconds' ELSE NULL END,updated_at=now()
			WHERE id=$1 AND claim_token=$2 AND state='processing'`, job.Document.ID, job.ClaimToken, state, encoded, code)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("correction document claim no longer owned")
		}
		_, err = tx.Exec(ctx, `WITH latest AS (
			SELECT DISTINCT ON(role) role,state FROM public.fiscal_correction_documents
			WHERE correction_id=$1 ORDER BY role,attempt DESC
		), outcome AS (
			SELECT CASE
				WHEN bool_or(state='unknown') THEN 'unknown'
				WHEN bool_or(state='failed') THEN 'failed'
				WHEN count(*)=2 AND bool_and(state IN ('issued','demo')) THEN 'completed'
				WHEN bool_or(state='processing') THEN 'processing'
				ELSE 'queued' END AS state FROM latest
		)
		UPDATE public.fiscal_corrections c SET state=o.state,updated_at=now() FROM outcome o WHERE c.id=$1`, job.CorrectionID)
		return err
	})
}
