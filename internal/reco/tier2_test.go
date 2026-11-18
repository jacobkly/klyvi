package reco

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// gatedProfiles holds every upsert until release is closed.
type gatedProfiles struct {
	release chan struct{}

	mu       sync.Mutex
	upserted []uuid.UUID
}

func (g *gatedProfiles) GetProfile(context.Context, uuid.UUID) (*TasteProfile, error) {
	return nil, nil
}

func (g *gatedProfiles) UpsertProfile(_ context.Context, p *TasteProfile) error {
	<-g.release
	g.mu.Lock()
	g.upserted = append(g.upserted, p.UserID)
	g.mu.Unlock()
	return nil
}

// Persisting the profile is a side effect the feed does not need, so it must
// not cost the request a round trip.
func TestTier2_PersistsProfileOffTheRequestPath(t *testing.T) {
	cfg := DefaultConfig()
	repo := &gatedProfiles{release: make(chan struct{})}
	t2 := NewTier2(cfg, repo)

	userID := uuid.New()
	user := &UserContext{
		UserID: userID,
		Liked: []SignalItem{{MediaID: 1, Weight: 1, Features: &MediaFeatures{
			MediaID: 1, GenreIDs: []int{18}, KeywordIDs: []int{825}, Year: 1999, VoteCount: 5000,
		}}},
	}
	cands := []Candidate{{MediaID: 2, Features: &MediaFeatures{
		MediaID: 2, GenreIDs: []int{18}, KeywordIDs: []int{825}, Year: 1999, VoteCount: 5000,
	}}}

	done := make(chan error, 1)
	go func() {
		_, err := t2.Score(context.Background(), user, cands)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Score: %v", err)
		}
	case <-time.After(2 * time.Second):
		close(repo.release)
		t.Fatal("Score blocked on the profile upsert")
	}

	close(repo.release)
	t2.Wait()

	if len(repo.upserted) != 1 || repo.upserted[0] != userID {
		t.Errorf("upserted = %v, want one upsert for %v", repo.upserted, userID)
	}
}

// panickingProfiles stands in for a bug in the profile write.
type panickingProfiles struct{}

func (panickingProfiles) GetProfile(context.Context, uuid.UUID) (*TasteProfile, error) {
	return nil, nil
}

func (panickingProfiles) UpsertProfile(context.Context, *TasteProfile) error {
	panic("upsert bug")
}

// A panic in the background save is logged and dropped. Unrecovered, it would
// end the API process. The save slot is still given back.
func TestTier2_ProfileSavePanicIsContained(t *testing.T) {
	t2 := NewTier2(DefaultConfig(), panickingProfiles{})
	for range maxConcurrentProfileSaves + 1 {
		t2.saveProfile(&TasteProfile{UserID: uuid.New()})
		t2.Wait()
	}
	if n := len(t2.saving); n != 0 {
		t.Errorf("save slots held after the panics = %d, want 0", n)
	}
}
