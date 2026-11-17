// Package plots keeps one vector per catalog title, embedded from the plot
// section of its English Wikipedia article (CC BY-SA). The article comes from
// the title's Wikidata facts. TMDB text never reaches the model: the job reads
// only article URLs from the database and only Wikipedia text from the web.
package plots

import (
	"context"
	"fmt"
	"log"
	"time"
)

// MediaTypes are the catalog types the job covers.
var MediaTypes = []string{"movie", "tv"}

// Target is a catalog title with a Wikipedia article, and the revision and
// model its stored vector came from, if it has one.
type Target struct {
	TMDBID     int     `db:"tmdb_id"`
	ArticleURL string  `db:"wikipedia_url"`
	RevisionID *int64  `db:"revision_id"`
	Model      *string `db:"model"`
}

// Plot is what the job stores for a title. Embedding is nil when the revision
// has no usable plot section.
type Plot struct {
	TMDBID     int
	RevisionID int64
	Model      string
	Words      int
	Embedding  []float32
}

type plotStore interface {
	Targets(ctx context.Context, mediaType string) ([]Target, error)
	Save(ctx context.Context, mediaType string, plots []Plot) error
	Delete(ctx context.Context, mediaType string, tmdbIDs []int) error
	Prune(ctx context.Context, mediaType string) (int, error)
}

type articleSource interface {
	Revisions(ctx context.Context, titles []string) (map[string]int64, error)
	Texts(ctx context.Context, revisionIDs []int64) (map[int64]string, error)
}

type embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// Job refreshes the plot vectors of the whole catalog, a batch of articles at
// a time.
type Job struct {
	store    plotStore
	source   articleSource
	embedder embedder
	model    string
	pause    time.Duration
	started  bool // a Wikipedia request went out this run
}

// NewJob builds a job that embeds with the named model and waits pause between
// Wikipedia requests, to stay polite to a shared public service.
func NewJob(store plotStore, source articleSource, e embedder, model string, pause time.Duration) *Job {
	return &Job{store: store, source: source, embedder: e, model: model, pause: pause}
}

// Result counts what a run did.
type Result struct {
	Articles  int // titles with an article
	Unchanged int // articles whose revision and model match the stored vector
	Embedded  int // plots embedded
	NoPlot    int // changed articles without a usable plot section
	Removed   int // rows dropped because the article or its link is gone
	Failed    int // batches that failed
}

// Run refreshes every title of the media types. Only articles whose revision
// changed since the last run, or titles without a vector from the current
// model, are fetched and embedded, so a rerun against an unchanged Wikipedia
// embeds nothing. A failed batch is logged and skipped, and the run returns an
// error at the end if any failed.
func (j *Job) Run(ctx context.Context, mediaTypes []string) (Result, error) {
	var res Result
	j.started = false
	for _, mt := range mediaTypes {
		pruned, err := j.store.Prune(ctx, mt)
		if err != nil {
			return res, err
		}
		res.Removed += pruned
		targets, err := j.store.Targets(ctx, mt)
		if err != nil {
			return res, err
		}
		for start := 0; start < len(targets); start += MaxPerRequest {
			batch := targets[start:min(start+MaxPerRequest, len(targets))]
			if err := j.runBatch(ctx, mt, batch, &res); err != nil {
				if ctx.Err() != nil {
					return res, ctx.Err()
				}
				res.Failed++
				log.Printf("plots: %s batch from tmdb id %d: %v", mt, batch[0].TMDBID, err)
			}
			if n := start/MaxPerRequest + 1; n%20 == 0 {
				log.Printf("plots: %s %d of %d titles, %d embedded so far", mt, start+len(batch), len(targets), res.Embedded)
			}
		}
	}
	if res.Failed > 0 {
		return res, fmt.Errorf("%d batches failed", res.Failed)
	}
	return res, nil
}

func (j *Job) runBatch(ctx context.Context, mediaType string, batch []Target, res *Result) error {
	titles := make([]string, 0, len(batch))
	titleOf := make(map[int]string, len(batch))
	for _, t := range batch {
		if title, ok := TitleFromURL(t.ArticleURL); ok {
			titleOf[t.TMDBID] = title
			titles = append(titles, title)
		}
	}
	if err := j.wait(ctx); err != nil {
		return err
	}
	revs, err := j.source.Revisions(ctx, titles)
	if err != nil {
		return err
	}

	var todo []Target
	var gone []int
	var revIDs []int64
	seen := map[int64]bool{}
	for _, t := range batch {
		rev, ok := revs[titleOf[t.TMDBID]]
		if !ok {
			if t.RevisionID != nil {
				gone = append(gone, t.TMDBID)
			}
			continue
		}
		res.Articles++
		if t.RevisionID != nil && *t.RevisionID == rev && t.Model != nil && *t.Model == j.model {
			res.Unchanged++
			continue
		}
		t.RevisionID = &rev
		todo = append(todo, t)
		if !seen[rev] {
			seen[rev] = true
			revIDs = append(revIDs, rev)
		}
	}
	if len(gone) > 0 {
		if err := j.store.Delete(ctx, mediaType, gone); err != nil {
			return err
		}
		res.Removed += len(gone)
	}
	if len(todo) == 0 {
		return nil
	}

	if err := j.wait(ctx); err != nil {
		return err
	}
	texts, err := j.source.Texts(ctx, revIDs)
	if err != nil {
		return err
	}
	plots := make([]Plot, 0, len(todo))
	for _, t := range todo {
		wikitext, ok := texts[*t.RevisionID]
		if !ok {
			continue // the revision vanished between the two calls, next run catches it
		}
		p, err := j.plot(ctx, t.TMDBID, *t.RevisionID, wikitext)
		if err != nil {
			return err
		}
		if p.Embedding == nil {
			res.NoPlot++
		} else {
			res.Embedded++
		}
		plots = append(plots, p)
	}
	return j.store.Save(ctx, mediaType, plots)
}

// plot builds the stored row for one article revision. The model sees only
// the cleaned plot section of the Wikipedia wikitext.
func (j *Job) plot(ctx context.Context, tmdbID int, revisionID int64, wikitext string) (Plot, error) {
	text, words := ExtractPlot(wikitext)
	p := Plot{TMDBID: tmdbID, RevisionID: revisionID, Model: j.model, Words: words}
	if words < MinPlotWords {
		return p, nil
	}
	vec, err := j.embedder.Embed(ctx, EmbedInput(text))
	if err != nil {
		return p, fmt.Errorf("tmdb id %d: %w", tmdbID, err)
	}
	p.Embedding = vec
	return p, nil
}

// wait spaces out Wikipedia requests: no wait before a run's first request,
// then a pause before each one after it.
func (j *Job) wait(ctx context.Context) error {
	if !j.started || j.pause <= 0 {
		j.started = true
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(j.pause):
		return nil
	}
}
