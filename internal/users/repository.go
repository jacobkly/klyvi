package users

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
)

// settingsArg converts the optional settings blob to a driver-friendly
// value: nil pointer → SQL NULL (COALESCE keeps the stored blob), non-nil →
// the raw bytes for the JSONB write.
func settingsArg(s *json.RawMessage) any {
	if s == nil {
		return nil
	}
	return []byte(*s)
}

// uniqueViolation is the SQLSTATE Postgres raises when an insert or update
// collides with a unique index.
const uniqueViolation = "23505"

// usernameUniqueIndex is the index created in migration 00022. Matching on
// its name keeps an unrelated future unique violation from being reported to
// the user as a username collision.
const usernameUniqueIndex = "users_username_lower_key"

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// EnsureUser inserts a row for the given Supabase user UUID if one does not
// already exist. Username gets a deterministic sentinel ("user_<8>") because
// the column is NOT NULL UNIQUE; the user can rename later. ON CONFLICT
// (id) DO NOTHING makes this safe to call on every authenticated request.
func (r *Repository) EnsureUser(ctx context.Context, id uuid.UUID) error {
	username := defaultUsername(id)
	_, err := r.db.ExecContext(ctx, `
		insert into users (id, username)
		values ($1, $2)
		on conflict (id) do nothing
	`, id, username)
	return err
}

func (r *Repository) GetUserByID(ctx context.Context, id uuid.UUID) (*User, error) {
	var u User
	err := r.db.GetContext(ctx, &u,
		`select * from users where id = $1`, id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &u, err
}

// statsScanRow is the raw scan shape for StatsRowsByUser; JSONB and array
// columns land as bytes/strings here and are decoded into StatsRow.
type statsScanRow struct {
	MediaType             string  `db:"media_type"`
	Status                *string `db:"status"`
	Score                 *int    `db:"score"`
	EpisodeProgress       *int    `db:"episode_progress"`
	TotalRewatches        int     `db:"total_rewatches"`
	RuntimeMinutes        *int    `db:"runtime_minutes"`
	EpisodeRuntimeMinutes *int    `db:"episode_runtime_minutes"`
	EpisodeCount          *int    `db:"episode_count"`
	ReleaseYear           *int    `db:"release_year"`
	GenresRaw             []byte  `db:"genres_raw"`
	CountriesRaw          []byte  `db:"countries_raw"`
}

// StatsRowsByUser projects the user's tracking entries with the catalog
// facts the stats aggregation needs. This is a read-model over media_list +
// catalog tables owned here because profile stats are a users-domain
// concept; the write paths for those tables stay in their own packages.
func (r *Repository) StatsRowsByUser(ctx context.Context, id uuid.UUID) ([]StatsRow, error) {
	var raw []statsScanRow
	err := r.db.SelectContext(ctx, &raw, `
		select
			ml.media_type, ml.status, ml.score, ml.episode_progress, ml.total_rewatches,
			m.runtime as runtime_minutes,
			(
				select round(avg((e->>'runtime')::numeric))::int
				from jsonb_array_elements(tvse.episodes) e
				where jsonb_typeof(e->'runtime') = 'number'
			) as episode_runtime_minutes,
			case when tvse.episodes is not null then jsonb_array_length(tvse.episodes) end as episode_count,
			coalesce(
				extract(year from m.release_date)::int,
				extract(year from tvse.air_date)::int,
				extract(year from tvs.first_air_date)::int
			) as release_year,
			coalesce(m.genres, tvs.genres) as genres_raw,
			coalesce(m.production_countries, tvs.production_countries) as countries_raw
		from media_list ml
		left join media_index mi on mi.media_id = ml.media_id
		left join movies m on ml.media_type = 'movie' and m.movie_id = mi.id
		left join tv_series tvs on ml.media_type = 'season' and tvs.tv_id = mi.id
		left join tv_seasons tvse
			on ml.media_type = 'season'
			and tvse.tv_id = mi.id
			and tvse.season_number = mi.season_number
		where ml.user_id = $1
	`, id)
	if err != nil {
		return nil, fmt.Errorf("stats rows: %w", err)
	}

	rows := make([]StatsRow, 0, len(raw))
	for _, sr := range raw {
		rows = append(rows, StatsRow{
			MediaType:             sr.MediaType,
			Status:                sr.Status,
			Score:                 sr.Score,
			EpisodeProgress:       sr.EpisodeProgress,
			TotalRewatches:        sr.TotalRewatches,
			RuntimeMinutes:        sr.RuntimeMinutes,
			EpisodeRuntimeMinutes: sr.EpisodeRuntimeMinutes,
			EpisodeCount:          sr.EpisodeCount,
			ReleaseYear:           sr.ReleaseYear,
			Genres:                decodeGenreNames(sr.GenresRaw),
			Countries:             decodeCountryLabels(sr.CountriesRaw),
		})
	}
	return rows, nil
}

// DeleteAccountData removes all app-side rows for a user in one transaction:
// tracking, interactions, taste profile, and the users row itself. Account
// deletion is inherently cross-table, so this one method reaches beyond the
// users table by design — the alternative, orchestrating four packages'
// repos without a shared transaction, could half-delete on a failure.
func (r *Repository) DeleteAccountData(ctx context.Context, id uuid.UUID) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// media_list, interactions, taste_profiles reference users(id) ON DELETE
	// CASCADE, so deleting the users row would suffice — but deleting them
	// explicitly first keeps this correct even if a future table drops the
	// cascade, and reads clearly.
	stmts := []string{
		`delete from media_list where user_id = $1`,
		`delete from interactions where user_id = $1`,
		`delete from taste_profiles where user_id = $1`,
		`delete from users where id = $1`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return fmt.Errorf("delete account data: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete tx: %w", err)
	}
	return nil
}

