#!/usr/bin/env bash
set -euo pipefail
umask 022

fail() {
	printf 'Remote deploy error: %s\n' "$*" >&2
	exit 1
}

valid_path() {
	[[ $1 == /* && $1 != / && $1 != */ && $1 != *//* &&
		$1 != *[[:cntrl:]]* && $1/ != */../* && $1/ != */./* ]]
}

separate_paths() {
	[[ $1 != "$2" && $1 != "$2/"* && $2 != "$1/"* ]]
}

action=${1:-}
shift || fail 'An action is required.'
case "$action" in
	lock|unlock)
		(($# == 2)) || fail 'Expected release root and lock token.'
		root=$1
		token=$2
		valid_path "$root" || fail 'Invalid release root.'
		if [[ $action == lock ]]; then
			mkdir -p -- "$root"
			mkdir -- "$root/.deploy-lock" || fail "Deployment is already locked at $root/.deploy-lock."
			printf '%s\n' "$token" > "$root/.deploy-lock/owner"
		elif [[ -f $root/.deploy-lock/owner && $(<"$root/.deploy-lock/owner") == "$token" ]]; then
			rm -- "$root/.deploy-lock/owner"
			rmdir -- "$root/.deploy-lock"
		fi
		exit 0
		;;
	check-migrations|migrate)
		(($# == 4)) || fail 'Expected migrations directory, JSON config, optional env file and migration command.'
		migration_dir=$1
		config_file=$2
		env_file=$3
		migration_bin=$4
		command -v "$migration_bin" >/dev/null || fail "Cannot find migration command: $migration_bin"
		unset DB_CONN
		if [[ -n $env_file ]]; then
			[[ -r $env_file ]] || fail "Cannot read migration environment: $env_file"
			set -a
			source "$env_file"
			set +a
		fi
		if [[ -z ${DB_CONN:-} ]]; then
			command -v python3 >/dev/null || fail 'Python 3 is required to read the database URL from JSON; alternatively set DB_CONN in REMOTE_DB_ENV_FILE.'
			DB_CONN=$(python3 - "$config_file" <<'PY'
import json
import os
import sys

try:
	with open(sys.argv[1], encoding="utf-8-sig") as stream:
		config = json.load(stream)
	database = config.get("database", {})
	value = database.get("primary") or os.environ.get(database.get("primary_env", "PRONOGTI_DATABASE_URL"), "")
	if not isinstance(value, str) or not value.strip():
		raise ValueError("database.primary or its primary_env environment variable must contain the primary PostgreSQL URL")
	print(value)
except (OSError, ValueError, TypeError, AttributeError) as error:
	print("Cannot resolve migration database: " + str(error), file=sys.stderr)
	sys.exit(1)
PY
			) || fail 'Configure the primary database URL in the remote JSON config or REMOTE_DB_ENV_FILE.'
		fi
		[[ $DB_CONN == postgres://* || $DB_CONN == postgresql://* ]] || fail 'golang-migrate requires DB_CONN to be a PostgreSQL URL.'
		if [[ $action == migrate ]]; then
			"$migration_bin" -path "$migration_dir" -database "$DB_CONN" up
		fi
		exit 0
		;;
esac

(($# == 9)) || fail 'Expected release root, component, kind, active path, unit, health URL, systemctl mode, config path and lock token.'
release_root=$1
component=$2
kind=$3
active_path=$4
unit=$5
health_url=$6
systemctl_mode=$7
config_file=$8
lock_token=$9
[[ $component == backend || $component == admin || $component == kiosk || $component == migrations ]] || fail 'Invalid component.'
[[ $kind == file || $kind == directory ]] || fail 'Invalid component kind.'
valid_path "$release_root" && valid_path "$active_path" || fail 'Invalid publish path.'
[[ -z $config_file ]] || valid_path "$config_file" || fail 'Invalid config path.'
separate_paths "$release_root" "$active_path" || fail 'Release and active paths overlap.'
[[ $systemctl_mode == sudo || $systemctl_mode == direct ]] || fail 'Invalid systemctl mode.'
[[ -z $unit || $unit =~ ^[[:alnum:]_][[:alnum:]_.@:-]*\.service$ ]] || fail 'Invalid systemd unit.'
[[ -z $health_url || $health_url == http://* || $health_url == https://* ]] || fail 'Health URL must use HTTP or HTTPS.'
lock_dir=${release_root%/*}/.deploy-lock
[[ -f $lock_dir/owner && $(<"$lock_dir/owner") == "$lock_token" ]] || fail 'Deployment lock ownership changed.'

release_path=$release_root/$component
if [[ $kind == file ]]; then
	release_path=$release_path/pronogti
fi
active_parent=${active_path%/*}
state_dir=$release_root/.deploy-state/$component
systemctl_bin=''
if [[ -n $unit ]]; then
	systemctl_bin=$(command -v systemctl) || fail 'systemctl is required.'
fi

service_action() {
	if [[ $systemctl_mode == sudo ]]; then
		sudo -n -- "$systemctl_bin" "$1" "$unit"
	else
		"$systemctl_bin" --no-ask-password "$1" "$unit"
	fi
}

wait_for_health() {
	local attempt
	for ((attempt = 1; attempt <= 8; attempt++)); do
		if { [[ -z $unit ]] || "$systemctl_bin" is-active --quiet "$unit"; } &&
			{ [[ -z $health_url ]] || curl -q --fail --silent --output /dev/null --max-time 2 -- "$health_url"; }; then
			return 0
		fi
		if ((attempt < 8)); then sleep 1; fi
	done
	return 1
}

switch_link() {
	local target=$1
	local temporary
	temporary=$(mktemp -d -- "$active_parent/.pronogti-link.XXXXXXXX") || return 1
	if ! ln -s -- "$target" "$temporary/active" || ! mv -Tf -- "$temporary/active" "$active_path"; then
		rm -f -- "$temporary/active"
		rmdir -- "$temporary"
		return 1
	fi
	rmdir -- "$temporary"
}

case "$action" in
	preflight)
		command -v rsync >/dev/null || fail 'rsync is required on every server.'
		mv --version >/dev/null 2>&1 || fail 'GNU coreutils are required.'
		[[ ! -e $release_root && ! -L $release_root || -d $release_root && ! -L $release_root ]] || fail 'Release root is not a directory.'
		mkdir -p -- "$active_parent" "$release_root"
		[[ -w $active_parent && -x $active_parent && -w $release_root ]] || fail 'Publish and release directories must be writable.'
		physical_release=$(cd -P -- "$release_root" && pwd -P)
		physical_parent=$(cd -P -- "$active_parent" && pwd -P)
		separate_paths "$physical_release" "$physical_parent/${active_path##*/}" || fail 'Physical publish and release paths overlap.'
		if [[ -e $active_path && ! -L $active_path ]]; then
			if [[ $kind == file ]]; then
				[[ -f $active_path ]] || fail "Expected an executable file at $active_path"
			else
				[[ -d $active_path ]] || fail "Expected a directory at $active_path"
			fi
		fi
		if [[ -n $unit ]]; then
			[[ -r $config_file ]] || fail "Place the production configuration at $config_file before deploying."
			[[ -f $config_file && ! -L $config_file ]] || fail 'Use a regular persistent config file; token refresh replaces config symlinks.'
			[[ $("$systemctl_bin" show --property=LoadState --value "$unit") == loaded ]] || fail "Install the systemd service $unit first."
			if [[ $systemctl_mode == sudo ]]; then
				sudo -n -l -- "$systemctl_bin" restart "$unit" >/dev/null || fail "Passwordless restart is required for $unit."
				sudo -n -l -- "$systemctl_bin" stop "$unit" >/dev/null || fail "Passwordless stop is required for $unit."
			fi
		fi
		if [[ -n $health_url ]]; then command -v curl >/dev/null || fail 'curl is required for HTTP checks.'; fi
		;;
	activate)
		if [[ $kind == file ]]; then
			[[ -f $release_path && -x $release_path ]] || fail 'Staged backend executable is missing.'
		else
			[[ -d $release_path ]] || fail 'Staged directory is missing.'
		fi
		mkdir -p -- "${state_dir%/*}"
		mkdir -- "$state_dir" || fail 'This component was already activated for this release.'
		if [[ -L $active_path ]]; then
			readlink -- "$active_path" > "$state_dir/previous-link"
		elif [[ -e $active_path ]]; then
			# Preserve existing direct deployments on their first conversion to symlinks.
			mv -- "$active_path" "$state_dir/original"
		else
			: > "$state_dir/previous-missing"
		fi
		switch_link "$release_path" || fail "Could not publish $active_path"
		if [[ -n $unit ]]; then service_action restart; fi
		wait_for_health || fail "Readiness check failed for ${unit:-$active_path}."
		printf 'Activated %s -> %s\n' "$active_path" "$release_path"
		;;
	rollback)
		[[ -d $state_dir && ! -f $state_dir/rolled-back ]] || exit 0
		if [[ -e $active_path || -L $active_path ]]; then
			[[ -L $active_path && $(readlink -- "$active_path") == "$release_path" ]] || fail "Active path changed independently; refusing rollback: $active_path"
		fi
		if [[ -f $state_dir/previous-link ]]; then
			switch_link "$(<"$state_dir/previous-link")"
		elif [[ -e $state_dir/original ]]; then
			if [[ -L $active_path ]]; then rm -- "$active_path"; fi
			mv -- "$state_dir/original" "$active_path"
		elif [[ -f $state_dir/previous-missing ]]; then
			if [[ -n $unit ]]; then service_action stop; fi
			rm -f -- "$active_path"
		else
			fail "Incomplete rollback state: $state_dir"
		fi
		if [[ -n $unit && ! -f $state_dir/previous-missing ]]; then
			service_action restart
		fi
		: > "$state_dir/rolled-back"
		printf 'Restored %s\n' "$active_path"
		;;
	*) fail "Unknown action: $action" ;;
esac
