package tv

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"klyvi-api/internal/platform/db"
)

// RecoCandidateRow is the feed projection of one cached series, the TV twin
// of movies.RecoCandidateRow. It is keyed by the series' 'tv' media_index row,
// so a series without one is never a candidate: the feed could not name it
// in an impression or a dismissal.
//
// GenreIDs are TMDB's TV genre ids as stored (Action & Adventure is 10759,
// not 28). Mapping them into the movie genre space is the feed's job.
type RecoCandidateRow struct {
	TVID         int           `db:"tv_id"`
	MediaID      int           `db:"media_id"`
	Name         *string       `db:"name"`
	PosterPath   *string       `db:"poster_path"`
	BackdropPath *string       `db:"backdrop_path"`
	GenreIDs     db.IntArray   `db:"genre_ids"`
	KeywordIDs   db.IntArray   `db:"keyword_ids"`
	FirstAirDate *sql.NullTime `db:"first_air_date"`
	VoteAverage  float64       `db:"vote_average"`
	VoteCount    int           `db:"vote_count"`
}

// recoCandidateColumns is the select list every RecoCandidateRow query uses,
// over `tv_series s join media_index mi`. The vote columns are nullable on
// tv_series, unlike the struct fields.
const recoCandidateColumns = `
	s.tv_id, mi.media_id, s.name, s.poster_path, s.backdrop_path,
	s.genre_ids, s.keyword_ids, s.first_air_date,
	coalesce(s.vote_average, 0) as vote_average, coalesce(s.vote_count, 0) as vote_count`

// CandidatesByMediaIDs returns the feed projection for the series among a
// set of media_index media_ids. Movie and season ids in the set are ignored,
// so a user's whole history can be passed as is.
func (r *Repository) CandidatesByMediaIDs(ctx context.Context, mediaIDs []int) ([]RecoCandidateRow, error) {
	if len(mediaIDs) == 0 {
		return nil, nil
	}
	var rows []RecoCandidateRow
	err := r.db.SelectContext(ctx, &rows, `
		select `+recoCandidateColumns+`
		from tv_series s
		join media_index mi on mi.id = s.tv_id and mi.media_type = 'tv'
		where mi.media_id = any($1)
	`, mediaIDs)
	return rows, err
}

// RecoCandidatesChangedSince returns the feed projection of every indexed
// series written at or after since, voted or not. The zero time reads the
// whole catalog. It fills the in-process catalog the feed draws from.
func (r *Repository) RecoCandidatesChangedSince(ctx context.Context, since time.Time) ([]RecoCandidateRow, error) {
	var rows []RecoCandidateRow
	err := r.db.SelectContext(ctx, &rows, `
		select `+recoCandidateColumns+`
		from tv_series s
		join media_index mi on mi.id = s.tv_id and mi.media_type = 'tv'
		where coalesce(s.updated_at, 'epoch') >= $1
	`, since)
	return rows, err
}

// ListCandidatesForReco is the popularity fallback: up to limit voted
// series, most-voted first.
func (r *Repository) ListCandidatesForReco(ctx context.Context, limit int) ([]RecoCandidateRow, error) {
	var rows []RecoCandidateRow
	err := r.db.SelectContext(ctx, &rows, `
		select `+recoCandidateColumns+`
		from tv_series s
		join media_index mi on mi.id = s.tv_id and mi.media_type = 'tv'
		where s.vote_count > 0
		order by s.vote_count desc, mi.media_id
		limit $1
	`, limit)
	return rows, err
}

// ListCandidatesByTaste returns up to limit series that share a keyword or
// a genre id with the user's taste, above the vote floor. Keyword matches
// come first and genre-only matches fill the rest, each most-voted first,
// for the reasons given on movies.Repository.ListCandidatesByTaste.
func (r *Repository) ListCandidatesByTaste(ctx context.Context, genreIDs, keywordIDs []int, minVotes, limit int) ([]RecoCandidateRow, error) {
	seen := make(map[int]bool)
	out := make([]RecoCandidateRow, 0, limit)
	add := func(rows []RecoCandidateRow) {
		for _, row := range rows {
			if len(out) >= limit {
				return
			}
			if !seen[row.MediaID] {
				seen[row.MediaID] = true
				out = append(out, row)
			}
		}
	}

	if len(keywordIDs) > 0 {
		rows, err := r.candidatesByOverlap(ctx, "keyword_ids", keywordIDs, minVotes, limit)
		if err != nil {
			return nil, err
		}
		add(rows)
	}
	if len(out) < limit && len(genreIDs) > 0 {
		rows, err := r.candidatesByOverlap(ctx, "genre_ids", genreIDs, minVotes, limit)
		if err != nil {
			return nil, err
		}
		add(rows)
	}
	return out, nil
}

