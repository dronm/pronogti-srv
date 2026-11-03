BEGIN;

-- The receipt driver runs separately from the web backend. A profile identifies
-- one logical device in that service; demo profiles never contact the service.
CREATE TABLE public.fiscal_profiles (
	id bigserial PRIMARY KEY,
	company_id bigint NOT NULL REFERENCES public.salons (company_id),
	name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
	enabled boolean NOT NULL DEFAULT true,
	mode text NOT NULL DEFAULT 'demo' CHECK (mode IN ('demo', 'live')),
	driver_base_url text CHECK (driver_base_url ~ '^https?://[^[:space:]]+$'),
	device_id text CHECK (device_id ~ '^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$'),
	-- Store an environment-variable name, never a plaintext bearer token.
	bearer_token_env text CHECK (bearer_token_env ~ '^[A-Za-z_][A-Za-z0-9_]*$'),
	timeout_seconds integer NOT NULL DEFAULT 100 CHECK (timeout_seconds BETWEEN 1 AND 300),
	cashier_name text NOT NULL DEFAULT 'Cashier' CHECK (length(btrim(cashier_name)) BETWEEN 1 AND 64),
	cashier_inn text CHECK (cashier_inn ~ '^[0-9]{12}$'),
	tax_system integer NOT NULL DEFAULT 1 CHECK (tax_system IN (1, 2, 4, 16, 32)),
	service_vat_rate integer NOT NULL DEFAULT 6 CHECK (service_vat_rate BETWEEN 1 AND 12),
	good_vat_rate integer NOT NULL DEFAULT 6 CHECK (good_vat_rate BETWEEN 1 AND 12),
	payment_method integer NOT NULL DEFAULT 4 CHECK (payment_method BETWEEN 1 AND 7),
	settlement_address text,
	settlement_place text,
	internet_payment boolean NOT NULL DEFAULT false,
	suppress_print boolean NOT NULL DEFAULT false,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT fiscal_profiles_id_company_key UNIQUE (id, company_id),
	CONSTRAINT fiscal_profiles_live_configuration_check CHECK (
		mode <> 'live' OR (driver_base_url IS NOT NULL AND device_id IS NOT NULL)
	),
	CONSTRAINT fiscal_profiles_internet_place_check CHECK (
		NOT internet_payment OR nullif(btrim(settlement_place), '') IS NOT NULL
	)
);

ALTER TABLE public.kiosks
	ADD COLUMN fiscal_profile_id bigint,
	ADD CONSTRAINT kiosks_fiscal_profile_company_fkey
		FOREIGN KEY (fiscal_profile_id, company_id)
		REFERENCES public.fiscal_profiles (id, company_id);

ALTER TABLE public.salons
	ADD COLUMN default_fiscal_profile_id bigint,
	ADD CONSTRAINT salons_fiscal_profile_company_fkey
		FOREIGN KEY (default_fiscal_profile_id, company_id)
		REFERENCES public.fiscal_profiles (id, company_id);

CREATE FUNCTION public.kiosks_clear_fiscal_profile() RETURNS trigger AS $$
BEGIN
	IF NEW.company_id IS DISTINCT FROM OLD.company_id
		AND NEW.fiscal_profile_id IS NOT DISTINCT FROM OLD.fiscal_profile_id THEN
		NEW.fiscal_profile_id := NULL;
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER kiosks_clear_fiscal_profile
	BEFORE UPDATE ON public.kiosks
	FOR EACH ROW EXECUTE FUNCTION public.kiosks_clear_fiscal_profile();

INSERT INTO public.fiscal_profiles (company_id, name)
	SELECT company_id, 'Demo receipts' FROM public.salons;

UPDATE public.salons s SET default_fiscal_profile_id = p.id
	FROM public.fiscal_profiles p
	WHERE p.company_id = s.company_id AND s.default_fiscal_profile_id IS NULL;

