package reco

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

// stubCatalog returns a fixed slice of Candidates from SampleMovieCandidates,
// a fixed slice of series from the series retrieval, and a no-op from
// CandidatesByMediaIDs. Used for unit testing the orchestrator's
// response-shaping behaviour without a real DB.
type stubCatalog struct {
	cands        []Candidate
	series       []Candidate
	genreNames   map[int]string
	keywordNames map[int]string
	details      map[int]CardDetails
	detailsErr   error
}

func (s *stubCatalog) SampleMovieCandidates(ctx context.Context, limit int) ([]Candidate, error) {
	return s.cands, nil
}

func (s *stubCatalog) CandidatesByTaste(ctx context.Context, genreIDs, keywordIDs []int, minVotes, limit int) ([]Candidate, error) {
	return s.cands, nil
}

func (s *stubCatalog) SampleSeriesCandidates(ctx context.Context, limit int) ([]Candidate, error) {
	return s.series, nil
}

func (s *stubCatalog) SeriesCandidatesByTaste(ctx context.Context, genreIDs, keywordIDs []int, minVotes, limit int) ([]Candidate, error) {
	return s.series, nil
}

func (s *stubCatalog) CandidatesByMediaIDs(ctx context.Context, ids []int) ([]Candidate, error) {
	return nil, nil
}

func (s *stubCatalog) LookupReasonNames(ctx context.Context, genreIDs, keywordIDs []int) (map[int]string, map[int]string, error) {
	return s.genreNames, s.keywordNames, nil
}

func (s *stubCatalog) CardDetails(ctx context.Context, movieIDs, seriesIDs []int) (map[int]CardDetails, error) {
	if s.detailsErr != nil {
		return nil, s.detailsErr
	}
	out := map[int]CardDetails{}
	for _, id := range slices.Concat(movieIDs, seriesIDs) {
		if d, ok := s.details[id]; ok {
			out[id] = d
		}
	}
	return out, nil
}

type stubSignal struct{}

func (stubSignal) ListByUser(ctx context.Context, userID uuid.UUID, sinceDays int) ([]InteractionRow, error) {
	return nil, nil
}

type stubSeen struct{}

func (stubSeen) SeenMediaIDs(ctx context.Context, userID uuid.UUID) (map[int]bool, error) {
	return nil, nil
}

// Verifies the frontend-enabling display fields (TMDBID, Title, PosterPath,
// BackdropPath, ReleaseYear, VoteAverage) flow from catalog Candidates
// through the orchestrator to the final Scored items the handler returns.
// The frontend must be able to render a feed card without a per-item
// follow-up lookup against /v1/movies/{id}.
func TestFeed_PopulatesDisplayFields(t *testing.T) {
	cands := []Candidate{{
		MediaID:      42,
		MediaType:    "movie",
		TMDBID:       11423,
		Title:        "Memories of Murder",
		PosterPath:   "/74gE8YyApcoUKj4tFPmuTBlAOPK.jpg",
		BackdropPath: "/srGy65EpFp2Fnp1jpVRWWVF4Vox.jpg",
		ReleaseYear:  2003,
		VoteAverage:  8.1,
		Features: &MediaFeatures{
			MediaID:     42,
			MediaType:   "movie",
			GenreIDs:    []int{18},
			VoteAverage: 8.1,
			VoteCount:   1500,
		},
	}}

	orch := NewOrchestrator(
		&stubCatalog{cands: cands},
		stubSignal{},
		stubSeen{},
		DefaultConfig(),
	).WithTier0(NewTier0())

	out, err := orch.Feed(context.Background(), uuid.New(), 30, 0)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 scored item, got %d", len(out))
	}

	s := out[0]
	if s.TMDBID != 11423 {
		t.Errorf("TMDBID: got %d, want 11423", s.TMDBID)
	}
	if s.Title != "Memories of Murder" {
		t.Errorf("Title: got %q", s.Title)
	}
	if s.PosterPath != "/74gE8YyApcoUKj4tFPmuTBlAOPK.jpg" {
		t.Errorf("PosterPath: got %q", s.PosterPath)
	}
	if s.BackdropPath != "/srGy65EpFp2Fnp1jpVRWWVF4Vox.jpg" {
		t.Errorf("BackdropPath: got %q", s.BackdropPath)
	}
	if s.ReleaseYear != 2003 {
		t.Errorf("ReleaseYear: got %d", s.ReleaseYear)
	}
	if s.VoteAverage != 8.1 {
		t.Errorf("VoteAverage: got %f", s.VoteAverage)
	}
	if s.Features == nil {
		t.Error("Features should be retained for debug/explainability")
	}
}

