package reco

import "math"

// Weight computes the signed strength of a single interaction:
//
//	weight = base(kind) * rating_adjust(rating) * recency_decay(age)
//
// Positive means "like" signal, negative means "dislike". rating_adjust is
// neutral 0 at rating=50, +1 at rating=100, −1 at rating=0 — so a low
// rating produces NEGATIVE signal, not just weak positive. Treating every
// rating as positive is a common, real bug; this function exists in part
// to make sure the codebase does not silently regress into it.
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
// additively (after each is recency-decayed).
//
// A season's signal rolls up to its series as the mean over the seasons the
// user has signal on, so a strong first season and a weak fourth average out
// instead of counting as two votes for the show. Series-level interactions
// add to that mean directly. A season with no indexed series is skipped.
func AggregateSignal(cfg Config, interactions []InteractionRow) map[int]float64 {
	signal, _ := aggregateSignal(cfg, interactions)
	return signal
}

// aggregateSignal is AggregateSignal plus the number of interactions the
// signal used, which is the volume the tier cascade reads.
func aggregateSignal(cfg Config, interactions []InteractionRow) (map[int]float64, int) {
	out := make(map[int]float64, len(interactions))
	seasons := make(map[int]map[int]float64) // series media_id → season media_id → weight
	used := 0
	for _, it := range interactions {
		switch it.MediaType {
		case "movie", "tv":
		case "season":
			if it.SeriesMediaID == 0 {
				continue
			}
		default:
			continue
		}
		w := Weight(cfg, it.Kind, it.Rating, it.AgeDays)
		if w == 0 {
			continue
		}
		used++
		if it.MediaType != "season" {
			out[it.MediaID] += w
			continue
		}
		bySeason := seasons[it.SeriesMediaID]
		if bySeason == nil {
			bySeason = make(map[int]float64)
			seasons[it.SeriesMediaID] = bySeason
		}
		bySeason[it.MediaID] += w
	}
	for series, bySeason := range seasons {
		var sum float64
		for _, w := range bySeason {
			sum += w
		}
		out[series] += sum / float64(len(bySeason))
	}
	return out, used
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
