BEGIN;

CREATE TABLE public.payment_profiles (
	id bigserial PRIMARY KEY,
	company_id bigint NOT NULL REFERENCES public.salons (company_id),
	name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
	provider text NOT NULL DEFAULT 'tochka' CHECK (provider = 'tochka'),
	environment text NOT NULL CHECK (environment IN ('mock', 'sandbox', 'production')),
	api_base_url text NOT NULL CHECK (api_base_url ~ '^https?://[^[:space:]]+$'),
	jwt_ciphertext bytea CHECK (jwt_ciphertext IS NULL OR octet_length(jwt_ciphertext) > 0),
	client_id text,
	customer_code text,
	sbp_merchant_id text,
	account_id text,
	qr_ttl_minutes integer NOT NULL DEFAULT 15 CHECK (qr_ttl_minutes BETWEEN 1 AND 129600),
	http_timeout_seconds integer NOT NULL DEFAULT 20 CHECK (http_timeout_seconds BETWEEN 1 AND 300),
	enabled boolean NOT NULL DEFAULT false,
	config_version bigint NOT NULL DEFAULT 1 CHECK (config_version > 0),
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT payment_profiles_id_company_key UNIQUE (id, company_id),
	CONSTRAINT payment_profiles_id_company_environment_key UNIQUE (id, company_id, environment),
	CONSTRAINT payment_profiles_enabled_configuration_check CHECK (
		NOT enabled OR (
			jwt_ciphertext IS NOT NULL
			AND nullif(btrim(customer_code), '') IS NOT NULL
			AND nullif(btrim(sbp_merchant_id), '') IS NOT NULL
			AND nullif(btrim(account_id), '') IS NOT NULL
		)
	),
	CONSTRAINT payment_profiles_production_https_check CHECK (
		environment <> 'production' OR api_base_url LIKE 'https://%'
	)
);

ALTER TABLE public.kiosks
	ADD CONSTRAINT kiosks_id_company_key UNIQUE (id, company_id),
	ADD COLUMN payment_profile_id bigint,
	ADD CONSTRAINT kiosks_payment_profile_company_fkey
		FOREIGN KEY (payment_profile_id, company_id)
		REFERENCES public.payment_profiles (id, company_id);

ALTER TABLE public.salons
	ADD COLUMN default_payment_profile_id bigint,
	ADD CONSTRAINT salons_payment_profile_company_fkey
		FOREIGN KEY (default_payment_profile_id, company_id)
		REFERENCES public.payment_profiles (id, company_id);

-- A device can be reassigned without rewriting its historical checkouts. Its
-- old company's bank profile must not follow it to the destination company.
CREATE FUNCTION public.kiosks_clear_payment_profile() RETURNS trigger AS $$
BEGIN
	IF NEW.company_id IS DISTINCT FROM OLD.company_id
		AND NEW.payment_profile_id IS NOT DISTINCT FROM OLD.payment_profile_id THEN
		NEW.payment_profile_id := NULL;
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER kiosks_clear_payment_profile
	BEFORE UPDATE ON public.kiosks
	FOR EACH ROW EXECUTE FUNCTION public.kiosks_clear_payment_profile();

