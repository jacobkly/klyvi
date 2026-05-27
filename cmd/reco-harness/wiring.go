package main

import (
	"context"
	"encoding/json"

	"klyvi-api/internal/movies"
	"klyvi-api/internal/reco"
)

// catalogAdapter is the same shape as cmd/api's adapter but local to the
// harness binary. Both binaries need to bridge movies.Repository onto
// reco's Candidate type; extracting the wiring to a shared package is
// future cleanup.
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
		year := 0
		if row.ReleaseDate != nil && row.ReleaseDate.Valid {
			year = row.ReleaseDate.Time.Year()
		}
		cands = append(cands, reco.Candidate{
			MediaID:   row.MediaID,
			MediaType: "movie",
			Features: &reco.MediaFeatures{
				MediaID:     row.MediaID,
				MediaType:   "movie",
				GenreIDs:    parseIDArray(row.Genres),
				KeywordIDs:  parseIDArray(row.Keywords),
				Year:        year,
				VoteAverage: row.VoteAverage,
				VoteCount:   row.VoteCount,
			},
		})
	}
	return cands
}

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
