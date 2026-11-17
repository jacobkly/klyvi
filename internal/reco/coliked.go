package reco

import (
	"context"
	"log"
)

// CoLiked is one title other Klyvi users liked along with a given title: how
// many users liked both, and the cosine of the two titles' sets of likers.
type CoLiked struct {
	ID    int
	Users int
	Sim   float64
}

// CoLikedSource reads, for each of the given titles, the titles most often
// liked by the same users. The co-liked counts table is one.
type CoLikedSource interface {
	CoLiked(ctx context.Context, mediaIDs []int) (map[int][]CoLiked, error)
}

// Shrink is how far a pair seen by users users is trusted, users/(users+k):
// nothing with no users, half at k, close to all far above it. It keeps a pair
// two people happen to share from outweighing the content scores.
func Shrink(users int, k float64) float64 {
	if users <= 0 {
		return 0
	}
	return float64(users) / (float64(users) + k)
}

// CoLikedScores credits each title other users liked along with the liked
// titles: its cosine times Shrink, weighted by the liked title it came from,
// over the total liked weight. A title nobody co-liked is absent.
func CoLikedScores(liked []SignalItem, near map[int][]CoLiked, k float64) map[int]float64 {
	var total float64
	out := map[int]float64{}
	for _, it := range liked {
		if it.Weight <= 0 {
			continue
		}
		total += it.Weight
		for _, n := range near[it.MediaID] {
			out[n.ID] += it.Weight * n.Sim * Shrink(n.Users, k)
		}
	}
	if total == 0 {
		return nil
	}
	for id := range out {
		out[id] /= total
	}
	return out
}

// coLikedScores is the co-liked term for each title, or nil when it is off or
// has no source. A failed read scores nothing, so the feed is what it would
// be with the term off.
func (s sideIndexes) coLikedScores(ctx context.Context, cfg Config, liked []SignalItem) map[int]float64 {
	if cfg.CoLikedWeight == 0 || s.coLiked == nil || len(liked) == 0 {
		return nil
	}
	ids := make([]int, 0, len(liked))
	for _, it := range liked {
		ids = append(ids, it.MediaID)
	}
	near, err := s.coLiked.CoLiked(ctx, ids)
	if err != nil {
		log.Printf("reco: co-liked titles: %v (scoring without them)", err)
		return nil
	}
	return CoLikedScores(liked, near, cfg.CoLikedShrink)
}
