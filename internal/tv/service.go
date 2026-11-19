package tv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"klyvi-api/internal/platform/tmdb"
	"klyvi-api/internal/toprated"
)

// TMDBClient is the only external dependency the TV service currently has.
// Defining the interface here keeps the service coupled to behavior, not to
// one specific HTTP client implementation.
type TMDBClient interface {
	TMDBRequest(ctx context.Context, method, endpoint string, body interface{}) (map[string]interface{}, error)
}

// TVRepository is the persistence boundary the TV service depends on. The
// interface lives in the consumer package per the repo's convention
// (matching movies/service.go).
type TVRepository interface {
	GetTVSeriesByID(ctx context.Context, tvID int) (*TVSeries, error)
	HasTVSeriesFeatures(ctx context.Context, tvID int) (bool, error)
	InsertTVSeries(ctx context.Context, series *TVSeries) error
	EnsureMediaIndexSeries(ctx context.Context, tvID int) error
	GetTVSeasonByTVIDAndNumber(ctx context.Context, tvID, seasonNumber int) (*TVSeason, error)
	InsertTVSeason(ctx context.Context, season *TVSeason) error
	EnsureMediaIndexSeason(ctx context.Context, tvID, seasonNumber int) error
	GetTVIDAndSeasonByMediaID(ctx context.Context, mediaID int) (int, int, error)
	ReplaceTVSeries(ctx context.Context, series *TVSeries) error
	DeleteSeries(ctx context.Context, tvID int) error
	ReplaceTVSeason(ctx context.Context, season *TVSeason) error
	DeleteSeason(ctx context.Context, tvID, seasonNumber int) error
}

type Service struct {
	client TMDBClient
	repo   TVRepository
}

// NewService wires the TV service to its TMDB and persistence boundaries.
func NewService(client TMDBClient, repo TVRepository) *Service {
	return &Service{client: client, repo: repo}
}

// GetTvById returns a TV series detail (when seasonNum is 0) or a TV season
// detail (when seasonNum > 0). The "external" idType means id is a TMDB
// tv_id; the "internal" idType means id is a media_index media_id (which
// per the committed convention points at a season row — series-level rows
// in media_index do not exist).
func (s *Service) GetTvById(ctx context.Context, idType string, id int, seasonNum int) (interface{}, error) {
	tvID, effectiveSeasonNum, err := s.resolveTVID(ctx, idType, id, seasonNum)
	if err != nil {
		return nil, err
	}

	if effectiveSeasonNum > 0 {
		return s.getSeasonDetail(ctx, tvID, effectiveSeasonNum)
	}
	return s.getSeriesDetail(ctx, tvID)
}

// resolveTVID maps the (idType, id, seasonNum) tuple from the handler into a
// concrete (tv_id, season_number) pair the rest of the service can use. The
// "internal" path looks up media_index to recover the parent tv_id from a
// season row.
func (s *Service) resolveTVID(ctx context.Context, idType string, id, seasonNum int) (int, int, error) {
	switch idType {
	case "external":
		return id, seasonNum, nil
	case "internal":
		tvID, indexedSeasonNum, err := s.repo.GetTVIDAndSeasonByMediaID(ctx, id)
		if err != nil {
			return 0, 0, err
		}
		if tvID == 0 {
			return 0, 0, ErrTVNotFound
		}
		// If the caller passed an explicit seasonNum it wins (e.g. asking
		// for series-level data via an internal id sends seasonNum=0).
		if seasonNum > 0 {
			return tvID, seasonNum, nil
		}
		return tvID, indexedSeasonNum, nil
	default:
		return 0, 0, ErrInvalidIDType
	}
}

func (s *Service) getSeriesDetail(ctx context.Context, tvID int) (*TVSeries, error) {
	cached, err := s.repo.GetTVSeriesByID(ctx, tvID)
	if err != nil {
		return nil, err
	}
	if cached != nil && hasName(cached) && cached.HasExtras {
		return cached, nil
	}

	// A row cached without its name is refetched and overwritten, so the
	// detail page never shows a series as untitled. So is one cached before
	// the rating and trailer were fetched, and it stays the fallback.
	store := s.repo.InsertTVSeries
	if cached != nil {
		store = s.repo.ReplaceTVSeries
	}
	series, err := s.fetchSeriesInto(ctx, tvID, store)
	if err != nil {
		if cached != nil && hasName(cached) {
			log.Printf("serving cached series %d without its extras: %v", tvID, err)
			return cached, nil
		}
		return nil, err
	}

	if err := s.repo.EnsureMediaIndexSeries(ctx, tvID); err != nil {
		log.Printf("best-effort EnsureMediaIndexSeries failed for tv=%d: %v", tvID, err)
	}

	return series, nil
}

