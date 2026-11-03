package imports

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// zipDir packs every file in dir into a ZIP, the way Trakt hands over its
// export from Settings, Data.
func zipDir(t *testing.T, dir string) []byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	files := map[string]string{}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		files[e.Name()] = string(data)
	}
	return zipFiles(t, files)
}

// zipFiles packs named contents into a ZIP, in name order.
func zipFiles(t *testing.T, files map[string]string) []byte {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip %s: %v", name, err)
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			t.Fatalf("zip %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func intPtr(n int) *int { return &n }

// rowsString prints rows with their scores dereferenced, so a failed
// comparison reads as data rather than pointers.
func rowsString(rows []Row) string {
	var b strings.Builder
	for _, r := range rows {
		score := "none"
		if r.Score != nil {
			score = fmt.Sprint(*r.Score)
		}
		fmt.Fprintf(&b, "\n  %s %q %d tmdb=%d season=%d watched=%d score=%s",
			r.Type, r.Title, r.Year, r.TMDBID, r.Season, r.Watched, score)
	}
	return b.String()
}

func assertRows(t *testing.T, got, want []Row) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows:%s\nwant:%s", rowsString(got), rowsString(want))
	}
}

// The trimmed export holds watched films, a finished miniseries with one
// episode rewatched, three episodes of another show, a special, and ratings
// for a film, a show, a season and an episode. It lands as one row per
// film, per rated show and per season, with the episodes rolled up.
func TestParse_TraktExport(t *testing.T) {
	rows, err := parseImport(SourceTrakt, zipDir(t, "testdata/trakt-export"))
	if err != nil {
		t.Fatalf("parseImport: %v", err)
	}

	assertRows(t, rows, []Row{
		{Title: "Arrival", Year: 2016, Type: "movie", TMDBID: 329865},
		{Title: "Dune", Year: 2021, Type: "movie", TMDBID: 438631, Score: intPtr(90)},
		// No TMDB id in the export, so this one is searched by title.
		{Title: "Obscure Short", Year: 2003, Type: "movie"},
		// Five distinct episodes, though six plays. The season rating rides along.
		{Title: "Chernobyl", Year: 2019, Type: "tv", TMDBID: 87108, Season: 1, Watched: 5, Score: intPtr(100)},
		// The show's own rating, with no season: a series-level signal.
		{Title: "Severance", Year: 2022, Type: "tv", TMDBID: 95396, Score: intPtr(80)},
		// The special (season 0) and the episode rating are left out.
		{Title: "Severance", Year: 2022, Type: "tv", TMDBID: 95396, Season: 1, Watched: 3},
	})
}

// One JSON file from the export, uploaded on its own, reads the same way.
func TestParse_TraktJSONFile(t *testing.T) {
	data, err := os.ReadFile("testdata/trakt-export/watched-history-1.json")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := parseImport(SourceTrakt, data)
	if err != nil {
		t.Fatalf("parseImport: %v", err)
	}

	assertRows(t, rows, []Row{
		{Title: "Arrival", Year: 2016, Type: "movie", TMDBID: 329865},
		{Title: "Dune", Year: 2021, Type: "movie", TMDBID: 438631},
		{Title: "Obscure Short", Year: 2003, Type: "movie"},
		{Title: "Chernobyl", Year: 2019, Type: "tv", TMDBID: 87108, Season: 1, Watched: 5},
	})
}

// Backup tools that call Trakt's API save its watched lists, where a show
// carries its seasons and episodes and a film its play count. Those read
// too.
func TestParse_TraktWatchedLists(t *testing.T) {
	export := zipFiles(t, map[string]string{
		"watched_movies.json": `[
			{"plays": 2, "last_watched_at": "2024-01-01T00:00:00.000Z",
			 "movie": {"title": "Dune", "year": 2021, "ids": {"trakt": 1, "tmdb": 438631}}}
		]`,
		"watched_shows.json": `[
			{"plays": 4, "last_watched_at": "2024-01-01T00:00:00.000Z", "reset_at": null,
			 "show": {"title": "Chernobyl", "year": 2019, "ids": {"trakt": 2, "tmdb": 87108}},
			 "seasons": [
				{"number": 1, "episodes": [
					{"number": 1, "plays": 2, "last_watched_at": "2024-01-01T00:00:00.000Z"},
					{"number": 2, "plays": 1, "last_watched_at": "2024-01-01T00:00:00.000Z"},
					{"number": 3, "plays": 1, "last_watched_at": "2024-01-01T00:00:00.000Z"}
				]}
			 ]}
		]`,
	})
	rows, err := parseImport(SourceTrakt, export)
	if err != nil {
		t.Fatalf("parseImport: %v", err)
	}

	assertRows(t, rows, []Row{
		{Title: "Dune", Year: 2021, Type: "movie", TMDBID: 438631},
		{Title: "Chernobyl", Year: 2019, Type: "tv", TMDBID: 87108, Season: 1, Watched: 3},
	})
}

