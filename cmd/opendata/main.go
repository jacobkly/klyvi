// opendata is the weekly open-data job. It matches every catalog title to its
// Wikidata item by TMDB id, stores the facts the recommender may use, and
// prints how much of the catalog Wikidata covers:
//
//	go run ./cmd/opendata
//	go run ./cmd/opendata -media movie -timeout 30m
//
// Wikidata is CC0. A run is about 160 queries to its public query service, one
// a second. Running it twice in a row changes nothing.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"klyvi-api/config"
	"klyvi-api/internal/opendata"
	"klyvi-api/internal/platform/db"
)

// envFile is registered at package level so tests can see its default.
var envFile = config.EnvFileFlag(flag.CommandLine)

func main() {
	timeout := flag.Duration("timeout", time.Hour, "overall run timeout")
	media := flag.String("media", "all", "which titles to refresh: all, movie or tv")
	batch := flag.Int("batch", 200, "TMDB ids per Wikidata query")
	pause := flag.Duration("pause", time.Second, "wait between Wikidata queries")
	userAgent := flag.String("user-agent", "opendata-facts-sync/1.0", "user agent sent to Wikidata")
	coverageOnly := flag.Bool("coverage", false, "only print the coverage report")
	flag.Parse()

	mediaTypes := opendata.MediaTypes
	switch *media {
	case "all":
	case "movie", "tv":
		mediaTypes = []string{*media}
	default:
		log.Fatalf("-media must be all, movie or tv, not %q", *media)
	}

	config.LoadEnvFile(*envFile)
	cfg := config.New()
	dbConn, err := db.New(cfg.DB)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer func() { _ = dbConn.Close() }()
	repo := opendata.NewRepository(dbConn)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	failed := false
	if !*coverageOnly {
		job := opendata.NewJob(repo, opendata.NewWikidata(opendata.WikidataEndpoint, *userAgent), *batch, *pause)
		start := time.Now()
		res, err := job.Run(ctx, mediaTypes)
		log.Printf("opendata: checked %d titles, matched %d, changed %d rows, %d batches failed, in %s",
			res.Checked, res.Matched, res.Changed, res.Failed, time.Since(start).Round(time.Second))
		if err != nil {
			log.Printf("opendata: %v", err)
			failed = true
		}
	}

	cov, err := repo.Coverage(ctx)
	if err != nil {
		log.Printf("opendata: %v", err)
		os.Exit(1)
	}
	printCoverage(cov)
	if failed {
		os.Exit(1)
	}
}

func printCoverage(cov []opendata.Coverage) {
	pct := func(n, of int) string {
		if of == 0 {
			return "-"
		}
		return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(of))
	}
	fmt.Println("type   titles  matched  cast    subject  location  director  wikipedia")
	for _, c := range cov {
		fmt.Printf("%-6s %6d  %-7s  %-6s  %-7s  %-8s  %-8s  %s\n", c.MediaType, c.Titles,
			pct(c.Matched, c.Titles), pct(c.WithCast, c.Titles), pct(c.WithSubject, c.Titles),
			pct(c.WithLocation, c.Titles), pct(c.WithDirector, c.Titles), pct(c.WithWikipedia, c.Titles))
	}
}
