package imports

import "testing"

// Letterboxd's ratings.csv exports 0.5–5.0 star ratings; they must scale to
// the 0–100 score the tracker stores, and rows without a rating still import
// (as watched, no score).
func TestParse_Letterboxd(t *testing.T) {
	csv := "Date,Name,Year,Letterboxd URI,Rating\n" +
		"2024-01-02,Parasite,2019,https://boxd.it/x,4.5\n" +
		"2024-01-03,The Matrix,1999,https://boxd.it/y,5\n" +
		"2024-01-04,Cats,2019,https://boxd.it/z,\n"

	rows, err := parseImport(SourceLetterboxd, []byte(csv))
	if err != nil {
		t.Fatalf("parseImport: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}

	if rows[0].Title != "Parasite" || rows[0].Year != 2019 {
		t.Errorf("row 0 = %+v, want Parasite 2019", rows[0])
	}
	// 4.5 stars → 90.
	if rows[0].Score == nil || *rows[0].Score != 90 {
		t.Errorf("row 0 score = %v, want 90", rows[0].Score)
	}
	// 5 stars → 100.
	if rows[1].Score == nil || *rows[1].Score != 100 {
		t.Errorf("row 1 score = %v, want 100", rows[1].Score)
	}
	// No rating → nil, still imported.
	if rows[2].Score != nil {
		t.Errorf("row 2 score = %v, want nil", rows[2].Score)
	}
}

// A generic CSV uses a 0–100 rating directly and is tolerant of column order.
func TestParse_GenericCSV(t *testing.T) {
	csv := "title,rating,year\n" +
		"Whiplash,88,2014\n" +
		"Sicario,,2015\n"

	rows, err := parseImport(SourceCSV, []byte(csv))
	if err != nil {
		t.Fatalf("parseImport: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].Title != "Whiplash" || rows[0].Year != 2014 || rows[0].Score == nil || *rows[0].Score != 88 {
		t.Errorf("row 0 = %+v, want Whiplash 2014 score 88", rows[0])
	}
	if rows[1].Score != nil {
		t.Errorf("row 1 score = %v, want nil", rows[1].Score)
	}
}

func TestParse_TraktCSV(t *testing.T) {
	csv := "title,year,rating\nDune,2021,9\n"
	rows, err := parseImport(SourceTrakt, []byte(csv))
	if err != nil {
		t.Fatalf("parseImport: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	// Trakt rates 1–10; ×10 → 90.
	if rows[0].Score == nil || *rows[0].Score != 90 {
		t.Errorf("score = %v, want 90", rows[0].Score)
	}
}

func TestParse_RejectsEmptyAndHeaderOnly(t *testing.T) {
	if _, err := parseImport(SourceCSV, []byte("")); err == nil {
		t.Error("empty file should error")
	}
	if rows, err := parseImport(SourceCSV, []byte("title,year,rating\n")); err != nil {
		t.Errorf("header-only should parse to zero rows, got %v", err)
	} else if len(rows) != 0 {
		t.Errorf("header-only rows = %d, want 0", len(rows))
	}
}

func TestParse_SkipsRowsMissingTitle(t *testing.T) {
	csv := "title,year,rating\n,2019,5\nParasite,2019,5\n"
	rows, err := parseImport(SourceCSV, []byte(csv))
	if err != nil {
		t.Fatalf("parseImport: %v", err)
	}
	if len(rows) != 1 || rows[0].Title != "Parasite" {
		t.Errorf("rows = %+v, want just Parasite (untitled row skipped)", rows)
	}
}
