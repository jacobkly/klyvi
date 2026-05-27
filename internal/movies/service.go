package movies

import (
	"context"
	"fmt"
	"log"
)

// TMDBClient is the external data boundary for the movies service.
// The service only needs "something that can make TMDB requests", so it
// depends on this small behavior contract instead of a concrete client type.
//
// In production, `internal/platform/tmdb.Client` satisfies this interface.
// In tests, a fake implementation can be passed in to avoid real HTTP calls.
type TMDBClient interface {
	TMDBRequest(method, endpoint string, body interface{}) (map[string]interface{}, error)
}

// MovieRepository is the persistence boundary for the movies service.
// It describes only the repository operations that the service actually uses.
//
// Keeping this interface in the consumer package is idiomatic Go: the service
// defines the dependency shape it needs, and the concrete repository simply
// satisfies that contract.
type MovieRepository interface {
	GetByTMDBID(ctx context.Context, tmdbID int) (*Movie, error)
	GetMovieIDByMediaID(ctx context.Context, mediaID int) (int, error)
	InsertMovie(ctx context.Context, movie *Movie) error
	EnsureMediaIndex(ctx context.Context, movieID int) error
	GetCollectionIDByMovieID(ctx context.Context, movieID int) (int, error)
	GetCollectionByCollectionID(ctx context.Context, collectionID int) ([]MovieCollection, error)
	InsertMovieCollectionBatch(ctx context.Context, entries []MovieCollection) error
}

type Service struct {
	client TMDBClient
	repo   MovieRepository
}

// NewService wires the movies service to its external boundaries.
// Accepting interfaces here keeps the service easy to test and lets the
// concrete TMDB/DB implementations change without rewriting service logic.
func NewService(client TMDBClient, repo MovieRepository) *Service {
	return &Service{
		client: client,
		repo:   repo,
	}
}

func (s *Service) GetMovieById(ctx context.Context, id int, idType string) (*Movie, error) {
	var movieID int

	switch idType {
	case "tmdb":
		movieID = id

	case "media":
		var err error
		movieID, err = s.repo.GetMovieIDByMediaID(ctx, id)
		if err != nil {
			return nil, err
		}
		if movieID == 0 {
			return nil, fmt.Errorf("media not found")
		}

	default:
		return nil, fmt.Errorf("invalid id_type")
	}

	movie, err := s.repo.GetByTMDBID(ctx, movieID)
	if err != nil {
		return nil, err
	}
	if movie != nil {
		return movie, nil
	}

	raw, err := s.client.TMDBRequest(
		"GET",
		fmt.Sprintf("/movie/%d?language=en-US&append_to_response=keywords,credits", movieID),
		nil,
	)
	if err != nil {
		return nil, err
	}

	normalized := NormalizeTMDBMovie(raw)
	if err := s.repo.InsertMovie(ctx, normalized); err != nil {
		return nil, err
	}

	if err := s.repo.EnsureMediaIndex(ctx, movieID); err != nil {
		log.Printf("best-effort EnsureMediaIndex failed for movie %d: %v", movieID, err)
	}

	return normalized, nil
}

func (s *Service) GetMovieRecommendations(id int) (interface{}, error) {
	endpoint := fmt.Sprintf("/movie/%d/recommendations?language=en-US&page=1", id)
	recommendations, err := s.client.TMDBRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	return recommendations, nil
}

func (s *Service) GetMovieCollection(
	ctx context.Context,
	movieID int,
) ([]MovieCollection, error) {
	collectionID, err := s.repo.GetCollectionIDByMovieID(ctx, movieID)
	if err != nil {
		return nil, err
	}

	if collectionID != 0 {
		return s.repo.GetCollectionByCollectionID(ctx, collectionID)
	}

	rawMovie, err := s.client.TMDBRequest(
		"GET",
		fmt.Sprintf("/movie/%d?language=en-US&append_to_response=keywords,credits", movieID),
		nil,
	)
	if err != nil {
		return nil, err
	}

	belongsTo, ok := rawMovie["belongs_to_collection"].(map[string]interface{})
	if !ok || belongsTo == nil {
		return nil, nil
	}

	collectionID = int(belongsTo["id"].(float64))

	rawCollection, err := s.client.TMDBRequest(
		"GET",
		fmt.Sprintf("/collection/%d?language=en-US", collectionID),
		nil,
	)
	if err != nil {
		return nil, err
	}

	collection := NormalizeTMDBMovieCollection(rawCollection, collectionID)

	if err := s.repo.InsertMovieCollectionBatch(ctx, collection); err != nil {
		log.Printf("best-effort InsertMovieCollectionBatch failed for collection %d: %v", collectionID, err)
	}

	return collection, nil
}

func (s *Service) GetTrendingMovies() (interface{}, error) {
	endpoint := "/trending/movie/week?language=en-US"
	trending, err := s.client.TMDBRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	return trending, nil
}

func (s *Service) GetUpcomingMovies() (interface{}, error) {
	endpoint := "/movie/upcoming?language=en-US&page=1"
	upcoming, err := s.client.TMDBRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	return upcoming, nil
}

func (s *Service) GetPopularMovies() (interface{}, error) {
	endpoint := "/movie/popular?language=en-US&page=1"
	upcoming, err := s.client.TMDBRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	return upcoming, nil
}

func (s *Service) GetTopRatedMovies() (interface{}, error) {
	endpoint := "/movie/top_rated?language=en-US&page=1"
	upcoming, err := s.client.TMDBRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	return upcoming, nil
}

func (s *Service) GetMovieList(listType string) (interface{}, error) {
	var endpoint string

	switch listType {
	case "trending":
		endpoint = "/trending/movie/week?language=en-US"
	case "upcoming":
		endpoint = "/movie/upcoming?language=en-US&page=1"
	case "popular":
		endpoint = "/movie/popular?language=en-US&page=1"
	case "top_rated":
		endpoint = "/movie/top_rated?language=en-US&page=1"
	default:
		return nil, fmt.Errorf("invalid list type")
	}

	return s.client.TMDBRequest("GET", endpoint, nil)
}
