package timing

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestRecorderAccumulates(t *testing.T) {
	ctx, rec := NewContext(context.Background())
	if FromContext(ctx) != rec {
		t.Fatal("FromContext did not return the recorder stored by NewContext")
	}

	rec.AddDB(10 * time.Millisecond)
	rec.AddDB(5 * time.Millisecond)
	rec.AddTMDB(40 * time.Millisecond)

	got := rec.Snapshot()
	want := Snapshot{DBQueries: 2, DB: 15 * time.Millisecond, TMDBCalls: 1, TMDB: 40 * time.Millisecond}
	if got != want {
		t.Fatalf("Snapshot() = %+v, want %+v", got, want)
	}
}

func TestFromContextWithoutRecorderIsNil(t *testing.T) {
	if rec := FromContext(context.Background()); rec != nil {
		t.Fatalf("FromContext(background) = %v, want nil", rec)
	}
}

func TestNilRecorderIsSafe(t *testing.T) {
	var rec *Recorder
	rec.AddDB(time.Millisecond)
	rec.AddTMDB(time.Millisecond)
	if got := rec.Snapshot(); got != (Snapshot{}) {
		t.Fatalf("nil Snapshot() = %+v, want zero", got)
	}
}

func TestRecorderIsSafeForConcurrentUse(t *testing.T) {
	_, rec := NewContext(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec.AddDB(time.Millisecond)
		}()
	}
	wg.Wait()
	if got := rec.Snapshot().DBQueries; got != 50 {
		t.Fatalf("DBQueries = %d, want 50", got)
	}
}

func TestServerTimingHeader(t *testing.T) {
	tests := []struct {
		name  string
		snap  Snapshot
		total time.Duration
		want  string
	}{
		{
			name:  "no db or tmdb work",
			snap:  Snapshot{},
			total: 2 * time.Millisecond,
			want:  `total;dur=2.0`,
		},
		{
			name:  "db only",
			snap:  Snapshot{DBQueries: 3, DB: 45200 * time.Microsecond},
			total: 50 * time.Millisecond,
			want:  `db;desc="3 queries";dur=45.2, total;dur=50.0`,
		},
		{
			name:  "db and tmdb",
			snap:  Snapshot{DBQueries: 1, DB: 8 * time.Millisecond, TMDBCalls: 2, TMDB: 120 * time.Millisecond},
			total: 131 * time.Millisecond,
			want:  `db;desc="1 query";dur=8.0, tmdb;desc="2 calls";dur=120.0, total;dur=131.0`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.snap.ServerTiming(tt.total); got != tt.want {
				t.Fatalf("ServerTiming() = %q, want %q", got, tt.want)
			}
		})
	}
}
