// seed-catalog populates the local `movies` cache by forcing the existing
// movies.Service fetch+persist path over a broad set of TMDB movie ids, so
// the recommender has a large, varied candidate pool to work over (not just
// the few hundred most globally-popular films).
//
// It draws ids from three sources, deduped:
//   - /movie/popular      — the mainstream base
//   - /movie/top_rated    — the critically-strong base
//   - /discover/movie      — a sweep across genre × decade, sorted by
//     vote_count within each bucket, which is what
//     produces real spread (e.g. most-voted 1980s
//     horror, most-voted 2000s romance) instead of
//     more blockbusters.
//
// Every collected id then goes through movies.Service.GetMovieById, which is
// the same cache-then-fetch path the live API uses — so each row lands with
// keywords and credits via append_to_response. The run is idempotent: ids
// already cached are served from the DB and not re-fetched, so re-running to
// top up (or resuming after an interruption) is cheap.
//
// With -media tv the same sweep runs over /tv/popular, /tv/top_rated and
// /discover/tv by first air date, and every id goes through
// tv.Service.EnsureSeries: cached with keywords and credits plus its 'tv'
// media_index row, skipped when already cached.
//
//	go run ./cmd/seed-catalog                  # default ~10k sweep
//	go run ./cmd/seed-catalog -max-movies 3000 # smaller run
//	go run ./cmd/seed-catalog -media tv        # series sweep
//	go run ./cmd/seed-catalog -slim-credits    # trim credits cached before the trim
//	go run ./cmd/seed-catalog -refresh-older-than 150d -timeout 2h
//
// The last form is the nightly job. TMDB's terms cap its cache at six months,
// so it refetches every cached row older than the cutoff, oldest first, until
// the time budget runs out, and the next night carries on where it stopped.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jmoiron/sqlx"

	"klyvi-api/config"
	"klyvi-api/internal/movies"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/tmdb"
	"klyvi-api/internal/tv"
)

// discoverGenres are TMDB's movie genre ids. These ids are stable; the sweep
// walks each one across every decade window to spread the catalog across
// tastes rather than piling up on whatever is globally popular. (TV Movie is
// deliberately omitted — not a theatrical catalog.)
var discoverGenres = []int{
	28,    // Action
	12,    // Adventure
	16,    // Animation
	35,    // Comedy
	80,    // Crime
	99,    // Documentary
	18,    // Drama
	10751, // Family
	14,    // Fantasy
	36,    // History
	27,    // Horror
	10402, // Music
	9648,  // Mystery
	10749, // Romance
	878,   // Science Fiction
	53,    // Thriller
	10752, // War
	37,    // Western
}

// decadeWindow is an inclusive primary-release-date range for the discover
// sweep. Pre-1950 films are sparse and low-vote, so the oldest bucket is a
// wider 1950–1969 span.
type decadeWindow struct{ gte, lte string }

var decadeWindows = []decadeWindow{
	{"1950-01-01", "1969-12-31"},
	{"1970-01-01", "1979-12-31"},
	{"1980-01-01", "1989-12-31"},
	{"1990-01-01", "1999-12-31"},
	{"2000-01-01", "2009-12-31"},
	{"2010-01-01", "2019-12-31"},
	{"2020-01-01", "2025-12-31"},
}

// tvDiscoverGenres are TMDB's TV genre ids, which differ from the movie ids
// where TV merges genres (Action & Adventure, Sci-Fi & Fantasy, War &
// Politics). News, Soap and Talk are left out: daily formats, not what anyone
// puts on tonight.
var tvDiscoverGenres = []int{
	10759, // Action & Adventure
	16,    // Animation
	35,    // Comedy
	80,    // Crime
	99,    // Documentary
	18,    // Drama
	10751, // Family
	10762, // Kids
	9648,  // Mystery
	10764, // Reality
	10765, // Sci-Fi & Fantasy
	10768, // War & Politics
	37,    // Western
}

// tvDecadeWindows are first-air-date ranges. Voted TV skews recent, so the
// 2010s split in two and everything before 1980 shares one window.
var tvDecadeWindows = []decadeWindow{
	{"1950-01-01", "1979-12-31"},
	{"1980-01-01", "1989-12-31"},
	{"1990-01-01", "1999-12-31"},
	{"2000-01-01", "2009-12-31"},
	{"2010-01-01", "2014-12-31"},
	{"2015-01-01", "2019-12-31"},
	{"2020-01-01", "2026-12-31"},
}

