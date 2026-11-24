# Physical terminal payments with Terminal Bridge

Pronogti can coordinate a physical card terminal attached to a registered Windows kiosk. Terminal Bridge is a separate Windows tray application: the browser requests payment from Pronogti, and Terminal Bridge retrieves commands and delivers results through authenticated outbound HTTPS.

This integration is separate from the existing `card` method, which opens Tochka's hosted internet-acquiring page. The physical-terminal payment channel does not use an internet-acquiring checkout URL or treat the browser as evidence of payment.

## Deployment configuration

Apply all pending database migrations before enabling the integration. Its deployment switch and timing settings are stored in the backend's JSON configuration:

```json
{
	"terminal_bridge": {
		"enabled": false,
		"allow_live": false,
		"allow_simulated": false,
		"command_ttl_seconds": 120,
		"max_poll_seconds": 25,
		"lease_seconds": 45,
		"heartbeat_max_age_seconds": 60
	}
}
```

No environment variables are consulted for this section. Existing configurations without `terminal_bridge` load the same safe defaults. Agent tokens are provisioned separately; the backend stores their hashes in PostgreSQL rather than adding credentials to deployment configuration.

| Setting | Meaning |
| --- | --- |
| `enabled` | Allows new terminal payment initiation; false by default. Authenticated result delivery/recovery remains mounted to drain already accepted work |
| `allow_live` | Allows the integration to process live financial operations after the remaining profile/device checks pass |
| `allow_simulated` | Allows explicitly simulated terminal operations for isolated testing; false by default |
| `command_ttl_seconds` | Time allowed to start a newly created command, from 10 to 900 seconds |
| `max_poll_seconds` | Maximum long-poll wait, from 1 to 60 seconds |
| `lease_seconds` | Command delivery lease, at least `max_poll_seconds + 5` and no greater than 900 seconds |
| `heartbeat_max_age_seconds` | Agent heartbeat freshness limit, from 10 to 3600 seconds |

When enabled, `http.write_timeout` must be at least `max_poll_seconds + 5` seconds. Set reverse-proxy response timeouts to accommodate the same wait. A dispatch deadline governs starting a payment; it does not prove failure after a terminal operation has started.

## Provision an installation

1. Apply migrations through `000024_terminal_payments` using the primary database.
2. Create or select an enabled company-owned payment profile with `terminals_enabled: true`. A terminal-only profile uses the existing `provider: "tochka"`, `cards_enabled: false` and an empty `api_base_url`. It does not require internet-acquiring JWT, customer code, SBP identity or hosted-checkout merchant identity. A profile with a nonempty online API URL retains the existing online credential checks.
3. Select that profile on the payment-terminal binding during registration. Existing kiosk/salon payment-profile defaults continue to serve online QR/hosted-card methods; do not replace a working online default with a terminal-only profile just to add physical acquiring. Company, kiosk, agent, profile environment and terminal binding must agree.
4. Register a Terminal Bridge agent for the kiosk and chosen `mock` or `production` environment. Save its one-time plaintext token directly to the Windows application's `config.json`; PostgreSQL retains only its hash.
5. Register the payment terminal under that agent. Use the same logical `terminal_id` in the backend and Windows application, the bank-assigned `bank_terminal_id`, and a stable `physical_id` identifying the actual device. The database permits one enabled terminal binding per kiosk.
6. Set the Windows application's backend URL to the externally reachable API base, including any reverse-proxy prefix, and set its `agent_id` and token. Verify heartbeat and connector reachability.
7. For isolated simulator testing, use a `mock` profile/agent and explicitly enable `allow_simulated`. For actual acquiring, use matching `production` identities and explicitly enable `allow_live` only after the deployment and bank-device checks pass.

Agent registration and token rotation return the plaintext credential once. Normal list/detail responses do not expose either the token or its hash. Rotation revokes the previous credential. Keep the kiosk's browser-registration credential separate from this agent token. Disabling the deployment feature stops new initiation; disabling the agent itself revokes authentication and prevents its pending results from being delivered until it is enabled again. Drain outstanding results before routine agent retirement.

An existing used payment profile's bank identity remains immutable. Create another profile rather than converting a previously used online identity into a different acquiring identity. Terminal bindings are also immutable except for enablement; replacement uses a new binding and retains the old history.

## Windows application compatibility

Terminal Bridge 0.1 uses the base claim/events/heartbeat contract. Its D230 adapter implements the bank-approved single-response INPAS SmartSale/DC2 flow. The bank must configure the terminal firmware, connector and acknowledgment settings for that supported profile. A response containing optional application-confirmation field `51` remains uncertain; the adapter does not send an application acknowledgment or repeat the sale.

