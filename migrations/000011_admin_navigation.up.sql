BEGIN;

-- The registry describes SPA navigation. It does not grant API permissions.
CREATE TABLE public.application_routes (
	id serial PRIMARY KEY,
	name varchar(100) NOT NULL UNIQUE CHECK (name ~ '^[A-Za-z][A-Za-z0-9_-]{0,99}$'),
	path varchar(2000) NOT NULL CHECK (left(path, 1) = '/' AND left(path, 2) <> '//'),
	descr varchar(250) NOT NULL CHECK (btrim(descr) <> ''),
	section varchar(250) NOT NULL CHECK (btrim(section) <> ''),
	icon varchar(250),
	menu_available boolean NOT NULL DEFAULT false,
	is_active boolean NOT NULL DEFAULT true,
	created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE public.main_menus (
	id serial PRIMARY KEY,
	role_id role_types CHECK (role_id IS NULL OR role_id = 'admin'),
	user_id integer REFERENCES public.users(id) ON DELETE CASCADE,
	parent_id integer REFERENCES public.main_menus(id) ON DELETE CASCADE,
	caption varchar(250) NOT NULL CHECK (btrim(caption) <> ''),
	route_id integer REFERENCES public.application_routes(id) ON DELETE SET NULL,
	icon varchar(250),
	sort_order integer NOT NULL DEFAULT 0,
	is_active boolean NOT NULL DEFAULT true,
	created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
	CONSTRAINT main_menus_owner_check CHECK ((role_id IS NULL) <> (user_id IS NULL)),
	CONSTRAINT main_menus_parent_check CHECK (parent_id IS NULL OR parent_id <> id)
);

CREATE INDEX main_menus_role_idx ON public.main_menus(role_id, sort_order, id);
CREATE INDEX main_menus_user_idx ON public.main_menus(user_id, sort_order, id);
CREATE INDEX main_menus_parent_idx ON public.main_menus(parent_id);
CREATE INDEX main_menus_route_idx ON public.main_menus(route_id);

CREATE FUNCTION public.admin_navigation_updated_at() RETURNS trigger AS $$
BEGIN
	NEW.updated_at := CURRENT_TIMESTAMP;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER application_routes_updated_at BEFORE UPDATE ON public.application_routes
	FOR EACH ROW EXECUTE FUNCTION public.admin_navigation_updated_at();

CREATE FUNCTION public.main_menus_validate() RETURNS trigger AS $$
DECLARE
	parent public.main_menus%ROWTYPE;
BEGIN
	-- The service takes the same lock before reading a row for update. Taking
	-- it here also protects hierarchy checks for ordinary direct SQL writes.
	PERFORM pg_advisory_xact_lock(1886544231, 1);
	IF NEW.user_id IS NOT NULL AND NOT EXISTS (
		SELECT 1 FROM public.users WHERE id = NEW.user_id AND role_id = 'admin'
	) THEN
		RAISE EXCEPTION 'menu owner must be an administrator' USING ERRCODE = '23514';
	END IF;
	IF NEW.parent_id IS NOT NULL THEN
		SELECT * INTO parent FROM public.main_menus WHERE id = NEW.parent_id;
		IF NOT FOUND OR parent.role_id IS DISTINCT FROM NEW.role_id
			OR parent.user_id IS DISTINCT FROM NEW.user_id OR parent.route_id IS NOT NULL THEN
			RAISE EXCEPTION 'parent must be a group with the same menu owner' USING ERRCODE = '23514';
		END IF;
		IF EXISTS (
			WITH RECURSIVE ancestors AS (
				SELECT id, parent_id FROM public.main_menus WHERE id = NEW.parent_id
				UNION
				SELECT m.id, m.parent_id FROM public.main_menus m JOIN ancestors a ON m.id = a.parent_id
			)
			SELECT 1 FROM ancestors WHERE id = NEW.id
		) THEN
			RAISE EXCEPTION 'menu hierarchy cannot contain a cycle' USING ERRCODE = '23514';
		END IF;
	END IF;
	IF EXISTS (
		SELECT 1 FROM public.main_menus child WHERE child.parent_id = NEW.id
		AND (NEW.route_id IS NOT NULL OR child.role_id IS DISTINCT FROM NEW.role_id
			OR child.user_id IS DISTINCT FROM NEW.user_id)
	) THEN
		RAISE EXCEPTION 'a group with children cannot become a route or change owner' USING ERRCODE = '23514';
	END IF;
	IF NEW.route_id IS NOT NULL AND (TG_OP = 'INSERT' OR NEW.route_id IS DISTINCT FROM OLD.route_id) AND NOT EXISTS (
		SELECT 1 FROM public.application_routes WHERE id = NEW.route_id AND is_active AND menu_available
	) THEN
		RAISE EXCEPTION 'menu route must be active and available for navigation' USING ERRCODE = '23514';
	END IF;
	NEW.updated_at := CURRENT_TIMESTAMP;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER main_menus_validate BEFORE INSERT OR UPDATE ON public.main_menus
	FOR EACH ROW EXECUTE FUNCTION public.main_menus_validate();

CREATE VIEW public.main_menus_list AS
SELECT
	m.id,
	m.role_id,
	CASE WHEN u.id IS NULL THEN NULL ELSE
		jsonb_build_object('keys', jsonb_build_object('id', u.id), 'descr', u.name) END AS users_ref,
	CASE WHEN parent.id IS NULL THEN NULL ELSE
		jsonb_build_object('keys', jsonb_build_object('id', parent.id), 'descr', parent.caption) END AS main_menus_ref,
	CASE WHEN ar.id IS NULL THEN NULL ELSE
		jsonb_build_object('keys', jsonb_build_object('id', ar.id), 'descr', ar.descr) END AS application_routes_ref,
	ar.name AS route_name,
	m.caption,
	m.icon,
	m.sort_order,
	m.is_active,
	m.created_at,
	m.updated_at
FROM public.main_menus m
LEFT JOIN public.users u ON u.id = m.user_id
LEFT JOIN public.main_menus parent ON parent.id = m.parent_id
LEFT JOIN public.application_routes ar ON ar.id = m.route_id;

-- A deliberately narrow projection for the menu owner's autocomplete.
CREATE VIEW public.admin_menu_users AS
SELECT id, name, role_id FROM public.users WHERE role_id = 'admin';

INSERT INTO public.application_routes(name, path, descr, section, icon, menu_available)
VALUES
	('main', '/', 'Главная', 'Формы', 'pi pi-home', true),
	('userProfile', '/profile', 'Профиль', 'Пользователь', 'pi pi-user', false),
	('mainMenuConstructor', '/main-menu-constructor', 'Конструктор меню', 'Администрирование', 'pi pi-sitemap', false),
	('applicationRoutes', '/application-routes', 'Все формы', 'Администрирование', 'pi pi-list', true);

INSERT INTO public.main_menus(role_id, caption, route_id, icon, sort_order)
SELECT 'admin'::role_types, descr, id, icon, 10 FROM public.application_routes WHERE name = 'main';

COMMIT;
