# Pronogti deployment

Run these commands from the backend checkout on your development computer. One
`pronogti` executable serves the customer/kiosk API, administrator API, bank
callbacks and background workers. The administrator and kiosk are separate Vue
builds. `cmd/admin` provisions accounts; it is not another HTTP service.

## Paths

The supplied configuration keeps the application directory in place:

| Component | Production path |
| --- | --- |
| Executable | `/home/andrey/www/www.katren.org/htdocs/pronogti/pronogti` |
| Administrator frontend | `/home/andrey/www/www.katren.org/htdocs/pronogti/admin` |
| Kiosk/customer frontend | `/home/andrey/www/www.katren.org/htdocs/pronogti/dist` |
| Runtime configuration | `/home/andrey/www/www.katren.org/htdocs/pronogti/config.json` |
| Latest deployed migrations | `/home/andrey/www/www.katren.org/htdocs/pronogti/migrations` |
| Releases | `/home/andrey/www/www.katren.org/pronogti-deploy/releases/<release-id>` |

Only the executable, frontend directories and migrations entry become symlinks.
The application root and configuration remain persistent. Runtime token refresh
writes `config.json` atomically, so the service user needs write access to both
that file and its parent directory. Use a regular persistent configuration file,
not a link into a release.

## One-time setup

1. Create the local deployment configuration:

	```sh
	cp deploy/deploy.conf.example deploy/deploy.conf
	```

2. Edit `deploy/deploy.conf`. `SERVERS=(sakko meln)` reuses your SSH aliases;
	change the list if needed. All hosts use the same remote paths and service
	name. Set `BACKEND_UNIT` to the installed systemd unit name.

3. Set `KIOSK_DIR` to the actual kiosk/customer Vue source directory. It must
	contain `package.json`. The administrator source is already set to
	`/home/andrey/www/dronm-workspace/apps/pronogti-admin`. The supplied Go backend
	directory is not a frontend source directory. Install each frontend's dependencies
	using its normal workspace/package-manager workflow before deployment. Build
	commands are Bash arrays and can use `pnpm` or another package manager.

4. On each server, provide the production `config.json` at `REMOTE_CONFIG_FILE`
	and install the backend systemd service. See
	[pronogti.service.example](pronogti.service.example). The deploy script does not
	upload your development config, enable services or install systemd units.
	Configure PostgreSQL, Redis, YClients, payment/fiscal settings, allowed origins
	and HTTPS cookies for production. Configuration environment references must
	also be available to the service, for example through its `EnvironmentFile`.

5. The local machine needs Bash 4.3+, SSH, rsync, make and Go 1.25+ for a backend
	build, plus the frontend build tools. Remote servers need Bash, rsync, GNU
	coreutils and systemd. The SSH user must be able to write the application and
	release directories. Enable noninteractive SSH access from this same user.

6. The migration host, by default the first entry in `SERVERS`, needs
	`golang-migrate` with PostgreSQL support. Set `MIGRATE_BIN` to its full path if
	it is absent from the SSH login PATH. The complete `migrations/` tree is copied
	to every server; `migrate up` runs once against the shared primary database.
	`migrations/sql/` contains manual helpers and is never executed automatically.

7. Configure nginx to serve the kiosk at `/`, the administrator at `/admin/`,
	and proxy `/api/` to the backend with that prefix removed. See
	[nginx.locations.example](nginx.locations.example). The example belongs inside
	your existing TLS server block; adapt it to your existing configuration.

### Database connection

No separate remote `.env` file is required when `config.json` contains
`database.primary` as a `postgres://` or `postgresql://` URL. Python 3 on the
migration host reads that value without printing it.

If you use `database.primary_env`, the named variable must be available in the
migration SSH session too. Systemd's environment is not inherited by SSH. You
can set `REMOTE_DB_ENV_FILE` to an existing trusted Bash file on the migration
host which defines that variable, or defines `DB_CONN` directly:

```sh
DB_CONN='postgres://USER:PASSWORD@127.0.0.1:5432/pronogti?sslmode=disable'
```

