BEGIN;

CREATE TABLE public.salons (
	company_id bigint PRIMARY KEY CHECK (company_id > 0),
	slug text NOT NULL UNIQUE CHECK (length(slug) BETWEEN 1 AND 100 AND slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
	name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
	is_active boolean NOT NULL DEFAULT true,
	session_version bigint NOT NULL DEFAULT 1 CHECK (session_version > 0),
	sort_order integer NOT NULL DEFAULT 0,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE FUNCTION public.salons_before_update() RETURNS trigger AS $$
BEGIN
	NEW.updated_at := now();
	IF NEW.company_id IS DISTINCT FROM OLD.company_id
		OR NEW.slug IS DISTINCT FROM OLD.slug
		OR NEW.name IS DISTINCT FROM OLD.name
		OR NEW.is_active IS DISTINCT FROM OLD.is_active THEN
		NEW.session_version := OLD.session_version + 1;
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER salons_before_update
	BEFORE UPDATE ON public.salons
	FOR EACH ROW EXECUTE FUNCTION public.salons_before_update();

-- This company was explicitly selected for the initial Pronogti deployment.
-- Publish additional companies with the salon administration command.
INSERT INTO public.salons (company_id, slug, name)
VALUES (781898, 'company-781898', 'Salon 781898');

COMMIT;
