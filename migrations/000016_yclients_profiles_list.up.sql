BEGIN;

CREATE VIEW public.yclients_profiles_list AS
SELECT
	p.id,
	p.company_id,
	p.name,
	p.mode,
	p.enabled,
	p.cashless_account_id,
	p.storage_id,
	p.service_policy,
	p.service_income_expense_id,
	p.config_version,
	p.created_at,
	p.updated_at,
	json_build_object(
		'keys', json_build_object('id', p.company_id),
		'descr', COALESCE(c.name, s.name),
		'dataType', 'companies'
	) AS company
FROM public.yclients_profiles p
JOIN public.salons s ON s.company_id = p.company_id
LEFT JOIN public.companies c ON c.id = p.company_id;

COMMIT;
