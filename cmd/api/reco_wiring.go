package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"klyvi-api/internal/movies"
	"klyvi-api/internal/reco"
)

// catalogAdapter wraps movies.Repository to satisfy reco.CatalogRepository.
// Translates the movies cache rows into reco's MediaFeatures shape.
type catalogAdapter struct {
	repo *movies.Repository
}

func newCatalogAdapter(repo *movies.Repository) *catalogAdapter {
	return &catalogAdapter{repo: repo}
}

func (a *catalogAdapter) SampleMovieCandidates(ctx context.Context, limit int) ([]reco.Candidate, error) {
	rows, err := a.repo.ListCandidatesForReco(ctx, limit)
	if err != nil {
		return nil, err
	}
	return rowsToCandidates(rows), nil
}

func (a *catalogAdapter) CandidatesByMediaIDs(ctx context.Context, mediaIDs []int) ([]reco.Candidate, error) {
	rows, err := a.repo.CandidatesByMediaIDs(ctx, mediaIDs)
	if err != nil {
		return nil, err
	}
	return rowsToCandidates(rows), nil
}

// LookupReasonNames satisfies reco.CatalogRepository — two cheap JSONB
// scans over the movies cache, one for genres and one for keywords.
// Each is wrapped in its own context so a failure on one doesn't poison
// the other.
func (a *catalogAdapter) LookupReasonNames(ctx context.Context, genreIDs, keywordIDs []int) (map[int]string, map[int]string, error) {
	genres, err := a.repo.LookupGenreNames(ctx, genreIDs)
	if err != nil {
		return nil, nil, err
	}
	keywords, err := a.repo.LookupKeywordNames(ctx, keywordIDs)
	if err != nil {
		return nil, nil, err
	}
	return genres, keywords, nil
}

func rowsToCandidates(rows []movies.RecoCandidateRow) []reco.Candidate {
	cands := make([]reco.Candidate, 0, len(rows))
	for _, row := range rows {
		year := yearOf(row.ReleaseDate)
		feat := &reco.MediaFeatures{
			MediaID:     row.MediaID,
			MediaType:   "movie",
			GenreIDs:    parseIDArray(row.Genres),
			KeywordIDs:  parseIDArray(row.Keywords),
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

func (a *signalAdapter) ListByUser(ctx context.Context, userID uuid.UUID, sinceDays int) ([]reco.InteractionRow, error) {
	var query string
	args := []any{userID}
	if sinceDays > 0 {
		query = `
			select media_id, media_type, kind, rating,
			       extract(epoch from (now() - created_at)) / 86400.0 as age_days
			from interactions
			where user_id = $1 and created_at >= now() - make_interval(days => $2)
			order by created_at desc
		`
		args = append(args, sinceDays)
	} else {
		query = `
			select media_id, media_type, kind, rating,
			       extract(epoch from (now() - created_at)) / 86400.0 as age_days
			from interactions
			where user_id = $1
			order by created_at desc
		`
	}

	var rows []struct {
		MediaID   int     `db:"media_id"`
		MediaType string  `db:"media_type"`
		Kind      string  `db:"kind"`
		Rating    *int    `db:"rating"`
		AgeDays   float64 `db:"age_days"`
	}
	if err := a.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}

	out := make([]reco.InteractionRow, len(rows))
	for i, r := range rows {
		out[i] = reco.InteractionRow{
			MediaID:   r.MediaID,
			MediaType: r.MediaType,
			Kind:      r.Kind,
			Rating:    r.Rating,
			AgeDays:   r.AgeDays,
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
// already engaged with.
type seenAdapter struct {
	db *sqlx.DB
}

func newSeenAdapter(db *sqlx.DB) *seenAdapter { return &seenAdapter{db: db} }

func (a *seenAdapter) SeenMediaIDs(ctx context.Context, userID uuid.UUID) (map[int]bool, error) {
	var ids []int
	err := a.db.SelectContext(ctx, &ids, `
		select media_id from interactions where user_id = $1
		union
		select media_id from media_list where user_id = $1
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

// parseIDArray pulls integer ids out of a TMDB-shaped JSONB array of
// {"id":..., "name":...} objects. Used for both genres and keywords.
func parseIDArray(raw *json.RawMessage) []int {
	if raw == nil || len(*raw) == 0 {
		return nil
	}
	var items []struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(*raw, &items); err != nil {
		return nil
	}
	out := make([]int, 0, len(items))
	for _, it := range items {
		if it.ID != 0 {
			out = append(out, it.ID)
		}
	}
	return out
}

// yearOf returns the 4-digit year from a nullable release date, or 0.
func yearOf(t *sql.NullTime) int {
	if t == nil || !t.Valid {
		return 0
	}
	return t.Time.Year()
}
