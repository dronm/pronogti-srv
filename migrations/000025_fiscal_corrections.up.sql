BEGIN;

-- Each correction is an immutable pair of fiscal documents. Bank payments and
-- original receipts remain unchanged; attempts and uncertain outcomes persist.
CREATE TABLE public.fiscal_corrections (
	id uuid PRIMARY KEY,
	order_id uuid NOT NULL,
	payment_id uuid NOT NULL,
	company_id bigint NOT NULL,
	environment text NOT NULL CHECK (environment IN ('mock','sandbox','production')),
	operator_id bigint NOT NULL REFERENCES public.users(id),
	source_version text NOT NULL CHECK (source_version='sale' OR source_version ~ '^[0-9a-f-]{36}$'),
	reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 2000),
	basis text NOT NULL CHECK (basis IN ('voluntary','prescription')),
	calculation_date date NOT NULL,
	prescription_number text NOT NULL DEFAULT '',
	fiscal_snapshot jsonb NOT NULL CHECK (jsonb_typeof(fiscal_snapshot)='object'),
	original_payload jsonb NOT NULL CHECK (jsonb_typeof(original_payload)='object'),
	original_identity jsonb NOT NULL CHECK (jsonb_typeof(original_identity)='object'),
	reversal_payload jsonb NOT NULL CHECK (jsonb_typeof(reversal_payload)='object'),
	replacement_payload jsonb NOT NULL CHECK (jsonb_typeof(replacement_payload)='object'),
	state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','processing','completed','failed','unknown')),
	idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
	request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT fiscal_corrections_payment_fkey FOREIGN KEY(payment_id,order_id,company_id,environment)
		REFERENCES public.payments(id,order_id,company_id,environment),
	CONSTRAINT fiscal_corrections_request_key UNIQUE(order_id,idempotency_key),
	CONSTRAINT fiscal_corrections_source_version UNIQUE(order_id,source_version),
	CONSTRAINT fiscal_corrections_prescription CHECK ((basis='prescription')=(length(btrim(prescription_number))>0))
);
CREATE INDEX fiscal_corrections_history_idx ON public.fiscal_corrections(order_id,created_at DESC);
CREATE UNIQUE INDEX fiscal_corrections_one_active_idx ON public.fiscal_corrections(order_id) WHERE state<>'completed';

CREATE TABLE public.fiscal_correction_documents (
	id uuid PRIMARY KEY,
	correction_id uuid NOT NULL REFERENCES public.fiscal_corrections(id),
	role text NOT NULL CHECK (role IN ('reversal','replacement')),
	attempt integer NOT NULL DEFAULT 1 CHECK (attempt>0),
	operator_id bigint NOT NULL REFERENCES public.users(id),
	resume_key text,
	request_id text NOT NULL UNIQUE CHECK (request_id ~ '^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$'),
	payload jsonb NOT NULL CHECK (jsonb_typeof(payload)='object'),
	state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','processing','issued','demo','failed','unknown')),
	response_payload jsonb CHECK (jsonb_typeof(response_payload)='object'),
	initial_response_payload jsonb CHECK (jsonb_typeof(initial_response_payload)='object'),
	error_code text,
	claim_token uuid,
	claimed_at timestamptz,
	dispatch_started_at timestamptz,
	next_attempt_at timestamptz DEFAULT now(),
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT fiscal_correction_documents_attempt UNIQUE(correction_id,role,attempt),
	CONSTRAINT fiscal_correction_documents_resume_key UNIQUE(correction_id,resume_key),
	CONSTRAINT fiscal_correction_documents_claim CHECK ((claim_token IS NULL)=(claimed_at IS NULL))
);
CREATE INDEX fiscal_correction_documents_due_idx ON public.fiscal_correction_documents(next_attempt_at) WHERE state IN ('pending','processing','unknown');

CREATE FUNCTION public.fiscal_corrections_immutable() RETURNS trigger AS $$
BEGIN
	IF (to_jsonb(NEW)-ARRAY['state','updated_at']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','updated_at']) THEN
		RAISE EXCEPTION 'fiscal correction snapshots are immutable' USING ERRCODE='23514';
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER fiscal_corrections_immutable BEFORE UPDATE ON public.fiscal_corrections FOR EACH ROW EXECUTE FUNCTION public.fiscal_corrections_immutable();

CREATE FUNCTION public.fiscal_correction_documents_immutable() RETURNS trigger AS $$
BEGIN
	IF (to_jsonb(NEW)-ARRAY['state','response_payload','initial_response_payload','error_code','claim_token','claimed_at','dispatch_started_at','next_attempt_at','updated_at']) IS DISTINCT FROM
		(to_jsonb(OLD)-ARRAY['state','response_payload','initial_response_payload','error_code','claim_token','claimed_at','dispatch_started_at','next_attempt_at','updated_at']) THEN
		RAISE EXCEPTION 'fiscal correction request identity and payload are immutable' USING ERRCODE='23514';
	END IF;
	IF OLD.state IN ('issued','demo','failed') AND NEW IS DISTINCT FROM OLD THEN
		RAISE EXCEPTION 'final fiscal correction attempts are immutable' USING ERRCODE='23514';
	END IF;
	IF OLD.initial_response_payload IS NOT NULL AND NEW.initial_response_payload IS DISTINCT FROM OLD.initial_response_payload THEN
		RAISE EXCEPTION 'initial fiscal correction response is immutable' USING ERRCODE='23514';
	END IF;
	IF OLD.dispatch_started_at IS NOT NULL AND NEW.dispatch_started_at IS DISTINCT FROM OLD.dispatch_started_at THEN
		RAISE EXCEPTION 'fiscal correction dispatch marker is immutable' USING ERRCODE='23514';
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER fiscal_correction_documents_immutable BEFORE UPDATE ON public.fiscal_correction_documents FOR EACH ROW EXECUTE FUNCTION public.fiscal_correction_documents_immutable();

COMMIT;
