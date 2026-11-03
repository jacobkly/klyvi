package reco

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// countedRepos stands in for the catalog, the history and the seen set at
// once, and counts every load, so a test can prove a cached page touched
// none of them.
type countedRepos struct {
	stubCatalog
	loads atomic.Int32

	mu   sync.Mutex
	seen map[int]bool
}

// newCountedRepos serves n candidates, media ids 1..n, whose vote averages
// fall with the id, so voteScorer ranks them in id order.
func newCountedRepos(n int) *countedRepos {
	r := &countedRepos{seen: map[int]bool{}}
	for i := 1; i <= n; i++ {
		r.cands = append(r.cands, Candidate{MediaID: i, MediaType: "movie", VoteAverage: float64(1000 - i)})
	}
	r.genreNames = map[int]string{18: "Drama"}
	return r
}

func (r *countedRepos) SampleMovieCandidates(context.Context, int) ([]Candidate, error) {
	r.loads.Add(1)
	return r.cands, nil
}

func (r *countedRepos) LookupReasonNames(ctx context.Context, genreIDs, keywordIDs []int) (map[int]string, map[int]string, error) {
	r.loads.Add(1)
	return r.stubCatalog.LookupReasonNames(ctx, genreIDs, keywordIDs)
}

func (r *countedRepos) CardDetails(ctx context.Context, movieIDs, seriesIDs []int) (map[int]CardDetails, error) {
	r.loads.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stubCatalog.CardDetails(ctx, movieIDs, seriesIDs)
}

func (r *countedRepos) failDetails(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.detailsErr = err
}

func (r *countedRepos) ListByUser(context.Context, uuid.UUID, int) ([]InteractionRow, error) {
	r.loads.Add(1)
	return nil, nil
}

func (r *countedRepos) SeenMediaIDs(context.Context, uuid.UUID) (map[int]bool, error) {
	r.loads.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.seen), nil
}

func (r *countedRepos) markSeen(id int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen[id] = true
}

// voteScorer ranks by vote average and gives every item one genre reason. It
// stands in for a real tier so these tests exercise the cache, not scoring.
type voteScorer struct{}

func (voteScorer) Name() string { return "vote" }

func (voteScorer) Score(_ context.Context, _ *UserContext, cands []Candidate) ([]Scored, error) {
	out := make([]Scored, len(cands))
	for i, c := range cands {
		out[i] = Scored{Candidate: c, Score: c.VoteAverage, Reasons: []Reason{{Kind: "genre", ID: 18}}}
	}
	return out, nil
}

func newCachedOrchestrator(repos *countedRepos) (*Orchestrator, *FeedCache) {
	fc := NewFeedCache(time.Minute, 8)
	orch := NewOrchestrator(repos, repos, repos, DefaultConfig()).
		WithTier0(voteScorer{}).
		WithFeedCache(fc)
	return orch, fc
}

func TestFeed_CachedFeedServesLaterPagesWithoutLoading(t *testing.T) {
	repos := newCountedRepos(60)
	orch, _ := newCachedOrchestrator(repos)
	ctx, user := context.Background(), uuid.New()

	first, err := orch.Feed(ctx, user, 20, 0)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	loads := repos.loads.Load()

	second, err := orch.Feed(ctx, user, 20, 20)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if got := repos.loads.Load(); got != loads {
		t.Errorf("page 2 made %d loads, want 0", got-loads)
	}

	// Page 2 continues page 1, and reasons arrive named on both.
	for i, s := range append(first, second...) {
		if s.MediaID != i+1 {
			t.Fatalf("position %d: media_id %d, want %d", i, s.MediaID, i+1)
		}
		if s.Reasons[0].Name != "Drama" {
			t.Fatalf("position %d: reason name %q, want Drama", i, s.Reasons[0].Name)
		}
	}
}

