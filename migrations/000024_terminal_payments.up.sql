BEGIN;

-- Physical acquiring is a separate channel. Existing profile identities remain
-- required, while a terminal-only profile does not need internet-bank secrets.
ALTER TABLE public.payment_profiles
	ADD COLUMN terminals_enabled boolean NOT NULL DEFAULT false,
	ALTER COLUMN api_base_url SET DEFAULT '',
	DROP CONSTRAINT payment_profiles_api_base_url_check,
	ADD CONSTRAINT payment_profiles_api_base_url_check CHECK (
		api_base_url ~ '^https?://[^[:space:]]+$'
		OR (terminals_enabled AND api_base_url = '' AND NOT cards_enabled)
	),
	DROP CONSTRAINT payment_profiles_production_https_check,
	ADD CONSTRAINT payment_profiles_production_https_check CHECK (
		environment <> 'production' OR api_base_url LIKE 'https://%'
		OR (terminals_enabled AND api_base_url = '' AND NOT cards_enabled)
	),
	DROP CONSTRAINT payment_profiles_enabled_configuration_check,
	ADD CONSTRAINT payment_profiles_enabled_configuration_check CHECK (
		NOT enabled OR (
			(terminals_enabled AND api_base_url = '' AND NOT cards_enabled)
			OR (
				(jwt_ciphertext IS NOT NULL OR jwt_env IS NOT NULL)
				AND nullif(btrim(customer_code), '') IS NOT NULL
				AND ((nullif(btrim(sbp_merchant_id), '') IS NOT NULL AND nullif(btrim(account_id), '') IS NOT NULL)
					OR cards_enabled)
			)
		)
	);

CREATE OR REPLACE FUNCTION public.payment_profiles_before_update() RETURNS trigger AS $$
BEGIN
	IF ROW(NEW.id, NEW.company_id, NEW.provider, NEW.environment, NEW.api_base_url,
		NEW.customer_code, NEW.sbp_merchant_id, NEW.account_id)
		IS DISTINCT FROM
		ROW(OLD.id, OLD.company_id, OLD.provider, OLD.environment, OLD.api_base_url,
		OLD.customer_code, OLD.sbp_merchant_id, OLD.account_id)
		AND EXISTS (SELECT 1 FROM public.payments WHERE payment_profile_id = OLD.id) THEN
		RAISE EXCEPTION 'bank identity of a used payment profile is immutable; create another profile' USING ERRCODE = '23514';
	END IF;
	IF NEW.acquiring_merchant_id IS DISTINCT FROM OLD.acquiring_merchant_id
		AND EXISTS (SELECT 1 FROM public.payments WHERE payment_profile_id = OLD.id AND method = 'card') THEN
		RAISE EXCEPTION 'acquiring identity of a used card profile is immutable; create another profile' USING ERRCODE = '23514';
	END IF;
	IF ROW(NEW.company_id, NEW.name, NEW.provider, NEW.environment, NEW.api_base_url,
		NEW.jwt_ciphertext, NEW.jwt_env, NEW.client_id, NEW.customer_code, NEW.sbp_merchant_id,
		NEW.account_id, NEW.qr_ttl_minutes, NEW.http_timeout_seconds, NEW.enabled,
		NEW.cards_enabled, NEW.acquiring_merchant_id, NEW.card_ttl_minutes, NEW.card_redirect_url, NEW.card_fail_redirect_url,
		NEW.terminals_enabled)
		IS DISTINCT FROM
		ROW(OLD.company_id, OLD.name, OLD.provider, OLD.environment, OLD.api_base_url,
		OLD.jwt_ciphertext, OLD.jwt_env, OLD.client_id, OLD.customer_code, OLD.sbp_merchant_id,
		OLD.account_id, OLD.qr_ttl_minutes, OLD.http_timeout_seconds, OLD.enabled,
		OLD.cards_enabled, OLD.acquiring_merchant_id, OLD.card_ttl_minutes, OLD.card_redirect_url, OLD.card_fail_redirect_url,
		OLD.terminals_enabled) THEN
		NEW.config_version := OLD.config_version + 1;
	ELSE
		NEW.config_version := OLD.config_version;
	END IF;
	NEW.updated_at := now();
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

