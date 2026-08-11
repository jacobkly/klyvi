package imports

import (
	"reflect"
	"testing"
)

// UnmatchedTitles must round-trip through the JSONB column: Value() produces
// what the DB stores, Scan() reads it back into the same slice.
func TestUnmatchedTitles_RoundTrip(t *testing.T) {
	orig := UnmatchedTitles{
		{Title: "Sátántangó", Year: 1994},
		{Title: "Untitled Short", Year: 0},
	}

	v, err := orig.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}

	var got UnmatchedTitles
	if err := got.Scan(v); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !reflect.DeepEqual(orig, got) {
		t.Errorf("round-trip mismatch:\n orig=%+v\n  got=%+v", orig, got)
	}
}

// A NULL column scans to nil; an empty list values as "[]" so the JSON
// response is [] not null and the column never holds NULL.
func TestUnmatchedTitles_ScanNilAndEmptyValue(t *testing.T) {
	var u UnmatchedTitles
	if err := u.Scan(nil); err != nil {
		t.Fatalf("Scan(nil): %v", err)
	}
	if u != nil {
		t.Errorf("Scan(nil) should yield nil, got %+v", u)
	}

	v, err := UnmatchedTitles{}.Value()
	if err != nil {
		t.Fatalf("Value(empty): %v", err)
	}
	if s, ok := v.(string); !ok || s != "[]" {
		t.Errorf("empty Value should be \"[]\", got %#v", v)
	}
}

// Scan must accept both []byte (pq/pgx) and string sources for the JSONB.
func TestUnmatchedTitles_ScanBytesAndString(t *testing.T) {
	const raw = `[{"title":"Heat","year":1995}]`
	want := UnmatchedTitles{{Title: "Heat", Year: 1995}}

	var fromBytes UnmatchedTitles
	if err := fromBytes.Scan([]byte(raw)); err != nil {
		t.Fatalf("Scan([]byte): %v", err)
	}
	if !reflect.DeepEqual(fromBytes, want) {
		t.Errorf("from []byte: got %+v", fromBytes)
	}

	var fromString UnmatchedTitles
	if err := fromString.Scan(raw); err != nil {
		t.Fatalf("Scan(string): %v", err)
	}
	if !reflect.DeepEqual(fromString, want) {
		t.Errorf("from string: got %+v", fromString)
	}
}
