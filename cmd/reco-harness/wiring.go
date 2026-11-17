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
//
// facts puts each title's Wikidata facts on its features, as the API's
// reco.NewFactCatalog does, and the scorers weigh shared facts by it.
type catalogAdapter struct {
	repo   *movies.Repository
	tvRepo *tv.Repository
	facts  *reco.FactIndex
}

func newCatalogAdapter(repo *movies.Repository, tvRepo *tv.Repository, facts *reco.FactIndex) *catalogAdapter {
	return &catalogAdapter{repo: repo, tvRepo: tvRepo, facts: facts}
}

func (a *catalogAdapter) SampleMovieCandidates(ctx context.Context, limit int) ([]reco.Candidate, error) {
	rows, err := a.repo.ListCandidatesForReco(ctx, limit)
	if err != nil {
		return nil, err
	}
	return a.rowsToCandidates(rows), nil
}

// CandidatesByMediaIDs looks the ids up as films, then sends the ids no film
// matched to the series cache, as the API's adapter does, so a history that
// holds series resolves their features.
func (a *catalogAdapter) CandidatesByMediaIDs(ctx context.Context, mediaIDs []int) ([]reco.Candidate, error) {
	rows, err := a.repo.CandidatesByMediaIDs(ctx, mediaIDs)
	if err != nil {
		return nil, err
	}
	cands := a.rowsToCandidates(rows)

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
	return append(cands, a.seriesToCandidates(seriesRows)...), nil
}

func (a *catalogAdapter) CandidatesByTaste(ctx context.Context, genreIDs, keywordIDs []int, minVotes, limit int) ([]reco.Candidate, error) {
	rows, err := a.repo.ListCandidatesByTaste(ctx, genreIDs, keywordIDs, minVotes, limit)
	if err != nil {
		return nil, err
	}
	return a.rowsToCandidates(rows), nil
}

func (a *catalogAdapter) SampleSeriesCandidates(ctx context.Context, limit int) ([]reco.Candidate, error) {
	rows, err := a.tvRepo.ListCandidatesForReco(ctx, limit)
	if err != nil {
		return nil, err
	}
	return a.seriesToCandidates(rows), nil
}

func (a *catalogAdapter) SeriesCandidatesByTaste(ctx context.Context, genreIDs, keywordIDs []int, minVotes, limit int) ([]reco.Candidate, error) {
	rows, err := a.tvRepo.ListCandidatesByTaste(ctx, genreIDs, keywordIDs, minVotes, limit)
	if err != nil {
		return nil, err
	}
	return a.seriesToCandidates(rows), nil
}

// seriesToCandidates is rowsToCandidates for the series cache, with genres
// read into the film genre space as the API's adapter does.
func (a *catalogAdapter) seriesToCandidates(rows []tv.RecoCandidateRow) []reco.Candidate {
	cands := make([]reco.Candidate, 0, len(rows))
	for _, row := range rows {
		year := 0
		if row.FirstAirDate != nil && row.FirstAirDate.Valid {
			year = row.FirstAirDate.Time.Year()
		}
		cands = append(cands, reco.Candidate{
			MediaID:   row.MediaID,
			MediaType: "tv",
			TMDBID:    row.TVID,
			Title:     deref(row.Name),
			Features: &reco.MediaFeatures{
				MediaID:     row.MediaID,
				MediaType:   "tv",
				GenreIDs:    reco.CanonicalTVGenres(row.GenreIDs),
				KeywordIDs:  row.KeywordIDs,
				Year:        year,
				VoteAverage: row.VoteAverage,
				VoteCount:   row.VoteCount,
				FactIDs:     a.facts.IDs("tv", row.TVID),
			},
		})
	}
	return cands
}

func (a *catalogAdapter) rowsToCandidates(rows []movies.RecoCandidateRow) []reco.Candidate {
	cands := make([]reco.Candidate, 0, len(rows))
	for _, row := range rows {
		year := 0
		if row.ReleaseDate != nil && row.ReleaseDate.Valid {
			year = row.ReleaseDate.Time.Year()
		}
		cands = append(cands, reco.Candidate{
			MediaID:   row.MediaID,
			MediaType: "movie",
			TMDBID:    row.MovieID,
			Title:     deref(row.Title),
			Features: &reco.MediaFeatures{
				MediaID:     row.MediaID,
				MediaType:   "movie",
				GenreIDs:    row.GenreIDs,
				KeywordIDs:  row.KeywordIDs,
				Year:        year,
				VoteAverage: row.VoteAverage,
				VoteCount:   row.VoteCount,
				FactIDs:     a.facts.IDs("movie", row.MovieID),
			},
		})
	}
	return cands
}

// deref returns the string pointed to, or "" if the pointer is nil.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