ALTER TABLE public.payments
	DROP CONSTRAINT payments_method_check,
	ADD CONSTRAINT payments_method_check CHECK (method IN ('sbp', 'card', 'terminal_card')),
	DROP CONSTRAINT payments_method_identifiers_check,
	ADD CONSTRAINT payments_method_identifiers_check CHECK (
		(method = 'sbp' AND acquiring_operation_id IS NULL)
		OR (method = 'card' AND qrc_id IS NULL AND qr_image IS NULL AND qr_image_media_type IS NULL)
		OR (method = 'terminal_card' AND qrc_id IS NULL AND acquiring_operation_id IS NULL
			AND qr_image IS NULL AND qr_image_media_type IS NULL)
	);

-- Native terminal transaction numbers may only be unique on one terminal.
-- Terminal references live in terminal_commands, outside these online indexes.
DROP INDEX public.payments_paid_bank_reference_key;
CREATE UNIQUE INDEX payments_paid_bank_reference_key
	ON public.payments (payment_profile_id, method, ref_transaction_id)
	WHERE state = 'paid' AND method <> 'terminal_card' AND ref_transaction_id IS NOT NULL;
DROP INDEX public.payments_paid_bank_operation_key;
CREATE UNIQUE INDEX payments_paid_bank_operation_key
	ON public.payments (payment_profile_id, method, transaction_id)
	WHERE state = 'paid' AND method <> 'terminal_card' AND environment = 'production' AND transaction_id IS NOT NULL;

CREATE TABLE public.terminal_agents (
	id text PRIMARY KEY CHECK (length(btrim(id)) BETWEEN 1 AND 200),
	company_id bigint NOT NULL REFERENCES public.salons (company_id),
	kiosk_id bigint NOT NULL REFERENCES public.kiosks (id),
	environment text NOT NULL CHECK (environment IN ('mock', 'production')),
	token_hash text NOT NULL UNIQUE CHECK (token_hash ~ '^[0-9a-f]{64}$'),
	enabled boolean NOT NULL DEFAULT true,
	last_seen_at timestamptz,
	heartbeat jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(heartbeat) = 'object'),
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT terminal_agents_scope_key UNIQUE (id, company_id, kiosk_id),
	CONSTRAINT terminal_agents_environment_key UNIQUE (id, company_id, kiosk_id, environment)
);

CREATE FUNCTION public.terminal_agents_before_write() RETURNS trigger AS $$
BEGIN
	IF TG_OP = 'UPDATE' AND ROW(NEW.id, NEW.company_id, NEW.kiosk_id, NEW.environment, NEW.created_at)
		IS DISTINCT FROM ROW(OLD.id, OLD.company_id, OLD.kiosk_id, OLD.environment, OLD.created_at) THEN
		RAISE EXCEPTION 'terminal agent scope is immutable; register another agent' USING ERRCODE = '23514';
	END IF;
	IF TG_OP = 'INSERT' AND NOT EXISTS (
		SELECT 1 FROM public.kiosks WHERE id = NEW.kiosk_id AND company_id = NEW.company_id
	) THEN
		RAISE EXCEPTION 'terminal agent kiosk belongs to another company' USING ERRCODE = '23514';
	END IF;
	NEW.updated_at := now();
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER terminal_agents_before_write BEFORE INSERT OR UPDATE ON public.terminal_agents
	FOR EACH ROW EXECUTE FUNCTION public.terminal_agents_before_write();

