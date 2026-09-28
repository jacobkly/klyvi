package reco

import (
	"fmt"
	"math/rand"
	"testing"
)

// referenceMMR is the original allocate-per-comparison MMR, kept as the
// oracle the optimized MMRReorder must match pick for pick.
func referenceMMR(scored []Scored, k int, lambda float64) []Scored {
	if len(scored) == 0 || k <= 0 {
		return nil
	}
	if k > len(scored) {
		k = len(scored)
	}

	selected := make([]Scored, 0, k)
	remaining := make([]Scored, len(scored))
	copy(remaining, scored)

	bestIdx := 0
	for i := 1; i < len(remaining); i++ {
		if remaining[i].Score > remaining[bestIdx].Score {
			bestIdx = i
		}
	}
	selected = append(selected, remaining[bestIdx])
	remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)

	for len(selected) < k && len(remaining) > 0 {
		nextIdx := -1
		bestMMR := -1e18
		for i, c := range remaining {
			var maxSim float64
			for _, s := range selected {
				if sim := referenceSimilarity(c, s); sim > maxSim {
					maxSim = sim
				}
			}
			mmr := lambda*c.Score - (1-lambda)*maxSim
			if mmr > bestMMR {
				bestMMR = mmr
				nextIdx = i
			}
		}
		if nextIdx < 0 {
			break
		}
		selected = append(selected, remaining[nextIdx])
		remaining = append(remaining[:nextIdx], remaining[nextIdx+1:]...)
	}
	return selected
}

func referenceSimilarity(a, b Scored) float64 {
	if a.Features == nil || b.Features == nil {
		return 0
	}
	aSet := make(map[int]bool, len(a.Features.GenreIDs)+len(a.Features.KeywordIDs))
	for _, g := range a.Features.GenreIDs {
		aSet[encodeGenre(g)] = true
	}
	for _, k := range a.Features.KeywordIDs {
		aSet[encodeKeyword(k)] = true
	}
	bSize := len(b.Features.GenreIDs) + len(b.Features.KeywordIDs)
	if bSize == 0 || len(aSet) == 0 {
		return 0
	}
	var inter int
	for _, g := range b.Features.GenreIDs {
		if aSet[encodeGenre(g)] {
			inter++
		}
	}
	for _, k := range b.Features.KeywordIDs {
		if aSet[encodeKeyword(k)] {
			inter++
		}
	}
	return float64(inter) / float64(min(len(aSet), bSize))
}

// randomPool builds n candidates shaped like the real feed pool: 1 to 4
// genres from TMDB's 19, 0 to 40 keywords from a space small enough that
// candidates overlap. dupes adds repeated ids and nil or empty features, the
// edge cases the similarity math has to treat exactly as before.
func randomPool(rng *rand.Rand, n int, dupes bool) []Scored {
	pool := make([]Scored, n)
	for i := range pool {
		f := &MediaFeatures{}
		for g := rng.Intn(4) + 1; g > 0; g-- {
			f.GenreIDs = append(f.GenreIDs, rng.Intn(19)+1)
		}
		for k := rng.Intn(41); k > 0; k-- {
			f.KeywordIDs = append(f.KeywordIDs, rng.Intn(600)+1)
		}
		if dupes {
			switch rng.Intn(10) {
			case 0:
				f = nil
			case 1:
				f = &MediaFeatures{}
			case 2:
				// Same id as both genre and keyword must stay distinct.
				f.KeywordIDs = append(f.KeywordIDs, f.GenreIDs[0], f.GenreIDs[0])
			}
		} else {
			f.GenreIDs = distinct(f.GenreIDs)
			f.KeywordIDs = distinct(f.KeywordIDs)
		}
		pool[i] = Scored{
			Candidate: Candidate{MediaID: i + 1, Features: f},
			// Coarse scores so ties happen and tie-breaking is exercised.
			Score: float64(rng.Intn(200)) / 4,
		}
	}
	return pool
}

func distinct(ids []int) []int {
	seen := make(map[int]bool, len(ids))
	out := ids[:0]
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func TestMMRReorder_MatchesReferenceOrder(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for trial := range 40 {
		dupes := trial%2 == 1
		// Small pools: the reference is quadratic in k and slow by design.
		pool := randomPool(rng, 20+rng.Intn(100), dupes)
		k := 1 + rng.Intn(len(pool)+10) // sometimes past the pool size
		lambda := []float64{0, 0.3, 0.7, 1}[trial%4]

		want := referenceMMR(pool, k, lambda)
		got := MMRReorder(pool, k, lambda)
		if len(got) != len(want) {
			t.Fatalf("trial %d: got %d picks, want %d", trial, len(got), len(want))
		}
		for i := range want {
			if got[i].MediaID != want[i].MediaID {
				t.Fatalf("trial %d (k=%d λ=%.1f dupes=%v): pick %d = id %d, want id %d",
					trial, k, lambda, dupes, i, got[i].MediaID, want[i].MediaID)
			}
		}
	}
}

func TestMMRReorder_LeavesInputUntouched(t *testing.T) {
	pool := randomPool(rand.New(rand.NewSource(7)), 100, false)
	before := make([]int, len(pool))
	for i, s := range pool {
		before[i] = s.MediaID
	}
	MMRReorder(pool, 30, 0.7)
	for i, s := range pool {
		if s.MediaID != before[i] {
			t.Fatalf("input reordered at %d: id %d, want %d", i, s.MediaID, before[i])
		}
	}
}

// BenchmarkMMRReorder ranks a feed-sized pool (1,026 unseen candidates, the
// harness's power user) to one page (30) and to the full feed depth (200).
func BenchmarkMMRReorder(b *testing.B) {
	pool := randomPool(rand.New(rand.NewSource(1)), 1026, false)
	for _, k := range []int{30, maxFeedDepth} {
		b.Run(fmt.Sprintf("k=%d", k), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				MMRReorder(pool, k, 0.7)
			}
		})
	}
}
