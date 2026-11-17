package opendata

import (
	"context"
	"slices"
	"testing"

	"github.com/joeshaw/envdecode"

	"klyvi-api/config"
	"klyvi-api/internal/platform/db"
)

// Ids far above any real TMDB id, so the test never touches real rows.
const testID1, testID2 = 2_000_000_001, 2_000_000_002

func TestRepositorySave(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB-dependent test in -short mode")
	}
	var cfg config.ConfDB
	if err := envdecode.StrictDecode(&cfg); err != nil {
		t.Skipf("DB env incomplete: %v", err)
	}
	conn, err := db.New(cfg)
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	ctx := context.Background()
	cleanup := func() {
		_, _ = conn.ExecContext(ctx, `delete from wikidata_facts where tmdb_id in ($1, $2)`, testID1, testID2)
	}
	cleanup()
	t.Cleanup(func() { cleanup(); _ = conn.Close() })
	repo := NewRepository(conn)
	batch := []int{testID1, testID2}
	article := "https://en.wikipedia.org/wiki/Example"

	facts := func() []Facts {
		return []Facts{
			{TMDBID: testID1, WikidataID: 42, WikipediaURL: &article, Genres: []int{3, 1, 3}, Cast: []int{7}},
			{TMDBID: testID2, WikidataID: 43},
		}
	}
	save := func(name string, fs []Facts, want int) {
		t.Helper()
		got, err := repo.Save(ctx, "movie", batch, fs)
		if err != nil {
			t.Fatalf("%s: Save: %v", name, err)
		}
		if got != want {
			t.Errorf("%s: changed %d rows, want %d", name, got, want)
		}
	}

	save("first run", facts(), 2)
	save("rerun with nothing new", facts(), 0)

	var genres db.IntArray
	if err := conn.GetContext(ctx, &genres,
		`select genres from wikidata_facts where media_type = 'movie' and tmdb_id = $1`, testID1); err != nil {
		t.Fatalf("read genres: %v", err)
	}
	if len(genres) != 2 || genres[0] != 1 || genres[1] != 3 {
		t.Errorf("genres = %v, want sorted without repeats [1 3]", genres)
	}

	changed := facts()
	changed[0].Subjects = []int{99}
	save("one title gains a subject", changed, 1)

	save("the other loses its match", changed[:1], 1)
	var n int
	if err := conn.GetContext(ctx, &n,
		`select count(*) from wikidata_facts where tmdb_id in ($1, $2)`, testID1, testID2); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("rows = %d, want the unmatched title dropped", n)
	}

	changed[0].Directors = []int{120, 99}
	changed[0].Locations = []int{5}
	save("the title gains a director and a setting", changed[:1], 1)
	all, err := repo.RecoFacts(ctx)
	if err != nil {
		t.Fatalf("RecoFacts: %v", err)
	}
	var got []int
	for _, f := range all {
		if f.TMDBID == testID1 && f.MediaType == "movie" {
			got = f.IDs
		}
	}
	if want := []int{5, 99, 120}; !slices.Equal(got, want) {
		t.Errorf("reco facts = %v, want director, subject and setting in one sorted list %v", got, want)
	}
}
