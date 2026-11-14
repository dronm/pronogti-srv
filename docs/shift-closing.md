# Closing fiscal shifts

A shift is closed through the same TerminalFA HTTP service used for receipts.
Update **both the Pronogti backend and the bundled goterminalfa service** for the
new `closeShiftIfOpen` command. Apply migrations `000022` and `000023` before
starting the updated backend. Migration `000022` exposes full fiscal errors;
`000023` freezes shift-operation settings before dispatch, so retries cannot be
redirected to an edited profile connection or a new kiosk assignment. It checks the shift and, only when it is open,
executes the closing sequence under one device lock. An already closed shift is
a successful no-op. Fiscal profile settings supply the cashier, device, driver
authentication, printing option, and timeout. Disabled profiles are rejected;
demo profiles return a visible simulation result without contacting the device.
Live closure requires `fiscal.allow_live=true`.

## Administrator

Open the saved fiscal profile in the admin application and choose **Закрыть
смену**. Confirm the action. The result shows whether the shift was closed,
already closed, or failed, including the full device response.

The endpoint is `POST /admin/fiscal-profiles/{id}/close-shift`, with the existing
administrator cookie and `X-Admin-Session` nonce, one `Idempotency-Key` header,
and an empty JSON object. Use the same key after a lost response or an unknown
outcome. A definite failed result is also cached: after fixing its cause, an
operator can explicitly start a new attempt with a new key.

## Kiosk maintenance URL

Open `/kiosk/close-shift` on the kiosk, for example
`https://pronogti.katren.org/kiosk/close-shift`. The browser must already be bound
to an active kiosk; a customer session is not required. Enter the operator code
and confirm **Закрыть смену**. Opening the URL alone never closes the shift.

Set a long random operator code in server configuration:

```json
{
	"fiscal": {
		"worker_enabled": true,
		"allow_live": true,
		"poll_interval": "2s",
		"shift_operator_token": "REPLACE_WITH_A_LONG_RANDOM_OPERATOR_CODE"
	}
}
```

The code must have 16 to 512 bytes. An empty or omitted code disables kiosk shift
closing while admin and the local command remain available. The kiosk submits it
in the JSON body, never in a URL, and does not save it in browser storage.

The endpoint is `POST /kiosk/fiscal/close-shift`, with the bound kiosk cookie,
one `Idempotency-Key`, and `{"operator_token":"..."}`. The server selects the
kiosk's fiscal profile, falling back to its salon's default; callers cannot
choose another profile or device.

## Timeouts

Fiscal profiles allow a driver timeout up to 300 seconds. The complete HTTP chain
must allow that duration. For profiles with a 300-second timeout, set backend
`http.request_timeout` to `"310s"` and `http.write_timeout` to `"320s"`, and set
Nginx `proxy_read_timeout 330s;` for the backend location. The admin and kiosk
close-shift calls allow 330 seconds. A shorter timeout does not justify a new
operation key: retry with the original key to read/replay the driver's result.
Driver redirects are rejected, and malformed or mismatched responses are
reported as unknown rather than guessed as successful.

## Local command and daily cron

Build on the backend host (not the Windows kiosk):

```bash
make build-fiscal-shift
sudo install -m 0755 pronogti-fiscal-shift /usr/local/bin/pronogti-fiscal-shift
sudo install -m 0755 scripts/close-fiscal-shifts.sh /usr/local/bin/pronogti-close-fiscal-shifts
sudo install -d -m 0700 -o andrey -g andrey /var/lib/pronogti/fiscal-shifts
```

Manual one-profile run:

```bash
./pronogti-fiscal-shift -config /absolute/path/config.json -profile 1
```

Manual all-device run:

```bash
./pronogti-fiscal-shift -config /absolute/path/config.json -all
```

`-all` selects enabled live profiles and closes each driver URL/device pair once,
even when several profiles share the device. A nonzero exit means a failed,
unknown, interrupted, or configuration error. It fails clearly if
`fiscal.allow_live` is false. The command prints its `request_key` before
contacting hardware and emits JSON outcomes. For manual reconciliation, pass the
printed key back with `-request-key KEY`. Database connection variables referenced by `database.primary_env`, and live
profile bearer-token environment variables, must also be present in the cron
environment if configured. The standalone command does not source `.env` files.

For unattended runs, use the wrapper so a pending request key survives failure
or process termination. Its lock prevents overlapping runs. Its state directory
must be writable by the cron user and persistent between invocations. Use a
separate `PRONOGTI_SHIFT_STATE_DIR` for each independently scheduled scope.

Example user crontab, every day at 23:00 in the server's local timezone:

```cron
0 23 * * * PRONOGTI_CONFIG=/absolute/path/config.json /usr/local/bin/pronogti-close-fiscal-shifts >>/home/andrey/pronogti-shift-close.log 2>&1
```

To schedule just one profile, append `-profile 1`. Override
`PRONOGTI_FISCAL_SHIFT_BIN` if the binary is installed elsewhere.

The wrapper retains `pending-key` and `pending-scope` after **every** unsuccessful
run. Following runs replay the same operation, including already successful
profiles in a partially failed batch; they cannot silently close a new shift
instead. Check the driver journal and actual KKT state after an unknown outcome.
Do not change profile connection settings or the scheduled scope while resolving
a pending operation. Once an operator has reconciled the result, or fixed a
**definite** failed operation and intends a new attempt, remove both pending files
from the state directory. Never reset an unknown operation just to force a retry.
Successful runs remove pending state, allowing the next day's new operation.

The wrapper uses standard Linux utilities including `flock`, `sha256sum`, `od`,
and `mktemp`. No cron job is installed automatically. Shift closure is separate
from receipt processing; a later sale may open a new shift through the existing
auto-open workflow.