// candidatesByOverlap returns up to limit series whose column (genre_ids or
// keyword_ids, both GIN-indexed) overlaps ids, above the vote floor,
// most-voted first. column is a fixed internal identifier, never user input.
func (r *Repository) candidatesByOverlap(ctx context.Context, column string, ids []int, minVotes, limit int) ([]RecoCandidateRow, error) {
	var rows []RecoCandidateRow
	err := r.db.SelectContext(ctx, &rows, `
		select `+recoCandidateColumns+`
		from tv_series s
		join media_index mi on mi.id = s.tv_id and mi.media_type = 'tv'
		where s.vote_count >= $1 and s.`+column+` && $2::int[]
		order by s.vote_count desc, mi.media_id
		limit $3
	`, minVotes, ids, limit)
	return rows, err
}

// CardDetails is the card text the feed shows for one series: its synopsis,
// its genre names in TMDB order (TV's own, so Sci-Fi & Fantasy stays one
// genre), and its season count where a film's card has its runtime.
type CardDetails struct {
	MediaID     int
	Overview    string
	Genres      []string
	SeasonCount int
}

// CardDetailsByMediaIDs returns the card text for the series in a ranked
// feed, the TV twin of movies.Repository.CardDetailsByMediaIDs. Ids that are
// not a cached series are absent.
func (r *Repository) CardDetailsByMediaIDs(ctx context.Context, mediaIDs []int) ([]CardDetails, error) {
	if len(mediaIDs) == 0 {
		return []CardDetails{}, nil
	}
	var rows []struct {
		MediaID     int              `db:"media_id"`
		Overview    string           `db:"overview"`
		SeasonCount int              `db:"number_of_seasons"`
		Genres      *json.RawMessage `db:"genres"`
	}
	if err := r.db.SelectContext(ctx, &rows, `
		select mi.media_id, coalesce(s.overview, '') as overview,
		       coalesce(s.number_of_seasons, 0) as number_of_seasons, s.genres
		from tv_series s
		join media_index mi on mi.id = s.tv_id and mi.media_type = 'tv'
		where mi.media_id = any($1)
	`, mediaIDs); err != nil {
		return nil, fmt.Errorf("select series card details: %w", err)
	}

	out := make([]CardDetails, len(rows))
	for i, row := range rows {
		out[i] = CardDetails{
			MediaID:     row.MediaID,
			Overview:    row.Overview,
			Genres:      genreNames(row.Genres),
			SeasonCount: row.SeasonCount,
		}
	}
	return out, nil
}

// genreNames reads the names out of a TMDB genres array, in order. A missing
// or malformed column yields no names: the card goes without genres rather
// than failing the whole feed over one row.
func genreNames(raw *json.RawMessage) []string {
	names := []string{}
	if raw == nil {
		return names
	}
	var genres []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(*raw, &genres); err != nil {
		return names
	}
	for _, g := range genres {
		if g.Name != "" {
			names = append(names, g.Name)
		}
	}
	return names
}

// LookupGenreNames returns id→name pairs for the requested genre ids from
// the series cache. The feed reads TV genres in the film genre space, so
// this only adds the genres films lack (Kids, News, Reality, Soap, Talk).
// Ids no series carries are absent from the map.
func (r *Repository) LookupGenreNames(ctx context.Context, ids []int) (map[int]string, error) {
	if len(ids) == 0 {
		return map[int]string{}, nil
	}
	var rows []struct {
		ID   int    `db:"id"`
		Name string `db:"name"`
	}
	if err := r.db.SelectContext(ctx, &rows, `
		select distinct (elem->>'id')::int as id, elem->>'name' as name
		from tv_series, jsonb_array_elements(genres) elem
		where genre_ids && $1::int[] and (elem->>'id')::int = any($1)
	`, ids); err != nil {
		return nil, err
	}
	out := make(map[int]string, len(rows))
	for _, row := range rows {
		out[row.ID] = row.Name
	}
	return out, nil
}

// LookupKeywordNames returns id→name pairs for the requested keyword ids
// from the series cache. Keyword ids are shared with movies, so this only
// adds the names of keywords no cached movie carries. Ids no series carries
// are absent from the map. It reads both stored keyword shapes, as
// keyword_ids does (00030).
func (r *Repository) LookupKeywordNames(ctx context.Context, ids []int) (map[int]string, error) {
	if len(ids) == 0 {
		return map[int]string{}, nil
	}
	var rows []struct {
		ID   int    `db:"id"`
		Name string `db:"name"`
	}
	if err := r.db.SelectContext(ctx, &rows, `
		select distinct (elem->>'id')::int as id, elem->>'name' as name
		from tv_series,
		     jsonb_array_elements(case
		         when jsonb_typeof(keywords) = 'array' then keywords
		         when jsonb_typeof(keywords -> 'results') = 'array' then keywords -> 'results'
		         else '[]'::jsonb
		     end) elem
		where keyword_ids && $1::int[] and (elem->>'id')::int = any($1)
	`, ids); err != nil {
		return nil, err
	}
	out := make(map[int]string, len(rows))
	for _, row := range rows {
		out[row.ID] = row.Name
	}
	return out, nil
}