CREATE TABLE public.payment_orders (
	id uuid PRIMARY KEY,
	company_id bigint NOT NULL REFERENCES public.salons (company_id),
	-- Validate company membership at insertion, rather than making historical
	-- records prevent the device from ever being reassigned to another company.
	kiosk_id bigint REFERENCES public.kiosks (id),
	entry_mode text NOT NULL CHECK (entry_mode IN ('public', 'kiosk')),
	client_id bigint CHECK (client_id > 0),
	scope_hash text NOT NULL CHECK (scope_hash ~ '^[0-9a-f]{64}$'),
	customer_session_hash text NOT NULL CHECK (customer_session_hash ~ '^[0-9a-f]{64}$'),
	visit_id bigint CHECK (visit_id > 0),
	record_id bigint CHECK (record_id > 0),
	items_snapshot jsonb NOT NULL CHECK (
		jsonb_typeof(items_snapshot) = 'array' AND jsonb_array_length(items_snapshot) > 0
	),
	subtotal_kopecks bigint NOT NULL CHECK (subtotal_kopecks BETWEEN 1 AND 9007199254740991),
	bonus_kopecks bigint NOT NULL DEFAULT 0 CHECK (bonus_kopecks >= 0),
	payable_kopecks bigint NOT NULL CHECK (payable_kopecks BETWEEN 1 AND 9007199254740991),
	currency text NOT NULL DEFAULT 'RUB' CHECK (currency = 'RUB'),
	state text NOT NULL DEFAULT 'prepared' CHECK (
		state IN ('prepared', 'payment_pending', 'paid', 'cancelled', 'expired')
	),
	idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
	request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
	yclients_sync_status text NOT NULL DEFAULT 'not_started' CHECK (
		yclients_sync_status IN ('not_started', 'pending', 'succeeded', 'failed')
	),
	expires_at timestamptz NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT payment_orders_id_company_key UNIQUE (id, company_id),
	CONSTRAINT payment_orders_owner_idempotency_key UNIQUE (company_id, customer_session_hash, idempotency_key),
	CONSTRAINT payment_orders_mode_kiosk_check CHECK (
		(entry_mode = 'kiosk' AND kiosk_id IS NOT NULL)
		OR (entry_mode = 'public' AND kiosk_id IS NULL)
	),
	CONSTRAINT payment_orders_visit_reference_check CHECK (
		(visit_id IS NULL AND record_id IS NULL)
		OR (visit_id IS NOT NULL AND record_id IS NOT NULL AND client_id IS NOT NULL)
	),
	CONSTRAINT payment_orders_payable_check CHECK (
		bonus_kopecks <= subtotal_kopecks AND payable_kopecks = subtotal_kopecks - bonus_kopecks
	),
	CONSTRAINT payment_orders_lifetime_check CHECK (expires_at > created_at)
);

CREATE INDEX payment_orders_owner_idx
	ON public.payment_orders (company_id, customer_session_hash, scope_hash);
CREATE INDEX payment_orders_pending_expiry_idx
	ON public.payment_orders (expires_at) WHERE state IN ('prepared', 'payment_pending');
-- Several quotes can exist, but a visit can have only one in-flight charge.
CREATE UNIQUE INDEX payment_orders_one_pending_visit_idx
	ON public.payment_orders (company_id, visit_id, record_id)
	WHERE state = 'payment_pending' AND visit_id IS NOT NULL;

CREATE FUNCTION public.payment_orders_before_insert() RETURNS trigger AS $$
BEGIN
	IF NEW.kiosk_id IS NOT NULL AND NOT EXISTS (
		SELECT 1 FROM public.kiosks WHERE id = NEW.kiosk_id AND company_id = NEW.company_id
	) THEN
		RAISE EXCEPTION 'checkout kiosk does not belong to the selected company' USING ERRCODE = '23514';
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payment_orders_before_insert
	BEFORE INSERT ON public.payment_orders
	FOR EACH ROW EXECUTE FUNCTION public.payment_orders_before_insert();

-- Quotes are append-only snapshots. Later stages may update payment/sync state,
-- but a change to items, owner, price, or lifetime requires a new checkout.
CREATE FUNCTION public.payment_orders_before_update() RETURNS trigger AS $$
BEGIN
	IF ROW(NEW.id, NEW.company_id, NEW.kiosk_id, NEW.entry_mode, NEW.client_id,
		NEW.scope_hash, NEW.customer_session_hash, NEW.visit_id, NEW.record_id,
		NEW.items_snapshot, NEW.subtotal_kopecks, NEW.bonus_kopecks, NEW.payable_kopecks,
		NEW.currency, NEW.idempotency_key, NEW.request_hash, NEW.expires_at, NEW.created_at)
		IS DISTINCT FROM
		ROW(OLD.id, OLD.company_id, OLD.kiosk_id, OLD.entry_mode, OLD.client_id,
		OLD.scope_hash, OLD.customer_session_hash, OLD.visit_id, OLD.record_id,
		OLD.items_snapshot, OLD.subtotal_kopecks, OLD.bonus_kopecks, OLD.payable_kopecks,
		OLD.currency, OLD.idempotency_key, OLD.request_hash, OLD.expires_at, OLD.created_at) THEN
		RAISE EXCEPTION 'checkout pricing snapshots are immutable' USING ERRCODE = '23514';
	END IF;
	NEW.updated_at := now();
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payment_orders_before_update
	BEFORE UPDATE ON public.payment_orders
	FOR EACH ROW EXECUTE FUNCTION public.payment_orders_before_update();

