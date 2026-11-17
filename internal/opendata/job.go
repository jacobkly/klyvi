package opendata

import (
	"context"
	"fmt"
	"log"
	"time"
)

// MediaTypes are the catalog types the job covers.
var MediaTypes = []string{"movie", "tv"}

type factStore interface {
	CatalogIDs(ctx context.Context, mediaType string) ([]int, error)
	Save(ctx context.Context, mediaType string, batch []int, facts []Facts) (int, error)
}

type factSource interface {
	Facts(ctx context.Context, mediaType string, tmdbIDs []int) ([]Facts, error)
}

// Job refreshes the Wikidata facts of the whole catalog, a batch of ids at a
// time.
type Job struct {
	store  factStore
	source factSource
	batch  int
	pause  time.Duration
}

// NewJob builds a job that asks the source about batch ids at a time and waits
// pause between requests, to stay polite to a shared public service.
func NewJob(store factStore, source factSource, batch int, pause time.Duration) *Job {
	return &Job{store: store, source: source, batch: max(batch, 1), pause: pause}
}

// Result counts what a run did.
type Result struct {
	Checked int // catalog titles asked about
	Matched int // titles Wikidata matched
	Changed int // rows inserted, changed or dropped
	Failed  int // batches that failed
}

// Run refreshes every title of the media types. A failed batch is logged and
// skipped so one bad response cannot stop the rest, and the run returns an
// error at the end if any failed.
func (j *Job) Run(ctx context.Context, mediaTypes []string) (Result, error) {
	var res Result
	first := true
	for _, mt := range mediaTypes {
		ids, err := j.store.CatalogIDs(ctx, mt)
		if err != nil {
			return res, err
		}
		for start := 0; start < len(ids); start += j.batch {
			if !first && j.pause > 0 {
				select {
				case <-ctx.Done():
					return res, ctx.Err()
				case <-time.After(j.pause):
				}
			}
			first = false

			batch := ids[start:min(start+j.batch, len(ids))]
			facts, err := j.source.Facts(ctx, mt, batch)
			if err == nil {
				var changed int
				if changed, err = j.store.Save(ctx, mt, batch, facts); err == nil {
					res.Checked += len(batch)
					res.Matched += len(facts)
					res.Changed += changed
					continue
				}
			}
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			res.Failed++
			log.Printf("opendata: %s batch from tmdb id %d: %v", mt, batch[0], err)
		}
	}
	if res.Failed > 0 {
		return res, fmt.Errorf("%d batches failed", res.Failed)
	}
	return res, nil
}