-- Freeze fiscal routing/settings before payment, so a later mode or device
-- change cannot turn an already prepared demo checkout into a live receipt.
ALTER TABLE public.payment_orders ADD COLUMN fiscal_snapshot jsonb;

CREATE FUNCTION public.fiscal_demo_snapshot(selected_company_id bigint) RETURNS jsonb AS $$
	SELECT jsonb_build_object(
		'id', NULL, 'company_id', selected_company_id,
		'name', 'Unconfigured demo', 'enabled', true, 'mode', 'demo',
		'timeout_seconds', 100, 'cashier_name', 'Cashier',
		'tax_system', 1, 'service_vat_rate', 6, 'good_vat_rate', 6,
		'payment_method', 4, 'internet_payment', false, 'suppress_print', false
	);
$$ LANGUAGE sql IMMUTABLE;

-- Existing quotes predate fiscal configuration and must remain safe to test.
UPDATE public.payment_orders
	SET fiscal_snapshot = public.fiscal_demo_snapshot(company_id);

ALTER TABLE public.payment_orders
	ALTER COLUMN fiscal_snapshot SET NOT NULL,
	ADD CONSTRAINT payment_orders_fiscal_snapshot_check CHECK (
		jsonb_typeof(fiscal_snapshot) = 'object'
		AND fiscal_snapshot ->> 'mode' IN ('demo', 'live')
	);

CREATE FUNCTION public.payment_orders_fiscal_before_insert() RETURNS trigger AS $$
DECLARE
	selected_profile_id bigint;
	selected_profile public.fiscal_profiles%ROWTYPE;
BEGIN
	SELECT COALESCE(k.fiscal_profile_id, s.default_fiscal_profile_id)
	INTO selected_profile_id
	FROM public.salons s
	LEFT JOIN public.kiosks k ON k.id = NEW.kiosk_id AND k.company_id = NEW.company_id
	WHERE s.company_id = NEW.company_id;
	IF selected_profile_id IS NULL THEN
		-- New salons without an explicit profile remain in demo mode.
		NEW.fiscal_snapshot := public.fiscal_demo_snapshot(NEW.company_id);
	ELSE
		SELECT * INTO selected_profile FROM public.fiscal_profiles
		WHERE id = selected_profile_id AND company_id = NEW.company_id;
		IF NOT FOUND OR NOT selected_profile.enabled THEN
			RAISE EXCEPTION 'checkout fiscal profile is unavailable or disabled' USING ERRCODE = '23514';
		END IF;
		NEW.fiscal_snapshot := to_jsonb(selected_profile) - 'created_at' - 'updated_at';
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payment_orders_fiscal_before_insert
	BEFORE INSERT ON public.payment_orders
	FOR EACH ROW EXECUTE FUNCTION public.payment_orders_fiscal_before_insert();

CREATE FUNCTION public.payment_orders_fiscal_before_update() RETURNS trigger AS $$
BEGIN
	IF NEW.fiscal_snapshot IS DISTINCT FROM OLD.fiscal_snapshot THEN
		RAISE EXCEPTION 'checkout fiscal snapshots are immutable' USING ERRCODE = '23514';
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payment_orders_fiscal_before_update
	BEFORE UPDATE ON public.payment_orders
	FOR EACH ROW EXECUTE FUNCTION public.payment_orders_fiscal_before_update();

ALTER TABLE public.payments
	ADD CONSTRAINT payments_id_order_company_environment_key UNIQUE (id, order_id, company_id, environment);

