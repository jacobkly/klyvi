package reco

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"
)

const (
	// maxConcurrentProfileSaves caps background profile writes. A feed that
	// finds every slot busy skips its save; the next feed request saves an
	// equivalent profile anyway.
	maxConcurrentProfileSaves = 4
	profileSaveTimeout        = 10 * time.Second
)

// Tier2 scores candidates against a persisted TasteProfile. The argument
// for Tier 2 over Tier 1: feed requests become a fast scored lookup
// against pre-aggregated weights rather than re-deriving them each call.
// In this implementation the profile is rebuilt from the orchestrator's
// already-loaded user signal on every request and persisted as a side
// effect, after the feed returns — the speed win lands once the read path is
// optimized to skip feature-lookup when the profile is fresh.
type Tier2 struct {
	cfg  Config
	repo ProfileRepository
	idf  *FeatureIDF

	saving  chan struct{}
	pending sync.WaitGroup
}

func NewTier2(cfg Config, repo ProfileRepository) *Tier2 {
	return &Tier2{cfg: cfg, repo: repo, saving: make(chan struct{}, maxConcurrentProfileSaves)}
}

// Wait blocks until background profile saves finish. Tests call it before
// reading the table, and the server calls it while shutting down.
func (t *Tier2) Wait() {
	t.pending.Wait()
}

// WithIDF attaches the same global IDF table Tier 1 uses, so the taste profile
// this tier builds and persists is damped by catalog ubiquity. A nil idf
// leaves profile weights as raw pooled sums. Returns the receiver for chaining.
func (t *Tier2) WithIDF(idf *FeatureIDF) *Tier2 { t.idf = idf; return t }

func (t *Tier2) Name() string { return "tier2" }

func (t *Tier2) Score(ctx context.Context, u *UserContext, cands []Candidate) ([]Scored, error) {
	if u == nil || len(u.Liked) == 0 {
		return nil, nil
	}

	profile := BuildProfile(u.UserID, u.Liked, u.Disliked, t.idf)
	t.saveProfile(profile)

	out := make([]Scored, 0, len(cands))
	for _, c := range cands {
		if c.Features == nil {
			continue
		}
		if c.Features.VoteCount < t.cfg.QualityFloorVotes {
			continue
		}

		gSim := overlapScore(c.Features.GenreIDs, profile.GenreWeights)
		kSim := overlapScore(c.Features.KeywordIDs, profile.KeywordWeights)
		eSim := eraSimilarity(c.Features.Year, profile.EraWeights)

		score := t.cfg.GenreWeight*gSim + t.cfg.KeywordWeight*kSim + t.cfg.EraWeight*eSim
		if score <= 0 {
			continue
		}

		out = append(out, Scored{
			Candidate: c,
			Score:     score,
			Reasons:   topReasons(c.Features, profile.KeywordWeights, profile.GenreWeights),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}

// saveProfile persists the profile in the background. Best-effort: failing to
// save does not touch the feed, the frontend just gets a slightly stale "your
// taste" view. The profile is never mutated after BuildProfile, so the
// goroutine can read it while Score keeps scoring against it.
func (t *Tier2) saveProfile(p *TasteProfile) {
	select {
	case t.saving <- struct{}{}:
	default:
		return
	}

	t.pending.Add(1)
	go func() {
		defer t.pending.Done()
		defer func() { <-t.saving }()

		ctx, cancel := context.WithTimeout(context.Background(), profileSaveTimeout)
		defer cancel()
		if err := t.repo.UpsertProfile(ctx, p); err != nil {
			log.Printf("best-effort taste profile save for %s failed: %v", p.UserID, err)
		}
	}()
}
