package tracking

import (
	"context"
	"log"

	"github.com/google/uuid"
)

// TrackingRepository is the persistence surface the service depends on. Lives
// in the consumer package per repo convention.
type TrackingRepository interface {
	EnsureMediaIndexAndGetID(ctx context.Context, tmdbID int, mediaType string, seasonNumber *int) (int, error)
	AddEntry(ctx context.Context, userID uuid.UUID, mediaID int, p AddParams) (*Entry, error)
	GetEntry(ctx context.Context, userID uuid.UUID, mediaID int) (*Entry, error)
	GetEntryByTMDB(ctx context.Context, userID uuid.UUID, tmdbID int, mediaType string, seasonNumber *int) (*Entry, error)
	UpdateEntry(ctx context.Context, userID uuid.UUID, mediaID int, patch UpdatePatch) (*Entry, error)
	DeleteEntry(ctx context.Context, userID uuid.UUID, mediaID int) (bool, error)
	ListByUser(ctx context.Context, userID uuid.UUID, filters ListFilters) ([]Entry, error)
	ResetScoresByMediaType(ctx context.Context, userID uuid.UUID, mediaType string) (int64, error)
	DeleteByMediaType(ctx context.Context, userID uuid.UUID, mediaType string) (int64, error)
}

// FeedInvalidator drops a user's cached recommendations. The reco feed cache
// satisfies it.
type FeedInvalidator interface {
	Invalidate(userID uuid.UUID)
}

// SeriesEnsurer caches a series with its keywords and credits, and its 'tv'
// media_index row. The tv service satisfies it.
type SeriesEnsurer interface {
	EnsureSeries(ctx context.Context, tvID int) error
}

type Service struct {
	repo   TrackingRepository
	feed   FeedInvalidator
	series SeriesEnsurer
}

func NewService(repo TrackingRepository) *Service {
	return &Service{repo: repo}
}

// WithFeedInvalidator drops the user's cached feed after every write that
// changes their list, since tracked items leave the feed.
func (s *Service) WithFeedInvalidator(f FeedInvalidator) *Service {
	s.feed = f
	return s
}

// WithSeriesEnsurer caches the parent series of every season the user adds,
// so what they think of the season can roll up to the show.
func (s *Service) WithSeriesEnsurer(e SeriesEnsurer) *Service {
	s.series = e
	return s
}

func (s *Service) listChanged(userID uuid.UUID) {
	if s.feed != nil {
		s.feed.Invalidate(userID)
	}
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
	Favorite        *bool   `json:"favorite"`
}

// UpdateRequest is the validated input for PATCH /v1/tracking/{media_id}.
// Any nil field is a "no change" sentinel.
type UpdateRequest struct {
	Status          *string `json:"status"`
	Score           *int    `json:"score"`
	EpisodeProgress *int    `json:"episode_progress"`
	Notes           *string `json:"notes"`
	Favorite        *bool   `json:"favorite"`
}

