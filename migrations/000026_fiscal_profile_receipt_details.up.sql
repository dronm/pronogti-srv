BEGIN;

ALTER TABLE public.fiscal_profiles
	ADD COLUMN automatic_device_number text CHECK (char_length(btrim(automatic_device_number)) BETWEEN 1 AND 20),
	ADD COLUMN time_zone integer CHECK (time_zone BETWEEN 1 AND 11),
	ADD COLUMN send_settlement_details boolean NOT NULL DEFAULT false;

-- Profiles with an address or place already include those details on receipts.
UPDATE public.fiscal_profiles
SET send_settlement_details = true
WHERE NULLIF(btrim(settlement_address), '') IS NOT NULL
	OR NULLIF(btrim(settlement_place), '') IS NOT NULL;

COMMIT;
