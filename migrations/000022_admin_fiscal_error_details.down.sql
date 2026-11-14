BEGIN;

DROP VIEW public.admin_payment_orders_detail;

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

COMMIT;
