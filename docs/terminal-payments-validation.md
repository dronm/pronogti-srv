# Terminal payment backend validation

Validated on 14 November 2026 using Go 1.26.11 and an isolated PostgreSQL 17 test database.

Passed:

- The full backend `go test -race ./...` suite with PostgreSQL integration enabled, including existing payment, fiscalization and HTTP tests.
- `go vet ./...` and formatting checks.
- All backend schemas pass `codegen validate` using an isolated configuration that disables external frontend, API-test and generated-registry outputs. The checked-in production codegen configuration is unchanged; validating its external Vue dependency requires the separate frontend workspace.
- Native production-style builds of `cmd/pronogti` and `cmd/terminal` with `CGO_ENABLED=0`, `-buildvcs=false` and `-trimpath`.
- Migration 24 up/down/up, guarded rollback with financial history, terminal-only payment profiles and safe administrative projections.
- Authoritative kiosk checkout and ownership checks, idempotent retries after payment/expiry, immutable routing, company-wide physical-device exclusion and simulator/live isolation.
- Agent authentication, rotated-token fencing, strict bounded requests, durable event acknowledgements, duplicate/conflicting results and monotonic paid state.
- Claim/reclaim recovery, expiry only before first delivery, uncertain-payment holds and operator decisions finalized only after the matching durable Bridge acknowledgement.
- Reuse of the existing fiscal and YClients job pipeline without duplicate financial completion.

The companion Terminal Bridge 0.2.0 passes its native tests, race checks, native/Windows static checks and Windows amd64 cross-builds. Its release includes its own validation notes and executable checksums.

No production configuration, bank account, terminal or production database was used for validation. Actual PAX D230 payment behavior and the Windows tray interface still require acceptance testing on the kiosk with the bank-approved connector profile. The kiosk/admin frontend changes are separate from this backend delivery.
