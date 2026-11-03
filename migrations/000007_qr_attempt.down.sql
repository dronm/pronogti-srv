BEGIN;

DROP TRIGGER payments_profile_snapshot_before_update ON public.payments;
DROP FUNCTION public.payments_profile_snapshot_before_update();
DROP TRIGGER payments_profile_snapshot_before_insert ON public.payments;
DROP FUNCTION public.payments_profile_snapshot_before_insert();
ALTER TABLE public.payments DROP COLUMN profile_snapshot;

-- Rolling back cannot leave an enabled profile without the original encrypted
-- credential format. Disable env-only profiles before restoring the old check.
UPDATE public.payment_profiles SET enabled = false
	WHERE enabled AND jwt_ciphertext IS NULL;

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

ALTER TABLE public.payment_profiles
	DROP CONSTRAINT payment_profiles_enabled_configuration_check,
	DROP COLUMN jwt_env,
	ADD CONSTRAINT payment_profiles_enabled_configuration_check CHECK (
		NOT enabled OR (
			jwt_ciphertext IS NOT NULL
			AND nullif(btrim(customer_code), '') IS NOT NULL
			AND nullif(btrim(sbp_merchant_id), '') IS NOT NULL
			AND nullif(btrim(account_id), '') IS NOT NULL
		)
	);

COMMIT;
