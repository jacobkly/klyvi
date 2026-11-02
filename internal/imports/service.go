package imports

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"

	"klyvi-api/internal/interactions"
	"klyvi-api/internal/tracking"
)

// Repo is the persistence surface the import service needs.
type Repo interface {
	Create(ctx context.Context, userID uuid.UUID, source string) (*Import, error)
	Get(ctx context.Context, userID, id uuid.UUID) (*Import, error)
	SetStatus(ctx context.Context, id uuid.UUID, status string, errMsg *string) error
	SetProgress(ctx context.Context, id uuid.UUID, total, matched, unmatched int) error
	SetUnmatchedTitles(ctx context.Context, id uuid.UUID, titles UnmatchedTitles) error
}

// maxUnmatchedTitles caps how many unmatched titles are retained for the
// client to display. The `unmatched` count is always exact; only the listed
// titles are bounded, so a pathological import can't bloat the row.
const maxUnmatchedTitles = 200

// TrackAdder writes an imported title into the tracking list.
type TrackAdder interface {
	Add(ctx context.Context, userID uuid.UUID, req tracking.AddRequest) (*tracking.Entry, error)
}

// SignalRecorder writes a rated interaction so the recommender learns from
// imported history.
type SignalRecorder interface {
	Record(ctx context.Context, userID uuid.UUID, req interactions.RecordRequest) (*interactions.Interaction, error)
}

// MovieCacher ensures a matched film's catalog detail (title, poster, genres,
// keywords) is persisted. Without this a film that isn't already in the cache
// tracks as an "Untitled" row with no reco features. Behaviour-named per repo
// convention; movies.Service satisfies it.
type MovieCacher interface {
	EnsureCached(ctx context.Context, tmdbID int) error
}

type Service struct {
	repo     Repo
	client   TMDBClient
	movies   MovieCacher
	tracking TrackAdder
	signals  SignalRecorder

	// workers holds one slot per running import (importWorkers in all).
	workers chan struct{}
}

func NewService(repo Repo, client TMDBClient, mc MovieCacher, tr TrackAdder, sr SignalRecorder) *Service {
	return &Service{
		repo: repo, client: client, movies: mc, tracking: tr, signals: sr,
		workers: make(chan struct{}, importWorkers),
	}
}

// Start parses the file, creates a job and hands it to the worker pool,
// returning the pending record immediately. The file is already read into
// memory by the handler (capped at maxImportBytes). Parsing here, not in the
// worker, lets a broken or oversized file be refused before any job exists.
func (s *Service) Start(ctx context.Context, userID uuid.UUID, source string, data []byte) (*Import, error) {
	if !ValidSource(source) {
		return nil, fmt.Errorf("unsupported source %q", source)
	}
	rows, err := parseImport(Source(source), data)
	if err != nil {
		return nil, fmt.Errorf("could not parse import file: %w", err)
	}
	if len(rows) > maxImportRows {
		return nil, ErrTooManyRows
	}

	imp, err := s.repo.Create(ctx, userID, source)
	if err != nil {
		return nil, err
	}

	// Detach from the request context — it is cancelled when the response
	// returns, and the worker must outlive it.
	go s.run(context.Background(), imp.ID, userID, rows)

	return imp, nil
}

// Get returns a job's current state (with staleness applied by the repo).
func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (*Import, error) {
	return s.repo.Get(ctx, userID, id)
}

// run is the worker: wait for a free slot → match each row → write tracking
// + a rated interaction → record counts. The job stays pending while it
// waits. Unmatched rows are counted, never dropped silently, and a single
// unmatched or unwritable row does not fail the job.
func (s *Service) run(ctx context.Context, id, userID uuid.UUID, rows []Row) {
	s.workers <- struct{}{}
	defer func() { <-s.workers }()

	if err := s.repo.SetStatus(ctx, id, StatusRunning, nil); err != nil {
		log.Printf("import %s: could not mark running: %v", id, err)
		return
	}

	total := len(rows)
	var matched, unmatched int
	var unmatchedTitles UnmatchedTitles
	_ = s.repo.SetProgress(ctx, id, total, 0, 0)

	for i, row := range rows {
		tmdbID := matchRow(ctx, s.client, row)
		if tmdbID == 0 {
			unmatched++
			// A title we could not resolve at all — capture it (bounded) so
			// the client can surface it and let the user find it manually.
			if len(unmatchedTitles) < maxUnmatchedTitles {
				unmatchedTitles = append(unmatchedTitles, UnmatchedTitle{Title: row.Title, Year: row.Year})
			}
		} else if s.writeMatch(ctx, userID, tmdbID, row.Score) {
			matched++
		} else {
			unmatched++
		}

		// Report progress periodically so a poller sees movement and the
		// staleness clock keeps resetting on a long import.
		if (i+1)%10 == 0 {
			_ = s.repo.SetProgress(ctx, id, total, matched, unmatched)
		}
	}

	_ = s.repo.SetProgress(ctx, id, total, matched, unmatched)
	_ = s.repo.SetUnmatchedTitles(ctx, id, unmatchedTitles)
	if err := s.repo.SetStatus(ctx, id, StatusDone, nil); err != nil {
		log.Printf("import %s: could not mark done: %v", id, err)
	}
}

// writeMatch records a matched title as a completed tracking entry plus a
// rated interaction (when the row carried a score) so the recommender learns
// from the imported history. Reports whether the write succeeded.
func (s *Service) writeMatch(ctx context.Context, userID uuid.UUID, tmdbID int, score *int) bool {
	// Ensure the film's catalog detail is cached first, so it renders in the
	// library and carries genres/keywords for the recommender. Best-effort: a
	// rare detail-fetch failure should not lose the watched record — the row
	// still lands and a later backfill can fill the metadata.
	if err := s.movies.EnsureCached(ctx, tmdbID); err != nil {
		log.Printf("import: cache movie %d failed (entry will lack metadata until re-fetched): %v", tmdbID, err)
	}

	completed := tracking.StatusCompleted
	if _, err := s.tracking.Add(ctx, userID, tracking.AddRequest{
		TMDBID:    tmdbID,
		MediaType: tracking.MediaTypeMovie,
		Status:    &completed,
		Score:     score,
	}); err != nil {
		log.Printf("import: track add for tmdb %d failed: %v", tmdbID, err)
		return false
	}

	// A rated interaction only makes sense with a score; a watched-but-
	// unrated title still lands as a tracking entry above.
	if score != nil {
		source := interactions.SourceOnboarding // closest source enum for a backfill
		if _, err := s.signals.Record(ctx, userID, interactions.RecordRequest{
			TMDBID:    tmdbID,
			MediaType: interactions.MediaTypeMovie,
			Kind:      interactions.KindRated,
			Rating:    score,
			Source:    &source,
		}); err != nil {
			log.Printf("import: signal record for tmdb %d failed: %v", tmdbID, err)
		}
	}
	return true
}
