// notify is the nightly notices job. It refetches every series someone tracks
// and every planned film near its release, creates the notices that are due, and
// clears out old ones. Run it right after the catalog refresh:
//
//	go run ./cmd/notify
//	go run ./cmd/notify -timeout 15m
//
// TMDB calls are bounded by what people track, so a run stays in the hundreds.
// Running it twice creates nothing new.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"klyvi-api/config"
	"klyvi-api/internal/movies"
	"klyvi-api/internal/notifications"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/platform/tmdb"
	"klyvi-api/internal/tv"
)

// envFile is registered at package level so tests can see its default.
var envFile = config.EnvFileFlag(flag.CommandLine)

func main() {
	timeout := flag.Duration("timeout", 30*time.Minute, "overall run timeout")
	flag.Parse()

	config.LoadEnvFile(*envFile)
	cfg := config.New()

	dbConn, err := db.New(cfg.DB)
	if err != nil {
		log.Fatalf("db: %v", err)
	}

	client := tmdb.NewBackgroundClient(tmdb.NewClient(cfg.TMDB.APIKey))
	job := notifications.NewJob(
		notifications.NewRepository(dbConn),
		tv.NewService(client, tv.NewRepository(dbConn)),
		movies.NewService(client, movies.NewRepository(dbConn)),
	)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	res, runErr := job.Run(ctx)
	cancel()
	_ = dbConn.Close()

	log.Printf("notify: checked %d series and %d films, created %d notices, deleted %d, %d fetches failed",
		res.SeriesChecked, res.FilmsChecked, res.Created, res.Deleted, res.Failed)
	if runErr != nil {
		log.Printf("notify: %v", runErr)
		os.Exit(1)
	}
}
