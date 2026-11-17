package reco

import "sort"

// A "because" reason names a title the user rated highly that a pick shares
// keywords with: "because you liked Parasite". It is explanation only and
// never moves a pick.
const (
	// BecauseKind is the Reason.Kind of a "because you liked" reason. Its ID
	// is the title's TMDB id and its Name the title.
	BecauseKind = "because"

	// becauseMinRating is the lowest rating, out of 100, that makes a title
	// one the user liked enough to be named.
	becauseMinRating = 70

	// A pick must share at least becauseMinShared keywords with the title,
	// worth at least becauseMinWeight in IDF. A keyword in 1% of the catalog
	// is worth about 5.6 and one in 10% about 3.3, so two rare keywords pass
	// and two everyday ones ("based on novel or book", "woman director") do
	// not. In the harness feeds 9 named a title on 82% of picks, some of them
	// loose (Dune for a sitcom), 12 on 59% with those gone, and 15 began to
	// drop good pairs. Without IDF tables every keyword is worth 1, so the
	// bar is becauseMinSharedNoIDF shared keywords instead.
	becauseMinShared      = 2
	becauseMinWeight      = 12.0
	becauseMinSharedNoIDF = 3

	// maxReasons is how many reasons a pick carries in all.
	maxReasons = 3
)

// Anchor is a title the user rated highly, which picks can be explained by.
type Anchor struct {
	MediaID  int
	TMDBID   int
	Title    string
	Keywords []int
}

// Anchors returns the titles in the user's history rated becauseMinRating or
// more, a rated season standing for its series, that are still liked overall
// and that byID holds. They come back in media id order so ties between them
// resolve the same way every time.
func Anchors(interactions []InteractionRow, liked []SignalItem, byID map[int]Candidate) []Anchor {
	stillLiked := make(map[int]bool, len(liked))
	for _, it := range liked {
		stillLiked[it.MediaID] = true
	}
	rated := map[int]bool{}
	for _, it := range interactions {
		if it.Kind != "rated" || it.Rating == nil || *it.Rating < becauseMinRating {
			continue
		}
		id := it.MediaID
		if it.MediaType == "season" {
			id = it.SeriesMediaID
		}
		if id != 0 && stillLiked[id] {
			rated[id] = true
		}
	}

	out := make([]Anchor, 0, len(rated))
	for id := range rated {
		c, ok := byID[id]
		if !ok || c.Title == "" || c.Features == nil || len(c.Features.KeywordIDs) == 0 {
			continue
		}
		out = append(out, Anchor{MediaID: id, TMDBID: c.TMDBID, Title: c.Title, Keywords: c.Features.KeywordIDs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MediaID < out[j].MediaID })
	return out
}

// AddBecauseReasons puts a "because" reason first on each pick that shares
// enough keywords with one of the anchors, naming the anchor it shares the
// most IDF weight with. Keywords are weighed by the pick's own type's table.
// A pick keeps its strongest other reasons, up to maxReasons in all.
func AddBecauseReasons(ranked []Scored, anchors []Anchor, idf *MediaIDF) {
	if len(anchors) == 0 {
		return
	}
	byKeyword := map[int][]int{} // keyword id to the anchors carrying it
	for i, a := range anchors {
		for _, k := range a.Keywords {
			byKeyword[k] = append(byKeyword[k], i)
		}
	}

	weight := make([]float64, len(anchors))
	shared := make([]int, len(anchors))
	for i := range ranked {
		f := ranked[i].Features
		if f == nil || len(f.KeywordIDs) == 0 {
			continue
		}
		table := idf.For(f.MediaType)
		clear(weight)
		clear(shared)
		for _, k := range f.KeywordIDs {
			for _, a := range byKeyword[k] {
				weight[a] += table.KeywordWeight(k)
				shared[a]++
			}
		}

		best := -1
		for a := range anchors {
			if anchors[a].MediaID == f.MediaID || !strongOverlap(shared[a], weight[a], table != nil) {
				continue
			}
			if best < 0 || weight[a] > weight[best] {
				best = a
			}
		}
		if best < 0 {
			continue
		}
		because := Reason{Kind: BecauseKind, ID: anchors[best].TMDBID, Name: anchors[best].Title}
		rest := ranked[i].Reasons
		if len(rest) > maxReasons-1 {
			rest = rest[:maxReasons-1]
		}
		ranked[i].Reasons = append([]Reason{because}, rest...)
	}
}

// strongOverlap is whether a pick shares enough with an anchor to name it.
func strongOverlap(shared int, weight float64, weighted bool) bool {
	if !weighted {
		return shared >= becauseMinSharedNoIDF
	}
	return shared >= becauseMinShared && weight >= becauseMinWeight
}
