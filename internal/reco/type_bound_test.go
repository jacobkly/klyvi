package reco

import (
	"context"
	"math"
	"slices"
	"testing"

	"github.com/google/uuid"
)

// scoredRun is n items of one type with scores falling from top. They carry
// no features, so MMR orders them by score alone.
func scoredRun(firstID, n int, mediaType string, top float64) []Scored {
	out := make([]Scored, n)
	for i := range out {
		out[i] = Scored{Candidate: Candidate{MediaID: firstID + i, MediaType: mediaType}, Score: top - float64(i)}
	}
	return out
}

// Shows that all outscore the films still fill no more than their share of
// any stretch of the feed from the top: one of the first two, two of the
// first five, four of the first ten. The top show still opens the feed.
func TestMMRReorderWithin_HoldsEachTypeToItsShare(t *testing.T) {
	pool := slices.Concat(scoredRun(1000, 10, "tv", 100), scoredRun(1, 10, "movie", 50))

	got := MMRReorderWithin(pool, 10, 0.7, TypeBound{TV: 0.4, Movie: 1})

	want := []int{1000, 1, 1001, 2, 3, 1002, 4, 1003, 5, 6}
	if !slices.Equal(idsOf(got), want) {
		t.Errorf("feed = %v, want %v", idsOf(got), want)
	}
}

// When the films run out the shows take the rest: the bound orders the feed
// and never shortens it.
func TestMMRReorderWithin_LetsOneTypeTakeTheRest(t *testing.T) {
	pool := slices.Concat(scoredRun(1000, 6, "tv", 100), scoredRun(1, 2, "movie", 50))

	got := MMRReorderWithin(pool, 8, 0.7, TypeBound{TV: 0.4, Movie: 1})

	want := []int{1000, 1, 1001, 2, 1002, 1003, 1004, 1005}
	if !slices.Equal(idsOf(got), want) {
		t.Errorf("feed = %v, want %v", idsOf(got), want)
	}
}

// Ranking deeper never reorders the top, so page 2 continues page 1 without
// repeats or gaps.
func TestMMRReorderWithin_IsPrefixStable(t *testing.T) {
	pool := slices.Concat(scoredRun(1000, 30, "tv", 100), scoredRun(1, 30, "movie", 80))
	bound := TypeBound{TV: 0.4, Movie: 1}

	short := MMRReorderWithin(pool, 10, 0.7, bound)
	long := MMRReorderWithin(pool, 40, 0.7, bound)

	if !slices.Equal(idsOf(short), idsOf(long[:10])) {
		t.Errorf("top 10 = %v, but ranking 40 deep opens %v", idsOf(short), idsOf(long[:10]))
	}
}

// Series get the TV share of the feed and films the rest, unless the user's
// own history leans further one way. Then that type gets the user's share.
func TestFeedTypeBound(t *testing.T) {
	film := SignalItem{MediaID: 1, Weight: 1, Features: &MediaFeatures{MediaType: "movie"}}
	show := SignalItem{MediaID: 2, Weight: 1, Features: &MediaFeatures{MediaType: "tv"}}
	unresolved := SignalItem{MediaID: 3, Weight: 1}

	cases := []struct {
		name  string
		liked []SignalItem
		want  TypeBound
	}{
		{"films only", []SignalItem{film, film}, TypeBound{TV: 0.4, Movie: 1}},
		{"shows only", []SignalItem{show, show}, TypeBound{TV: 1, Movie: 0.6}},
		{"half and half", []SignalItem{film, show}, TypeBound{TV: 0.5, Movie: 0.6}},
		{"no history", nil, TypeBound{TV: 0.4, Movie: 1}},
		{"unresolved items do not count", []SignalItem{show, unresolved}, TypeBound{TV: 1, Movie: 0.6}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FeedTypeBound(DefaultConfig(), tc.liked)
			if math.Abs(got.TV-tc.want.TV) > 1e-9 || math.Abs(got.Movie-tc.want.Movie) > 1e-9 {
				t.Errorf("FeedTypeBound = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A film-only user whose shows all outscore their films still gets a feed
// led by films: shows fill no more than the TV share of its top.
func TestFeed_ShowsDoNotTakeOverAFilmOnlyUsersFeed(t *testing.T) {
	var films, series []Candidate
	for i := range 10 {
		films = append(films, pick(11+i, "movie", []int{18}, nil))
		series = append(series, pick(1001+i, "tv", []int{18}, []int{500, 501}))
	}
	cat := mixedCatalog{
		stubCatalog: &stubCatalog{cands: films, series: series},
		history:     map[int]*MediaFeatures{1: pick(1, "movie", []int{18}, []int{500, 501}).Features},
	}
	rated := rowsSignal{{MediaID: 1, MediaType: "movie", Kind: "rated", Rating: ptrInt(95)}}
	orch := NewOrchestrator(cat, rated, stubSeen{}, DefaultConfig()).WithTier1(NewTier1(DefaultConfig()))

	out, err := orch.Feed(context.Background(), uuid.New(), FeedAll, 10, 0)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(out) != 10 {
		t.Fatalf("feed holds %d items, want 10: %v", len(out), idsOf(out))
	}
	if tv := countScored(out, "tv"); tv == 0 || tv > 4 {
		t.Errorf("top 10 holds %d shows, want 1 to 4: %v", tv, idsOf(out))
	}
}
