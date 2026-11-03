package reco

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// recordingScorer remembers the user it was asked to score for, so a test
// can tell which tier ran and what taste it was handed.
type recordingScorer struct {
	user *UserContext
}

func (s *recordingScorer) Name() string { return "recording" }

func (s *recordingScorer) Score(_ context.Context, u *UserContext, cands []Candidate) ([]Scored, error) {
	s.user = u
	out := make([]Scored, len(cands))
	for i, c := range cands {
		out[i] = Scored{Candidate: c, Reasons: []Reason{}}
	}
	return out, nil
}

type rowsSignal []InteractionRow

func (r rowsSignal) ListByUser(context.Context, uuid.UUID, int) ([]InteractionRow, error) {
	return r, nil
}

type seenSet map[int]bool

func (s seenSet) SeenMediaIDs(context.Context, uuid.UUID) (map[int]bool, error) {
	return s, nil
}

// seriesCatalog knows features only for series ids (multiples of 1000), so a
// lookup by season id finds nothing.
type seriesCatalog struct {
	*stubCatalog
}

func (s seriesCatalog) CandidatesByMediaIDs(_ context.Context, ids []int) ([]Candidate, error) {
	var out []Candidate
	for _, id := range ids {
		if id%1000 == 0 {
			out = append(out, Candidate{MediaID: id, MediaType: "tv", Features: &MediaFeatures{
				MediaID: id, MediaType: "tv", GenreIDs: []int{18}, KeywordIDs: []int{id},
			}})
		}
	}
	return out, nil
}

// ratedSeasons returns n rated seasons, four to a series: series 1000 holds
// seasons 1001-1004, series 2000 the next four, and so on. withSeries false
// leaves the rows with no series to roll up to.
func ratedSeasons(n, rating int, withSeries bool) []InteractionRow {
	rows := make([]InteractionRow, n)
	for i := range rows {
		series := 1000 * (i/4 + 1)
		rows[i] = InteractionRow{MediaID: series + i%4 + 1, MediaType: "season", Kind: "rated", Rating: ptrInt(rating)}
		if withSeries {
			rows[i].SeriesMediaID = series
		}
	}
	return rows
}

// The tier comes from the interactions the signal uses. Seasons count once
// they roll up to a series, and rows that add nothing do not count.
func TestFeed_SeasonsOnlyUserGetsTheTierTheirSeasonsEarn(t *testing.T) {
	tests := []struct {
		name string
		rows []InteractionRow
		want int
	}{
		{"twenty rated seasons earn tier 2", ratedSeasons(20, 90, true), 2},
		{"three rated seasons earn tier 1", ratedSeasons(3, 90, true), 1},
		{"seasons with no series earn nothing", ratedSeasons(3, 90, false), 0},
		{"neutral season ratings earn nothing", ratedSeasons(3, 50, true), 0},
	}
	pool := []Candidate{{MediaID: 7, MediaType: "movie", Title: "Heat"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tiers := []*recordingScorer{{}, {}, {}}
			orch := NewOrchestrator(seriesCatalog{&stubCatalog{cands: pool}}, rowsSignal(tt.rows), stubSeen{}, DefaultConfig()).
				WithTier0(tiers[0]).WithTier1(tiers[1]).WithTier2(tiers[2])

			if _, err := orch.Feed(context.Background(), uuid.New(), FeedAll, 10, 0); err != nil {
				t.Fatalf("Feed: %v", err)
			}
			for tier, s := range tiers {
				if ran := s.user != nil; ran != (tier == tt.want) {
					t.Errorf("tier %d ran = %v, want tier %d to run", tier, ran, tt.want)
				}
			}
		})
	}
}

// A seasons-only user's taste is their series, with its TV features, rather
// than the empty taste the season rows used to leave behind.
func TestFeed_SeasonsOnlyUserTastesOfTheirSeries(t *testing.T) {
	tier1 := &recordingScorer{}
	orch := NewOrchestrator(seriesCatalog{&stubCatalog{}}, rowsSignal(ratedSeasons(3, 90, true)), stubSeen{}, DefaultConfig()).
		WithTier1(tier1)

	if _, err := orch.Feed(context.Background(), uuid.New(), FeedAll, 10, 0); err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if tier1.user == nil {
		t.Fatal("tier 1 did not run")
	}
	liked := tier1.user.Liked
	if len(liked) != 1 || liked[0].MediaID != 1000 {
		t.Fatalf("liked = %+v, want series 1000 alone", liked)
	}
	if f := liked[0].Features; f == nil || f.MediaType != "tv" {
		t.Errorf("series 1000 features = %+v, want its TV features", f)
	}
}

// The seen set names a series once any season of it is tracked (the seen
// adapter expands it), and a seen series never reaches the feed.
func TestFeed_NeverRecommendsASeenSeries(t *testing.T) {
	voted := func(id int, mediaType string) *MediaFeatures {
		return &MediaFeatures{MediaID: id, MediaType: mediaType, VoteAverage: 8.5, VoteCount: 5000}
	}
	pool := []Candidate{
		{MediaID: 1000, MediaType: "tv", Title: "Breaking Bad", Features: voted(1000, "tv")},
		{MediaID: 7, MediaType: "movie", Title: "Heat", Features: voted(7, "movie")},
	}
	orch := NewOrchestrator(&stubCatalog{cands: pool}, stubSignal{}, seenSet{1000: true, 1001: true}, DefaultConfig()).
		WithTier0(NewTier0())

	out, err := orch.Feed(context.Background(), uuid.New(), FeedAll, 10, 0)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	for _, s := range out {
		if s.MediaID == 1000 {
			t.Fatalf("feed recommends %q, a series the user is tracking", s.Title)
		}
	}
	if len(out) != 1 {
		t.Errorf("feed = %d items, want the one unseen movie", len(out))
	}
}
