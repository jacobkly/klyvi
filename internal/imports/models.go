package imports

import (
	"time"

	"github.com/google/uuid"
)

// Status values for an import job.
const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"
)

// Import mirrors a row in the imports table — one background job.
type Import struct {
	ID        uuid.UUID `db:"id" json:"id"`
	UserID    uuid.UUID `db:"user_id" json:"-"`
	Source    string    `db:"source" json:"source"`
	Status    string    `db:"status" json:"status"`
	Total     int       `db:"total" json:"total"`
	Matched   int       `db:"matched" json:"matched"`
	Unmatched int       `db:"unmatched" json:"unmatched"`
	Error     *string   `db:"error" json:"error"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// maxImportBytes caps the upload. Well above a lifetime Letterboxd export.
const maxImportBytes = 10 * 1024 * 1024

// staleAfter is how long a 'running' job may go without an update before a
// reader reports it failed. A job dies with the server (in-process worker),
// so this is how a client stops polling a job that will never finish.
const staleAfter = 10 * time.Minute
