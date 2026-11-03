BEGIN;

-- Dropping card identity while financial records exist would lose recovery data.
DO $$ BEGIN
	IF EXISTS (SELECT 1 FROM public.payments WHERE method = 'card') THEN
		RAISE EXCEPTION 'card payment records exist; retain migration 000009 and their recovery journals';
	END IF;
END; $$;

DROP TABLE public.bank_card_webhook_inbox;
DROP TRIGGER payments_card_identity_before_update ON public.payments;
DROP FUNCTION public.payments_card_identity_before_update();
DROP INDEX public.payments_production_acquiring_operation_key;
DROP INDEX public.payments_paid_bank_reference_key;
DROP INDEX public.payments_paid_bank_operation_key;
ALTER TABLE public.payments DROP CONSTRAINT payments_method_identifiers_check,
	DROP COLUMN acquiring_operation_id, DROP COLUMN method;
CREATE UNIQUE INDEX payments_paid_bank_reference_key
	ON public.payments(payment_profile_id,ref_transaction_id)
	WHERE state='paid' AND ref_transaction_id IS NOT NULL;
CREATE UNIQUE INDEX payments_paid_bank_operation_key
	ON public.payments(payment_profile_id,transaction_id)
	WHERE state='paid' AND environment='production' AND transaction_id IS NOT NULL;

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


ALTER TABLE public.payment_profiles DROP CONSTRAINT payment_profiles_card_configuration_check,
	DROP CONSTRAINT payment_profiles_enabled_configuration_check,
	ADD CONSTRAINT payment_profiles_enabled_configuration_check CHECK (
		NOT enabled OR ((jwt_ciphertext IS NOT NULL OR jwt_env IS NOT NULL)
			AND nullif(btrim(customer_code),'') IS NOT NULL
			AND nullif(btrim(sbp_merchant_id),'') IS NOT NULL
			AND nullif(btrim(account_id),'') IS NOT NULL)
	),
	DROP COLUMN cards_enabled, DROP COLUMN acquiring_merchant_id, DROP COLUMN card_ttl_minutes,
	DROP COLUMN card_redirect_url, DROP COLUMN card_fail_redirect_url;

COMMIT;
