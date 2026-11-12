ALTER TABLE public.fiscal_receipts
	ADD COLUMN email_attempts integer NOT NULL DEFAULT 0 CHECK (email_attempts >= 0),
	ADD COLUMN email_attempted_at timestamptz;
