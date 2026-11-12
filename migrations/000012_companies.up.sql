BEGIN;

CREATE TABLE public.companies (
	id bigint PRIMARY KEY CHECK (id > 0),
	name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200)
);

-- Reuse the existing YClients IDs; no internal surrogate ID or sequence.
INSERT INTO public.companies (id, name)
SELECT company_id, name FROM public.salons;

CREATE INDEX companies_name_idx ON public.companies (lower(name), id);

COMMIT;
