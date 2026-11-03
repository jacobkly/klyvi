package tv_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"klyvi-api/internal/tv"
)

// recoSeries is one synthetic series for the candidate tests. Series without
// a 'tv' media_index row have no identity the feed could name, so they are
// never candidates.
type recoSeries struct {
	tvID      int
	name      string
	voteCount int
	genres    string
	keywords  string
	indexed   bool
}

// seedRecoSeries writes the series and their index rows, removes them when
// the test ends, and returns each indexed series' 'tv' media_id.
func seedRecoSeries(t *testing.T, dbh *sqlx.DB, series []recoSeries) map[int]int {
	t.Helper()
	for _, s := range series {
		resetShow(t, dbh, s.tvID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mediaIDs := make(map[int]int)
	for _, s := range series {
		if _, err := dbh.ExecContext(ctx, `
			insert into tv_series (tv_id, name, vote_count, vote_average, genres, keywords, first_air_date)
			values ($1, $2, $3, 7.5, $4::jsonb, $5::jsonb, '2019-02-01')`,
			s.tvID, s.name, s.voteCount, s.genres, s.keywords); err != nil {
			t.Fatalf("insert series %d: %v", s.tvID, err)
		}
		if !s.indexed {
			continue
		}
		var mediaID int
		if err := dbh.GetContext(ctx, &mediaID, `
			insert into media_index (id, media_type) values ($1, 'tv')
			returning media_id`, s.tvID); err != nil {
			t.Fatalf("index series %d: %v", s.tvID, err)
		}
		mediaIDs[s.tvID] = mediaID
	}
	return mediaIDs
}

func tvIDsOf(rows []tv.RecoCandidateRow) []int {
	ids := make([]int, len(rows))
	for i, r := range rows {
		ids[i] = r.TVID
	}
	return ids
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Taste retrieval mirrors movies: series sharing a keyword come first, then
// series sharing only a genre, each most-voted first, above the vote floor.
func TestListCandidatesByTaste_KeywordMatchesFirstAboveTheFloor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, repo, dbh := setup(t)
	requireMigration(t, dbh, 30)

	const (
		keywordID = 2_100_000_910
		genreID   = 2_100_000_911
		kw        = `[{"id": 2100000910, "name": "reco test keyword"}]`
		genre     = `[{"id": 2100000911, "name": "Reco Test Genre"}]`
	)
	const (
		keywordHigh = 2_100_000_030
		keywordLow  = 2_100_000_031
		belowFloor  = 2_100_000_032
		genreOnly   = 2_100_000_033
		unindexed   = 2_100_000_034
	)
	mediaIDs := seedRecoSeries(t, dbh, []recoSeries{
		{keywordHigh, "Keyword High", 500, `[]`, kw, true},
		{keywordLow, "Keyword Low", 100, `[]`, kw, true},
		{belowFloor, "Below Floor", 5, genre, kw, true},
		// More votes than any keyword match, and still after them.
		{genreOnly, "Genre Only", 900, genre, `[]`, true},
		{unindexed, "Unindexed", 1000, genre, kw, false},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows, err := repo.ListCandidatesByTaste(ctx, []int{genreID}, []int{keywordID}, 50, 10)
	if err != nil {
		t.Fatalf("ListCandidatesByTaste: %v", err)
	}
	if want := []int{keywordHigh, keywordLow, genreOnly}; !sameInts(tvIDsOf(rows), want) {
		t.Fatalf("candidates = %v, want %v", tvIDsOf(rows), want)
	}

	first := rows[0]
	if first.MediaID != mediaIDs[keywordHigh] {
		t.Errorf("media_id = %d, want the series' 'tv' row %d", first.MediaID, mediaIDs[keywordHigh])
	}
	if first.Name == nil || *first.Name != "Keyword High" {
		t.Errorf("name = %v, want Keyword High", first.Name)
	}
	if !sameInts(first.KeywordIDs, []int{keywordID}) || len(first.GenreIDs) != 0 {
		t.Errorf("features = genres %v keywords %v, want none and [%d]", first.GenreIDs, first.KeywordIDs, keywordID)
	}
	if first.VoteCount != 500 || first.VoteAverage != 7.5 {
		t.Errorf("votes = %d at %.1f, want 500 at 7.5", first.VoteCount, first.VoteAverage)
	}
	if first.FirstAirDate == nil || !first.FirstAirDate.Valid || first.FirstAirDate.Time.Year() != 2019 {
		t.Errorf("first_air_date = %v, want 2019", first.FirstAirDate)
	}

	capped, err := repo.ListCandidatesByTaste(ctx, []int{genreID}, []int{keywordID}, 50, 1)
	if err != nil {
		t.Fatalf("ListCandidatesByTaste capped: %v", err)
	}
	if want := []int{keywordHigh}; !sameInts(tvIDsOf(capped), want) {
		t.Errorf("capped candidates = %v, want %v", tvIDsOf(capped), want)
	}
}

// A user's history holds movie, season and series media ids side by side.
// The TV lookup answers for the series ones only, even when a movie or a
// season shares the series' TMDB id.
func TestCandidatesByMediaIDs_AnswersForSeriesOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, repo, dbh := setup(t)
	requireMigration(t, dbh, 30)

	const show = 2_100_000_035
	mediaIDs := seedRecoSeries(t, dbh, []recoSeries{
		{show, "History Show", 40, `[]`, `[]`, true},
	})
	t.Cleanup(func() {
		cleanupExec(dbh, `delete from media_index where id = $1 and media_type = 'movie'`, show)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var seasonMediaID, movieMediaID int
	if err := dbh.GetContext(ctx, &seasonMediaID, `
		insert into media_index (id, season_number, media_type) values ($1, 1, 'season')
		returning media_id`, show); err != nil {
		t.Fatalf("index season: %v", err)
	}
	if err := dbh.GetContext(ctx, &movieMediaID, `
		insert into media_index (id, media_type) values ($1, 'movie')
		returning media_id`, show); err != nil {
		t.Fatalf("index movie: %v", err)
	}

	rows, err := repo.CandidatesByMediaIDs(ctx, []int{movieMediaID, seasonMediaID, mediaIDs[show]})
	if err != nil {
		t.Fatalf("CandidatesByMediaIDs: %v", err)
	}
	if len(rows) != 1 || rows[0].MediaID != mediaIDs[show] || rows[0].TVID != show {
		t.Fatalf("rows = %+v, want only series %d as media_id %d", rows, show, mediaIDs[show])
	}

	none, err := repo.CandidatesByMediaIDs(ctx, nil)
	if err != nil || len(none) != 0 {
		t.Errorf("empty lookup = %v, %v, want no rows and no error", none, err)
	}
}

// The popularity fallback is the most-voted indexed series.
func TestListCandidatesForReco_MostVotedIndexedSeries(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, repo, dbh := setup(t)
	requireMigration(t, dbh, 30)

	const (
		top       = 2_100_000_036
		unindexed = 2_100_000_037
	)
	seedRecoSeries(t, dbh, []recoSeries{
		{top, "Top Series", 2_000_000_000, `[]`, `[]`, true},
		{unindexed, "Unindexed Top", 2_000_000_001, `[]`, `[]`, false},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows, err := repo.ListCandidatesForReco(ctx, 5)
	if err != nil {
		t.Fatalf("ListCandidatesForReco: %v", err)
	}
	if len(rows) == 0 || rows[0].TVID != top {
		t.Fatalf("first fallback candidate = %v, want %d", tvIDsOf(rows), top)
	}
	if len(rows) > 5 {
		t.Errorf("got %d rows, want at most 5", len(rows))
	}
	for i, r := range rows {
		if r.VoteCount <= 0 {
			t.Errorf("row %d (tv %d) has %d votes, want a voted series", i, r.TVID, r.VoteCount)
		}
		if i > 0 && r.VoteCount > rows[i-1].VoteCount {
			t.Errorf("row %d has more votes than row %d", i, i-1)
		}
	}
}

// Keyword ids are shared with movies, but some only ever appear on series.
// Their names come from the series cache, in either stored keyword shape
// (see TestSchema_TVSeriesFeatureArraysReadBothKeywordShapes).
func TestLookupKeywordNames_ReadsSeriesKeywords(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, repo, dbh := setup(t)
	requireMigration(t, dbh, 30)

	const (
		bareKeyword     = 2_100_000_912
		envelopeKeyword = 2_100_000_913
		unknownKeyword  = 2_100_000_914
	)
	seedRecoSeries(t, dbh, []recoSeries{
		{2_100_000_038, "Bare Keywords", 10, `[]`,
			`[{"id": 2100000912, "name": "series-only keyword"}]`, true},
		{2_100_000_039, "Envelope Keywords", 10, `[]`,
			`{"results": [{"id": 2100000913, "name": "enveloped keyword"}]}`, true},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	names, err := repo.LookupKeywordNames(ctx, []int{bareKeyword, envelopeKeyword, unknownKeyword})
	if err != nil {
		t.Fatalf("LookupKeywordNames: %v", err)
	}
	want := map[int]string{bareKeyword: "series-only keyword", envelopeKeyword: "enveloped keyword"}
	if len(names) != len(want) || names[bareKeyword] != want[bareKeyword] || names[envelopeKeyword] != want[envelopeKeyword] {
		t.Errorf("names = %v, want %v", names, want)
	}
}

// TV has genres no film carries (Kids, News, Reality, Soap, Talk). A reason
// built on one needs its name from the series cache.
func TestLookupGenreNames_ReadsSeriesGenres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, repo, dbh := setup(t)
	requireMigration(t, dbh, 30)

	const (
		seriesGenre  = 2_100_000_915
		unknownGenre = 2_100_000_916
	)
	seedRecoSeries(t, dbh, []recoSeries{
		{2_100_000_040, "Series Genre", 10, `[{"id": 2100000915, "name": "series-only genre"}]`, `[]`, true},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	names, err := repo.LookupGenreNames(ctx, []int{seriesGenre, unknownGenre})
	if err != nil {
		t.Fatalf("LookupGenreNames: %v", err)
	}
	if len(names) != 1 || names[seriesGenre] != "series-only genre" {
		t.Errorf("names = %v, want only %d named", names, seriesGenre)
	}
}

// A show's feed card carries its synopsis, its genre names in TMDB order and
// its season count. A series with no synopsis or genres still has a card,
// blank rather than missing, and its genres encode as [] not null.
func TestCardDetailsByMediaIDs_ReadsTheSeriesCard(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, repo, dbh := setup(t)
	requireMigration(t, dbh, 30)

	const (
		fullCard = 2_100_000_064
		bareCard = 2_100_000_065
	)
	ids := seedRecoSeries(t, dbh, []recoSeries{
		{fullCard, "Full Card", 100, `[{"id": 18, "name": "Drama"}, {"id": 80, "name": "Crime"}]`, `[]`, true},
		{bareCard, "Bare Card", 100, `[]`, `[]`, true},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := dbh.ExecContext(ctx,
		`update tv_series set overview = 'A teacher turns to crime.', number_of_seasons = 5 where tv_id = $1`,
		fullCard); err != nil {
		t.Fatalf("set the full card: %v", err)
	}

	got, err := repo.CardDetailsByMediaIDs(ctx, []int{ids[fullCard], ids[bareCard]})
	if err != nil {
		t.Fatalf("CardDetailsByMediaIDs: %v", err)
	}
	byID := map[int]tv.CardDetails{}
	for _, d := range got {
		byID[d.MediaID] = d
	}

	full := byID[ids[fullCard]]
	if full.Overview != "A teacher turns to crime." || full.SeasonCount != 5 ||
		!slices.Equal(full.Genres, []string{"Drama", "Crime"}) {
		t.Errorf("full card = %+v, want its synopsis, Drama then Crime, and 5 seasons", full)
	}
	bare, ok := byID[ids[bareCard]]
	if !ok {
		t.Fatalf("no card for the bare series, got %+v", got)
	}
	if bare.Overview != "" || bare.SeasonCount != 0 || bare.Genres == nil || len(bare.Genres) != 0 {
		t.Errorf("bare card = %+v, want a blank card with empty genres", bare)
	}
}
