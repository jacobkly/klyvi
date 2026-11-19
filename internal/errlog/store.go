package errlog

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// DefaultMaxWebGroups caps the distinct web error groups kept. The report
// endpoint is open to anyone, so without a cap it could fill the table. A
// known group keeps counting past the cap, and the API's own errors are
// never capped, so web noise can't hide a server failure.
const DefaultMaxWebGroups = 1000

// Store writes events to error_events.
type Store struct {
	db           sqlx.ExtContext
	maxWebGroups int
}

// NewStore returns a store over db (a pool or a transaction).
func NewStore(db sqlx.ExtContext) *Store {
	return &Store{db: db, maxWebGroups: DefaultMaxWebGroups}
}

// Record adds e to its group, or starts the group. A resolved group that
// recurs is reopened. It reports false when the web cap dropped the event.
func (s *Store) Record(ctx context.Context, e Event) (bool, error) {
	e = e.clean()
	var stack, route, method *string
	if e.Stack != "" {
		stack = &e.Stack
	}
	if e.Route != "" {
		route = &e.Route
	}
	if e.Method != "" {
		method = &e.Method
	}
	var status *int
	if e.Status != 0 {
		status = &e.Status
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO error_events (fingerprint, source, kind, message, stack, route, method, status, user_id)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9
		WHERE $2::text <> 'web'
		   OR EXISTS (SELECT 1 FROM error_events WHERE fingerprint = $1)
		   OR (SELECT count(*) FROM error_events WHERE source = 'web') < $10
		ON CONFLICT (fingerprint) DO UPDATE SET
			count = error_events.count + 1,
			last_seen = now(),
			message = EXCLUDED.message,
			stack = coalesce(EXCLUDED.stack, error_events.stack),
			status = coalesce(EXCLUDED.status, error_events.status),
			user_id = coalesce(EXCLUDED.user_id, error_events.user_id),
			resolved_at = NULL`,
		e.Fingerprint(), e.Source, e.Kind, e.Message, stack, route, method, status, e.UserID, s.maxWebGroups)
	if err != nil {
		return false, fmt.Errorf("record error event: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("record error event: %w", err)
	}
	return n > 0, nil
}

// Prune deletes groups not seen within olderThan, and reports how many.
func (s *Store) Prune(ctx context.Context, olderThan time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM error_events WHERE last_seen < now() - make_interval(secs => $1)`,
		olderThan.Seconds())
	if err != nil {
		return 0, fmt.Errorf("prune error events: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune error events: %w", err)
	}
	return n, nil
}
