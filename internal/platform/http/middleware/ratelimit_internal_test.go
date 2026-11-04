package middleware

import (
	"fmt"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestVisitorTable_NeverGrowsPastItsCap(t *testing.T) {
	table := newVisitorTable(rate.Limit(1), 1, 100)
	now := time.Now()

	for i := range 1000 {
		if table.limiterFor(fmt.Sprintf("client-%d", i), now) == nil {
			t.Fatalf("client %d got no limiter", i)
		}
		if got := len(table.visitors); got > 100 {
			t.Fatalf("after client %d the table holds %d entries, want at most 100", i, got)
		}
	}
}

// A full table must not fail closed: a new client still gets a working bucket.
func TestVisitorTable_NewClientIsServedWhenFull(t *testing.T) {
	table := newVisitorTable(rate.Limit(1), 1, 2)
	now := time.Now()

	table.limiterFor("a", now)
	table.limiterFor("b", now)
	if !table.limiterFor("c", now).Allow() {
		t.Error("a new client on a full table should get a fresh bucket")
	}
}

func TestVisitorTable_SweepsExpiredEntries(t *testing.T) {
	table := newVisitorTable(rate.Limit(1), 1, 100)
	start := time.Now()

	table.limiterFor("idle", start)
	table.limiterFor("active", start.Add(visitorTTL))
	table.limiterFor("active", start.Add(visitorTTL+sweepInterval+time.Second))

	if _, ok := table.visitors["idle"]; ok {
		t.Error("an idle entry past its TTL should have been swept")
	}
	if _, ok := table.visitors["active"]; !ok {
		t.Error("an entry still in use should stay")
	}
}

func TestBucketKey(t *testing.T) {
	cases := []struct {
		name string
		ip   string
		want string
	}{
		{"ipv4 stays whole", "203.0.113.10", "203.0.113.10"},
		{"ipv4-mapped becomes ipv4", "::ffff:203.0.113.10", "203.0.113.10"},
		{"ipv6 keeps its /64", "2001:db8:1:2:aaaa:bbbb:cccc:dddd", "2001:db8:1:2::/64"},
		{"zone is dropped", "fe80::1%eth0", "fe80::/64"},
		{"unparseable passes through", "not-an-ip", "not-an-ip"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bucketKey(tc.ip); got != tc.want {
				t.Errorf("bucketKey(%q) = %q, want %q", tc.ip, got, tc.want)
			}
		})
	}
}
