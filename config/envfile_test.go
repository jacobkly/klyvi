package config_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"klyvi-api/config"
)

// With no -env flag a job command reads the local stack's settings, so a
// stray run on the PC never writes to the hosted database.
func TestEnvFileFlag_DefaultsToTheLocalStack(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte("KLYVI_ENVFILE_TEST=local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env.dev"), []byte("KLYVI_ENVFILE_TEST=hosted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("KLYVI_ENVFILE_TEST", "")
	os.Unsetenv("KLYVI_ENVFILE_TEST")

	fs := flag.NewFlagSet("job", flag.ContinueOnError)
	envFile := config.EnvFileFlag(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	config.LoadEnvFile(*envFile)

	if got := os.Getenv("KLYVI_ENVFILE_TEST"); got != "local" {
		t.Fatalf("read %q, want the local settings", got)
	}
}

func TestEnvFileFlag_ReachesHostedOnlyWhenAsked(t *testing.T) {
	fs := flag.NewFlagSet("job", flag.ContinueOnError)
	envFile := config.EnvFileFlag(fs)
	if err := fs.Parse([]string{"-env", ".env.dev"}); err != nil {
		t.Fatal(err)
	}
	if *envFile != ".env.dev" {
		t.Fatalf("env file %q, want .env.dev", *envFile)
	}
}
