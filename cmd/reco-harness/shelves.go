package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"

	"klyvi-api/internal/movies"
	"klyvi-api/internal/reco"
	"klyvi-api/internal/similar"
)

// shelfRows is how many titles of each "more like this" shelf are printed.
const shelfRows = 10

// noTMDB fails every call, so a shelf shows Klyvi's own ranking alone and
// the harness never reaches TMDB.
type noTMDB struct{}

func (noTMDB) TMDBRequest(context.Context, string, string, interface{}) (map[string]interface{}, error) {
	return nil, errors.New("the harness does not call TMDB")
}

// shelfTarget is one title named by -shelves.
type shelfTarget struct {
	mediaType string
	tmdbID    int
}

// parseShelves reads "movie:550,tv:1396" into targets.
func parseShelves(spec string) ([]shelfTarget, error) {
	var out []shelfTarget
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kind, id, ok := strings.Cut(part, ":")
		n, err := strconv.Atoi(id)
		if !ok || err != nil || n <= 0 || (kind != "movie" && kind != "tv") {
			return nil, fmt.Errorf("%q: want movie:<tmdb id> or tv:<tmdb id>", part)
		}
		out = append(out, shelfTarget{kind, n})
	}
	if len(out) == 0 {
		return nil, errors.New("no titles")
	}
	return out, nil
}

// printShelves prints each title's "more like this" shelf from the local
// catalog without plots and with plots at weight w, side by side.
func printShelves(ctx context.Context, dbConn *sqlx.DB, repo *movies.Repository, plots *reco.PlotIndex,
	targets []shelfTarget, cfg reco.Config, w float64) error {
	shelfCfg := similar.Config{KeywordWeight: cfg.KeywordWeight, GenreWeight: cfg.GenreWeight, MinVotes: cfg.QualityFloorVotes}
	ensure := func(context.Context, int) error { return nil }
	build := func(mediaType string, weight float64) (*similar.Service, error) {
		gdf, kdf, corpus, err := repo.LoadFeatureStats(ctx, mediaType)
		if err != nil {
			return nil, err
		}
		idf := reco.NewFeatureIDF(gdf, kdf, corpus)
		if mediaType == "tv" {
			return similar.NewTVService(similar.NewTVCatalog(dbConn), ensure, noTMDB{}, idf, shelfCfg).WithPlots(plots, weight), nil
		}
		return similar.NewMovieService(similar.NewMovieCatalog(dbConn), ensure, noTMDB{}, idf, shelfCfg).WithPlots(plots, weight), nil
	}

	for _, t := range targets {
		off, err := build(t.mediaType, 0)
		if err != nil {
			return err
		}
		on, err := build(t.mediaType, w)
		if err != nil {
			return err
		}
		before, err := off.Similar(ctx, t.tmdbID, shelfRows)
		if err != nil {
			return fmt.Errorf("%s %d: %w", t.mediaType, t.tmdbID, err)
		}
		after, err := on.Similar(ctx, t.tmdbID, shelfRows)
		if err != nil {
			return fmt.Errorf("%s %d: %w", t.mediaType, t.tmdbID, err)
		}

		fmt.Printf("\n## %s (%s %d), plot vector: %v\n\n", shelfTitle(ctx, dbConn, t), t.mediaType, t.tmdbID,
			plots.Vector(t.mediaType, t.tmdbID) != nil)
		fmt.Printf("| # | without plots | with plots at %.2f |\n|---|---|---|\n", w)
		for i := range max(len(before), len(after)) {
			fmt.Printf("| %d | %s | %s |\n", i+1, shelfCell(before, i), shelfCell(after, i))
		}
	}
	return nil
}

func shelfCell(items []similar.Item, i int) string {
	if i >= len(items) {
		return ""
	}
	it := items[i]
	if it.Year > 0 {
		return fmt.Sprintf("%s (%d) %.2f", it.Title, it.Year, it.Score)
	}
	return fmt.Sprintf("%s %.2f", it.Title, it.Score)
}

func shelfTitle(ctx context.Context, dbConn *sqlx.DB, t shelfTarget) string {
	q := `select coalesce(title, '') from movies where movie_id = $1`
	if t.mediaType == "tv" {
		q = `select coalesce(name, '') from tv_series where tv_id = $1`
	}
	var title string
	if err := dbConn.GetContext(ctx, &title, q, t.tmdbID); err != nil || title == "" {
		return "not cached"
	}
	return title
}
