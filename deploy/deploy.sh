#!/usr/bin/env bash
set -euo pipefail

usage() {
	cat <<'USAGE'
Usage: deploy.sh [MODE] [--skip-migrations] [--skip-build] [--dry-run]

Default: deploy the Pronogti backend, admin frontend and kiosk frontend.
Modes:
	--all                Backend and both frontends
	--backend-only       One backend serving both customer and admin APIs
	--frontends-only     Both Vue frontends
	--admin-only         Admin Vue frontend
	--kiosk-only         Kiosk/customer Vue frontend
	--migrations-only    Upload migrations to all servers and apply once

DEPLOY_CONFIG selects a trusted Bash config (default: deploy/deploy.conf).
USAGE
}

die() {
	printf 'Deploy error: %s\n' "$*" >&2
	exit 1
}

mode=''
skip_migrations=false
skip_build=false
dry_run=false
while (($#)); do
	case "$1" in
		--all|--backend-only|--frontends-only|--admin-only|--kiosk-only|--migrations-only)
			[[ -z $mode ]] || die 'Choose one deployment mode.'
			mode=$1
			;;
		--skip-migrations) skip_migrations=true ;;
		--skip-build) skip_build=true ;;
		--dry-run) dry_run=true ;;
		--help|-h) usage; exit 0 ;;
		*) die "Unknown option: $1. See --help." ;;
	esac
	shift
done
[[ $mode != --migrations-only || $skip_migrations == false ]] || die 'Migrations-only cannot skip migrations.'

deploy_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
root_dir=$(cd -- "$deploy_dir/.." && pwd -P)
config=${DEPLOY_CONFIG:-$deploy_dir/deploy.conf}
[[ -r $config ]] || die "Copy deploy/deploy.conf.example to $config and edit SERVERS, BACKEND_UNIT and KIOSK_DIR."
declare -a SERVERS=()
declare -a ADMIN_BUILD_CMD=(npm run build -- --base=/admin/)
declare -a KIOSK_BUILD_CMD=(npm run build -- --base=/)
source "$config"

components=()
copy_migrations=false
case "$mode" in
	''|--all) components=(backend admin kiosk); copy_migrations=true ;;
	--backend-only) components=(backend); copy_migrations=true ;;
	--frontends-only) components=(admin kiosk) ;;
	--admin-only) components=(admin) ;;
	--kiosk-only) components=(kiosk) ;;
	--migrations-only) copy_migrations=true ;;
esac
run_migrations=false
if [[ $copy_migrations == true && $skip_migrations == false ]]; then run_migrations=true; fi

