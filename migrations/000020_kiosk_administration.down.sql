BEGIN;

CREATE OR REPLACE VIEW public.kiosks_list AS
SELECT
	k.id,
	k.name,
	k.company_id,
	k.is_active,
	k.payment_profile_id,
	k.fiscal_profile_id,
	k.yclients_profile_id,
	k.created_at,
	k.updated_at,
	json_build_object(
		'keys', json_build_object('id', k.company_id),
		'descr', COALESCE(c.name, s.name, '#' || k.company_id::text),
		'dataType', 'companies'
	) AS company,
	CASE WHEN p.id IS NOT NULL THEN json_build_object(
		'keys', json_build_object('id', p.id),
		'descr', p.name,
		'dataType', 'payment_profiles'
	) END AS payment_profile,
	CASE WHEN f.id IS NOT NULL THEN json_build_object(
		'keys', json_build_object('id', f.id),
		'descr', f.name,
		'dataType', 'fiscal_profiles'
	) END AS fiscal_profile,
	CASE WHEN y.id IS NOT NULL THEN json_build_object(
		'keys', json_build_object('id', y.id),
		'descr', y.name,
		'dataType', 'yclients_profiles'
	) END AS yclients_profile
FROM public.kiosks k
LEFT JOIN public.companies c ON c.id = k.company_id
LEFT JOIN public.salons s ON s.company_id = k.company_id
LEFT JOIN public.payment_profiles p
	ON p.id = k.payment_profile_id AND p.company_id = k.company_id
LEFT JOIN public.fiscal_profiles f
	ON f.id = k.fiscal_profile_id AND f.company_id = k.company_id
LEFT JOIN public.yclients_profiles y
	ON y.id = k.yclients_profile_id AND y.company_id = k.company_id;

ALTER TABLE public.kiosks
	DROP CONSTRAINT kiosks_archived_inactive_check,
	DROP COLUMN archived_at;

COMMIT;
