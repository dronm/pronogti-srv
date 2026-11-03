package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSalonCommandValidatesBeforeLoadingConfig(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{nil, "usage:"},
		{[]string{"unknown"}, "unknown salon action"},
		{[]string{"upsert"}, "positive -company"},
		{[]string{"upsert", "-company", "781898", "-slug", "Upper", "-name", "Salon"}, "salon slug"},
		{[]string{"upsert", "-company", "781898", "-slug", "valid"}, "salon name"},
		{[]string{"disable", "-company", "0"}, "positive -company"},
		{[]string{"enable", "-company", "-1"}, "positive -company"},
		{[]string{"upsert", "-company", "781898", "-slug", "valid", "-name", "Salon", "extra"}, "unexpected positional"},
	} {
		args := append([]string{"-config", "/missing-pronogti-salon-test-config.json"}, tt.args...)
		var out, diagnostics bytes.Buffer
		err := run(args, &out, &diagnostics)
		if err == nil || !strings.Contains(err.Error(), tt.want) || out.Len() != 0 {
			t.Fatalf("args=%v error=%v output=%q", tt.args, err, out.String())
		}
	}
}