Audited operator resolution additionally requires the recovery-capable Terminal Bridge 0.2 protocol. That agent advertises `supports_resolutions: true`, applies the backend's resolution to the original local command without calling the terminal sale method, and returns a durable resolution acknowledgement event. A 0.1 agent can participate in normal payment flow, but cannot complete this local-journal resolution handshake; upgrade it before resolving an uncertain operation.

The DC Service health endpoint establishes connector availability, not physical D230 connectivity. The agent reports `connector_reachable` separately from `connected`. A reachable connector with an unverified physical device is an attention condition, not evidence that a payment succeeded or failed.

The simulator is explicit. Its outcomes carry `simulated: true`; those outcomes cannot confirm a production checkout. Terminal model support is implemented behind replaceable adapters, while commands and payment identity remain stable.

## Financial lifecycle

The backend creates a command only from an authoritative immutable checkout. Company, kiosk, customer, payment method, physical device and exact amount are validated before an operation can be started. A registered agent can act only for its assigned kiosk and terminals.

Terminal Bridge records the command in its durable SQLite journal before calling the driver. Repeated delivery keeps the same command/payment IDs, amount, currency, terminal and deadline. It does not start another charge. The backend retains ownership of command delivery until a matching outcome event has been durably committed and acknowledged.

An approved terminal result enters the existing paid-to-YClients/fiscal workflow. Payment success, YClients synchronization and receipt issuance remain separate persisted states. A receipt or CRM failure must not revert the bank payment or permit another charge. The newer full fiscal error information, payment purpose text and shift behavior remain part of the existing completion pipeline.

The physical terminal's bank slip and the Terminal FA fiscal receipt are separate documents. Check the fiscal profile's in-person/internet and printing settings for the actual kiosk arrangement rather than copying an online-only profile blindly.

## Uncertain payments and recovery

An HTTP timeout, application restart, disconnected cable, expired browser session or missing callback does not establish that the customer was not charged. Such operations retain an uncertain state and block another financial attempt until the original outcome is established.

The initial D230 adapter does not offer arbitrary-transaction lookup. Its reconciliation entry point never resends the sale or guesses from an unrelated last transaction. Resolve the original payment using verified terminal/bank evidence and the authorized operator workflow. Keep the original payment identity and audit trail.

Do not clear an uncertain payment by deleting either database, changing terminal identity, creating a new command ID or selecting another payment method. If the physical terminal is replaced, retain its old identity and history for outstanding payment/refund investigations.

An operator resolution records the decision `paid` or `not_paid`, a reason, verified evidence reference, supporting evidence and the authenticated operator identity. Both decisions are initially **pending**: the payment remains uncertain and the terminal remains held. The instruction is delivered to the original agent as data, not as a new sale. Only after the original local journal durably applies the resolution and its matching acknowledgement event is committed by the backend does the backend finalize paid and queue YClients/fiscal jobs, or finalize verified nonpayment and release the hold. A successful resolution-request HTTP response is not completion of this handshake and never force-clears the device.

The existing online card/SBP refund adapter does not refund a physical-terminal transaction. Terminal payment refund quotes are explicitly ineligible with reason `terminal_refunds_unsupported`; use the bank's verified terminal/refund procedure until a dedicated adapter is implemented.

## Kiosk API

Physical-terminal initiation requires the registered kiosk and current customer context. Use an authoritative checkout ID and an `Idempotency-Key`; the browser does not supply a payment amount or terminal route. Read the resulting payment through the existing kiosk payment-status endpoint. Its method is `terminal_card`, with terminal/command details instead of a QR payload or hosted card URL.

| Method and path | Purpose |
| --- | --- |
| `POST /kiosk/init-terminal-pay` | Start or retrieve the durable terminal attempt |
| `POST /kiosk/me/init-terminal-pay` | Equivalent kiosk alias |
| `GET /kiosk/payments/{id}` | Read persisted payment, completion and terminal command state |

Both initiation routes accept `{ "checkout_id": "UUID" }` plus the existing kiosk/customer session and idempotency headers. The payment response includes `terminal_id`, `command_id` and `command_state` when applicable. The Vue payment button and presentation of these fields are a separate frontend update.

Public customer routes used by personal browsers do not expose physical-terminal control. Customer-session expiry or browser closure does not stop delivery of an already started payment result.

## Agent API

These routes use a dedicated agent bearer token and no Redis/customer cookies. They are mounted separately from the browser session middleware:

| Method and path | Purpose |
| --- | --- |
| `POST /terminal-bridge/v1/agents/{agent_id}/heartbeat` | Records agent health, terminal capabilities and recovery support |
| `POST /terminal-bridge/v1/agents/{agent_id}/commands/claim` | Long-polls for an immutable command or pending resolution |
| `POST /terminal-bridge/v1/agents/{agent_id}/events` | Durably accepts idempotent progress, financial outcomes and resolution acknowledgements |

Every request is JSON and includes `Authorization: Bearer <agent token>`. The token is scoped to the agent/kiosk/company/environment. Browser `Origin` requests are rejected. HTTPS must be supplied by the reverse proxy for any nonlocal deployment.