CREATE TABLE public.payment_terminals (
	id bigserial PRIMARY KEY,
	agent_id text NOT NULL,
	company_id bigint NOT NULL,
	kiosk_id bigint NOT NULL REFERENCES public.kiosks (id),
	payment_profile_id bigint NOT NULL,
	terminal_id text NOT NULL CHECK (length(btrim(terminal_id)) BETWEEN 1 AND 200),
	bank_terminal_id text NOT NULL DEFAULT '' CHECK (length(bank_terminal_id) <= 200),
	physical_id text NOT NULL CHECK (length(btrim(physical_id)) BETWEEN 1 AND 500),
	enabled boolean NOT NULL DEFAULT true,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT payment_terminals_agent_scope_fkey FOREIGN KEY (agent_id, company_id, kiosk_id)
		REFERENCES public.terminal_agents (id, company_id, kiosk_id),
	CONSTRAINT payment_terminals_profile_company_fkey FOREIGN KEY (payment_profile_id, company_id)
		REFERENCES public.payment_profiles (id, company_id),
	CONSTRAINT payment_terminals_agent_terminal_key UNIQUE (agent_id, terminal_id),
	CONSTRAINT payment_terminals_scope_key UNIQUE (id, agent_id, company_id, kiosk_id, payment_profile_id)
);
CREATE UNIQUE INDEX payment_terminals_enabled_physical_key
	ON public.payment_terminals (company_id, physical_id) WHERE enabled;
CREATE UNIQUE INDEX payment_terminals_enabled_kiosk_key
	ON public.payment_terminals (kiosk_id) WHERE enabled;
CREATE UNIQUE INDEX payment_terminals_enabled_bank_key
	ON public.payment_terminals (company_id, bank_terminal_id) WHERE enabled AND bank_terminal_id <> '';
CREATE INDEX payment_terminals_kiosk_idx ON public.payment_terminals (company_id, kiosk_id) WHERE enabled;

CREATE FUNCTION public.payment_terminals_before_write() RETURNS trigger AS $$
BEGIN
	IF TG_OP = 'UPDATE' AND ROW(NEW.id, NEW.agent_id, NEW.company_id, NEW.kiosk_id, NEW.payment_profile_id,
		NEW.terminal_id, NEW.bank_terminal_id, NEW.physical_id, NEW.created_at)
		IS DISTINCT FROM ROW(OLD.id, OLD.agent_id, OLD.company_id, OLD.kiosk_id, OLD.payment_profile_id,
		OLD.terminal_id, OLD.bank_terminal_id, OLD.physical_id, OLD.created_at) THEN
		RAISE EXCEPTION 'payment terminal routing is immutable; register another terminal' USING ERRCODE = '23514';
	END IF;
	IF TG_OP = 'INSERT' AND NOT EXISTS (
		SELECT 1 FROM public.terminal_agents a JOIN public.payment_profiles p ON p.id = NEW.payment_profile_id
		WHERE a.id = NEW.agent_id AND a.company_id = NEW.company_id AND a.kiosk_id = NEW.kiosk_id
			AND p.company_id = NEW.company_id AND p.environment = a.environment
	) THEN
		RAISE EXCEPTION 'terminal agent and payment profile scopes differ' USING ERRCODE = '23514';
	END IF;
	NEW.updated_at := now();
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER payment_terminals_before_write BEFORE INSERT OR UPDATE ON public.payment_terminals
	FOR EACH ROW EXECUTE FUNCTION public.payment_terminals_before_write();

