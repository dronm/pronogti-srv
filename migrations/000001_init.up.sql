
begin;
	-- Adding new type
	CREATE TYPE locales AS ENUM (
	
		'ru'			
				
	);
		
	/* type get function */
	CREATE OR REPLACE FUNCTION enum_locales_val(locales,locales)
	RETURNS text AS $$
		SELECT
		CASE
		WHEN $1='ru'::locales AND $2='ru'::locales THEN 'Русский'
		ELSE ''
		END;		
	$$ LANGUAGE sql;	
	
	-- Adding new type
	CREATE TYPE role_types AS ENUM (
	
		'admin'			
	,
		'client'			
	);
		
	/* type get function */
	CREATE OR REPLACE FUNCTION enum_role_types_val(role_types,locales)
	RETURNS text AS $$
		SELECT
		CASE
		WHEN $1='admin'::role_types AND $2='ru'::locales THEN 'Администратор'
		WHEN $1='client'::role_types AND $2='ru'::locales THEN 'Клиент'
		ELSE ''
		END;		
	$$ LANGUAGE sql;	
	

	CREATE TABLE public.users
		(id serial NOT NULL,
		name  varchar(50) NOT NULL,
		role_id role_types NOT NULL,
		pwd  varchar(32),
		create_dt timestampTZ
				DEFAULT CURRENT_TIMESTAMP,
		banned bool
				DEFAULT FALSE,
		locale_id locales,CONSTRAINT users_pkey PRIMARY KEY (id)
		);
		CREATE UNIQUE INDEX users_name_idx
		ON users(lower(name));

	--logins
		CREATE TABLE public.logins
		(id serial NOT NULL,date_time_in timestampTZ,date_time_out timestampTZ,ip  varchar(15),session_id  varchar(128),user_id int,pub_key  varchar(15),set_date_time timestampTZ,headers jsonb,user_agent jsonb,CONSTRAINT logins_pkey PRIMARY KEY (id)
		);
		CREATE INDEX logins_session_idx
		ON logins(session_id);
		CREATE INDEX logins_user_idx
		ON logins(user_id);
		CREATE INDEX logins_pub_key_idx
		ON logins(pub_key);

	--login devices
		CREATE TABLE public.login_device_bans
		(user_id int NOT NULL,hash  varchar(32) NOT NULL,create_dt timestampTZ
				DEFAULT CURRENT_TIMESTAMP,CONSTRAINT login_device_bans_pkey PRIMARY KEY (user_id,hash)
		);
commit;

