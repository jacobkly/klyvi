package users_test

import (
	"testing"
	"time"

	"klyvi-api/internal/users"
)

func intp(v int) *int { return &v }

// A small but realistic library: three movies (two watched, one planned),
// two seasons (one finished, one in progress). Numbers below are chosen so
// every KPI is hand-checkable.
func statsFixtureRows() []users.StatsRow {
	return []users.StatsRow{
		// Watched movies: 120 + 90 minutes, scores 90 and 70.
		{MediaType: "movie", Status: strPtr("completed"), Score: intp(90), RuntimeMinutes: intp(120), ReleaseYear: intp(1999), Genres: []string{"Drama", "Thriller"}, Countries: []string{"US"}},
		{MediaType: "movie", Status: strPtr("completed"), Score: intp(70), RuntimeMinutes: intp(90), ReleaseYear: intp(2010), Genres: []string{"Drama"}, Countries: []string{"KR"}},
		// Planned movie: 100 minutes of days_planned, no score.
		{MediaType: "movie", Status: strPtr("planning"), RuntimeMinutes: intp(100), ReleaseYear: intp(2020), Genres: []string{"Comedy"}, Countries: []string{"US"}},
		// Finished season: 10 episodes at 30 minutes.
		{MediaType: "season", Status: strPtr("completed"), Score: intp(80), EpisodeProgress: intp(10), EpisodeCount: intp(10), EpisodeRuntimeMinutes: intp(30), ReleaseYear: intp(2015), Genres: []string{"Drama"}, Countries: []string{"JP"}},
		// In-progress season: 4 of 12 episodes seen, 30-minute episodes.
		{MediaType: "season", Status: strPtr("watching"), EpisodeProgress: intp(4), EpisodeCount: intp(12), EpisodeRuntimeMinutes: intp(30), ReleaseYear: intp(2021), Genres: []string{"Action"}, Countries: []string{"JP"}},
	}
}

func strPtr(s string) *string { return &s }

func TestComputeStats_KPIs(t *testing.T) {
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	got := users.ComputeStats(statsFixtureRows(), nil, now)

	// Engaged = every non-planning entry.
	if got.KPIs.TotalFilms != 2 {
		t.Errorf("total_films = %d, want 2", got.KPIs.TotalFilms)
	}
	if got.KPIs.TotalSeasons != 2 {
		t.Errorf("total_seasons = %d, want 2", got.KPIs.TotalSeasons)
	}
	if got.KPIs.EpisodesWatched != 14 {
		t.Errorf("episodes_watched = %d, want 14", got.KPIs.EpisodesWatched)
	}

	// Watched minutes: 120 + 90 (movies) + 10×30 + 4×30 (seasons) = 630.
	wantDaysWatched := 630.0 / 60.0 / 24.0
	if diff := got.KPIs.DaysWatched - wantDaysWatched; diff > 0.001 || diff < -0.001 {
		t.Errorf("days_watched = %f, want %f", got.KPIs.DaysWatched, wantDaysWatched)
	}

	// Planned minutes: the one planned movie, 100.
	wantDaysPlanned := 100.0 / 60.0 / 24.0
	if diff := got.KPIs.DaysPlanned - wantDaysPlanned; diff > 0.001 || diff < -0.001 {
		t.Errorf("days_planned = %f, want %f", got.KPIs.DaysPlanned, wantDaysPlanned)
	}

	// Scores 90, 70, 80 → mean 80, population stddev √(200/3)≈8.165.
	if diff := got.KPIs.MeanScore - 80.0; diff > 0.001 || diff < -0.001 {
		t.Errorf("mean_score = %f, want 80", got.KPIs.MeanScore)
	}
	if diff := got.KPIs.ScoreStddev - 8.1649; diff > 0.001 || diff < -0.001 {
		t.Errorf("score_stddev = %f, want ≈8.1649", got.KPIs.ScoreStddev)
	}
}

