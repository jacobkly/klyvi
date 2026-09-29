// Package similar serves the "more like this" shelf on a title page: the
// cached titles whose keywords and genres overlap the source's most, weighted
// by how rare each shared feature is. TMDB's own list fills the shelf when the
// overlap is thin.
package similar

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"klyvi-api/internal/platform/cache"
	"klyvi-api/internal/platform/tmdb"
	"klyvi-api/internal/reco"
)

const (
	// MaxLimit is the longest shelf a request can ask for. DefaultLimit is
	// what it gets when it does not ask.
	MaxLimit     = 20
	DefaultLimit = 12

	// poolSize bounds the candidates read per source, best overlap first.
	poolSize = 500

	// A source with fewer keywords than this matches on tags too broad to
	// trust, so TMDB's list leads its shelf.
	minSourceKeywords = 3

	// Sequels share most of their keywords. Without a cap one franchise
	// would fill the shelf.
	maxPerCollection = 2

	maxReasons = 3

	cacheEntries = 2000
	cacheTTL     = 6 * time.Hour
)

// ErrNotFound is a title neither the catalog nor TMDB has.
var ErrNotFound = errors.New("title not found")

// Item is one title on a similar shelf.
type Item struct {
	TMDBID     int           `json:"tmdb_id"`
	MediaID    *int          `json:"media_id"`   // null for a TMDB title not cached yet
	MediaType  string        `json:"media_type"` // "movie" or "tv"
	Title      string        `json:"title"`
	Year       int           `json:"year"` // 0 when unknown
	PosterPath string        `json:"poster_path"`
	Score      float64       `json:"score"`   // 0..1, 0 for a title from TMDB's list
	Reasons    []reco.Reason `json:"reasons"` // the shared features, rarest first
}

// Source is the title a shelf is for.
type Source struct {
	TMDBID       int
	GenreIDs     []int
	KeywordIDs   []int
	CollectionID int // 0 when it is not part of one
	GenreNames   map[int]string
	KeywordNames map[int]string
}

// Candidate is a cached title sharing at least one keyword with the source.
type Candidate struct {
	TMDBID       int
	MediaID      int
	Title        string
	PosterPath   string
	Year         int
	GenreIDs     []int
	KeywordIDs   []int
	CollectionID int
}

// Resolved is what the catalog knows about a title from TMDB's list.
type Resolved struct {
	MediaID      int
	CollectionID int
}

// Catalog reads the cached titles a shelf is built from.
type Catalog interface {
	// Source returns nil when the title is not cached.
	Source(ctx context.Context, tmdbID int) (*Source, error)
	// Candidates returns up to limit titles with at least minVotes votes
	// that share a keyword with src, most shared keywords first.
	Candidates(ctx context.Context, src *Source, minVotes, limit int) ([]Candidate, error)
	// Resolve returns the cached titles among tmdbIDs, keyed by TMDB id.
	Resolve(ctx context.Context, tmdbIDs []int) (map[int]Resolved, error)
}

// TMDBClient is the TMDB request a shelf needs for its fill.
type TMDBClient interface {
	TMDBRequest(ctx context.Context, method, endpoint string, body interface{}) (map[string]interface{}, error)
}

// EnsureCached caches a title from TMDB if the catalog lacks it.
type EnsureCached func(ctx context.Context, tmdbID int) error

// Config weighs keyword against genre overlap and sets the vote floor a
// candidate needs.
type Config struct {
	KeywordWeight float64
	GenreWeight   float64
	MinVotes      int
}

// Service builds similar shelves for one media type.
type Service struct {
	mediaType string // "movie" or "tv", also TMDB's path segment
	catalog   Catalog
	ensure    EnsureCached
	client    TMDBClient
	idf       *reco.FeatureIDF // nil weighs every feature the same
	cfg       Config
	shelves   *cache.TTL[[]Item]
}

// NewMovieService builds shelves for movies.
func NewMovieService(catalog Catalog, ensure EnsureCached, client TMDBClient, idf *reco.FeatureIDF, cfg Config) *Service {
	return newService("movie", catalog, ensure, client, idf, cfg)
}

// NewTVService builds shelves for series.
func NewTVService(catalog Catalog, ensure EnsureCached, client TMDBClient, idf *reco.FeatureIDF, cfg Config) *Service {
	return newService("tv", catalog, ensure, client, idf, cfg)
}

func newService(mediaType string, catalog Catalog, ensure EnsureCached, client TMDBClient, idf *reco.FeatureIDF, cfg Config) *Service {
	return &Service{
		mediaType: mediaType,
		catalog:   catalog,
		ensure:    ensure,
		client:    client,
		idf:       idf,
		cfg:       cfg,
		shelves:   cache.NewTTL[[]Item](cacheEntries, cacheTTL),
	}
}

