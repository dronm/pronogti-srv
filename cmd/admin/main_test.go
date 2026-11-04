package main

import (
	"io"
	"strings"
	"testing"
)

func TestParseAdministratorCommand(t *testing.T) {
	for _, args := range [][]string{
		{"create", "-name", "admin"},
		{"-config", "other.json", "reset-password", "-name", "Operator", "-password-env", "PRONOGTI_ADMIN_PASSWORD"},
	} {
		value, err := parseCommand(args, io.Discard)
		if err != nil || value.name == "" {
			t.Fatalf("parse %v: %+v %v", args, value, err)
		}
		if args[0] == "-config" && (!value.reset || value.configPath != "other.json") {
			t.Fatalf("reset/config ignored: %+v", value)
		}
	}
	for _, args := range [][]string{
		{}, {"delete", "-name", "admin"}, {"create"}, {"create", "-name", "admin", "extra"},
		{"create", "-name", "admin", "-password", "leak"}, {"create", "-name", "admin", "-password-env", "BAD-VAR"},
	} {
		if _, err := parseCommand(args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestPasswordInputPreservesWhitespaceAndDoesNotUseArguments(t *testing.T) {
	password, err := readPassword(strings.NewReader("  password spaces  \r\n"), "", func(string) string { return "unused" })
	if err != nil || password != "  password spaces  " {
		t.Fatalf("password whitespace changed: %v", err)
	}
	password, err = readPassword(strings.NewReader("ignored"), "SECRET", func(name string) string {
		if name == "SECRET" {
			return "env password"
		}
		return ""
	})
	if err != nil || password != "env password" {
		t.Fatalf("environment password: %v", err)
	}
	if _, err = readPassword(strings.NewReader(""), "SECRET", func(string) string { return "" }); err == nil {
		t.Fatal("accepted missing environment password")
	}
	if _, err = readPassword(strings.NewReader(strings.Repeat("x", 1000)), "", func(string) string { return "" }); err == nil {
		t.Fatal("accepted unbounded password")
	}
}
