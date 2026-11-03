BEGIN;

-- A profile may resolve its token from a deployment secret. Existing encrypted
-- credentials remain supported; environment-variable names are not credentials.
ALTER TABLE public.payment_profiles
	ADD COLUMN jwt_env text CHECK (jwt_env ~ '^[A-Za-z_][A-Za-z0-9_]*$'),
	DROP CONSTRAINT payment_profiles_enabled_configuration_check,
	ADD CONSTRAINT payment_profiles_enabled_configuration_check CHECK (
		NOT enabled OR (
			(jwt_ciphertext IS NOT NULL OR jwt_env IS NOT NULL)
			AND nullif(btrim(customer_code), '') IS NOT NULL
			AND nullif(btrim(sbp_merchant_id), '') IS NOT NULL
			AND nullif(btrim(account_id), '') IS NOT NULL
		)
	);

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
		NEW.jwt_ciphertext, NEW.jwt_env, NEW.client_id, NEW.customer_code, NEW.sbp_merchant_id,
		NEW.account_id, NEW.qr_ttl_minutes, NEW.http_timeout_seconds, NEW.enabled)
		IS DISTINCT FROM
		ROW(OLD.company_id, OLD.name, OLD.provider, OLD.environment, OLD.api_base_url,
		OLD.jwt_ciphertext, OLD.jwt_env, OLD.client_id, OLD.customer_code, OLD.sbp_merchant_id,
		OLD.account_id, OLD.qr_ttl_minutes, OLD.http_timeout_seconds, OLD.enabled) THEN
		NEW.config_version := OLD.config_version + 1;
	ELSE
		NEW.config_version := OLD.config_version;
	END IF;
	NEW.updated_at := now();
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

ALTER TABLE public.payments
	ADD COLUMN profile_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb
		CHECK (jsonb_typeof(profile_snapshot) = 'object');

-- Legacy attempts retain an empty snapshot. Every newly created attempt must
-- freeze the bank identity selected for this order without copying credentials.
CREATE FUNCTION public.payments_profile_snapshot_before_insert() RETURNS trigger AS $$
BEGIN
	IF (NEW.profile_snapshot->'id') IS DISTINCT FROM to_jsonb(NEW.payment_profile_id)
		OR (NEW.profile_snapshot->'company_id') IS DISTINCT FROM to_jsonb(NEW.company_id)
		OR (NEW.profile_snapshot->>'environment') IS DISTINCT FROM NEW.environment
		OR NEW.profile_snapshot ? 'jwt_ciphertext' OR NEW.profile_snapshot ? 'jwt_env' THEN
		RAISE EXCEPTION 'new payment attempts require a matching credential-free profile snapshot' USING ERRCODE = '23514';
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payments_profile_snapshot_before_insert
	BEFORE INSERT ON public.payments
	FOR EACH ROW EXECUTE FUNCTION public.payments_profile_snapshot_before_insert();

CREATE FUNCTION public.payments_profile_snapshot_before_update() RETURNS trigger AS $$
BEGIN
	IF NEW.profile_snapshot IS DISTINCT FROM OLD.profile_snapshot THEN
		RAISE EXCEPTION 'payment profile snapshots are immutable' USING ERRCODE = '23514';
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payments_profile_snapshot_before_update
	BEFORE UPDATE ON public.payments
	FOR EACH ROW EXECUTE FUNCTION public.payments_profile_snapshot_before_update();

COMMIT;