// Similar returns up to limit titles like the one with tmdbID, best first.
// A full shelf is computed once and kept, so every limit slices the same one.
func (s *Service) Similar(ctx context.Context, tmdbID, limit int) ([]Item, error) {
	if limit < 1 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)

	key := strconv.Itoa(tmdbID)
	shelf, ok := s.shelves.Get(key)
	if !ok {
		var complete bool
		var err error
		shelf, complete, err = s.compute(ctx, tmdbID)
		if err != nil {
			return nil, err
		}
		if complete {
			s.shelves.Set(key, shelf)
		}
	}
	return shelf[:min(limit, len(shelf))], nil
}

// entry is a shelf title with the collection it counts toward.
type entry struct {
	item         Item
	collectionID int
}

// compute builds the full shelf. complete is false when TMDB could not be
// reached for a shelf that needed it, so the caller does not keep it.
func (s *Service) compute(ctx context.Context, tmdbID int) (shelf []Item, complete bool, err error) {
	src, err := s.catalog.Source(ctx, tmdbID)
	if err != nil {
		return nil, false, fmt.Errorf("load %s %d: %w", s.mediaType, tmdbID, err)
	}
	if src == nil || len(src.KeywordIDs) == 0 {
		if err := s.ensure(ctx, tmdbID); err != nil {
			if tmdb.IsStatus(err, http.StatusNotFound) {
				return nil, false, ErrNotFound
			}
			// TMDB's own list may still make a shelf.
			log.Printf("similar: cache %s %d: %v", s.mediaType, tmdbID, err)
		} else if src, err = s.catalog.Source(ctx, tmdbID); err != nil {
			return nil, false, fmt.Errorf("load %s %d: %w", s.mediaType, tmdbID, err)
		}
	}

	var overlap []entry
	if src != nil && len(src.KeywordIDs) > 0 {
		pool, err := s.catalog.Candidates(ctx, src, s.cfg.MinVotes, poolSize)
		if err != nil {
			return nil, false, fmt.Errorf("load candidates for %s %d: %w", s.mediaType, tmdbID, err)
		}
		overlap = s.rank(src, pool)
	}

	keywordRich := src != nil && len(src.KeywordIDs) >= minSourceKeywords
	if keywordRich {
		if shelf := assemble(tmdbID, overlap); len(shelf) >= MaxLimit {
			return shelf, true, nil
		}
	}

	fill, err := s.tmdbFill(ctx, tmdbID)
	switch {
	case tmdb.IsStatus(err, http.StatusNotFound):
		if src == nil {
			return nil, false, ErrNotFound
		}
		fill = nil
	case err != nil:
		if len(overlap) == 0 {
			return nil, false, fmt.Errorf("tmdb recommendations for %s %d: %w", s.mediaType, tmdbID, err)
		}
		log.Printf("similar: TMDB list for %s %d failed, serving the overlap alone: %v", s.mediaType, tmdbID, err)
		return assemble(tmdbID, overlap), false, nil
	}

	if keywordRich {
		return assemble(tmdbID, overlap, fill), true, nil
	}
	// A title the catalog could not cache gets another try next time.
	return assemble(tmdbID, fill, overlap), src != nil, nil
}

// rank scores each candidate against the source and orders them best first.
// A candidate's score blends the cosine similarity of its keywords and of its
// genres with the source's, each feature weighted by its IDF so a rare shared
// keyword counts for more than one half the catalog carries.
func (s *Service) rank(src *Source, pool []Candidate) []entry {
	srcKeywords := s.weigh(src.KeywordIDs, s.idf.KeywordWeight)
	srcGenres := s.weigh(src.GenreIDs, s.idf.GenreWeight)
	total := s.cfg.KeywordWeight + s.cfg.GenreWeight

	type scored struct {
		entry
		score float64
	}
	var ranked []scored
	for _, c := range pool {
		if c.TMDBID == src.TMDBID {
			continue
		}
		kSim := cosine(srcKeywords, s.weigh(c.KeywordIDs, s.idf.KeywordWeight))
		if kSim == 0 {
			continue
		}
		gSim := cosine(srcGenres, s.weigh(c.GenreIDs, s.idf.GenreWeight))
		score := kSim
		if total > 0 {
			score = (s.cfg.KeywordWeight*kSim + s.cfg.GenreWeight*gSim) / total
		}
		score = math.Min(1, math.Round(score*1000)/1000)

		mediaID := c.MediaID
		ranked = append(ranked, scored{
			entry: entry{
				item: Item{
					TMDBID:     c.TMDBID,
					MediaID:    &mediaID,
					MediaType:  s.mediaType,
					Title:      c.Title,
					Year:       c.Year,
					PosterPath: c.PosterPath,
					Score:      score,
					Reasons:    s.reasons(src, srcKeywords, srcGenres, c),
				},
				collectionID: c.CollectionID,
			},
			score: score,
		})
	}
	// Stable, so the pool's order (more shared keywords, then more votes)
	// breaks ties.
	slices.SortStableFunc(ranked, func(a, b scored) int { return cmp.Compare(b.score, a.score) })

	out := make([]entry, len(ranked))
	for i, r := range ranked {
		out[i] = r.entry
	}
	return out
}

