CREATE TABLE public.deploy_updates (
	id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	release_id text NOT NULL UNIQUE,
	admin boolean NOT NULL,
	kiosk boolean NOT NULL,
	updated_at timestamptz
);