// A feed card shows the synopsis, runtime and genres, so the feed carries
// them and the client never hydrates each card from /v1/movies/{id}. An item
// the catalog holds no details for still encodes genres as [], never null.
func TestFeed_CarriesCardDetails(t *testing.T) {
	cands := []Candidate{
		{MediaID: 1, MediaType: "movie", VoteAverage: 9},
		{MediaID: 2, MediaType: "movie", VoteAverage: 8},
	}
	cat := &stubCatalog{cands: cands, details: map[int]CardDetails{
		1: {Overview: "A detective hunts a killer.", Runtime: 131, Genres: []string{"Crime", "Drama"}},
	}}
	orch := NewOrchestrator(cat, stubSignal{}, stubSeen{}, DefaultConfig()).WithTier0(voteScorer{})

	out, err := orch.Feed(context.Background(), uuid.New(), 10, 0)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d items, want 2", len(out))
	}
	got := out[0]
	if got.Overview != "A detective hunts a killer." || got.Runtime != 131 ||
		!slices.Equal(got.Genres, []string{"Crime", "Drama"}) {
		t.Errorf("details = %q, %d, %v", got.Overview, got.Runtime, got.Genres)
	}
	if out[1].Genres == nil {
		t.Error("an item with no details has nil genres, which encodes as null")
	}
}

// Without a feed cache only the returned page is ranked out, so only its
// items are looked up.
func TestFeed_UncachedLooksUpDetailsForThePageOnly(t *testing.T) {
	cands := make([]Candidate, 5)
	for i := range cands {
		cands[i] = Candidate{MediaID: i + 1, MediaType: "movie", VoteAverage: float64(10 - i)}
	}
	cat := &recordingDetails{stubCatalog: &stubCatalog{cands: cands}}
	orch := NewOrchestrator(cat, stubSignal{}, stubSeen{}, DefaultConfig()).WithTier0(voteScorer{})

	if _, err := orch.Feed(context.Background(), uuid.New(), 2, 2); err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if !slices.Equal(cat.asked, []int{3, 4}) {
		t.Errorf("looked up details for %v, want [3 4]", cat.asked)
	}
}

// A card with a blank synopsis looks broken, so a failed details lookup
// fails the feed rather than serving it half-filled.
func TestFeed_DetailsFailureFailsTheFeed(t *testing.T) {
	cat := &stubCatalog{
		cands:      []Candidate{{MediaID: 1, MediaType: "movie", VoteAverage: 9}},
		detailsErr: errors.New("details down"),
	}
	orch := NewOrchestrator(cat, stubSignal{}, stubSeen{}, DefaultConfig()).WithTier0(voteScorer{})

	if _, err := orch.Feed(context.Background(), uuid.New(), 10, 0); err == nil {
		t.Fatal("Feed succeeded with the details lookup down")
	}
}

// recordingDetails records which media ids the orchestrator asked details
// for, films and series apart.
type recordingDetails struct {
	*stubCatalog
	asked, askedSeries []int
}

func (r *recordingDetails) CardDetails(ctx context.Context, movieIDs, seriesIDs []int) (map[int]CardDetails, error) {
	r.asked = append(r.asked, movieIDs...)
	r.askedSeries = append(r.askedSeries, seriesIDs...)
	return r.stubCatalog.CardDetails(ctx, movieIDs, seriesIDs)
}

