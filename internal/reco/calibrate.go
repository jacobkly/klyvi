package reco

import (
	"math"
	"sort"
)

// Calibration holds a feed's genre mix near the mix of what the user likes
// (Steck, "Calibrated Recommendations", 2018). If 30% of the likes are horror,
// about 30% of the feed is. The miscalibration is the KL divergence from the
// target mix to the feed's mix, and MMR pays Weight times the pool's top
// score for every nat of it, so the weight means the same at every tier's
// score scale. A zero Weight or an empty Target turns it off.
type Calibration struct {
	Weight float64
	Target map[int]float64 // genre id to its share of the likes, summing to 1
}

// calibrationSmoothing mixes a little of the target into the feed's mix, so a
// genre the feed lacks costs a large but finite amount (Steck's α).
const calibrationSmoothing = 0.01

func (c Calibration) on() bool { return c.Weight > 0 && len(c.Target) > 0 }

// FeedCalibration is the calibration a feed for these likes runs with.
func FeedCalibration(cfg Config, liked []SignalItem) Calibration {
	if cfg.CalibrationWeight <= 0 {
		return Calibration{}
	}
	return Calibration{Weight: cfg.CalibrationWeight, Target: GenreTarget(liked)}
}

// GenreTarget is the genre mix of the user's likes. Each like spreads its
// weight evenly over its genres. Likes with no genres add nothing.
func GenreTarget(liked []SignalItem) map[int]float64 {
	target := map[int]float64{}
	total := 0.0
	for _, li := range liked {
		if li.Features == nil || len(li.Features.GenreIDs) == 0 || li.Weight <= 0 {
			continue
		}
		share := li.Weight / float64(len(li.Features.GenreIDs))
		for _, g := range li.Features.GenreIDs {
			target[g] += share
		}
		total += li.Weight
	}
	if total == 0 {
		return nil
	}
	for g := range target {
		target[g] /= total
	}
	return target
}

// CalibrationError is the KL divergence from the target genre mix to the mix
// of the picks, each pick spreading one unit over its genres. 0 is a perfect
// match. It is 0 when there is no target.
func CalibrationError(target map[int]float64, picks []Scored) float64 {
	if len(target) == 0 {
		return 0
	}
	m := newGenreMix(target)
	for _, p := range picks {
		m.add(p.Features)
	}
	return m.divergence()
}

// genreMix is the running genre mix of the picks so far, kept against one
// target. Genres are held in sorted order so sums come out the same each run.
type genreMix struct {
	genres []int
	target []float64   // the target share of each of genres
	index  map[int]int // genre id to its place in genres
	counts []float64   // mass picked in each target genre
	mass   float64     // all mass picked, target genres or not
	base   []float64   // per step: each genre's smoothed mass if one more unit lands elsewhere
	logSum float64     // per step: Σ target·log(base)
	plogp  float64     // Σ target·log(target), the divergence's constant part
}

func newGenreMix(target map[int]float64) *genreMix {
	m := &genreMix{index: make(map[int]int, len(target))}
	for g, p := range target {
		if p > 0 {
			m.genres = append(m.genres, g)
		}
	}
	sort.Ints(m.genres)
	m.target = make([]float64, len(m.genres))
	for i, g := range m.genres {
		m.index[g] = i
		m.target[i] = target[g]
		m.plogp += m.target[i] * math.Log(m.target[i])
	}
	m.counts = make([]float64, len(m.genres))
	m.base = make([]float64, len(m.genres))
	return m
}

func (m *genreMix) add(f *MediaFeatures) {
	if f == nil || len(f.GenreIDs) == 0 {
		return
	}
	share := 1 / float64(len(f.GenreIDs))
	for _, g := range f.GenreIDs {
		if i, ok := m.index[g]; ok {
			m.counts[i] += share
		}
	}
	m.mass++
}

// smoothed is genre i's share of the mix in unnormalised form: the picked
// mass mixed with the target at the smoothing rate, for a list of the given
// total mass. Dividing by mass gives Steck's q~.
func (m *genreMix) smoothed(i int, counts, mass float64) float64 {
	return (1-calibrationSmoothing)*counts + calibrationSmoothing*m.target[i]*mass
}

// divergence is KL(target || q~) of the picks so far.
func (m *genreMix) divergence() float64 {
	if m.mass == 0 {
		return 0
	}
	kl := m.plogp + math.Log(m.mass)
	for i := range m.genres {
		kl -= m.target[i] * math.Log(m.smoothed(i, m.counts[i], m.mass))
	}
	return kl
}

// prepare readies divergenceWith for the next pick. Every candidate with
// genres grows the mix's mass by one, so the part of the sum each one shares
// is worked out once per step.
func (m *genreMix) prepare() {
	next := m.mass + 1
	m.logSum = 0
	for i := range m.genres {
		m.base[i] = m.smoothed(i, m.counts[i], next)
		m.logSum += m.target[i] * math.Log(m.base[i])
	}
}

// divergenceWith is the divergence the picks would have with f added. Only
// f's own genres differ from the shared part prepare worked out. A title with
// no genres leaves the mix as it is.
func (m *genreMix) divergenceWith(f *MediaFeatures) float64 {
	if f == nil || len(f.GenreIDs) == 0 {
		return m.divergence()
	}
	share := (1 - calibrationSmoothing) / float64(len(f.GenreIDs))
	sum := m.logSum
	for _, g := range f.GenreIDs {
		if i, ok := m.index[g]; ok {
			sum += m.target[i] * (math.Log(m.base[i]+share) - math.Log(m.base[i]))
		}
	}
	return m.plogp + math.Log(m.mass+1) - sum
}
