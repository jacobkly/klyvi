package reco

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

const (
	// maxFeedDepth bounds how deep pagination can go — the recommender ranks
	// at most this many items per user, so an offset past it returns an empty
	// page. Beyond a few pages personalized ranking is mostly noise, so this
	// also caps per-request scoring work.
	maxFeedDepth = 200

	// candidatePoolSize is the fixed number of candidates sampled per feed
	// request. It is constant (not page-dependent) on purpose: every page
	// ranks the same pool, so MMR's prefix-stable greedy order lets page 2
	// continue page 1 without dupes.
	candidatePoolSize = maxFeedDepth * 6
)

// CatalogRepository is the read surface the orchestrator uses to pull
// candidates and feature data from the movies and series caches. The reco
// package declares the interface; the API's catalog adapter satisfies it.
type CatalogRepository interface {
	// SampleMovieCandidates returns up to N candidates from the movies cache
	// ranked by popularity. This is the cold-start pool — what a zero-signal
	// user (Tier 0) sees, and the top-up when taste retrieval is thin.
	SampleMovieCandidates(ctx context.Context, limit int) ([]Candidate, error)

	// CandidatesByTaste returns up to `limit` candidates that share at least
	// one of the given genre/keyword ids, above the vote floor. This is the
	// taste-relevant pool for users with signal — the fix for feeds that
	// otherwise only ever rank the globally most-popular movies.
	CandidatesByTaste(ctx context.Context, genreIDs, keywordIDs []int, minVotes, limit int) ([]Candidate, error)

	// SampleSeriesCandidates and SeriesCandidatesByTaste are the two above
	// over the series cache. Genre ids reach SeriesCandidatesByTaste in TV's
	// own genre space (see TVGenresFor).
	SampleSeriesCandidates(ctx context.Context, limit int) ([]Candidate, error)
	SeriesCandidatesByTaste(ctx context.Context, genreIDs, keywordIDs []int, minVotes, limit int) ([]Candidate, error)

	// CandidatesByMediaIDs returns Candidates for a specific set of media_ids,
	// used to load features for items in the user's interaction history.
	CandidatesByMediaIDs(ctx context.Context, mediaIDs []int) ([]Candidate, error)

	// LookupReasonNames resolves genre and keyword ids to their human
	// names by scanning the JSONB columns on the movies cache. Missing
	// ids are simply absent from the returned maps — never an error.
	LookupReasonNames(ctx context.Context, genreIDs, keywordIDs []int) (map[int]string, map[int]string, error)

	// CardDetails returns the card text for the given film and series media
	// ids, keyed by media id. It runs over the ranked feed, never the
	// candidate pool, so the pool query stays narrow. Ids it does not hold are
	// simply absent.
	CardDetails(ctx context.Context, movieIDs, seriesIDs []int) (map[int]CardDetails, error)
}

// SignalRepository pulls the user's interaction history for the signal
// pipeline.
type SignalRepository interface {
	ListByUser(ctx context.Context, userID uuid.UUID, sinceDays int) ([]InteractionRow, error)
}

// SeenRepository returns the set of media_ids the user has touched
// (interactions or tracking). Filtering uses this.
type SeenRepository interface {
	SeenMediaIDs(ctx context.Context, userID uuid.UUID) (map[int]bool, error)
}

// InteractionRow is the orchestrator's projection of an interaction.
// Defined here so reco does not import internal/interactions (which would
// be a cross-domain cycle once interactions wires reco for cold-start
// onboarding).
type InteractionRow struct {
	MediaID   int
	MediaType string
	Kind      string
	Rating    *int
	AgeDays   float64
	// SeriesMediaID is a season's series' media_id, which its signal rolls up
	// to. Zero for any other row, or for a season whose series is not indexed.
	SeriesMediaID int
}

