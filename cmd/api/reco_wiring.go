package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"golang.org/x/sync/errgroup"

	"klyvi-api/internal/movies"
	"klyvi-api/internal/reco"
	"klyvi-api/internal/tv"
)

// newRecoOrchestrator wires the tier cascade to the DB-backed adapters and
// the given catalog: the database adapter, or the in-memory cachedCatalog in
// front of it. It is the one place production builds the recommender, so
// tests that measure the feed measure exactly what the server runs.
func newRecoOrchestrator(dbConn *sqlx.DB, catalog reco.CatalogRepository, cfg reco.Config, idf *reco.MediaIDF) *reco.Orchestrator {
	return reco.NewOrchestrator(
		catalog,
		newSignalAdapter(dbConn),
		newSeenAdapter(dbConn),
		cfg,
	).WithTier0(reco.NewTier0()).
		WithTier1(reco.NewTier1(cfg).WithIDF(idf)).
		WithTier2(reco.NewTier2(cfg, newProfileAdapter(dbConn)).WithIDF(idf))
}

// catalogAdapter wraps the movies and series repositories to satisfy
// reco.CatalogRepository, translating their cache rows into reco's
// MediaFeatures shape. The pool draws films and series apart. A user's
// history can hold series (seasons roll up to them), so features fall back to
// the series cache, and so do reason names, for the genres and keywords only
// series carry.
type catalogAdapter struct {
	repo   *movies.Repository
	tvRepo *tv.Repository
	names  *reasonNames
}

func newCatalogAdapter(repo *movies.Repository, tvRepo *tv.Repository) *catalogAdapter {
	return &catalogAdapter{repo: repo, tvRepo: tvRepo, names: newReasonNames(repo, tvRepo)}
}

func (a *catalogAdapter) SampleMovieCandidates(ctx context.Context, limit int) ([]reco.Candidate, error) {
	rows, err := a.repo.ListCandidatesForReco(ctx, limit)
	if err != nil {
		return nil, err
	}
	return rowsToCandidates(rows), nil
}

// CandidatesByMediaIDs looks the ids up as films, then sends the ids no film
// matched to the series cache. A films-only history makes one query.
func (a *catalogAdapter) CandidatesByMediaIDs(ctx context.Context, mediaIDs []int) ([]reco.Candidate, error) {
	rows, err := a.repo.CandidatesByMediaIDs(ctx, mediaIDs)
	if err != nil {
		return nil, err
	}
	cands := rowsToCandidates(rows)

	found := make(map[int]bool, len(cands))
	for _, c := range cands {
		found[c.MediaID] = true
	}
	var rest []int
	for _, id := range mediaIDs {
		if !found[id] {
			rest = append(rest, id)
		}
	}
	if len(rest) == 0 {
		return cands, nil
	}
	seriesRows, err := a.tvRepo.CandidatesByMediaIDs(ctx, rest)
	if err != nil {
		return nil, err
	}
	return append(cands, seriesToCandidates(seriesRows)...), nil
}

func (a *catalogAdapter) CandidatesByTaste(ctx context.Context, genreIDs, keywordIDs []int, minVotes, limit int) ([]reco.Candidate, error) {
	rows, err := a.repo.ListCandidatesByTaste(ctx, genreIDs, keywordIDs, minVotes, limit)
	if err != nil {
		return nil, err
	}
	return rowsToCandidates(rows), nil
}

func (a *catalogAdapter) SampleSeriesCandidates(ctx context.Context, limit int) ([]reco.Candidate, error) {
	rows, err := a.tvRepo.ListCandidatesForReco(ctx, limit)
	if err != nil {
		return nil, err
	}
	return seriesToCandidates(rows), nil
}

func (a *catalogAdapter) SeriesCandidatesByTaste(ctx context.Context, genreIDs, keywordIDs []int, minVotes, limit int) ([]reco.Candidate, error) {
	rows, err := a.tvRepo.ListCandidatesByTaste(ctx, genreIDs, keywordIDs, minVotes, limit)
	if err != nil {
		return nil, err
	}
	return seriesToCandidates(rows), nil
}

// LookupReasonNames satisfies reco.CatalogRepository from the in-process
// name cache, which only goes to the database for ids it has never seen.
func (a *catalogAdapter) LookupReasonNames(ctx context.Context, genreIDs, keywordIDs []int) (map[int]string, map[int]string, error) {
	return a.names.Lookup(ctx, genreIDs, keywordIDs)
}

