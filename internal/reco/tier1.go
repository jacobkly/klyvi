package reco

import (
	"context"
	"sort"
)

// Tier1 is the content-based scorer. For each candidate it computes a
// positive-set similarity minus α-weighted negative-set similarity over
// genre + keyword + era features. Keyword overlap dominates by design —
// that is the central differentiator; genre-only matching is what
// incumbents already do.
type Tier1 struct {
	cfg Config
}

func NewTier1(cfg Config) *Tier1 { return &Tier1{cfg: cfg} }

func (t *Tier1) Name() string { return "tier1" }

func (t *Tier1) Score(ctx context.Context, u *UserContext, cands []Candidate) ([]Scored, error) {
	if u == nil || len(u.Liked) == 0 {
		// Nothing to compare against — fall back is the orchestrator's job.
		return nil, nil
	}

	posGenre := pooledFeatureWeights(u.Liked, genreKeyset)
	posKeyword := pooledFeatureWeights(u.Liked, keywordKeyset)
	posEra := pooledEraWeights(u.Liked)

	negGenre := pooledFeatureWeights(u.Disliked, genreKeyset)
	negKeyword := pooledFeatureWeights(u.Disliked, keywordKeyset)
	negEra := pooledEraWeights(u.Disliked)

	out := make([]Scored, 0, len(cands))
	for _, c := range cands {
		if c.Features == nil {
			continue
		}
		if c.Features.VoteCount < t.cfg.QualityFloorVotes {
			continue
		}

		pos := t.scoreAgainst(c.Features, posGenre, posKeyword, posEra)
		neg := t.scoreAgainst(c.Features, negGenre, negKeyword, negEra)
		score := pos - t.cfg.NegativeSimWeight*neg

		if score <= 0 {
			continue
		}

		reasons := topReasons(c.Features, posKeyword, posGenre)
		out = append(out, Scored{Candidate: c, Score: score, Reasons: reasons})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}

// scoreAgainst computes the weighted feature-overlap score for a candidate
// against pooled liked-set (or disliked-set) feature weights.
func (t *Tier1) scoreAgainst(f *MediaFeatures, genreW, keywordW map[int]float64, eraW map[int]float64) float64 {
	gSim := overlapScore(f.GenreIDs, genreW)
	kSim := overlapScore(f.KeywordIDs, keywordW)
	eSim := eraSimilarity(f.Year, eraW)
	return t.cfg.GenreWeight*gSim + t.cfg.KeywordWeight*kSim + t.cfg.EraWeight*eSim
}

// overlapScore is the sum of pooled weights of features the candidate
// shares with the set. Normalized by the candidate's feature count so
// items with many genres/keywords aren't artificially boosted.
func overlapScore(featureIDs []int, pooled map[int]float64) float64 {
	if len(featureIDs) == 0 || len(pooled) == 0 {
		return 0
	}
	var s float64
	for _, id := range featureIDs {
		s += pooled[id]
	}
	return s / float64(len(featureIDs))
}

// eraSimilarity is 1.0 if the candidate's decade matches the pooled
// decade weights' top entry, falling off for adjacent decades.
func eraSimilarity(year int, pooledEra map[int]float64) float64 {
	if year == 0 || len(pooledEra) == 0 {
		return 0
	}
	candDecade := year / 10 * 10

	var s float64
	for d, w := range pooledEra {
		diff := candDecade - d
		if diff < 0 {
			diff = -diff
		}
		switch diff {
		case 0:
			s += w
		case 10:
			s += 0.5 * w
		}
	}
	return s
}

// pooledFeatureWeights sums signal weights across the items for each
// distinct feature id of the requested kind.
func pooledFeatureWeights(items []SignalItem, keyset func(*MediaFeatures) []int) map[int]float64 {
	out := make(map[int]float64)
	for _, it := range items {
		if it.Features == nil {
			continue
		}
		for _, id := range keyset(it.Features) {
			out[id] += it.Weight
		}
	}
	return out
}

func pooledEraWeights(items []SignalItem) map[int]float64 {
	out := make(map[int]float64)
	for _, it := range items {
		if it.Features == nil || it.Features.Year == 0 {
			continue
		}
		decade := it.Features.Year / 10 * 10
		out[decade] += it.Weight
	}
	return out
}

func genreKeyset(f *MediaFeatures) []int   { return f.GenreIDs }
func keywordKeyset(f *MediaFeatures) []int { return f.KeywordIDs }

// topReasons returns up to three structured Reasons identifying the
// features that contributed most to a candidate's score. Name is left
// empty here — the orchestrator resolves names against the catalog after
// all scorers have run, so we only pay for one DB lookup per feed call
// regardless of how many candidates contribute a given feature id.
func topReasons(f *MediaFeatures, posKeyword, posGenre map[int]float64) []Reason {
	type idScore struct {
		id    int
		score float64
		kind  string
	}
	var ranked []idScore
	for _, id := range f.KeywordIDs {
		if w := posKeyword[id]; w > 0 {
			ranked = append(ranked, idScore{id, w, "keyword"})
		}
	}
	for _, id := range f.GenreIDs {
		if w := posGenre[id]; w > 0 {
			ranked = append(ranked, idScore{id, w, "genre"})
		}
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	if len(ranked) > 3 {
		ranked = ranked[:3]
	}
	out := make([]Reason, len(ranked))
	for i, r := range ranked {
		out[i] = Reason{Kind: r.kind, ID: r.id}
	}
	return out
}

// itoa avoids pulling strconv just for reason labels.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