// Orchestrator wires a tier cascade to a user-facing feed.
type Orchestrator struct {
	catalog CatalogRepository
	signal  SignalRepository
	seen    SeenRepository
	cfg     Config

	tier0 Scorer
	tier1 Scorer
	tier2 Scorer

	cache *FeedCache // nil ranks every request afresh

	// reasonIDF weighs the keywords a "because" reason is chosen by. It is
	// loaded whether or not scoring uses IDF. nil counts every keyword the same.
	reasonIDF *MediaIDF
}

func NewOrchestrator(catalog CatalogRepository, signal SignalRepository, seen SeenRepository, cfg Config) *Orchestrator {
	return &Orchestrator{
		catalog: catalog,
		signal:  signal,
		seen:    seen,
		cfg:     cfg,
	}
}

// WithTier0 / WithTier1 / WithTier2 register scorers. Mutating the
// orchestrator after construction is fine because Feed reads them under no
// concurrent writes — wiring happens at startup only.
func (o *Orchestrator) WithTier0(s Scorer) *Orchestrator { o.tier0 = s; return o }
func (o *Orchestrator) WithTier1(s Scorer) *Orchestrator { o.tier1 = s; return o }
func (o *Orchestrator) WithTier2(s Scorer) *Orchestrator { o.tier2 = s; return o }

// WithFeedCache ranks each user's feed to full depth once and serves every
// page from the cache until it expires or is invalidated.
func (o *Orchestrator) WithFeedCache(c *FeedCache) *Orchestrator { o.cache = c; return o }

// WithReasonIDF sets the IDF tables "because you liked" reasons are weighed by.
func (o *Orchestrator) WithReasonIDF(idf *MediaIDF) *Orchestrator { o.reasonIDF = idf; return o }

// Wait blocks until every scorer's background writes finish.
func (o *Orchestrator) Wait() {
	for _, s := range []Scorer{o.tier0, o.tier1, o.tier2} {
		if w, ok := s.(interface{ Wait() }); ok {
			w.Wait()
		}
	}
}

