package interactions

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

type countingRepo struct{ rows int }

func (r *countingRepo) Record(context.Context, *Interaction) error {
	r.rows++
	return nil
}

func (r *countingRepo) ListByUser(context.Context, uuid.UUID, int) ([]Interaction, error) {
	return nil, nil
}

func (r *countingRepo) CountByUser(context.Context, uuid.UUID) (int, error) { return r.rows, nil }

type fixedMedia struct{}

func (fixedMedia) EnsureMediaIndexAndGetID(context.Context, int, string, *int) (int, error) {
	return 1, nil
}

var impression = RecordRequest{TMDBID: 550, MediaType: MediaTypeMovie, Kind: KindImpression}

func TestDailyCap(t *testing.T) {
	noon := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	alice, bob := uuid.New(), uuid.New()

	t.Run("refuses past the limit", func(t *testing.T) {
		c := newDailyCap(2)
		for i := range 2 {
			if !c.take(alice, noon) {
				t.Fatalf("write %d refused, want allowed", i+1)
			}
		}
		if c.take(alice, noon) {
			t.Error("write past the limit allowed, want refused")
		}
	})

	t.Run("counts each user separately", func(t *testing.T) {
		c := newDailyCap(1)
		c.take(alice, noon)
		if !c.take(bob, noon) {
			t.Error("one user's writes should not use up another's")
		}
	})

	t.Run("starts over on the next UTC day", func(t *testing.T) {
		c := newDailyCap(1)
		c.take(alice, noon)
		if c.take(alice, noon.Add(11*time.Hour)) {
			t.Error("still the same UTC day, want refused")
		}
		if !c.take(alice, noon.Add(12*time.Hour)) {
			t.Error("a new UTC day should allow writes again")
		}
	})
}

func TestRecordFromClient_StopsAtTheDailyCap(t *testing.T) {
	repo := &countingRepo{}
	svc := NewService(repo, fixedMedia{})
	userID := uuid.New()
	ctx := context.Background()

	for i := range MaxClientWritesPerDay {
		if _, err := svc.RecordFromClient(ctx, userID, impression); err != nil {
			t.Fatalf("write %d: %v", i+1, err)
		}
	}
	if _, err := svc.RecordFromClient(ctx, userID, impression); !errors.Is(err, ErrDailyCapReached) {
		t.Fatalf("write past the cap: err = %v, want ErrDailyCapReached", err)
	}
	if repo.rows != MaxClientWritesPerDay {
		t.Errorf("rows = %d, want %d", repo.rows, MaxClientWritesPerDay)
	}
}

func TestRecordBatch_RejectsRowsPastTheDailyCap(t *testing.T) {
	repo := &countingRepo{}
	svc := NewService(repo, fixedMedia{})
	svc.cap = newDailyCap(2)

	res, err := svc.RecordBatch(context.Background(), uuid.New(), []RecordRequest{impression, impression, impression})
	if err != nil {
		t.Fatalf("RecordBatch: %v", err)
	}
	if res.Accepted != 2 || res.Rejected != 1 {
		t.Errorf("accepted %d, rejected %d, want 2 and 1", res.Accepted, res.Rejected)
	}
	if repo.rows != 2 {
		t.Errorf("rows = %d, want 2", repo.rows)
	}
}

// An import can carry thousands of ratings in one go. It writes through Record
// and must neither be cut short nor use up the user's budget for the day.
func TestRecord_ImportPathDoesNotTouchTheCap(t *testing.T) {
	svc := NewService(&countingRepo{}, fixedMedia{})
	svc.cap = newDailyCap(1)
	userID := uuid.New()
	ctx := context.Background()

	for i := range 3 {
		if _, err := svc.Record(ctx, userID, impression); err != nil {
			t.Fatalf("import write %d: %v", i+1, err)
		}
	}
	if _, err := svc.RecordFromClient(ctx, userID, impression); err != nil {
		t.Errorf("client write after an import: %v", err)
	}
}

func TestStatusForError_DailyCap(t *testing.T) {
	status, msg := statusForError(ErrDailyCapReached)
	if status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", status)
	}
	if msg == "" || msg == "internal server error" {
		t.Errorf("message = %q, want an explanation", msg)
	}
}
