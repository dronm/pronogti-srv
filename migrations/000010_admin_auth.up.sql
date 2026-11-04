BEGIN;

-- Legacy pwd values are deliberately not promoted to administrator credentials.
-- Provision a bcrypt password with cmd/admin before using administrator login.
ALTER TABLE public.users
	ADD COLUMN password_hash text CHECK (password_hash IS NULL OR length(password_hash) = 60),
	ADD COLUMN session_version bigint NOT NULL DEFAULT 1 CHECK (session_version > 0);

ALTER TABLE public.logins
	ALTER COLUMN ip TYPE varchar(64),
	ADD COLUMN auth_kind text CHECK (auth_kind IS NULL OR auth_kind = 'admin');

CREATE INDEX logins_admin_open_idx ON public.logins(user_id,id)
	WHERE auth_kind = 'admin' AND date_time_out IS NULL;

CREATE FUNCTION public.users_admin_auth_before_update() RETURNS trigger AS $$
BEGIN
	IF ROW(NEW.password_hash, NEW.role_id, NEW.banned)
		IS DISTINCT FROM ROW(OLD.password_hash, OLD.role_id, OLD.banned) THEN
		NEW.session_version := OLD.session_version + 1;
		UPDATE public.logins SET date_time_out = now()
		WHERE user_id = OLD.id AND auth_kind = 'admin' AND date_time_out IS NULL;
	ELSIF NEW.session_version < OLD.session_version THEN
		RAISE EXCEPTION 'administrator session version cannot move backwards' USING ERRCODE = '23514';
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER users_admin_auth_before_update BEFORE UPDATE ON public.users
	FOR EACH ROW EXECUTE FUNCTION public.users_admin_auth_before_update();

COMMIT;
