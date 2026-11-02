package imports

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeRepo is an in-memory Repo that records what the worker writes, so a test
// can watch a job move from pending to running to done.
type fakeRepo struct {
	mu   sync.Mutex
	jobs map[uuid.UUID]*Import
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{jobs: map[uuid.UUID]*Import{}}
}

func (f *fakeRepo) Create(_ context.Context, userID uuid.UUID, source string) (*Import, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	imp := &Import{ID: uuid.New(), UserID: userID, Source: source, Status: StatusPending}
	f.jobs[imp.ID] = imp
	out := *imp
	return &out, nil
}

func (f *fakeRepo) Get(_ context.Context, _, id uuid.UUID) (*Import, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	imp, ok := f.jobs[id]
	if !ok {
		return nil, nil
	}
	out := *imp
	return &out, nil
}

func (f *fakeRepo) SetStatus(_ context.Context, id uuid.UUID, status string, errMsg *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs[id].Status = status
	f.jobs[id].Error = errMsg
	return nil
}

func (f *fakeRepo) SetProgress(_ context.Context, id uuid.UUID, total, matched, unmatched int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs[id].Total, f.jobs[id].Matched, f.jobs[id].Unmatched = total, matched, unmatched
	return nil
}

func (f *fakeRepo) SetUnmatchedTitles(context.Context, uuid.UUID, UnmatchedTitles) error {
	return nil
}

func (f *fakeRepo) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.jobs)
}

func (f *fakeRepo) status(id uuid.UUID) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jobs[id].Status
}

// noResultsTMDB answers every search with nothing, so each row is unmatched
// and the worker never reaches the tracking or signal writers.
type noResultsTMDB struct{}

func (noResultsTMDB) TMDBRequest(context.Context, string, string, interface{}) (map[string]interface{}, error) {
	return map[string]interface{}{"results": []any{}}, nil
}

// gatedTMDB holds every search until release is closed, and reports each call
// on entered as it arrives, so a test can keep workers busy and count them.
type gatedTMDB struct {
	entered chan struct{}
	release chan struct{}
}

func (g *gatedTMDB) TMDBRequest(context.Context, string, string, interface{}) (map[string]interface{}, error) {
	g.entered <- struct{}{}
	<-g.release
	return map[string]interface{}{"results": []any{}}, nil
}

func letterboxdFile(rows int) []byte {
	var b strings.Builder
	b.WriteString("Date,Name,Year,Rating\n")
	for i := range rows {
		fmt.Fprintf(&b, "2024-01-02,Film %d,,4\n", i)
	}
	return []byte(b.String())
}

func waitForStatus(t *testing.T, repo *fakeRepo, id uuid.UUID, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if repo.status(id) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s status = %q, want %q", id, repo.status(id), want)
}

func TestStart_RowCap(t *testing.T) {
	tests := []struct {
		name    string
		rows    int
		wantErr error
	}{
		{name: "a file at the cap is accepted", rows: maxImportRows},
		{name: "one row over the cap is refused", rows: maxImportRows + 1, wantErr: ErrTooManyRows},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			svc := NewService(repo, noResultsTMDB{}, nil, nil, nil)

			imp, err := svc.Start(context.Background(), uuid.New(), string(SourceLetterboxd), letterboxdFile(tt.rows))

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Start error = %v, want %v", err, tt.wantErr)
				}
				// Refused up front: no job row, no worker.
				if n := repo.count(); n != 0 {
					t.Errorf("created %d jobs, want none", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			waitForStatus(t, repo, imp.ID, StatusDone)
		})
	}
}

// A burst of uploads queues behind the worker pool instead of fanning out:
// with every worker busy, the next job stays pending until one frees up.
func TestStart_ExtraJobWaitsForAWorker(t *testing.T) {
	repo := newFakeRepo()
	gate := &gatedTMDB{entered: make(chan struct{}), release: make(chan struct{})}
	svc := NewService(repo, gate, nil, nil, nil)
	ctx := context.Background()

	var ids []uuid.UUID
	for range importWorkers + 1 {
		imp, err := svc.Start(ctx, uuid.New(), string(SourceLetterboxd), letterboxdFile(1))
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		ids = append(ids, imp.ID)
	}

	for range importWorkers {
		select {
		case <-gate.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("the pool did not start its workers")
		}
	}

	// Every worker is now held. The extra job must not reach TMDB.
	select {
	case <-gate.entered:
		t.Fatalf("job %d started with every worker busy", importWorkers+1)
	case <-time.After(150 * time.Millisecond):
	}
	var pending int
	for _, id := range ids {
		if repo.status(id) == StatusPending {
			pending++
		}
	}
	if pending != 1 {
		t.Errorf("pending jobs = %d, want 1 waiting for a worker", pending)
	}

	// Free the workers: the queued job gets one and finishes.
	close(gate.release)
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued job never got a worker")
	}
	for _, id := range ids {
		waitForStatus(t, repo, id, StatusDone)
	}
}
