package config

import (
	"flag"
	"log"

	"github.com/joho/godotenv"
)

// LocalEnvFile holds the local Supabase stack's settings. Job commands read it
// unless told otherwise, so reaching the hosted database takes -env .env.dev.
const LocalEnvFile = ".env.local"

// EnvFileFlag registers -env on fs, defaulting to the local stack.
func EnvFileFlag(fs *flag.FlagSet) *string {
	return fs.String("env", LocalEnvFile, "env file to read (-env .env.dev reaches the hosted database; already-set env vars win)")
}

// LoadEnvFile reads path into the environment without overriding variables
// that are already set. A missing file is fine: on the server the container
// environment carries the settings.
func LoadEnvFile(path string) {
	if err := godotenv.Load(path); err != nil {
		log.Printf("env file %s: %v (continuing, env may already be set)", path, err)
		return
	}
	log.Printf("settings from %s", path)
}