valid_path() {
	[[ $1 == /* && $1 != / && $1 != */ && $1 != *//* &&
		$1 != *[[:cntrl:]]* && $1/ != */../* && $1/ != */./* ]]
}

paths_overlap() {
	[[ $1 == "$2" || $1 == "$2/"* || $2 == "$1/"* ]]
}

((${#SERVERS[@]} > 0)) || die 'Set SERVERS to at least one SSH alias or user@hostname.'
declare -A seen_servers=()
for server in "${SERVERS[@]}"; do
	[[ $server =~ ^([[:alnum:]_][[:alnum:]_.-]*@)?[[:alnum:]_][[:alnum:]_.-]*$ ]] || die "Invalid SSH destination: $server"
	[[ ! ${seen_servers[$server]+set} ]] || die "Duplicate server: $server"
	seen_servers[$server]=1
done
migration_host=${MIGRATION_HOST:-${SERVERS[0]}}
[[ ${seen_servers[$migration_host]+set} ]] || die 'MIGRATION_HOST must belong to SERVERS.'
valid_path "${REMOTE_RELEASES_DIR:-}" || die 'Set REMOTE_RELEASES_DIR to an absolute path.'
valid_path "${REMOTE_CONFIG_FILE:-}" || die 'Set REMOTE_CONFIG_FILE to the stable production JSON config path.'
if paths_overlap "$REMOTE_RELEASES_DIR" "$REMOTE_CONFIG_FILE"; then
	die 'Keep REMOTE_CONFIG_FILE outside the release tree.'
fi
systemctl_mode=${SYSTEMCTL_MODE:-sudo}
[[ $systemctl_mode == sudo || $systemctl_mode == direct ]] || die 'SYSTEMCTL_MODE must be sudo or direct.'
goarch=${DEPLOY_GOARCH:-amd64}
[[ $goarch =~ ^[[:alnum:]_]+$ ]] || die 'Invalid DEPLOY_GOARCH.'
migrate_bin=${MIGRATE_BIN:-migrate}
[[ $migrate_bin != -* && -n $migrate_bin ]] || die 'Set MIGRATE_BIN to a command or absolute executable path.'
env_file=${REMOTE_DB_ENV_FILE:-}
[[ -z $env_file ]] || valid_path "$env_file" || die 'REMOTE_DB_ENV_FILE must be an absolute path when set.'
if [[ -n $env_file ]] && paths_overlap "$REMOTE_RELEASES_DIR" "$env_file"; then
	die 'Keep REMOTE_DB_ENV_FILE outside the release tree.'
fi

declare -A active_paths=(
	[backend]="${REMOTE_BACKEND_PATH:-}"
	[admin]="${REMOTE_ADMIN_PATH:-}"
	[kiosk]="${REMOTE_KIOSK_PATH:-}"
	[migrations]="${REMOTE_MIGRATIONS_PATH:-}"
)
declare -A units=([backend]="${BACKEND_UNIT:-}" [admin]='' [kiosk]='' [migrations]='')
declare -A health_urls=([backend]="${BACKEND_HEALTH_URL:-}" [admin]='' [kiosk]='' [migrations]='')
declare -A kinds=([backend]=file [admin]=directory [kiosk]=directory [migrations]=directory)
declare -A frontend_dirs=([admin]="${ADMIN_DIR:-}" [kiosk]="${KIOSK_DIR:-}")
declare -A frontend_outputs=([admin]="${ADMIN_DIST_DIR:-dist}" [kiosk]="${KIOSK_DIST_DIR:-dist}")

# Check every configured publish path, including components not selected today.
configured_paths=("$REMOTE_RELEASES_DIR" "$REMOTE_CONFIG_FILE")
if [[ -n $env_file && $env_file != "$REMOTE_CONFIG_FILE" ]]; then configured_paths+=("$env_file"); fi
for component in backend admin kiosk migrations; do
	active=${active_paths[$component]}
	[[ -n $active ]] || continue
	valid_path "$active" || die "Set an absolute publish path for $component."
	for path in "${configured_paths[@]}"; do
		if paths_overlap "$active" "$path"; then die "Remote paths overlap: $active and $path"; fi
	done
	configured_paths+=("$active")
done
if [[ $copy_migrations == true ]]; then components+=(migrations); fi
for component in "${components[@]}"; do
	[[ -n ${active_paths[$component]} ]] || die "Set the publish path for $component."
	if [[ $component == backend ]]; then
		[[ ${units[$component]} =~ ^[[:alnum:]_][[:alnum:]_.@:-]*\.service$ ]] || die 'Set BACKEND_UNIT to your installed systemd service name.'
	fi
done

printf 'Servers: %s\n' "${SERVERS[*]}"
for component in "${components[@]}"; do
	printf 'Deploy %-10s -> %s\n' "$component" "${active_paths[$component]}"
	if [[ $component == admin || $component == kiosk ]]; then
		printf '  Source: %s\n' "${frontend_dirs[$component]:-(set ${component^^}_DIR)}"
	fi
done
if [[ $run_migrations == true ]]; then
	printf 'Apply pending migrations once on %s using %s.\n' "$migration_host" "${env_file:-$REMOTE_CONFIG_FILE}"
fi
if [[ $dry_run == true ]]; then
	printf 'Plan only: no builds, SSH connections or changes.\n'
	exit 0
fi

for command in ssh rsync make; do command -v "$command" >/dev/null || die "Required local command missing: $command"; done
[[ -r $deploy_dir/remote.sh ]] || die 'deploy/remote.sh is missing.'
if [[ $copy_migrations == true ]]; then
	compgen -G "$root_dir/migrations/*.up.sql" >/dev/null || die 'No versioned SQL migrations found.'
fi
for component in "${components[@]}"; do
	case "$component" in
		admin|kiosk)
			project=${frontend_dirs[$component]}
			[[ -n $project ]] || die "Set ${component^^}_DIR in $config to the actual Vue source directory."
			if [[ $project != /* ]]; then project=$root_dir/$project; fi
			[[ -f $project/package.json ]] || die "No package.json for $component at $project; select the frontend source, not the backend."
			project=$(cd -- "$project" && pwd -P)
			frontend_dirs[$component]=$project
			output=${frontend_outputs[$component]}
			if [[ $output != /* ]]; then output=$project/$output; fi
			frontend_outputs[$component]=$output
			;;
	esac
done
if [[ -n ${frontend_dirs[admin]} && -n ${frontend_dirs[kiosk]} && ${frontend_dirs[admin]} == "${frontend_dirs[kiosk]}" ]]; then
	die 'Admin and kiosk must use separate frontend source projects.'
fi

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/pronogti-release.XXXXXXXX")
release_id=$(date -u +%Y%m%dT%H%M%SZ)-${work_dir##*.}
release_root=$REMOTE_RELEASES_DIR/$release_id
declare -a locked_servers=()
declare -a attempted_servers=()
declare -a attempted_components=()
rollout_complete=false

# Quote arguments for the remote login shell before Bash receives them.
remote_run() {
	local server=$1
	local command='bash -s --'
	local argument
	shift
	for argument in "$@"; do command+=" '${argument//\'/\'\\\'\'}'"; done
	ssh -o BatchMode=yes -- "$server" "$command" < "$deploy_dir/remote.sh"
}

remote_component() {
	local server=$1 action=$2 component=$3
	remote_run "$server" "$action" "$release_root" "$component" "${kinds[$component]}" \
		"${active_paths[$component]}" "${units[$component]}" "${health_urls[$component]}" \
		"$systemctl_mode" "$REMOTE_CONFIG_FILE" "$release_id"
}

cleanup() {
	local status=$?
	local index
	trap - EXIT
	trap '' HUP INT TERM
	if [[ $rollout_complete == false && ${#attempted_servers[@]} -gt 0 ]]; then
		printf 'Restoring activated application paths on all affected servers.\n' >&2
		for ((index = ${#attempted_servers[@]} - 1; index >= 0; index--)); do
			if ! remote_component "${attempted_servers[$index]}" rollback "${attempted_components[$index]}"; then
				printf 'Rollback needs attention: %s on %s (release %s).\n' "${attempted_components[$index]}" "${attempted_servers[$index]}" "$release_id" >&2
			fi
		done
	fi
	for ((index = ${#locked_servers[@]} - 1; index >= 0; index--)); do
		if ! remote_run "${locked_servers[$index]}" unlock "$REMOTE_RELEASES_DIR" "$release_id"; then
			printf 'Could not release the deploy lock on %s.\n' "${locked_servers[$index]}" >&2
			if ((status == 0)); then status=1; fi
		fi
	done
	rm -rf -- "$work_dir"
	if ((status != 0)); then printf 'Deployment failed. Release: %s\n' "$release_id" >&2; fi
	exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

declare -A artifacts=()
build_frontend() {
	local component=$1 command_name=$2
	local -n build_command=$command_name
	local project=${frontend_dirs[$component]}
	local output=${frontend_outputs[$component]}
	if [[ $skip_build == false ]]; then
		((${#build_command[@]} > 0)) || die "Set the build command for $component."
		(cd -- "$project" && "${build_command[@]}")
	fi
	[[ -d $output ]] || die "No build output for $component at $output"
	output=$(cd -- "$output" && pwd -P)
	if [[ $output == "$project" || $project == "$output/"* || $output == "$root_dir" || $root_dir == "$output/"* ]]; then
		die "Use a dedicated build output directory for $component."
	fi
	[[ -s $output/index.html ]] || die "No built index.html for $component at $output"
	artifacts[$component]=$work_dir/$component
	mkdir -- "${artifacts[$component]}"
	# Snapshot immediately so the next frontend build cannot change this artifact.
	cp -a -- "$output/." "${artifacts[$component]}/"
	local index=${artifacts[$component]}/index.html
	local release_meta="<meta name=\"pronogti-release\" content=\"$release_id\">"
	sed -i -e 's|<meta name="pronogti-release" content="[^"]*">||g' \
		-e "s|</head>|$release_meta</head>|I" "$index"
	[[ $(<"$index") == *"$release_meta"* ]] || die "Cannot stamp the release ID in $component/index.html."
}

# Complete all local builds before connecting to production.
for component in "${components[@]}"; do
	case "$component" in
		backend)
			mkdir -- "$work_dir/backend"
			if [[ $skip_build == true ]]; then
				binary=${BACKEND_BINARY:-pronogti}
				if [[ $binary != /* ]]; then binary=$root_dir/$binary; fi
				[[ -f $binary && -x $binary ]] || die "No executable at $binary for --skip-build."
				cp -p -- "$binary" "$work_dir/backend/pronogti"
			else
				GOOS=linux GOARCH="$goarch" make -C "$root_dir" prod "BUILD_OUTPUT=$work_dir/backend/pronogti"
			fi
			[[ -x $work_dir/backend/pronogti ]] || die 'Backend build did not produce pronogti.'
			artifacts[backend]=$work_dir/backend
			;;
		admin) build_frontend admin ADMIN_BUILD_CMD ;;
		kiosk) build_frontend kiosk KIOSK_BUILD_CMD ;;
		migrations) artifacts[migrations]=$root_dir/migrations ;;
	esac
done

# All deployments use the same lock order, even if SERVERS is reordered.
mapfile -t lock_order < <(printf '%s\n' "${SERVERS[@]}" | LC_ALL=C sort)
for server in "${lock_order[@]}"; do
	remote_run "$server" lock "$REMOTE_RELEASES_DIR" "$release_id"
	locked_servers+=("$server")
done
for server in "${SERVERS[@]}"; do
	for component in "${components[@]}"; do remote_component "$server" preflight "$component"; done
done
if [[ $run_migrations == true ]]; then
	remote_run "$migration_host" check-migrations "$release_root/migrations" "$REMOTE_CONFIG_FILE" "$env_file" "$migrate_bin"
fi

for server in "${SERVERS[@]}"; do
	printf 'Uploading release %s to %s\n' "$release_id" "$server"
	for component in "${components[@]}"; do
		rsync -azs --chmod=Du=rwx,Dgo=rx,Fu=rwX,Fgo=rX -e 'ssh -o BatchMode=yes' -- \
			"${artifacts[$component]}/" "$server:$release_root/$component/"
	done
done
if [[ $run_migrations == true ]]; then
	remote_run "$migration_host" migrate "$release_root/migrations" "$REMOTE_CONFIG_FILE" "$env_file" "$migrate_bin"
fi
notify_admin=false
notify_kiosk=false
if [[ ${artifacts[admin]+built} ]]; then notify_admin=true; fi
if [[ ${artifacts[kiosk]+built} ]]; then notify_kiosk=true; fi
remote_run "$migration_host" prepare-deploy-update "$REMOTE_CONFIG_FILE" "$env_file" "$release_id" "$notify_admin" "$notify_kiosk"
for component in "${components[@]}"; do
	for server in "${SERVERS[@]}"; do
		attempted_servers+=("$server")
		attempted_components+=("$component")
		remote_component "$server" activate "$component"
	done
done
rollout_complete=true
remote_run "$migration_host" complete-deploy-update "$REMOTE_CONFIG_FILE" "$env_file" "$release_id" ||
	die "Release $release_id is active, but its deployment record could not be confirmed."
printf 'Deployed release %s\n' "$release_id"
