BEGIN;

CREATE TABLE public.kiosks (
	id bigserial PRIMARY KEY,
	name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
	company_id bigint NOT NULL CHECK (company_id > 0),
	token_hash text NOT NULL UNIQUE CHECK (token_hash ~ '^[0-9a-f]{64}$'),
	is_active boolean NOT NULL DEFAULT true,
	session_version bigint NOT NULL DEFAULT 1 CHECK (session_version > 0),
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX kiosks_company_id_idx ON public.kiosks (company_id);

CREATE FUNCTION public.kiosks_before_update() RETURNS trigger AS $$
BEGIN
	NEW.updated_at := now();
	IF NEW.company_id IS DISTINCT FROM OLD.company_id
		OR NEW.token_hash IS DISTINCT FROM OLD.token_hash
		OR NEW.is_active IS DISTINCT FROM OLD.is_active THEN
		NEW.session_version := OLD.session_version + 1;
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER kiosks_before_update
	BEFORE UPDATE ON public.kiosks
	FOR EACH ROW EXECUTE FUNCTION public.kiosks_before_update();

COMMIT;
