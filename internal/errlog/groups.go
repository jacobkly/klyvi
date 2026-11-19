package errlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// ErrNotFound is returned when no error group has the given id.
var ErrNotFound = errors.New("error group not found")

// Status picks which groups a list shows.
type Status string

const (
	StatusOpen     Status = "open"
	StatusResolved Status = "resolved"
	StatusAll      Status = "all"
)

// Group is one stored error group, as the admin page shows it.
type Group struct {
	ID         int64      `db:"id" json:"id"`
	Source     string     `db:"source" json:"source"`
	Kind       string     `db:"kind" json:"kind"`
	Message    string     `db:"message" json:"message"`
	Stack      *string    `db:"stack" json:"stack"`
	Route      *string    `db:"route" json:"route"`
	Method     *string    `db:"method" json:"method"`
	Status     *int       `db:"status" json:"status"`
	UserID     *uuid.UUID `db:"user_id" json:"user_id"`
	Count      int        `db:"count" json:"count"`
	FirstSeen  time.Time  `db:"first_seen" json:"first_seen"`
	LastSeen   time.Time  `db:"last_seen" json:"last_seen"`
	ResolvedAt *time.Time `db:"resolved_at" json:"resolved_at"`
}

// Counts is how many groups are open and resolved.
type Counts struct {
	Open     int `db:"open" json:"open"`
	Resolved int `db:"resolved" json:"resolved"`
}

const groupColumns = `id, source, kind, message, stack, route, method, status, user_id,
	count, first_seen, last_seen, resolved_at`

// List returns one page of groups with the given status, most recently seen
// first.
func (s *Store) List(ctx context.Context, status Status, limit, offset int) ([]Group, error) {
	groups := []Group{}
	err := sqlx.SelectContext(ctx, s.db, &groups, `
		SELECT `+groupColumns+`
		FROM error_events
		WHERE $1::text = 'all' OR (($1::text = 'open') = (resolved_at IS NULL))
		ORDER BY last_seen DESC, id DESC
		LIMIT $2 OFFSET $3`, string(status), limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list error groups: %w", err)
	}
	return groups, nil
}

// Counts reports how many groups are open and how many resolved.
func (s *Store) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	err := sqlx.GetContext(ctx, s.db, &c, `
		SELECT count(*) FILTER (WHERE resolved_at IS NULL) AS open,
		       count(*) FILTER (WHERE resolved_at IS NOT NULL) AS resolved
		FROM error_events`)
	if err != nil {
		return Counts{}, fmt.Errorf("count error groups: %w", err)
	}
	return c, nil
}

// SetResolved marks a group resolved, keeping the first resolve time, or
// reopens it. A resolved group also reopens on its own when it recurs.
func (s *Store) SetResolved(ctx context.Context, id int64, resolved bool) (Group, error) {
	var g Group
	err := sqlx.GetContext(ctx, s.db, &g, `
		UPDATE error_events
		SET resolved_at = CASE WHEN $2 THEN coalesce(resolved_at, now()) END
		WHERE id = $1
		RETURNING `+groupColumns, id, resolved)
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, ErrNotFound
	}
	if err != nil {
		return Group{}, fmt.Errorf("set error group %d resolved=%v: %w", id, resolved, err)
	}
	return g, nil
}
