package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"klyvi-api/internal/movies"
	"klyvi-api/internal/platform/tmdb"
	"klyvi-api/internal/providers"
	"klyvi-api/internal/tv"
)

// TMDB's terms forbid caching its data for longer than six months, so a
// refresh never lets a row get older than this.
const maxRefreshAge = 180 * 24 * time.Hour

// staleListLimit bounds how many stale rows one table hands a run. Anything
// past it waits for the next run.
const staleListLimit = 100_000

// ageFlag is a -refresh-older-than value: whole days such as "150d", or any
// Go duration.
type ageFlag struct {
	d   time.Duration
	set bool
}

func (a *ageFlag) String() string {
	if a == nil || !a.set {
		return ""
	}
	return a.d.String()
}

func (a *ageFlag) Set(s string) error {
	d, err := parseAge(s)
	if err != nil {
		return err
	}
	if d < 0 {
		return fmt.Errorf("age %q is negative", s)
	}
	if d > maxRefreshAge {
		return fmt.Errorf("age %q is past TMDB's six month cache limit (at most 180d)", s)
	}
	a.d, a.set = d, true
	return nil
}

func parseAge(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, fmt.Errorf("age %q: want whole days such as 150d", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// staleItem is one cached TMDB record due for a refresh.
type staleItem struct {
	label     string
	updatedAt time.Time
	refresh   func(ctx context.Context) (removed bool, err error)
}

// staleSource lists one table's rows last written before cutoff, oldest first.
type staleSource struct {
	name string
	list func(ctx context.Context, cutoff time.Time, limit int) ([]staleItem, error)
}

// refresher refetches cached TMDB records oldest first, so the cache stays
// inside TMDB's six month limit. A run that ends early leaves the rest stale,
// and the next run starts with them.
type refresher struct {
	sources []staleSource
	cutoff  time.Time
	max     int // rows per run, 0 = no cap

	// throttlePause is the wait after TMDB answers 429.
	throttlePause time.Duration

	// A run of removals or failures this long stops the run. It points at
	// broken requests or a TMDB outage, not at titles TMDB really dropped,
	// and carrying on would empty the cache or burn the night on retries.
	maxRemovedInARow, maxFailedInARow int
}

type refreshResult struct {
	refreshed, removed, failed int
	left                       int  // stale rows still to do
	outOfTime                  bool // the time budget ended the run
}

func (r refresher) run(ctx context.Context) (refreshResult, error) {
	var res refreshResult
	var items []staleItem
	for _, src := range r.sources {
		got, err := src.list(ctx, r.cutoff, staleListLimit)
		if err != nil {
			return res, fmt.Errorf("list stale %s: %w", src.name, err)
		}
		log.Printf("%s: %d stale", src.name, len(got))
		items = append(items, got...)
	}
	slices.SortStableFunc(items, func(a, b staleItem) int { return a.updatedAt.Compare(b.updatedAt) })
	res.left = len(items)
	if r.max > 0 && len(items) > r.max {
		items = items[:r.max]
	}

	start := time.Now()
	var removedInARow, failedInARow int
	for i, it := range items {
		if ctx.Err() != nil {
			res.outOfTime = true
			break
		}
		removed, err := it.refresh(ctx)
		switch {
		case err != nil && ctx.Err() != nil:
			res.outOfTime = true
		case err != nil:
			res.failed++
			failedInARow++
			removedInARow = 0
			log.Printf("%s: %v", it.label, err)
			if tmdb.IsStatus(err, http.StatusTooManyRequests) {
				log.Printf("TMDB is throttling, pausing %s", r.throttlePause)
				if err := sleep(ctx, r.throttlePause); err != nil {
					res.outOfTime = true
				}
			}
		case removed:
			res.removed++
			res.left--
			removedInARow++
			failedInARow = 0
			log.Printf("%s: TMDB no longer has it, removed from the cache", it.label)
		default:
			res.refreshed++
			res.left--
			removedInARow, failedInARow = 0, 0
		}
		if res.outOfTime {
			break
		}
		if r.maxRemovedInARow > 0 && removedInARow >= r.maxRemovedInARow {
			return res, fmt.Errorf("%d removals in a row, stopping in case the requests are wrong", removedInARow)
		}
		if r.maxFailedInARow > 0 && failedInARow >= r.maxFailedInARow {
			return res, fmt.Errorf("%d failures in a row, stopping", failedInARow)
		}
		if (i+1)%100 == 0 {
			log.Printf("refreshed %d/%d (%.0fs elapsed)", i+1, len(items), time.Since(start).Seconds())
		}
	}
	return res, nil
}

// sleep pauses for d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// refreshCatalog refetches every cached row last written longer ago than
// age, then recomputes the recommender's feature stats if anything changed.
// Running out of time is not an error: the next run picks up the rest.
func refreshCatalog(ctx context.Context, dbConn *sqlx.DB, client *tmdb.Client, movieRepo *movies.Repository, tvRepo *tv.Repository, age time.Duration, maxRows int) error {
	r := refresher{
		sources:          catalogSources(movieRepo, movies.NewService(client, movieRepo), tvRepo, tv.NewService(client, tvRepo)),
		cutoff:           time.Now().Add(-age),
		max:              maxRows,
		throttlePause:    30 * time.Second,
		maxRemovedInARow: 25,
		maxFailedInARow:  25,
	}
	log.Printf("refreshing rows last written before %s", r.cutoff.Format(time.RFC3339))

	start := time.Now()
	res, err := r.run(ctx)
	log.Printf("refresh done in %s: %d refreshed, %d removed, %d failed, %d still stale",
		time.Since(start).Truncate(time.Second), res.refreshed, res.removed, res.failed, res.left)
	if res.outOfTime {
		log.Printf("the time budget ran out; the next run starts with the %d still stale", res.left)
	}

	// Where-to-watch copies refetch on view after a week. One nobody viewed
	// goes once it reaches the catalog age, inside TMDB's cache limit.
	pruneCtx, cancelPrune := context.WithTimeout(context.Background(), time.Minute)
	pruned, pruneErr := providers.NewRepository(dbConn).DeleteOlderThan(pruneCtx, r.cutoff)
	cancelPrune()
	if pruneErr != nil {
		err = errors.Join(err, pruneErr)
	} else {
		log.Printf("watch providers: %d copies past the catalog age removed", pruned)
	}

	if res.refreshed+res.removed > 0 {
		// The run's own budget may be spent by now. The stats are a few
		// seconds of SQL.
		statsCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if statsErr := refreshFeatureStats(statsCtx, dbConn, movieRepo, tvRepo); statsErr != nil {
			return errors.Join(err, fmt.Errorf("feature stats: %w", statsErr))
		}
	}
	return err
}

// catalogSources are the cached TMDB tables a refresh walks.
func catalogSources(movieRepo *movies.Repository, movieSvc *movies.Service, tvRepo *tv.Repository, tvSvc *tv.Service) []staleSource {
	return []staleSource{
		{name: "movies", list: func(ctx context.Context, cutoff time.Time, limit int) ([]staleItem, error) {
			rows, err := movieRepo.ListStaleMovies(ctx, cutoff, limit)
			items := make([]staleItem, len(rows))
			for i, row := range rows {
				items[i] = staleItem{
					label:     fmt.Sprintf("movie %d", row.ID),
					updatedAt: row.UpdatedAt,
					refresh:   func(ctx context.Context) (bool, error) { return movieSvc.RefreshMovie(ctx, row.ID) },
				}
			}
			return items, err
		}},
		{name: "movie collections", list: func(ctx context.Context, cutoff time.Time, limit int) ([]staleItem, error) {
			rows, err := movieRepo.ListStaleCollections(ctx, cutoff, limit)
			items := make([]staleItem, len(rows))
			for i, row := range rows {
				items[i] = staleItem{
					label:     fmt.Sprintf("collection %d", row.ID),
					updatedAt: row.UpdatedAt,
					refresh:   func(ctx context.Context) (bool, error) { return movieSvc.RefreshCollection(ctx, row.ID) },
				}
			}
			return items, err
		}},
		{name: "series", list: func(ctx context.Context, cutoff time.Time, limit int) ([]staleItem, error) {
			rows, err := tvRepo.ListStaleSeries(ctx, cutoff, limit)
			items := make([]staleItem, len(rows))
			for i, row := range rows {
				items[i] = staleItem{
					label:     fmt.Sprintf("series %d", row.TVID),
					updatedAt: row.UpdatedAt,
					refresh:   func(ctx context.Context) (bool, error) { return tvSvc.RefreshSeries(ctx, row.TVID) },
				}
			}
			return items, err
		}},
		{name: "seasons", list: func(ctx context.Context, cutoff time.Time, limit int) ([]staleItem, error) {
			rows, err := tvRepo.ListStaleSeasons(ctx, cutoff, limit)
			items := make([]staleItem, len(rows))
			for i, row := range rows {
				items[i] = staleItem{
					label:     fmt.Sprintf("season %d of series %d", row.SeasonNumber, row.TVID),
					updatedAt: row.UpdatedAt,
					refresh: func(ctx context.Context) (bool, error) {
						return tvSvc.RefreshSeason(ctx, row.TVID, row.SeasonNumber)
					},
				}
			}
			return items, err
		}},
	}
}
