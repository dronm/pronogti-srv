#!/usr/bin/env bash
set -euo pipefail
umask 077

# Build cmd/fiscal-shift once and point this wrapper at the installed binary.
# Pass -profile ID to select one profile, or omit arguments to close all live devices.
shift_binary="${PRONOGTI_FISCAL_SHIFT_BIN:-/usr/local/bin/pronogti-fiscal-shift}"
shift_config="${PRONOGTI_CONFIG:-/etc/pronogti/config.json}"
shift_state="${PRONOGTI_SHIFT_STATE_DIR:-/var/lib/pronogti/fiscal-shifts}"
if (($# == 0)); then
	set -- -all
fi
for shift_arg in "$@"; do
	if [[ "$shift_arg" == -request-key || "$shift_arg" == -request-key=* || "$shift_arg" == --request-key || "$shift_arg" == --request-key=* ]]; then
		printf '%s\n' 'The scheduled wrapper manages request keys; use the CLI directly for manual reconciliation.' >&2
		exit 2
	fi
done
mkdir -p "$shift_state"
exec 9>"$shift_state/lock"
flock -n 9 || exit 0
shift_key_file="$shift_state/pending-key"
shift_scope_file="$shift_state/pending-scope"
shift_scope=$(printf '%s\0' "$shift_config" "$@" | sha256sum | cut -d ' ' -f 1)
if [[ -e "$shift_key_file" ]]; then
	if [[ ! -f "$shift_scope_file" ]] || [[ "$(cat "$shift_scope_file")" != "$shift_scope" ]]; then
		printf '%s\n' 'A pending close operation belongs to different arguments/configuration. Reconcile it before changing the scheduled scope.' >&2
		exit 1
	fi
	shift_key=$(cat "$shift_key_file")
else
	shift_key=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
	printf '%s\n' "$shift_scope" >"$shift_scope_file"
	shift_pending=$(mktemp "$shift_state/pending-key.XXXXXX")
	printf '%s\n' "$shift_key" >"$shift_pending"
	mv "$shift_pending" "$shift_key_file"
fi
if "$shift_binary" -config "$shift_config" "$@" -request-key "$shift_key"; then
	rm -f "$shift_key_file" "$shift_scope_file"
else
	shift_exit=$?
	printf '%s\n' 'Close did not complete definitively. The pending request key is retained for the next run; inspect the log before resetting it.' >&2
	exit "$shift_exit"
fi