// Add validates the request, ensures the media_index row, and inserts the
// tracking entry. If the user already has this media tracked, the existing
// entry is returned unchanged (idempotent — frontend can call POST without
// first checking).
func (s *Service) Add(ctx context.Context, userID uuid.UUID, req AddRequest) (*Entry, error) {
	if err := validateIdentity(req.TMDBID, req.MediaType, req.SeasonNumber); err != nil {
		return nil, err
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

	entry, err := s.repo.AddEntry(ctx, userID, mediaID, AddParams{
		MediaType:       req.MediaType,
		Status:          req.Status,
		Score:           req.Score,
		EpisodeProgress: req.EpisodeProgress,
		Notes:           req.Notes,
		Favorite:        req.Favorite,
	})
	if err != nil {
		return nil, err
	}
	s.listChanged(userID)
	// Best effort: the entry is the user's and stands without the catalog.
	if s.series != nil && req.MediaType == MediaTypeSeason {
		if err := s.series.EnsureSeries(ctx, req.TMDBID); err != nil {
			log.Printf("best-effort EnsureSeries failed for tv=%d: %v", req.TMDBID, err)
		}
	}
	return entry, nil
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

	entry, err := s.repo.UpdateEntry(ctx, userID, mediaID, UpdatePatch{
		Status:          req.Status,
		Score:           req.Score,
		EpisodeProgress: req.EpisodeProgress,
		Notes:           req.Notes,
		Favorite:        req.Favorite,
	})
	if err != nil {
		return nil, err
	}
	if entry != nil {
		s.listChanged(userID)
	}
	return entry, nil
}

func (s *Service) Delete(ctx context.Context, userID uuid.UUID, mediaID int) (bool, error) {
	deleted, err := s.repo.DeleteEntry(ctx, userID, mediaID)
	if err != nil {
		return false, err
	}
	if deleted {
		s.listChanged(userID)
	}
	return deleted, nil
}

// LookupRequest names one catalog item by its TMDB identity, the way the
// client knows it on a detail page.
type LookupRequest struct {
	TMDBID       int
	MediaType    string
	SeasonNumber *int
}

// Lookup returns the user's entry for one catalog item, or nil when they do
// not track it, so a detail page never downloads the whole library.
func (s *Service) Lookup(ctx context.Context, userID uuid.UUID, req LookupRequest) (*Entry, error) {
	if err := validateIdentity(req.TMDBID, req.MediaType, req.SeasonNumber); err != nil {
		return nil, err
	}
	return s.repo.GetEntryByTMDB(ctx, userID, req.TMDBID, req.MediaType, req.SeasonNumber)
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

// ResetScores nulls every score in one media-type scope. Irreversible;
// the handler owns confirming intent, this owns doing exactly what was
// asked and reporting the count.
func (s *Service) ResetScores(ctx context.Context, userID uuid.UUID, mediaType string) (int64, error) {
	if err := validateMediaType(mediaType); err != nil {
		return 0, err
	}
	n, err := s.repo.ResetScoresByMediaType(ctx, userID, mediaType)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		s.listChanged(userID)
	}
	return n, nil
}

// DeleteList removes every tracking row in one media-type scope.
// Irreversible.
func (s *Service) DeleteList(ctx context.Context, userID uuid.UUID, mediaType string) (int64, error) {
	if err := validateMediaType(mediaType); err != nil {
		return 0, err
	}
	n, err := s.repo.DeleteByMediaType(ctx, userID, mediaType)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		s.listChanged(userID)
	}
	return n, nil
}

func validateMediaType(mt string) error {
	switch mt {
	case MediaTypeMovie, MediaTypeSeason:
		return nil
	default:
		return invalid("invalid media_type %q (must be 'movie' or 'season')", mt)
	}
}

// validateIdentity checks a TMDB identity: a season needs its number and a
// movie must not carry one.
func validateIdentity(tmdbID int, mediaType string, seasonNumber *int) error {
	if err := validateMediaType(mediaType); err != nil {
		return err
	}
	if mediaType == MediaTypeSeason && seasonNumber == nil {
		return invalid("season_number is required for media_type=season")
	}
	if mediaType == MediaTypeMovie && seasonNumber != nil {
		return invalid("season_number must be omitted for media_type=movie")
	}
	if tmdbID <= 0 {
		return invalid("tmdb_id is required and must be positive")
	}
	return nil
}

func validateStatus(s *string) error {
	if s == nil {
		return nil
	}
	switch *s {
	case StatusWatching, StatusPlanning, StatusCompleted, StatusRewatching, StatusPaused, StatusDropped:
		return nil
	default:
		return invalid("invalid status %q", *s)
	}
}

func validateScore(s *int) error {
	if s == nil {
		return nil
	}
	if *s < 0 || *s > 100 {
		return invalid("score must be between 0 and 100, got %d", *s)
	}
	return nil
}