// The cache fill looks details up for the whole ranked list, so every later
// page carries them without another load.
func TestFeed_CachedPagesCarryDetails(t *testing.T) {
	repos := newCountedRepos(30)
	repos.details = map[int]CardDetails{25: {Overview: "Deep in the feed.", Runtime: 90, Genres: []string{"Drama"}}}
	orch, _ := newCachedOrchestrator(repos)
	ctx, user := context.Background(), uuid.New()

	if _, err := orch.Feed(ctx, user, 10, 0); err != nil {
		t.Fatalf("page 1: %v", err)
	}
	loads := repos.loads.Load()
	page3, err := orch.Feed(ctx, user, 10, 20)
	if err != nil {
		t.Fatalf("page 3: %v", err)
	}
	if got := repos.loads.Load(); got != loads {
		t.Errorf("page 3 made %d loads, want 0", got-loads)
	}
	if got := page3[4]; got.MediaID != 25 || got.Overview != "Deep in the feed." || got.Runtime != 90 {
		t.Errorf("position 25 = media %d with %q, %d", got.MediaID, got.Overview, got.Runtime)
	}
}

// A feed whose details failed is never cached: the next request retries.
func TestFeed_DetailsFailureIsNotCached(t *testing.T) {
	repos := newCountedRepos(5)
	repos.details = map[int]CardDetails{1: {Overview: "Top pick."}}
	repos.failDetails(errors.New("details down"))
	orch, _ := newCachedOrchestrator(repos)
	ctx, user := context.Background(), uuid.New()

	if _, err := orch.Feed(ctx, user, 5, 0); err == nil {
		t.Fatal("Feed succeeded with the details lookup down")
	}
	repos.failDetails(nil)
	out, err := orch.Feed(ctx, user, 5, 0)
	if err != nil {
		t.Fatalf("Feed after recovery: %v", err)
	}
	if out[0].Overview != "Top pick." {
		t.Errorf("top overview = %q, want the recovered details", out[0].Overview)
	}
}

func TestFeed_CacheIsPerUser(t *testing.T) {
	repos := newCountedRepos(10)
	orch, _ := newCachedOrchestrator(repos)
	ctx := context.Background()

	if _, err := orch.Feed(ctx, uuid.New(), 5, 0); err != nil {
		t.Fatalf("first user: %v", err)
	}
	loads := repos.loads.Load()
	if _, err := orch.Feed(ctx, uuid.New(), 5, 0); err != nil {
		t.Fatalf("second user: %v", err)
	}
	if repos.loads.Load() == loads {
		t.Error("a second user was served the first user's cached feed")
	}
}

func TestFeed_InvalidateDropsTheCachedFeed(t *testing.T) {
	repos := newCountedRepos(10)
	orch, fc := newCachedOrchestrator(repos)
	ctx, user := context.Background(), uuid.New()

	before, err := orch.Feed(ctx, user, 5, 0)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	top := before[0].MediaID
	repos.markSeen(top)

	// Until the write is reported, the cached feed still serves.
	cached, err := orch.Feed(ctx, user, 5, 0)
	if err != nil {
		t.Fatalf("cached Feed: %v", err)
	}
	if cached[0].MediaID != top {
		t.Fatalf("cached feed changed without an invalidation: top %d, want %d", cached[0].MediaID, top)
	}

	fc.Invalidate(user)
	after, err := orch.Feed(ctx, user, 5, 0)
	if err != nil {
		t.Fatalf("Feed after invalidate: %v", err)
	}
	for _, s := range after {
		if s.MediaID == top {
			t.Fatalf("media %d is seen but still in the feed after Invalidate", top)
		}
	}
}

func TestFeed_CallerCannotAlterTheCachedFeed(t *testing.T) {
	repos := newCountedRepos(10)
	orch, _ := newCachedOrchestrator(repos)
	ctx, user := context.Background(), uuid.New()

	page, err := orch.Feed(ctx, user, 5, 0)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	page[0].MediaID = -1
	_ = append(page, Scored{Candidate: Candidate{MediaID: -2}})

	again, err := orch.Feed(ctx, user, 6, 0)
	if err != nil {
		t.Fatalf("Feed again: %v", err)
	}
	if again[0].MediaID != 1 || again[5].MediaID != 6 {
		t.Errorf("cached feed was altered through a returned page: got ids %d and %d",
			again[0].MediaID, again[5].MediaID)
	}
}

