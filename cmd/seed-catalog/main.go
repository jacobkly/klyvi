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
//	go run ./cmd/seed-catalog                  # default ~10k sweep
//	go run ./cmd/seed-catalog -max-movies 3000 # smaller run
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"

	"klyvi-api/config"
	"klyvi-api/internal/movies"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/tmdb"
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

func main() {
	popularPages := flag.Int("popular-pages", 15, "number of /movie/popular pages to walk")
	topRatedPages := flag.Int("top-rated-pages", 10, "number of /movie/top_rated pages to walk")
	discoverPages := flag.Int("discover-pages", 8, "pages per (genre × decade) discover bucket")
	minVotes := flag.Int("min-votes", 50, "discover vote_count.gte floor (keeps some quality without staying mainstream-only)")
	maxMovies := flag.Int("max-movies", 10000, "stop collecting ids once this many unique ids are gathered (0 = no cap)")
	timeout := flag.Duration("timeout", 3*time.Hour, "overall run timeout")
	backfill := flag.Bool("backfill-uncached", false, "instead of the sweep, cache every media_index movie that has no movies row (repairs imported 'Untitled' rows), then exit")
	refreshStats := flag.Bool("refresh-feature-stats", false, "instead of the sweep, recompute the feature_stats / feature_corpus tables (recommender IDF inputs) from the cached catalog, then exit")
	flag.Parse()

	if err := godotenv.Load(".env.dev"); err != nil {
		log.Printf("godotenv: %v (continuing — env may already be set)", err)
	}

	cfg := config.New()

	dbConn, err := db.New(cfg.DB)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer dbConn.Close()

	client := tmdb.NewClient(cfg.TMDB.APIKey)
	repo := movies.NewRepository(dbConn)
	svc := movies.NewService(client, repo)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	if *backfill {
		if err := backfillUncached(ctx, dbConn, svc); err != nil {
			log.Fatalf("backfill: %v", err)
		}
		os.Exit(0)
	}

	if *refreshStats {
		if err := refreshFeatureStats(ctx, dbConn, repo); err != nil {
			log.Fatalf("refresh feature stats: %v", err)
		}
		os.Exit(0)
	}

	ids, err := collectIDs(client, *popularPages, *topRatedPages, *discoverPages, *minVotes, *maxMovies)
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

// collectIDs returns a deduplicated, ordered list of TMDB movie ids drawn from
// popular, top_rated, and a genre × decade discover sweep. Collection stops
// early once maxMovies unique ids are gathered.
func collectIDs(client *tmdb.Client, popularPages, topRatedPages, discoverPages, minVotes, maxMovies int) ([]int, error) {
	seen := make(map[int]bool)
	var ids []int

	capped := func() bool { return maxMovies > 0 && len(ids) >= maxMovies }

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
			if seen[id] {
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
			raw, err := client.TMDBRequest("GET", build(p), nil)
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
		return fmt.Sprintf("/movie/popular?language=en-US&page=%d", p)
	}, popularPages); err != nil {
		return nil, fmt.Errorf("popular: %w", err)
	}
	if err := walk(func(p int) string {
		return fmt.Sprintf("/movie/top_rated?language=en-US&page=%d", p)
	}, topRatedPages); err != nil {
		return nil, fmt.Errorf("top_rated: %w", err)
	}

	for _, g := range discoverGenres {
		if capped() {
			break
		}
		for _, w := range decadeWindows {
			if capped() {
				break
			}
			genre, window := g, w
			if err := walk(func(p int) string {
				return discoverPath(genre, window, minVotes, p)
			}, discoverPages); err != nil {
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

// refreshFeatureStats recomputes the recommender's IDF inputs from the cached
// catalog and logs a summary so the operator can confirm the tables are
// populated. Run it after a seed or backfill — the recommender loads these at
// startup, so a following API restart picks up the new weights.
func refreshFeatureStats(ctx context.Context, dbConn *sqlx.DB, repo *movies.Repository) error {
	start := time.Now()
	if err := repo.RefreshFeatureStats(ctx); err != nil {
		return err
	}

	var summary struct {
		Corpus   int `db:"corpus"`
		Genres   int `db:"genres"`
		Keywords int `db:"keywords"`
	}
	err := dbConn.GetContext(ctx, &summary, `
		select
			coalesce((select doc_count from feature_corpus where media_type = 'movie'), 0) as corpus,
			(select count(*) from feature_stats where media_type = 'movie' and feature_type = 'genre')   as genres,
			(select count(*) from feature_stats where media_type = 'movie' and feature_type = 'keyword') as keywords
	`)
	if err != nil {
		return fmt.Errorf("summary: %w", err)
	}
	log.Printf("feature stats refreshed in %s: corpus=%d genres=%d keywords=%d",
		time.Since(start).Truncate(time.Millisecond), summary.Corpus, summary.Genres, summary.Keywords)
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
