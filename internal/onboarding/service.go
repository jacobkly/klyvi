package onboarding

import (
	"context"
	"log"
	"time"

	"klyvi-api/internal/movies"
)

// MaxPoolLimit caps one pool request. The curated pool is about 30 films, so
// this is the whole deck with room to grow, while an uncached pool can never
// turn one public request into an unbounded run of TMDB fetches.
const MaxPoolLimit = 50

// PoolRepository is the persistence boundary for the onboarding service.
type PoolRepository interface {
	ListActiveWithCatalog(ctx context.Context, limit int) ([]PoolRow, error)
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
// limit is capped at MaxPoolLimit, and 0 asks for that many.
//
// Films cached with full detail come straight from the pool query. Any other
// film goes through the standard movies.Service path, which fetches it from
// TMDB and caches it. That includes a film cached only from a search hit: its
// row has no keywords, and the rating the user gives it is a reco signal.
//
// Catalog failures for a single film are logged best-effort and that
// film is omitted from the response rather than failing the whole call;
// the spec explicitly mentions "filter out at /onboarding/start time" if
// a film is unavailable.
func (s *Service) ListEnriched(ctx context.Context, limit int) ([]EnrichedPoolEntry, error) {
	if limit <= 0 || limit > MaxPoolLimit {
		limit = MaxPoolLimit
	}
	rows, err := s.repo.ListActiveWithCatalog(ctx, limit)
	if err != nil {
		return nil, err
	}

	out := make([]EnrichedPoolEntry, 0, len(rows))
	for _, row := range rows {
		if row.Detailed {
			out = append(out, EnrichedPoolEntry{
				TMDBID:      row.TMDBID,
				Title:       derefString(row.Title),
				PosterPath:  derefString(row.PosterPath),
				ReleaseYear: yearOf(row.ReleaseDate),
				Dimension:   row.Dimension,
			})
			continue
		}

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
