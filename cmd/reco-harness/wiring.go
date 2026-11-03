package main

import (
	"context"

	"klyvi-api/internal/movies"
	"klyvi-api/internal/reco"
	"klyvi-api/internal/tv"
)

// catalogAdapter is the same shape as cmd/api's adapter but local to the
// harness binary. Both binaries need to bridge the movies and series
// repositories onto reco's Candidate type; extracting the wiring to a shared
// package is future cleanup.
type catalogAdapter struct {
	repo   *movies.Repository
	tvRepo *tv.Repository
}

func newCatalogAdapter(repo *movies.Repository, tvRepo *tv.Repository) *catalogAdapter {
	return &catalogAdapter{repo: repo, tvRepo: tvRepo}
}

func (a *catalogAdapter) SampleMovieCandidates(ctx context.Context, limit int) ([]reco.Candidate, error) {
	rows, err := a.repo.ListCandidatesForReco(ctx, limit)
	if err != nil {
		return nil, err
	}
	return rowsToCandidates(rows), nil
}

// CandidatesByMediaIDs looks the ids up as films, then sends the ids no film
// matched to the series cache, as the API's adapter does, so a history that
// holds series resolves their features.
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

// seriesToCandidates is rowsToCandidates for the series cache, with genres
// read into the film genre space as the API's adapter does.
func seriesToCandidates(rows []tv.RecoCandidateRow) []reco.Candidate {
	cands := make([]reco.Candidate, 0, len(rows))
	for _, row := range rows {
		year := 0
		if row.FirstAirDate != nil && row.FirstAirDate.Valid {
			year = row.FirstAirDate.Time.Year()
		}
		cands = append(cands, reco.Candidate{
			MediaID:   row.MediaID,
			MediaType: "tv",
			Features: &reco.MediaFeatures{
				MediaID:     row.MediaID,
				MediaType:   "tv",
				GenreIDs:    reco.CanonicalTVGenres(row.GenreIDs),
				KeywordIDs:  row.KeywordIDs,
				Year:        year,
				VoteAverage: row.VoteAverage,
				VoteCount:   row.VoteCount,
			},
		})
	}
	return cands
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
