package imports

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// UnmatchedTitle is one import row that could not be resolved to a catalog
// film — surfaced so the user can find and rate it manually.
type UnmatchedTitle struct {
	Title string `json:"title"`
	Year  int    `json:"year"`
}

// UnmatchedTitles is the JSONB-backed list stored on imports.unmatched_titles.
// It implements sql.Scanner and driver.Valuer so it round-trips through the
// column; an empty list stores as "[]" so the API returns [] not null.
type UnmatchedTitles []UnmatchedTitle

func (u *UnmatchedTitles) Scan(src any) error {
	if src == nil {
		*u = nil
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("unmatched_titles: unexpected scan type %T", src)
	}
	if len(b) == 0 {
		*u = nil
		return nil
	}
	return json.Unmarshal(b, u)
}

func (u UnmatchedTitles) Value() (driver.Value, error) {
	if len(u) == 0 {
		return "[]", nil
	}
	return json.Marshal(u)
}

// Status values for an import job.
const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"
)

// Import mirrors a row in the imports table — one background job.
type Import struct {
	ID        uuid.UUID       `db:"id" json:"id"`
	UserID    uuid.UUID       `db:"user_id" json:"-"`
	Source    string          `db:"source" json:"source"`
	Status    string          `db:"status" json:"status"`
	Total     int             `db:"total" json:"total"`
	Matched   int             `db:"matched" json:"matched"`
	Unmatched int             `db:"unmatched" json:"unmatched"`
	Titles    UnmatchedTitles `db:"unmatched_titles" json:"unmatched_titles"`
	Error     *string         `db:"error" json:"error"`
	CreatedAt time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt time.Time       `db:"updated_at" json:"updated_at"`
}

// maxImportBytes caps the upload. Well above a lifetime Letterboxd export.
const maxImportBytes = 10 * 1024 * 1024

// staleAfter is how long a 'running' job may go without an update before a
// reader reports it failed. A job dies with the server (in-process worker),
// so this is how a client stops polling a job that will never finish.
const staleAfter = 10 * time.Minute