CREATE TABLE public.terminal_commands (
	id uuid PRIMARY KEY,
	payment_id uuid NOT NULL UNIQUE,
	order_id uuid NOT NULL,
	company_id bigint NOT NULL,
	kiosk_id bigint NOT NULL REFERENCES public.kiosks (id),
	payment_profile_id bigint NOT NULL,
	agent_id text NOT NULL,
	device_id bigint NOT NULL,
	terminal_id text NOT NULL CHECK (length(btrim(terminal_id)) BETWEEN 1 AND 200),
	bank_terminal_id text NOT NULL DEFAULT '',
	physical_id text NOT NULL CHECK (length(btrim(physical_id)) BETWEEN 1 AND 500),
	environment text NOT NULL CHECK (environment IN ('mock', 'production')),
	amount_kopecks bigint NOT NULL CHECK (amount_kopecks BETWEEN 1 AND 9007199254740991),
	currency text NOT NULL DEFAULT 'RUB' CHECK (currency = 'RUB'),
	expires_at timestamptz NOT NULL,
	state text NOT NULL DEFAULT 'queued' CHECK (state IN (
		'queued', 'leased', 'dispatching', 'waiting', 'approved', 'declined', 'cancelled', 'unknown', 'expired', 'rejected'
	)),
	lease_until timestamptz,
	first_claimed_at timestamptz,
	agent_outcome jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(agent_outcome) = 'object'),
	transaction_id text,
	rrn text,
	stan text,
	authorization_code text,
	response_code text,
	error_code text,
	error_message text,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT terminal_commands_payment_fkey FOREIGN KEY (payment_id, order_id, company_id, environment)
		REFERENCES public.payments (id, order_id, company_id, environment),
	CONSTRAINT terminal_commands_agent_scope_fkey FOREIGN KEY (agent_id, company_id, kiosk_id, environment)
		REFERENCES public.terminal_agents (id, company_id, kiosk_id, environment),
	CONSTRAINT terminal_commands_device_scope_fkey FOREIGN KEY (device_id, agent_id, company_id, kiosk_id, payment_profile_id)
		REFERENCES public.payment_terminals (id, agent_id, company_id, kiosk_id, payment_profile_id),
	CONSTRAINT terminal_commands_id_payment_agent_key UNIQUE (id, payment_id, agent_id)
);
CREATE UNIQUE INDEX terminal_commands_one_device_idx ON public.terminal_commands (device_id)
	WHERE state IN ('queued', 'leased', 'dispatching', 'waiting', 'unknown');
CREATE UNIQUE INDEX terminal_commands_one_physical_idx ON public.terminal_commands (company_id, physical_id)
	WHERE state IN ('queued', 'leased', 'dispatching', 'waiting', 'unknown');
CREATE UNIQUE INDEX terminal_commands_one_bank_terminal_idx ON public.terminal_commands (company_id, bank_terminal_id)
	WHERE bank_terminal_id <> '' AND state IN ('queued', 'leased', 'dispatching', 'waiting', 'unknown');
CREATE INDEX terminal_commands_delivery_idx ON public.terminal_commands (agent_id, lease_until, created_at)
	WHERE state IN ('queued', 'leased', 'dispatching', 'waiting');

CREATE FUNCTION public.terminal_commands_before_write() RETURNS trigger AS $$
BEGIN
	IF TG_OP = 'UPDATE' THEN
		IF ROW(NEW.id, NEW.payment_id, NEW.order_id, NEW.company_id, NEW.kiosk_id, NEW.payment_profile_id,
			NEW.agent_id, NEW.device_id, NEW.terminal_id, NEW.bank_terminal_id, NEW.physical_id,
			NEW.environment, NEW.amount_kopecks, NEW.currency, NEW.expires_at, NEW.created_at)
			IS DISTINCT FROM ROW(OLD.id, OLD.payment_id, OLD.order_id, OLD.company_id, OLD.kiosk_id, OLD.payment_profile_id,
			OLD.agent_id, OLD.device_id, OLD.terminal_id, OLD.bank_terminal_id, OLD.physical_id,
			OLD.environment, OLD.amount_kopecks, OLD.currency, OLD.expires_at, OLD.created_at) THEN
			RAISE EXCEPTION 'terminal command identity, routing and amount are immutable' USING ERRCODE = '23514';
		END IF;
		IF OLD.state = 'approved' AND NEW.state <> 'approved' THEN
			RAISE EXCEPTION 'terminal approval cannot be downgraded' USING ERRCODE = '23514';
		END IF;
	ELSE
		IF NOT EXISTS (
			SELECT 1 FROM public.payments p JOIN public.payment_orders o ON o.id = p.order_id
			JOIN public.payment_terminals d ON d.id = NEW.device_id
			WHERE p.id = NEW.payment_id AND p.method = 'terminal_card'
				AND p.order_id = NEW.order_id AND p.company_id = NEW.company_id
				AND p.payment_profile_id = NEW.payment_profile_id AND p.environment = NEW.environment
				AND p.amount_kopecks = NEW.amount_kopecks AND p.currency = NEW.currency
				AND o.entry_mode = 'kiosk' AND o.kiosk_id = NEW.kiosk_id
				AND d.agent_id = NEW.agent_id AND d.company_id = NEW.company_id AND d.kiosk_id = NEW.kiosk_id
				AND d.payment_profile_id = NEW.payment_profile_id AND d.terminal_id = NEW.terminal_id
				AND d.bank_terminal_id = NEW.bank_terminal_id AND d.physical_id = NEW.physical_id
		) THEN
			RAISE EXCEPTION 'terminal command does not match its kiosk payment and frozen device' USING ERRCODE = '23514';
		END IF;
	END IF;
	NEW.updated_at := now();
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER terminal_commands_before_write BEFORE INSERT OR UPDATE ON public.terminal_commands
	FOR EACH ROW EXECUTE FUNCTION public.terminal_commands_before_write();

