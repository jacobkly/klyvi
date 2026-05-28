package onboarding

import (
	"context"
	"log"
	"time"

	"klyvi-api/internal/movies"
)

// PoolRepository is the persistence boundary for the onboarding service.
type PoolRepository interface {
	ListActive(ctx context.Context, limit int) ([]PoolEntry, error)
}

// MovieLookup is the catalog-side surface this service depends on. The
// concrete movies.Service satisfies it. Calls go through the standard
// cache-then-TMDB path, so an uncached pool film is fetched and persisted
// the same way `/v1/movies/{id}` would persist it.
type MovieLookup interface {
	GetMovieById(ctx context.Context, id int, idType string) (*movies.Movie, error)
}

type Service struct {
	repo    PoolRepository
	catalog MovieLookup
}

func NewService(repo PoolRepository, catalog MovieLookup) *Service {
	return &Service{repo: repo, catalog: catalog}
}

// ListEnriched returns the active onboarding pool joined with catalog
// data so the frontend has everything it needs to render the swipe deck.
// A pool entry that isn't yet in the movies cache triggers a TMDB fetch
// via the standard movies.Service path — subsequent calls hit the cache.
//
// Catalog failures for a single film are logged best-effort and that
// film is omitted from the response rather than failing the whole call;
// the spec explicitly mentions "filter out at /onboarding/start time" if
// a film is unavailable.
func (s *Service) ListEnriched(ctx context.Context, limit int) ([]EnrichedPoolEntry, error) {
	rows, err := s.repo.ListActive(ctx, limit)
	if err != nil {
		return nil, err
	}

	out := make([]EnrichedPoolEntry, 0, len(rows))
	for _, row := range rows {
		movie, err := s.catalog.GetMovieById(ctx, int(row.TMDBID), "tmdb")
		if err != nil {
			log.Printf("onboarding: skip tmdb_id=%d, catalog fetch failed: %v", row.TMDBID, err)
			continue
		}
		if movie == nil {
			log.Printf("onboarding: skip tmdb_id=%d, catalog returned nil", row.TMDBID)
			continue
		}

		out = append(out, EnrichedPoolEntry{
			TMDBID:      row.TMDBID,
			Title:       derefString(movie.Title),
			PosterPath:  derefString(movie.PosterPath),
			ReleaseYear: yearOf(movie.ReleaseDate),
			Dimension:   row.Dimension,
		})
	}
	return out, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// yearOf returns the 4-digit year from a nullable release date, or 0.
// Comparing an `interface{ Year() int }` to nil is unsafe with typed-nil
// pointers, so this takes *time.Time directly.
func yearOf(t *time.Time) int {
	if t == nil {
		return 0
	}
	return t.Year()
}
