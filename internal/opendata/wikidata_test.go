package opendata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const entity = "http://www.wikidata.org/entity/"

func row(kv ...string) map[string]map[string]string {
	b := map[string]map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		b[kv[i]] = map[string]string{"value": kv[i+1]}
	}
	return b
}

// fakeService answers the items query and the statements query with the given
// rows, telling them apart by the variables they select.
func fakeService(t *testing.T, items, statements []map[string]map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua != "test-agent/1.0" {
			t.Errorf("user agent = %q", ua)
		}
		q := r.FormValue("query")
		queries = append(queries, q)
		rows := items
		if strings.Contains(q, "?p ?v") {
			rows = statements
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": map[string]any{"bindings": rows}})
	}))
	t.Cleanup(srv.Close)
	return srv, &queries
}

func TestWikidataFacts(t *testing.T) {
	items := []map[string]map[string]string{
		row("tmdb", "550", "item", entity+"Q190050", "article", "https://en.wikipedia.org/wiki/Fight_Club"),
		// A duplicate item claiming the same id loses to the older one.
		row("tmdb", "550", "item", entity+"Q999999"),
		row("tmdb", "603", "item", entity+"Q83495"),
	}
	statements := []map[string]map[string]string{
		row("tmdb", "550", "item", entity+"Q190050", "p", "P57", "v", entity+"Q200566"),
		row("tmdb", "550", "item", entity+"Q190050", "p", "P136", "v", entity+"Q130232"),
		row("tmdb", "550", "item", entity+"Q190050", "p", "P136", "v", entity+"Q2484376"),
		row("tmdb", "550", "item", entity+"Q190050", "p", "P136", "v", entity+"Q130232"),
		row("tmdb", "550", "item", entity+"Q999999", "p", "P136", "v", entity+"Q1"),
		// Unknown values come back as blank nodes and are skipped.
		row("tmdb", "550", "item", entity+"Q190050", "p", "P840", "v", "http://www.wikidata.org/.well-known/genid/abc"),
		row("tmdb", "603", "item", entity+"Q83495", "p", "P161", "v", entity+"Q43416"),
		row("tmdb", "603", "item", entity+"Q83495", "p", "P921", "v", entity+"Q11461"),
		row("tmdb", "603", "item", entity+"Q83495", "p", "P179", "v", entity+"Q16153829"),
		row("tmdb", "603", "item", entity+"Q83495", "p", "P8345", "v", entity+"Q107039178"),
	}
	srv, queries := fakeService(t, items, statements)
	w := NewWikidata(srv.URL, "test-agent/1.0")

	got, err := w.Facts(context.Background(), "movie", []int{550, 603, 13})
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}
	article := "https://en.wikipedia.org/wiki/Fight_Club"
	want := []Facts{
		{MediaType: "movie", TMDBID: 550, WikidataID: 190050, WikipediaURL: &article,
			Genres: []int{130232, 2484376}, Cast: []int{}, Directors: []int{200566}, Subjects: []int{},
			Locations: []int{}, BasedOn: []int{}, Countries: []int{}, Series: []int{}, Franchises: []int{}},
		{MediaType: "movie", TMDBID: 603, WikidataID: 83495,
			Genres: []int{}, Cast: []int{43416}, Directors: []int{}, Subjects: []int{11461},
			Locations: []int{}, BasedOn: []int{}, Countries: []int{}, Series: []int{16153829},
			Franchises: []int{107039178}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("facts =\n%+v\nwant\n%+v", got, want)
	}
	if len(*queries) != 2 {
		t.Fatalf("ran %d queries, want 2", len(*queries))
	}
	for _, q := range *queries {
		if !strings.Contains(q, `"550" "603" "13"`) || !strings.Contains(q, "wdt:P4947") {
			t.Errorf("query does not ask for the film ids by TMDB movie ID:\n%s", q)
		}
	}
}

func TestWikidataFacts_ShowsUseTheTVProperty(t *testing.T) {
	srv, queries := fakeService(t, nil, nil)
	got, err := NewWikidata(srv.URL, "test-agent/1.0").Facts(context.Background(), "tv", []int{1399})
	if err != nil || len(got) != 0 {
		t.Fatalf("Facts = %v, %v; want nothing", got, err)
	}
	if len(*queries) != 1 || !strings.Contains((*queries)[0], "wdt:P4983") {
		t.Errorf("queries = %v, want one items query by TMDB TV series ID", *queries)
	}
}

func TestWikidataFacts_RetriesWhileBusy(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"results":{"bindings":[]}}`))
	}))
	t.Cleanup(srv.Close)
	w := NewWikidata(srv.URL, "test-agent/1.0")
	w.backoff = time.Millisecond

	if _, err := w.Facts(context.Background(), "movie", []int{1}); err != nil {
		t.Fatalf("Facts: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("calls = %d, want a retry after the 429", n)
	}
}

func TestWikidataFacts_ReportsAClientError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad query", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	if _, err := NewWikidata(srv.URL, "test-agent/1.0").Facts(context.Background(), "movie", []int{1}); err == nil {
		t.Fatal("want an error for a 400")
	}
}

func TestItemNumber(t *testing.T) {
	tests := []struct {
		in   string
		want int
		ok   bool
	}{
		{entity + "Q42", 42, true},
		{"Q7", 7, true},
		{"http://www.wikidata.org/.well-known/genid/1f2e", 0, false},
		{entity + "P31", 0, false},
		{"Qabc", 0, false},
	}
	for _, tc := range tests {
		got, ok := itemNumber(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("itemNumber(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