CREATE TABLE public.terminal_resolutions (
	id uuid PRIMARY KEY,
	command_id uuid NOT NULL,
	payment_id uuid NOT NULL,
	agent_id text NOT NULL,
	terminal_id text NOT NULL,
	amount_kopecks bigint NOT NULL CHECK (amount_kopecks BETWEEN 1 AND 9007199254740991),
	currency text NOT NULL CHECK (currency = 'RUB'),
	decision text NOT NULL CHECK (decision IN ('paid', 'not_paid')),
	reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 2000),
	reference text NOT NULL DEFAULT '' CHECK (length(reference) <= 500),
	evidence text NOT NULL CHECK (length(btrim(evidence)) BETWEEN 1 AND 8000),
	operator_id bigint NOT NULL REFERENCES public.users (id),
	resolved_at timestamptz NOT NULL,
	status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'acknowledged', 'superseded')),
	created_at timestamptz NOT NULL DEFAULT now(),
	acknowledged_at timestamptz,
	CONSTRAINT terminal_resolutions_command_fkey FOREIGN KEY (command_id, payment_id, agent_id)
		REFERENCES public.terminal_commands (id, payment_id, agent_id),
	CONSTRAINT terminal_resolutions_event_scope_key UNIQUE (id, command_id, payment_id, agent_id),
	CONSTRAINT terminal_resolutions_ack_check CHECK ((status = 'pending') = (acknowledged_at IS NULL))
);
CREATE UNIQUE INDEX terminal_resolutions_one_pending_idx ON public.terminal_resolutions (command_id)
	WHERE status = 'pending';
CREATE INDEX terminal_resolutions_delivery_idx ON public.terminal_resolutions (agent_id, created_at)
	WHERE status = 'pending';

