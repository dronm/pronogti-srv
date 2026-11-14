-- Freeze request settings so retrying an unknown shift operation cannot be
-- redirected to another device after a profile or kiosk assignment is edited.
CREATE TABLE public.fiscal_shift_requests (
	request_hash text PRIMARY KEY CHECK (request_hash ~ '^[0-9a-f]{64}$'),
	scope text NOT NULL,
	profile_snapshot jsonb NOT NULL CHECK (jsonb_typeof(profile_snapshot) = 'object'),
	created_at timestamptz NOT NULL DEFAULT now()
);
