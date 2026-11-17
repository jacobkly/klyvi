package plots

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type fakeStore struct {
	rows    map[int]Plot
	urls    map[int]string
	deleted []int
}

func (s *fakeStore) Targets(_ context.Context, _ string) ([]Target, error) {
	var out []Target
	for id, u := range s.urls {
		t := Target{TMDBID: id, ArticleURL: u}
		if p, ok := s.rows[id]; ok {
			t.RevisionID, t.Model = &p.RevisionID, &p.Model
		}
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b Target) int { return a.TMDBID - b.TMDBID })
	return out, nil
}

func (s *fakeStore) Save(_ context.Context, _ string, plots []Plot) error {
	for _, p := range plots {
		s.rows[p.TMDBID] = p
	}
	return nil
}

func (s *fakeStore) Delete(_ context.Context, _ string, ids []int) error {
	for _, id := range ids {
		delete(s.rows, id)
	}
	s.deleted = append(s.deleted, ids...)
	return nil
}

func (s *fakeStore) Prune(context.Context, string) (int, error) { return 0, nil }

// fakeWiki serves the current revision of each article by title, and the
// wikitext of each revision.
type fakeWiki struct {
	revs     map[string]int64
	texts    map[int64]string
	textHits int
	fail     bool
}

func (w *fakeWiki) Revisions(_ context.Context, titles []string) (map[string]int64, error) {
	if w.fail {
		return nil, errors.New("down")
	}
	out := map[string]int64{}
	for _, t := range titles {
		if r, ok := w.revs[t]; ok {
			out[t] = r
		}
	}
	return out, nil
}

func (w *fakeWiki) Texts(_ context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	for _, id := range ids {
		w.textHits++
		out[id] = w.texts[id]
	}
	return out, nil
}

// recorder is an embedder that keeps every input it is given.
type recorder struct{ inputs []string }

func (r *recorder) Embed(_ context.Context, text string) ([]float32, error) {
	r.inputs = append(r.inputs, text)
	return []float32{float32(len(r.inputs))}, nil
}

func article(plot string) string {
	return "{{Infobox film}}\n'''X''' is a film.\n== Plot ==\n" + plot + "\n== Cast ==\n* Someone\n"
}

var longPlot = strings.Repeat("A detective follows a thief across the city at night. ", 6)

