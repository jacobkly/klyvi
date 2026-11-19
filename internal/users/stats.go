package users

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// The stats endpoint aggregates the user's library into the profile Stats
// tab shape. Two inputs, both read-model projections: StatsRow (one per
// tracking entry, enriched with catalog facts) and StatsEvent (one per
// interaction, the honest event log behind the heatmap and watch years).
// All aggregation happens here in pure functions so the arithmetic is
// table-testable without a database.

// StatsRow is one tracked entry with the catalog facts stats need.
type StatsRow struct {
	MediaType             string   `db:"media_type"`
	Status                *string  `db:"status"`
	Score                 *int     `db:"score"`
	EpisodeProgress       *int     `db:"episode_progress"`
	TotalRewatches        int      `db:"total_rewatches"`
	RuntimeMinutes        *int     `db:"runtime_minutes"`         // movies
	EpisodeRuntimeMinutes *int     `db:"episode_runtime_minutes"` // seasons: per-episode
	EpisodeCount          *int     `db:"episode_count"`           // seasons
	ReleaseYear           *int     `db:"release_year"`
	Genres                []string // decoded from catalog JSONB
	Countries             []string // ISO codes / names
}

// StatsEvent is one interaction: the signal log doubles as the activity
// event source, which is what makes the heatmap honest — tracking rows only
// carry updated_at, interactions carry every action.
type StatsEvent struct {
	CreatedAt time.Time `db:"created_at"`
}

type StatsKPIs struct {
	TotalFilms      int     `json:"total_films"`
	TotalSeasons    int     `json:"total_seasons"`
	EpisodesWatched int     `json:"episodes_watched"`
	DaysWatched     float64 `json:"days_watched"`
	DaysPlanned     float64 `json:"days_planned"`
	MeanScore       float64 `json:"mean_score"`
	ScoreStddev     float64 `json:"score_stddev"`
}

type ScoreBand struct {
	Band   string  `json:"band"`
	Titles int     `json:"titles"`
	Hours  float64 `json:"hours"`
}

type YearBucket struct {
	Year   int     `json:"year"`
	Titles int     `json:"titles"`
	Hours  float64 `json:"hours"`
}

type GenreBucket struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	// MeanScore is nil when none of the genre's titles is scored, so the
	// client shows a dash rather than a mean of 0.
	MeanScore *float64 `json:"mean_score"`
	Hours     float64  `json:"hours"`
}

type PctBucket struct {
	Label string  `json:"label"`
	Pct   float64 `json:"pct"`
}

type ActivityDay struct {
	Date  string `json:"date"` // YYYY-MM-DD
	Count int    `json:"count"`
}

type Stats struct {
	KPIs              StatsKPIs     `json:"kpis"`
	ScoreDistribution []ScoreBand   `json:"score_distribution"`
	ReleaseYears      []YearBucket  `json:"release_years"`
	WatchYears        []YearBucket  `json:"watch_years"`
	Genres            []GenreBucket `json:"genres"`
	Formats           []PctBucket   `json:"formats"`
	Countries         []PctBucket   `json:"countries"`
	Activity          []ActivityDay `json:"activity"`
}

// activityDays is the heatmap window: 53 weeks, matching the client grid.
const activityDays = 371

// watchedMinutes is the time this entry has actually consumed. Movies count
// their full runtime once completed (per rewatch on top); seasons count
// watched episodes × per-episode runtime. Planning rows contribute nothing.
func watchedMinutes(r StatsRow) float64 {
	if status(r) == "planning" {
		return 0
	}
	switch r.MediaType {
	case "movie":
		if !engagedFully(r) {
			return 0
		}
		return float64(deref(r.RuntimeMinutes)) * float64(1+r.TotalRewatches)
	case "season":
		return float64(episodesWatched(r)) * float64(deref(r.EpisodeRuntimeMinutes))
	}
	return 0
}