// sweep names the TMDB lists a catalog seed draws ids from, and the discover
// vote floor used when -min-votes is not given.
type sweep struct {
	popular, topRated string // path formats taking a page number
	genres            []int
	windows           []decadeWindow
	discover          func(genre int, w decadeWindow, minVotes, page int) string
	minVotes          int
}

var movieSweep = sweep{
	popular:  "/movie/popular?language=en-US&page=%d",
	topRated: "/movie/top_rated?language=en-US&page=%d",
	genres:   discoverGenres,
	windows:  decadeWindows,
	discover: discoverPath,
	minVotes: 50,
}

var tvSweep = sweep{
	popular:  "/tv/popular?language=en-US&page=%d",
	topRated: "/tv/top_rated?language=en-US&page=%d",
	genres:   tvDiscoverGenres,
	windows:  tvDecadeWindows,
	discover: discoverTVPath,
	// A series with 20 votes is as established as a film with far more: TV
	// draws fewer votes per title.
	minVotes: 20,
}

// sweepLimits bounds a sweep: pages per list and per discover bucket, the
// discover vote floor (0 = the sweep's own), and a cap on unique ids (0 = no
// cap).
type sweepLimits struct {
	popularPages, topRatedPages, discoverPages int
	minVotes                                   int
	max                                        int
}

// tmdbRequester is the one TMDB call the sweep makes.
type tmdbRequester interface {
	TMDBRequest(ctx context.Context, method, endpoint string, body interface{}) (map[string]interface{}, error)
}

// envFile is registered at package level so tests can see its default.
var envFile = config.EnvFileFlag(flag.CommandLine)

func main() {
	popularPages := flag.Int("popular-pages", 15, "number of /movie/popular pages to walk")
	topRatedPages := flag.Int("top-rated-pages", 10, "number of /movie/top_rated pages to walk")
	discoverPages := flag.Int("discover-pages", 8, "pages per (genre × decade) discover bucket")
	minVotes := flag.Int("min-votes", 0, "discover vote_count.gte floor, keeping some quality without staying mainstream-only (0 = 50 for movies, 20 for series)")
	maxMovies := flag.Int("max-movies", 10000, "stop collecting ids once this many unique ids are gathered (0 = no cap)")
	media := flag.String("media", "movie", "catalog to sweep: movie or tv")
	maxSeries := flag.Int("max-series", 8000, "with -media tv, stop collecting ids once this many unique series are gathered (0 = no cap)")
	timeout := flag.Duration("timeout", 3*time.Hour, "overall run timeout")
	backfill := flag.Bool("backfill-uncached", false, "instead of the sweep, cache every media_index movie that has no movies row (repairs imported 'Untitled' rows), then exit")
	backfillSkinnyRows := flag.Bool("backfill-skinny", false, "instead of the sweep, refetch every cached movie that has no credits (rows warmed from search hits), then exit")
	refreshStats := flag.Bool("refresh-feature-stats", false, "instead of the sweep, recompute the feature_stats / feature_corpus tables (recommender IDF inputs) for movies and series from the cached catalog, then exit")
	slimCredits := flag.Bool("slim-credits", false, "instead of the sweep, trim the stored credits of every cached movie and series to what the detail pages read (rows cached before the trim), then exit")
	var refreshAge ageFlag
	flag.Var(&refreshAge, "refresh-older-than", "instead of the sweep, refetch every cached movie, collection, series and season last written longer ago than this (e.g. 150d, at most 180d under TMDB's cache limit), oldest first until -timeout, then exit")
	refreshMax := flag.Int("refresh-max", 0, "with -refresh-older-than, refetch at most this many rows (0 = no cap)")
	flag.Parse()

	config.LoadEnvFile(*envFile)

	cfg := config.New()

	dbConn, err := db.New(cfg.DB)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer dbConn.Close()

	client := tmdb.NewClient(cfg.TMDB.APIKey)
	repo := movies.NewRepository(dbConn)
	svc := movies.NewService(client, repo)
	tvRepo := tv.NewRepository(dbConn)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	if *backfill {
		if err := backfillUncached(ctx, dbConn, svc); err != nil {
			log.Fatalf("backfill: %v", err)
		}
		os.Exit(0)
	}

	if *backfillSkinnyRows {
		if err := backfillSkinny(ctx, dbConn, svc); err != nil {
			log.Fatalf("backfill skinny: %v", err)
		}
		os.Exit(0)
	}

	if *refreshStats {
		if err := refreshFeatureStats(ctx, dbConn, repo, tvRepo); err != nil {
			log.Fatalf("refresh feature stats: %v", err)
		}
		os.Exit(0)
	}

	if *slimCredits {
		if err := slimStoredCredits(ctx, dbConn, repo, tvRepo); err != nil {
			log.Fatalf("slim credits: %v", err)
		}
		os.Exit(0)
	}

	if refreshAge.set {
		if err := refreshCatalog(ctx, dbConn, client, repo, tvRepo, refreshAge.d, *refreshMax); err != nil {
			log.Fatalf("refresh: %v", err)
		}
		os.Exit(0)
	}

	limits := sweepLimits{
		popularPages:  *popularPages,
		topRatedPages: *topRatedPages,
		discoverPages: *discoverPages,
		minVotes:      *minVotes,
	}

	switch *media {
	case "movie":
	case "tv":
		limits.max = *maxSeries
		if err := seedSeries(ctx, client, tv.NewService(client, tvRepo), limits); err != nil {
			log.Fatalf("seed series: %v", err)
		}
		os.Exit(0)
	default:
		log.Fatalf("unknown -media %q (want movie or tv)", *media)
	}

	limits.max = *maxMovies
	ids, err := collectIDs(ctx, client, movieSweep, limits)
	if err != nil {
		log.Fatalf("collect ids: %v", err)
	}
	log.Printf("collected %d unique movie ids (popular+top_rated+discover)", len(ids))

	start := time.Now()
	var seeded, failed int
	for i, id := range ids {
		if _, err := svc.GetMovieById(ctx, id, "tmdb"); err != nil {
			log.Printf("[%d/%d] id=%d: %v", i+1, len(ids), id, err)
			failed++
			continue
		}
		// GetMovieById is cache-then-fetch — counting cache hits vs misses
		// without instrumenting the service is hard, so we just count
		// "processed successfully" and let the row-count delta speak.
		seeded++
		if (i+1)%100 == 0 {
			log.Printf("seeded %d/%d (%.0fs elapsed)", i+1, len(ids), time.Since(start).Seconds())
		}
	}

	log.Printf("done: %d processed, %d failed in %s", seeded, failed, time.Since(start))
	os.Exit(0)
}

