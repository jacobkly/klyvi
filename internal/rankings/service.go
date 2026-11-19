package rankings

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"klyvi-api/internal/toprated"
)

// TMDBClient is the external boundary: fetch a list page. Mirrors the small
// behaviour contract the movies/tv services use.
type TMDBClient interface {
	TMDBRequest(ctx context.Context, method, endpoint string, body interface{}) (map[string]interface{}, error)
}

// MediaIDResolver maps TMDB ids to internal media_index ids for items already
// in the local catalog, so a ranking can carry a media_id the client can act
// on. Unknown ids are simply absent from the returned map.
type MediaIDResolver interface {
	ResolveMovieMediaIDs(ctx context.Context, tmdbIDs []int) (map[int]int, error)
}

// pagesToFetch is how many pages of TMDB's discover list, best score first,
// make the pool (20 a page, so 200 titles) that is re-ranked and cut to 100.
const pagesToFetch = 10

// cacheTTL keeps a computed top list for a day. Rankings move slowly and the
// pool is public, so a per-request TMDB fan-out would be wasteful.
const cacheTTL = 24 * time.Hour

type cacheEntry struct {
	data     []Ranking
	computed time.Time
}

type Service struct {
	client   TMDBClient
	resolver MediaIDResolver

	mu    sync.Mutex
	cache map[mediaKind]cacheEntry

	// misses collapses concurrent cache misses for one list into one fetch.
	misses singleflight.Group
}

func NewService(client TMDBClient, resolver MediaIDResolver) *Service {
	return &Service{client: client, resolver: resolver, cache: map[mediaKind]cacheEntry{}}
}

// Movies returns the top-100 movies, cached for 24h.
func (s *Service) Movies(ctx context.Context) ([]Ranking, error) {
	return s.top(ctx, mediaMovie)
}

// TV returns the top-100 TV series, cached for 24h.
func (s *Service) TV(ctx context.Context) ([]Ranking, error) {
	return s.top(ctx, mediaTV)
}

func (s *Service) top(ctx context.Context, kind mediaKind) ([]Ranking, error) {
	if cached, ok := s.fresh(kind); ok {
		return cached, nil
	}

	// Concurrent misses share one fetch. The list is cached for every
	// visitor, so the fetch runs to the end even if the request that started
	// it goes away.
	v, err, _ := s.misses.Do(strconv.Itoa(int(kind)), func() (any, error) {
		// A caller that missed just as a fetch finished finds it here.
		if cached, ok := s.fresh(kind); ok {
			return cached, nil
		}
		return s.compute(context.WithoutCancel(ctx), kind)
	})
	if err != nil {
		return nil, err
	}
	return v.([]Ranking), nil
}

// compute fetches the list from TMDB, resolves media ids and caches it.
func (s *Service) compute(ctx context.Context, kind mediaKind) ([]Ranking, error) {
	results, err := s.fetchPages(ctx, kind)
	if err != nil {
		return nil, err
	}

	// media_id resolution is best-effort and movies-only: TV series-level
	// index rows do not exist yet, so TV rankings carry null media_ids.
	var resolved map[int]int
	if kind == mediaMovie && s.resolver != nil {
		ids := make([]int, 0, len(results))
		for _, r := range results {
			if id := intField(r, "id"); id != 0 {
				ids = append(ids, id)
			}
		}
		if m, err := s.resolver.ResolveMovieMediaIDs(ctx, ids); err == nil {
			resolved = m
		}
	}

	rankings := normalizeRankings(weigh(results, kind), kind, resolved)
	s.store(kind, rankings)
	return rankings, nil
}

func (s *Service) fetchPages(ctx context.Context, kind mediaKind) ([]map[string]any, error) {
	var all []map[string]any
	for page := 1; page <= pagesToFetch; page++ {
		endpoint := toprated.MoviePath(page)
		if kind == mediaTV {
			endpoint = toprated.TVPath(page)
		}
		resp, err := s.client.TMDBRequest(ctx, "GET", endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", endpoint, err)
		}
		results, ok := resp["results"].([]any)
		if !ok {
			continue
		}
		for _, r := range results {
			if m, ok := r.(map[string]any); ok {
				all = append(all, m)
			}
		}
	}
	return all, nil
}

func (s *Service) fresh(kind mediaKind) ([]Ranking, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.cache[kind]
	if !ok || time.Since(e.computed) > cacheTTL {
		return nil, false
	}
	return e.data, true
}

func (s *Service) store(kind mediaKind, data []Ranking) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache[kind] = cacheEntry{data: data, computed: time.Now()}
}
