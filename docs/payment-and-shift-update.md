# Payment diagnostics and shift closing

## Deployment

1. Apply migrations `000022_admin_fiscal_error_details` and
	`000023_fiscal_shift_requests` before starting the updated backend.
	`make migup` applies one migration at a time.
2. Build and restart the backend and the separately running TerminalFA HTTP
	service from `third_party/goterminalfa`. The driver needs the new
	`closeShiftIfOpen` command.
3. Build and deploy both frontend applications from their normal workspace,
	with the existing local `@katren` packages available for the admin build.
4. For live shift closing, retain or enable `fiscal.allow_live=true`. To allow
	the kiosk maintenance page, also set `fiscal.shift_operator_token` to a
	private random value of 16 to 512 bytes and restart the backend. This token
	is entered by the operator on the maintenance page.

See [Closing fiscal shifts](shift-closing.md) for the complete configuration,
HTTP timeout settings, CLI installation, persistent retry state, and cron example.
Existing `config.json` values have been preserved in this update.

## Fiscal receipt details

Payment order details now show the saved fiscal error message separately from
the code. **Полный ответ ККТ** expands the full stored response, including nested
failure and cleanup information. Long messages wrap and retain line breaks.

Historical responses become visible after migration 22. A historical row that
stored only a code cannot recover an explanation that was never recorded. New
local validation, configuration, and transport failures also save a readable
message for the administrator.

## QR descriptions

New QR payments use `Оплата <order ID>` as their bank payment purpose. Existing
QR codes retain their original bank description. Confirmation supports both
the new purpose and the previous `Checkout <order ID>` format, including early
webhook arrival before the QR registration response is saved.

## Close an open shift

- Admin: open a saved fiscal profile and choose **Закрыть смену**.
- Kiosk: open `https://pronogti.katren.org/kiosk/close-shift` in the bound kiosk
	browser, enter the operator key, and confirm closing.
- Backend host: build `make build-fiscal-shift`, then use
	`./pronogti-fiscal-shift -config /absolute/path/config.json -all`, or replace
	`-all` with `-profile ID`.
- Cron: install and schedule `scripts/close-fiscal-shifts.sh` as described in
	[Closing fiscal shifts](shift-closing.md). The wrapper retains an unfinished
	request key and prevents overlapping runs.

An already closed shift is a successful no-op. Opening the kiosk URL does not
send a closing command. A later sale may open a new shift through the existing
receipt workflow.

## Verification

Backend and bundled driver unit tests passed. New fiscal error, Russian and
legacy QR confirmation, and shift integration checks run against an isolated
PostgreSQL database. The kiosk production build and its 77 tests passed. New
admin diagnostics/API tests and the added panel/schema/API type checks passed.
The full admin build requires the project's matching local `@katren` packages;
those packages were not included in the uploaded source archives.

Two existing payment migration rollback tests fail against both the original
and updated backend when all integration tests are enabled: they roll migration
9 back while later administrator views still depend on it. The remaining
integration suite passes. Physical KKT operation has not been tested here.
