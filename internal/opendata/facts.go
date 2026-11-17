// Package opendata keeps open facts about catalog titles, from sources whose
// terms allow modelling them: Wikidata (CC0) now, Wikipedia plot text later.
// Titles are matched by the TMDB id the open source records for them, so no
// TMDB content ever leaves Klyvi or reaches a model.
package opendata

import (
	"slices"
	"strconv"
	"strings"
)

// Facts are the Wikidata statements Klyvi keeps about one catalog title. Every
// list holds Wikidata item numbers (Q42 is 42), sorted and without repeats.
type Facts struct {
	MediaType    string  `json:"media_type"`
	TMDBID       int     `json:"tmdb_id"`
	WikidataID   int     `json:"wikidata_id"`
	WikipediaURL *string `json:"wikipedia_url"`
	Genres       []int   `json:"genres"`
	Cast         []int   `json:"cast_members"`
	Directors    []int   `json:"directors"`
	Subjects     []int   `json:"subjects"`
	Locations    []int   `json:"locations"`
	BasedOn      []int   `json:"based_on"`
	Countries    []int   `json:"countries"`
	Series       []int   `json:"series"`
}

// properties maps each Wikidata property Klyvi reads to the list it fills.
var properties = []struct {
	id   string
	list func(*Facts) *[]int
}{
	{"P136", func(f *Facts) *[]int { return &f.Genres }},    // genre
	{"P161", func(f *Facts) *[]int { return &f.Cast }},      // cast member
	{"P57", func(f *Facts) *[]int { return &f.Directors }},  // director
	{"P921", func(f *Facts) *[]int { return &f.Subjects }},  // main subject
	{"P840", func(f *Facts) *[]int { return &f.Locations }}, // narrative location
	{"P144", func(f *Facts) *[]int { return &f.BasedOn }},   // based on
	{"P495", func(f *Facts) *[]int { return &f.Countries }}, // country of origin
	{"P179", func(f *Facts) *[]int { return &f.Series }},    // part of the series
}

// tmdbProperty is the Wikidata property holding a title's TMDB id.
func tmdbProperty(mediaType string) string {
	if mediaType == "tv" {
		return "P4983" // TMDB TV series ID
	}
	return "P4947" // TMDB movie ID
}

// normalize sorts every list and drops repeats, and turns nil lists into empty
// ones, so the same statements always store the same row.
func (f *Facts) normalize() {
	for _, p := range properties {
		l := p.list(f)
		slices.Sort(*l)
		*l = slices.Compact(*l)
		if *l == nil {
			*l = []int{}
		}
	}
}

// itemNumber is the number of a Wikidata item IRI or id: 42 for
// http://www.wikidata.org/entity/Q42 or Q42. It is false for anything else,
// such as the blank node Wikidata uses for an unknown value.
func itemNumber(s string) (int, bool) {
	s = s[strings.LastIndexByte(s, '/')+1:]
	if !strings.HasPrefix(s, "Q") {
		return 0, false
	}
	n, err := strconv.Atoi(s[1:])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
