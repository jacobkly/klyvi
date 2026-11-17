package plots

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"
)

// Repository owns the SQL for the wikipedia_plots table.
type Repository struct {
	db *sqlx.DB
}

// NewRepository builds a Repository on an open connection.
func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// Targets returns every title of the type with an English Wikipedia article,
// with the revision and model of its stored row, in TMDB id order. It reads
// the article link from wikidata_facts and nothing from TMDB's tables.
func (r *Repository) Targets(ctx context.Context, mediaType string) ([]Target, error) {
	out := []Target{}
	if err := r.db.SelectContext(ctx, &out, `
		select f.tmdb_id, f.wikipedia_url, p.revision_id, p.model
		  from wikidata_facts f
		  left join wikipedia_plots p on p.media_type = f.media_type and p.tmdb_id = f.tmdb_id
		 where f.media_type = $1 and f.wikipedia_url is not null
		 order by f.tmdb_id`, mediaType); err != nil {
		return nil, fmt.Errorf("plot targets: %w", err)
	}
	return out, nil
}

// Save inserts or replaces the rows of the plots.
func (r *Repository) Save(ctx context.Context, mediaType string, plots []Plot) error {
	if len(plots) == 0 {
		return nil
	}
	type row struct {
		TMDBID     int     `json:"tmdb_id"`
		RevisionID int64   `json:"revision_id"`
		Model      string  `json:"model"`
		Words      int     `json:"plot_words"`
		Embedding  *string `json:"embedding"`
	}
	rows := make([]row, len(plots))
	for i, p := range plots {
		rows[i] = row{TMDBID: p.TMDBID, RevisionID: p.RevisionID, Model: p.Model, Words: p.Words}
		if p.Embedding != nil {
			v := vectorLiteral(p.Embedding)
			rows[i].Embedding = &v
		}
	}
	data, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("encode plots: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, `
		insert into wikipedia_plots (media_type, tmdb_id, revision_id, model, plot_words, embedding)
		select $1, tmdb_id, revision_id, model, plot_words, embedding::extensions.halfvec
		  from jsonb_to_recordset($2::jsonb) as r(tmdb_id int, revision_id bigint, model text,
			plot_words int, embedding text)
		on conflict (media_type, tmdb_id) do update set
			revision_id = excluded.revision_id, model = excluded.model,
			plot_words = excluded.plot_words, embedding = excluded.embedding, updated_at = now()
	`, mediaType, string(data)); err != nil {
		return fmt.Errorf("save plots: %w", err)
	}
	return nil
}

// Delete drops the rows of these titles.
func (r *Repository) Delete(ctx context.Context, mediaType string, tmdbIDs []int) error {
	if _, err := r.db.ExecContext(ctx,
		`delete from wikipedia_plots where media_type = $1 and tmdb_id = any($2::int[])`,
		mediaType, tmdbIDs); err != nil {
		return fmt.Errorf("delete plots: %w", err)
	}
	return nil
}

// Prune drops the rows of titles that no longer have an article link, and
// returns how many it dropped.
func (r *Repository) Prune(ctx context.Context, mediaType string) (int, error) {
	res, err := r.db.ExecContext(ctx, `
		delete from wikipedia_plots p
		 where p.media_type = $1
		   and not exists (select 1 from wikidata_facts f
		                    where f.media_type = p.media_type and f.tmdb_id = p.tmdb_id
		                      and f.wikipedia_url is not null)`, mediaType)
	if err != nil {
		return 0, fmt.Errorf("prune plots: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune plots: %w", err)
	}
	return int(n), nil
}

// Coverage is how much of one media type's catalog has a plot vector.
type Coverage struct {
	MediaType   string `db:"media_type"`
	Titles      int    `db:"titles"`
	WithArticle int    `db:"with_article"`
	WithVector  int    `db:"with_vector"`
	MedianWords int    `db:"median_words"`
}

// Coverage reports, per media type, how many catalog titles have an article
// and how many a plot vector, plus the median plot length of those embedded.
func (r *Repository) Coverage(ctx context.Context) ([]Coverage, error) {
	out := []Coverage{}
	if err := r.db.SelectContext(ctx, &out, `
		select mi.media_type, count(*) as titles,
		       count(*) filter (where f.wikipedia_url is not null) as with_article,
		       count(p.embedding) as with_vector,
		       coalesce(percentile_disc(0.5) within group (order by p.plot_words)
		                filter (where p.embedding is not null), 0) as median_words
		  from media_index mi
		  left join wikidata_facts f on f.media_type = mi.media_type and f.tmdb_id = mi.id
		  left join wikipedia_plots p on p.media_type = mi.media_type and p.tmdb_id = mi.id
		 where mi.media_type in ('movie', 'tv')
		 group by mi.media_type
		 order by mi.media_type`); err != nil {
		return nil, fmt.Errorf("plot coverage: %w", err)
	}
	return out, nil
}

// TableBytes is the on-disk size of the table, its index and TOAST included.
func (r *Repository) TableBytes(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.GetContext(ctx, &n, `select pg_total_relation_size('public.wikipedia_plots')`); err != nil {
		return 0, fmt.Errorf("plot table size: %w", err)
	}
	return n, nil
}

// Vector is one title's stored plot vector.
type Vector struct {
	MediaType string
	TMDBID    int
	Embedding []float32
}

// Vectors reads every stored plot vector of one model: the model most rows
// hold, so while a new model's run is under way the feed keeps the old one's
// vectors until the new one holds the most, and never compares across two.
func (r *Repository) Vectors(ctx context.Context) ([]Vector, error) {
	var rows []struct {
		MediaType string `db:"media_type"`
		TMDBID    int    `db:"tmdb_id"`
		Embedding string `db:"embedding"`
	}
	if err := r.db.SelectContext(ctx, &rows, `
		select media_type, tmdb_id, embedding::text as embedding
		  from wikipedia_plots
		 where embedding is not null
		   and model = (select model from wikipedia_plots where embedding is not null
		                 group by model order by count(*) desc, model limit 1)`); err != nil {
		return nil, fmt.Errorf("plot vectors: %w", err)
	}
	out := make([]Vector, 0, len(rows))
	for _, row := range rows {
		v, err := parseVector(row.Embedding)
		if err != nil {
			return nil, fmt.Errorf("plot vector of %s %d: %w", row.MediaType, row.TMDBID, err)
		}
		out = append(out, Vector{MediaType: row.MediaType, TMDBID: row.TMDBID, Embedding: v})
	}
	return out, nil
}

// parseVector reads pgvector's text form, [1,2,3].
func parseVector(s string) ([]float32, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		return nil, fmt.Errorf("not a vector: %.20q", s)
	}
	s = s[1 : len(s)-1]
	if s == "" {
		return []float32{}, nil
	}
	parts := strings.Split(s, ",")
	out := make([]float32, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil, fmt.Errorf("vector component %d: %w", i, err)
		}
		out[i] = float32(f)
	}
	return out, nil
}

// vectorLiteral writes v in pgvector's text form, [1,2,3].
func vectorLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
