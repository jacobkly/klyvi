package reco

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"klyvi-api/internal/platform/cache"
)

// errFeedComputeAborted is what callers waiting on a shared computation get
// when the computation panicked instead of returning.
var errFeedComputeAborted = errors.New("reco: feed computation aborted")

// FeedCache holds each user's ranked feed, maxFeedDepth deep, so later pages
// and the other screens that open on the feed reuse one computation instead
// of reloading the history, the seen set and the candidate pool. Each media
// filter of a user's feed is its own entry.
//
// An entry lasts until its TTL lapses or Invalidate reports a write that
// changes the user's signal. There is one API instance, so every such write
// passes through this process.
type FeedCache struct {
	ready *cache.TTL[[]Scored]

	mu      sync.Mutex
	flights map[string]*feedFlight // by feedKey, like ready
}

// feedFlight is one computation in progress. Concurrent misses for the same
// user wait on it instead of starting their own.
type feedFlight struct {
	done   chan struct{}
	ranked []Scored
	err    error
	stale  bool // a write landed mid-flight; guarded by FeedCache.mu
}

// NewFeedCache holds up to maxFeeds feeds, each for ttl. A user's films-only,
// series-only and mixed feeds count as three.
func NewFeedCache(ttl time.Duration, maxFeeds int) *FeedCache {
	return &FeedCache{
		ready:   cache.NewTTL[[]Scored](maxFeeds, ttl),
		flights: map[string]*feedFlight{},
	}
}

// feedKey names one media filter of a user's feed in the cache.
func feedKey(userID uuid.UUID, media FeedMedia) string {
	return userID.String() + "/" + string(media)
}

// ranked returns the user's cached ranking for the media, or computes it with
// compute. Concurrent misses for one feed share a single computation. A
// result that a write overtook still goes to the callers who asked before the
// write, but it is not cached. The returned slice is shared and must not be
// modified.
func (c *FeedCache) ranked(ctx context.Context, userID uuid.UUID, media FeedMedia, compute func(context.Context) ([]Scored, error)) ([]Scored, error) {
	key := feedKey(userID, media)

	c.mu.Lock()
	if r, ok := c.ready.Get(key); ok {
		c.mu.Unlock()
		return r, nil
	}
	if f, ok := c.flights[key]; ok {
		c.mu.Unlock()
		select {
		case <-f.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		// The flight runs on its first caller's context. If that request
		// went away mid-compute, this one is still live and starts over.
		if isContextErr(f.err) && ctx.Err() == nil {
			return c.ranked(ctx, userID, media, compute)
		}
		return f.ranked, f.err
	}
	f := &feedFlight{done: make(chan struct{})}
	c.flights[key] = f
	c.mu.Unlock()

	// Settle the flight even if compute panics, or every later request for
	// this user would wait on a flight that never lands.
	returned := false
	defer func() {
		c.mu.Lock()
		if c.flights[key] == f {
			delete(c.flights, key)
		}
		if returned && f.err == nil && !f.stale {
			c.ready.Set(key, f.ranked)
		}
		c.mu.Unlock()
		if !returned {
			f.err = errFeedComputeAborted
		}
		close(f.done)
	}()

	f.ranked, f.err = compute(ctx)
	returned = true
	return f.ranked, f.err
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// Invalidate drops every media filter of the user's cached feed after a write
// that changes their signal or seen set. A computation already running for
// them finishes for its own callers but is not cached, and later callers
// start a fresh one.
func (c *FeedCache) Invalidate(userID uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range feedMedia {
		key := feedKey(userID, m)
		c.ready.Remove(key)
		if f, ok := c.flights[key]; ok {
			f.stale = true
			delete(c.flights, key)
		}
	}
}