// seedSeries sweeps TV ids and caches each series through EnsureSeries, which
// skips series already cached with keywords and credits, so a re-run tops up
// or resumes cheaply.
func seedSeries(ctx context.Context, client tmdbRequester, svc *tv.Service, limits sweepLimits) error {
	ids, err := collectIDs(ctx, client, tvSweep, limits)
	if err != nil {
		return fmt.Errorf("collect ids: %w", err)
	}
	log.Printf("collected %d unique series ids (popular+top_rated+discover)", len(ids))

	start := time.Now()
	var failed int
	for i, id := range ids {
		if err := svc.EnsureSeries(ctx, id); err != nil {
			log.Printf("[%d/%d] tv=%d: %v", i+1, len(ids), id, err)
			failed++
		}
		if (i+1)%100 == 0 {
			log.Printf("seeded %d/%d series (%.0fs elapsed)", i+1, len(ids), time.Since(start).Seconds())
		}
	}
	log.Printf("done: %d series processed, %d failed in %s", len(ids)-failed, failed, time.Since(start))
	return nil
}

// collectIDs returns a deduplicated, ordered list of TMDB ids drawn from a
// sweep's popular and top rated lists and its genre × decade discover
// buckets. Collection stops early once limits.max unique ids are gathered.
func collectIDs(ctx context.Context, client tmdbRequester, sw sweep, limits sweepLimits) ([]int, error) {
	seen := make(map[int]bool)
	var ids []int

	capped := func() bool { return limits.max > 0 && len(ids) >= limits.max }

	minVotes := limits.minVotes
	if minVotes == 0 {
		minVotes = sw.minVotes
	}

	add := func(raw map[string]interface{}) {
		results, ok := raw["results"].([]interface{})
		if !ok {
			return
		}
		for _, r := range results {
			item, ok := r.(map[string]interface{})
			if !ok {
				continue
			}
			idF, ok := item["id"].(float64)
			if !ok {
				continue
			}
			id := int(idF)
			if seen[id] || capped() {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}

	// walk pages 1..pages for a bucket, stopping at the cap or when TMDB
	// reports no further pages (narrow discover buckets are often 1-2 pages).
	walk := func(build func(page int) string, pages int) error {
		for p := 1; p <= pages; p++ {
			if capped() {
				return nil
			}
			raw, err := client.TMDBRequest(ctx, "GET", build(p), nil)
			if err != nil {
				return err
			}
			add(raw)
			if tp, ok := raw["total_pages"].(float64); ok && float64(p) >= tp {
				break
			}
		}
		return nil
	}

	if err := walk(func(p int) string {
		return fmt.Sprintf(sw.popular, p)
	}, limits.popularPages); err != nil {
		return nil, fmt.Errorf("popular: %w", err)
	}
	if err := walk(func(p int) string {
		return fmt.Sprintf(sw.topRated, p)
	}, limits.topRatedPages); err != nil {
		return nil, fmt.Errorf("top_rated: %w", err)
	}

	for _, g := range sw.genres {
		if capped() {
			break
		}
		for _, w := range sw.windows {
			if capped() {
				break
			}
			genre, window := g, w
			if err := walk(func(p int) string {
				return sw.discover(genre, window, minVotes, p)
			}, limits.discoverPages); err != nil {
				return nil, fmt.Errorf("discover genre=%d %s..%s: %w", genre, window.gte, window.lte, err)
			}
		}
	}

	return ids, nil
}

// backfillUncached caches every movie that has a media_index row but no
// movies-cache row — the rows the importer created before it started caching
// matched films, which render as "Untitled" and carry no reco features.
// Idempotent: GetMovieById is cache-then-fetch.
func backfillUncached(ctx context.Context, dbConn *sqlx.DB, svc *movies.Service) error {
	var ids []int
	err := dbConn.SelectContext(ctx, &ids, `
		select mi.id
		from media_index mi
		left join movies m on m.movie_id = mi.id
		where mi.media_type = 'movie' and m.movie_id is null
		order by mi.id
	`)
	if err != nil {
		return fmt.Errorf("query uncached: %w", err)
	}
	log.Printf("backfill: %d uncached movies to fetch", len(ids))

	start := time.Now()
	var done, failed int
	for i, id := range ids {
		if _, err := svc.GetMovieById(ctx, id, "tmdb"); err != nil {
			log.Printf("backfill [%d/%d] id=%d: %v", i+1, len(ids), id, err)
			failed++
			continue
		}
		done++
		if (i+1)%50 == 0 {
			log.Printf("backfill %d/%d (%.0fs elapsed)", i+1, len(ids), time.Since(start).Seconds())
		}
	}
	log.Printf("backfill done: %d cached, %d failed in %s", done, failed, time.Since(start))
	return nil
}

// backfillSkinny refetches every cached movie that has no credits. Those rows
// came from search hits, which carry neither keywords nor credits, so they are
// invisible to the recommender. GetMovieById treats them as misses and
// upgrades them in place. Idempotent.
func backfillSkinny(ctx context.Context, dbConn *sqlx.DB, svc *movies.Service) error {
	var ids []int
	if err := dbConn.SelectContext(ctx, &ids,
		`select movie_id from movies where credits is null order by movie_id`); err != nil {
		return fmt.Errorf("query skinny rows: %w", err)
	}
	log.Printf("backfill-skinny: %d movies without detail", len(ids))

	start := time.Now()
	var failed int
	for i, id := range ids {
		m, err := svc.GetMovieById(ctx, id, "tmdb")
		if err == nil && m.Credits == nil {
			err = fmt.Errorf("still no credits (TMDB unreachable or id gone)")
		}
		if err != nil {
			log.Printf("backfill-skinny [%d/%d] id=%d: %v", i+1, len(ids), id, err)
			failed++
		}
	}
	log.Printf("backfill-skinny done: %d upgraded, %d failed in %s",
		len(ids)-failed, failed, time.Since(start).Truncate(time.Millisecond))
	return nil
}

// slimPage is how many rows slimStoredCredits reads and rewrites at a time.
// Untrimmed credits average tens of KB, so a page is a few MB over the wire.
const slimPage = 200

// slimStoredCredits trims the credits of every cached movie and series, a
// page at a time. Credits are trimmed on write now, but rows cached before
// that carry TMDB's full cast and crew, and the upsert never replaces a row
// that already has credits. Idempotent: a trimmed row is left alone.
func slimStoredCredits(ctx context.Context, dbConn *sqlx.DB, movieRepo *movies.Repository, tvRepo *tv.Repository) error {
	tables := []struct {
		name   string
		idsSQL string
		slim   func(context.Context, []int) (int, error)
	}{
		{"movies", `select movie_id from movies where credits is not null order by movie_id`, movieRepo.SlimStoredCredits},
		{"tv_series", `select tv_id from tv_series where credits is not null order by tv_id`, tvRepo.SlimStoredCredits},
	}
	for _, t := range tables {
		var ids []int
		if err := dbConn.SelectContext(ctx, &ids, t.idsSQL); err != nil {
			return fmt.Errorf("list %s: %w", t.name, err)
		}
		log.Printf("slim-credits: %d %s rows with credits", len(ids), t.name)

		start := time.Now()
		var slimmed int
		for i := 0; i < len(ids); i += slimPage {
			page := ids[i:min(i+slimPage, len(ids))]
			n, err := t.slim(ctx, page)
			if err != nil {
				return fmt.Errorf("%s from id %d: %w", t.name, page[0], err)
			}
			slimmed += n
			log.Printf("slim-credits %s: %d/%d checked, %d trimmed (%.0fs elapsed)",
				t.name, i+len(page), len(ids), slimmed, time.Since(start).Seconds())
		}
		log.Printf("slim-credits %s done: %d of %d rows trimmed in %s",
			t.name, slimmed, len(ids), time.Since(start).Truncate(time.Millisecond))
	}
	return nil
}

// refreshFeatureStats recomputes the recommender's IDF inputs from the cached
// catalog, movies then series, and logs a summary per media type so the
// operator can confirm the tables are populated. Run it after a seed or
// backfill — the recommender loads these at startup, so a following API
// restart picks up the new weights.
func refreshFeatureStats(ctx context.Context, dbConn *sqlx.DB, movieRepo *movies.Repository, tvRepo *tv.Repository) error {
	partitions := []struct {
		mediaType string
		refresh   func(context.Context) error
	}{
		{"movie", movieRepo.RefreshFeatureStats},
		{"tv", tvRepo.RefreshFeatureStats},
	}
	for _, p := range partitions {
		start := time.Now()
		if err := p.refresh(ctx); err != nil {
			return fmt.Errorf("%s: %w", p.mediaType, err)
		}

		var summary struct {
			Corpus   int `db:"corpus"`
			Genres   int `db:"genres"`
			Keywords int `db:"keywords"`
		}
		err := dbConn.GetContext(ctx, &summary, `
			select
				coalesce((select doc_count from feature_corpus where media_type = $1), 0) as corpus,
				(select count(*) from feature_stats where media_type = $1 and feature_type = 'genre')   as genres,
				(select count(*) from feature_stats where media_type = $1 and feature_type = 'keyword') as keywords
		`, p.mediaType)
		if err != nil {
			return fmt.Errorf("%s summary: %w", p.mediaType, err)
		}
		log.Printf("%s feature stats refreshed in %s: corpus=%d genres=%d keywords=%d", p.mediaType,
			time.Since(start).Truncate(time.Millisecond), summary.Corpus, summary.Genres, summary.Keywords)
	}
	return nil
}

func discoverPath(genre int, w decadeWindow, minVotes, page int) string {
	return fmt.Sprintf(
		"/discover/movie?language=en-US&include_adult=false&sort_by=vote_count.desc"+
			"&vote_count.gte=%d&with_genres=%d"+
			"&primary_release_date.gte=%s&primary_release_date.lte=%s&page=%d",
		minVotes, genre, w.gte, w.lte, page,
	)
}

func discoverTVPath(genre int, w decadeWindow, minVotes, page int) string {
	return fmt.Sprintf(
		"/discover/tv?language=en-US&include_adult=false&include_null_first_air_dates=false"+
			"&sort_by=vote_count.desc&vote_count.gte=%d&with_genres=%d"+
			"&first_air_date.gte=%s&first_air_date.lte=%s&page=%d",
		minVotes, genre, w.gte, w.lte, page,
	)
}