// StatsEventsByUser returns the deliberate-action timestamps feeding the
// activity heatmap and watch years. Impressions are excluded — passive
// scroll-bys are not activity.
func (r *Repository) StatsEventsByUser(ctx context.Context, id uuid.UUID) ([]StatsEvent, error) {
	var events []StatsEvent
	err := r.db.SelectContext(ctx, &events, `
		select created_at from interactions
		where user_id = $1 and kind in ('logged', 'rated', 'saved')
		order by created_at
	`, id)
	if err != nil {
		return nil, fmt.Errorf("stats events: %w", err)
	}
	return events, nil
}

// decodeGenreNames extracts names from the catalog genres JSONB shape
// ([{id, name}, …]). Unparseable input degrades to no genres rather than
// failing the whole stats call.
func decodeGenreNames(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var gs []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &gs); err != nil {
		return nil
	}
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		if g.Name != "" {
			out = append(out, g.Name)
		}
	}
	return out
}

// decodeCountryLabels reads production_countries, which movies and series
// both carry as [{iso_3166_1, name}]. A plain ISO-code array still decodes.
func decodeCountryLabels(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var objs []struct {
		ISO  string `json:"iso_3166_1"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &objs); err == nil && len(objs) > 0 && (objs[0].ISO != "" || objs[0].Name != "") {
		out := make([]string, 0, len(objs))
		for _, o := range objs {
			if o.ISO != "" {
				out = append(out, o.ISO)
			}
		}
		return out
	}
	var codes []string
	if err := json.Unmarshal(raw, &codes); err == nil {
		return codes
	}
	return nil
}

// ClearAvatarURL nulls avatar_url — the COALESCE patch path cannot express
// "set to NULL", so the avatar delete flow gets its own statement.
func (r *Repository) ClearAvatarURL(ctx context.Context, id uuid.UUID) (*User, error) {
	var u User
	err := r.db.QueryRowxContext(ctx, `
		update users set avatar_url = null, updated_at = now()
		where id = $1
		returning *
	`, id).StructScan(&u)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("clear avatar url: %w", err)
	}
	return &u, nil
}

// UsernameExists reports whether a username is already held, compared
// case-insensitively so it agrees with the users_username_lower_key index.
func (r *Repository) UsernameExists(ctx context.Context, name string) (bool, error) {
	var exists bool
	err := r.db.QueryRowxContext(ctx, `
		select exists(select 1 from users where lower(username) = lower($1))
	`, name).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("username exists check: %w", err)
	}
	return exists, nil
}

// UpdateProfilePatch is the partial-update shape. nil fields are unchanged.
type UpdateProfilePatch struct {
	Username  *string
	Bio       *string
	AvatarURL *string
	BannerURL *string
	// Settings replaces the whole JSONB blob when non-nil.
	Settings *json.RawMessage
	// Birthday is tri-state — see OptionalDate.
	Birthday OptionalDate
}

// UpdateProfile applies a partial patch to the user row using COALESCE — only
// non-nil fields in the patch overwrite. Returns the updated row, or nil if
// no row matched the id.
//
// username_changed_at is stamped in the same statement that writes the name,
// so the two can never drift. The lower() comparison reads the pre-update
// value, which means re-saving the same name (or only its capitalisation)
// does not count as a change and does not start a cooldown.
//
// A concurrent update that loses the race on users_username_lower_key comes
// back as ErrUsernameTaken rather than a driver error, so the caller does not
// have to know what SQLSTATE 23505 is.
func (r *Repository) UpdateProfile(ctx context.Context, id uuid.UUID, patch UpdateProfilePatch) (*User, error) {
	var u User
	err := r.db.QueryRowxContext(ctx, `
		update users set
			username            = coalesce($2, username),
			username_changed_at = case
				when $2 is not null and lower($2) <> lower(username) then now()
				else username_changed_at
			end,
			bio        = coalesce($3, bio),
			avatar_url = coalesce($4, avatar_url),
			banner_url = coalesce($5, banner_url),
			settings   = coalesce($6, settings),
			birthday   = case when $7 then $8 else birthday end,
			updated_at = now()
		where id = $1
		returning *
	`, id, patch.Username, patch.Bio, patch.AvatarURL, patch.BannerURL, settingsArg(patch.Settings),
		patch.Birthday.Set, patch.Birthday.Value).StructScan(&u)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		if isUsernameTaken(err) {
			return nil, ErrUsernameTaken
		}
		return nil, fmt.Errorf("update profile: %w", err)
	}
	return &u, nil
}

// isUsernameTaken reports whether err is a unique violation on the username
// index specifically.
func isUsernameTaken(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == uniqueViolation && pgErr.ConstraintName == usernameUniqueIndex
}

func defaultUsername(id uuid.UUID) string {
	s := id.String()
	return fmt.Sprintf("user_%s", s[:8])
}
