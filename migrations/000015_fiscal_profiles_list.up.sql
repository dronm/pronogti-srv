BEGIN;

CREATE VIEW public.fiscal_profiles_list AS
SELECT
	p.id,
	p.company_id,
	p.name,
	p.enabled,
	p.mode,
	p.driver_base_url,
	p.device_id,
	p.bearer_token_env,
	p.timeout_seconds,
	p.cashier_name,
	p.cashier_inn,
	p.tax_system,
	p.service_vat_rate,
	p.good_vat_rate,
	p.payment_method,
	p.settlement_address,
	p.settlement_place,
	p.internet_payment,
	p.suppress_print,
	p.created_at,
	p.updated_at,
	json_build_object(
		'keys', json_build_object('id', p.company_id),
		'descr', COALESCE(c.name, s.name),
		'dataType', 'companies'
	) AS company
FROM public.fiscal_profiles p
JOIN public.salons s ON s.company_id = p.company_id
LEFT JOIN public.companies c ON c.id = p.company_id;

COMMIT;
