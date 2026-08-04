// Package cache is a small in-process TTL+LRU cache over hashicorp/golang-lru.
// Every cache is bounded by both a max entry count and a TTL, so memory is
// capped and entries never outlive their freshness window.
package cache

import (
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

// TTL is a goroutine-safe, size- and time-bounded cache. Values are treated as
// read-only once stored; callers must not mutate what Get returns, since the
// same value is handed to every subsequent hit.
type TTL[V any] struct {
	lru *expirable.LRU[string, V]
}

// NewTTL builds a cache holding at most maxEntries, each expiring after ttl.
func NewTTL[V any](maxEntries int, ttl time.Duration) *TTL[V] {
	return &TTL[V]{lru: expirable.NewLRU[string, V](maxEntries, nil, ttl)}
}

func (c *TTL[V]) Get(key string) (V, bool) { return c.lru.Get(key) }

func (c *TTL[V]) Set(key string, v V) { c.lru.Add(key, v) }
