package main

import (
	"bytes"
	"testing"
)

func TestKioskCommandValidatesBeforeOpeningDatabase(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"unknown"},
		{"create"},
		{"create", "-name", "Desk", "-company", "0"},
		{"rotate"},
		{"disable", "-id", "-1"},
		{"enable", "-id", "0"},
		{"create", "-name", "Desk", "-company", "12", "extra"},
	} {
		var out, diagnostics bytes.Buffer
		if err := run(args, &out, &diagnostics); err == nil {
			t.Fatalf("accepted invalid arguments %v", args)
		}
		if out.Len() != 0 {
			t.Fatalf("unexpected credential output on invalid arguments %v", args)
		}
	}
}