A claim body has `terminal_ids` and `wait_seconds`. The backend returns `204` when there is no work, or `200` with a `command` or `resolution` object. A sale command contains `command_id`, `payment_id`, `terminal_id`, integer `amount_kopecks`, `currency: "RUB"` and `expires_at`.

Events are submitted as `{ "events": [...] }`. A successful `200` response contains explicit `acknowledged_event_ids`; the agent retains everything else in its outbox. Event ID/content conflicts are rejected rather than silently overwritten. The backend verifies command identity and exact amount/currency before recording a financial transition.

Heartbeat device status distinguishes `connected`, `connector_reachable` and `simulated`. Capabilities are descriptive and do not authorize an unsupported operation. Recovery-capable agents also advertise `supports_resolutions: true`.

## Administrator API

The following routes require the existing administrator session. Writes also require the existing administrator nonce/CSRF protection. They are handwritten backend handlers; catalogue YAML metadata does not register a second generated CRUD path.

| Method and path | Purpose |
| --- | --- |
| `GET /admin/terminal-agents` | List safe agent details |
| `POST /admin/terminal-agents` | Register an agent and return its new token once |
| `GET /admin/terminal-agents/{agent_id}` | Inspect an agent |
| `PATCH /admin/terminal-agents/{agent_id}` | Change `enabled` only |
| `POST /admin/terminal-agents/{agent_id}/rotate-token` | Revoke the old agent credential and issue a new one |
| `GET /admin/payment-terminals` | List terminal bindings |
| `POST /admin/payment-terminals` | Register an immutable terminal binding |
| `GET /admin/payment-terminals/{id}` | Inspect a binding |
| `PATCH /admin/payment-terminals/{id}` | Change `enabled` only |
| `GET /admin/terminal-commands` | Inspect command history and outcome/error details |
| `GET /admin/terminal-commands/{command_id}` | Inspect one command |
| `POST /admin/terminal-commands/{command_id}/resolve` | Submit an audited `paid`/`not_paid` decision with supporting evidence |

Agent creation accepts `id`, `company_id`, `kiosk_id`, `environment` and `enabled`. Terminal registration accepts `agent_id`, `company_id`, `kiosk_id`, `payment_profile_id`, `terminal_id`, `bank_terminal_id`, `physical_id` and `enabled`. The backend validates scope and environment relationships; naming an existing kiosk or profile is not sufficient authorization by itself.

The terminal agent, payment terminal and terminal command schemas generate read-only catalogue services with callable `List` and `Detail` methods. Each schema sets `httpRoutes.enabled` to `false` so the handwritten administrator handlers own the routes above. The catalogue models omit agent credentials.

## Server administration CLI

Build the server-side provisioning utility with `make build-terminal`; the output is `pronogti-terminal`. It uses the same backend JSON configuration and database. It does not contact the bank, YClients or the fiscal device.

Place the global `-config` option before the action. The examples below register a mock installation; substitute actual company, kiosk and profile IDs:

```sh
./pronogti-terminal -config ./config.json register-agent \
	-agent kiosk-terminal-1 -company 781898 -kiosk 1 -environment mock

./pronogti-terminal -config ./config.json register-terminal \
	-agent kiosk-terminal-1 -company 781898 -kiosk 1 -profile 1 \
	-terminal demo-terminal -bank-terminal DEMO-TERMINAL \
	-physical simulator:kiosk-terminal-1

./pronogti-terminal -config ./config.json list
./pronogti-terminal -config ./config.json status -command COMMAND_UUID
```

For a real installation, register the agent with `-environment production`, use the actual bank terminal identity, and identify the physical device with a stable inventory identity such as its serial number. Both registration actions default to `-enabled=true`; `-enabled=false` provisions an initially disabled record.

Other actions are:

```sh
./pronogti-terminal -config ./config.json rotate-token -agent kiosk-terminal-1
./pronogti-terminal -config ./config.json disable-agent -agent kiosk-terminal-1
./pronogti-terminal -config ./config.json enable-agent -agent kiosk-terminal-1
./pronogti-terminal -config ./config.json disable-terminal -id TERMINAL_DATABASE_ID
./pronogti-terminal -config ./config.json enable-terminal -id TERMINAL_DATABASE_ID
```

Agent creation/rotation prints the new plaintext token once; transfer it to the Windows application's configuration rather than logs or frontend code. The CLI has no operator-resolution action. Audited decisions use the administrator API so they carry an authenticated operator identity and supporting evidence.

## Frontend scope

The backend terminal endpoints are available for integration by the kiosk and administrator applications. This backend change does not regenerate or deliver a Vue frontend. Existing public personal-browser routes must not be used to control a physical kiosk terminal. Update the kiosk's terminal payment action and admin controls separately using the documented API and catalogue metadata.
