package search

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"sync"
	"time"

	"klyvi-api/internal/movies"
)

// TMDBClient is the upstream boundary for search. Search does not care about
// the concrete transport implementation; it only needs a component that can
// execute TMDB requests.
type TMDBClient interface {
	TMDBRequest(method, endpoint string, body interface{}) (map[string]interface{}, error)
}

// MovieRepository is the persistence boundary search uses to warm the movies
// cache from search hits. Search only needs the two batch writes; the
// interface is declared here in the consumer package to match the rest of the
// repo's convention.
type MovieRepository interface {
	InsertMovies(ctx context.Context, batch []*movies.Movie) error
	EnsureMediaIndexes(ctx context.Context, movieIDs []int) error
}

const (
	// maxConcurrentWarms caps background cache writes. A search that arrives
	// while every slot is busy skips warming, which only costs a cache miss.
	maxConcurrentWarms = 2
	warmTimeout        = 10 * time.Second
)

type Service struct {
	client  TMDBClient
	repo    MovieRepository
	warming chan struct{}
	wg      sync.WaitGroup
}

// NewService wires search to the TMDB boundary and the movie cache. The repo
// is used best-effort to populate the movies cache from search hits, after the
// response and never on the request's clock.
func NewService(client TMDBClient, repo MovieRepository) *Service {
	return &Service{
		client:  client,
		repo:    repo,
		warming: make(chan struct{}, maxConcurrentWarms),
	}
}

func (s *Service) GetSearchResult(ctx context.Context, searchType, query string) (interface{}, error) {
	var endpoint string

	switch searchType {
	case "movie":
		endpoint = "/search/movie"
	case "tv":
		endpoint = "/search/tv"
	case "person":
		endpoint = "/search/person"
	default:
		endpoint = "/search/multi"
	}

	endpoint = fmt.Sprintf("%s?query=%s&page=1&language=en-US", endpoint, url.QueryEscape(query))
	searchResult, err := s.client.TMDBRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	// Warm the movie cache from search hits. Only the movie and multi paths
	// can produce movie items; tv/person searches never do.
	if searchType == "movie" || searchType == "" {
		s.warmMovieCache(movieHits(searchType, searchResult))
	}

	return searchResult, nil
}

// Wait blocks until background cache warming finishes. Tests call it before
// reading the cache, and the server calls it while shutting down.
func (s *Service) Wait() {
	s.wg.Wait()
}

// movieHits normalizes the movie items in a search response, dropping repeats.
// It runs on the request goroutine so the background write never shares the
// response map with the JSON encoder.
func movieHits(searchType string, raw map[string]interface{}) []*movies.Movie {
	results, ok := raw["results"].([]interface{})
	if !ok {
		return nil
	}

	seen := make(map[int]bool, len(results))
	var hits []*movies.Movie
	for _, r := range results {
		item, ok := r.(map[string]interface{})
		if !ok {
			continue
		}

		// In multi search, results are heterogeneous and tagged with
		// media_type. Skip anything that isn't a movie.
		if searchType == "" {
			mt, _ := item["media_type"].(string)
			if mt != "movie" {
				continue
			}
		}

		m := movies.NormalizeTMDBMovie(item)
		if m.MovieID == 0 || seen[m.MovieID] {
			continue
		}
		seen[m.MovieID] = true
		hits = append(hits, m)
	}
	return hits
}

// warmMovieCache persists search hits into the movies cache and media_index
// in the background, one statement per table. Errors are logged, never
// surfaced: the user is searching, not waiting for a cache fill.
func (s *Service) warmMovieCache(hits []*movies.Movie) {
	if len(hits) == 0 {
		return
	}
	select {
	case s.warming <- struct{}{}:
	default:
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.warming }()

		ctx, cancel := context.WithTimeout(context.Background(), warmTimeout)
		defer cancel()

		if err := s.repo.InsertMovies(ctx, hits); err != nil {
			log.Printf("best-effort InsertMovies from search (%d hits) failed: %v", len(hits), err)
			return
		}
		ids := make([]int, len(hits))
		for i, m := range hits {
			ids[i] = m.MovieID
		}
		if err := s.repo.EnsureMediaIndexes(ctx, ids); err != nil {
			log.Printf("best-effort EnsureMediaIndexes from search (%d hits) failed: %v", len(ids), err)
		}
	}()
}
