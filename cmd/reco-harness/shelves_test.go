package main

import "testing"

func TestParseShelves(t *testing.T) {
	got, err := parseShelves(" movie:550, tv:1396 ,")
	if err != nil || len(got) != 2 || got[0] != (shelfTarget{"movie", 550}) || got[1] != (shelfTarget{"tv", 1396}) {
		t.Errorf("parseShelves = %+v, %v", got, err)
	}
	for _, bad := range []string{"", "550", "season:1", "movie:x", "movie:-1"} {
		if _, err := parseShelves(bad); err == nil {
			t.Errorf("parseShelves(%q) should fail", bad)
		}
	}
}