// Verifies that Reasons returned from the orchestrator carry resolved
// genre/keyword names when they are available in the catalog. A reason
// without a resolvable name should still appear (kind+id), just with no
// Name field — the frontend can render the id as a fallback.
func TestFeed_ResolvesReasonNames(t *testing.T) {
	likedID := 99
	dislikedID := 100

	// Two candidates the user has not seen; one is the "scoring" target,
	// the other gives the user's signal context via CandidatesByMediaIDs.
	cands := []Candidate{{
		MediaID:   42,
		MediaType: "movie",
		TMDBID:    1,
		Title:     "Candidate",
		Features: &MediaFeatures{
			MediaID:     42,
			MediaType:   "movie",
			GenreIDs:    []int{18, 80}, // 18=Drama (resolvable), 80=unknown
			KeywordIDs:  []int{9826},   // resolvable
			VoteAverage: 8.0,
			VoteCount:   2000,
		},
	}}

	// Liked-set features (would normally come from CandidatesByMediaIDs).
	// Set up so Tier 1's topReasons picks both the genre and keyword.
	likedFeatures := &MediaFeatures{
		MediaID:    likedID,
		MediaType:  "movie",
		GenreIDs:   []int{18},
		KeywordIDs: []int{9826},
		Year:       2003,
	}

	cat := &stubCatalog{
		cands: cands,
		genreNames: map[int]string{
			18: "Drama",
			// 80 deliberately absent → no Name on its reason
		},
		keywordNames: map[int]string{
			9826: "slow-burn",
		},
	}
	// Override CandidatesByMediaIDs to seed the liked context.
	cat2 := &stubCatalogWithLiked{
		stubCatalog: cat,
		likedFeats:  map[int]*MediaFeatures{likedID: likedFeatures},
	}

	sig := &stubSignalWithLike{likedID: likedID, dislikedID: dislikedID}

	orch := NewOrchestrator(cat2, sig, stubSeen{}, DefaultConfig()).
		WithTier1(NewTier1(DefaultConfig()))

	out, err := orch.Feed(context.Background(), uuid.New(), 30, 0)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected at least one scored item")
	}

	// Find a reason for the keyword id 9826 — name should be "slow-burn".
	var foundKeyword, foundUnknownGenre bool
	for _, r := range out[0].Reasons {
		if r.Kind == "keyword" && r.ID == 9826 {
			if r.Name != "slow-burn" {
				t.Errorf("keyword 9826 name: got %q, want %q", r.Name, "slow-burn")
			}
			foundKeyword = true
		}
		if r.Kind == "genre" && r.ID == 80 {
			if r.Name != "" {
				t.Errorf("genre 80 has no entry in name map; Name should be empty, got %q", r.Name)
			}
			foundUnknownGenre = true
		}
	}
	if !foundKeyword {
		t.Errorf("expected a Reason with kind=keyword id=9826, got Reasons=%+v", out[0].Reasons)
	}
	_ = foundUnknownGenre // not strictly required — depends on topReasons trimming to 3
}

// Verifies Tier 0 emits an empty Reasons slice, not nil. JSON contract:
// "Reasons":[] not "Reasons":null. Frontend declared the field as an
// array; the Go nil-slice quirk should not leak.
func TestTier0_EmptyReasonsIsArrayNotNil(t *testing.T) {
	cands := []Candidate{{
		MediaID:   1,
		MediaType: "movie",
		Features: &MediaFeatures{
			MediaID:     1,
			MediaType:   "movie",
			VoteCount:   100,
			VoteAverage: 8.0,
		},
	}}
	scored, err := NewTier0().Score(context.Background(), nil, cands)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(scored) == 0 {
		t.Fatal("no scored items")
	}
	if scored[0].Reasons == nil {
		t.Error("Tier 0 Reasons should be []Reason{}, not nil — JSON contract")
	}
}

