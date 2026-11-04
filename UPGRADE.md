# Updating an existing installation

Extract a release into a clean source directory and copy your local configuration
into it. Extracting a ZIP over an older directory replaces files but does not
remove files deleted by a later release.

For installations updated over the former draft-saving implementation, remove
these retired files from the backend root:

```sh
rm -f internal/services/payDraft_test.go
rm -f internal/services/qr.go
rm -f internal/models/payDraftUpdate.go
rm -f internal/models/qr.go
```

These files belong to the removed `SavePayDraft` API and former static QR stub.
Keep the current `internal/services/payDraft.go`: it still reads existing unpaid
visits. Do not delete the entire services or models directory.

The retired `payDraft_test.go` causes `SavePayDraft undefined` when it survives
an archive overlay. This release tests pending paid-visit filtering in
`internal/services/payDraft_completion_test.go`; keep that replacement test.

Keep `config.json` or `pronogti.json`, deployment environment variables, database
contents, kiosk credentials, and the Terminal FA driver's persistent SQLite
journal. The example configuration files contain placeholders, not your runtime
credentials.

Run all pending migrations before starting the updated backend. `make migup`
applies only one migration; repeat it until current, or use the migration tool's
`up` command without a count:

```sh
migrate -database "$PRONOGTI_DATABASE_URL" -path ./migrations up
make test
make build
```

Apply all pending migrations through `000011_admin_navigation`, including
`000009_card_checkout`. Existing QR profiles
continue working; card checkout is disabled until its acquiring settings are
configured. See [card payments](docs/card-payments.md) and
[QR payments](docs/qr-payments.md) for bank, synchronization and fiscal
configuration. Do not delete previous payment records or external request
journals to clear an uncertain outcome; recovery uses their original identities.

Administrator login requires migration `000010_admin_auth` and a bcrypt password
provisioned through `cmd/admin`. No legacy password is automatically accepted.
Keep the bundled `third_party/session` directory and its Go module replacement:
it isolates administrator and customer Redis managers. See
[administrator setup](docs/admin.md) for credentials, cookie/origin configuration
and installation of the separate Vue workspace app.

Migration `000011_admin_navigation` adds the saved administrator menu and route
registry, including a default Home entry. Restart the backend and rebuild the
administrator Vue app together. Profile, Menu constructor, All forms, About and
Logout are supplied by the shared menu. In All forms, synchronize the compiled
route manifest after adding generated pages; saved menu entries retain their
route IDs across synchronization.
