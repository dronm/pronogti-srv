BEGIN;

-- Financial history must never be erased by a rollback. Restore the application
-- or reconcile/migrate terminal records explicitly before removing this feature.
DO $$
BEGIN
	IF EXISTS (SELECT 1 FROM public.payments WHERE method = 'terminal_card') THEN
		RAISE EXCEPTION 'cannot roll back terminal payments while terminal payment history exists';
	END IF;
	IF EXISTS (SELECT 1 FROM public.payment_profiles WHERE api_base_url = '') THEN
		RAISE EXCEPTION 'remove terminal-only payment profiles before rolling back terminal support';
	END IF;
END;
$$;

DROP TABLE public.terminal_agent_events;
DROP TABLE public.terminal_resolutions;
DROP TABLE public.terminal_commands;
DROP TABLE public.payment_terminals;
DROP TABLE public.terminal_agents;
DROP FUNCTION public.terminal_agent_events_before_update();
DROP FUNCTION public.terminal_resolutions_before_write();
DROP FUNCTION public.terminal_commands_before_write();
DROP FUNCTION public.payment_terminals_before_write();
DROP FUNCTION public.terminal_agents_before_write();

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

DROP VIEW public.payment_profiles_list;
CREATE VIEW public.payment_profiles_list AS
SELECT
	p.id,
	p.company_id,
	p.name,
	p.provider,
	p.environment,
	p.api_base_url,
	p.jwt_env,
	p.client_id,
	p.customer_code,
	p.sbp_merchant_id,
	p.account_id,
	p.qr_ttl_minutes,
	p.http_timeout_seconds,
	p.enabled,
	p.cards_enabled,
	p.acquiring_merchant_id,
	p.card_ttl_minutes,
	p.card_redirect_url,
	p.card_fail_redirect_url,
	p.config_version,
	p.created_at,
	p.updated_at,
	json_build_object(
		'keys', json_build_object('id', p.company_id),
		'descr', COALESCE(c.name, s.name),
		'dataType', 'companies'
	) AS company
FROM public.payment_profiles p
JOIN public.salons s ON s.company_id = p.company_id
LEFT JOIN public.companies c ON c.id = p.company_id;

ALTER TABLE public.payment_profiles
	ALTER COLUMN api_base_url DROP DEFAULT,
	DROP CONSTRAINT payment_profiles_api_base_url_check,
	ADD CONSTRAINT payment_profiles_api_base_url_check CHECK (api_base_url ~ '^https?://[^[:space:]]+$'),
	DROP CONSTRAINT payment_profiles_production_https_check,
	ADD CONSTRAINT payment_profiles_production_https_check CHECK (
		environment <> 'production' OR api_base_url LIKE 'https://%'
	),
	DROP CONSTRAINT payment_profiles_enabled_configuration_check,
	ADD CONSTRAINT payment_profiles_enabled_configuration_check CHECK (
		NOT enabled OR (
			(jwt_ciphertext IS NOT NULL OR jwt_env IS NOT NULL)
			AND nullif(btrim(customer_code), '') IS NOT NULL
			AND ((nullif(btrim(sbp_merchant_id), '') IS NOT NULL AND nullif(btrim(account_id), '') IS NOT NULL)
				OR cards_enabled)
		)
	),
	DROP COLUMN terminals_enabled;

ALTER TABLE public.payments
	DROP CONSTRAINT payments_method_check,
	ADD CONSTRAINT payments_method_check CHECK (method IN ('sbp', 'card')),
	DROP CONSTRAINT payments_method_identifiers_check,
	ADD CONSTRAINT payments_method_identifiers_check CHECK (
		(method = 'sbp' AND acquiring_operation_id IS NULL)
		OR (method = 'card' AND qrc_id IS NULL AND qr_image IS NULL AND qr_image_media_type IS NULL)
	);

DROP INDEX public.payments_paid_bank_reference_key;
CREATE UNIQUE INDEX payments_paid_bank_reference_key
	ON public.payments (payment_profile_id, method, ref_transaction_id)
	WHERE state = 'paid' AND ref_transaction_id IS NOT NULL;
DROP INDEX public.payments_paid_bank_operation_key;
CREATE UNIQUE INDEX payments_paid_bank_operation_key
	ON public.payments (payment_profile_id, method, transaction_id)
	WHERE state = 'paid' AND environment = 'production' AND transaction_id IS NOT NULL;

COMMIT;
