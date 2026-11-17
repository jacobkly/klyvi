package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"klyvi-api/internal/movies"
	"klyvi-api/internal/reco"
	"klyvi-api/internal/tv"
)

const (
	// catalogLookback re-reads rows written shortly before the last refresh.
	// updated_at is stamped when a write starts, so a long seed transaction
	// can commit rows dated before a refresh that could not yet see them.
	catalogLookback = 10 * time.Minute
	// catalogFullReload is how often a refresh reads the whole catalog, which
	// drops titles that left it and picks up any index row added late.
	catalogFullReload = time.Hour
)

// cacheEntry is one title as the pool draws it: the candidate the scorer
// reads, and the ids and votes the draw filters on. genres are as stored, so
// a series' are TV genre ids, the space SeriesCandidatesByTaste is asked in.
type cacheEntry struct {
	cand     reco.Candidate
	genres   []int
	keywords []int
	votes    int
}

// catalogSnapshot is one immutable view of the catalog. Each type's list is
// sorted most voted first, ties to the lower media id, the order the SQL
// draws use.
type catalogSnapshot struct {
	films, series []*cacheEntry
	byID          map[int]*cacheEntry
}

// catalogLoader reads every indexed film and series written at or after
// since. The zero time reads the whole catalog.
type catalogLoader func(ctx context.Context, since time.Time) ([]cacheEntry, error)

// cachedCatalog answers the feed's candidate draws from memory. The SQL draws
// ship about 1,200 rows per feed, which over a homelab-to-Supabase link was
// most of the feed's time. Until the first load lands, and for anything it
// does not hold, it reads through to the database adapter. Card details and
// reason names always do.
type cachedCatalog struct {
	reco.CatalogRepository // the database adapter

	load catalogLoader
	now  func() time.Time
	snap atomic.Pointer[catalogSnapshot]

	mu       sync.Mutex // serialises refreshes and guards the fields below
	entries  map[int]cacheEntry
	last     time.Time
	lastFull time.Time
}

func newCachedCatalog(sql reco.CatalogRepository, load catalogLoader) *cachedCatalog {
	return &cachedCatalog{CatalogRepository: sql, load: load, now: time.Now}
}

// repoCatalogLoader reads films and series side by side from the caches.
func repoCatalogLoader(movieRepo *movies.Repository, tvRepo *tv.Repository) catalogLoader {
	return func(ctx context.Context, since time.Time) ([]cacheEntry, error) {
		var (
			films  []movies.RecoCandidateRow
			series []tv.RecoCandidateRow
		)
		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() (err error) {
			films, err = movieRepo.RecoCandidatesChangedSince(gctx, since)
			return err
		})
		g.Go(func() (err error) {
			series, err = tvRepo.RecoCandidatesChangedSince(gctx, since)
			return err
		})
		if err := g.Wait(); err != nil {
			return nil, err
		}
		out := make([]cacheEntry, 0, len(films)+len(series))
		for i, c := range rowsToCandidates(films) {
			out = append(out, cacheEntry{c, films[i].GenreIDs, films[i].KeywordIDs, films[i].VoteCount})
		}
		for i, c := range seriesToCandidates(series) {
			out = append(out, cacheEntry{c, series[i].GenreIDs, series[i].KeywordIDs, series[i].VoteCount})
		}
		return out, nil
	}
}

// Refresh reads what changed since the last refresh, or the whole catalog on
// the first one and once every catalogFullReload, and swaps in a new snapshot.
func (c *cachedCatalog) Refresh(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	start := c.now()
	full := c.entries == nil || start.Sub(c.lastFull) >= catalogFullReload
	var since time.Time
	if !full {
		since = c.last.Add(-catalogLookback)
	}
	rows, err := c.load(ctx, since)
	if err != nil {
		return fmt.Errorf("load catalog: %w", err)
	}

	if full {
		c.entries = make(map[int]cacheEntry, len(rows))
	}
	for _, e := range rows {
		c.entries[e.cand.MediaID] = e
	}
	c.snap.Store(buildSnapshot(c.entries))
	c.last = start
	if full {
		c.lastFull = start
	}
	return nil
}

// Run refreshes every interval until ctx ends. A failed refresh keeps the
// last snapshot, which is at worst a few minutes stale.
func (c *cachedCatalog) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.Refresh(ctx); err != nil && ctx.Err() == nil {
				log.Printf("reco catalog: refresh: %v", err)
			}
		}
	}
}

// Size is how many films and series the cache holds, 0 before the first load.
func (c *cachedCatalog) Size() (films, series int) {
	s := c.snap.Load()
	if s == nil {
		return 0, 0
	}
	return len(s.films), len(s.series)
}

