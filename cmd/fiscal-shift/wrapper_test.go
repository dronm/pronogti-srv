package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScheduledWrapperRetainsPendingKeyAfterFailure(t *testing.T) {
	for _, command := range []string{"bash", "flock", "sha256sum", "od", "mktemp"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Skip(command + " unavailable")
		}
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "fake-fiscal-shift")
	fixture := `#!/usr/bin/env bash
set -euo pipefail
while (($#)); do
	if [[ "$1" == -request-key ]]; then
		printf '%s\n' "$2" >> "$SHIFT_TEST_LOG"
		break
	fi
	shift
done
if [[ -f "$SHIFT_TEST_FAIL" ]]; then
	exit 1
fi
`
	if err := os.WriteFile(binary, []byte(fixture), 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "calls")
	failPath := filepath.Join(dir, "fail")
	statePath := filepath.Join(dir, "state")
	if err := os.WriteFile(failPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		command := exec.Command("bash", append([]string{"../../scripts/close-fiscal-shifts.sh"}, args...)...)
		command.Env = append(os.Environ(), "PRONOGTI_FISCAL_SHIFT_BIN="+binary, "PRONOGTI_CONFIG="+filepath.Join(dir, "config.json"), "PRONOGTI_SHIFT_STATE_DIR="+statePath, "SHIFT_TEST_LOG="+logPath, "SHIFT_TEST_FAIL="+failPath)
		return command.Run()
	}
	if err := run(); err == nil {
		t.Fatal("failed close returned success")
	}
	pending, err := os.ReadFile(filepath.Join(statePath, "pending-key"))
	if err != nil || len(pending) == 0 {
		t.Fatal("pending key not persisted")
	}
	if err := run("-profile", "5"); err == nil {
		t.Fatal("changed scope bypassed pending operation")
	}
	if err := os.Remove(failPath); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(statePath, "pending-key")); !os.IsNotExist(err) {
		t.Fatal("completed request key not cleared")
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	keys := strings.Fields(string(calls))
	if len(keys) != 3 || keys[0] != keys[1] || keys[1] == keys[2] {
		t.Fatalf("wrong persisted request keys: %v", keys)
	}
}