-- Bank attempts and the verified event inbox are infrastructure for the next
-- integration stage. Checkout preparation does not write either table.
CREATE TABLE public.payments (
	id uuid PRIMARY KEY,
	order_id uuid NOT NULL,
	company_id bigint NOT NULL,
	payment_profile_id bigint NOT NULL,
	environment text NOT NULL CHECK (environment IN ('mock', 'sandbox', 'production')),
	amount_kopecks bigint NOT NULL CHECK (amount_kopecks BETWEEN 1 AND 9007199254740991),
	currency text NOT NULL DEFAULT 'RUB' CHECK (currency = 'RUB'),
	state text NOT NULL DEFAULT 'creating' CHECK (
		state IN ('creating', 'pending', 'paid', 'failed', 'expired', 'unknown')
	),
	idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
	request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
	qrc_id text,
	bank_payload text,
	qr_image bytea,
	qr_image_media_type text CHECK (qr_image_media_type IN ('image/png', 'image/svg+xml')),
	bank_status text,
	transaction_id text,
	ref_transaction_id text,
	expires_at timestamptz,
	last_checked_at timestamptz,
	next_check_at timestamptz,
	paid_at timestamptz,
	error_details jsonb,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT payments_order_company_fkey FOREIGN KEY (order_id, company_id)
		REFERENCES public.payment_orders (id, company_id),
	CONSTRAINT payments_profile_company_environment_fkey
		FOREIGN KEY (payment_profile_id, company_id, environment)
		REFERENCES public.payment_profiles (id, company_id, environment),
	CONSTRAINT payments_id_environment_key UNIQUE (id, environment),
	CONSTRAINT payments_order_idempotency_key UNIQUE (order_id, idempotency_key),
	CONSTRAINT payments_qr_image_check CHECK (
		(qr_image IS NULL AND qr_image_media_type IS NULL)
		OR (qr_image IS NOT NULL AND qr_image_media_type IS NOT NULL)
	)
);

CREATE UNIQUE INDEX payments_one_active_attempt_idx
	ON public.payments (order_id) WHERE state IN ('creating', 'pending', 'unknown');
-- Sandbox examples may reuse a bank QR identifier. Never make it globally unique.
CREATE INDEX payments_profile_qrc_idx ON public.payments (payment_profile_id, qrc_id)
	WHERE qrc_id IS NOT NULL;
CREATE INDEX payments_reconciliation_idx ON public.payments (next_check_at)
	WHERE state IN ('creating', 'pending', 'unknown', 'expired');

CREATE FUNCTION public.payments_check_order_amount() RETURNS trigger AS $$
DECLARE
	expected_amount bigint;
	expected_currency text;
BEGIN
	SELECT payable_kopecks, currency INTO expected_amount, expected_currency
	FROM public.payment_orders
	WHERE id = NEW.order_id AND company_id = NEW.company_id;
	IF NOT FOUND THEN
		RAISE EXCEPTION 'payment checkout does not belong to the selected company' USING ERRCODE = '23503';
	END IF;
	IF NEW.amount_kopecks IS DISTINCT FROM expected_amount
		OR NEW.currency IS DISTINCT FROM expected_currency THEN
		RAISE EXCEPTION 'payment amount and currency must match the checkout' USING ERRCODE = '23514';
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payments_check_order_amount
	BEFORE INSERT OR UPDATE OF order_id, company_id, amount_kopecks, currency ON public.payments
	FOR EACH ROW EXECUTE FUNCTION public.payments_check_order_amount();

