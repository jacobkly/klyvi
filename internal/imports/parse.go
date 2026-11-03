package imports

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// Source identifies an import file format.
type Source string

const (
	SourceLetterboxd Source = "letterboxd"
	SourceTrakt      Source = "trakt"
	SourceCSV        Source = "csv"
)

// ValidSource reports whether s is a supported source.
func ValidSource(s string) bool {
	switch Source(s) {
	case SourceLetterboxd, SourceTrakt, SourceCSV:
		return true
	default:
		return false
	}
}

// Row is a parsed import line, format-agnostic. Score is nil when the source
// row had no rating (still imported as watched). Score is already on the
// tracker's 0–100 scale. Type is the row's lower-cased type column ("movie",
// "show", "episode"...), empty when the file has none.
//
// TMDBID is the title's TMDB id when the file gives one, so it needs no
// search. Season is the season a show row tracks, 0 for a row about the
// whole show. Watched is how many distinct episodes of that season were
// watched, 0 when the file does not say.
type Row struct {
	Title   string
	Year    int
	Score   *int
	Type    string
	TMDBID  int
	Season  int
	Watched int
}

// isFilm reports whether the row names a film. Files without a type column
// are film lists, so an untyped row is a film.
func (r Row) isFilm() bool {
	switch r.Type {
	case "", "movie", "film":
		return true
	default:
		return false
	}
}

// isShow reports whether the row names a show or one of its seasons.
func (r Row) isShow() bool {
	switch r.Type {
	case "tv", "show", "series", "season":
		return true
	default:
		return false
	}
}

// utf8BOM is the byte order mark some tools write at the start of a file.
var utf8BOM = []byte("\xef\xbb\xbf")

// parseImport turns a source file into normalised rows. Trakt hands over a
// ZIP of JSON files, or one of those files on its own. Everything else is a
// CSV, where the rating column's scale is the real difference between
// sources (Letterboxd: 0.5–5 stars, Trakt: 1–10, generic: 0–100).
func parseImport(source Source, data []byte) ([]Row, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("import file is empty")
	}
	if isZIP(data) {
		if source != SourceTrakt {
			return nil, errors.New("this is a ZIP file. Upload the CSV inside it instead (ratings.csv for Letterboxd)")
		}
		return parseTraktExport(data, maxExportBytes)
	}
	if text := bytes.TrimSpace(bytes.TrimPrefix(data, utf8BOM)); source == SourceTrakt && text[0] == '[' {
		return parseTraktJSON(text)
	}
	return parseCSV(source, data)
}

// parseCSV reads a CSV with a header row. Only a title (or Letterboxd's
// name) column is required.
func parseCSV(source Source, data []byte) ([]Row, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1 // tolerate ragged rows
	r.TrimLeadingSpace = true

	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	cols := indexColumns(header)
	if _, ok := cols["title"]; !ok {
		// Letterboxd names the title column "Name".
		if i, ok := cols["name"]; ok {
			cols["title"] = i
		} else {
			return nil, fmt.Errorf("no title/name column found in header")
		}
	}

	var rows []Row
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read row: %w", err)
		}

		title := strings.TrimSpace(field(rec, cols["title"]))
		if title == "" {
			continue // a row with no title is not importable
		}

		rows = append(rows, Row{
			Title:  title,
			Year:   atoiSafe(field(rec, column(cols, "year"))),
			Score:  parseScore(source, field(rec, column(cols, "rating"))),
			Type:   strings.ToLower(strings.TrimSpace(field(rec, column(cols, "type")))),
			TMDBID: atoiSafe(field(rec, column(cols, "tmdb_id"))),
			Season: atoiSafe(field(rec, column(cols, "season"))),
		})
	}
	return rows, nil
}

// indexColumns maps lower-cased header names to their position.
func indexColumns(header []string) map[string]int {
	m := make(map[string]int, len(header))
	for i, h := range header {
		m[strings.ToLower(strings.TrimSpace(h))] = i
	}
	return m
}

// column is the position of a header name, or -1 when the file lacks it.
func column(cols map[string]int, name string) int {
	if i, ok := cols[name]; ok {
		return i
	}
	return -1
}

func field(rec []string, idx int) string {
	if idx < 0 || idx >= len(rec) {
		return ""
	}
	return rec[idx]
}

// parseScore maps a source-specific rating string to a 0–100 score, or nil
// when absent. Out-of-range results are clamped rather than dropped.
func parseScore(source Source, raw string) *int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil
	}

	var scaled float64
	switch source {
	case SourceLetterboxd:
		scaled = f * 20 // 0.5–5 stars → 10–100
	case SourceTrakt:
		scaled = f * 10 // 1–10 → 10–100
	default:
		scaled = f // already 0–100
	}

	v := clampScore(scaled)
	return &v
}

// clampScore rounds a scaled rating onto the tracker's 0–100.
func clampScore(scaled float64) int {
	v := int(math.Round(scaled))
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return v
}

func atoiSafe(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
