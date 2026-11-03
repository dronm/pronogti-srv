BEGIN;

-- Bank confirmation, CRM synchronization and fiscalization have independent
-- durable progress. No migration or trigger performs network/device operations.
CREATE TABLE public.yclients_profiles (
	id bigserial PRIMARY KEY,
	company_id bigint NOT NULL REFERENCES public.salons (company_id),
	name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
	mode text NOT NULL DEFAULT 'demo' CHECK (mode IN ('demo', 'live')),
	enabled boolean NOT NULL DEFAULT true,
	cashless_account_id bigint NOT NULL DEFAULT 0 CHECK (cashless_account_id >= 0),
	storage_id bigint NOT NULL DEFAULT 0 CHECK (storage_id >= 0),
	service_policy text NOT NULL DEFAULT 'financial_income'
		CHECK (service_policy IN ('require_visit', 'financial_income')),
	service_income_expense_id bigint NOT NULL DEFAULT 0 CHECK (service_income_expense_id >= 0),
	config_version bigint NOT NULL DEFAULT 1 CHECK (config_version > 0),
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT yclients_profiles_id_company_key UNIQUE (id, company_id),
	CONSTRAINT yclients_profiles_live_account_check CHECK (mode <> 'live' OR cashless_account_id > 0)
);

ALTER TABLE public.kiosks
	ADD COLUMN yclients_profile_id bigint,
	ADD CONSTRAINT kiosks_yclients_profile_company_fkey FOREIGN KEY (yclients_profile_id, company_id)
		REFERENCES public.yclients_profiles (id, company_id);

ALTER TABLE public.salons
	ADD COLUMN default_yclients_profile_id bigint,
	ADD CONSTRAINT salons_yclients_profile_company_fkey FOREIGN KEY (default_yclients_profile_id, company_id)
		REFERENCES public.yclients_profiles (id, company_id);

CREATE FUNCTION public.kiosks_clear_yclients_profile() RETURNS trigger AS $$
BEGIN
	IF NEW.company_id IS DISTINCT FROM OLD.company_id
		AND NEW.yclients_profile_id IS NOT DISTINCT FROM OLD.yclients_profile_id THEN
		NEW.yclients_profile_id := NULL;
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER kiosks_clear_yclients_profile BEFORE UPDATE ON public.kiosks
	FOR EACH ROW EXECUTE FUNCTION public.kiosks_clear_yclients_profile();

CREATE FUNCTION public.yclients_profiles_before_update() RETURNS trigger AS $$
BEGIN
	IF ROW(NEW.company_id, NEW.name, NEW.mode, NEW.enabled, NEW.cashless_account_id,
		NEW.storage_id, NEW.service_policy, NEW.service_income_expense_id)
		IS DISTINCT FROM
		ROW(OLD.company_id, OLD.name, OLD.mode, OLD.enabled, OLD.cashless_account_id,
		OLD.storage_id, OLD.service_policy, OLD.service_income_expense_id) THEN
		NEW.config_version := OLD.config_version + 1;
	ELSE
		NEW.config_version := OLD.config_version;
	END IF;
	NEW.updated_at := now();
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER yclients_profiles_before_update BEFORE UPDATE ON public.yclients_profiles
	FOR EACH ROW EXECUTE FUNCTION public.yclients_profiles_before_update();

INSERT INTO public.yclients_profiles (company_id, name)
	SELECT company_id, 'Demo YClients settlement' FROM public.salons;

UPDATE public.salons s SET default_yclients_profile_id = p.id
	FROM public.yclients_profiles p WHERE p.company_id = s.company_id;

CREATE FUNCTION public.yclients_demo_snapshot(selected_company_id bigint) RETURNS jsonb AS $$
	SELECT jsonb_build_object(
		'id', NULL, 'company_id', selected_company_id, 'name', 'Unconfigured demo',
		'mode', 'demo', 'enabled', true, 'cashless_account_id', 0, 'storage_id', 0,
		'service_policy', 'financial_income', 'service_income_expense_id', 0, 'config_version', 1
	);
