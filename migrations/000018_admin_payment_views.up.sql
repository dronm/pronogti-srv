BEGIN;

-- Admin collections use text keys. Keep the payment joins on their native UUIDs.
CREATE INDEX payment_orders_admin_id_idx ON public.payment_orders ((id::text));
CREATE INDEX payments_admin_id_idx ON public.payments ((id::text));
CREATE INDEX payments_admin_order_idx ON public.payments ((order_id::text), created_at DESC);
CREATE INDEX payment_events_admin_payment_idx ON public.payment_events ((payment_id::text), received_at DESC);

CREATE VIEW public.admin_payment_orders_list AS
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
	COALESCE(f.state, 'not_started') AS fiscal_status
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

CREATE VIEW public.admin_payment_orders_detail AS
SELECT
	l.*,
	o.client_id,
	o.visit_id,
	o.record_id,
	items.items_snapshot,
	o.subtotal_kopecks,
	o.bonus_kopecks,
	o.expires_at,
	o.updated_at,
	y.error_code AS yclients_error_code,
	y.completed_at AS yclients_completed_at,
	f.error_code AS fiscal_error_code,
	f.completed_at AS fiscal_completed_at,
	documents.yclients_document_ids,
	CASE
		WHEN f.state = 'issued'
			AND jsonb_typeof(f.response_payload #> '{data,receipt,documentNumber}') = 'number'
			AND (f.response_payload #>> '{data,receipt,documentNumber}') ~ '^[0-9]{1,10}$'
		THEN f.response_payload #>> '{data,receipt,documentNumber}'
	END AS fiscal_document_number
FROM public.payment_orders o
JOIN public.admin_payment_orders_list l ON l.id = o.id::text
LEFT JOIN public.yclients_jobs y ON y.order_id = o.id
LEFT JOIN public.fiscal_receipts f ON f.order_id = o.id
CROSS JOIN LATERAL (
	SELECT jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
		'source', i.value -> 'source',
		'kind', i.value -> 'kind',
		'id', i.value -> 'id',
		'line_id', i.value -> 'line_id',
		'staff_id', i.value -> 'staff_id',
		'title', i.value -> 'title',
		'staff_name', i.value -> 'staff_name',
		'quantity', i.value -> 'quantity',
		'unit_price_kopecks', i.value -> 'unit_price_kopecks',
		'amount_kopecks', i.value -> 'amount_kopecks'
	)) ORDER BY i.position) AS items_snapshot
	FROM jsonb_array_elements(o.items_snapshot) WITH ORDINALITY AS i(value, position)
) items
CROSS JOIN LATERAL (
	SELECT string_agg(DISTINCT op.result ->> 'document_id', ', '
		ORDER BY op.result ->> 'document_id') AS yclients_document_ids
	FROM public.yclients_operations op
	WHERE op.order_id = o.id AND op.state = 'succeeded'
		AND jsonb_typeof(op.result -> 'document_id') = 'number'
		AND (op.result ->> 'document_id') ~ '^[1-9][0-9]{0,18}$'
) documents;

CREATE VIEW public.admin_payments AS
SELECT
	p.id::text AS id,
	p.order_id::text AS order_id,
	p.company_id,
	s.name AS salon_name,
	p.payment_profile_id,
	profile.name AS profile_name,
	p.environment,
	p.method,
	p.state,
	p.currency,
	p.amount_kopecks,
	p.created_at,
	p.updated_at,
	p.paid_at,
	p.expires_at,
	p.last_checked_at,
	p.next_check_at,
	p.qrc_id,
	p.acquiring_operation_id,
	p.bank_status,
	p.transaction_id,
	p.ref_transaction_id,
	COALESCE(NULLIF(p.ref_transaction_id, ''), NULLIF(p.transaction_id, ''), NULLIF(p.acquiring_operation_id, '')) AS bank_reference,
	p.error_details ->> 'code' AS error_code
FROM public.payments p
JOIN public.salons s ON s.company_id = p.company_id
JOIN public.payment_profiles profile ON profile.id = p.payment_profile_id
	AND profile.company_id = p.company_id AND profile.environment = p.environment;

CREATE VIEW public.admin_payment_events AS
SELECT
	e.id,
	e.payment_id::text AS payment_id,
	p.order_id::text AS order_id,
	COALESCE(p.company_id, profile.company_id) AS company_id,
	s.name AS salon_name,
	COALESCE(e.payment_profile_id, p.payment_profile_id) AS payment_profile_id,
	e.provider,
	e.environment,
	e.event_type,
	e.source,
	e.processing_state,
	e.received_at,
	e.next_matching_at,
	e.processed_at,
	e.processing_error,
	e.signature_verified
FROM public.payment_events e
LEFT JOIN public.payments p ON p.id = e.payment_id AND p.environment = e.environment
LEFT JOIN public.payment_profiles profile ON profile.id = e.payment_profile_id
LEFT JOIN public.salons s ON s.company_id = COALESCE(p.company_id, profile.company_id);

COMMIT;
