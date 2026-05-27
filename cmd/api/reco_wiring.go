package main

import (
	"context"
	"database/sql"
	"encoding/json"

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

func rowsToCandidates(rows []movies.RecoCandidateRow) []reco.Candidate {
	cands := make([]reco.Candidate, 0, len(rows))
	for _, row := range rows {
		feat := &reco.MediaFeatures{
			MediaID:     row.MediaID,
			MediaType:   "movie",
			GenreIDs:    parseIDArray(row.Genres),
			KeywordIDs:  parseIDArray(row.Keywords),
			Year:        yearOf(row.ReleaseDate),
			VoteAverage: row.VoteAverage,
			VoteCount:   row.VoteCount,
		}
		cands = append(cands, reco.Candidate{
			MediaID:   row.MediaID,
			MediaType: "movie",
			Features:  feat,
		})
	}
	return cands
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
			where user_id = $1 and created_at >= now() - ($2 || ' days')::interval
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