Protect that file from other users. A `DB_CONN` value in this optional file takes
precedence over the JSON config and avoids the Python requirement. It must point
at the primary, not a read replica. The database, database user and necessary
permissions must already exist before the first deployment.

### Service permissions

`SYSTEMCTL_MODE=sudo` runs `sudo -n`, so it never waits for a password prompt.
For the default unit and the usual `/usr/bin/systemctl` location, a narrowly
scoped sudoers entry is:

```sudoers
andrey ALL=(root) NOPASSWD: /usr/bin/systemctl restart pronogti.service, /usr/bin/systemctl stop pronogti.service
```

Install this through `visudo` and adjust the executable path and unit to your
server. `SYSTEMCTL_MODE=direct` uses the SSH user's existing system service
permissions. The optional `BACKEND_HEALTH_URL` is requested from each server;
`http://127.0.0.1:59000/salons` is an existing public database-backed endpoint.
There is no `/health` route. HTTP checks require curl on the servers. Without a
health URL, deployment checks that systemd reports the service active.

### Frontend build settings

The default admin command is `npm run build -- --base=/admin/`; the kiosk uses
`npm run build -- --base=/`. Output defaults to each project's `dist` directory.
If your project uses another build command, set `ADMIN_BUILD_CMD` or
`KIOSK_BUILD_CMD` and the corresponding output directory.

Configure both apps' production API prefix as `/api` using their existing
frontend environment settings. Configure the admin router's history base as
`/admin/` as well if it has a separate setting; Vite's asset base flag alone
cannot change a hard-coded router base. The kiosk's public URL is `/`, even
though its files live in a production directory called `dist`. No development
frontend files or environment settings are modified by deployment.

## Commands

```sh
make deploy-plan
make deploy-all
```

The plan prints destinations and sources without building or connecting to SSH.
It can show an unset `KIOSK_DIR`; an actual kiosk deployment requires that value.

| Command | Components |
| --- | --- |
| `make deploy`, `make deploy-all`, `make deploy-full` | Backend, admin, kiosk and migrations |
| `make deploy-backend` | Backend and migrations |
| `make deploy-admin` | Administrator frontend only |
| `make deploy-kiosk` | Kiosk/customer frontend only |
| `make deploy-frontend`, `make deploy-frontends` | Both frontends |
| `make deploy-migrations` | Upload migrations everywhere and apply once |
| `make deploy-no-migrations` | All components; upload migrations but skip execution |
| `make deploy-plan` | Preview the complete deployment |

Use `DEPLOY_CONFIG=/absolute/path/deploy.conf` to select another local config.
Pass additional options through `DEPLOY_ARGS`, for example:

```sh
make deploy-backend DEPLOY_ARGS=--skip-migrations
make deploy-admin DEPLOY_ARGS=--dry-run
make deploy-all DEPLOY_ARGS=--skip-build
```

`--skip-build` uses the existing `BACKEND_BINARY` and frontend output directories.
The normal flow always builds first. Build output is snapshotted locally before
any SSH operation. Relative frontend project paths and `BACKEND_BINARY` resolve
against the backend checkout; relative frontend output paths resolve against
their frontend project.

## Rollout and recovery

Deployment acquires a lock on every server, stages all artifacts everywhere,
applies pending migrations once and only then activates components. Existing
direct deployments are preserved when first converted to symlinks: their files
are moved into that release's `.deploy-state/<component>/original` directory.
The first conversion of an existing directory has a brief rename interval;
later symlink switches are atomic. Keep old releases and these backups until
you no longer need them. No automatic release deletion is performed.

If an activation or readiness check fails, the script attempts to restore every
component it activated, across all affected servers, in reverse order. It
restarts restored backends or stops a failed first deployment. It reports any
rollback that needs manual attention. A network outage can prevent restoration;
inspect the printed release ID and the active paths on that host before retrying.
Successfully applied database migrations remain applied. Use migrations that
support both application versions during rollout and rollback.

Each lock is `REMOTE_RELEASES_DIR/.deploy-lock`, with an `owner` file containing
the release ID. An interrupted deployment can leave a lock behind. Verify that
no deployment or remote activation is still running before removing its owner
file and empty lock directory. Do not remove release directories that are still
referenced by an active symlink.
