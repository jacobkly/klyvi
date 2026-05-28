package reco

import (
	"context"
	"sort"

	"github.com/google/uuid"
)

// CatalogRepository is the read surface the orchestrator uses to pull
// candidates and feature data from the movies cache. The reco package
// declares the interface; the concrete movies.Repository (Phase 4
// extension) will satisfy it.
type CatalogRepository interface {
	// SampleMovieCandidates returns up to N candidates from the movies cache
	// suitable for ranking. Tier 0 just pages over this; Tier 1 narrows by
	// pre-filter.
	SampleMovieCandidates(ctx context.Context, limit int) ([]Candidate, error)

	// CandidatesByMediaIDs returns Candidates for a specific set of media_ids,
	// used to load features for items in the user's interaction history.
	CandidatesByMediaIDs(ctx context.Context, mediaIDs []int) ([]Candidate, error)

	// LookupReasonNames resolves genre and keyword ids to their human
	// names by scanning the JSONB columns on the movies cache. Missing
	// ids are simply absent from the returned maps — never an error.
	LookupReasonNames(ctx context.Context, genreIDs, keywordIDs []int) (map[int]string, map[int]string, error)
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

// Feed returns the top-K recommendations for a user. The orchestrator picks
// the active scorer based on signal volume:
//   - 0 interactions → Tier 0 (cold).
//   - 1..N interactions → Tier 1 (content-based).
//   - >= N interactions AND Tier 2 ready → Tier 2 (taste profile).
//
// Filtering (already-seen) and diversification wrap whichever tier runs.
func (o *Orchestrator) Feed(ctx context.Context, userID uuid.UUID) ([]Scored, error) {
	interactions, err := o.signal.ListByUser(ctx, userID, 0)
	if err != nil {
		return nil, err
	}
	signalVolume := len(interactions)

	scorer := o.pickScorer(signalVolume)
	if scorer == nil {
		return []Scored{}, nil
	}

	seen, err := o.seen.SeenMediaIDs(ctx, userID)
	if err != nil {
		return nil, err
	}

	user, err := o.buildUserContext(ctx, userID, interactions, seen)
	if err != nil {
		return nil, err
	}

	cands, err := o.catalog.SampleMovieCandidates(ctx, o.cfg.FeedSize*10)
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
	// feed. Tier 0 candidates have nil Features in the synthetic test
	// path, in which case candidateSimilarity returns 0 and MMRReorder
	// degenerates to plain score order.
	ranked := MMRReorder(scored, o.cfg.FeedSize, o.cfg.MMRLambda)

	// Resolve reason names. Done once over the entire feed instead of
	// per-candidate so the lookup is a single DB round-trip regardless
	// of feed size.
	o.resolveReasonNames(ctx, ranked)

	return ranked, nil
}

// resolveReasonNames mutates the scored slice, filling in Reason.Name
// for any (kind,id) the catalog can resolve. Failures are logged
// best-effort and do not block the response — the frontend can render
// the id alone if the name is missing.
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

// pickScorer applies the tier cascade. Slice 4-7/4-8 will introduce
// Tier 1/2; until then everyone gets Tier 0.
func (o *Orchestrator) pickScorer(signalVolume int) Scorer {
	if signalVolume >= 20 && o.tier2 != nil {
		return o.tier2
	}
	if signalVolume >= 1 && o.tier1 != nil {
		return o.tier1
	}
	return o.tier0
}

// buildUserContext turns raw interactions into the SignalItem slices a
// Scorer consumes. Looks up MediaFeatures from the catalog for every
// interacted media id — the cost is bounded by the user's interaction
// count (typically <100).
func (o *Orchestrator) buildUserContext(ctx context.Context, userID uuid.UUID, interactions []InteractionRow, seen map[int]bool) (*UserContext, error) {
	signal := AggregateSignal(o.cfg, interactions)
	liked, disliked := SplitLikedDisliked(signal)

	ids := make([]int, 0, len(liked)+len(disliked))
	for id := range liked {
		ids = append(ids, id)
	}
	for id := range disliked {
		ids = append(ids, id)
	}

	featByID := map[int]*MediaFeatures{}
	if len(ids) > 0 {
		cands, err := o.catalog.CandidatesByMediaIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, c := range cands {
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
	}, nil
}
