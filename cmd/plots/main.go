// plots is the weekly plot-vector job. For every catalog title with an English
// Wikipedia article (from the opendata job's Wikidata facts), it embeds the
// article's plot section with a small local model and stores the vector:
//
//	go run ./cmd/plots -model ./models/bge-small-en-v1.5
//	go run ./cmd/plots -coverage
//
// Wikipedia text is CC BY-SA. TMDB text never reaches the model. A run asks
// Wikipedia for the current revision of 50 articles a second and fetches and
// embeds only the articles that changed, so a rerun is quick and cheap. The
// first run embeds the whole catalog and takes about an hour.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"time"

	"klyvi-api/config"
	"klyvi-api/internal/embed"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/plots"
)

// memoryLimit keeps the job inside the API service's 512 MB container limit,
// which the job inherits. Without it the model's working memory grows past
// the limit and the container thrashes. GOMEMLIMIT overrides it.
const memoryLimit = 350 << 20

// envFile is registered at package level so tests can see its default.
var envFile = config.EnvFileFlag(flag.CommandLine)

func main() {
	timeout := flag.Duration("timeout", 3*time.Hour, "overall run timeout")
	media := flag.String("media", "all", "which titles to refresh: all, movie or tv")
	modelDir := flag.String("model", "/model", "directory holding the embedding model")
	pause := flag.Duration("pause", time.Second, "wait between Wikipedia requests")
	userAgent := flag.String("user-agent", "opendata-plots-sync/1.0", "user agent sent to Wikipedia")
	coverageOnly := flag.Bool("coverage", false, "only print the coverage report")
	flag.Parse()

	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(memoryLimit)
	}

	mediaTypes := plots.MediaTypes
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
	repo := plots.NewRepository(dbConn)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	failed := false
	if !*coverageOnly {
		failed = !run(ctx, repo, mediaTypes, *modelDir, *userAgent, *pause)
	}

	cov, err := repo.Coverage(ctx)
	if err != nil {
		log.Printf("plots: %v", err)
		os.Exit(1)
	}
	size, err := repo.TableBytes(ctx)
	if err != nil {
		log.Printf("plots: %v", err)
		os.Exit(1)
	}
	printCoverage(cov, size)
	if failed {
		os.Exit(1)
	}
}

// run embeds what changed and reports whether every batch succeeded.
func run(ctx context.Context, repo *plots.Repository, mediaTypes []string, modelDir, userAgent string, pause time.Duration) bool {
	start := time.Now()
	model, err := embed.Load(ctx, modelDir)
	if err != nil {
		log.Printf("plots: %v", err)
		return false
	}
	defer func() { _ = model.Close() }()
	log.Printf("plots: model %s loaded in %s", embed.ModelID, time.Since(start).Round(time.Millisecond))

	job := plots.NewJob(repo, plots.NewWikipedia(plots.WikipediaEndpoint, userAgent), model, embed.ModelID, pause)
	res, err := job.Run(ctx, mediaTypes)
	log.Printf("plots: %d articles, %d unchanged, %d embedded, %d without a plot, %d removed, %d batches failed, in %s",
		res.Articles, res.Unchanged, res.Embedded, res.NoPlot, res.Removed, res.Failed, time.Since(start).Round(time.Second))
	if err != nil {
		log.Printf("plots: %v", err)
		return false
	}
	return true
}

func printCoverage(cov []plots.Coverage, tableBytes int64) {
	pct := func(n, of int) string {
		if of == 0 {
			return "-"
		}
		return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(of))
	}
	fmt.Println("type   titles  article  vector  median words")
	for _, c := range cov {
		fmt.Printf("%-6s %6d  %-7s  %-6s  %d\n", c.MediaType, c.Titles,
			pct(c.WithArticle, c.Titles), pct(c.WithVector, c.Titles), c.MedianWords)
	}
	fmt.Printf("table size %.1f MB\n", float64(tableBytes)/(1<<20))
}
