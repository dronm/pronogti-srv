BEGIN;

ALTER TABLE public.payment_orders ADD COLUMN customer_name text NOT NULL DEFAULT '';

CREATE OR REPLACE FUNCTION public.payment_orders_completion_before_update() RETURNS trigger AS $$
BEGIN
	IF NEW.customer_contact IS DISTINCT FROM OLD.customer_contact
		OR NEW.customer_name IS DISTINCT FROM OLD.customer_name
		OR NEW.yclients_snapshot IS DISTINCT FROM OLD.yclients_snapshot THEN
		RAISE EXCEPTION 'checkout customer contact and YClients snapshots are immutable' USING ERRCODE = '23514';
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Keep the existing list columns in place for the dependent detail view.
CREATE OR REPLACE VIEW public.admin_payment_orders_list AS
SELECT
	o.id::text AS id,
	o.company_id,
	s.name AS salon_name,
	o.kiosk_id,
	k.name AS kiosk_name,
	o.entry_mode,
	o.payable_kopecks,
	o.currency,
	o.state,
	o.created_at,
	a.id::text AS payment_id,
	a.method AS payment_method,
	a.state AS payment_state,
	a.environment,
	a.paid_at,
	COALESCE(NULLIF(a.ref_transaction_id, ''), NULLIF(a.transaction_id, ''), NULLIF(a.acquiring_operation_id, '')) AS bank_reference,
	o.yclients_sync_status,
	COALESCE(f.state, 'not_started') AS fiscal_status,
	NULLIF(o.customer_name, '') AS client_name,
	CASE WHEN o.customer_contact ~ '^[+]?[78][0-9]{10}$'
		THEN '7' || right(o.customer_contact, 10) END AS client_phone
FROM public.payment_orders o
JOIN public.salons s ON s.company_id = o.company_id
LEFT JOIN public.kiosks k ON k.id = o.kiosk_id
LEFT JOIN public.fiscal_receipts f ON f.order_id = o.id
LEFT JOIN LATERAL (
	SELECT p.id, p.method, p.state, p.environment, p.paid_at,
		p.ref_transaction_id, p.transaction_id, p.acquiring_operation_id
	FROM public.payments p
	WHERE p.order_id = o.id AND p.company_id = o.company_id
	ORDER BY CASE
		WHEN p.state = 'paid' THEN 0
		WHEN p.state IN ('creating', 'pending', 'unknown') THEN 1
		ELSE 2
	END, p.created_at DESC, p.id DESC
	LIMIT 1
) a ON true;

COMMIT;
