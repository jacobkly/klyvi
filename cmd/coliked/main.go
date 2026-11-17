// coliked is the nightly co-liked job. It counts, from every user's ratings
// and logs, which titles the same users like together, and replaces the
// co_liked table the recommender's "people who liked this also liked" term
// reads:
//
//	go run ./cmd/coliked
//	go run ./cmd/coliked -min-users 3 -per-title 30
//
// It reads and writes only the database. Running it twice in a row changes
// nothing.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"

	"klyvi-api/config"
	"klyvi-api/internal/coliked"
	"klyvi-api/internal/platform/db"
)

func main() {
	def := coliked.DefaultOptions()
	timeout := flag.Duration("timeout", 30*time.Minute, "overall run timeout")
	likedRating := flag.Int("liked-rating", def.LikedRating, "lowest rating, 0 to 100, that counts as liking a title")
	userCap := flag.Int("user-cap", def.UserCap, "most recent liked titles counted per user")
	minUsers := flag.Int("min-users", def.MinUsers, "fewest users a pair needs to be kept")
	perTitle := flag.Int("per-title", def.PerTitle, "neighbours kept per title")
	flag.Parse()

	if err := godotenv.Load(".env.dev"); err != nil {
		log.Printf("godotenv: %v (continuing, env may already be set)", err)
	}
	cfg := config.New()
	dbConn, err := db.New(cfg.DB)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer func() { _ = dbConn.Close() }()

	opts := coliked.Options{
		LikedRating: *likedRating,
		UserCap:     *userCap,
		MinUsers:    *minUsers,
		Shrink:      cfg.Reco.CoLikedShrink,
		PerTitle:    *perTitle,
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	start := time.Now()
	n, err := coliked.NewRepository(dbConn).Rebuild(ctx, opts)
	if err != nil {
		log.Printf("coliked: %v", err)
		os.Exit(1)
	}
	log.Printf("coliked: stored %d pairs in %s", n, time.Since(start).Round(time.Millisecond))
}