// EnsureSeries caches a series with its keywords and credits, unless it
// already is, and writes its 'tv' media_index row. Signal on a season rolls
// up to its series, which is only possible once the series' features are
// cached.
func (s *Service) EnsureSeries(ctx context.Context, tvID int) error {
	cached, err := s.repo.HasTVSeriesFeatures(ctx, tvID)
	if err != nil {
		return fmt.Errorf("check series %d: %w", tvID, err)
	}
	if !cached {
		if _, err := s.fetchSeries(ctx, tvID); err != nil {
			return fmt.Errorf("fetch series %d: %w", tvID, err)
		}
	}
	if err := s.repo.EnsureMediaIndexSeries(ctx, tvID); err != nil {
		return fmt.Errorf("index series %d: %w", tvID, err)
	}
	return nil
}

// SeasonEpisodeCounts gives the episode count of each season of a show,
// keyed by season number, from the cached series. It caches the series
// first when it is not. An import uses it to tell a finished season from
// one in progress, and a real season from one the show does not have.
func (s *Service) SeasonEpisodeCounts(ctx context.Context, tvID int) (map[int]int, error) {
	if err := s.EnsureSeries(ctx, tvID); err != nil {
		return nil, err
	}
	series, err := s.repo.GetTVSeriesByID(ctx, tvID)
	if err != nil {
		return nil, fmt.Errorf("read series %d: %w", tvID, err)
	}
	counts := map[int]int{}
	if series == nil || series.Seasons == nil {
		return counts, nil
	}
	var seasons []struct {
		SeasonNumber int `json:"season_number"`
		EpisodeCount int `json:"episode_count"`
	}
	if err := json.Unmarshal(*series.Seasons, &seasons); err != nil {
		return nil, fmt.Errorf("decode seasons of series %d: %w", tvID, err)
	}
	for _, season := range seasons {
		counts[season.SeasonNumber] = season.EpisodeCount
	}
	return counts, nil
}

// detailEndpoint is the TMDB detail request for a series, with everything the
// cache stores appended.
func detailEndpoint(tvID int) string {
	return fmt.Sprintf("/tv/%d?language=en-US&append_to_response=%s", tvID, tmdb.TVDetailAppend)
}

// fetchSeries fetches a series detail from TMDB with its keywords and
// credits, and caches it.
func (s *Service) fetchSeries(ctx context.Context, tvID int) (*TVSeries, error) {
	return s.fetchSeriesInto(ctx, tvID, s.repo.InsertTVSeries)
}