// CardDetails satisfies reco.CatalogRepository with one query over the
// ranked films and one over the ranked series, side by side. Either repo
// skips its query when it has no ids.
func (a *catalogAdapter) CardDetails(ctx context.Context, movieIDs, seriesIDs []int) (map[int]reco.CardDetails, error) {
	var (
		films  []movies.CardDetails
		series []tv.CardDetails
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		films, err = a.repo.CardDetailsByMediaIDs(gctx, movieIDs)
		return err
	})
	g.Go(func() (err error) {
		series, err = a.tvRepo.CardDetailsByMediaIDs(gctx, seriesIDs)
		return err
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}

	out := make(map[int]reco.CardDetails, len(films)+len(series))
	for _, r := range films {
		out[r.MediaID] = reco.CardDetails{Overview: r.Overview, Runtime: r.Runtime, Genres: r.Genres}
	}
	for _, r := range series {
		out[r.MediaID] = reco.CardDetails{Overview: r.Overview, Genres: r.Genres, SeasonCount: r.SeasonCount}
	}
	return out, nil
}

func rowsToCandidates(rows []movies.RecoCandidateRow) []reco.Candidate {
	cands := make([]reco.Candidate, 0, len(rows))
	for _, row := range rows {
		year := yearOf(row.ReleaseDate)
		feat := &reco.MediaFeatures{
			MediaID:     row.MediaID,
			MediaType:   "movie",
			GenreIDs:    row.GenreIDs,
			KeywordIDs:  row.KeywordIDs,
			Year:        year,
			VoteAverage: row.VoteAverage,
			VoteCount:   row.VoteCount,
		}
		cands = append(cands, reco.Candidate{
			MediaID:      row.MediaID,
			MediaType:    "movie",
			TMDBID:       row.MovieID,
			Title:        derefString(row.Title),
			PosterPath:   derefString(row.PosterPath),
			BackdropPath: derefString(row.BackdropPath),
			ReleaseYear:  year,
			VoteAverage:  row.VoteAverage,
			Features:     feat,
		})
	}
	return cands
}

// seriesToCandidates is rowsToCandidates for the series cache. Genres are
// read into the film genre space, so a series and a film that share a genre
// score as sharing it.
func seriesToCandidates(rows []tv.RecoCandidateRow) []reco.Candidate {
	cands := make([]reco.Candidate, 0, len(rows))
	for _, row := range rows {
		year := yearOf(row.FirstAirDate)
		feat := &reco.MediaFeatures{
			MediaID:     row.MediaID,
			MediaType:   "tv",
			GenreIDs:    reco.CanonicalTVGenres(row.GenreIDs),
			KeywordIDs:  row.KeywordIDs,
			Year:        year,
			VoteAverage: row.VoteAverage,
			VoteCount:   row.VoteCount,
		}
		cands = append(cands, reco.Candidate{
			MediaID:      row.MediaID,
			MediaType:    "tv",
			TMDBID:       row.TVID,
			Title:        derefString(row.Name),
			PosterPath:   derefString(row.PosterPath),
			BackdropPath: derefString(row.BackdropPath),
			ReleaseYear:  year,
			VoteAverage:  row.VoteAverage,
			Features:     feat,
		})
	}
	return cands
}

// derefString returns the string pointed to, or "" if the pointer is nil.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// signalAdapter wraps the DB directly. Building it on the interactions
// repo would require translating Interaction → InteractionRow per row;
// going to SQL once is simpler and avoids a redundant package dependency.
type signalAdapter struct {
	db *sqlx.DB
}

func newSignalAdapter(db *sqlx.DB) *signalAdapter { return &signalAdapter{db: db} }

// signalSelect reads a user's interactions ($1), each season carrying its
// series' media_id for the rollup (0 when the series is not indexed).
const signalSelect = `
	select i.media_id, i.media_type, i.kind, i.rating,
	       extract(epoch from (now() - i.created_at)) / 86400.0 as age_days,
	       coalesce(series.media_id, 0) as series_media_id
	from interactions i
	left join media_index season on season.media_id = i.media_id and season.media_type = 'season'
	left join media_index series on series.id = season.id and series.media_type = 'tv'
	where i.user_id = $1`

func (a *signalAdapter) ListByUser(ctx context.Context, userID uuid.UUID, sinceDays int) ([]reco.InteractionRow, error) {
	query := signalSelect + ` order by i.created_at desc`
	args := []any{userID}
	if sinceDays > 0 {
		query = signalSelect + `
			and i.created_at >= now() - make_interval(days => $2)
			order by i.created_at desc`
		args = append(args, sinceDays)
	}

	var rows []struct {
		MediaID       int     `db:"media_id"`
		MediaType     string  `db:"media_type"`
		Kind          string  `db:"kind"`
		Rating        *int    `db:"rating"`
		AgeDays       float64 `db:"age_days"`
		SeriesMediaID int     `db:"series_media_id"`
	}
	if err := a.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}

	out := make([]reco.InteractionRow, len(rows))
	for i, r := range rows {
		out[i] = reco.InteractionRow{
			MediaID:       r.MediaID,
			MediaType:     r.MediaType,
			Kind:          r.Kind,
			Rating:        r.Rating,
			AgeDays:       r.AgeDays,
			SeriesMediaID: r.SeriesMediaID,
		}
	}
	return out, nil
}