func TestComputeStats_Distributions(t *testing.T) {
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	got := users.ComputeStats(statsFixtureRows(), nil, now)

	// Ten bands, always. 90 → "81-90", 80 → "71-80", 70 → "61-70".
	if len(got.ScoreDistribution) != 10 {
		t.Fatalf("bands = %d, want 10", len(got.ScoreDistribution))
	}
	titles := map[string]int{}
	for _, b := range got.ScoreDistribution {
		titles[b.Band] = b.Titles
	}
	if titles["81-90"] != 1 || titles["71-80"] != 1 || titles["61-70"] != 1 {
		t.Errorf("band titles = %v, want one each in 81-90 / 71-80 / 61-70", titles)
	}

	// Genres sorted by count: Drama appears 3 times.
	if len(got.Genres) == 0 || got.Genres[0].Name != "Drama" || got.Genres[0].Count != 3 {
		t.Fatalf("top genre = %+v, want Drama×3", got.Genres)
	}
	// Drama's mean score: 90, 70, 80 → 80.
	if m := got.Genres[0].MeanScore; m == nil || *m-80.0 > 0.001 || *m-80.0 < -0.001 {
		t.Errorf("Drama mean = %v, want 80", m)
	}
	// Comedy's only title is unscored, so it has no mean rather than a 0
	// that would read as a real score.
	for _, g := range got.Genres {
		if g.Name == "Comedy" && g.MeanScore != nil {
			t.Errorf("Comedy mean = %v, want nil (nothing scored)", *g.MeanScore)
		}
	}

	// Formats: 3 movies, 2 seasons → 60/40.
	fm := map[string]float64{}
	for _, f := range got.Formats {
		fm[f.Label] = f.Pct
	}
	if fm["movie"] != 60 || fm["season"] != 40 {
		t.Errorf("formats = %+v, want movie 60 / season 40", got.Formats)
	}

	// Release years present for every distinct year.
	if len(got.ReleaseYears) != 5 {
		t.Errorf("release_years = %d entries, want 5", len(got.ReleaseYears))
	}
}

func TestComputeStats_ActivityAndWatchYears(t *testing.T) {
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	events := []users.StatsEvent{
		{CreatedAt: now.AddDate(0, 0, -1)},                     // yesterday
		{CreatedAt: now.AddDate(0, 0, -1).Add(2 * time.Hour)},  // same day
		{CreatedAt: now.AddDate(0, 0, -100)},                   // ~3 months back
		{CreatedAt: now.AddDate(-1, 0, 0)},                     // last year (outside 371d)
		{CreatedAt: now.AddDate(0, 0, -400)},                   // outside the heatmap window
	}

	got := users.ComputeStats(nil, events, now)

	if len(got.Activity) != 371 {
		t.Fatalf("activity days = %d, want 371", len(got.Activity))
	}
	// The last cell is today; the one before is yesterday with 2 events.
	yesterday := got.Activity[len(got.Activity)-2]
	if yesterday.Count != 2 {
		t.Errorf("yesterday count = %d, want 2 (%+v)", yesterday.Count, yesterday)
	}
	total := 0
	for _, d := range got.Activity {
		total += d.Count
	}
	if total != 4 { // -1 ×2, -100, and -365 (the -1y event) fall inside 371 days
		t.Errorf("total heatmap events = %d, want 4", total)
	}

	// watch_years counts every event's year, unbounded by the heatmap window.
	years := map[int]int{}
	for _, y := range got.WatchYears {
		years[y.Year] = y.Titles
	}
	if years[2026] != 3 || years[2025] != 2 {
		t.Errorf("watch_years = %+v, want 2026:3 2025:2", got.WatchYears)
	}
}

// Zero-state: a fresh user gets a fully-shaped, all-zero payload — never
// nulls — so the client renders empty charts without special cases.
func TestComputeStats_EmptyLibrary(t *testing.T) {
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	got := users.ComputeStats(nil, nil, now)

	if got.KPIs.TotalFilms != 0 || got.KPIs.MeanScore != 0 {
		t.Errorf("zero KPIs expected, got %+v", got.KPIs)
	}
	if len(got.ScoreDistribution) != 10 {
		t.Errorf("bands = %d, want 10 even when empty", len(got.ScoreDistribution))
	}
	if got.Genres == nil || got.ReleaseYears == nil || got.WatchYears == nil ||
		got.Formats == nil || got.Countries == nil {
		t.Error("slices must be non-nil so JSON encodes [] not null")
	}
	if len(got.Activity) != 371 {
		t.Errorf("activity days = %d, want 371", len(got.Activity))
	}
}

// TV's combined genres count as the film genres they're made of, so a
// library of both doesn't list "Sci-Fi & Fantasy" beside "Science Fiction".
func TestComputeStats_SplitsTVGenres(t *testing.T) {
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	done := "completed"
	rows := []users.StatsRow{
		{MediaType: "movie", Status: &done, Genres: []string{"Science Fiction", "Action"}},
		{MediaType: "season", Status: &done, Genres: []string{"Sci-Fi & Fantasy", "Action & Adventure", "Drama"}},
	}
	got := users.ComputeStats(rows, nil, now)

	counts := map[string]int{}
	for _, g := range got.Genres {
		counts[g.Name] = g.Count
	}
	want := map[string]int{"Science Fiction": 2, "Action": 2, "Fantasy": 1, "Adventure": 1, "Drama": 1}
	for name, n := range want {
		if counts[name] != n {
			t.Errorf("%s = %d, want %d (all: %v)", name, counts[name], n, counts)
		}
	}
	if _, ok := counts["Sci-Fi & Fantasy"]; ok {
		t.Errorf("Sci-Fi & Fantasy is still listed: %v", counts)
	}
	if _, ok := counts["Action & Adventure"]; ok {
		t.Errorf("Action & Adventure is still listed: %v", counts)
	}
}
