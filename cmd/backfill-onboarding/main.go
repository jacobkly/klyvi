// backfill-onboarding files past onboarding answers in the library, the way
// new answers are filed since U.7: each title an account said it has seen
// becomes a Completed entry with no score, unless it is already tracked. The
// like or dislike stays a feed signal only.
//
// It is a one-off bulk write. Count first, then write:
//
//	go run ./cmd/backfill-onboarding -env .env.dev -dry-run
//	go run ./cmd/backfill-onboarding -env .env.dev
//
// -user narrows either run to one account. Running it twice writes nothing new.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"github.com/google/uuid"

	"klyvi-api/config"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/tracking"
)

// envFile is registered at package level so tests can see its default.
var envFile = config.EnvFileFlag(flag.CommandLine)

func main() {
	dryRun := flag.Bool("dry-run", false, "count the entries it would create, and write nothing")
	user := flag.String("user", "", "only this account's answers (a user id)")
	timeout := flag.Duration("timeout", 10*time.Minute, "overall run timeout")
	flag.Parse()

	var userID *uuid.UUID
	if *user != "" {
		id, err := uuid.Parse(*user)
		if err != nil {
			log.Fatalf("backfill-onboarding: -user: %v", err)
		}
		userID = &id
	}

	config.LoadEnvFile(*envFile)
	dbConn, err := db.New(*config.NewDB())
	if err != nil {
		log.Fatalf("db: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	res, err := tracking.NewRepository(dbConn).BackfillOnboardingSeen(ctx, userID, *dryRun)
	cancel()
	_ = dbConn.Close()
	if err != nil {
		log.Printf("backfill-onboarding: %v", err)
		os.Exit(1)
	}

	if *dryRun {
		log.Printf("backfill-onboarding: dry run, would create %d entries across %d accounts", res.Entries, res.Accounts)
		return
	}
	log.Printf("backfill-onboarding: created %d entries across %d accounts", res.Entries, res.Accounts)
}
