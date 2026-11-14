package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestTerminalOperatorRejectsInvalidCommandsBeforeOpeningDatabase(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"register-agent"}, {"rotate-token"}, {"register-terminal", "-agent", "one"}, {"status"}, {"enable-terminal", "-id", "0"}, {"list", "unexpected"}} {
		var out, errOut bytes.Buffer
		if err := run(args, &out, &errOut); err == nil || strings.Contains(err.Error(), "configuration") {
			t.Fatalf("invalid command reached config/database: %v err=%v", args, err)
		}
	}
}