// weigh maps each feature id to its IDF weight.
func (s *Service) weigh(ids []int, weight func(int) float64) map[int]float64 {
	out := make(map[int]float64, len(ids))
	for _, id := range ids {
		out[id] = weight(id)
	}
	return out
}

// cosine is the cosine similarity of two weighted feature sets. A feature's
// weight is the same on both sides, so a shared one adds its weight squared.
func cosine(a, b map[int]float64) float64 {
	var dot, normA, normB float64
	for id, w := range a {
		normA += w * w
		if _, ok := b[id]; ok {
			dot += w * w
		}
	}
	if dot == 0 {
		return 0
	}
	for _, w := range b {
		normB += w * w
	}
	return dot / math.Sqrt(normA*normB)
}

// reasons names the features a candidate shares with the source: keywords
// rarest first, then genres if there is room.
func (s *Service) reasons(src *Source, srcKeywords, srcGenres map[int]float64, c Candidate) []reco.Reason {
	shared := func(ids []int, weights map[int]float64) []int {
		var out []int
		for _, id := range ids {
			if _, ok := weights[id]; ok {
				out = append(out, id)
			}
		}
		slices.SortFunc(out, func(a, b int) int {
			return cmp.Or(cmp.Compare(weights[b], weights[a]), cmp.Compare(a, b))
		})
		return out
	}

	out := []reco.Reason{}
	for _, id := range shared(c.KeywordIDs, srcKeywords) {
		if len(out) == maxReasons {
			return out
		}
		out = append(out, reco.Reason{Kind: "keyword", ID: id, Name: src.KeywordNames[id]})
	}
	for _, id := range shared(c.GenreIDs, srcGenres) {
		if len(out) == maxReasons {
			return out
		}
		out = append(out, reco.Reason{Kind: "genre", ID: id, Name: src.GenreNames[id]})
	}
	return out
}

// tmdbFill is TMDB's own recommendations for the title, with the ones the
// catalog has resolved to their media ids.
func (s *Service) tmdbFill(ctx context.Context, tmdbID int) ([]entry, error) {
	raw, err := s.client.TMDBRequest(ctx, http.MethodGet,
		fmt.Sprintf("/%s/%d/recommendations?language=en-US&page=1", s.mediaType, tmdbID), nil)
	if err != nil {
		return nil, err
	}
	results, _ := raw["results"].([]interface{})

	var fill []entry
	var ids []int
	for _, r := range results {
		m, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		if adult, _ := m["adult"].(bool); adult {
			continue
		}
		id, _ := m["id"].(float64)
		if id <= 0 {
			continue
		}
		fill = append(fill, entry{item: Item{
			TMDBID:     int(id),
			MediaType:  s.mediaType,
			Title:      firstString(m, "title", "name"),
			Year:       yearOf(firstString(m, "release_date", "first_air_date")),
			PosterPath: firstString(m, "poster_path"),
			Reasons:    []reco.Reason{},
		}})
		ids = append(ids, int(id))
	}
	if len(ids) == 0 {
		return nil, nil
	}

	resolved, err := s.catalog.Resolve(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("resolve TMDB list: %w", err)
	}
	for i := range fill {
		if r, ok := resolved[fill[i].item.TMDBID]; ok {
			mediaID := r.MediaID
			fill[i].item.MediaID = &mediaID
			fill[i].collectionID = r.CollectionID
		}
	}
	return fill, nil
}

// assemble joins ranked lists into one shelf: first mention wins, the source
// never appears, no collection has more than maxPerCollection titles, and it
// stops at MaxLimit.
func assemble(sourceID int, lists ...[]entry) []Item {
	shelf := []Item{}
	seen := map[int]bool{sourceID: true}
	perCollection := map[int]int{}
	for _, list := range lists {
		for _, e := range list {
			if len(shelf) == MaxLimit {
				return shelf
			}
			if seen[e.item.TMDBID] {
				continue
			}
			if e.collectionID != 0 && perCollection[e.collectionID] == maxPerCollection {
				continue
			}
			seen[e.item.TMDBID] = true
			perCollection[e.collectionID]++
			shelf = append(shelf, e.item)
		}
	}
	return shelf
}

func firstString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, _ := m[k].(string); v != "" {
			return v
		}
	}
	return ""
}

// yearOf reads the year from a TMDB date such as "2019-05-30".
func yearOf(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return y
}
