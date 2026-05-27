package tv

import (
	"context"
	"fmt"
	"log"
)

// TMDBClient is the only external dependency the TV service currently has.
// Defining the interface here keeps the service coupled to behavior, not to
// one specific HTTP client implementation.
type TMDBClient interface {
	TMDBRequest(method, endpoint string, body interface{}) (map[string]interface{}, error)
}

// TVRepository is the persistence boundary the TV service depends on. The
// interface lives in the consumer package per the repo's convention
// (matching movies/service.go).
type TVRepository interface {
	GetTVSeriesByID(ctx context.Context, tvID int) (*TVSeries, error)
	InsertTVSeries(ctx context.Context, series *TVSeries) error
	GetTVSeasonByTVIDAndNumber(ctx context.Context, tvID, seasonNumber int) (*TVSeason, error)
	InsertTVSeason(ctx context.Context, season *TVSeason) error
	EnsureMediaIndexSeason(ctx context.Context, tvID, seasonNumber int) error
	GetTVIDAndSeasonByMediaID(ctx context.Context, mediaID int) (int, int, error)
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
			return 0, 0, fmt.Errorf("media not found")
		}
		// If the caller passed an explicit seasonNum it wins (e.g. asking
		// for series-level data via an internal id sends seasonNum=0).
		if seasonNum > 0 {
			return tvID, seasonNum, nil
		}
		return tvID, indexedSeasonNum, nil
	default:
		return 0, 0, fmt.Errorf("invalid id type: must be 'external' or 'internal'")
	}
}

func (s *Service) getSeriesDetail(ctx context.Context, tvID int) (*TVSeries, error) {
	cached, err := s.repo.GetTVSeriesByID(ctx, tvID)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		return cached, nil
	}

	raw, err := s.client.TMDBRequest(
		"GET",
		fmt.Sprintf("/tv/%d?language=en-US&append_to_response=keywords,credits", tvID),
		nil,
	)
	if err != nil {
		return nil, err
	}

	normalized := NormalizeTMDBTVSeries(raw)
	if err := s.repo.InsertTVSeries(ctx, normalized); err != nil {
		return nil, err
	}

	return normalized, nil
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
		"GET",
		fmt.Sprintf("/tv/%d/season/%d?language=en-US&append_to_response=credits", tvID, seasonNum),
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

	return normalized, nil
}

func (s *Service) GetTvRecommendations(ctx context.Context, idType string, id int) (interface{}, error) {
	tvID, _, err := s.resolveTVID(ctx, idType, id, 0)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("/tv/%d/recommendations?language=en-US&page=1", tvID)
	return s.client.TMDBRequest("GET", endpoint, nil)
}

func (s *Service) GetTvCollection(ctx context.Context, idType string, id int) (interface{}, error) {
	tvID, _, err := s.resolveTVID(ctx, idType, id, 0)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("/tv/%d?language=en-US", tvID)
	tv, err := s.client.TMDBRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	seasons, ok := tv["seasons"].([]interface{})
	if !ok || seasons == nil {
		return nil, nil
	}

	return seasons, nil
}

func (s *Service) GetTvList(listType string) (interface{}, error) {
	var endpoint string

	switch listType {
	case "trending":
		endpoint = "/trending/tv/week?language=en-US"
	case "upcoming":
		endpoint = "/tv/on_the_air?language=en-US&page=1"
	case "popular":
		endpoint = "/tv/popular?language=en-US&page=1"
	case "top_rated":
		endpoint = "/tv/top_rated?language=en-US&page=1"
	default:
		return nil, fmt.Errorf("invalid list type")
	}

	return s.client.TMDBRequest("GET", endpoint, nil)
}
