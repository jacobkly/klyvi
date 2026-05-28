// seed-onboarding populates the onboarding_pool table with the curated
// starter set defined in `docs/onboarding-spec.md` §4. Each film is
// resolved to its TMDB id via the TMDB search endpoint, then upserted
// keyed by tmdb_id — re-running the binary is idempotent and safe.
//
// The seed does NOT pre-warm the movies cache. The first call to
// `GET /v1/onboarding/pool` will fetch any missing film through the
// standard movies service (cache-then-TMDB), populating the cache as a
// side effect.
//
//	go run ./cmd/seed-onboarding
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"time"

	"github.com/joho/godotenv"

	"klyvi-api/config"
	"klyvi-api/internal/onboarding"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/tmdb"
)

// seedEntry is the curator's input — title and year are searched against
// TMDB to resolve a tmdb_id. dimension is the taste-axis the film tests
// for (per spec §4 "Selection criteria").
type seedEntry struct {
	Title     string
	Year      int
	Dimension string
}

// pool is the spec §4 starter set. Order here becomes display_order
// (position * 10). The pool is grouped by dimension following the
// spec's organization.
var pool = []seedEntry{
	// Polarizing arthouse / slow cinema
	{"The Tree of Life", 2011, "arthouse"},
	{"The Lighthouse", 2019, "arthouse"},
	{"2001: A Space Odyssey", 1968, "arthouse"},
	{"Uncut Gems", 2019, "arthouse"},

	// Auteur signatures
	{"Pulp Fiction", 1994, "auteur"},
	{"The Grand Budapest Hotel", 2014, "auteur"},
	{"Everything Everywhere All at Once", 2022, "auteur"},
	{"Drive", 2011, "auteur"},

	// Modern crowd-pleasers (still polarizing on tone)
	{"La La Land", 2016, "modern_crowdpleaser"},
	{"Knives Out", 2019, "modern_crowdpleaser"},
	{"Top Gun: Maverick", 2022, "modern_crowdpleaser"},
	{"Spider-Man: Into the Spider-Verse", 2018, "modern_crowdpleaser"},

	// Intense drama
	{"Whiplash", 2014, "intense_drama"},
	{"Marriage Story", 2019, "intense_drama"},
	{"The Social Network", 2010, "intense_drama"},
	{"Past Lives", 2023, "intense_drama"},

	// Genre anchors (test if they like the genre at all)
	{"Hereditary", 2018, "genre_horror"},
	{"John Wick", 2014, "genre_action"},
	{"Get Out", 2017, "genre_horror"},
	{"Mad Max: Fury Road", 2015, "genre_action"},

	// International / non-Hollywood
	{"Parasite", 2019, "international"},
	{"Anatomy of a Fall", 2023, "international"},
	{"Oldboy", 2003, "international"},

	// Coming of age / character study
	{"Lady Bird", 2017, "coming_of_age"},
	{"Moonlight", 2016, "coming_of_age"},
	{"Eternal Sunshine of the Spotless Mind", 2004, "coming_of_age"},

	// Classic / era test
	{"Goodfellas", 1990, "classic"},
	{"Chinatown", 1974, "classic"},
	{"Casablanca", 1942, "classic"},

	// Popcorn baseline (for the cynics)
	{"The Avengers", 2012, "popcorn"},
	{"Jurassic Park", 1993, "popcorn"},
}

func main() {
	dryRun := flag.Bool("dry-run", false, "resolve TMDB ids and print, do not write to DB")
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
	repo := onboarding.NewRepository(dbConn)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var ok, missing int
	for i, e := range pool {
		tmdbID, err := resolveTMDBID(ctx, client, e.Title, e.Year)
		if err != nil {
			log.Printf("[%d/%d] %q (%d): could not resolve — %v", i+1, len(pool), e.Title, e.Year, err)
			missing++
			continue
		}

		log.Printf("[%d/%d] %q (%d) → tmdb_id=%d  dimension=%s", i+1, len(pool), e.Title, e.Year, tmdbID, e.Dimension)
		ok++

		if *dryRun {
			continue
		}
		entry := onboarding.PoolEntry{
			TMDBID:       tmdbID,
			Dimension:    e.Dimension,
			DisplayOrder: (i + 1) * 10,
			Active:       true,
		}
		if err := repo.Upsert(ctx, entry); err != nil {
			log.Fatalf("upsert tmdb_id=%d: %v", tmdbID, err)
		}
	}

	log.Printf("done: %d resolved, %d missing (out of %d)", ok, missing, len(pool))
}

// resolveTMDBID searches TMDB for the given (title, year) and returns the
// first result's id. Year disambiguates common titles ("Drive", etc.).
func resolveTMDBID(ctx context.Context, client *tmdb.Client, title string, year int) (int64, error) {
	endpoint := fmt.Sprintf("/search/movie?query=%s&year=%d&language=en-US",
		url.QueryEscape(title), year)
	raw, err := client.TMDBRequest("GET", endpoint, nil)
	if err != nil {
		return 0, err
	}

	results, ok := raw["results"].([]interface{})
	if !ok || len(results) == 0 {
		return 0, fmt.Errorf("no results")
	}

	first, ok := results[0].(map[string]interface{})
	if !ok {
		return 0, fmt.Errorf("first result not an object")
	}
	idF, ok := first["id"].(float64)
	if !ok {
		return 0, fmt.Errorf("first result has no id")
	}
	_ = ctx // ctx reserved for future cancellation; client doesn't accept it yet
	return int64(idF), nil
}