// engagedFully: a movie only counts as watched time once it has actually
// been watched — completed or rewatching. A watchlisted-but-paused movie
// contributing full runtime would flatter the numbers.
func engagedFully(r StatsRow) bool {
	s := status(r)
	return s == "completed" || s == "rewatching"
}

// episodesWatched: explicit progress wins; a completed season with no
// recorded progress falls back to its episode count.
func episodesWatched(r StatsRow) int {
	if r.MediaType != "season" {
		return 0
	}
	n := deref(r.EpisodeProgress)
	if status(r) == "completed" && n < deref(r.EpisodeCount) {
		n = deref(r.EpisodeCount)
	}
	return n
}

func plannedMinutes(r StatsRow) float64 {
	if status(r) != "planning" {
		return 0
	}
	switch r.MediaType {
	case "movie":
		return float64(deref(r.RuntimeMinutes))
	case "season":
		return float64(deref(r.EpisodeCount)) * float64(deref(r.EpisodeRuntimeMinutes))
	}
	return 0
}

func status(r StatsRow) string {
	if r.Status == nil {
		return ""
	}
	return *r.Status
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// ComputeStats aggregates the library into the Stats tab payload. Pure —
// `now` is a parameter so the heatmap window is deterministic under test.
func ComputeStats(rows []StatsRow, events []StatsEvent, now time.Time) *Stats {
	s := &Stats{
		ScoreDistribution: make([]ScoreBand, 10),
		ReleaseYears:      []YearBucket{},
		WatchYears:        []YearBucket{},
		Genres:            []GenreBucket{},
		Formats:           []PctBucket{},
		Countries:         []PctBucket{},
	}

	// --- KPIs + per-entry accumulation feeding every distribution.
	var scores []int
	var watchedTotal, plannedTotal float64
	genreAgg := map[string]*GenreBucket{}
	genreScores := map[string][]int{}
	releaseYears := map[int]*YearBucket{}
	countryCounts := map[string]int{}
	formatCounts := map[string]int{}
	countryTotal := 0

	for _, r := range rows {
		engaged := status(r) != "planning"
		w := watchedMinutes(r)
		watchedTotal += w
		plannedTotal += plannedMinutes(r)

		if engaged {
			switch r.MediaType {
			case "movie":
				s.KPIs.TotalFilms++
			case "season":
				s.KPIs.TotalSeasons++
			}
		}
		s.KPIs.EpisodesWatched += episodesWatched(r)

		if r.Score != nil {
			scores = append(scores, *r.Score)
			band := scoreBandIndex(*r.Score)
			s.ScoreDistribution[band].Titles++
			s.ScoreDistribution[band].Hours += w / 60
		}

		for _, g := range splitGenres(r.Genres) {
			b := genreAgg[g]
			if b == nil {
				b = &GenreBucket{Name: g}
				genreAgg[g] = b
			}
			b.Count++
			b.Hours += w / 60
			if r.Score != nil {
				genreScores[g] = append(genreScores[g], *r.Score)
			}
		}

		if r.ReleaseYear != nil {
			y := releaseYears[*r.ReleaseYear]
			if y == nil {
				y = &YearBucket{Year: *r.ReleaseYear}
				releaseYears[*r.ReleaseYear] = y
			}
			y.Titles++
			y.Hours += w / 60
		}

		formatCounts[r.MediaType]++
		for _, c := range r.Countries {
			countryCounts[c]++
			countryTotal++
		}
	}

	s.KPIs.DaysWatched = watchedTotal / 60 / 24
	s.KPIs.DaysPlanned = plannedTotal / 60 / 24
	s.KPIs.MeanScore, s.KPIs.ScoreStddev = meanStddev(scores)

	for i := range s.ScoreDistribution {
		s.ScoreDistribution[i].Band = bandLabel(i)
	}

	for name, b := range genreAgg {
		if vals := genreScores[name]; len(vals) > 0 {
			mean, _ := meanStddev(vals)
			b.MeanScore = &mean
		}
		s.Genres = append(s.Genres, *b)
	}
	sort.Slice(s.Genres, func(i, j int) bool {
		if s.Genres[i].Count != s.Genres[j].Count {
			return s.Genres[i].Count > s.Genres[j].Count
		}
		return s.Genres[i].Name < s.Genres[j].Name
	})

	for _, y := range releaseYears {
		s.ReleaseYears = append(s.ReleaseYears, *y)
	}
	sort.Slice(s.ReleaseYears, func(i, j int) bool { return s.ReleaseYears[i].Year < s.ReleaseYears[j].Year })

	if n := len(rows); n > 0 {
		for _, label := range []string{"movie", "season"} {
			if c := formatCounts[label]; c > 0 {
				s.Formats = append(s.Formats, PctBucket{Label: label, Pct: round1(100 * float64(c) / float64(n))})
			}
		}
	}
	if countryTotal > 0 {
		type kv struct {
			label string
			count int
		}
		var cs []kv
		for label, count := range countryCounts {
			cs = append(cs, kv{label, count})
		}
		sort.Slice(cs, func(i, j int) bool {
			if cs[i].count != cs[j].count {
				return cs[i].count > cs[j].count
			}
			return cs[i].label < cs[j].label
		})
		const topCountries = 10
		for i, c := range cs {
			if i == topCountries {
				break
			}
			s.Countries = append(s.Countries, PctBucket{Label: c.label, Pct: round1(100 * float64(c.count) / float64(countryTotal))})
		}
	}

	// --- Event-derived: heatmap window + watch years.
	s.Activity = make([]ActivityDay, activityDays)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	start := today.AddDate(0, 0, -(activityDays - 1))
	for i := range s.Activity {
		s.Activity[i].Date = start.AddDate(0, 0, i).Format("2006-01-02")
	}
	watchYears := map[int]*YearBucket{}
	for _, e := range events {
		d := e.CreatedAt.UTC()
		day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
		if idx := int(day.Sub(start).Hours() / 24); idx >= 0 && idx < activityDays {
			s.Activity[idx].Count++
		}

		y := watchYears[d.Year()]
		if y == nil {
			y = &YearBucket{Year: d.Year()}
			watchYears[d.Year()] = y
		}
		y.Titles++
	}
	for _, y := range watchYears {
		s.WatchYears = append(s.WatchYears, *y)
	}
	sort.Slice(s.WatchYears, func(i, j int) bool { return s.WatchYears[i].Year < s.WatchYears[j].Year })

	return s
}

// scoreBandIndex maps 1..100 into ten bands of ten; 0 shares the lowest.
func scoreBandIndex(score int) int {
	if score <= 0 {
		return 0
	}
	if score > 100 {
		score = 100
	}
	return (score - 1) / 10
}

// tvGenreParts are TV's combined genres, as the film genres they're made of.
// Counting them whole would list "Sci-Fi & Fantasy" beside "Science Fiction".
var tvGenreParts = map[string][]string{
	"Sci-Fi & Fantasy":   {"Science Fiction", "Fantasy"},
	"Action & Adventure": {"Action", "Adventure"},
}

// splitGenres expands TV's combined genres and drops repeats, so a title
// counts once in each genre.
func splitGenres(genres []string) []string {
	out := make([]string, 0, len(genres)+2)
	seen := map[string]bool{}
	for _, g := range genres {
		parts, ok := tvGenreParts[g]
		if !ok {
			parts = []string{g}
		}
		for _, p := range parts {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

func bandLabel(i int) string {
	return fmt.Sprintf("%d-%d", i*10+1, i*10+10)
}

// meanStddev is the population mean + stddev, zero-safe.
func meanStddev(vals []int) (float64, float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range vals {
		sum += float64(v)
	}
	mean := sum / float64(len(vals))
	var sq float64
	for _, v := range vals {
		d := float64(v) - mean
		sq += d * d
	}
	return mean, math.Sqrt(sq / float64(len(vals)))
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
