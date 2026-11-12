BEGIN;

DROP VIEW public.admin_payment_events;
DROP VIEW public.admin_payments;
DROP VIEW public.admin_payment_orders_detail;
DROP VIEW public.admin_payment_orders_list;

DROP INDEX public.payment_events_admin_payment_idx;
DROP INDEX public.payments_admin_order_idx;
DROP INDEX public.payments_admin_id_idx;
DROP INDEX public.payment_orders_admin_id_idx;

COMMIT;
