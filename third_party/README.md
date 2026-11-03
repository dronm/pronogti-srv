# Bundled dependencies

## Receipt driver

`goterminalfa` is the unmodified source from the user-supplied `goterminalfa.zip` (SHA-256 `83ec72e6951d92aff66ce9b9591dec21834b732a9cd915c59af6deaef8a244ae`). Runtime SQLite data, logs and local configuration are excluded.

The backend pins this snapshot with a relative Go module replacement and declares its command as a Go tool. The complete receipt workflow runs in the separate driver HTTP service; the backend uses the documented HTTP API. See [receipt configuration](../docs/fiscalization.md).

## Tochka payment client

`paytochkabank` is the unmodified source from the user-supplied `payTochkaBank.zip`
(SHA-256 `f31bd53b16cc50f628b696473441acc6f2403dc6831661125701340eed7ba30b`).
The backend pins this source snapshot through a relative Go module replacement.
It uses `SBP.CreateDynamicQR` for a fixed-amount dynamic QR and preserves uncertain
write outcomes; a lost response is not permission to create a second QR.

## YClients client

`goyclients` contains the customized user-supplied source, including the
context-aware account, sale and visit APIs required by settlement. The backend
pins it through `replace github.com/dronm/goyclients => ./third_party/goyclients`
so deployments do not depend on a developer's home directory. Integration tests
that need live credentials remain opt-in; ordinary tests use local HTTP fixtures.
