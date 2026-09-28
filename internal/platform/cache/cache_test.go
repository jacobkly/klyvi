package cache_test

import (
	"testing"
	"time"

	"klyvi-api/internal/platform/cache"
)

func TestTTL_SetGet(t *testing.T) {
	c := cache.NewTTL[int](8, time.Minute)

	if _, ok := c.Get("missing"); ok {
		t.Fatal("empty cache returned a hit")
	}

	c.Set("k", 42)
	got, ok := c.Get("k")
	if !ok || got != 42 {
		t.Fatalf("Get(k) = %d, %v; want 42, true", got, ok)
	}
}

func TestTTL_Remove(t *testing.T) {
	c := cache.NewTTL[int](8, time.Minute)
	c.Set("k", 1)
	c.Remove("k")
	if _, ok := c.Get("k"); ok {
		t.Error("value should be gone after Remove")
	}
	c.Remove("never-set") // must not panic
}

func TestTTL_EvictsLeastRecentlyUsedPastCapacity(t *testing.T) {
	c := cache.NewTTL[string](2, time.Minute)
	c.Set("a", "1")
	c.Set("b", "2")
	c.Set("c", "3") // exceeds capacity → "a" (LRU) is evicted

	if _, ok := c.Get("a"); ok {
		t.Error("expected 'a' to be evicted past capacity")
	}
	if _, ok := c.Get("b"); !ok {
		t.Error("expected 'b' to still be present")
	}
	if _, ok := c.Get("c"); !ok {
		t.Error("expected 'c' to still be present")
	}
}

func TestTTL_ExpiresAfterTTL(t *testing.T) {
	c := cache.NewTTL[int](8, 20*time.Millisecond)
	c.Set("k", 1)
	if _, ok := c.Get("k"); !ok {
		t.Fatal("value should be present immediately after Set")
	}

	time.Sleep(40 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Error("value should have expired after the TTL window")
	}
}
