package tracking

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// TrackingRepository is the persistence surface the service depends on. Lives
// in the consumer package per repo convention.
type TrackingRepository interface {
	EnsureMediaIndexAndGetID(ctx context.Context, tmdbID int, mediaType string, seasonNumber *int) (int, error)
	AddEntry(ctx context.Context, userID uuid.UUID, mediaID int, mediaType string, status *string, score *int, episodeProgress *int, notes *string) (*Entry, error)
	GetEntry(ctx context.Context, userID uuid.UUID, mediaID int) (*Entry, error)
	UpdateEntry(ctx context.Context, userID uuid.UUID, mediaID int, patch UpdatePatch) (*Entry, error)
	DeleteEntry(ctx context.Context, userID uuid.UUID, mediaID int) (bool, error)
	ListByUser(ctx context.Context, userID uuid.UUID, filters ListFilters) ([]Entry, error)
}

type Service struct {
	repo TrackingRepository
}

func NewService(repo TrackingRepository) *Service {
	return &Service{repo: repo}
}

// AddRequest is the validated input for POST /v1/tracking. TMDBID +
// MediaType (+ SeasonNumber for 'season') identify the media; the service
// resolves these to an internal media_id via media_index.
type AddRequest struct {
	TMDBID          int     `json:"tmdb_id"`
	MediaType       string  `json:"media_type"`
	SeasonNumber    *int    `json:"season_number"`
	Status          *string `json:"status"`
	Score           *int    `json:"score"`
	EpisodeProgress *int    `json:"episode_progress"`
	Notes           *string `json:"notes"`
}

// UpdateRequest is the validated input for PATCH /v1/tracking/{media_id}.
// Any nil field is a "no change" sentinel.
type UpdateRequest struct {
	Status          *string `json:"status"`
	Score           *int    `json:"score"`
	EpisodeProgress *int    `json:"episode_progress"`
	Notes           *string `json:"notes"`
}

// Add validates the request, ensures the media_index row, and inserts the
// tracking entry. If the user already has this media tracked, the existing
// entry is returned unchanged (idempotent — frontend can call POST without
// first checking).
func (s *Service) Add(ctx context.Context, userID uuid.UUID, req AddRequest) (*Entry, error) {
	if err := validateMediaType(req.MediaType); err != nil {
		return nil, err
	}
	if req.MediaType == MediaTypeSeason && req.SeasonNumber == nil {
		return nil, fmt.Errorf("season_number is required for media_type=season")
	}
	if req.MediaType == MediaTypeMovie && req.SeasonNumber != nil {
		return nil, fmt.Errorf("season_number must be omitted for media_type=movie")
	}
	if req.TMDBID <= 0 {
		return nil, fmt.Errorf("tmdb_id is required and must be positive")
	}
	if err := validateStatus(req.Status); err != nil {
		return nil, err
	}
	if err := validateScore(req.Score); err != nil {
		return nil, err
	}

	mediaID, err := s.repo.EnsureMediaIndexAndGetID(ctx, req.TMDBID, req.MediaType, req.SeasonNumber)
	if err != nil {
		return nil, err
	}

	return s.repo.AddEntry(ctx, userID, mediaID, req.MediaType, req.Status, req.Score, req.EpisodeProgress, req.Notes)
}

// Update applies a patch to an existing entry. Returns nil if no entry
// matches (user, mediaID).
func (s *Service) Update(ctx context.Context, userID uuid.UUID, mediaID int, req UpdateRequest) (*Entry, error) {
	if err := validateStatus(req.Status); err != nil {
		return nil, err
	}
	if err := validateScore(req.Score); err != nil {
		return nil, err
	}

	return s.repo.UpdateEntry(ctx, userID, mediaID, UpdatePatch{
		Status:          req.Status,
		Score:           req.Score,
		EpisodeProgress: req.EpisodeProgress,
		Notes:           req.Notes,
	})
}

func (s *Service) Delete(ctx context.Context, userID uuid.UUID, mediaID int) (bool, error) {
	return s.repo.DeleteEntry(ctx, userID, mediaID)
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, filters ListFilters) ([]Entry, error) {
	if filters.MediaType != nil {
		if err := validateMediaType(*filters.MediaType); err != nil {
			return nil, err
		}
	}
	if filters.Status != nil {
		s := filters.Status
		if err := validateStatus(s); err != nil {
			return nil, err
		}
	}
	return s.repo.ListByUser(ctx, userID, filters)
}

func validateMediaType(mt string) error {
	switch mt {
	case MediaTypeMovie, MediaTypeSeason:
		return nil
	default:
		return fmt.Errorf("invalid media_type %q (must be 'movie' or 'season')", mt)
	}
}

func validateStatus(s *string) error {
	if s == nil {
		return nil
	}
	switch *s {
	case StatusWatching, StatusPlanning, StatusCompleted, StatusRewatching, StatusPaused, StatusDropped:
		return nil
	default:
		return fmt.Errorf("invalid status %q", *s)
	}
}

func validateScore(s *int) error {
	if s == nil {
		return nil
	}
	if *s < 0 || *s > 100 {
		return fmt.Errorf("score must be between 0 and 100, got %d", *s)
	}
	return nil
}
