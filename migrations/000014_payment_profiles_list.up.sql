BEGIN;

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

COMMIT;