// Feed returns the top-K recommendations of the given media for a user. The
// zero media is the mixed feed. The orchestrator picks the active scorer
// based on signal volume:
//   - 0 interactions → Tier 0 (cold).
//   - 1..N interactions → Tier 1 (content-based).
//   - >= N interactions AND Tier 2 ready → Tier 2 (taste profile).
//
// Filtering (already-seen) and diversification wrap whichever tier runs.
func (o *Orchestrator) Feed(ctx context.Context, userID uuid.UUID, media FeedMedia, limit, offset int) ([]Scored, error) {
	if media == "" {
		media = FeedAll
	}
	if limit <= 0 {
		limit = o.cfg.FeedSize
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= maxFeedDepth {
		return []Scored{}, nil
	}
	want := offset + limit
	if want > maxFeedDepth {
		want = maxFeedDepth
	}

	if o.cache == nil {
		ranked, err := o.rank(ctx, userID, media, want)
		if err != nil {
			return nil, err
		}
		// Resolve names and details for the returned page only — one DB
		// round-trip each regardless of page size, and we never pay for items
		// we don't return.
		page := pageOf(ranked, offset, limit)
		o.resolveReasonNames(ctx, page)
		if err := o.fillCardDetails(ctx, page); err != nil {
			return nil, err
		}
		return page, nil
	}

	ranked, err := o.cache.ranked(ctx, userID, media, func(ctx context.Context) ([]Scored, error) {
		ranked, err := o.rank(ctx, userID, media, maxFeedDepth)
		if err != nil {
			return nil, err
		}
		// Every request shares the cached list, so names and details go in
		// before it is cached and nothing writes to it afterwards.
		o.resolveReasonNames(ctx, ranked)
		if err := o.fillCardDetails(ctx, ranked); err != nil {
			return nil, err
		}
		return ranked, nil
	})
	if err != nil {
		return nil, err
	}
	// A copy, so the caller cannot overwrite or append into the cached list.
	return slices.Clone(pageOf(ranked, offset, limit)), nil
}

// pageOf slices one page out of a ranked list, empty past its end.
func pageOf(ranked []Scored, offset, limit int) []Scored {
	if offset >= len(ranked) {
		return []Scored{}
	}
	return ranked[offset:min(offset+limit, len(ranked))]
}

// rank loads the user's signal, builds and scores the candidate pool of the
// given media, and returns the top depth items in feed order.
func (o *Orchestrator) rank(ctx context.Context, userID uuid.UUID, media FeedMedia, depth int) ([]Scored, error) {
	cfg := o.cfg.ForMedia(media)

	// The history and the seen set are independent reads; load them together
	// so the feed waits for one round trip, not two.
	var (
		interactions []InteractionRow
		seen         map[int]bool
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		interactions, err = o.signal.ListByUser(gctx, userID, 0)
		return err
	})
	g.Go(func() (err error) {
		seen, err = o.seen.SeenMediaIDs(gctx, userID)
		return err
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}

	// The tier reads the signal the scorers will use, so rows that add nothing
	// (a season with no indexed series, a neutral rating) do not promote a user
	// into a tier with an empty taste.
	signal, volume := aggregateSignal(o.cfg, interactions)
	scorer := o.pickScorer(volume)
	if scorer == nil {
		return []Scored{}, nil
	}

	user, history, err := o.buildUserContext(ctx, userID, signal, seen)
	if err != nil {
		return nil, err
	}

	// Build a fixed-size candidate pool: taste-relevant for users with signal,
	// popularity for cold users, topped up so a thin taste match never starves
	// the feed. Size is independent of the requested page so pagination stays
	// stable — every page ranks the same pool, and MMR's greedy selection is
	// prefix-stable, so page 2 continues page 1 without dupes.
	cands, err := BuildCandidatePool(ctx, o.catalog, user.Liked, cfg)
	if err != nil {
		return nil, err
	}

	filtered := cands[:0]
	for _, c := range cands {
		if !seen[c.MediaID] {
			filtered = append(filtered, c)
		}
	}

	scored, err := scorer.Score(ctx, user, filtered)
	if err != nil {
		return nil, err
	}

	sort.Slice(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })

	// MMR rerank trades a little relevance for a noticeably more varied
	// feed, and holds films and series to their shares of it so neither
	// takes over. Tier 0 candidates have nil Features in the synthetic test
	// path, in which case their similarity is 0 and MMR degenerates to plain
	// score order. Rank down to `depth` (the deepest item the caller needs);
	// MMR's greedy order is prefix-stable, so ranking deeper never disturbs
	// earlier pages.
	ranked := MMRReorderWithin(scored, depth, cfg.MMRLambda, FeedTypeBound(cfg, user.Liked))
	AddBecauseReasons(ranked, Anchors(interactions, user.Liked, history), o.reasonIDF)
	return ranked, nil
}

// resolveReasonNames mutates the scored slice, filling in Reason.Name
// for any (kind,id) the catalog can resolve. Name resolution is best-effort:
// a lookup failure is ignored and does not block the response — the frontend
// can render the id alone if the name is missing.
func (o *Orchestrator) resolveReasonNames(ctx context.Context, scored []Scored) {
	// Collect unique ids per kind.
	gSet := map[int]struct{}{}
	kSet := map[int]struct{}{}
	for _, s := range scored {
		for _, r := range s.Reasons {
			switch r.Kind {
			case "genre":
				gSet[r.ID] = struct{}{}
			case "keyword":
				kSet[r.ID] = struct{}{}
			}
		}
	}
	if len(gSet) == 0 && len(kSet) == 0 {
		return
	}

	gIDs := make([]int, 0, len(gSet))
	for id := range gSet {
		gIDs = append(gIDs, id)
	}
	kIDs := make([]int, 0, len(kSet))
	for id := range kSet {
		kIDs = append(kIDs, id)
	}

	gNames, kNames, err := o.catalog.LookupReasonNames(ctx, gIDs, kIDs)
	if err != nil {
		// Best-effort: missing names are not fatal for the feed.
		return
	}
	for i := range scored {
		for j, r := range scored[i].Reasons {
			switch r.Kind {
			case "genre":
				if n, ok := gNames[r.ID]; ok {
					scored[i].Reasons[j].Name = n
				}
			case "keyword":
				if n, ok := kNames[r.ID]; ok {
					scored[i].Reasons[j].Name = n
				}
			}
		}
	}
}

