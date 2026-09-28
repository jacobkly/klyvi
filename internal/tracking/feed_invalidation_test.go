package tracking_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"klyvi-api/internal/tracking"
)

// memTracking answers every write as if the row exists, or as if it does
// not when missing is set, so each operation's no-op path can be tested.
type memTracking struct{ missing bool }

func (m *memTracking) EnsureMediaIndexAndGetID(_ context.Context, tmdbID int, _ string, _ *int) (int, error) {
	return tmdbID, nil
}

func (m *memTracking) AddEntry(context.Context, uuid.UUID, int, tracking.AddParams) (*tracking.Entry, error) {
	return &tracking.Entry{}, nil
}

func (m *memTracking) GetEntry(context.Context, uuid.UUID, int) (*tracking.Entry, error) {
	return nil, nil
}

func (m *memTracking) UpdateEntry(context.Context, uuid.UUID, int, tracking.UpdatePatch) (*tracking.Entry, error) {
	if m.missing {
		return nil, nil
	}
	return &tracking.Entry{}, nil
}

func (m *memTracking) DeleteEntry(context.Context, uuid.UUID, int) (bool, error) {
	return !m.missing, nil
}

func (m *memTracking) ListByUser(context.Context, uuid.UUID, tracking.ListFilters) ([]tracking.Entry, error) {
	return nil, nil
}

func (m *memTracking) ResetScoresByMediaType(context.Context, uuid.UUID, string) (int64, error) {
	if m.missing {
		return 0, nil
	}
	return 3, nil
}

func (m *memTracking) DeleteByMediaType(context.Context, uuid.UUID, string) (int64, error) {
	if m.missing {
		return 0, nil
	}
	return 3, nil
}

// invalidations records which users had their cached feed dropped.
type invalidations struct{ users []uuid.UUID }

func (i *invalidations) Invalidate(userID uuid.UUID) { i.users = append(i.users, userID) }

func TestTrackingWrites_DropTheCachedFeed(t *testing.T) {
	ctx := context.Background()
	score := 70
	cases := []struct {
		name    string
		missing bool
		run     func(*tracking.Service, uuid.UUID) error
		want    bool
	}{
		{"add", false, func(s *tracking.Service, u uuid.UUID) error {
			_, err := s.Add(ctx, u, tracking.AddRequest{TMDBID: 550, MediaType: tracking.MediaTypeMovie})
			return err
		}, true},
		{"update", false, func(s *tracking.Service, u uuid.UUID) error {
			_, err := s.Update(ctx, u, 1, tracking.UpdateRequest{Score: &score})
			return err
		}, true},
		{"update of an untracked item", true, func(s *tracking.Service, u uuid.UUID) error {
			_, err := s.Update(ctx, u, 1, tracking.UpdateRequest{Score: &score})
			return err
		}, false},
		{"delete", false, func(s *tracking.Service, u uuid.UUID) error {
			_, err := s.Delete(ctx, u, 1)
			return err
		}, true},
		{"delete of an untracked item", true, func(s *tracking.Service, u uuid.UUID) error {
			_, err := s.Delete(ctx, u, 1)
			return err
		}, false},
		{"reset scores", false, func(s *tracking.Service, u uuid.UUID) error {
			_, err := s.ResetScores(ctx, u, tracking.MediaTypeMovie)
			return err
		}, true},
		{"reset scores on an empty list", true, func(s *tracking.Service, u uuid.UUID) error {
			_, err := s.ResetScores(ctx, u, tracking.MediaTypeMovie)
			return err
		}, false},
		{"delete list", false, func(s *tracking.Service, u uuid.UUID) error {
			_, err := s.DeleteList(ctx, u, tracking.MediaTypeMovie)
			return err
		}, true},
		{"list", false, func(s *tracking.Service, u uuid.UUID) error {
			_, err := s.List(ctx, u, tracking.ListFilters{})
			return err
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inv := &invalidations{}
			svc := tracking.NewService(&memTracking{missing: tc.missing}).WithFeedInvalidator(inv)
			user := uuid.New()

			if err := tc.run(svc, user); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			got := len(inv.users) == 1 && inv.users[0] == user
			if got != tc.want {
				t.Errorf("feed dropped = %v (calls %v), want %v", got, inv.users, tc.want)
			}
		})
	}
}
