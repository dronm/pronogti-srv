BEGIN;

CREATE VIEW public.salons_detail AS
SELECT
	s.company_id,
	s.slug,
	s.name,
	s.is_active,
	s.session_version,
	s.sort_order,
	s.created_at,
	s.updated_at,
	s.default_payment_profile_id,
	s.default_fiscal_profile_id,
	s.default_yclients_profile_id,
	json_build_object(
		'keys', json_build_object('id', s.company_id),
		'descr', COALESCE(c.name, s.name),
		'dataType', 'companies'
	) AS company,
	CASE WHEN p.id IS NOT NULL THEN json_build_object(
		'keys', json_build_object('id', p.id),
		'descr', p.name,
		'dataType', 'payment_profiles'
	) END AS default_payment_profile,
	CASE WHEN f.id IS NOT NULL THEN json_build_object(
		'keys', json_build_object('id', f.id),
		'descr', f.name,
		'dataType', 'fiscal_profiles'
	) END AS default_fiscal_profile,
	CASE WHEN y.id IS NOT NULL THEN json_build_object(
		'keys', json_build_object('id', y.id),
		'descr', y.name,
		'dataType', 'yclients_profiles'
	) END AS default_yclients_profile
FROM public.salons s
LEFT JOIN public.companies c ON c.id = s.company_id
LEFT JOIN public.payment_profiles p
	ON p.id = s.default_payment_profile_id AND p.company_id = s.company_id
LEFT JOIN public.fiscal_profiles f
	ON f.id = s.default_fiscal_profile_id AND f.company_id = s.company_id
LEFT JOIN public.yclients_profiles y
	ON y.id = s.default_yclients_profile_id AND y.company_id = s.company_id;

COMMIT;
