package interactions

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// InteractionRepository is the persistence surface this service depends on.
type InteractionRepository interface {
	Record(ctx context.Context, e *Interaction) error
	ListByUser(ctx context.Context, userID uuid.UUID, sinceDays int) ([]Interaction, error)
	CountByUser(ctx context.Context, userID uuid.UUID) (int, error)
}

// MediaIndexResolver is the spine-side dependency: resolve (tmdb_id,
// media_type, season_number) into the internal media_index media_id. The
// tracking package's Repository satisfies this interface.
type MediaIndexResolver interface {
	EnsureMediaIndexAndGetID(ctx context.Context, tmdbID int, mediaType string, seasonNumber *int) (int, error)
}

type Service struct {
	repo  InteractionRepository
	media MediaIndexResolver
}

func NewService(repo InteractionRepository, media MediaIndexResolver) *Service {
	return &Service{repo: repo, media: media}
}

// RecordRequest is the validated body of POST /v1/interactions. The
// frontend identifies media by TMDB id + type (+ season_number for
// 'season'); the service resolves to a media_index media_id.
type RecordRequest struct {
	TMDBID       int     `json:"tmdb_id"`
	MediaType    string  `json:"media_type"`
	SeasonNumber *int    `json:"season_number"`
	Kind         string  `json:"kind"`
	Rating       *int    `json:"rating"`
	Source       *string `json:"source"`
}

func (s *Service) Record(ctx context.Context, userID uuid.UUID, req RecordRequest) (*Interaction, error) {
	if req.TMDBID <= 0 {
		return nil, fmt.Errorf("tmdb_id is required and must be positive")
	}
	if err := validateMediaType(req.MediaType); err != nil {
		return nil, err
	}
	if req.MediaType == MediaTypeSeason && req.SeasonNumber == nil {
		return nil, fmt.Errorf("season_number is required for media_type=season")
	}
	if req.MediaType == MediaTypeMovie && req.SeasonNumber != nil {
		return nil, fmt.Errorf("season_number must be omitted for media_type=movie")
	}
	if err := validateKind(req.Kind); err != nil {
		return nil, err
	}
	if req.Kind == KindRated && req.Rating == nil {
		return nil, fmt.Errorf("rating is required when kind=rated")
	}
	if req.Rating != nil && (*req.Rating < 0 || *req.Rating > 100) {
		return nil, fmt.Errorf("rating must be between 0 and 100")
	}
	if req.Source != nil {
		if err := validateSource(*req.Source); err != nil {
			return nil, err
		}
	}

	mediaID, err := s.media.EnsureMediaIndexAndGetID(ctx, req.TMDBID, req.MediaType, req.SeasonNumber)
	if err != nil {
		return nil, err
	}

	e := &Interaction{
		UserID:    userID,
		MediaID:   mediaID,
		MediaType: req.MediaType,
		Kind:      req.Kind,
		Rating:    req.Rating,
		Source:    req.Source,
	}
	if err := s.repo.Record(ctx, e); err != nil {
		return nil, err
	}
	return e, nil
}

func (s *Service) ListByUser(ctx context.Context, userID uuid.UUID, sinceDays int) ([]Interaction, error) {
	return s.repo.ListByUser(ctx, userID, sinceDays)
}

func (s *Service) CountByUser(ctx context.Context, userID uuid.UUID) (int, error) {
	return s.repo.CountByUser(ctx, userID)
}

func validateMediaType(mt string) error {
	switch mt {
	case MediaTypeMovie, MediaTypeSeason:
		return nil
	default:
		return fmt.Errorf("invalid media_type %q", mt)
	}
}

func validateKind(k string) error {
	switch k {
	case KindLogged, KindRated, KindDismissed, KindSaved, KindImpression, KindClicked:
		return nil
	default:
		return fmt.Errorf("invalid kind %q", k)
	}
}

func validateSource(s string) error {
	switch s {
	case SourceSearch, SourceDetail, SourceFeed, SourceOnboarding:
		return nil
	default:
		return fmt.Errorf("invalid source %q", s)
	}
}