// fillCardDetails sets the synopsis, genre names, and runtime or season count
// on every item. Unlike reason names these are not optional: a card with a
// blank synopsis looks broken, so a failed lookup fails the feed (and is
// never cached). Genres is never nil, so it encodes as [] for an item with no
// details.
func (o *Orchestrator) fillCardDetails(ctx context.Context, scored []Scored) error {
	if len(scored) == 0 {
		return nil
	}
	var movieIDs, seriesIDs []int
	for _, s := range scored {
		if s.MediaType == "tv" {
			seriesIDs = append(seriesIDs, s.MediaID)
		} else {
			movieIDs = append(movieIDs, s.MediaID)
		}
	}
	details, err := o.catalog.CardDetails(ctx, movieIDs, seriesIDs)
	if err != nil {
		return fmt.Errorf("card details: %w", err)
	}
	for i := range scored {
		d := details[scored[i].MediaID]
		scored[i].Overview, scored[i].Runtime, scored[i].Genres = d.Overview, d.Runtime, d.Genres
		scored[i].SeasonCount = d.SeasonCount
		if scored[i].Genres == nil {
			scored[i].Genres = []string{}
		}
	}
	return nil
}

// pickScorer applies the tier cascade to the number of interactions the
// user's signal is built from.
func (o *Orchestrator) pickScorer(signalVolume int) Scorer {
	if signalVolume >= 20 && o.tier2 != nil {
		return o.tier2
	}
	if signalVolume >= 1 && o.tier1 != nil {
		return o.tier1
	}
	return o.tier0
}

// buildUserContext turns the aggregated signal into the SignalItem slices a
// Scorer consumes. Looks up MediaFeatures from the catalog for every media id
// in it (films, and series for rolled-up seasons) — the cost is bounded by
// the user's interaction count (typically <100). It also returns those
// titles' catalog rows by media id, which name the "because" reasons.
func (o *Orchestrator) buildUserContext(ctx context.Context, userID uuid.UUID, signal map[int]float64, seen map[int]bool) (*UserContext, map[int]Candidate, error) {
	liked, disliked := SplitLikedDisliked(signal)

	ids := make([]int, 0, len(liked)+len(disliked))
	for id := range liked {
		ids = append(ids, id)
	}
	for id := range disliked {
		ids = append(ids, id)
	}

	featByID := map[int]*MediaFeatures{}
	history := map[int]Candidate{}
	if len(ids) > 0 {
		cands, err := o.catalog.CandidatesByMediaIDs(ctx, ids)
		if err != nil {
			return nil, nil, err
		}
		for _, c := range cands {
			history[c.MediaID] = c
			if c.Features != nil {
				featByID[c.MediaID] = c.Features
			}
		}
	}

	likedItems := make([]SignalItem, 0, len(liked))
	for id, w := range liked {
		likedItems = append(likedItems, SignalItem{MediaID: id, Weight: w, Features: featByID[id]})
	}
	dislikedItems := make([]SignalItem, 0, len(disliked))
	for id, w := range disliked {
		dislikedItems = append(dislikedItems, SignalItem{MediaID: id, Weight: w, Features: featByID[id]})
	}

	return &UserContext{
		UserID:       userID,
		Liked:        likedItems,
		Disliked:     dislikedItems,
		SeenMediaIDs: seen,
	}, history, nil
}
