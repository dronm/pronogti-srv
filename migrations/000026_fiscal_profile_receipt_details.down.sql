BEGIN;

ALTER TABLE public.fiscal_profiles
	DROP COLUMN automatic_device_number,
	DROP COLUMN time_zone,
	DROP COLUMN send_settlement_details;

COMMIT;
