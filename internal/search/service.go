package search

import (
	"context"
	"fmt"
	"log"
	"net/url"

	"klyvi-api/internal/movies"
)

// TMDBClient is the upstream boundary for search. Search does not care about
// the concrete transport implementation; it only needs a component that can
// execute TMDB requests.
type TMDBClient interface {
	TMDBRequest(method, endpoint string, body interface{}) (map[string]interface{}, error)
}

// MovieRepository is the persistence boundary search uses to warm the movies
// cache from search hits. Search only needs the two write operations; the
// interface is declared here in the consumer package to match the rest of the
// repo's convention.
type MovieRepository interface {
	InsertMovie(ctx context.Context, movie *movies.Movie) error
	EnsureMediaIndex(ctx context.Context, movieID int) error
}

type Service struct {
	client TMDBClient
	repo   MovieRepository
}

// NewService wires search to the TMDB boundary and the movie cache. The repo
// is used best-effort to populate the movies cache from search hits; failures
// are logged but never block the search response.
func NewService(client TMDBClient, repo MovieRepository) *Service {
	return &Service{client: client, repo: repo}
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
		s.warmMovieCache(ctx, searchType, searchResult)
	}

	return searchResult, nil
}

// warmMovieCache best-effort persists movie hits from a search response into
// the movies cache and media_index. Errors are logged, never surfaced — the
// user is searching, not waiting for a cache fill.
func (s *Service) warmMovieCache(ctx context.Context, searchType string, raw map[string]interface{}) {
	results, ok := raw["results"].([]interface{})
	if !ok {
		return
	}

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
		if m.MovieID == 0 {
			continue
		}

		if err := s.repo.InsertMovie(ctx, m); err != nil {
			log.Printf("best-effort InsertMovie from search (id=%d) failed: %v", m.MovieID, err)
			continue
		}
		if err := s.repo.EnsureMediaIndex(ctx, m.MovieID); err != nil {
			log.Printf("best-effort EnsureMediaIndex from search (id=%d) failed: %v", m.MovieID, err)
		}
	}
}
