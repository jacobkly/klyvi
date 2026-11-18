package opendata

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jmoiron/sqlx"

	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/reco"
)

// Repository owns the SQL for the wikidata_facts table.
type Repository struct {
	db *sqlx.DB
}

// NewRepository builds a Repository on an open connection.
func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// RecoFacts returns the facts the recommender matches titles on: each title's
// directors, main subjects, narrative locations and source material as one
// sorted list. Titles with none of them are left out.
func (r *Repository) RecoFacts(ctx context.Context) ([]reco.TitleFacts, error) {
	var rows []struct {
		MediaType string      `db:"media_type"`
		TMDBID    int         `db:"tmdb_id"`
		IDs       db.IntArray `db:"ids"`
	}
	if err := r.db.SelectContext(ctx, &rows, `
		select media_type, tmdb_id,
		       array(select distinct u from unnest(directors || subjects || locations || based_on) u order by u) as ids
		  from wikidata_facts
		 where cardinality(directors || subjects || locations || based_on) > 0`); err != nil {
		return nil, fmt.Errorf("reco facts: %w", err)
	}
	out := make([]reco.TitleFacts, len(rows))
	for i, row := range rows {
		out[i] = reco.TitleFacts{MediaType: row.MediaType, TMDBID: row.TMDBID, IDs: row.IDs}
	}
	return out, nil
}

// CatalogIDs returns the TMDB id of every catalog title of the type, in order.
func (r *Repository) CatalogIDs(ctx context.Context, mediaType string) ([]int, error) {
	ids := []int{}
	if err := r.db.SelectContext(ctx, &ids,
		`select id from media_index where media_type = $1 order by id`, mediaType); err != nil {
		return nil, fmt.Errorf("catalog ids: %w", err)
	}
	return ids, nil
}

// Save stores the facts found for one batch of TMDB ids and drops the rows of
// ids in the batch that no longer match. It returns how many rows it inserted,
// changed or dropped. A row whose facts are unchanged is not touched, so a
// rerun against an unchanged Wikidata changes nothing.
func (r *Repository) Save(ctx context.Context, mediaType string, batch []int, facts []Facts) (int, error) {
	matched := make([]int, 0, len(facts))
	for i := range facts {
		facts[i].MediaType = mediaType
		facts[i].normalize()
		matched = append(matched, facts[i].TMDBID)
	}
	rows, err := json.Marshal(facts)
	if err != nil {
		return 0, fmt.Errorf("encode facts: %w", err)
	}

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		insert into wikidata_facts (media_type, tmdb_id, wikidata_id, wikipedia_url, genres,
			cast_members, directors, subjects, locations, based_on, countries, series, franchises)
		select media_type, tmdb_id, wikidata_id, wikipedia_url, genres,
			cast_members, directors, subjects, locations, based_on, countries, series, franchises
		from jsonb_to_recordset($1::jsonb) as r(media_type text, tmdb_id int, wikidata_id int,
			wikipedia_url text, genres int[], cast_members int[], directors int[], subjects int[],
			locations int[], based_on int[], countries int[], series int[], franchises int[])
		on conflict (media_type, tmdb_id) do update set
			wikidata_id = excluded.wikidata_id, wikipedia_url = excluded.wikipedia_url,
			genres = excluded.genres, cast_members = excluded.cast_members,
			directors = excluded.directors, subjects = excluded.subjects,
			locations = excluded.locations, based_on = excluded.based_on,
			countries = excluded.countries, series = excluded.series,
			franchises = excluded.franchises, updated_at = now()
		where (wikidata_facts.wikidata_id, wikidata_facts.wikipedia_url, wikidata_facts.genres,
				wikidata_facts.cast_members, wikidata_facts.directors, wikidata_facts.subjects,
				wikidata_facts.locations, wikidata_facts.based_on, wikidata_facts.countries,
				wikidata_facts.series, wikidata_facts.franchises)
			is distinct from (excluded.wikidata_id, excluded.wikipedia_url, excluded.genres,
				excluded.cast_members, excluded.directors, excluded.subjects, excluded.locations,
				excluded.based_on, excluded.countries, excluded.series, excluded.franchises)
	`, string(rows))
	if err != nil {
		return 0, fmt.Errorf("upsert facts: %w", err)
	}
	upserted, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("upsert facts: %w", err)
	}

	res, err = tx.ExecContext(ctx, `
		delete from wikidata_facts
		where media_type = $1 and tmdb_id = any($2::int[]) and not (tmdb_id = any($3::int[]))
	`, mediaType, batch, matched)
	if err != nil {
		return 0, fmt.Errorf("drop unmatched facts: %w", err)
	}
	dropped, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("drop unmatched facts: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return int(upserted + dropped), nil
}

// Coverage is how much of one media type's catalog Wikidata covers.
type Coverage struct {
	MediaType     string `db:"media_type"`
	Titles        int    `db:"titles"`
	Matched       int    `db:"matched"`
	WithCast      int    `db:"with_cast"`
	WithSubject   int    `db:"with_subject"`
	WithLocation  int    `db:"with_location"`
	WithDirector  int    `db:"with_director"`
	WithWikipedia int    `db:"with_wikipedia"`
}

// Coverage reports, per media type, how many catalog titles have a Wikidata
// match and how many of those carry each kind of fact.
func (r *Repository) Coverage(ctx context.Context) ([]Coverage, error) {
	out := []Coverage{}
	err := r.db.SelectContext(ctx, &out, `
		select mi.media_type, count(*) as titles, count(f.tmdb_id) as matched,
			count(*) filter (where cardinality(f.cast_members) > 0) as with_cast,
			count(*) filter (where cardinality(f.subjects) > 0) as with_subject,
			count(*) filter (where cardinality(f.locations) > 0) as with_location,
			count(*) filter (where cardinality(f.directors) > 0) as with_director,
			count(*) filter (where f.wikipedia_url is not null) as with_wikipedia
		from media_index mi
		left join wikidata_facts f on f.media_type = mi.media_type and f.tmdb_id = mi.id
		where mi.media_type in ('movie', 'tv')
		group by mi.media_type
		order by mi.media_type
	`)
	if err != nil {
		return nil, fmt.Errorf("coverage: %w", err)
	}
	return out, nil
}
