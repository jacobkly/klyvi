package interactions

import (
	"time"

	"github.com/google/uuid"
)

// Kind values per the interactions table CHECK constraint. Each maps to a
// base weight constant in internal/reco/signal.go.
const (
	KindLogged     = "logged"
	KindRated      = "rated"
	KindDismissed  = "dismissed"
	KindSaved      = "saved"
	KindImpression = "impression"
	KindClicked    = "clicked"
)

// Source values per the interactions table CHECK constraint.
const (
	SourceSearch     = "search"
	SourceDetail     = "detail"
	SourceFeed       = "feed"
	SourceOnboarding = "onboarding"
)

// MediaType values (mirror of the tracking and media_index conventions).
const (
	MediaTypeMovie  = "movie"
	MediaTypeSeason = "season"
)

// Interaction is a single signal event from a user — a row in the
// `interactions` table. The `weight` derived value (per ARCHITECTURE §5.2)
// is computed at read time in the reco signal pipeline, not stored.
type Interaction struct {
	ID        int       `db:"id" json:"id"`
	UserID    uuid.UUID `db:"user_id" json:"user_id"`
	MediaID   int       `db:"media_id" json:"media_id"`
	MediaType string    `db:"media_type" json:"media_type"`
	Kind      string    `db:"kind" json:"kind"`
	Rating    *int      `db:"rating" json:"rating"`
	Source    *string   `db:"source" json:"source"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}
