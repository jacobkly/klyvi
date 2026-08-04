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

	// AllowedOrigins is a comma-separated list of CORS origins (e.g.
	// "http://localhost:3000,https://klyvi.app"). Optional — defaults
	// to localhost dev origins when unset.
	AllowedOrigins string `env:"ALLOWED_ORIGINS"`

	// RateLimitRPM / RateLimitBurst tune the global per-IP rate limiter on the
	// /v1 surface. Optional; the defaults suit a public catalog where a single
	// page load fires several requests.
	RateLimitRPM   int `env:"SERVER_RATELIMIT_RPM,default=120"`
	RateLimitBurst int `env:"SERVER_RATELIMIT_BURST,default=40"`
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
//
// URL + ServiceRoleKey enable the two server-brokered Supabase features:
// avatar storage and auth-user deletion. Optional — when unset, those two
// endpoints return an explicit "not configured" error and everything else
// works. The service-role key is server-side only and must never reach a
// client bundle.
type ConfSupabase struct {
	JWKSURL  string `env:"SUPABASE_JWKS_URL,required"`
	Issuer   string `env:"SUPABASE_JWT_ISSUER"`
	Audience string `env:"SUPABASE_JWT_AUDIENCE"`

	URL            string `env:"SUPABASE_URL"`
	ServiceRoleKey string `env:"SUPABASE_SERVICE_ROLE_KEY"`
	StorageBucket  string `env:"SUPABASE_STORAGE_BUCKET,default=avatars"`
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
