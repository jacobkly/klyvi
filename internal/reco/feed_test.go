package reco

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// stubCatalog returns a fixed slice of Candidates from SampleMovieCandidates
// and a no-op from CandidatesByMediaIDs. Used for unit testing the
// orchestrator's response-shaping behaviour without a real DB.
type stubCatalog struct {
	cands         []Candidate
	genreNames    map[int]string
	keywordNames  map[int]string
}

func (s *stubCatalog) SampleMovieCandidates(ctx context.Context, limit int) ([]Candidate, error) {
	return s.cands, nil
}

func (s *stubCatalog) CandidatesByMediaIDs(ctx context.Context, ids []int) ([]Candidate, error) {
	return nil, nil
}

func (s *stubCatalog) LookupReasonNames(ctx context.Context, genreIDs, keywordIDs []int) (map[int]string, map[int]string, error) {
	return s.genreNames, s.keywordNames, nil
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

	out, err := orch.Feed(context.Background(), uuid.New())
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

	out, err := orch.Feed(context.Background(), uuid.New())
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