CREATE TABLE public.fiscal_receipts (
	order_id uuid PRIMARY KEY,
	company_id bigint NOT NULL,
	payment_id uuid NOT NULL,
	environment text NOT NULL CHECK (environment IN ('mock', 'sandbox', 'production')),
	fiscal_snapshot jsonb NOT NULL CHECK (jsonb_typeof(fiscal_snapshot) = 'object'),
	request_id text NOT NULL UNIQUE CHECK (request_id ~ '^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$'),
	state text NOT NULL DEFAULT 'pending' CHECK (
		state IN ('pending', 'processing', 'demo', 'issued', 'failed', 'unknown')
	),
	request_payload jsonb CHECK (jsonb_typeof(request_payload) = 'object'),
	response_payload jsonb CHECK (jsonb_typeof(response_payload) = 'object'),
	error_code text,
	claimed_at timestamptz,
	completed_at timestamptz,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT fiscal_receipts_order_company_fkey FOREIGN KEY (order_id, company_id)
		REFERENCES public.payment_orders (id, company_id),
	CONSTRAINT fiscal_receipts_payment_identity_fkey FOREIGN KEY (payment_id, order_id, company_id, environment)
		REFERENCES public.payments (id, order_id, company_id, environment)
);

CREATE INDEX fiscal_receipts_pending_idx ON public.fiscal_receipts (created_at)
	WHERE state = 'pending';
CREATE INDEX fiscal_receipts_reconciliation_idx ON public.fiscal_receipts (claimed_at)
	WHERE state IN ('processing', 'unknown');

-- A confirmed bank payment queues exactly one sale receipt for the order. This
-- trigger only writes the durable queue; it never performs device/network I/O.
CREATE FUNCTION public.payments_enqueue_fiscal_receipt() RETURNS trigger AS $$
BEGIN
	IF NEW.state = 'paid' THEN
		INSERT INTO public.fiscal_receipts (
			order_id, company_id, payment_id, environment, fiscal_snapshot, request_id
		)
		SELECT o.id, o.company_id, NEW.id, NEW.environment, o.fiscal_snapshot,
			'pronogti-sale-' || o.id::text
		FROM public.payment_orders o
		WHERE o.id = NEW.order_id AND o.company_id = NEW.company_id
		ON CONFLICT (order_id) DO NOTHING;
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payments_enqueue_fiscal_receipt
	AFTER INSERT OR UPDATE OF state ON public.payments
	FOR EACH ROW EXECUTE FUNCTION public.payments_enqueue_fiscal_receipt();

INSERT INTO public.fiscal_receipts (
	order_id, company_id, payment_id, environment, fiscal_snapshot, request_id
)
	SELECT DISTINCT ON (o.id)
		o.id, o.company_id, p.id, p.environment, o.fiscal_snapshot,
		'pronogti-sale-' || o.id::text
	FROM public.payment_orders o
	JOIN public.payments p ON p.order_id = o.id AND p.company_id = o.company_id
	WHERE p.state = 'paid'
	ORDER BY o.id, p.created_at, p.id;

CREATE FUNCTION public.fiscal_receipts_before_update() RETURNS trigger AS $$
BEGIN
	IF ROW(NEW.order_id, NEW.company_id, NEW.payment_id, NEW.environment,
		NEW.fiscal_snapshot, NEW.request_id, NEW.created_at)
		IS DISTINCT FROM
		ROW(OLD.order_id, OLD.company_id, OLD.payment_id, OLD.environment,
		OLD.fiscal_snapshot, OLD.request_id, OLD.created_at) THEN
		RAISE EXCEPTION 'fiscal receipt identities and snapshots are immutable' USING ERRCODE = '23514';
	END IF;
	IF OLD.request_payload IS NOT NULL AND NEW.request_payload IS DISTINCT FROM OLD.request_payload THEN
		RAISE EXCEPTION 'a fiscal receipt request payload cannot be replaced' USING ERRCODE = '23514';
	END IF;
	NEW.updated_at := now();
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER fiscal_receipts_before_update
	BEFORE UPDATE ON public.fiscal_receipts
	FOR EACH ROW EXECUTE FUNCTION public.fiscal_receipts_before_update();

CREATE FUNCTION public.fiscal_profiles_before_update() RETURNS trigger AS $$
BEGIN
	NEW.updated_at := now();
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER fiscal_profiles_before_update
	BEFORE UPDATE ON public.fiscal_profiles
	FOR EACH ROW EXECUTE FUNCTION public.fiscal_profiles_before_update();

COMMIT;
