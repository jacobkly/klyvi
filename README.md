# Klyvi API

Klyvi is a personalized movie and TV discovery app that learns your taste and helps you find what to watch next. Track what you've watched, plan to watch, or are currently watching, and the recommendations build from there.

This repository (`klyvi`) contains the backend REST API, built with Go. The frontend lives in a separate repo: [klyvi-web](https://github/jacobkly/klyvi).

## Why Separate Repos?

- Easier deployment (separate API and web hosting)
- Simpler CI/CD and maintenance
- Scalable for future features and contributors

## Tech Stack

- **Backend:** [Go](https://go.dev/) REST API
- **Database:** [Supabase](https://supabase.com/) (PostgreSQL)
- **Authentication:** Supabase Auth
- **Storage:** Supabase Buckets (for user-uploaded content)
- **3rd-Party API:** [TMDB](https://www.themoviedb.org/) (The Movie Database)
- **Migrations:** [Goose](https://github.com/pressly/goose) for database schema management

## Hosting

- Single VM deployment, API and frontend behind an Nginx proxy

## Project Goal

Make it easy to find what to watch next, with recommendations that reflect a user's actual taste across all movies and TV rather than a single streaming catalog.

## Milestones

- ✅ Initial TMDB client layer (with retry + rate limiting)
- ✅ Initial database design (movies, TV series/seasons, collections, tracking, interactions, taste profiles, onboarding pool)
- ✅ Catalog ingestion: full movie and TV data fetched, normalized, and cached (incl. keywords + credits)
- ✅ Supabase JWT auth middleware (JWKS, asymmetric); auto-create-on-first-auth
- ✅ Tracking: log, rate, watchlist, and history per user (movies + TV seasons)
- ✅ Interactions log: per-event signal capture for the recommender
- ✅ Recommendation engine — Tier 0 (Bayesian quality), Tier 1 (keyword-weighted content), Tier 2 (persisted taste profile), MMR diversification, already-seen filtering
- ✅ Onboarding curated pool endpoint for cold-start swipe deck
- ✅ Leave-one-out evaluation harness
- ✅ Deployment (homelab Docker behind a Cloudflare tunnel, image built by CI)
- ⬜ TV in the recommender (season→series rollup)
- ⬜ Tier 3 embeddings (pgvector) and explanations, free for everyone

## Getting Started

**Prerequisites:** Go 1.24+, Docker, a Supabase project (Postgres + Auth on asymmetric JWT keys), a TMDB API key.

Create a `.env.dev` file (see `.env.example` for the full template):

```bash
SERVER_PORT=8080
SERVER_TIMEOUT_READ=3s
SERVER_TIMEOUT_WRITE=5s
SERVER_TIMEOUT_IDLE=5s
SERVER_DEBUG=true

# Optional — comma-separated CORS origins for the frontend.
# Defaults to common localhost dev ports when unset.
# ALLOWED_ORIGINS=http://localhost:3000

DB_HOST=...           # Supabase pooler host
DB_PORT=5432
DB_USER=...
DB_PASS=...
DB_NAME=postgres
DB_SSLMODE=require
DB_DEBUG=true

TMDB_API_KEY=...
TMDB_BASE_URL=https://api.themoviedb.org/3

# Supabase JWT verification (asymmetric only — HS256 not supported).
# https://<project-ref>.supabase.co/auth/v1/.well-known/jwks.json
SUPABASE_JWKS_URL=...
# Optional strict claim checks — leave unset to skip.
# SUPABASE_JWT_ISSUER=https://<project-ref>.supabase.co/auth/v1
# SUPABASE_JWT_AUDIENCE=authenticated
```

Run with Docker (applies migrations on boot, then starts the API on `:8080`):

```bash
docker compose up --build
```

Or run locally:

```bash
# Apply schema
go run ./cmd/migrate up

# One-time: populate the movies cache so Tier 0 of the recommender has content
go run ./cmd/seed-catalog

# One-time: seed the curated onboarding swipe deck (~30 films)
go run ./cmd/seed-onboarding

# Start the API
go run ./cmd/api
```

### Local database (rehearsing migrations)

A disposable local Supabase stack (Postgres 17 with pgvector and the Supabase
roles) is configured in `supabase/config.toml`. goose owns the schema, so the
CLI's own migrations and seeding are off. Ports sit in the 544xx range so the
stack can run beside other local Supabase projects.

Create a `.env.local` with the CLI's fixed local credentials:

```bash
DB_HOST=127.0.0.1
DB_PORT=54422
DB_USER=postgres
DB_PASS=postgres
DB_NAME=postgres
DB_SSLMODE=disable
DB_DEBUG=true
```

Then rehearse every new migration here (up, down, up) before running it
against the hosted project:

```bash
npx supabase start                             # needs Docker Desktop
go run ./cmd/migrate -env .env.local up
go run ./cmd/migrate -env .env.local down      # prove the down works
go run ./cmd/migrate -env .env.local up
npx supabase stop                              # or `stop --no-backup` to wipe it
```

`cmd/migrate` defaults to `.env.dev` (the hosted project). Variables already
set in the environment win over the file.

Health check:

```bash
curl http://localhost:8080/health
```

Tests:

```bash
# Sequential to dodge Supabase's pooler connection limit when run in parallel
go test -p 1 ./...

# Fast loop — skips DB- and TMDB-touching tests
go test -short ./...
```

Evaluating the recommender (after a user has rated ~10+ items):

```bash
go run ./cmd/reco-harness                       # real users in the DB
go run ./cmd/reco-harness -synthetic-users 5    # synthetic, plumbing-only
```

Stay tuned for updates!