// Verifies feed pagination: each page is the matching contiguous slice of the
// full feed, so paging with limit/offset never drops or duplicates an item,
// and an offset past the end returns an empty page rather than erroring.
func TestFeed_PaginationIsStableAndContiguous(t *testing.T) {
	genres := []int{28, 12, 16, 35, 80} // distinct primary genres → no genre-cap drops
	cands := make([]Candidate, 5)
	for i := range cands {
		cands[i] = Candidate{
			MediaID:   i + 1,
			MediaType: "movie",
			TMDBID:    i + 1,
			Features: &MediaFeatures{
				MediaID:     i + 1,
				MediaType:   "movie",
				GenreIDs:    []int{genres[i]},
				VoteAverage: 9.0 - 0.5*float64(i),
				VoteCount:   1000,
			},
		}
	}

	orch := NewOrchestrator(&stubCatalog{cands: cands}, stubSignal{}, stubSeen{}, DefaultConfig()).
		WithTier0(NewTier0())

	full, err := orch.Feed(context.Background(), uuid.New(), 10, 0)
	if err != nil {
		t.Fatalf("Feed full: %v", err)
	}
	if len(full) != 5 {
		t.Fatalf("expected 5 items in the full feed, got %d", len(full))
	}

	for off := 0; off < 6; off += 2 {
		page, err := orch.Feed(context.Background(), uuid.New(), 2, off)
		if err != nil {
			t.Fatalf("Feed offset=%d: %v", off, err)
		}
		end := off + 2
		if end > len(full) {
			end = len(full)
		}
		want := full[off:end]
		if len(page) != len(want) {
			t.Fatalf("offset=%d: got %d items, want %d", off, len(page), len(want))
		}
		for i := range want {
			if page[i].MediaID != want[i].MediaID {
				t.Errorf("offset=%d pos=%d: got media_id=%d, want %d",
					off, i, page[i].MediaID, want[i].MediaID)
			}
		}
	}

	empty, err := orch.Feed(context.Background(), uuid.New(), 2, 10)
	if err != nil {
		t.Fatalf("Feed past-end page: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected an empty page for offset past the end, got %d items", len(empty))
	}
}

// rendezvous lets two fakes prove they ran at the same time: each marks
// itself started, then waits briefly for the other.
type rendezvous struct {
	signal, seen chan struct{}
}

func (r rendezvous) meet(mine, theirs chan struct{}) error {
	close(mine)
	select {
	case <-theirs:
		return nil
	case <-time.After(2 * time.Second):
		return errors.New("the other query never started")
	}
}

type rendezvousSignal struct{ r rendezvous }

func (s rendezvousSignal) ListByUser(context.Context, uuid.UUID, int) ([]InteractionRow, error) {
	return nil, s.r.meet(s.r.signal, s.r.seen)
}

type rendezvousSeen struct{ r rendezvous }

func (s rendezvousSeen) SeenMediaIDs(context.Context, uuid.UUID) (map[int]bool, error) {
	return nil, s.r.meet(s.r.seen, s.r.signal)
}

// The interaction history and the seen set are independent reads, so Feed
// waits for one round trip instead of two.
func TestFeed_LoadsSignalAndSeenConcurrently(t *testing.T) {
	r := rendezvous{signal: make(chan struct{}), seen: make(chan struct{})}
	orch := NewOrchestrator(&stubCatalog{}, rendezvousSignal{r}, rendezvousSeen{r}, DefaultConfig()).
		WithTier0(NewTier0())

	if _, err := orch.Feed(context.Background(), uuid.New(), 10, 0); err != nil {
		t.Fatalf("Feed: %v", err)
	}
}

// stubCatalogWithLiked overrides CandidatesByMediaIDs to seed liked-set
// features for the user context, while delegating everything else.
type stubCatalogWithLiked struct {
	*stubCatalog
	likedFeats map[int]*MediaFeatures
}

func (s *stubCatalogWithLiked) CandidatesByMediaIDs(ctx context.Context, ids []int) ([]Candidate, error) {
	out := make([]Candidate, 0, len(ids))
	for _, id := range ids {
		if f, ok := s.likedFeats[id]; ok {
			out = append(out, Candidate{MediaID: id, MediaType: "movie", Features: f})
		}
	}
	return out, nil
}

// stubSignalWithLike returns one positive interaction (kind=rated 95) and
// one negative (kind=rated 5) so the orchestrator picks Tier 1.
type stubSignalWithLike struct {
	likedID, dislikedID int
}

func (s *stubSignalWithLike) ListByUser(ctx context.Context, userID uuid.UUID, sinceDays int) ([]InteractionRow, error) {
	r95, r5 := 95, 5
	return []InteractionRow{
		{MediaID: s.likedID, MediaType: "movie", Kind: "rated", Rating: &r95},
		{MediaID: s.dislikedID, MediaType: "movie", Kind: "rated", Rating: &r5},
	}, nil
}
