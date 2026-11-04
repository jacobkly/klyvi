package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"klyvi-api/internal/platform/tmdb"
)

// How far around today a notice is created, in days. A premiere gets a week of
// warning, and anything that came out recently still counts, so a missed night
// or a title added late isn't lost. A film's cached date is checked over a wider
// span, because that's how a date TMDB moved gets noticed.
const (
	premiereDaysAhead  = 7
	noticeDaysBack     = 30
	filmRefetchDaysOut = 60
)

const (
	defaultThrottlePause   = 30 * time.Second
	defaultMaxFailedInARow = 25
	dateLayout             = "2006-01-02"
)

// SeriesRefresher refetches a series from TMDB into the cache. removed means
// TMDB no longer has it. tv.Service provides it.
type SeriesRefresher interface {
	RefreshSeries(ctx context.Context, tvID int) (removed bool, err error)
}

// FilmRefresher is SeriesRefresher for films. movies.Service provides it.
type FilmRefresher interface {
	RefreshMovie(ctx context.Context, movieID int) (removed bool, err error)
}

// Job is the nightly run: it refetches what people track and creates the
// notices that are due. Every step is safe to repeat, since the table's unique
// key turns a second notice for the same event into nothing.
type Job struct {
	repo   *Repository
	series SeriesRefresher
	films  FilmRefresher

	// Now is the clock.
	Now func() time.Time
	// ThrottlePause is the wait after TMDB answers 429.
	ThrottlePause time.Duration
	// MaxFailedInARow stops the run. A streak like that means an outage, and
	// the next night can do the work instead.
	MaxFailedInARow int
}

// NewJob builds a Job with the production settings.
func NewJob(repo *Repository, series SeriesRefresher, films FilmRefresher) *Job {
	return &Job{
		repo:            repo,
		series:          series,
		films:           films,
		Now:             time.Now,
		ThrottlePause:   defaultThrottlePause,
		MaxFailedInARow: defaultMaxFailedInARow,
	}
}

// JobResult is what a run did, for its log line.
type JobResult struct {
	SeriesChecked int
	FilmsChecked  int
	Failed        int
	Created       int
	Deleted       int
}

// Run does one night's work. It returns what it managed even when it fails.
func (j *Job) Run(ctx context.Context) (JobResult, error) {
	now := j.Now().UTC()
	r := &jobRun{Job: j, today: now}

	deleted, err := j.repo.DeleteExpired(ctx, now)
	if err != nil {
		return r.res, err
	}
	r.res.Deleted = int(deleted)

	if err := r.checkSeries(ctx); err != nil {
		return r.res, err
	}
	if err := r.checkFilms(ctx); err != nil {
		return r.res, err
	}
	return r.res, nil
}

// jobRun is the state of one run.
type jobRun struct {
	*Job
	today        time.Time
	res          JobResult
	failedInARow int
}

// day formats the date offset days from today. Dates stay YYYY-MM-DD strings
// throughout, which compare correctly as text, and a missing date sorts before
// any window and so falls out on its own.
func (r *jobRun) day(offset int) string {
	return r.today.AddDate(0, 0, offset).Format(dateLayout)
}

func (r *jobRun) checkSeries(ctx context.Context) error {
	trackers, err := r.repo.SeriesTrackers(ctx)
	if err != nil {
		return err
	}
	ids, byID := groupByTitle(trackers, func(t SeriesTracker) int { return t.TVID })

	from, to := r.day(-noticeDaysBack), r.day(premiereDaysAhead)
	for _, tvID := range ids {
		r.res.SeriesChecked++
		current, err := r.refresh(ctx, fmt.Sprintf("series %d", tvID), func() (bool, error) {
			return r.series.RefreshSeries(ctx, tvID)
		})
		if err != nil {
			return err
		}
		if !current {
			continue
		}

		name, raw, err := r.repo.SeriesSeasons(ctx, tvID)
		if err != nil {
			return err
		}
		var seasons []struct {
			SeasonNumber int    `json:"season_number"`
			AirDate      string `json:"air_date"`
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &seasons); err != nil {
				return fmt.Errorf("decode seasons of series %d: %w", tvID, err)
			}
		}

		if name == "" {
			continue
		}

		for _, season := range seasons {
			if season.AirDate < from || season.AirDate > to {
				continue
			}
			for _, t := range byID[tvID] {
				// A notice needs a season newer than the user's latest. That also
				// rules out specials, since season 0 is never newer than anything.
				if !t.NotifyPremieres || season.SeasonNumber <= t.HighestSeason {
					continue
				}
				number := season.SeasonNumber
				if err := r.insert(ctx, NewNotice{
					UserID:       t.UserID,
					Kind:         KindSeasonPremiere,
					MediaType:    "tv",
					TMDBID:       tvID,
					SeasonNumber: &number,
					Title:        name,
					EventDate:    season.AirDate,
				}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (r *jobRun) checkFilms(ctx context.Context) error {
	planners, err := r.repo.FilmPlanners(ctx, r.day(-filmRefetchDaysOut), r.day(filmRefetchDaysOut))
	if err != nil {
		return err
	}
	ids, byID := groupByTitle(planners, func(p FilmPlanner) int { return p.TMDBID })

	from, to := r.day(-noticeDaysBack), r.day(0)
	for _, movieID := range ids {
		r.res.FilmsChecked++
		current, err := r.refresh(ctx, fmt.Sprintf("film %d", movieID), func() (bool, error) {
			return r.films.RefreshMovie(ctx, movieID)
		})
		if err != nil {
			return err
		}
		if !current {
			continue
		}

		title, release, err := r.repo.FilmRelease(ctx, movieID)
		if err != nil {
			return err
		}
		if title == "" || release < from || release > to {
			continue
		}
		for _, p := range byID[movieID] {
			if !p.NotifyReleases {
				continue
			}
			if err := r.insert(ctx, NewNotice{
				UserID:    p.UserID,
				Kind:      KindFilmRelease,
				MediaType: "movie",
				TMDBID:    movieID,
				Title:     title,
				EventDate: release,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// refresh runs one TMDB fetch and reports whether the title's cached row is now
// current. A failed fetch costs that title until tomorrow. The error is only for
// what ends the run.
func (r *jobRun) refresh(ctx context.Context, label string, fetch func() (bool, error)) (bool, error) {
	removed, err := fetch()
	if err == nil {
		r.failedInARow = 0
		return !removed, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}

	r.res.Failed++
	r.failedInARow++
	log.Printf("notify: %s: %v", label, err)
	if r.failedInARow >= r.MaxFailedInARow {
		return false, fmt.Errorf("%d failures in a row, stopping", r.failedInARow)
	}
	if tmdb.IsStatus(err, http.StatusTooManyRequests) {
		log.Printf("notify: TMDB is throttling, pausing %s", r.ThrottlePause)
		if err := sleep(ctx, r.ThrottlePause); err != nil {
			return false, err
		}
	}
	return false, nil
}

func (r *jobRun) insert(ctx context.Context, n NewNotice) error {
	created, err := r.repo.Insert(ctx, n)
	if err != nil {
		return err
	}
	if created {
		r.res.Created++
	}
	return nil
}

// groupByTitle groups rows by title id and keeps the order ids first appear in.
func groupByTitle[T any](rows []T, id func(T) int) ([]int, map[int][]T) {
	var ids []int
	byID := map[int][]T{}
	for _, row := range rows {
		key := id(row)
		if _, seen := byID[key]; !seen {
			ids = append(ids, key)
		}
		byID[key] = append(byID[key], row)
	}
	return ids, byID
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
