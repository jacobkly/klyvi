package imports

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/google/uuid"

	"klyvi-api/internal/interactions"
	"klyvi-api/internal/platform/tmdb"
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

// SeriesCatalog gives the episode count of each season of a show, keyed by
// season number, caching the series on the way. Tracking a season needs to
// know the season exists and how many episodes finish it. tv.Service
// satisfies it.
type SeriesCatalog interface {
	SeasonEpisodeCounts(ctx context.Context, tvID int) (map[int]int, error)
}

type Service struct {
	repo     Repo
	client   TMDBClient
	movies   MovieCacher
	tracking TrackAdder
	signals  SignalRecorder
	series   SeriesCatalog

	// workers holds one slot per running import (importWorkers in all).
	workers chan struct{}
}

func NewService(repo Repo, client TMDBClient, mc MovieCacher, tr TrackAdder, sr SignalRecorder) *Service {
	return &Service{
		repo: repo, client: client, movies: mc, tracking: tr, signals: sr,
		workers: make(chan struct{}, importWorkers),
	}
}

// WithSeries lets imports track shows. Without it, show rows are unmatched.
func (s *Service) WithSeries(sc SeriesCatalog) *Service {
	s.series = sc
	return s
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
// waits. Unmatched rows are counted and listed, never dropped silently, and
// a single unmatched or unwritable row does not fail the job.
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
	counts := &episodeCounts{catalog: s.series, known: map[int]map[int]int{}}

	for i, row := range rows {
		// An episode or an unknown type is neither, and counts as unmatched.
		var ok bool
		switch {
		case row.isFilm():
			ok = s.importFilm(ctx, userID, row)
		case row.isShow():
			ok = s.importShow(ctx, userID, row, counts)
		}
		if ok {
			matched++
		} else {
			unmatched++
			// Capture it (bounded) so the client can surface it and let the
			// user find it manually.
			if len(unmatchedTitles) < maxUnmatchedTitles {
				unmatchedTitles = append(unmatchedTitles, unmatchedTitle(row))
			}
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

// unmatchedTitle names a row for the unmatched list. A season says which,
// so the user knows what to look for.
func unmatchedTitle(row Row) UnmatchedTitle {
	title := row.Title
	if row.isShow() && row.Season > 0 {
		title = fmt.Sprintf("%s, season %d", row.Title, row.Season)
	}
	return UnmatchedTitle{Title: title, Year: row.Year}
}

// importFilm matches a film row, by its TMDB id when the file gives one or
// by search, and writes it. Reports whether it landed.
func (s *Service) importFilm(ctx context.Context, userID uuid.UUID, row Row) bool {
	tmdbID := row.TMDBID
	if tmdbID <= 0 {
		tmdbID = matchRow(ctx, s.client, row)
	}
	if tmdbID == 0 {
		return false
	}
	return s.writeMatch(ctx, userID, tmdbID, row.Score)
}

// importShow tracks one season of a show, or records a rating of the whole
// show when the row names no season. Only a sure match is written: the show
// comes from its TMDB id or a strong search match, and the season must be
// one TMDB lists for it. Reports whether it landed.
func (s *Service) importShow(ctx context.Context, userID uuid.UUID, row Row, counts *episodeCounts) bool {
	if s.series == nil {
		return false
	}
	// A whole show with no rating has nothing to record, since tracking is
	// per season. Checked first so it costs no search.
	if row.Season <= 0 && row.Score == nil {
		return false
	}
	tvID := row.TMDBID
	if tvID <= 0 {
		tvID = matchShow(ctx, s.client, row)
	}
	if tvID == 0 {
		return false
	}
	seasons, ok := counts.get(ctx, tvID)
	if !ok {
		return false
	}

	if row.Season <= 0 {
		if err := s.recordRating(ctx, userID, tvID, interactions.MediaTypeTV, nil, *row.Score); err != nil {
			log.Printf("import: signal record for tv %d failed: %v", tvID, err)
			return false
		}
		return true
	}

	episodes, ok := seasons[row.Season]
	if !ok {
		return false
	}
	// A season is finished unless the file counts fewer watched episodes
	// than it has. A file that does not count them lists what was seen.
	status, progress := tracking.StatusCompleted, episodes
	if row.Watched > 0 && row.Watched < episodes {
		status, progress = tracking.StatusWatching, row.Watched
	}
	season := row.Season
	req := tracking.AddRequest{
		TMDBID:       tvID,
		MediaType:    tracking.MediaTypeSeason,
		SeasonNumber: &season,
		Status:       &status,
		Score:        row.Score,
	}
	if progress > 0 {
		req.EpisodeProgress = &progress
	}
	if _, err := s.tracking.Add(ctx, userID, req); err != nil {
		log.Printf("import: track add for tv %d season %d failed: %v", tvID, season, err)
		return false
	}
	if row.Score != nil {
		if err := s.recordRating(ctx, userID, tvID, interactions.MediaTypeSeason, &season, *row.Score); err != nil {
			log.Printf("import: signal record for tv %d season %d failed: %v", tvID, season, err)
		}
	}
	return true
}

// episodeCounts reads each show's seasons from the catalog once per import,
// failures included, so a show's many rows cost one lookup.
type episodeCounts struct {
	catalog SeriesCatalog
	known   map[int]map[int]int // nil for a show the catalog could not give
}

func (c *episodeCounts) get(ctx context.Context, tvID int) (map[int]int, bool) {
	if seasons, seen := c.known[tvID]; seen {
		return seasons, seasons != nil
	}
	seasons, err := c.catalog.SeasonEpisodeCounts(ctx, tvID)
	if err != nil {
		log.Printf("import: seasons of tv %d unavailable: %v", tvID, err)
		seasons = nil
	}
	c.known[tvID] = seasons
	return seasons, seasons != nil
}

// writeMatch records a matched title as a completed tracking entry plus a
// rated interaction (when the row carried a score) so the recommender learns
// from the imported history. Reports whether the write succeeded.
func (s *Service) writeMatch(ctx context.Context, userID uuid.UUID, tmdbID int, score *int) bool {
	// Ensure the film's catalog detail is cached first, so it renders in the
	// library and carries genres/keywords for the recommender. Best-effort: a
	// rare detail-fetch failure should not lose the watched record — the row
	// still lands and a later backfill can fill the metadata. A film TMDB
	// does not have, from a wrong id in the file, is unmatched instead.
	if err := s.movies.EnsureCached(ctx, tmdbID); tmdb.IsStatus(err, http.StatusNotFound) {
		return false
	} else if err != nil {
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
		if err := s.recordRating(ctx, userID, tmdbID, interactions.MediaTypeMovie, nil, *score); err != nil {
			log.Printf("import: signal record for tmdb %d failed: %v", tmdbID, err)
		}
	}
	return true
}

// recordRating writes an imported rating as a rated interaction.
func (s *Service) recordRating(ctx context.Context, userID uuid.UUID, tmdbID int, mediaType string, season *int, score int) error {
	source := interactions.SourceOnboarding // closest source enum for a backfill
	_, err := s.signals.Record(ctx, userID, interactions.RecordRequest{
		TMDBID:       tmdbID,
		MediaType:    mediaType,
		SeasonNumber: season,
		Kind:         interactions.KindRated,
		Rating:       &score,
		Source:       &source,
	})
	return err
}
