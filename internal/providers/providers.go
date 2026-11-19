// Package providers answers "where can I watch this": the services that
// stream, rent or sell a title in one country. TMDB supplies the data, which
// comes from JustWatch, and the client must credit JustWatch beside it.
package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"klyvi-api/internal/platform/tmdb"
)

// MaxAge is how long a copy is served before a view refetches it. Services
// come and go far faster than the rest of the catalog changes.
const MaxAge = 7 * 24 * time.Hour

// DefaultRegion is the country used when the client names none.
const DefaultRegion = "US"

// ErrNotFound means TMDB has no such title.
var ErrNotFound = errors.New("title not found")

// Provider is one service offering the title.
type Provider struct {
	ID       int     `json:"provider_id"`
	Name     string  `json:"provider_name"`
	LogoPath *string `json:"logo_path"`
	Priority int     `json:"-"`
}

// Result is one country's offers for a title.
type Result struct {
	Region string `json:"region"`
	// Regions lists every country with offers, for the country picker.
	Regions []string `json:"regions"`
	// Link is TMDB's watch page for the title in this country, which credits
	// JustWatch and links on to each service. Null when TMDB gives none.
	Link   *string    `json:"link"`
	Stream []Provider `json:"stream"`
	Free   []Provider `json:"free"`
	Ads    []Provider `json:"ads"`
	Rent   []Provider `json:"rent"`
	Buy    []Provider `json:"buy"`
}

// Copy is a stored TMDB answer: its results object, keyed by country code.
type Copy struct {
	Results   json.RawMessage `db:"results"`
	FetchedAt time.Time       `db:"fetched_at"`
}

// Requester is the TMDB call the service makes.
type Requester interface {
	TMDBRequest(ctx context.Context, method, endpoint string, body interface{}) (map[string]interface{}, error)
}

// Store keeps one copy per title.
type Store interface {
	Get(ctx context.Context, mediaType string, tmdbID int) (*Copy, error)
	Put(ctx context.Context, mediaType string, tmdbID int, results json.RawMessage) error
}

type Service struct {
	tmdb  Requester
	store Store
}

func NewService(t Requester, s Store) *Service {
	return &Service{tmdb: t, store: s}
}

// Get returns a title's offers in one country. A copy under a week old is
// served as is. An older one refetches, and is still served if TMDB fails.
func (s *Service) Get(ctx context.Context, mediaType string, tmdbID int, region string) (*Result, error) {
	cached, err := s.store.Get(ctx, mediaType, tmdbID)
	if err != nil {
		return nil, fmt.Errorf("read providers: %w", err)
	}
	if cached != nil && time.Since(cached.FetchedAt) < MaxAge {
		return pick(cached.Results, region)
	}

	raw, err := s.tmdb.TMDBRequest(ctx, http.MethodGet, endpoint(mediaType, tmdbID), nil)
	if err != nil {
		if cached != nil {
			log.Printf("providers: refetch %s %d failed, serving the stored copy: %v", mediaType, tmdbID, err)
			return pick(cached.Results, region)
		}
		if tmdb.IsStatus(err, http.StatusNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("fetch providers: %w", err)
	}
	results, err := json.Marshal(raw["results"])
	if err != nil || string(results) == "null" {
		results = json.RawMessage(`{}`)
	}
	if err := s.store.Put(ctx, mediaType, tmdbID, results); err != nil {
		// The answer is still good. The next view tries to store it again.
		log.Printf("providers: store %s %d: %v", mediaType, tmdbID, err)
	}
	return pick(results, region)
}

func endpoint(mediaType string, tmdbID int) string {
	return fmt.Sprintf("/%s/%d/watch/providers", mediaType, tmdbID)
}

type wireProvider struct {
	ID       int     `json:"provider_id"`
	Name     string  `json:"provider_name"`
	LogoPath *string `json:"logo_path"`
	Priority int     `json:"display_priority"`
}

type wireRegion struct {
	Link     string         `json:"link"`
	Flatrate []wireProvider `json:"flatrate"`
	Free     []wireProvider `json:"free"`
	Ads      []wireProvider `json:"ads"`
	Rent     []wireProvider `json:"rent"`
	Buy      []wireProvider `json:"buy"`
}

// tmdbWatchPage is the only link passed on. It lands on TMDB's own page.
const tmdbWatchPage = "https://www.themoviedb.org/"

func pick(results json.RawMessage, region string) (*Result, error) {
	var all map[string]wireRegion
	if err := json.Unmarshal(results, &all); err != nil {
		return nil, fmt.Errorf("decode providers: %w", err)
	}
	out := &Result{Region: region, Regions: []string{}}
	for code := range all {
		out.Regions = append(out.Regions, code)
	}
	slices.Sort(out.Regions)

	r := all[region]
	if strings.HasPrefix(r.Link, tmdbWatchPage) {
		link := r.Link
		out.Link = &link
	}
	out.Stream = list(r.Flatrate)
	out.Free = list(r.Free)
	out.Ads = list(r.Ads)
	out.Rent = list(r.Rent)
	out.Buy = list(r.Buy)
	return out, nil
}

// list orders one kind of offer by TMDB's display priority, and is never nil.
func list(in []wireProvider) []Provider {
	out := make([]Provider, 0, len(in))
	for _, p := range in {
		if p.Name == "" {
			continue
		}
		out = append(out, Provider{ID: p.ID, Name: p.Name, LogoPath: p.LogoPath, Priority: p.Priority})
	}
	slices.SortStableFunc(out, func(a, b Provider) int { return a.Priority - b.Priority })
	return out
}

// Repository stores the copies in watch_providers.
type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// Get returns the stored copy, or nil when there is none.
func (r *Repository) Get(ctx context.Context, mediaType string, tmdbID int) (*Copy, error) {
	var c Copy
	err := r.db.GetContext(ctx, &c, `
		select results, fetched_at from watch_providers
		 where media_type = $1 and tmdb_id = $2`, mediaType, tmdbID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get watch providers: %w", err)
	}
	return &c, nil
}

// Put stores a fresh copy, replacing any older one.
func (r *Repository) Put(ctx context.Context, mediaType string, tmdbID int, results json.RawMessage) error {
	if _, err := r.db.ExecContext(ctx, `
		insert into watch_providers (media_type, tmdb_id, results, fetched_at)
		values ($1, $2, $3, now())
		on conflict (media_type, tmdb_id)
		do update set results = excluded.results, fetched_at = excluded.fetched_at`,
		mediaType, tmdbID, []byte(results)); err != nil {
		return fmt.Errorf("put watch providers: %w", err)
	}
	return nil
}

// DeleteOlderThan removes copies fetched before cutoff and says how many.
func (r *Repository) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `delete from watch_providers where fetched_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune watch providers: %w", err)
	}
	return res.RowsAffected()
}
