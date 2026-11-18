package main

import (
	"testing"

	"klyvi-api/config"
)

// Run with no -env flag, the command reads the local stack's settings.
func TestEnvFlagDefaultsToTheLocalStack(t *testing.T) {
	f := flags.Lookup("env")
	if f == nil {
		t.Fatal("no -env flag")
	}
	if f.DefValue != config.LocalEnvFile {
		t.Fatalf("-env defaults to %q, want %q", f.DefValue, config.LocalEnvFile)
	}
}