func TestFeedCache_WriteDuringComputeIsNotCached(t *testing.T) {
	fc := NewFeedCache(time.Minute, 8)
	ctx, user := context.Background(), uuid.New()

	started, release := make(chan struct{}), make(chan struct{})
	var computes atomic.Int32
	compute := func(context.Context) ([]Scored, error) {
		if computes.Add(1) == 1 {
			close(started)
			<-release
		}
		return []Scored{}, nil
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := fc.ranked(ctx, user, compute); err != nil {
			t.Errorf("first ranked: %v", err)
		}
	}()
	<-started
	fc.Invalidate(user) // the user rates something while the feed computes
	close(release)
	<-done

	if _, err := fc.ranked(ctx, user, compute); err != nil {
		t.Fatalf("second ranked: %v", err)
	}
	if n := computes.Load(); n != 2 {
		t.Errorf("computes = %d, want 2: a feed computed across a write was cached", n)
	}
}

func TestFeedCache_ConcurrentMissesShareOneCompute(t *testing.T) {
	fc := NewFeedCache(time.Minute, 8)
	ctx, user := context.Background(), uuid.New()

	release := make(chan struct{})
	var computes atomic.Int32
	compute := func(context.Context) ([]Scored, error) {
		computes.Add(1)
		<-release
		return []Scored{}, nil
	}

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := fc.ranked(ctx, user, compute); err != nil {
				t.Errorf("ranked: %v", err)
			}
		}()
	}
	// Give every caller time to arrive while the first is still computing.
	// Arriving late only turns a join into a cache hit, so the count holds
	// either way; the wait just makes the join the path under test.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if n := computes.Load(); n != 1 {
		t.Errorf("computes = %d, want 1", n)
	}
}

func TestFeedCache_JoinerOutlivesALeaderThatHungUp(t *testing.T) {
	fc := NewFeedCache(time.Minute, 8)
	user := uuid.New()

	leaderCtx, hangUp := context.WithCancel(context.Background())
	started := make(chan struct{})
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		_, _ = fc.ranked(leaderCtx, user, func(ctx context.Context) ([]Scored, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		})
	}()
	<-started

	joinerDone := make(chan error, 1)
	go func() {
		ranked, err := fc.ranked(context.Background(), user, func(context.Context) ([]Scored, error) {
			return []Scored{{Score: 1}}, nil
		})
		if err == nil && len(ranked) != 1 {
			err = fmt.Errorf("got %d items, want 1", len(ranked))
		}
		joinerDone <- err
	}()
	// Let the joiner reach the leader's flight before the leader's client
	// disconnects. Arriving late means it computes on its own, which passes
	// too; the wait makes the join the path under test.
	time.Sleep(50 * time.Millisecond)
	hangUp()
	<-leaderDone

	select {
	case err := <-joinerDone:
		if err != nil {
			t.Fatalf("joiner with a live request got: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("joiner never returned")
	}
}

func TestFeedCache_PanickedComputeDoesNotWedgeTheUser(t *testing.T) {
	fc := NewFeedCache(time.Minute, 8)
	ctx, user := context.Background(), uuid.New()

	func() {
		defer func() { _ = recover() }()
		_, _ = fc.ranked(ctx, user, func(context.Context) ([]Scored, error) { panic("scorer bug") })
	}()

	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ran := false
	if _, err := fc.ranked(waitCtx, user, func(context.Context) ([]Scored, error) {
		ran = true
		return []Scored{}, nil
	}); err != nil {
		t.Fatalf("ranked after a panic: %v", err)
	}
	if !ran {
		t.Error("the next request reused the panicked computation instead of computing")
	}
}
