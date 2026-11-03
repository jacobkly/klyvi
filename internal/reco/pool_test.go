package reco

import (
	"context"
	"slices"
	"testing"
)

// poolSource serves fixed film and series catalogs, and records what the
// series retrieval was asked, so a test can see the pool's mix and query.
type poolSource struct {
	films, series []Candidate

	seriesAsked  bool
	seriesGenres []int
}

func (p *poolSource) SampleMovieCandidates(_ context.Context, limit int) ([]Candidate, error) {
	return firstN(p.films, limit), nil
}

func (p *poolSource) CandidatesByTaste(_ context.Context, _, _ []int, _, limit int) ([]Candidate, error) {
	return firstN(p.films, limit), nil
}

func (p *poolSource) SampleSeriesCandidates(_ context.Context, limit int) ([]Candidate, error) {
	p.seriesAsked = true
	return firstN(p.series, limit), nil
}

func (p *poolSource) SeriesCandidatesByTaste(_ context.Context, genreIDs, _ []int, _, limit int) ([]Candidate, error) {
	p.seriesAsked = true
	p.seriesGenres = genreIDs
	return firstN(p.series, limit), nil
}

func firstN(cs []Candidate, n int) []Candidate {
	return cs[:min(n, len(cs))]
}

// catalogOf returns n candidates of one media type, media ids from first up.
func catalogOf(mediaType string, first, n int) []Candidate {
	out := make([]Candidate, n)
	for i := range out {
		id := first + i
		out[i] = Candidate{MediaID: id, MediaType: mediaType, Features: &MediaFeatures{MediaID: id, MediaType: mediaType}}
	}
	return out
}

func countType(cs []Candidate, mediaType string) int {
	n := 0
	for _, c := range cs {
		if c.MediaType == mediaType {
			n++
		}
	}
	return n
}

// likedDrama is a one-film taste, enough to send the pool to taste retrieval.
var likedDrama = []SignalItem{{MediaID: 1, Weight: 1, Features: &MediaFeatures{MediaType: "movie", GenreIDs: []int{18}}}}

// Series vote counts run lower than films', so one pool sorted by votes would
// hold no shows. Series get their own share of the pool, for a cold user and
// a user with taste alike.
func TestBuildCandidatePool_KeepsAShareForSeries(t *testing.T) {
	for _, tt := range []struct {
		name  string
		liked []SignalItem
	}{
		{"cold user", nil},
		{"user with taste", likedDrama},
	} {
		t.Run(tt.name, func(t *testing.T) {
			src := &poolSource{films: catalogOf("movie", 1, 2000), series: catalogOf("tv", 100_001, 1000)}

			pool, err := BuildCandidatePool(context.Background(), src, tt.liked, DefaultConfig())
			if err != nil {
				t.Fatalf("BuildCandidatePool: %v", err)
			}
			if len(pool) != candidatePoolSize {
				t.Errorf("pool = %d candidates, want %d", len(pool), candidatePoolSize)
			}
			if got, want := countType(pool, "tv"), 480; got != want {
				t.Errorf("pool holds %d series, want %d (40%% of %d)", got, want, candidatePoolSize)
			}
		})
	}
}

// A series catalog too thin to fill its share leaves the rest to films, so
// the pool stays full.
func TestBuildCandidatePool_FilmsFillWhatSeriesCannot(t *testing.T) {
	src := &poolSource{films: catalogOf("movie", 1, 2000), series: catalogOf("tv", 100_001, 100)}

	pool, err := BuildCandidatePool(context.Background(), src, likedDrama, DefaultConfig())
	if err != nil {
		t.Fatalf("BuildCandidatePool: %v", err)
	}
	if got := countType(pool, "tv"); got != 100 {
		t.Errorf("pool holds %d series, want all 100", got)
	}
	if got, want := countType(pool, "movie"), candidatePoolSize-100; got != want {
		t.Errorf("pool holds %d films, want %d", got, want)
	}
}

// A zero share is the films-only pool, and costs no series query.
func TestBuildCandidatePool_ZeroShareIsFilmsOnly(t *testing.T) {
	src := &poolSource{films: catalogOf("movie", 1, 2000), series: catalogOf("tv", 100_001, 1000)}
	cfg := DefaultConfig()
	cfg.TVShare = 0

	pool, err := BuildCandidatePool(context.Background(), src, likedDrama, cfg)
	if err != nil {
		t.Fatalf("BuildCandidatePool: %v", err)
	}
	if src.seriesAsked {
		t.Error("a zero TV share still queried series")
	}
	if got := countType(pool, "movie"); got != candidatePoolSize {
		t.Errorf("pool holds %d films, want %d", got, candidatePoolSize)
	}
}

// The taste is held in film genres, but the series cache stores TV's own ids,
// so a taste for science fiction must also ask for Sci-Fi & Fantasy.
func TestBuildCandidatePool_AsksForSeriesInTVGenres(t *testing.T) {
	src := &poolSource{films: catalogOf("movie", 1, 10), series: catalogOf("tv", 100_001, 10)}
	liked := []SignalItem{{MediaID: 1, Weight: 1, Features: &MediaFeatures{MediaType: "movie", GenreIDs: []int{878}}}}

	if _, err := BuildCandidatePool(context.Background(), src, liked, DefaultConfig()); err != nil {
		t.Fatalf("BuildCandidatePool: %v", err)
	}
	if !slices.Equal(src.seriesGenres, []int{878, 10765}) {
		t.Errorf("series asked for genres %v, want [878 10765]", src.seriesGenres)
	}
}
