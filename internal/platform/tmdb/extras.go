package tmdb

import (
	"regexp"
	"strings"
)

// MovieDetailAppend and TVDetailAppend are the append_to_response lists of a
// detail fetch. One request brings the keywords and credits the recommender
// reads, plus the age rating, trailers and (for TV) external ids the title
// pages show.
const (
	MovieDetailAppend = "keywords,credits,release_dates,videos"
	TVDetailAppend    = "keywords,credits,content_ratings,videos,external_ids"
)

// RatingRegion is the country whose age rating Klyvi shows.
const RatingRegion = "US"

var (
	youTubeKey = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	imdbID     = regexp.MustCompile(`^tt\d+$`)
)

// HasExtras reports whether a decoded detail answer was fetched with the
// appended extras. A row cached without them is refetched on first view.
func HasExtras(raw map[string]any) bool {
	_, ok := raw["videos"].(map[string]any)
	return ok
}

// results returns raw[key]["results"] as a list of objects.
func results(raw map[string]any, key string) []map[string]any {
	wrapper, ok := raw[key].(map[string]any)
	if !ok {
		return nil
	}
	list, _ := wrapper["results"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func text(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// MovieCertification is a film's US age rating ("PG-13") from an appended
// release_dates, or nil. The theatrical release's rating wins, then the
// limited release's, then any other US rating.
func MovieCertification(raw map[string]any) *string {
	for _, country := range results(raw, "release_dates") {
		if text(country, "iso_3166_1") != RatingRegion {
			continue
		}
		dates, _ := country["release_dates"].([]any)
		best, bestRank := "", 0
		for _, d := range dates {
			m, ok := d.(map[string]any)
			if !ok {
				continue
			}
			cert := text(m, "certification")
			if cert == "" {
				continue
			}
			rank := 1
			switch t, _ := m["type"].(float64); t {
			case 3: // theatrical
				rank = 3
			case 2: // limited
				rank = 2
			}
			if rank > bestRank {
				best, bestRank = cert, rank
			}
		}
		return ptr(best)
	}
	return nil
}

// TVRating is a series' US age rating ("TV-MA") from an appended
// content_ratings, or nil.
func TVRating(raw map[string]any) *string {
	for _, country := range results(raw, "content_ratings") {
		if text(country, "iso_3166_1") == RatingRegion {
			return ptr(text(country, "rating"))
		}
	}
	return nil
}

// TrailerKey is the YouTube id of a title's trailer from an appended videos,
// or nil. An official trailer beats a fan upload, and the first one released
// beats later cuts. A key that isn't a YouTube id is never returned, since the
// web builds a player URL from it.
func TrailerKey(raw map[string]any) *string {
	best, bestOfficial, bestAt := "", false, ""
	for _, v := range results(raw, "videos") {
		key := text(v, "key")
		if text(v, "site") != "YouTube" || text(v, "type") != "Trailer" || !youTubeKey.MatchString(key) {
			continue
		}
		official, _ := v["official"].(bool)
		at := text(v, "published_at")
		better := best == "" ||
			(official && !bestOfficial) ||
			(official == bestOfficial && at != "" && (bestAt == "" || at < bestAt))
		if better {
			best, bestOfficial, bestAt = key, official, at
		}
	}
	return ptr(best)
}

// ExternalIMDbID is the IMDb id from an appended external_ids, or nil.
func ExternalIMDbID(raw map[string]any) *string {
	ids, ok := raw["external_ids"].(map[string]any)
	if !ok {
		return nil
	}
	id := text(ids, "imdb_id")
	if !imdbID.MatchString(id) {
		return nil
	}
	return &id
}