func buildSnapshot(entries map[int]cacheEntry) *catalogSnapshot {
	s := &catalogSnapshot{byID: make(map[int]*cacheEntry, len(entries))}
	for id, e := range entries {
		s.byID[id] = &e
		if e.cand.MediaType == "tv" {
			s.series = append(s.series, &e)
		} else {
			s.films = append(s.films, &e)
		}
	}
	byVotes := func(list []*cacheEntry) {
		sort.Slice(list, func(i, j int) bool {
			if list[i].votes != list[j].votes {
				return list[i].votes > list[j].votes
			}
			return list[i].cand.MediaID < list[j].cand.MediaID
		})
	}
	byVotes(s.films)
	byVotes(s.series)
	return s
}

func (c *cachedCatalog) CandidatesByTaste(ctx context.Context, genreIDs, keywordIDs []int, minVotes, limit int) ([]reco.Candidate, error) {
	s := c.snap.Load()
	if s == nil {
		return c.CatalogRepository.CandidatesByTaste(ctx, genreIDs, keywordIDs, minVotes, limit)
	}
	return byTaste(s.films, genreIDs, keywordIDs, minVotes, limit), nil
}

func (c *cachedCatalog) SeriesCandidatesByTaste(ctx context.Context, genreIDs, keywordIDs []int, minVotes, limit int) ([]reco.Candidate, error) {
	s := c.snap.Load()
	if s == nil {
		return c.CatalogRepository.SeriesCandidatesByTaste(ctx, genreIDs, keywordIDs, minVotes, limit)
	}
	return byTaste(s.series, genreIDs, keywordIDs, minVotes, limit), nil
}

func (c *cachedCatalog) SampleMovieCandidates(ctx context.Context, limit int) ([]reco.Candidate, error) {
	s := c.snap.Load()
	if s == nil {
		return c.CatalogRepository.SampleMovieCandidates(ctx, limit)
	}
	return mostVoted(s.films, limit), nil
}

func (c *cachedCatalog) SampleSeriesCandidates(ctx context.Context, limit int) ([]reco.Candidate, error) {
	s := c.snap.Load()
	if s == nil {
		return c.CatalogRepository.SampleSeriesCandidates(ctx, limit)
	}
	return mostVoted(s.series, limit), nil
}

// CandidatesByMediaIDs answers a history from memory and sends only the ids
// the cache does not hold, such as a title added since the last refresh, to
// the database.
func (c *cachedCatalog) CandidatesByMediaIDs(ctx context.Context, mediaIDs []int) ([]reco.Candidate, error) {
	s := c.snap.Load()
	if s == nil {
		return c.CatalogRepository.CandidatesByMediaIDs(ctx, mediaIDs)
	}
	out := make([]reco.Candidate, 0, len(mediaIDs))
	var missing []int
	for _, id := range mediaIDs {
		if e, ok := s.byID[id]; ok {
			out = append(out, e.cand)
		} else {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return out, nil
	}
	rest, err := c.CatalogRepository.CandidatesByMediaIDs(ctx, missing)
	if err != nil {
		return nil, err
	}
	return append(out, rest...), nil
}

// byTaste is the SQL taste draw over one sorted list: titles at or above the
// vote floor that share a keyword, most voted first, then those that share
// only a genre, up to limit.
func byTaste(list []*cacheEntry, genreIDs, keywordIDs []int, minVotes, limit int) []reco.Candidate {
	out := make([]reco.Candidate, 0, min(limit, len(list)))
	taken := map[int]bool{}
	pass := func(ids []int, field func(*cacheEntry) []int) {
		want := make(map[int]bool, len(ids))
		for _, id := range ids {
			want[id] = true
		}
		for _, e := range list {
			if len(out) >= limit || e.votes < minVotes {
				return
			}
			if taken[e.cand.MediaID] || !sharesAny(field(e), want) {
				continue
			}
			taken[e.cand.MediaID] = true
			out = append(out, e.cand)
		}
	}
	if len(keywordIDs) > 0 {
		pass(keywordIDs, func(e *cacheEntry) []int { return e.keywords })
	}
	if len(genreIDs) > 0 {
		pass(genreIDs, func(e *cacheEntry) []int { return e.genres })
	}
	return out
}

func sharesAny(have []int, want map[int]bool) bool {
	for _, id := range have {
		if want[id] {
			return true
		}
	}
	return false
}

// mostVoted is the popular draw: up to limit voted titles, most voted first.
func mostVoted(list []*cacheEntry, limit int) []reco.Candidate {
	out := make([]reco.Candidate, 0, min(limit, len(list)))
	for _, e := range list {
		if len(out) >= limit || e.votes <= 0 {
			break
		}
		out = append(out, e.cand)
	}
	return out
}