// fetchSeriesInto fetches a series from TMDB and caches it with store. An
// answer with no name is an error and caches nothing.
func (s *Service) fetchSeriesInto(ctx context.Context, tvID int, store func(context.Context, *TVSeries) error) (*TVSeries, error) {
	raw, err := s.client.TMDBRequest(
		ctx,
		"GET",
		detailEndpoint(tvID),
		nil,
	)
	if err != nil {
		return nil, err
	}

	normalized := NormalizeTMDBTVSeries(raw)
	if !hasName(normalized) {
		return nil, fmt.Errorf("series %d: %w", tvID, errNamelessSeries)
	}
	if err := store(ctx, normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

// errNamelessSeries marks a TMDB series answer with neither a name nor an
// original name. Caching it would show the series as untitled.
var errNamelessSeries = errors.New("tmdb sent a series with no name")

func hasName(series *TVSeries) bool {
	return series.Name != nil && strings.TrimSpace(*series.Name) != ""
}

func (s *Service) getSeasonDetail(ctx context.Context, tvID, seasonNum int) (*TVSeason, error) {
	cached, err := s.repo.GetTVSeasonByTVIDAndNumber(ctx, tvID, seasonNum)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		return cached, nil
	}

	raw, err := s.client.TMDBRequest(
		ctx,
		"GET",
		fmt.Sprintf("/tv/%d/season/%d?language=en-US", tvID, seasonNum),
		nil,
	)
	if err != nil {
		return nil, err
	}

	normalized := NormalizeTMDBTVSeason(raw, tvID)
	if err := s.repo.InsertTVSeason(ctx, normalized); err != nil {
		return nil, err
	}

	if err := s.repo.EnsureMediaIndexSeason(ctx, tvID, seasonNum); err != nil {
		log.Printf("best-effort EnsureMediaIndexSeason failed for tv=%d season=%d: %v", tvID, seasonNum, err)
	}
	// A season carries no genres or keywords of its own. Its series does.
	if err := s.EnsureSeries(ctx, tvID); err != nil {
		log.Printf("best-effort EnsureSeries failed for tv=%d: %v", tvID, err)
	}

	return normalized, nil
}

// RefreshSeries refetches a cached series from TMDB and overwrites the cached
// row. TMDB's terms cap how long its data may be cached, so a series TMDB no
// longer has is deleted with its seasons, and removed reports that.
func (s *Service) RefreshSeries(ctx context.Context, tvID int) (removed bool, err error) {
	raw, err := s.client.TMDBRequest(
		ctx,
		"GET",
		detailEndpoint(tvID),
		nil,
	)
	if tmdb.IsStatus(err, http.StatusNotFound) {
		if err := s.repo.DeleteSeries(ctx, tvID); err != nil {
			return false, fmt.Errorf("delete series %d: %w", tvID, err)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("fetch series %d: %w", tvID, err)
	}
	normalized := NormalizeTMDBTVSeries(raw)
	if !hasName(normalized) {
		return false, fmt.Errorf("refresh series %d: %w", tvID, errNamelessSeries)
	}
	if err := s.repo.ReplaceTVSeries(ctx, normalized); err != nil {
		return false, fmt.Errorf("replace series %d: %w", tvID, err)
	}
	return false, nil
}

// RefreshSeason refetches a cached season from TMDB and overwrites it, or
// deletes it when TMDB no longer has it.
func (s *Service) RefreshSeason(ctx context.Context, tvID, seasonNum int) (removed bool, err error) {
	raw, err := s.client.TMDBRequest(
		ctx,
		"GET",
		fmt.Sprintf("/tv/%d/season/%d?language=en-US", tvID, seasonNum),
		nil,
	)
	if tmdb.IsStatus(err, http.StatusNotFound) {
		if err := s.repo.DeleteSeason(ctx, tvID, seasonNum); err != nil {
			return false, fmt.Errorf("delete season %d of %d: %w", seasonNum, tvID, err)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("fetch season %d of %d: %w", seasonNum, tvID, err)
	}
	if err := s.repo.ReplaceTVSeason(ctx, NormalizeTMDBTVSeason(raw, tvID)); err != nil {
		return false, fmt.Errorf("replace season %d of %d: %w", seasonNum, tvID, err)
	}
	return false, nil
}

func (s *Service) GetTvCollection(ctx context.Context, idType string, id int) (interface{}, error) {
	tvID, _, err := s.resolveTVID(ctx, idType, id, 0)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("/tv/%d?language=en-US", tvID)
	tv, err := s.client.TMDBRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	seasons, ok := tv["seasons"].([]interface{})
	if !ok || seasons == nil {
		return nil, nil
	}

	return seasons, nil
}

func (s *Service) GetTvList(ctx context.Context, listType string, page int) (interface{}, error) {
	endpoint, err := listEndpoint(listType, page)
	if err != nil {
		return nil, err
	}

	return s.client.TMDBRequest(ctx, "GET", endpoint, nil)
}

// tmdbMaxPage is TMDB's hard upper bound on the page parameter.
const tmdbMaxPage = 500

// listEndpoint builds the TMDB list path for a browse page. Out-of-range
// pages clamp rather than error, mirroring the movies package.
func listEndpoint(listType string, page int) (string, error) {
	if page < 1 {
		page = 1
	}
	if page > tmdbMaxPage {
		page = tmdbMaxPage
	}

	var base string
	switch listType {
	case "trending":
		base = "/trending/tv/week"
	case "upcoming":
		base = "/tv/on_the_air"
	case "popular":
		base = "/tv/popular"
	case "top_rated":
		// Top rated follows the shared rule, not TMDB's own list, where a
		// 9.0 with 40 votes outranks The Godfather.
		return toprated.TVPath(page), nil
	default:
		return "", ErrInvalidListType
	}

	return fmt.Sprintf("%s?language=en-US&page=%d", base, page), nil
}
