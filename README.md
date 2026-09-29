# Klyvi API

Klyvi is a personalized movie and TV discovery app that learns your taste and answers one question well: **what should I watch tonight?**

It rests on two pillars that feed each other:

- **Tracking.** Log, rate, and watchlist what you watch. TV is tracked per season, so a strong season 1 and a weak season 4 keep their own scores.
- **Recommendation.** A tiered recommender that weights the TMDB keywords a title shares with what you already like, not just its genre, and explains every pick with reason chips ("slow burn", "class differences").

Every tracking action is also a recommendation signal. Klyvi is free, with no paid tier and no ads.

This repository (`klyvi`) is the backend REST API, built with Go. It is the only place business logic lives. The web client is a separate repo: [klyvi-web](https://github.com/jacobkly/klyvi-web).

## Why Separate Repos?

- The API and the web app deploy on their own schedules, to different hosts
- Each side keeps a simpler build, test, and CI setup
- The web app stays a thin client, so no scoring logic leaks into it

## Tech Stack

- **Backend:** [Go](https://go.dev/) 1.24 REST API on [chi](https://github.com/go-chi/chi), sqlx over pgx
- **Database:** [Supabase](https://supabase.com/) (PostgreSQL)
- **Authentication:** Supabase Auth, verified against the project JWKS (asymmetric keys only)
- **Storage:** Supabase Storage for avatars
- **3rd-Party API:** [TMDB](https://www.themoviedb.org/) (The Movie Database) for the catalog
- **Migrations:** [Goose](https://github.com/pressly/goose)
- **Caching:** in-process TTL and LRU caches, plus `Cache-Control` headers on public reads

## Hosting

- The API runs as a Docker container on a home server, reachable only through a Cloudflare tunnel
- GitHub Actions builds the image on every push to `main` and publishes it to GHCR. Watchtower on the server pulls the new image
- The web client is hosted on Vercel
- A daily GitHub Actions job keeps the free Supabase project from pausing

The deploy files live in `deploy/`: the API service, plus a shared edge stack with the tunnel and the auto-updater.

## What the API Does

| Area | Endpoints | Auth |
|---|---|---|
| Catalog | `/v1/movies`, `/v1/tv`: lists, detail (with seasons), collections | Public |
| Similar titles | `/v1/movies/{id}/similar`, `/v1/tv/{id}/similar` | Public |
| Search | `/v1/search` | Public |
| Rankings | `/v1/rankings/movies`, `/v1/rankings/tv` (Top 100) | Public |
| Onboarding | `/v1/onboarding/pool`, `/v1/users/username-available` | Public |
| Account | `/v1/users/me`: profile, avatar, stats, exports, deletion | Signed in |
| Tracking | `/v1/tracking`: status, score, progress, notes, favorites | Signed in |
| Signals | `/v1/interactions` (single and batch) | Signed in |
| Recommendations | `/v1/reco/feed` | Signed in |
| Imports | `/v1/imports` (Letterboxd, Trakt, or CSV) | Signed in |

Every success response is wrapped as `{ version, stats, data }`, and errors are `{ error }`. Every `/v1` route is rate-limited per client IP, with tighter limits on search and the username check. `/health` is left unthrottled for uptime checks.

## How Recommendations Work

- **Tier 0: cold start.** With no ratings yet, a Bayesian quality score picks well-liked titles.
- **Tier 1: content matching.** Your first ratings build taste weights over TMDB keywords, genres, and era. Candidates are ranked by overlap, with keywords weighted well above genre. Titles you rated low push similar ones down.
- **Tier 2: taste profile.** Past a set number of ratings, the feed scores against a per-user taste profile, which is saved in the background after each request.
- **Diversity.** MMR reranking keeps the feed from filling with one franchise, and anything you've already seen is filtered out.
- **Reasons.** Every pick carries the keywords and genres that matched, which the client shows as reason chips.

"More like this" is a separate, non-personal ranking. It scores titles by the keywords they share with the one you're looking at, weighting rare keywords above common ones, and fills a thin shelf from TMDB's own list.

There are no machine-learned models or embeddings. TMDB's terms don't allow them on its data, so matching stays explainable overlap.

The feed is cached per user for 10 minutes and cleared whenever you track, rate, or import something.

## Milestones

- ✅ TMDB client layer (retry, rate limiting, request context)
- ✅ Database design (movies, TV series and seasons, collections, tracking, interactions, taste profiles, onboarding pool)
- ✅ Catalog ingestion: movie and TV data fetched, normalized, and cached, including keywords and credits
- ✅ Supabase JWT auth middleware (JWKS, asymmetric), with the user created on first sign-in
- ✅ Tracking: log, rate, watchlist, and history per user (movies and TV seasons)
- ✅ Interactions log: per-event signal capture for the recommender
- ✅ Recommender: Tier 0, Tier 1, and Tier 2, MMR diversification, already-seen filtering, reason chips
- ✅ Onboarding: curated cold-start pool, plus Letterboxd, Trakt, and CSV imports
- ✅ Leave-one-out evaluation harness
- ✅ Profile stats, Top 100 rankings, avatars, data export, and account deletion
- ✅ Performance pass: fewer queries per request, response caches, gzip, cacheable public reads
- ✅ TV catalog: series identity, a TV seeder, and TV candidate retrieval
- ✅ Catalog refresh that keeps cached TMDB data under its six-month limit
- ✅ "More like this" for movies and TV
- ✅ Deployment (home server Docker behind a Cloudflare tunnel, image built by CI)
- ⬜ TV in the personalized feed (season-to-series rollup)

## Getting Started

**Prerequisites:** Go 1.24+, a Supabase project (Postgres, plus Auth on asymmetric JWT keys), and a TMDB API key. Docker is needed only for the local database and for building the image.

Create a `.env.dev` file (see `.env.example` for the full template):

```bash
SERVER_PORT=8080
SERVER_TIMEOUT_READ=3s
SERVER_TIMEOUT_WRITE=5s
SERVER_TIMEOUT_IDLE=5s
SERVER_DEBUG=true

# Optional. Comma-separated CORS origins for the frontend.
# Defaults to common localhost dev ports when unset.
# ALLOWED_ORIGINS=http://localhost:3000

# Optional. Per-IP rate limit for the public API (defaults shown).
# SERVER_RATELIMIT_RPM=120
# SERVER_RATELIMIT_BURST=40

DB_HOST=...           # Supabase pooler host
DB_PORT=5432
DB_USER=...
DB_PASS=...
DB_NAME=postgres
DB_SSLMODE=require
DB_DEBUG=true

TMDB_API_KEY=...
TMDB_BASE_URL=https://api.themoviedb.org/3

# Supabase JWT verification (asymmetric only, HS256 is not supported).
# https://<project-ref>.supabase.co/auth/v1/.well-known/jwks.json
SUPABASE_JWKS_URL=...
# Optional strict claim checks. Leave unset to skip.
# SUPABASE_JWT_ISSUER=https://<project-ref>.supabase.co/auth/v1
# SUPABASE_JWT_AUDIENCE=authenticated

# Optional. Enables avatar uploads and auth-user deletion. Server-side only,
# this key bypasses RLS.
# SUPABASE_URL=https://<project-ref>.supabase.co
# SUPABASE_SERVICE_ROLE_KEY=...
# SUPABASE_STORAGE_BUCKET=...
```

The recommender weights (`RECO_*`) are optional too. Their defaults are the values tuned with the harness, and `.env.example` documents each one.

Apply the schema, seed the catalog, and start the API:

```bash
# Apply schema
go run ./cmd/migrate up

# Populate the movie cache so the recommender has content
go run ./cmd/seed-catalog

# Populate the TV cache, then recompute the keyword stats the rankings use
go run ./cmd/seed-catalog -media tv
go run ./cmd/seed-catalog -refresh-feature-stats

# Seed the curated onboarding swipe deck
go run ./cmd/seed-onboarding

# Start the API (it reads only the environment, so export the file first)
set -a && . ./.env.dev && set +a
go run ./cmd/api
```

The migrate, seed, and harness commands load `.env.dev` on their own. Variables already set in the environment win.

Or build and run the same image CI builds (it contains only the API, so run migrations first):

```bash
docker build -t klyvi-api .
docker run --env-file .env.dev -p 8080:8080 klyvi-api
```

### Keeping the catalog fresh

TMDB allows cached data to be kept for up to six months. Run the refresh nightly and it refetches the oldest rows first until its timeout:

```bash
go run ./cmd/seed-catalog -refresh-older-than 150d -timeout 2h
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

# Fast loop, skips DB- and TMDB-touching tests
go test -short ./...
```

Evaluating the recommender (after a user has rated ~10+ items):

```bash
go run ./cmd/reco-harness                       # real users in the DB
go run ./cmd/reco-harness -synthetic-users 5    # synthetic, plumbing-only
```

## Attribution

This product uses the TMDB API but is not endorsed or certified by TMDB.

## License

[MIT](LICENSE)
