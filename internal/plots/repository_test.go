package plots

import (
	"context"
	"testing"

	"github.com/joeshaw/envdecode"

	"klyvi-api/config"
	"klyvi-api/internal/platform/db"
)

// Ids far above any real TMDB id, so the test never touches real rows.
const testID1, testID2 = 2_000_000_101, 2_000_000_102

func TestRepository(t *testing.T) {
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
		_, _ = conn.ExecContext(ctx, `delete from wikipedia_plots where tmdb_id in ($1, $2)`, testID1, testID2)
		_, _ = conn.ExecContext(ctx, `delete from wikidata_facts where tmdb_id in ($1, $2)`, testID1, testID2)
	}
	cleanup()
	t.Cleanup(func() { cleanup(); _ = conn.Close() })
	if _, err := conn.ExecContext(ctx, `
		insert into wikidata_facts (media_type, tmdb_id, wikidata_id, wikipedia_url)
		values ('movie', $1, 1, 'https://en.wikipedia.org/wiki/One'), ('movie', $2, 2, null)`,
		testID1, testID2); err != nil {
		t.Fatalf("seed facts: %v", err)
	}
	repo := NewRepository(conn)

	target := func() *Target {
		t.Helper()
		ts, err := repo.Targets(ctx, "movie")
		if err != nil {
			t.Fatalf("Targets: %v", err)
		}
		var found *Target
		for i := range ts {
			if ts[i].TMDBID == testID2 {
				t.Errorf("Targets lists a title without an article")
			}
			if ts[i].TMDBID == testID1 {
				found = &ts[i]
			}
		}
		if found == nil {
			t.Fatalf("Targets lacks the title with an article")
		}
		return found
	}

	if got := target(); got.RevisionID != nil || got.ArticleURL != "https://en.wikipedia.org/wiki/One" {
		t.Errorf("before any save: %+v", got)
	}

	vec := make([]float32, 384)
	vec[0], vec[1] = 0.6, -0.8
	if err := repo.Save(ctx, "movie", []Plot{{TMDBID: testID1, RevisionID: 7, Model: "m", Words: 300, Embedding: vec}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := target(); got.RevisionID == nil || *got.RevisionID != 7 || *got.Model != "m" {
		t.Errorf("after a save: %+v", got)
	}
	var first float64
	if err := conn.GetContext(ctx, &first,
		`select (embedding::extensions.vector::real[])[2] from wikipedia_plots where tmdb_id = $1`, testID1); err != nil {
		t.Fatalf("read vector: %v", err)
	}
	if first > -0.79 || first < -0.81 {
		t.Errorf("stored second component %.4f, want about -0.8", first)
	}

	if err := repo.Save(ctx, "movie", []Plot{{TMDBID: testID1, RevisionID: 8, Model: "m", Words: 12}}); err != nil {
		t.Fatalf("Save without a plot: %v", err)
	}
	var hasVector bool
	if err := conn.GetContext(ctx, &hasVector,
		`select embedding is not null from wikipedia_plots where tmdb_id = $1`, testID1); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if hasVector {
		t.Error("a revision without a plot kept the old vector")
	}

	if _, err := conn.ExecContext(ctx, `update wikidata_facts set wikipedia_url = null where tmdb_id = $1`, testID1); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if n, err := repo.Prune(ctx, "movie"); err != nil || n != 1 {
		t.Errorf("Prune = %d, %v, want 1 row dropped", n, err)
	}

	if _, err := repo.Coverage(ctx); err != nil {
		t.Errorf("Coverage: %v", err)
	}
	if _, err := repo.TableBytes(ctx); err != nil {
		t.Errorf("TableBytes: %v", err)
	}
}

func TestVectorLiteral(t *testing.T) {
	if got := vectorLiteral([]float32{0.5, -1, 0.25}); got != "[0.5,-1,0.25]" {
		t.Errorf("vectorLiteral = %q", got)
	}
}
