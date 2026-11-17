package reco

import (
	"math"
	"slices"
)

// TitlePlot is one title's plot vector: the embedding of the plot section of
// its English Wikipedia article, from the plots job. Wikipedia text is CC
// BY-SA, so no TMDB text is in any of these vectors.
type TitlePlot struct {
	MediaType string
	TMDBID    int
	Vector    []float32
}

// PlotIndex holds a unit vector per title whose article has a plot section.
// The vectors are centered on their mean before they are normalized: a small
// sentence model puts every English text in one narrow cone, so raw cosines
// sit between about 0.5 and 0.9 whatever the stories, and centering spreads
// them back out around 0. A nil index holds nothing.
type PlotIndex struct {
	vecs map[factKey][]float32
}

// NewPlotIndex centers and normalizes the titles' vectors. Vectors of another
// length than the first title's, and vectors that center to nothing, are left
// out.
func NewPlotIndex(titles []TitlePlot) *PlotIndex {
	x := &PlotIndex{vecs: make(map[factKey][]float32, len(titles))}
	dims := 0
	for _, t := range titles {
		if len(t.Vector) > 0 {
			dims = len(t.Vector)
			break
		}
	}
	if dims == 0 {
		return x
	}
	mean := make([]float64, dims)
	n := 0
	for _, t := range titles {
		if len(t.Vector) != dims || norm(t.Vector) == 0 {
			continue
		}
		for i, v := range t.Vector {
			mean[i] += float64(v)
		}
		n++
	}
	for i := range mean {
		mean[i] /= float64(n)
	}
	for _, t := range titles {
		if len(t.Vector) != dims || norm(t.Vector) == 0 {
			continue
		}
		c := make([]float64, dims)
		var sq float64
		for i, v := range t.Vector {
			c[i] = float64(v) - mean[i]
			sq += c[i] * c[i]
		}
		if sq < 1e-12 {
			continue
		}
		l := math.Sqrt(sq)
		out := make([]float32, dims)
		for i := range c {
			out[i] = float32(c[i] / l)
		}
		x.vecs[factKey{t.MediaType, t.TMDBID}] = out
	}
	return x
}

func norm(v []float32) float64 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return math.Sqrt(s)
}

// Len is how many titles hold a plot vector.
func (x *PlotIndex) Len() int {
	if x == nil {
		return 0
	}
	return len(x.vecs)
}

// Vector is a title's centered unit plot vector, nil when it has none.
func (x *PlotIndex) Vector(mediaType string, tmdbID int) []float32 {
	if x == nil {
		return nil
	}
	return x.vecs[factKey{mediaType, tmdbID}]
}

const (
	// plotNeighbours is how many of the liked plots nearest a candidate its
	// plot score averages. A taste holds many kinds of story, so a candidate is
	// scored on the few liked titles it is like, not on the mean of them all.
	plotNeighbours = 3
	// maxPlotLikes caps the liked plots a candidate is compared with, keeping
	// the strongest likes, so a long history costs no more than this per title.
	maxPlotLikes = 100
)

// pooledPlots are the plot vectors of the items with positive weight, the
// strongest first, at most maxPlotLikes of them.
func pooledPlots(items []SignalItem) [][]float32 {
	type weighted struct {
		vec []float32
		w   float64
	}
	var ws []weighted
	for _, it := range items {
		if it.Weight <= 0 || it.Features == nil || it.Features.Plot == nil {
			continue
		}
		ws = append(ws, weighted{it.Features.Plot, it.Weight})
	}
	slices.SortStableFunc(ws, func(a, b weighted) int {
		switch {
		case a.w > b.w:
			return -1
		case a.w < b.w:
			return 1
		}
		return 0
	})
	if len(ws) > maxPlotLikes {
		ws = ws[:maxPlotLikes]
	}
	out := make([][]float32, len(ws))
	for i, w := range ws {
		out[i] = w.vec
	}
	return out
}

// plotScore is the mean cosine between the candidate's plot and the
// plotNeighbours liked plots nearest it, each counted as 0 when negative, so
// the score runs from 0 to 1. Fewer liked plots average over as many as
// there are. It is 0 when either side has no plot.
func plotScore(plot []float32, liked [][]float32) float64 {
	if plot == nil || len(liked) == 0 {
		return 0
	}
	var top [plotNeighbours]float64
	for _, l := range liked {
		if len(l) != len(plot) {
			continue
		}
		var s float32
		for i := range plot {
			s += plot[i] * l[i]
		}
		c := float64(s)
		if c <= top[plotNeighbours-1] {
			continue
		}
		j := plotNeighbours - 1
		for j > 0 && top[j-1] < c {
			top[j] = top[j-1]
			j--
		}
		top[j] = c
	}
	k := min(plotNeighbours, len(liked))
	var sum float64
	for _, c := range top[:k] {
		sum += c
	}
	return sum / float64(k)
}

// PlotNeighbour is a title whose story is close to another's, by the cosine
// of their plot vectors.
type PlotNeighbour struct {
	TMDBID int
	Sim    float64
}

// Nearest is up to k titles of the same media type whose plots are nearest
// the title's, nearest first, leaving out the title itself and any with a
// cosine of 0 or less. It is nil when the title has no plot vector.
func (x *PlotIndex) Nearest(mediaType string, tmdbID, k int) []PlotNeighbour {
	src := x.Vector(mediaType, tmdbID)
	if src == nil || k < 1 {
		return nil
	}
	var out []PlotNeighbour
	for key, v := range x.vecs {
		if key.mediaType != mediaType || key.tmdbID == tmdbID {
			continue
		}
		if c := cosine32(src, v); c > 0 {
			out = append(out, PlotNeighbour{TMDBID: key.tmdbID, Sim: c})
		}
	}
	slices.SortFunc(out, func(a, b PlotNeighbour) int {
		switch {
		case a.Sim > b.Sim:
			return -1
		case a.Sim < b.Sim:
			return 1
		}
		return a.TMDBID - b.TMDBID
	})
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// Similarity is the cosine of two titles' plots of one media type, counted
// as 0 when negative or when either has no plot vector.
func (x *PlotIndex) Similarity(mediaType string, a, b int) float64 {
	va, vb := x.Vector(mediaType, a), x.Vector(mediaType, b)
	if va == nil || vb == nil {
		return 0
	}
	return max(0, cosine32(va, vb))
}

// cosine32 is the dot product of two unit vectors of the same length.
func cosine32(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return float64(s)
}
