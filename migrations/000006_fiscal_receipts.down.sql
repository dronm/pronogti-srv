BEGIN;

DROP TRIGGER IF EXISTS payments_enqueue_fiscal_receipt ON public.payments;
DROP FUNCTION IF EXISTS public.payments_enqueue_fiscal_receipt();
DROP TABLE IF EXISTS public.fiscal_receipts;
DROP FUNCTION IF EXISTS public.fiscal_receipts_before_update();
ALTER TABLE public.payments DROP CONSTRAINT IF EXISTS payments_id_order_company_environment_key;

DROP TRIGGER IF EXISTS payment_orders_fiscal_before_insert ON public.payment_orders;
DROP TRIGGER IF EXISTS payment_orders_fiscal_before_update ON public.payment_orders;
DROP FUNCTION IF EXISTS public.payment_orders_fiscal_before_insert();
DROP FUNCTION IF EXISTS public.payment_orders_fiscal_before_update();
ALTER TABLE public.payment_orders DROP COLUMN IF EXISTS fiscal_snapshot;
DROP FUNCTION IF EXISTS public.fiscal_demo_snapshot(bigint);

DROP TRIGGER IF EXISTS kiosks_clear_fiscal_profile ON public.kiosks;
DROP FUNCTION IF EXISTS public.kiosks_clear_fiscal_profile();
ALTER TABLE public.kiosks
	DROP CONSTRAINT IF EXISTS kiosks_fiscal_profile_company_fkey,
	DROP COLUMN IF EXISTS fiscal_profile_id;
ALTER TABLE public.salons
	DROP CONSTRAINT IF EXISTS salons_fiscal_profile_company_fkey,
	DROP COLUMN IF EXISTS default_fiscal_profile_id;
DROP TABLE IF EXISTS public.fiscal_profiles;
DROP FUNCTION IF EXISTS public.fiscal_profiles_before_update();

COMMIT;
