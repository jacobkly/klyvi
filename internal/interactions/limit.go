package interactions

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MaxClientWritesPerDay caps the interactions one user can send in a UTC day.
// A busy day of browsing is a few hundred rows, so a real user never gets near
// it. Without a cap, an account posting batches at the rate limit could write
// millions of rows a day and fill the database.
const MaxClientWritesPerDay = 5000

// ErrDailyCapReached is returned once a user has used up today's budget.
var ErrDailyCapReached = errors.New("daily interaction limit reached")

// dailyCap counts client writes per user for the current UTC day. It is
// in-process like the rate limiter, since a single API instance is the current
// deployment. A restart resets the counts, which only grants some extra rows. It
// counts writes instead of querying the table so that imports, which land
// thousands of rows through the same table, don't use up the user's budget.
type dailyCap struct {
	mu     sync.Mutex
	limit  int
	day    time.Time
	counts map[uuid.UUID]int
}

func newDailyCap(limit int) *dailyCap {
	return &dailyCap{limit: limit, counts: make(map[uuid.UUID]int)}
}

// take reports whether the user can write once more today, and counts it if so.
// The map is cleared each day, so it holds at most one day's active users.
func (c *dailyCap) take(userID uuid.UUID, now time.Time) bool {
	day := now.UTC().Truncate(24 * time.Hour)

	c.mu.Lock()
	defer c.mu.Unlock()
	if !day.Equal(c.day) {
		c.day = day
		clear(c.counts)
	}
	if c.counts[userID] >= c.limit {
		return false
	}
	c.counts[userID]++
	return true
}
