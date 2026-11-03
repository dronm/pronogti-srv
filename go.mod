module github.com/dronm/pronogti

go 1.25.0

require (
	github.com/dronm/ds/v4 v4.0.0-20260612024620-1d1425e7a834
	github.com/dronm/goyclients v0.0.0-20260114113417-d78b12de7dd5
	github.com/dronm/modelbind v0.0.0-20261029092949-96c71e56f70f
	github.com/dronm/session v0.0.0-20251220011348-444cd0bd6dad
	github.com/dronm/webapp v0.0.0-20261029093221-bf472cb24144
	github.com/redis/go-redis/v9 v9.17.2
	golang.org/x/text v0.29.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/creack/goselect v0.1.2 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/dronm/goterminalfa v0.0.0 // indirect
	github.com/dronm/paytochkabank v0.0.0
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.9.2 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	go.bug.st/serial v1.6.4 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/sys v0.22.0 // indirect
	modernc.org/libc v1.55.3 // indirect
	modernc.org/mathutil v1.6.0 // indirect
	modernc.org/memory v1.8.0 // indirect
	modernc.org/sqlite v1.34.5 // indirect
)

// The uploaded receipt driver is kept as a source dependency. Its high-level
// workflow is exposed by HTTP; run it separately with go tool goterminalfa.
replace github.com/dronm/goterminalfa => ./third_party/goterminalfa

tool github.com/dronm/goterminalfa/cmd/goterminalfa

// The customized YClients source is bundled for reproducible deployment.
replace github.com/dronm/goyclients => ./third_party/goyclients

replace github.com/dronm/paytochkabank => ./third_party/paytochkabank
