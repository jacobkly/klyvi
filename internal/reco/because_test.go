package reco

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func rating(n int) *int { return &n }

func titled(id, tmdb int, title, mediaType string, keywords ...int) Candidate {
	return Candidate{MediaID: id, TMDBID: tmdb, Title: title, MediaType: mediaType,
		Features: &MediaFeatures{MediaID: id, MediaType: mediaType, KeywordIDs: keywords}}
}

func TestAnchors(t *testing.T) {
	interactions := []InteractionRow{
		{MediaID: 1, MediaType: "movie", Kind: "rated", Rating: rating(90)},
		{MediaID: 2, MediaType: "movie", Kind: "rated", Rating: rating(60)}, // not high enough
		{MediaID: 3, MediaType: "movie", Kind: "logged"},                    // not rated
		{MediaID: 4, MediaType: "movie", Kind: "rated", Rating: rating(80)}, // no longer liked
		{MediaID: 51, MediaType: "season", SeriesMediaID: 5, Kind: "rated", Rating: rating(85)},
		{MediaID: 6, MediaType: "movie", Kind: "rated", Rating: rating(100)}, // not in the catalog
	}
	liked := []SignalItem{{MediaID: 1}, {MediaID: 2}, {MediaID: 3}, {MediaID: 5}, {MediaID: 6}}
	byID := map[int]Candidate{
		1: titled(1, 101, "Parasite", "movie", 7),
		2: titled(2, 102, "Okja", "movie", 7),
		3: titled(3, 103, "Mother", "movie", 7),
		4: titled(4, 104, "Snowpiercer", "movie", 7),
		5: titled(5, 105, "Severance", "tv", 7),
	}

	got := Anchors(interactions, liked, byID)
	var names []string
	for _, a := range got {
		names = append(names, a.Title)
	}
	if want := []string{"Parasite", "Severance"}; !reflect.DeepEqual(names, want) {
		t.Errorf("anchors = %v, want %v", names, want)
	}
}

func TestAddBecauseReasons(t *testing.T) {
	// Over a 10,000 title catalog: rare keywords (in 10 titles) are worth
	// about 7.8 each, common ones (in 3,000) about 2.2.
	const rare1, rare2, rare3, common1, common2 = 1, 2, 3, 100, 101
	df := map[int]int{rare1: 10, rare2: 10, rare3: 10, common1: 3000, common2: 3000}
	idf := &MediaIDF{Movie: NewFeatureIDF(nil, df, 10000), TV: NewFeatureIDF(nil, df, 10000)}

	anchors := []Anchor{
		{MediaID: 10, TMDBID: 496243, Title: "Parasite", Keywords: []int{rare1, rare2, common1, common2}},
		{MediaID: 11, TMDBID: 1396, Title: "Breaking Bad", Keywords: []int{rare1, rare2, rare3}},
	}
	featureReasons := []Reason{{Kind: "keyword", ID: rare1}, {Kind: "genre", ID: 18}, {Kind: "genre", ID: 53}}

	tests := []struct {
		name      string
		mediaType string
		keywords  []int
		idf       *MediaIDF
		want      string // the named title, "" for no because reason
	}{
		{"two rare keywords name the title", "movie", []int{rare1, rare2}, idf, "Parasite"},
		{"the title sharing the most weight wins", "movie", []int{rare1, rare2, rare3}, idf, "Breaking Bad"},
		{"a series is explained the same way", "tv", []int{rare1, rare2, rare3}, idf, "Breaking Bad"},
		{"one shared keyword is too weak", "movie", []int{rare1}, idf, ""},
		{"two common keywords are too weak", "movie", []int{common1, common2}, idf, ""},
		{"no keywords, no reason", "movie", nil, idf, ""},
		{"without IDF two shared keywords are too weak", "movie", []int{rare1, rare2}, nil, ""},
		{"without IDF three shared keywords name the title", "movie", []int{rare1, rare2, rare3}, nil, "Breaking Bad"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ranked := []Scored{{
				Candidate: Candidate{MediaID: 99, MediaType: tc.mediaType,
					Features: &MediaFeatures{MediaID: 99, MediaType: tc.mediaType, KeywordIDs: tc.keywords}},
				Reasons: append([]Reason(nil), featureReasons...),
			}}
			AddBecauseReasons(ranked, anchors, tc.idf)

			got := ranked[0].Reasons
			if tc.want == "" {
				if !reflect.DeepEqual(got, featureReasons) {
					t.Errorf("reasons = %v, want them untouched", got)
				}
				return
			}
			if len(got) != maxReasons || got[0].Kind != BecauseKind || got[0].Name != tc.want {
				t.Fatalf("reasons = %v, want %q first and %d in all", got, tc.want, maxReasons)
			}
			if !reflect.DeepEqual(got[1:], featureReasons[:2]) {
				t.Errorf("other reasons = %v, want the two strongest kept", got[1:])
			}
		})
	}
}

// A title is never named as the reason for itself.
func TestAddBecauseReasons_NeverItself(t *testing.T) {
	anchors := []Anchor{{MediaID: 10, TMDBID: 1, Title: "Parasite", Keywords: []int{1, 2, 3}}}
	ranked := []Scored{{Candidate: Candidate{MediaID: 10, MediaType: "movie",
		Features: &MediaFeatures{MediaID: 10, MediaType: "movie", KeywordIDs: []int{1, 2, 3}}}}}
	AddBecauseReasons(ranked, anchors, nil)
	if len(ranked[0].Reasons) != 0 {
		t.Errorf("reasons = %v, want none", ranked[0].Reasons)
	}
}

// titledHistory is a catalog whose lookups by id find the user's titled
// history, as the real adapters do.
type titledHistory struct {
	*stubCatalog
	history []Candidate
}

func (c titledHistory) CandidatesByMediaIDs(_ context.Context, ids []int) ([]Candidate, error) {
	var out []Candidate
	for _, h := range c.history {
		for _, id := range ids {
			if h.MediaID == id {
				out = append(out, h)
			}
		}
	}
	return out, nil
}

// The feed names the highly rated title a pick shares keywords with, and
// leaves a pick that shares nothing unexplained by title.
func TestFeed_ExplainsPicksByATitleTheUserRated(t *testing.T) {
	catalog := titledHistory{
		stubCatalog: &stubCatalog{cands: []Candidate{
			titled(7, 700, "Mother", "movie", 1, 2, 3),
			titled(8, 800, "Paddington", "movie", 9),
		}},
		history: []Candidate{titled(1, 496243, "Parasite", "movie", 1, 2, 3)},
	}
	rows := rowsSignal{{MediaID: 1, MediaType: "movie", Kind: "rated", Rating: rating(95)}}
	scorer := &recordingScorer{}
	orch := NewOrchestrator(catalog, rows, stubSeen{}, DefaultConfig()).
		WithTier0(scorer).WithTier1(scorer).WithTier2(scorer)

	feed, err := orch.Feed(context.Background(), uuid.New(), FeedAll, 10, 0)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	reasons := map[string][]Reason{}
	for _, s := range feed {
		reasons[s.Title] = s.Reasons
	}
	want := []Reason{{Kind: BecauseKind, ID: 496243, Name: "Parasite"}}
	if got := reasons["Mother"]; !reflect.DeepEqual(got, want) {
		t.Errorf("Mother reasons = %v, want %v", got, want)
	}
	if got := reasons["Paddington"]; len(got) != 0 {
		t.Errorf("Paddington reasons = %v, want none", got)
	}
}
