BEGIN;

ALTER TABLE public.payment_profiles
	ADD COLUMN cards_enabled boolean NOT NULL DEFAULT false,
	ADD COLUMN acquiring_merchant_id text CHECK (acquiring_merchant_id ~ '^[0-9]{15}$'),
	ADD COLUMN card_ttl_minutes integer NOT NULL DEFAULT 15 CHECK (card_ttl_minutes BETWEEN 1 AND 44640),
	ADD COLUMN card_redirect_url text CHECK (length(card_redirect_url) <= 2083 AND card_redirect_url ~ '^https?://[^[:space:]]+$'),
	ADD COLUMN card_fail_redirect_url text CHECK (length(card_fail_redirect_url) <= 2083 AND card_fail_redirect_url ~ '^https?://[^[:space:]]+$'),
	ADD CONSTRAINT payment_profiles_card_configuration_check CHECK (
		NOT cards_enabled OR acquiring_merchant_id IS NOT NULL
	),
	DROP CONSTRAINT payment_profiles_enabled_configuration_check,
	ADD CONSTRAINT payment_profiles_enabled_configuration_check CHECK (
		NOT enabled OR (
			(jwt_ciphertext IS NOT NULL OR jwt_env IS NOT NULL)
			AND nullif(btrim(customer_code), '') IS NOT NULL
			AND ((nullif(btrim(sbp_merchant_id), '') IS NOT NULL AND nullif(btrim(account_id), '') IS NOT NULL)
				OR cards_enabled)
		)
	);

ALTER TABLE public.payments
	ADD COLUMN method text NOT NULL DEFAULT 'sbp' CHECK (method IN ('sbp', 'card')),
	ADD COLUMN acquiring_operation_id text CHECK (length(acquiring_operation_id) BETWEEN 1 AND 128),
	ADD CONSTRAINT payments_method_identifiers_check CHECK (
		(method = 'sbp' AND acquiring_operation_id IS NULL)
		OR (method = 'card' AND qrc_id IS NULL AND qr_image IS NULL AND qr_image_media_type IS NULL)
	);

CREATE UNIQUE INDEX payments_production_acquiring_operation_key
	ON public.payments (payment_profile_id, acquiring_operation_id)
	WHERE environment = 'production' AND method = 'card' AND acquiring_operation_id IS NOT NULL;

-- SBP and acquiring identifiers come from separate bank products.
DROP INDEX public.payments_paid_bank_reference_key;
CREATE UNIQUE INDEX payments_paid_bank_reference_key
	ON public.payments (payment_profile_id, method, ref_transaction_id)
	WHERE state = 'paid' AND ref_transaction_id IS NOT NULL;
DROP INDEX public.payments_paid_bank_operation_key;
CREATE UNIQUE INDEX payments_paid_bank_operation_key
	ON public.payments (payment_profile_id, method, transaction_id)
	WHERE state = 'paid' AND environment = 'production' AND transaction_id IS NOT NULL;

CREATE FUNCTION public.payments_card_identity_before_update() RETURNS trigger AS $$
BEGIN
	IF NEW.method IS DISTINCT FROM OLD.method OR (
		OLD.acquiring_operation_id IS NOT NULL
		AND NEW.acquiring_operation_id IS DISTINCT FROM OLD.acquiring_operation_id
	) THEN
		RAISE EXCEPTION 'payment method and assigned acquiring operation are immutable' USING ERRCODE = '23514';
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER payments_card_identity_before_update BEFORE UPDATE ON public.payments
	FOR EACH ROW EXECUTE FUNCTION public.payments_card_identity_before_update();

CREATE OR REPLACE FUNCTION public.payment_profiles_before_update() RETURNS trigger AS $$
BEGIN
	IF ROW(NEW.id, NEW.company_id, NEW.provider, NEW.environment, NEW.api_base_url,
		NEW.customer_code, NEW.sbp_merchant_id, NEW.account_id)
		IS DISTINCT FROM
		ROW(OLD.id, OLD.company_id, OLD.provider, OLD.environment, OLD.api_base_url,
		OLD.customer_code, OLD.sbp_merchant_id, OLD.account_id)
		AND EXISTS (SELECT 1 FROM public.payments WHERE payment_profile_id = OLD.id) THEN
		RAISE EXCEPTION 'bank identity of a used payment profile is immutable; create another profile' USING ERRCODE = '23514';
	END IF;
	IF NEW.acquiring_merchant_id IS DISTINCT FROM OLD.acquiring_merchant_id
		AND EXISTS (SELECT 1 FROM public.payments WHERE payment_profile_id = OLD.id AND method = 'card') THEN
		RAISE EXCEPTION 'acquiring identity of a used card profile is immutable; create another profile' USING ERRCODE = '23514';
	END IF;
	IF ROW(NEW.company_id, NEW.name, NEW.provider, NEW.environment, NEW.api_base_url,
		NEW.jwt_ciphertext, NEW.jwt_env, NEW.client_id, NEW.customer_code, NEW.sbp_merchant_id,
		NEW.account_id, NEW.qr_ttl_minutes, NEW.http_timeout_seconds, NEW.enabled,
		NEW.cards_enabled, NEW.acquiring_merchant_id, NEW.card_ttl_minutes, NEW.card_redirect_url, NEW.card_fail_redirect_url)
		IS DISTINCT FROM
		ROW(OLD.company_id, OLD.name, OLD.provider, OLD.environment, OLD.api_base_url,
		OLD.jwt_ciphertext, OLD.jwt_env, OLD.client_id, OLD.customer_code, OLD.sbp_merchant_id,
		OLD.account_id, OLD.qr_ttl_minutes, OLD.http_timeout_seconds, OLD.enabled,
		OLD.cards_enabled, OLD.acquiring_merchant_id, OLD.card_ttl_minutes, OLD.card_redirect_url, OLD.card_fail_redirect_url) THEN
		NEW.config_version := OLD.config_version + 1;
	ELSE
		NEW.config_version := OLD.config_version;
	END IF;
	NEW.updated_at := now();
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE public.bank_card_webhook_inbox (
	event_key text PRIMARY KEY CHECK (length(event_key) BETWEEN 1 AND 256),
	payment_profile_id bigint REFERENCES public.payment_profiles (id),
	payload jsonb NOT NULL,
	matching_snapshot jsonb NOT NULL,
	state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'processed', 'ignored')),
	received_at timestamptz NOT NULL DEFAULT now(),
	next_matching_at timestamptz NOT NULL DEFAULT now(),
	processing_error text
);
CREATE INDEX bank_card_webhook_pending_idx ON public.bank_card_webhook_inbox(next_matching_at, received_at)
	WHERE state = 'pending';

COMMIT;