$$ LANGUAGE sql IMMUTABLE;

ALTER TABLE public.payment_orders
	ADD COLUMN customer_contact text NOT NULL DEFAULT '' CHECK (length(customer_contact) <= 64),
	ADD COLUMN yclients_snapshot jsonb;

-- Previously prepared orders predate explicit CRM configuration and remain demo.
UPDATE public.payment_orders SET yclients_snapshot = public.yclients_demo_snapshot(company_id);

ALTER TABLE public.payment_orders
	ALTER COLUMN yclients_snapshot SET NOT NULL,
	ADD CONSTRAINT payment_orders_yclients_snapshot_check CHECK (
		jsonb_typeof(yclients_snapshot) = 'object'
		AND yclients_snapshot->>'mode' IN ('demo', 'live')
	),
	DROP CONSTRAINT payment_orders_yclients_sync_status_check,
	ADD CONSTRAINT payment_orders_yclients_sync_status_check CHECK (
		yclients_sync_status IN ('not_started', 'pending', 'processing', 'succeeded', 'failed', 'unknown', 'blocked', 'demo')
	);

CREATE FUNCTION public.payment_orders_yclients_before_insert() RETURNS trigger AS $$
DECLARE
	selected_profile_id bigint;
	selected_profile public.yclients_profiles%ROWTYPE;
BEGIN
	SELECT COALESCE(k.yclients_profile_id, s.default_yclients_profile_id)
	INTO selected_profile_id
	FROM public.salons s
	LEFT JOIN public.kiosks k ON k.id = NEW.kiosk_id AND k.company_id = NEW.company_id
	WHERE s.company_id = NEW.company_id;
	IF selected_profile_id IS NULL THEN
		NEW.yclients_snapshot := public.yclients_demo_snapshot(NEW.company_id);
	ELSE
		SELECT * INTO selected_profile FROM public.yclients_profiles
		WHERE id = selected_profile_id AND company_id = NEW.company_id;
		IF NOT FOUND OR NOT selected_profile.enabled THEN
			RAISE EXCEPTION 'checkout YClients profile is unavailable or disabled' USING ERRCODE = '23514';
		END IF;
		NEW.yclients_snapshot := to_jsonb(selected_profile) - 'created_at' - 'updated_at';
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payment_orders_yclients_before_insert BEFORE INSERT ON public.payment_orders
	FOR EACH ROW EXECUTE FUNCTION public.payment_orders_yclients_before_insert();

CREATE FUNCTION public.payment_orders_completion_before_update() RETURNS trigger AS $$
BEGIN
	IF NEW.customer_contact IS DISTINCT FROM OLD.customer_contact
		OR NEW.yclients_snapshot IS DISTINCT FROM OLD.yclients_snapshot THEN
		RAISE EXCEPTION 'checkout customer contact and YClients snapshots are immutable' USING ERRCODE = '23514';
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payment_orders_completion_before_update BEFORE UPDATE ON public.payment_orders
	FOR EACH ROW EXECUTE FUNCTION public.payment_orders_completion_before_update();

-- The customer must not pay the same unpaid visit again while the CRM still
-- shows its old balance after bank confirmation.
DROP INDEX public.payment_orders_one_pending_visit_idx;
CREATE UNIQUE INDEX payment_orders_one_pending_visit_idx
	ON public.payment_orders (company_id, visit_id, record_id)
	WHERE visit_id IS NOT NULL AND (
		state = 'payment_pending'
		OR (state = 'paid' AND yclients_sync_status NOT IN ('succeeded', 'demo'))
	);

ALTER TABLE public.payments ADD COLUMN confirmation_claimed_at timestamptz;

CREATE UNIQUE INDEX payments_production_qrc_key
	ON public.payments (payment_profile_id, qrc_id)
	WHERE environment = 'production' AND qrc_id IS NOT NULL;
CREATE UNIQUE INDEX payments_paid_bank_reference_key
	ON public.payments (payment_profile_id, ref_transaction_id)
	WHERE state = 'paid' AND ref_transaction_id IS NOT NULL;
