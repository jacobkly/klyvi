// notify is the nightly notices job. It refetches every series someone tracks
// and every planned film near its release, creates the notices that are due, and
// clears out old ones. Run it right after the catalog refresh:
//
//	go run ./cmd/notify
//	go run ./cmd/notify -timeout 15m
//
// TMDB calls are bounded by what people track, so a run stays in the hundreds.
// Running it twice creates nothing new. It also deletes error groups
// (error_events) not seen in 30 days, the retention the privacy page states.
//
// With -policy it does something else: it posts one notice about a change to
// the terms or privacy policy to every account, linking to the page, and runs
// nothing nightly. Post it before the change takes effect, as the terms
// promise. The same text posted twice reaches nobody twice.
//
//	go run ./cmd/notify -policy "Our terms change on 1 March. Here's what's new." -link /terms
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"github.com/jmoiron/sqlx"

	"klyvi-api/config"
	"klyvi-api/internal/errlog"
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
	policy := flag.String("policy", "", "post this policy notice to every account instead of running the nightly job")
	link := flag.String("link", "", "the site path a policy notice opens, such as /terms")
	flag.Parse()

	config.LoadEnvFile(*envFile)
	cfg := config.New()

	dbConn, err := db.New(cfg.DB)
	if err != nil {
		log.Fatalf("db: %v", err)
	}

	if *policy != "" || *link != "" {
		postPolicy(dbConn, *policy, *link, *timeout)
		return
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
	pruneErrors(dbConn)
	_ = dbConn.Close()

	log.Printf("notify: checked %d series and %d films, created %d notices, deleted %d, %d fetches failed",
		res.SeriesChecked, res.FilmsChecked, res.Created, res.Deleted, res.Failed)
	if runErr != nil {
		log.Printf("notify: %v", runErr)
		os.Exit(1)
	}
}

// pruneErrors deletes error groups past retention. A failure is logged and
// left for the next night, so housekeeping never fails the notices run.
func pruneErrors(dbConn *sqlx.DB) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	n, err := errlog.NewStore(dbConn).Prune(ctx, errlog.Retention)
	if err != nil {
		log.Printf("notify: error log prune: %v", err)
		return
	}
	log.Printf("notify: deleted %d error groups unseen for 30 days", n)
}

// postPolicy posts one policy notice to every account and exits non-zero if it
// can't.
func postPolicy(dbConn *sqlx.DB, text, link string, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	created, err := notifications.NewService(notifications.NewRepository(dbConn)).PostPolicy(ctx, text, link)
	cancel()
	_ = dbConn.Close()
	if err != nil {
		log.Printf("notify: %v", err)
		os.Exit(1)
	}
	log.Printf("notify: posted the policy notice to %d accounts", created)
}
