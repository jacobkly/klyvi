package users_test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"klyvi-api/internal/interactions"
	"klyvi-api/internal/notifications"
	"klyvi-api/internal/tracking"
	"klyvi-api/internal/users"
)

// fakeExportSource stands in for the tracking + interactions services so the
// export shapes can be tested without a database.
type fakeExportSource struct {
	entries      []tracking.Entry
	interactions []interactions.Interaction
	notices      []notifications.Notification
}

func (f *fakeExportSource) ListAll(context.Context, uuid.UUID) ([]notifications.Notification, error) {
	return f.notices, nil
}

func (f *fakeExportSource) List(context.Context, uuid.UUID, tracking.ListFilters) ([]tracking.Entry, error) {
	return f.entries, nil
}

func (f *fakeExportSource) ListByUser(context.Context, uuid.UUID, int) ([]interactions.Interaction, error) {
	return f.interactions, nil
}

func exportFixture() *fakeExportSource {
	title := "Fight Club"
	status := "completed"
	score := 95
	return &fakeExportSource{
		entries: []tracking.Entry{
			{MediaID: 1, MediaType: "movie", Status: &status, Score: &score, Title: &title},
		},
		interactions: []interactions.Interaction{
			{MediaID: 1, MediaType: "movie", Kind: "rated", Rating: &score},
		},
		notices: []notifications.Notification{
			{ID: 1, Kind: notifications.KindFilmRelease, MediaType: "movie", Title: title},
		},
	}
}

func exportSvc(src *fakeExportSource) *users.Service {
	repo := &fakeUserRepo{user: &users.User{ID: uuid.New(), Username: "jacob"}}
	return users.NewService(repo).WithExportSources(src, src, src)
}

// The CSV export is a spreadsheet the user opens elsewhere, so its header row
// and quoting have to be stable and correct.
func TestExportCSV(t *testing.T) {
	svc := exportSvc(exportFixture())

	data, err := svc.ExportTrackingCSV(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("ExportTrackingCSV: %v", err)
	}

	rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (header + 1 entry)", len(rows))
	}

	header := rows[0]
	wantCols := map[string]bool{"title": true, "media_type": true, "status": true, "score": true}
	seen := map[string]bool{}
	for _, c := range header {
		seen[c] = true
	}
	for col := range wantCols {
		if !seen[col] {
			t.Errorf("header missing %q column (got %v)", col, header)
		}
	}

	// The one data row carries the enriched title, not just an id.
	if !contains(rows[1], "Fight Club") {
		t.Errorf("data row missing enriched title: %v", rows[1])
	}
}

func TestExportJSON(t *testing.T) {
	svc := exportSvc(exportFixture())

	data, err := svc.ExportTrackingJSON(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("ExportTrackingJSON: %v", err)
	}

	var out []map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("entries = %d, want 1", len(out))
	}
	if out[0]["title"] != "Fight Club" {
		t.Errorf("title = %v, want Fight Club", out[0]["title"])
	}
}

// The GDPR bundle is the whole account: profile, tracking, interactions, notices
// — enough for a user to see everything held about them.
func TestExportAccountData(t *testing.T) {
	src := exportFixture()
	repo := &fakeUserRepo{user: &users.User{ID: uuid.New(), Username: "jacob", NotifyReleases: true}}
	svc := users.NewService(repo).WithExportSources(src, src, src)

	data, err := svc.ExportAccountData(context.Background(), repo.user.ID)
	if err != nil {
		t.Fatalf("ExportAccountData: %v", err)
	}

	var bundle struct {
		User          json.RawMessage   `json:"user"`
		Tracking      []json.RawMessage `json:"tracking"`
		Interactions  []json.RawMessage `json:"interactions"`
		Notifications []json.RawMessage `json:"notifications"`
	}
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(bundle.User) == 0 {
		t.Error("bundle missing user")
	}
	if !strings.Contains(string(bundle.User), `"notify_releases": true`) {
		t.Errorf("bundle user missing its preferences: %s", bundle.User)
	}
	if len(bundle.Notifications) != 1 {
		t.Errorf("notifications = %d, want 1", len(bundle.Notifications))
	}
	if len(bundle.Tracking) != 1 {
		t.Errorf("tracking entries = %d, want 1", len(bundle.Tracking))
	}
	if len(bundle.Interactions) != 1 {
		t.Errorf("interactions = %d, want 1", len(bundle.Interactions))
	}
}

func contains(row []string, want string) bool {
	for _, v := range row {
		if v == want {
			return true
		}
	}
	return false
}
