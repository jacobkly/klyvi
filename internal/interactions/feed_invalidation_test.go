package interactions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"klyvi-api/internal/interactions"
)

// memInteractions accepts every write, or fails every write when fail is set.
type memInteractions struct{ fail bool }

func (m *memInteractions) Record(context.Context, *interactions.Interaction) error {
	if m.fail {
		return errors.New("db down")
	}
	return nil
}

func (m *memInteractions) ListByUser(context.Context, uuid.UUID, int) ([]interactions.Interaction, error) {
	return nil, nil
}

func (m *memInteractions) CountByUser(context.Context, uuid.UUID) (int, error) { return 0, nil }

type tmdbIDAsMediaID struct{}

func (tmdbIDAsMediaID) EnsureMediaIndexAndGetID(_ context.Context, tmdbID int, _ string, _ *int) (int, error) {
	return tmdbID, nil
}

// invalidations records which users had their cached feed dropped.
type invalidations struct{ users []uuid.UUID }

func (i *invalidations) Invalidate(userID uuid.UUID) { i.users = append(i.users, userID) }

func TestRecord_DropsTheCachedFeedOnlyForDeliberateSignals(t *testing.T) {
	cases := []struct {
		kind   string
		rating *int
		want   bool
	}{
		{interactions.KindRated, intPtr(80), true},
		{interactions.KindLogged, nil, true},
		{interactions.KindSaved, nil, true},
		{interactions.KindDismissed, nil, true},
		// Passive signals fire while the user browses the feed itself.
		{interactions.KindImpression, nil, false},
		{interactions.KindClicked, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			inv := &invalidations{}
			svc := interactions.NewService(&memInteractions{}, tmdbIDAsMediaID{}).WithFeedInvalidator(inv)
			user := uuid.New()

			if _, err := svc.Record(context.Background(), user, interactions.RecordRequest{
				TMDBID: 550, MediaType: "movie", Kind: tc.kind, Rating: tc.rating,
			}); err != nil {
				t.Fatalf("Record: %v", err)
			}

			got := len(inv.users) == 1 && inv.users[0] == user
			if got != tc.want {
				t.Errorf("feed dropped = %v (calls %v), want %v", got, inv.users, tc.want)
			}
		})
	}
}

func TestRecord_FailedWriteKeepsTheCachedFeed(t *testing.T) {
	inv := &invalidations{}
	svc := interactions.NewService(&memInteractions{fail: true}, tmdbIDAsMediaID{}).WithFeedInvalidator(inv)

	if _, err := svc.Record(context.Background(), uuid.New(), interactions.RecordRequest{
		TMDBID: 550, MediaType: "movie", Kind: interactions.KindRated, Rating: intPtr(80),
	}); err == nil {
		t.Fatal("Record succeeded against a failing repository")
	}
	if len(inv.users) != 0 {
		t.Errorf("a failed write dropped the cached feed")
	}
}
