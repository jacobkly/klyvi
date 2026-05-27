package reco

import (
	"context"
	"sort"
)

// Tier0 is the cold-start scorer (ARCHITECTURE §5.3). It ignores user
// signal entirely and ranks the catalog by Bayesian-weighted quality (so
// items with few votes are pulled toward the global mean instead of
// rocketing to the top on a single 10/10 vote) plus a genre cap that
// prevents the feed from being five action movies.
type Tier0 struct {
	minVotes    int // m in the Bayesian formula
	maxPerGenre int // diversification cap on the primary genre
}

func NewTier0() *Tier0 {
	return &Tier0{
		minVotes:    25,
		maxPerGenre: 3,
	}
}

func (t *Tier0) Name() string { return "tier0" }

// Score ranks candidates by Bayesian-weighted rating:
//
//	WR = (v / (v + m)) * R + (m / (v + m)) * C
//
// where v is the candidate's vote_count, m is a confidence threshold, R is
// the candidate's vote_average, and C is the catalog's global mean rating.
// After scoring, a single pass caps the number of picks per primary genre
// — the cheapest form of diversification, sufficient at this tier.
func (t *Tier0) Score(ctx context.Context, u *UserContext, cands []Candidate) ([]Scored, error) {
	if len(cands) == 0 {
		return nil, nil
	}

	var sumR float64
	var n int
	for _, c := range cands {
		if c.Features != nil && c.Features.VoteCount > 0 {
			sumR += c.Features.VoteAverage
			n++
		}
	}
	if n == 0 {
		return nil, nil
	}
	globalMean := sumR / float64(n)
	m := float64(t.minVotes)

	type genreScored struct {
		Scored
		primaryGenre int
	}

	scored := make([]genreScored, 0, len(cands))
	for _, c := range cands {
		if c.Features == nil {
			continue
		}
		v := float64(c.Features.VoteCount)
		if v == 0 {
			continue
		}
		R := c.Features.VoteAverage
		wr := (v/(v+m))*R + (m/(v+m))*globalMean

		primary := 0
		if len(c.Features.GenreIDs) > 0 {
			primary = c.Features.GenreIDs[0]
		}

		scored = append(scored, genreScored{
			Scored: Scored{
				Candidate: c,
				Score:     wr,
				Reasons:   []string{"high catalog quality"},
			},
			primaryGenre: primary,
		})
	}

	sort.Slice(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })

	perGenre := make(map[int]int)
	out := make([]Scored, 0, len(scored))
	for _, s := range scored {
		if perGenre[s.primaryGenre] >= t.maxPerGenre {
			continue
		}
		perGenre[s.primaryGenre]++
		out = append(out, s.Scored)
	}
	return out, nil
}
