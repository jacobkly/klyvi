package movies

import (
	"encoding/json"
	"time"
)

func NormalizeTMDBMovie(raw map[string]interface{}) *Movie {
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

	// TMDB returns keywords wrapped: { "keywords": { "keywords": [...] } }.
	// Store the inner array directly so the column holds the useful shape.
	var keywordsField *json.RawMessage
	if wrapper, ok := raw["keywords"].(map[string]interface{}); ok {
		keywordsField = j(wrapper["keywords"])
	}

	return &Movie{
		MovieID:             intVal("id"),
		Adult:               boolVal("adult"),
		BackdropPath:        str("backdrop_path"),
		BelongsToCollection: j(raw["belongs_to_collection"]),
		Budget:              intVal("budget"),
		Genres:              j(raw["genres"]),
		Homepage:            str("homepage"),
		IMDBID:              str("imdb_id"),
		OriginalLanguage:    str("original_language"),
		OriginalTitle:       str("original_title"),
		Overview:            str("overview"),
		Popularity:          floatVal("popularity"),
		PosterPath:          str("poster_path"),
		ProductionCompanies: j(raw["production_companies"]),
		ProductionCountries: j(raw["production_countries"]),
		ReleaseDate:         parseDate("release_date"),
		Revenue:             int64(floatVal("revenue")),
		Runtime:             intVal("runtime"),
		SpokenLanguages:     j(raw["spoken_languages"]),
		Status:              str("status"),
		Tagline:             str("tagline"),
		Title:               str("title"),
		Video:               boolVal("video"),
		VoteAverage:         floatVal("vote_average"),
		VoteCount:           intVal("vote_count"),
		Keywords:            keywordsField,
		Credits:             j(raw["credits"]),
	}
}

func NormalizeTMDBMovieCollection(
	raw map[string]interface{},
	collectionID int,
) []MovieCollection {
	parts, ok := raw["parts"].([]interface{})
	if !ok {
		return nil
	}

	var result []MovieCollection

	for i, p := range parts {
		m := p.(map[string]interface{})

		var poster *string
		if v, ok := m["poster_path"].(string); ok {
			poster = &v
		}

		var vote *float64
		if v, ok := m["vote_average"].(float64); ok {
			vv := v
			vote = &vv
		}

		pos := i + 1

		result = append(result, MovieCollection{
			CollectionID: collectionID,
			MovieID:      int(m["id"].(float64)),
			Name:         m["title"].(string),
			PosterPath:   poster,
			VoteAverage:  vote,
			Position:     &pos,
		})
	}

	return result
}