// profileAdapter persists and loads TasteProfile rows from the
// taste_profiles table. Implements reco.ProfileRepository.
type profileAdapter struct {
	db *sqlx.DB
}

func newProfileAdapter(db *sqlx.DB) *profileAdapter { return &profileAdapter{db: db} }

func (a *profileAdapter) GetProfile(ctx context.Context, userID uuid.UUID) (*reco.TasteProfile, error) {
	var row struct {
		UserID             uuid.UUID       `db:"user_id"`
		GenreWeights       json.RawMessage `db:"genre_weights"`
		KeywordWeights     json.RawMessage `db:"keyword_weights"`
		EraWeights         json.RawMessage `db:"era_weights"`
		QualitySensitivity float64         `db:"quality_sensitivity"`
		LikedCount         int             `db:"liked_count"`
		DislikedCount      int             `db:"disliked_count"`
		UpdatedAt          time.Time       `db:"updated_at"`
	}
	err := a.db.GetContext(ctx, &row,
		`select * from taste_profiles where user_id = $1`, userID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return &reco.TasteProfile{
		UserID:             row.UserID,
		GenreWeights:       intKeyMap(row.GenreWeights),
		KeywordWeights:     intKeyMap(row.KeywordWeights),
		EraWeights:         intKeyMap(row.EraWeights),
		QualitySensitivity: row.QualitySensitivity,
		LikedCount:         row.LikedCount,
		DislikedCount:      row.DislikedCount,
		UpdatedAt:          row.UpdatedAt,
	}, nil
}

func (a *profileAdapter) UpsertProfile(ctx context.Context, p *reco.TasteProfile) error {
	genreJSON, _ := json.Marshal(stringKeyMap(p.GenreWeights))
	keywordJSON, _ := json.Marshal(stringKeyMap(p.KeywordWeights))
	eraJSON, _ := json.Marshal(stringKeyMap(p.EraWeights))

	_, err := a.db.ExecContext(ctx, `
		insert into taste_profiles
			(user_id, genre_weights, keyword_weights, era_weights,
			 quality_sensitivity, liked_count, disliked_count, updated_at)
		values ($1, $2, $3, $4, $5, $6, $7, now())
		on conflict (user_id) do update set
			genre_weights = excluded.genre_weights,
			keyword_weights = excluded.keyword_weights,
			era_weights = excluded.era_weights,
			quality_sensitivity = excluded.quality_sensitivity,
			liked_count = excluded.liked_count,
			disliked_count = excluded.disliked_count,
			updated_at = now()
	`, p.UserID, genreJSON, keywordJSON, eraJSON,
		p.QualitySensitivity, p.LikedCount, p.DislikedCount)
	return err
}

// JSONB stores keys as strings. Round-trip helpers keep the in-memory
// TasteProfile typed as map[int]float64 — friendlier for math.
func stringKeyMap(m map[int]float64) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[itoa(k)] = v
	}
	return out
}

func intKeyMap(raw json.RawMessage) map[int]float64 {
	if len(raw) == 0 {
		return map[int]float64{}
	}
	var s map[string]float64
	if err := json.Unmarshal(raw, &s); err != nil {
		return map[int]float64{}
	}
	out := make(map[int]float64, len(s))
	for k, v := range s {
		n, err := atoi(k)
		if err != nil {
			continue
		}
		out[n] = v
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func atoi(s string) (int, error) {
	var n int
	var sign = 1
	for i, c := range s {
		if i == 0 && c == '-' {
			sign = -1
			continue
		}
		if c < '0' || c > '9' {
			return 0, errParse
		}
		n = n*10 + int(c-'0')
	}
	return sign * n, nil
}

var errParse = &parseErr{}

type parseErr struct{}

func (*parseErr) Error() string { return "parse error" }

// seenAdapter unions the user's interactions + media_list rows to produce
// the set of "already-touched" media_ids. The recommender filters out
// these candidates so the feed never recommends something the user has
// already engaged with. A touched season adds its series, so tracking any
// season of a show keeps the show out of the feed.
type seenAdapter struct {
	db *sqlx.DB
}

func newSeenAdapter(db *sqlx.DB) *seenAdapter { return &seenAdapter{db: db} }

func (a *seenAdapter) SeenMediaIDs(ctx context.Context, userID uuid.UUID) (map[int]bool, error) {
	var ids []int
	err := a.db.SelectContext(ctx, &ids, `
		with touched as (
			select media_id from interactions where user_id = $1
			union
			select media_id from media_list where user_id = $1
		)
		select media_id from touched
		union
		select series.media_id
		from touched t
		join media_index season on season.media_id = t.media_id and season.media_type = 'season'
		join media_index series on series.id = season.id and series.media_type = 'tv'
	`, userID)
	if err != nil {
		return nil, err
	}
	out := make(map[int]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// yearOf returns the 4-digit year from a nullable release date, or 0.
func yearOf(t *sql.NullTime) int {
	if t == nil || !t.Valid {
		return 0
	}
	return t.Time.Year()
}
