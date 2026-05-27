package tv

import (
	"encoding/json"
	"time"
)

// NormalizeTMDBTVSeries converts a raw TMDB series detail payload (with
// append_to_response=keywords,credits) into the cache shape. TV's append
// wraps keywords as { "results": [...] } (movies uses "keywords") — we
// store the inner array directly so the column holds the useful shape.
func NormalizeTMDBTVSeries(raw map[string]interface{}) *TVSeries {
	j := func(v interface{}) *json.RawMessage {
		if v == nil {
			return nil
		}
		b, _ := json.Marshal(v)
		rm := json.RawMessage(b)
		return &rm
	}

	str := func(k string) *string {
		if v, ok := raw[k].(string); ok {
			return &v
		}
		return nil
	}

	intVal := func(k string) int {
		if v, ok := raw[k].(float64); ok {
			return int(v)
		}
		return 0
	}

	floatVal := func(k string) float64 {
		if v, ok := raw[k].(float64); ok {
			return v
		}
		return 0
	}

	boolVal := func(k string) bool {
		if v, ok := raw[k].(bool); ok {
			return v
		}
		return false
	}

	parseDate := func(k string) *time.Time {
		v, ok := raw[k].(string)
		if !ok || v == "" {
			return nil
		}
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return nil
		}
		return &t
	}

	// TV's append_to_response wraps keywords under "results", not "keywords".
	var keywordsField *json.RawMessage
	if wrapper, ok := raw["keywords"].(map[string]interface{}); ok {
		keywordsField = j(wrapper["results"])
	}

	return &TVSeries{
		TVID:                intVal("id"),
		Adult:               boolVal("adult"),
		BackdropPath:        str("backdrop_path"),
		CreatedBy:           j(raw["created_by"]),
		FirstAirDate:        parseDate("first_air_date"),
		Genres:              j(raw["genres"]),
		Homepage:            str("homepage"),
		InProduction:        boolVal("in_production"),
		LastAirDate:         parseDate("last_air_date"),
		LastEpisodeToAir:    j(raw["last_episode_to_air"]),
		NextEpisodeToAir:    j(raw["next_episode_to_air"]),
		Networks:            j(raw["networks"]),
		NumberOfEpisodes:    intVal("number_of_episodes"),
		NumberOfSeasons:     intVal("number_of_seasons"),
		OriginalLanguage:    str("original_language"),
		OriginalName:        str("original_name"),
		Overview:            str("overview"),
		Popularity:          floatVal("popularity"),
		PosterPath:          str("poster_path"),
		ProductionCompanies: j(raw["production_companies"]),
		ProductionCountries: j(raw["production_countries"]),
		Seasons:             j(raw["seasons"]),
		SpokenLanguages:     j(raw["spoken_languages"]),
		Status:              str("status"),
		Tagline:             str("tagline"),
		Type:                str("type"),
		VoteAverage:         floatVal("vote_average"),
		VoteCount:           intVal("vote_count"),
		Keywords:            keywordsField,
		Credits:             j(raw["credits"]),
	}
}

// NormalizeTMDBTVSeason converts a TMDB season detail payload into the cache
// shape. The season payload does not include its parent tv_id, so the caller
// must pass it explicitly. TMDB season detail does not return networks or a
// vote_count, so those columns stay at their defaults.
func NormalizeTMDBTVSeason(raw map[string]interface{}, tvID int) *TVSeason {
	j := func(v interface{}) *json.RawMessage {
		if v == nil {
			return nil
		}
		b, _ := json.Marshal(v)
		rm := json.RawMessage(b)
		return &rm
	}

	str := func(k string) *string {
		if v, ok := raw[k].(string); ok {
			return &v
		}
		return nil
	}

	intVal := func(k string) int {
		if v, ok := raw[k].(float64); ok {
			return int(v)
		}
		return 0
	}

	floatVal := func(k string) float64 {
		if v, ok := raw[k].(float64); ok {
			return v
		}
		return 0
	}

	parseDate := func(k string) *time.Time {
		v, ok := raw[k].(string)
		if !ok || v == "" {
			return nil
		}
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return nil
		}
		return &t
	}

	return &TVSeason{
		TVID:         tvID,
		SeasonNumber: intVal("season_number"),
		AirDate:      parseDate("air_date"),
		Name:         str("name"),
		Overview:     str("overview"),
		PosterPath:   str("poster_path"),
		VoteAverage:  floatVal("vote_average"),
		Episodes:     j(raw["episodes"]),
	}
}
