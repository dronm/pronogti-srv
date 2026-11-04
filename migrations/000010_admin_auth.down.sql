BEGIN;

-- Revoke admin login evidence before removing the password/session fields.
UPDATE public.logins SET date_time_out = COALESCE(date_time_out,now()) WHERE auth_kind = 'admin';
DROP TRIGGER users_admin_auth_before_update ON public.users;
DROP FUNCTION public.users_admin_auth_before_update();
DROP INDEX public.logins_admin_open_idx;
ALTER TABLE public.logins DROP COLUMN auth_kind;
-- Keep the widened IP column so existing IPv6 audit data is not truncated.
ALTER TABLE public.users DROP COLUMN password_hash, DROP COLUMN session_version;

COMMIT;