CREATE FUNCTION public.terminal_resolutions_before_write() RETURNS trigger AS $$
BEGIN
	IF TG_OP = 'UPDATE' THEN
		IF ROW(NEW.id, NEW.command_id, NEW.payment_id, NEW.agent_id, NEW.terminal_id, NEW.amount_kopecks,
			NEW.currency, NEW.decision, NEW.reason, NEW.reference, NEW.evidence, NEW.operator_id, NEW.resolved_at, NEW.created_at)
			IS DISTINCT FROM ROW(OLD.id, OLD.command_id, OLD.payment_id, OLD.agent_id, OLD.terminal_id, OLD.amount_kopecks,
			OLD.currency, OLD.decision, OLD.reason, OLD.reference, OLD.evidence, OLD.operator_id, OLD.resolved_at, OLD.created_at) THEN
			RAISE EXCEPTION 'terminal resolution evidence and identity are immutable' USING ERRCODE = '23514';
		END IF;
		IF OLD.status IN ('acknowledged', 'superseded')
			AND ROW(NEW.status, NEW.acknowledged_at) IS DISTINCT FROM ROW(OLD.status, OLD.acknowledged_at) THEN
			RAISE EXCEPTION 'terminal resolution completion is final' USING ERRCODE = '23514';
		END IF;
	ELSE
		IF NOT EXISTS (
			SELECT 1 FROM public.terminal_commands c WHERE c.id = NEW.command_id
				AND c.payment_id = NEW.payment_id AND c.agent_id = NEW.agent_id AND c.terminal_id = NEW.terminal_id
				AND c.amount_kopecks = NEW.amount_kopecks AND c.currency = NEW.currency
		) THEN
			RAISE EXCEPTION 'terminal resolution does not match its original command' USING ERRCODE = '23514';
		END IF;
	END IF;
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER terminal_resolutions_before_write BEFORE INSERT OR UPDATE ON public.terminal_resolutions
	FOR EACH ROW EXECUTE FUNCTION public.terminal_resolutions_before_write();

CREATE TABLE public.terminal_agent_events (
	agent_id text NOT NULL REFERENCES public.terminal_agents (id),
	event_id text NOT NULL CHECK (length(btrim(event_id)) BETWEEN 1 AND 200),
	command_id uuid NOT NULL,
	payment_id uuid NOT NULL,
	terminal_id text NOT NULL,
	state text NOT NULL CHECK (state IN ('queued', 'dispatching', 'waiting', 'approved', 'declined', 'cancelled', 'unknown', 'expired', 'rejected')),
	resolution_id uuid,
	payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
	content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
	created_at timestamptz NOT NULL DEFAULT now(),
	processed_at timestamptz,
	processing_state text NOT NULL DEFAULT 'processed' CHECK (processing_state IN ('processed', 'ignored', 'conflict')),
	error_code text,
	PRIMARY KEY (agent_id, event_id),
	CONSTRAINT terminal_agent_events_command_fkey FOREIGN KEY (command_id, payment_id, agent_id)
		REFERENCES public.terminal_commands (id, payment_id, agent_id),
	CONSTRAINT terminal_agent_events_resolution_scope_fkey FOREIGN KEY (resolution_id, command_id, payment_id, agent_id)
		REFERENCES public.terminal_resolutions (id, command_id, payment_id, agent_id)
);
CREATE INDEX terminal_agent_events_payment_idx ON public.terminal_agent_events (payment_id, created_at);

CREATE FUNCTION public.terminal_agent_events_before_update() RETURNS trigger AS $$
BEGIN
	RAISE EXCEPTION 'terminal agent events are an append-only inbox' USING ERRCODE = '23514';
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER terminal_agent_events_before_update BEFORE UPDATE ON public.terminal_agent_events
	FOR EACH ROW EXECUTE FUNCTION public.terminal_agent_events_before_update();

-- Append the channel flag without changing existing generated-view columns.
CREATE OR REPLACE VIEW public.payment_profiles_list AS
SELECT
	p.id,
	p.company_id,
	p.name,
	p.provider,
	p.environment,
	p.api_base_url,
	p.jwt_env,
	p.client_id,
	p.customer_code,
	p.sbp_merchant_id,
	p.account_id,
	p.qr_ttl_minutes,
	p.http_timeout_seconds,
	p.enabled,
	p.cards_enabled,
	p.acquiring_merchant_id,
	p.card_ttl_minutes,
	p.card_redirect_url,
	p.card_fail_redirect_url,
	p.config_version,
	p.created_at,
	p.updated_at,
	json_build_object(
		'keys', json_build_object('id', p.company_id),
		'descr', COALESCE(c.name, s.name),
		'dataType', 'companies'
	) AS company,
	p.terminals_enabled
FROM public.payment_profiles p
JOIN public.salons s ON s.company_id = p.company_id
LEFT JOIN public.companies c ON c.id = p.company_id;

COMMIT;
