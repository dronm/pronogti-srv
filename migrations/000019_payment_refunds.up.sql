BEGIN;

CREATE TABLE public.payment_refunds (
	id uuid PRIMARY KEY,
	payment_id uuid NOT NULL,
	order_id uuid NOT NULL,
	company_id bigint NOT NULL,
	environment text NOT NULL,
	operator_id bigint NOT NULL REFERENCES public.users (id),
	idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
	request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
	amount_kopecks bigint NOT NULL CHECK (amount_kopecks BETWEEN 1 AND 9007199254740991),
	lines jsonb NOT NULL CHECK (jsonb_typeof(lines) = 'array' AND jsonb_array_length(lines) > 0),
	receipt_payload jsonb NOT NULL CHECK (jsonb_typeof(receipt_payload) = 'object'),
	bank_state text NOT NULL DEFAULT 'queued' CHECK (
		bank_state IN ('queued', 'prepared', 'submitting', 'pending', 'confirmed', 'rejected', 'unknown')
	),
	bank_baseline jsonb CHECK (jsonb_typeof(bank_baseline) = 'array'),
	bank_reference text,
	fiscal_state text NOT NULL DEFAULT 'waiting' CHECK (
		fiscal_state IN ('waiting', 'queued', 'submitting', 'issued', 'demo', 'failed', 'unknown')
	),
	receipt_number bigint CHECK (receipt_number > 0),
	error_code text,
	next_attempt_at timestamptz DEFAULT now(),
	claim_token uuid,
	claimed_at timestamptz,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT payment_refunds_payment_fkey FOREIGN KEY (payment_id, order_id, company_id, environment)
		REFERENCES public.payments (id, order_id, company_id, environment),
	CONSTRAINT payment_refunds_idempotency_key UNIQUE (payment_id, idempotency_key),
	CONSTRAINT payment_refunds_fiscal_after_bank CHECK ((bank_state = 'confirmed') = (fiscal_state <> 'waiting')),
	CONSTRAINT payment_refunds_claim_check CHECK ((claim_token IS NULL) = (claimed_at IS NULL))
);

-- A possibly dispatched refund still owns the reservation. A further refund
-- can start after bank rejection or successful bank and receipt completion.
CREATE UNIQUE INDEX payment_refunds_one_active_idx ON public.payment_refunds (payment_id)
	WHERE bank_state <> 'rejected' AND fiscal_state NOT IN ('issued', 'demo');
CREATE INDEX payment_refunds_due_idx ON public.payment_refunds (next_attempt_at, id)
	WHERE next_attempt_at IS NOT NULL;

COMMIT;