CREATE UNIQUE INDEX payments_paid_bank_operation_key
	ON public.payments (payment_profile_id, transaction_id)
	WHERE state = 'paid' AND environment = 'production' AND transaction_id IS NOT NULL;

ALTER TABLE public.payment_events
	ADD COLUMN payment_profile_id bigint REFERENCES public.payment_profiles (id),
	ADD COLUMN next_matching_at timestamptz NOT NULL DEFAULT now(),
	ADD COLUMN matching_snapshot jsonb CHECK (matching_snapshot IS NULL OR jsonb_typeof(matching_snapshot) = 'object');

CREATE INDEX payment_events_matching_due_idx ON public.payment_events (next_matching_at, id)
	WHERE processing_state = 'pending';

-- The preexisting source-aware signature guard already permits authenticated
-- reconciliation reads and forbids unsigned webhook entries. Keep it intact.

-- A verified API-client notification may precede QR persistence and may span
-- multiple profiles. Defer profile/environment assignment until QR matching.
CREATE TABLE public.bank_webhook_inbox (
	event_key text PRIMARY KEY,
	payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
	matching_snapshot jsonb NOT NULL CHECK (jsonb_typeof(matching_snapshot) = 'object'),
	state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'processed', 'ignored')),
	received_at timestamptz NOT NULL DEFAULT now(),
	next_matching_at timestamptz NOT NULL DEFAULT now(),
	processing_error text
);
CREATE INDEX bank_webhook_inbox_matching_due_idx
	ON public.bank_webhook_inbox (next_matching_at, event_key) WHERE state = 'pending';

CREATE TABLE public.yclients_jobs (
	order_id uuid PRIMARY KEY,
	company_id bigint NOT NULL,
	payment_id uuid NOT NULL,
	environment text NOT NULL CHECK (environment IN ('mock', 'sandbox', 'production')),
	profile_snapshot jsonb NOT NULL CHECK (jsonb_typeof(profile_snapshot) = 'object'),
	state text NOT NULL DEFAULT 'pending'
		CHECK (state IN ('pending', 'processing', 'demo', 'succeeded', 'failed', 'unknown', 'blocked')),
	claimed_at timestamptz,
	next_attempt_at timestamptz,
	completed_at timestamptz,
	error_code text,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT yclients_jobs_order_company_fkey FOREIGN KEY (order_id, company_id)
		REFERENCES public.payment_orders (id, company_id),
	CONSTRAINT yclients_jobs_payment_identity_fkey FOREIGN KEY (payment_id, order_id, company_id, environment)
		REFERENCES public.payments (id, order_id, company_id, environment)
);

CREATE INDEX yclients_jobs_pending_idx ON public.yclients_jobs (next_attempt_at, created_at)
	WHERE state IN ('pending', 'processing', 'unknown', 'blocked');

CREATE FUNCTION public.yclients_jobs_before_update() RETURNS trigger AS $$
BEGIN
	IF ROW(NEW.order_id, NEW.company_id, NEW.payment_id, NEW.environment, NEW.profile_snapshot, NEW.created_at)
		IS DISTINCT FROM
		ROW(OLD.order_id, OLD.company_id, OLD.payment_id, OLD.environment, OLD.profile_snapshot, OLD.created_at) THEN
		RAISE EXCEPTION 'YClients job identities and snapshots are immutable' USING ERRCODE = '23514';
	END IF;
	NEW.updated_at := now();
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER yclients_jobs_before_update BEFORE UPDATE ON public.yclients_jobs
	FOR EACH ROW EXECUTE FUNCTION public.yclients_jobs_before_update();