func TestJobRun(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{rows: map[int]Plot{}, urls: map[int]string{
		1: "https://en.wikipedia.org/wiki/Heist_Film",
		2: "https://en.wikipedia.org/wiki/Stub_Film",
		3: "https://en.wikipedia.org/wiki/Deleted_Film",
	}}
	wiki := &fakeWiki{
		revs:  map[string]int64{"Heist Film": 10, "Stub Film": 20},
		texts: map[int64]string{10: article(longPlot), 20: article("Too short.")},
	}
	rec := &recorder{}
	job := NewJob(store, wiki, rec, "model-a", 0)

	res, err := job.Run(ctx, []string{"movie"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Articles != 2 || res.Embedded != 1 || res.NoPlot != 1 || res.Unchanged != 0 {
		t.Errorf("first run = %+v, want 2 articles, 1 embedded, 1 without a plot", res)
	}
	if p := store.rows[2]; p.Embedding != nil || p.RevisionID != 20 {
		t.Errorf("stub row = %+v, want a revision with no vector", p)
	}

	t.Run("a rerun with nothing new embeds nothing", func(t *testing.T) {
		hits := wiki.textHits
		res, err := job.Run(ctx, []string{"movie"})
		if err != nil || res.Unchanged != 2 || res.Embedded != 0 || wiki.textHits != hits {
			t.Errorf("rerun = %+v, %v, %d text fetches", res, err, wiki.textHits-hits)
		}
	})

	t.Run("an edited article is embedded again", func(t *testing.T) {
		wiki.revs["Heist Film"] = 11
		wiki.texts[11] = article(longPlot + " It ends badly.")
		res, err := job.Run(ctx, []string{"movie"})
		if err != nil || res.Embedded != 1 || res.Unchanged != 1 || store.rows[1].RevisionID != 11 {
			t.Errorf("after an edit = %+v, %v, row %+v", res, err, store.rows[1])
		}
	})

	t.Run("a new model re-embeds every plot", func(t *testing.T) {
		res, err := NewJob(store, wiki, rec, "model-b", 0).Run(ctx, []string{"movie"})
		if err != nil || res.Embedded != 1 || res.NoPlot != 1 || store.rows[1].Model != "model-b" {
			t.Errorf("new model = %+v, %v", res, err)
		}
	})

	t.Run("a deleted article drops its row", func(t *testing.T) {
		delete(wiki.revs, "Stub Film")
		res, err := job.Run(ctx, []string{"movie"})
		if _, ok := store.rows[2]; err != nil || ok || res.Removed != 1 {
			t.Errorf("after a delete = %+v, %v, rows %v", res, err, store.rows)
		}
	})
}

func TestJobRun_KeepsGoingPastAFailedBatch(t *testing.T) {
	store := &fakeStore{rows: map[int]Plot{}, urls: map[int]string{1: "https://en.wikipedia.org/wiki/A"}}
	res, err := NewJob(store, &fakeWiki{fail: true}, &recorder{}, "m", 0).Run(context.Background(), MediaTypes)
	if err == nil || res.Failed != 2 {
		t.Errorf("Run = %+v, %v, want a failed batch per media type and an error", res, err)
	}
}

// TestJobRun_EmbedsOnlyWikipediaText is the guard on TMDB's terms: the model
// may see Wikipedia plot text and nothing else. Every input must be the
// cleaned plot of the article the fake Wikipedia served, and none of the rest
// of the page.
func TestJobRun_EmbedsOnlyWikipediaText(t *testing.T) {
	urls := map[int]string{}
	revs := map[string]int64{}
	texts := map[int64]string{}
	plots := map[string]bool{}
	for i := 1; i <= 120; i++ { // more than two API batches
		title := "Film " + strconv.Itoa(i)
		urls[i] = "https://en.wikipedia.org/wiki/" + strings.ReplaceAll(title, " ", "_")
		revs[title] = int64(1000 + i)
		plot := strings.Repeat("Plot of "+title+" goes on and on. ", 10)
		texts[int64(1000+i)] = article(plot)
		plots[EmbedInput(strings.TrimSpace(plot))] = true
	}
	rec := &recorder{}
	res, err := NewJob(&fakeStore{rows: map[int]Plot{}, urls: urls}, &fakeWiki{revs: revs, texts: texts},
		rec, "m", 0).Run(context.Background(), []string{"movie"})
	if err != nil || res.Embedded != 120 {
		t.Fatalf("Run = %+v, %v", res, err)
	}
	for _, in := range rec.inputs {
		if !plots[in] {
			t.Errorf("model saw %q, which is not a Wikipedia plot", in)
		}
		if strings.Contains(in, "Infobox") || strings.Contains(in, "Cast") || strings.Contains(in, "is a film") {
			t.Errorf("model saw text from outside the plot section: %q", in)
		}
	}
}

// TestNoTMDBImports keeps TMDB text out of this package and the embedder by
// construction: neither may import the packages that hold it.
func TestNoTMDBImports(t *testing.T) {
	banned := []string{"internal/platform/tmdb", "internal/movies", "internal/tv", "internal/search",
		"internal/similar", "internal/reco", "internal/onboarding"}
	for _, dir := range []string{".", filepath.Join("..", "embed")} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no Go files in %s: %v", dir, err)
		}
		for _, name := range files {
			f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			for _, imp := range f.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				for _, b := range banned {
					if strings.HasSuffix(path, b) {
						t.Errorf("%s imports %s", name, path)
					}
				}
			}
		}
	}
}