// A show with no TMDB id still rolls its episodes up, keyed by its title
// and year, and is searched later.
func TestParse_TraktShowWithoutTMDBID(t *testing.T) {
	export := zipFiles(t, map[string]string{
		"watched-history-1.json": `[
			{"watched_at": "2024-01-01T00:00:00.000Z", "type": "episode",
			 "episode": {"season": 2, "number": 1}, "show": {"title": "Local Show", "year": 2015, "ids": {"tmdb": null}}},
			{"watched_at": "2024-01-02T00:00:00.000Z", "type": "episode",
			 "episode": {"season": 2, "number": 2}, "show": {"title": "Local Show", "year": 2015, "ids": {"tmdb": null}}}
		]`,
	})
	rows, err := parseImport(SourceTrakt, export)
	if err != nil {
		t.Fatalf("parseImport: %v", err)
	}

	assertRows(t, rows, []Row{
		{Title: "Local Show", Year: 2015, Type: "tv", Season: 2, Watched: 2},
	})
}

// A ZIP with no JSON in it is not a Trakt export. Saying so beats an import
// that finishes with nothing.
func TestParse_TraktZIPWithoutJSON(t *testing.T) {
	export := zipFiles(t, map[string]string{"ratings.csv": "Date,Name,Year,Rating\n"})
	if _, err := parseImport(SourceTrakt, export); err == nil {
		t.Fatal("a ZIP with no JSON should be refused")
	}
}

// The other sources read a CSV. Given a ZIP, the error says to upload the
// CSV inside it, instead of failing to find a title column in binary.
func TestParse_ZIPForACSVSource(t *testing.T) {
	export := zipFiles(t, map[string]string{"ratings.csv": "Date,Name,Year,Rating\n"})
	for _, source := range []Source{SourceLetterboxd, SourceCSV} {
		_, err := parseImport(source, export)
		if err == nil || !strings.Contains(err.Error(), "CSV") {
			t.Errorf("%s: error = %v, want one asking for the CSV", source, err)
		}
	}
}

// A ZIP is small on the wire but can unpack to far more. Reading stops at
// the limit.
func TestParseTraktExport_StopsAtTheUnpackLimit(t *testing.T) {
	export := zipFiles(t, map[string]string{
		"watched-history-1.json": `[` + strings.Repeat(" ", 4096) + `]`,
	})
	if _, err := parseTraktExport(export, 1024); err == nil {
		t.Fatal("an export over the unpack limit should be refused")
	}
	if _, err := parseTraktExport(export, 1<<20); err != nil {
		t.Fatalf("the same export under the limit: %v", err)
	}
}

// The decoder sees each run of whitespace outside a string as one space, so
// padding costs nothing to skip, while strings keep every byte.
func TestCompactJSON(t *testing.T) {
	in := "[\n  {\"title\" :  \"Two  \\\"  Spaces\" ,\r\n\t\"year\": 2019 }  \n]"
	want := "[ {\"title\" : \"Two  \\\"  Spaces\" , \"year\": 2019 } ]"

	got, err := io.ReadAll(newCompactJSON(strings.NewReader(in)))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("compacted =\n%q\nwant\n%q", got, want)
	}
}

// An export entry is a few hundred bytes. One far larger is refused before
// the decoder holds all of it in memory.
func TestParseTraktExport_RefusesAHugeEntry(t *testing.T) {
	huge := `[{"type": "movie", "rating": 8, "movie": {"title": "` + strings.Repeat("x", maxEntryBytes) + `"}}]`
	export := zipFiles(t, map[string]string{"ratings-movies.json": huge})

	_, err := parseTraktExport(export, maxExportBytes)
	if !errors.Is(err, errEntryTooLarge) {
		t.Fatalf("error = %v, want %v", err, errEntryTooLarge)
	}
}

// The row cap holds while reading, so a crafted export cannot grow the
// roll-up far past what Start would refuse anyway.
func TestParseTraktExport_StopsAtTheRowCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("[")
	for i := range maxImportRows + 1 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"type":"movie","rating":7,"movie":{"title":"Film %d","ids":{"tmdb":%d}}}`, i, i+1)
	}
	b.WriteString("]")
	export := zipFiles(t, map[string]string{"ratings-movies.json": b.String()})

	_, err := parseTraktExport(export, maxExportBytes)
	if !errors.Is(err, ErrTooManyRows) {
		t.Fatalf("error = %v, want %v", err, ErrTooManyRows)
	}
}