CREATE FUNCTION public.payments_enqueue_yclients_job() RETURNS trigger AS $$
DECLARE inserted_count integer;
BEGIN
	IF NEW.state = 'paid' THEN
		INSERT INTO public.yclients_jobs (order_id, company_id, payment_id, environment, profile_snapshot)
			SELECT o.id, o.company_id, NEW.id, NEW.environment, o.yclients_snapshot
			FROM public.payment_orders o WHERE o.id = NEW.order_id AND o.company_id = NEW.company_id
			ON CONFLICT (order_id) DO NOTHING;
		GET DIAGNOSTICS inserted_count = ROW_COUNT;
		IF inserted_count = 1 THEN
			UPDATE public.payment_orders SET yclients_sync_status = 'pending'
				WHERE id = NEW.order_id AND yclients_sync_status = 'not_started';
		END IF;
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payments_enqueue_yclients_job AFTER INSERT OR UPDATE OF state ON public.payments
	FOR EACH ROW EXECUTE FUNCTION public.payments_enqueue_yclients_job();

INSERT INTO public.yclients_jobs (order_id, company_id, payment_id, environment, profile_snapshot)
	SELECT DISTINCT ON (o.id) o.id, o.company_id, p.id, p.environment, o.yclients_snapshot
	FROM public.payment_orders o JOIN public.payments p ON p.order_id = o.id AND p.company_id = o.company_id
	WHERE p.state = 'paid' ORDER BY o.id, p.created_at, p.id;

UPDATE public.payment_orders o SET yclients_sync_status = 'pending'
	WHERE o.yclients_sync_status = 'not_started' AND EXISTS (
		SELECT 1 FROM public.yclients_jobs j WHERE j.order_id = o.id
	);

CREATE TABLE public.yclients_operations (
	order_id uuid NOT NULL REFERENCES public.yclients_jobs (order_id),
	id text NOT NULL CHECK (length(btrim(id)) BETWEEN 1 AND 200),
	sequence_no integer NOT NULL CHECK (sequence_no >= 0),
	kind text NOT NULL CHECK (length(btrim(kind)) BETWEEN 1 AND 100),
	state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'dispatched', 'succeeded', 'failed', 'unknown')),
	request jsonb NOT NULL CHECK (jsonb_typeof(request) = 'object'),
	result jsonb,
	dispatched_at timestamptz,
	error_code text,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (order_id, id),
	UNIQUE (order_id, sequence_no)
);

CREATE FUNCTION public.yclients_operations_before_update() RETURNS trigger AS $$
BEGIN
	IF ROW(NEW.order_id, NEW.id, NEW.sequence_no, NEW.kind, NEW.request, NEW.created_at)
		IS DISTINCT FROM ROW(OLD.order_id, OLD.id, OLD.sequence_no, OLD.kind, OLD.request, OLD.created_at) THEN
		RAISE EXCEPTION 'YClients operation identity and requests are immutable' USING ERRCODE = '23514';
	END IF;
	IF OLD.dispatched_at IS NOT NULL AND NEW.dispatched_at IS DISTINCT FROM OLD.dispatched_at THEN
		RAISE EXCEPTION 'YClients dispatch markers cannot be replaced' USING ERRCODE = '23514';
	END IF;
	NEW.updated_at := now();
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER yclients_operations_before_update BEFORE UPDATE ON public.yclients_operations
	FOR EACH ROW EXECUTE FUNCTION public.yclients_operations_before_update();

ALTER TABLE public.fiscal_receipts ADD COLUMN dispatch_started_at timestamptz;

-- Existing ambiguous/completed physical work must never become an undispatched
-- request merely because the marker was introduced after the operation.
UPDATE public.fiscal_receipts SET dispatch_started_at = COALESCE(claimed_at, created_at)
	WHERE state IN ('processing', 'unknown', 'issued');

CREATE FUNCTION public.fiscal_receipts_dispatch_before_update() RETURNS trigger AS $$
BEGIN
	IF OLD.dispatch_started_at IS NOT NULL AND NEW.dispatch_started_at IS DISTINCT FROM OLD.dispatch_started_at THEN
		RAISE EXCEPTION 'fiscal dispatch markers cannot be replaced' USING ERRCODE = '23514';
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER fiscal_receipts_dispatch_before_update BEFORE UPDATE ON public.fiscal_receipts
	FOR EACH ROW EXECUTE FUNCTION public.fiscal_receipts_dispatch_before_update();

COMMIT;
