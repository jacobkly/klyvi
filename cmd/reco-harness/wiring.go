package main

import (
	"context"

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

func (a *catalogAdapter) CandidatesByTaste(ctx context.Context, genreIDs, keywordIDs []int, minVotes, limit int) ([]reco.Candidate, error) {
	rows, err := a.repo.ListCandidatesByTaste(ctx, genreIDs, keywordIDs, minVotes, limit)
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
				GenreIDs:    row.GenreIDs,
				KeywordIDs:  row.KeywordIDs,
				Year:        year,
				VoteAverage: row.VoteAverage,
				VoteCount:   row.VoteCount,
			},
		})
	}
	return cands
}
