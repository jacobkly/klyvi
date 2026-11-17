package similar

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jmoiron/sqlx"

	"klyvi-api/internal/platform/db"
)

// SQLCatalog reads shelves' titles from the cached catalog tables. One is
// built per media type, over movies or tv_series.
type SQLCatalog struct {
	db *sqlx.DB
	q  catalogQueries
}

type catalogQueries struct {
	source, candidates, byIDs, resolve string
}

// NewMovieCatalog reads movies. A movie's collection comes from its
// belongs_to_collection.
func NewMovieCatalog(dbConn *sqlx.DB) *SQLCatalog {
	return &SQLCatalog{db: dbConn, q: catalogQueries{
		source: `
			select movie_id as tmdb_id, genre_ids, keyword_ids,
			       coalesce((belongs_to_collection->>'id')::int, 0) as collection_id,
			       genres, keywords
			from movies
			where movie_id = $1`,
		// Most shared keywords first, so the pool holds the best overlap
		// rather than the most popular titles that share one broad tag.
		candidates: `
			select m.movie_id as tmdb_id, mi.media_id,
			       coalesce(m.title, '') as title, coalesce(m.poster_path, '') as poster_path,
			       coalesce(extract(year from m.release_date)::int, 0) as year,
			       m.genre_ids, m.keyword_ids,
			       coalesce((m.belongs_to_collection->>'id')::int, 0) as collection_id
			from movies m
			join media_index mi on mi.id = m.movie_id and mi.media_type = 'movie'
			where m.vote_count >= $1 and m.keyword_ids && $2::int[] and m.movie_id <> $3
			order by (select count(*) from unnest(m.keyword_ids) k where k = any($2::int[])) desc,
			         m.vote_count desc, m.movie_id
			limit $4`,
		byIDs: `
			select m.movie_id as tmdb_id, mi.media_id,
			       coalesce(m.title, '') as title, coalesce(m.poster_path, '') as poster_path,
			       coalesce(extract(year from m.release_date)::int, 0) as year,
			       m.genre_ids, m.keyword_ids,
			       coalesce((m.belongs_to_collection->>'id')::int, 0) as collection_id
			from movies m
			join media_index mi on mi.id = m.movie_id and mi.media_type = 'movie'
			where m.vote_count >= $1 and m.movie_id = any($2::int[])
			order by m.movie_id`,
		resolve: `
			select mi.id as tmdb_id, mi.media_id,
			       coalesce((m.belongs_to_collection->>'id')::int, 0) as collection_id
			from media_index mi
			left join movies m on m.movie_id = mi.id
			where mi.media_type = 'movie' and mi.id = any($1::int[])`,
	}}
}

// NewTVCatalog reads series. Series have no collections. It needs the
// feature arrays from migration 00030.
func NewTVCatalog(dbConn *sqlx.DB) *SQLCatalog {
	return &SQLCatalog{db: dbConn, q: catalogQueries{
		source: `
			select tv_id as tmdb_id, genre_ids, keyword_ids, 0 as collection_id,
			       genres, keywords
			from tv_series
			where tv_id = $1`,
		candidates: `
			select s.tv_id as tmdb_id, mi.media_id,
			       coalesce(s.name, '') as title, coalesce(s.poster_path, '') as poster_path,
			       coalesce(extract(year from s.first_air_date)::int, 0) as year,
			       s.genre_ids, s.keyword_ids, 0 as collection_id
			from tv_series s
			join media_index mi on mi.id = s.tv_id and mi.media_type = 'tv'
			where s.vote_count >= $1 and s.keyword_ids && $2::int[] and s.tv_id <> $3
			order by (select count(*) from unnest(s.keyword_ids) k where k = any($2::int[])) desc,
			         s.vote_count desc, s.tv_id
			limit $4`,
		byIDs: `
			select s.tv_id as tmdb_id, mi.media_id,
			       coalesce(s.name, '') as title, coalesce(s.poster_path, '') as poster_path,
			       coalesce(extract(year from s.first_air_date)::int, 0) as year,
			       s.genre_ids, s.keyword_ids, 0 as collection_id
			from tv_series s
			join media_index mi on mi.id = s.tv_id and mi.media_type = 'tv'
			where s.vote_count >= $1 and s.tv_id = any($2::int[])
			order by s.tv_id`,
		resolve: `
			select mi.id as tmdb_id, mi.media_id, 0 as collection_id
			from media_index mi
			where mi.media_type = 'tv' and mi.id = any($1::int[])`,
	}}
}

