package config

import (
	"log"
	"time"

	"github.com/joeshaw/envdecode"
)

type Conf struct {
	Server   ConfServer
	DB       ConfDB
	TMDB     ConfTMDB
	Supabase ConfSupabase
}

type ConfServer struct {
	Port         int           `env:"SERVER_PORT,required"`
	TimeoutRead  time.Duration `env:"SERVER_TIMEOUT_READ,required"`
	TimeoutWrite time.Duration `env:"SERVER_TIMEOUT_WRITE,required"`
	TimeoutIdle  time.Duration `env:"SERVER_TIMEOUT_IDLE,required"`
	Debug        bool          `env:"SERVER_DEBUG,required"`
}

type ConfDB struct {
	Host     string `env:"DB_HOST,required"`
	Port     int    `env:"DB_PORT,required"`
	Username string `env:"DB_USER,required"`
	Password string `env:"DB_PASS,required"`
	DBName   string `env:"DB_NAME,required"`
	SSLMode  string `env:"DB_SSLMODE,required"`
	Debug    bool   `env:"DB_DEBUG,required"`
}

type ConfTMDB struct {
	APIKey string `env:"TMDB_API_KEY,required"`
}

// ConfSupabase holds the verification-side knobs for Supabase JWTs. Issuer
// and Audience are optional; when unset, those claim checks are skipped.
type ConfSupabase struct {
	JWKSURL  string `env:"SUPABASE_JWKS_URL,required"`
	Issuer   string `env:"SUPABASE_JWT_ISSUER"`
	Audience string `env:"SUPABASE_JWT_AUDIENCE"`
}

func New() *Conf {
	var c Conf
	if err := envdecode.StrictDecode(&c); err != nil {
		log.Fatalf("Failed to decode: %s", err)
	}

	return &c
}

func NewDB() *ConfDB {
	var c ConfDB
	if err := envdecode.StrictDecode(&c); err != nil {
		log.Fatalf("Failed to decode: %s", err)
	}

	return &c
}

func NewTMDB() *ConfTMDB {
	var c ConfTMDB
	if err := envdecode.StrictDecode(&c); err != nil {
		log.Fatalf("Failed to decode: %s", err)
	}
	return &c
}

func NewSupabase() *ConfSupabase {
	var c ConfSupabase
	if err := envdecode.StrictDecode(&c); err != nil {
		log.Fatalf("Failed to decode: %s", err)
	}
	return &c
}
