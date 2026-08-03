package imports

import (
	"bytes"
	"encoding/csv"
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
// tracker's 0–100 scale.
type Row struct {
	Title string
	Year  int
	Score *int
}

// parseImport turns a source file into normalised rows. It is CSV-based for
// all three sources today; the rating column's scale is the only real
// difference (Letterboxd: 0.5–5 stars, Trakt: 1–10, generic: 0–100).
func parseImport(source Source, data []byte) ([]Row, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("import file is empty")
	}

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
			Title: title,
			Year:  atoiSafe(field(rec, cols["year"])),
			Score: parseScore(source, field(rec, cols["rating"])),
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

	v := int(math.Round(scaled))
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return &v
}

func atoiSafe(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
