package reco

import "math"

// Weight computes the signed strength of a single interaction per
// ARCHITECTURE §5.2:
//
//	weight = base(kind) * rating_adjust(rating) * recency_decay(age)
//
// Positive means "like" signal, negative means "dislike". rating_adjust is
// neutral 0 at rating=50, +1 at rating=100, −1 at rating=0 — so a low
// rating produces NEGATIVE signal, not just weak positive (the common bug
// the architecture explicitly warns against).
func Weight(cfg Config, kind string, rating *int, ageDays float64) float64 {
	base := cfg.baseForKind(kind)
	if base == 0 {
		return 0
	}

	ratingAdj := 1.0
	if kind == "rated" && rating != nil {
		ratingAdj = float64(*rating-50) / 50.0
	}

	if ageDays < 0 {
		ageDays = 0
	}
	decay := math.Exp(-ageDays / cfg.HalfLifeDays * math.Ln2)

	return base * ratingAdj * decay
}

func (c Config) baseForKind(kind string) float64 {
	switch kind {
	case "logged":
		return c.BaseLogged
	case "rated":
		return c.BaseRated
	case "dismissed":
		return c.BaseDismissed
	case "saved":
		return c.BaseSaved
	case "clicked":
		return c.BaseClicked
	case "impression":
		return c.BaseImpression
	}
	return 0
}

// AggregateSignal turns a slice of interactions into per-media_id signed
// signal strength. Multiple interactions for the same media combine
// additively (after each is recency-decayed). Movies-only this phase —
// season rows are skipped (rollup deferred to Phase 4b per §1.3).
func AggregateSignal(cfg Config, interactions []InteractionRow) map[int]float64 {
	out := make(map[int]float64, len(interactions))
	for _, it := range interactions {
		if it.MediaType != "movie" {
			continue
		}
		w := Weight(cfg, it.Kind, it.Rating, it.AgeDays)
		if w == 0 {
			continue
		}
		out[it.MediaID] += w
	}
	return out
}

// SplitLikedDisliked separates the aggregated signal into positive and
// negative buckets for Tier 1/2 scoring. Items with zero net weight are
// dropped — neither liked nor disliked.
func SplitLikedDisliked(signal map[int]float64) (liked map[int]float64, disliked map[int]float64) {
	liked = make(map[int]float64)
	disliked = make(map[int]float64)
	for id, w := range signal {
		switch {
		case w > 0:
			liked[id] = w
		case w < 0:
			disliked[id] = -w // store magnitudes; sign carried by which map you're in
		}
	}
	return liked, disliked
}
