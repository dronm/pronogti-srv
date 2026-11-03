BEGIN;

DROP TRIGGER payment_profiles_before_update ON public.payment_profiles;
DROP FUNCTION public.payment_profiles_before_update();
DROP TABLE public.payment_events;
DROP TRIGGER payments_before_update ON public.payments;
DROP FUNCTION public.payments_before_update();
DROP TRIGGER payments_check_order_amount ON public.payments;
DROP FUNCTION public.payments_check_order_amount();
DROP TABLE public.payments;
DROP TRIGGER payment_orders_before_update ON public.payment_orders;
DROP FUNCTION public.payment_orders_before_update();
DROP TRIGGER payment_orders_before_insert ON public.payment_orders;
DROP FUNCTION public.payment_orders_before_insert();
DROP TABLE public.payment_orders;
DROP TRIGGER kiosks_clear_payment_profile ON public.kiosks;
DROP FUNCTION public.kiosks_clear_payment_profile();
ALTER TABLE public.salons
	DROP CONSTRAINT salons_payment_profile_company_fkey,
	DROP COLUMN default_payment_profile_id;
ALTER TABLE public.kiosks
	DROP CONSTRAINT kiosks_payment_profile_company_fkey,
	DROP COLUMN payment_profile_id,
	DROP CONSTRAINT kiosks_id_company_key;
DROP TABLE public.payment_profiles;

COMMIT;
