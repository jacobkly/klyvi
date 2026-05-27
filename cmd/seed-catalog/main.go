// seed-catalog walks TMDB's popular + top_rated movie lists and forces the
// existing movies.Service cache+persist path to populate the local `movies`
// table with detail rows (including keywords and credits via
// append_to_response). Run once after a fresh deploy so Tier 0 of the
// recommender has content to recommend over.
//
//	go run ./cmd/seed-catalog
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"

	"klyvi-api/config"
	"klyvi-api/internal/movies"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/tmdb"
)

func main() {
	popularPages := flag.Int("popular-pages", 15, "number of /movie/popular pages to walk")
	topRatedPages := flag.Int("top-rated-pages", 10, "number of /movie/top_rated pages to walk")
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	ids, err := collectIDs(ctx, client, *popularPages, *topRatedPages)
	if err != nil {
		log.Fatalf("collect ids: %v", err)
	}
	log.Printf("collected %d unique movie ids from popular/top_rated", len(ids))

	start := time.Now()
	var seeded, skipped, failed int
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
		if (i+1)%50 == 0 {
			log.Printf("seeded %d/%d (%.0fs elapsed)", i+1, len(ids), time.Since(start).Seconds())
		}
	}
	_ = skipped

	log.Printf("done: %d processed, %d failed in %s", seeded, failed, time.Since(start))
	os.Exit(0)
}

// collectIDs returns a deduplicated, ordered list of TMDB movie ids drawn
// from /movie/popular and /movie/top_rated. Pages 1..popularPages and
// 1..topRatedPages respectively.
func collectIDs(ctx context.Context, client *tmdb.Client, popularPages, topRatedPages int) ([]int, error) {
	seen := make(map[int]bool)
	var ids []int

	walk := func(endpoint string, pages int) error {
		for p := 1; p <= pages; p++ {
			raw, err := client.TMDBRequest("GET",
				fmt.Sprintf("%s?language=en-US&page=%d", endpoint, p), nil)
			if err != nil {
				return fmt.Errorf("%s page %d: %w", endpoint, p, err)
			}
			results, ok := raw["results"].([]interface{})
			if !ok {
				return fmt.Errorf("%s page %d: missing results array", endpoint, p)
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
		return nil
	}

	if err := walk("/movie/popular", popularPages); err != nil {
		return nil, err
	}
	if err := walk("/movie/top_rated", topRatedPages); err != nil {
		return nil, err
	}
	return ids, nil
}
