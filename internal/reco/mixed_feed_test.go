package reco

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"
)

// mixedCatalog serves films and series, and knows the features of the items
// in a user's history by media id, films and series alike.
type mixedCatalog struct {
	*stubCatalog
	history map[int]*MediaFeatures
}

func (m mixedCatalog) CandidatesByMediaIDs(_ context.Context, ids []int) ([]Candidate, error) {
	var out []Candidate
	for _, id := range ids {
		if f, ok := m.history[id]; ok {
			out = append(out, Candidate{MediaID: id, MediaType: f.MediaType, Features: f})
		}
	}
	return out, nil
}

// pick is one candidate with the features the tests score on. Every pick is
// well voted, so it clears the quality floor.
func pick(id int, mediaType string, genres, keywords []int) Candidate {
	return Candidate{MediaID: id, MediaType: mediaType, Features: &MediaFeatures{
		MediaID: id, MediaType: mediaType, GenreIDs: genres, KeywordIDs: keywords, VoteCount: 1000,
	}}
}

func idsOf(items []Scored) []int {
	ids := make([]int, len(items))
	for i, s := range items {
		ids[i] = s.MediaID
	}
	return ids
}

func countScored(items []Scored, mediaType string) int {
	n := 0
	for _, s := range items {
		if s.MediaType == mediaType {
			n++
		}
	}
	return n
}

// A user who has only rated films still gets the shows that share their
// taste, mixed in among the films, and not the shows that share nothing.
func TestFeed_MovieOnlyUserGetsRelevantShowsMixedIn(t *testing.T) {
	films := []Candidate{
		pick(11, "movie", []int{18}, []int{500}),
		pick(12, "movie", []int{18}, []int{500}),
		pick(13, "movie", []int{18}, []int{501}),
		pick(14, "movie", []int{18}, nil),
		pick(15, "movie", []int{18}, nil),
	}
	series := []Candidate{
		pick(1001, "tv", []int{18}, []int{500, 501}),
		pick(1002, "tv", []int{35}, []int{900}),
	}
	cat := mixedCatalog{
		stubCatalog: &stubCatalog{cands: films, series: series},
		history:     map[int]*MediaFeatures{1: pick(1, "movie", []int{18}, []int{500, 501}).Features},
	}
	rated := rowsSignal{{MediaID: 1, MediaType: "movie", Kind: "rated", Rating: ptrInt(95)}}
	orch := NewOrchestrator(cat, rated, stubSeen{}, DefaultConfig()).WithTier1(NewTier1(DefaultConfig()))

	out, err := orch.Feed(context.Background(), uuid.New(), 10, 0)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	ids := idsOf(out)
	if !slices.Contains(ids, 1001) {
		t.Errorf("feed %v leaves out show 1001, which shares the user's keywords", ids)
	}
	if slices.Contains(ids, 1002) {
		t.Errorf("feed %v holds show 1002, which shares nothing with the user", ids)
	}
	if countScored(out, "movie") == 0 {
		t.Errorf("feed %v holds no films for a film-only user", ids)
	}
	for _, s := range out {
		if s.MediaID == 1001 && s.MediaType != "tv" {
			t.Errorf("show 1001 has media type %q, want tv", s.MediaType)
		}
	}
}

// A user who has only rated seasons gets a feed that opens with shows. Shows
// sharing their series' keywords outrank films sharing only its genre, and
// the series they already watch stays out.
func TestFeed_SeasonsOnlyUserGetsATVLedFeed(t *testing.T) {
	films := []Candidate{
		pick(11, "movie", []int{18}, nil),
		pick(12, "movie", []int{18}, nil),
		pick(13, "movie", []int{18}, nil),
		pick(14, "movie", []int{18}, nil),
	}
	series := []Candidate{
		pick(1000, "tv", []int{18}, []int{600, 601}),
		pick(2000, "tv", []int{18}, []int{600}),
		pick(3000, "tv", []int{18}, []int{601}),
		pick(4000, "tv", []int{18}, []int{600, 601}),
	}
	cat := mixedCatalog{
		stubCatalog: &stubCatalog{cands: films, series: series},
		history:     map[int]*MediaFeatures{1000: series[0].Features},
	}
	seen := seenSet{1000: true, 1001: true, 1002: true, 1003: true}
	orch := NewOrchestrator(cat, rowsSignal(ratedSeasons(3, 90, true)), seen, DefaultConfig()).
		WithTier1(NewTier1(DefaultConfig()))

	out, err := orch.Feed(context.Background(), uuid.New(), 5, 0)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	ids := idsOf(out)
	if len(out) == 0 || out[0].MediaType != "tv" {
		t.Fatalf("feed %v does not open with a show", ids)
	}
	if tv, film := countScored(out, "tv"), countScored(out, "movie"); tv <= film {
		t.Errorf("feed %v holds %d shows and %d films, want shows to lead", ids, tv, film)
	}
	if slices.Contains(ids, 1000) {
		t.Errorf("feed %v recommends series 1000, which the user watches", ids)
	}
}

// A show's card carries its synopsis and genres like a film's, with its
// season count where a film has its runtime. Series details are asked for
// as series.
func TestFeed_TVCardsCarryTheirSeasonCount(t *testing.T) {
	cat := &recordingDetails{stubCatalog: &stubCatalog{
		cands:  []Candidate{{MediaID: 1, MediaType: "movie", VoteAverage: 9}},
		series: []Candidate{{MediaID: 2, MediaType: "tv", VoteAverage: 8}},
		details: map[int]CardDetails{
			1: {Overview: "A detective hunts a killer.", Runtime: 131, Genres: []string{"Crime"}},
			2: {Overview: "A teacher turns to crime.", Genres: []string{"Drama", "Crime"}, SeasonCount: 5},
		},
	}}
	orch := NewOrchestrator(cat, stubSignal{}, stubSeen{}, DefaultConfig()).WithTier0(voteScorer{})

	out, err := orch.Feed(context.Background(), uuid.New(), 10, 0)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("feed = %v, want the film and the show", idsOf(out))
	}
	show := out[1]
	if show.MediaID != 2 || show.SeasonCount != 5 || show.Runtime != 0 ||
		show.Overview != "A teacher turns to crime." || !slices.Equal(show.Genres, []string{"Drama", "Crime"}) {
		t.Errorf("show card = %+v, want its synopsis, genres and 5 seasons", show)
	}
	if film := out[0]; film.Runtime != 131 || film.SeasonCount != 0 {
		t.Errorf("film card runtime %d, seasons %d, want 131 and 0", film.Runtime, film.SeasonCount)
	}
	if !slices.Equal(cat.askedSeries, []int{2}) {
		t.Errorf("asked series details for %v, want [2]", cat.askedSeries)
	}
}
