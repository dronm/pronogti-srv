BEGIN;

DROP VIEW public.admin_menu_users;
DROP VIEW public.main_menus_list;
DROP TABLE public.main_menus;
DROP FUNCTION public.main_menus_validate();
DROP TABLE public.application_routes;
DROP FUNCTION public.admin_navigation_updated_at();

COMMIT;
