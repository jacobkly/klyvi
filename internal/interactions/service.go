package interactions

import (
	"context"
	"log"
	"time"

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

// SeenMarker files a title as Completed with no score, leaving an existing
// library entry alone. The tracking package's Repository satisfies it.
type SeenMarker interface {
	MarkSeen(ctx context.Context, userID uuid.UUID, mediaID int, mediaType string) error
}

type Service struct {
	repo   InteractionRepository
	media  MediaIndexResolver
	feed   FeedInvalidator
	series SeriesEnsurer
	seen   SeenMarker
	cap    *dailyCap
}

func NewService(repo InteractionRepository, media MediaIndexResolver) *Service {
	return &Service{repo: repo, media: media, cap: newDailyCap(MaxClientWritesPerDay)}
}

// WithFeedInvalidator drops the user's cached feed after every deliberate
// signal they record.
func (s *Service) WithFeedInvalidator(f FeedInvalidator) *Service {
	s.feed = f
	return s
}

// WithSeriesEnsurer caches the series behind every deliberate signal on a
// season or a series, so the signal has the show's features to roll up to.
func (s *Service) WithSeriesEnsurer(e SeriesEnsurer) *Service {
	s.series = e
	return s
}

// WithSeenMarker turns every onboarding answer into a library entry: the
// user said they have seen the title, so it is Completed, with no score.
func (s *Service) WithSeenMarker(m SeenMarker) *Service {
	s.seen = m
	return s
}

// RecordRequest is the validated body of POST /v1/interactions. The
// frontend identifies media by TMDB id + type (+ season_number for
// 'season'); the service resolves to a media_index media_id. For 'season'
// and 'tv' the TMDB id is the series id.
type RecordRequest struct {
	TMDBID       int     `json:"tmdb_id"`
	MediaType    string  `json:"media_type"`
	SeasonNumber *int    `json:"season_number"`
	Kind         string  `json:"kind"`
	Rating       *int    `json:"rating"`
	Source       *string `json:"source"`
}

// RecordFromClient is Record for a write the client asked for directly: it
// counts against the user's daily cap and returns ErrDailyCapReached past it.
func (s *Service) RecordFromClient(ctx context.Context, userID uuid.UUID, req RecordRequest) (*Interaction, error) {
	if !s.cap.take(userID, time.Now()) {
		return nil, ErrDailyCapReached
	}
	return s.Record(ctx, userID, req)
}

// Record validates and stores one interaction with no daily cap. Server-side
// writers use it directly, because an import is bounded by its file size and can
// legitimately carry thousands of ratings.
func (s *Service) Record(ctx context.Context, userID uuid.UUID, req RecordRequest) (*Interaction, error) {
	if req.TMDBID <= 0 {
		return nil, invalid("tmdb_id is required and must be positive")
	}
	if err := validateMediaType(req.MediaType); err != nil {
		return nil, err
	}
	if req.MediaType == MediaTypeSeason && req.SeasonNumber == nil {
		return nil, invalid("season_number is required for media_type=season")
	}
	if req.MediaType != MediaTypeSeason && req.SeasonNumber != nil {
		return nil, invalid("season_number must be omitted for media_type=%s", req.MediaType)
	}
	if err := validateKind(req.Kind); err != nil {
		return nil, err
	}
	if req.Kind == KindRated && req.Rating == nil {
		return nil, invalid("rating is required when kind=rated")
	}
	if req.Rating != nil && (*req.Rating < 0 || *req.Rating > 100) {
		return nil, invalid("rating must be between 0 and 100")
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
	// Best effort: the answer stands as a feed signal either way, and the
	// backfill command catches up an entry that failed here.
	if s.seen != nil && isOnboardingAnswer(req) {
		if err := s.seen.MarkSeen(ctx, userID, mediaID, req.MediaType); err != nil {
			log.Printf("best-effort MarkSeen failed for %s=%d: %v", req.MediaType, req.TMDBID, err)
		}
	}
	// Impressions and clicks fire while the user browses the feed and barely
	// move it, so they leave the cached feed alone until it expires. They
	// also only name candidates the catalog already holds.
	if req.Kind == KindImpression || req.Kind == KindClicked {
		return e, nil
	}
	if s.feed != nil {
		s.feed.Invalidate(userID)
	}
	// Best effort: the signal is the user's and stands without the catalog.
	if s.series != nil && (req.MediaType == MediaTypeSeason || req.MediaType == MediaTypeTV) {
		if err := s.series.EnsureSeries(ctx, req.TMDBID); err != nil {
			log.Printf("best-effort EnsureSeries failed for tv=%d: %v", req.TMDBID, err)
		}
	}
	return e, nil
}

// MaxBatchSize caps a single batch. A screenful of impressions is well under
// this; the cap stops a client from turning one request into an unbounded
// write.
const MaxBatchSize = 25

// BatchResult reports how a batch fared. Not all-or-nothing: a malformed row
// is rejected while the good rows still land, so one bad impression cannot
// cost a whole screenful of real signal.
type BatchResult struct {
	Accepted int `json:"accepted"`
	Rejected int `json:"rejected"`
}

// RecordBatch records many interactions in one call. Each row is validated
// and written independently via RecordFromClient; a row that fails, or lands
// past the daily cap, counts toward Rejected and does not abort the rest.
func (s *Service) RecordBatch(ctx context.Context, userID uuid.UUID, reqs []RecordRequest) (*BatchResult, error) {
	if len(reqs) == 0 {
		return nil, invalid("batch must contain at least one interaction")
	}
	if len(reqs) > MaxBatchSize {
		return nil, invalid("batch too large: %d exceeds the limit of %d", len(reqs), MaxBatchSize)
	}

	var res BatchResult
	for _, req := range reqs {
		if _, err := s.RecordFromClient(ctx, userID, req); err != nil {
			res.Rejected++
			continue
		}
		res.Accepted++
	}
	return &res, nil
}

func (s *Service) ListByUser(ctx context.Context, userID uuid.UUID, sinceDays int) ([]Interaction, error) {
	return s.repo.ListByUser(ctx, userID, sinceDays)
}

func (s *Service) CountByUser(ctx context.Context, userID uuid.UUID) (int, error) {
	return s.repo.CountByUser(ctx, userID)
}

// isOnboardingAnswer reports whether a request is an onboarding answer that
// says the user has seen the title. Unseen titles send nothing, and a
// series is never tracked, only its seasons.
func isOnboardingAnswer(req RecordRequest) bool {
	return req.Kind == KindRated &&
		req.Source != nil && *req.Source == SourceOnboarding &&
		req.MediaType != MediaTypeTV
}

func validateMediaType(mt string) error {
	switch mt {
	case MediaTypeMovie, MediaTypeSeason, MediaTypeTV:
		return nil
	default:
		return invalid("invalid media_type %q", mt)
	}
}

func validateKind(k string) error {
	switch k {
	case KindLogged, KindRated, KindDismissed, KindSaved, KindImpression, KindClicked:
		return nil
	default:
		return invalid("invalid kind %q", k)
	}
}

func validateSource(s string) error {
	switch s {
	case SourceSearch, SourceDetail, SourceFeed, SourceOnboarding:
		return nil
	default:
		return invalid("invalid source %q", s)
	}
}
