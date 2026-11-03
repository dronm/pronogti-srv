BEGIN;

DROP TABLE public.bank_webhook_inbox;

DROP TRIGGER fiscal_receipts_dispatch_before_update ON public.fiscal_receipts;
DROP FUNCTION public.fiscal_receipts_dispatch_before_update();
ALTER TABLE public.fiscal_receipts DROP COLUMN dispatch_started_at;

DROP TRIGGER yclients_operations_before_update ON public.yclients_operations;
DROP FUNCTION public.yclients_operations_before_update();
DROP TABLE public.yclients_operations;

DROP TRIGGER payments_enqueue_yclients_job ON public.payments;
DROP FUNCTION public.payments_enqueue_yclients_job();
DROP TRIGGER yclients_jobs_before_update ON public.yclients_jobs;
DROP FUNCTION public.yclients_jobs_before_update();
DROP TABLE public.yclients_jobs;

DROP INDEX public.payment_events_matching_due_idx;
ALTER TABLE public.payment_events
	DROP COLUMN next_matching_at,
	DROP COLUMN matching_snapshot,
	DROP COLUMN payment_profile_id;
-- Signature semantics are unchanged in both directions; no audit evidence is
-- deleted or relabeled as signature-verified during rollback.

DROP INDEX public.payments_paid_bank_operation_key;
DROP INDEX public.payments_paid_bank_reference_key;
DROP INDEX public.payments_production_qrc_key;
ALTER TABLE public.payments DROP COLUMN confirmation_claimed_at;

DROP INDEX public.payment_orders_one_pending_visit_idx;
CREATE UNIQUE INDEX payment_orders_one_pending_visit_idx
	ON public.payment_orders (company_id, visit_id, record_id)
	WHERE state = 'payment_pending' AND visit_id IS NOT NULL;

DROP TRIGGER payment_orders_completion_before_update ON public.payment_orders;
DROP FUNCTION public.payment_orders_completion_before_update();
DROP TRIGGER payment_orders_yclients_before_insert ON public.payment_orders;
DROP FUNCTION public.payment_orders_yclients_before_insert();

UPDATE public.payment_orders SET yclients_sync_status = CASE
	WHEN yclients_sync_status = 'processing' THEN 'pending'
	WHEN yclients_sync_status = 'demo' THEN 'succeeded'
	WHEN yclients_sync_status IN ('unknown', 'blocked') THEN 'failed'
	ELSE yclients_sync_status END;

ALTER TABLE public.payment_orders
	DROP CONSTRAINT payment_orders_yclients_sync_status_check,
	ADD CONSTRAINT payment_orders_yclients_sync_status_check CHECK (
		yclients_sync_status IN ('not_started', 'pending', 'succeeded', 'failed')
	),
	DROP COLUMN yclients_snapshot,
	DROP COLUMN customer_contact;

DROP FUNCTION public.yclients_demo_snapshot(bigint);
DROP TRIGGER kiosks_clear_yclients_profile ON public.kiosks;
DROP FUNCTION public.kiosks_clear_yclients_profile();
ALTER TABLE public.kiosks DROP COLUMN yclients_profile_id;
ALTER TABLE public.salons DROP COLUMN default_yclients_profile_id;
DROP TRIGGER yclients_profiles_before_update ON public.yclients_profiles;
DROP FUNCTION public.yclients_profiles_before_update();
DROP TABLE public.yclients_profiles;

COMMIT;