type sourceRow struct {
	TMDBID       int         `db:"tmdb_id"`
	GenreIDs     db.IntArray `db:"genre_ids"`
	KeywordIDs   db.IntArray `db:"keyword_ids"`
	CollectionID int         `db:"collection_id"`
	Genres       []byte      `db:"genres"`
	Keywords     []byte      `db:"keywords"`
}

// Source returns nil when the title is not cached.
func (c *SQLCatalog) Source(ctx context.Context, tmdbID int) (*Source, error) {
	var row sourceRow
	err := c.db.GetContext(ctx, &row, c.q.source, tmdbID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Source{
		TMDBID:       row.TMDBID,
		GenreIDs:     row.GenreIDs,
		KeywordIDs:   row.KeywordIDs,
		CollectionID: row.CollectionID,
		GenreNames:   decodeNames(row.Genres),
		KeywordNames: decodeNames(row.Keywords),
	}, nil
}

type candidateRow struct {
	TMDBID       int         `db:"tmdb_id"`
	MediaID      int         `db:"media_id"`
	Title        string      `db:"title"`
	PosterPath   string      `db:"poster_path"`
	Year         int         `db:"year"`
	GenreIDs     db.IntArray `db:"genre_ids"`
	KeywordIDs   db.IntArray `db:"keyword_ids"`
	CollectionID int         `db:"collection_id"`
}

// Candidates returns up to limit indexed titles with at least minVotes votes
// that share a keyword with src, most shared keywords first, then most votes.
func (c *SQLCatalog) Candidates(ctx context.Context, src *Source, minVotes, limit int) ([]Candidate, error) {
	if len(src.KeywordIDs) == 0 {
		return []Candidate{}, nil
	}
	var rows []candidateRow
	if err := c.db.SelectContext(ctx, &rows, c.q.candidates, minVotes, []int(src.KeywordIDs), src.TMDBID, limit); err != nil {
		return nil, err
	}
	return toCandidates(rows), nil
}

// ByTMDBIDs returns the indexed titles among tmdbIDs with at least minVotes
// votes, by TMDB id.
func (c *SQLCatalog) ByTMDBIDs(ctx context.Context, tmdbIDs []int, minVotes int) ([]Candidate, error) {
	if len(tmdbIDs) == 0 {
		return []Candidate{}, nil
	}
	var rows []candidateRow
	if err := c.db.SelectContext(ctx, &rows, c.q.byIDs, minVotes, tmdbIDs); err != nil {
		return nil, err
	}
	return toCandidates(rows), nil
}

func toCandidates(rows []candidateRow) []Candidate {
	out := make([]Candidate, len(rows))
	for i, r := range rows {
		out[i] = Candidate{
			TMDBID:       r.TMDBID,
			MediaID:      r.MediaID,
			Title:        r.Title,
			PosterPath:   r.PosterPath,
			Year:         r.Year,
			GenreIDs:     r.GenreIDs,
			KeywordIDs:   r.KeywordIDs,
			CollectionID: r.CollectionID,
		}
	}
	return out
}

// Resolve returns the indexed titles among tmdbIDs, keyed by TMDB id.
func (c *SQLCatalog) Resolve(ctx context.Context, tmdbIDs []int) (map[int]Resolved, error) {
	out := make(map[int]Resolved, len(tmdbIDs))
	if len(tmdbIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		TMDBID       int `db:"tmdb_id"`
		MediaID      int `db:"media_id"`
		CollectionID int `db:"collection_id"`
	}
	if err := c.db.SelectContext(ctx, &rows, c.q.resolve, tmdbIDs); err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.TMDBID] = Resolved{MediaID: r.MediaID, CollectionID: r.CollectionID}
	}
	return out, nil
}

// decodeNames reads {id, name} pairs from a cached genres or keywords
// column. The normalizers store a bare array, but rows cached before they
// unwrapped TMDB's envelope may still hold {"results": [...]} or
// {"keywords": [...]}.
func decodeNames(raw []byte) map[int]string {
	type named struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	out := map[int]string{}
	if len(raw) == 0 {
		return out
	}
	var list []named
	if err := json.Unmarshal(raw, &list); err != nil {
		var envelope struct {
			Results  []named `json:"results"`
			Keywords []named `json:"keywords"`
		}
		if json.Unmarshal(raw, &envelope) != nil {
			return out
		}
		list = append(envelope.Results, envelope.Keywords...)
	}
	for _, n := range list {
		if n.Name != "" {
			out[n.ID] = n.Name
		}
	}
	return out
}