CREATE FUNCTION public.payments_before_update() RETURNS trigger AS $$
BEGIN
	IF ROW(NEW.id, NEW.order_id, NEW.company_id, NEW.payment_profile_id, NEW.environment,
		NEW.amount_kopecks, NEW.currency, NEW.idempotency_key, NEW.request_hash, NEW.created_at)
		IS DISTINCT FROM
		ROW(OLD.id, OLD.order_id, OLD.company_id, OLD.payment_profile_id, OLD.environment,
		OLD.amount_kopecks, OLD.currency, OLD.idempotency_key, OLD.request_hash, OLD.created_at) THEN
		RAISE EXCEPTION 'payment attempt identity and amount are immutable' USING ERRCODE = '23514';
	END IF;
	NEW.updated_at := now();
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payments_before_update
	BEFORE UPDATE ON public.payments
	FOR EACH ROW EXECUTE FUNCTION public.payments_before_update();

CREATE TABLE public.payment_events (
	id bigserial PRIMARY KEY,
	provider text NOT NULL DEFAULT 'tochka' CHECK (provider = 'tochka'),
	environment text NOT NULL CHECK (environment IN ('mock', 'sandbox', 'production')),
	payment_id uuid,
	event_type text NOT NULL CHECK (length(btrim(event_type)) BETWEEN 1 AND 100),
	source text NOT NULL DEFAULT 'webhook' CHECK (source IN ('webhook', 'reconciliation')),
	deduplication_key text NOT NULL CHECK (length(deduplication_key) BETWEEN 1 AND 256),
	signature_verified boolean NOT NULL DEFAULT true,
	payload jsonb NOT NULL,
	processing_state text NOT NULL DEFAULT 'pending' CHECK (
		processing_state IN ('pending', 'processed', 'failed', 'ignored')
	),
	received_at timestamptz NOT NULL DEFAULT now(),
	processed_at timestamptz,
	processing_error text,
	CONSTRAINT payment_events_payment_environment_fkey FOREIGN KEY (payment_id, environment)
		REFERENCES public.payments (id, environment),
	CONSTRAINT payment_events_bank_deduplication_key UNIQUE (provider, environment, deduplication_key),
	CONSTRAINT payment_events_verified_webhook_check CHECK (source <> 'webhook' OR signature_verified)
);

CREATE INDEX payment_events_pending_idx ON public.payment_events (received_at)
	WHERE processing_state IN ('pending', 'failed');

CREATE FUNCTION public.payment_profiles_before_update() RETURNS trigger AS $$
BEGIN
	IF ROW(NEW.id, NEW.company_id, NEW.provider, NEW.environment, NEW.api_base_url,
		NEW.customer_code, NEW.sbp_merchant_id, NEW.account_id)
		IS DISTINCT FROM
		ROW(OLD.id, OLD.company_id, OLD.provider, OLD.environment, OLD.api_base_url,
		OLD.customer_code, OLD.sbp_merchant_id, OLD.account_id)
		AND EXISTS (SELECT 1 FROM public.payments WHERE payment_profile_id = OLD.id) THEN
		RAISE EXCEPTION 'bank identity of a used payment profile is immutable; create another profile' USING ERRCODE = '23514';
	END IF;
	IF ROW(NEW.company_id, NEW.name, NEW.provider, NEW.environment, NEW.api_base_url,
		NEW.jwt_ciphertext, NEW.client_id, NEW.customer_code, NEW.sbp_merchant_id,
		NEW.account_id, NEW.qr_ttl_minutes, NEW.http_timeout_seconds, NEW.enabled)
		IS DISTINCT FROM
		ROW(OLD.company_id, OLD.name, OLD.provider, OLD.environment, OLD.api_base_url,
		OLD.jwt_ciphertext, OLD.client_id, OLD.customer_code, OLD.sbp_merchant_id,
		OLD.account_id, OLD.qr_ttl_minutes, OLD.http_timeout_seconds, OLD.enabled) THEN
		NEW.config_version := OLD.config_version + 1;
	ELSE
		NEW.config_version := OLD.config_version;
	END IF;
	NEW.updated_at := now();
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payment_profiles_before_update
	BEFORE UPDATE ON public.payment_profiles
	FOR EACH ROW EXECUTE FUNCTION public.payment_profiles_before_update();

COMMIT;
