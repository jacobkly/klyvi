package users

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"klyvi-api/internal/interactions"
	"klyvi-api/internal/notifications"
	"klyvi-api/internal/tracking"
)

// Export composes data owned by other domains (tracking, interactions,
// notifications) into user-facing downloads. The readers are consumer interfaces
// so this package depends on behaviour, not on those services' construction.
// Import direction is one-way — none of them imports users.

// TrackingReader is the enriched tracking list the export needs.
type TrackingReader interface {
	List(ctx context.Context, userID uuid.UUID, filters tracking.ListFilters) ([]tracking.Entry, error)
}

// InteractionReader is the interaction history the export needs. sinceDays<=0
// means "all history" in the interactions service.
type InteractionReader interface {
	ListByUser(ctx context.Context, userID uuid.UUID, sinceDays int) ([]interactions.Interaction, error)
}

// NoticeReader is the notice history the account export needs.
type NoticeReader interface {
	ListAll(ctx context.Context, userID uuid.UUID) ([]notifications.Notification, error)
}

// WithExportSources wires the data sources the export endpoints read from.
func (s *Service) WithExportSources(tr TrackingReader, ir InteractionReader, nr NoticeReader) *Service {
	s.tracking = tr
	s.interactions = ir
	s.notices = nr
	return s
}

// ExportTrackingCSV renders the tracking list as a spreadsheet, one row per
// entry, enriched with the display title so the file is readable on its own.
func (s *Service) ExportTrackingCSV(ctx context.Context, id uuid.UUID) ([]byte, error) {
	entries, err := s.tracking.List(ctx, id, tracking.ListFilters{})
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{
		"media_id", "media_type", "title", "season_number",
		"status", "score", "episode_progress", "favorite", "updated_at",
	})
	for _, e := range entries {
		_ = w.Write([]string{
			strconv.Itoa(e.MediaID),
			e.MediaType,
			csvCell(derefStr(e.Title)),
			intField(e.SeasonNumber),
			derefStr(e.Status),
			intField(e.Score),
			intField(e.EpisodeProgress),
			strconv.FormatBool(e.Favorite),
			e.UpdatedAt.Format("2006-01-02"),
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("write csv: %w", err)
	}
	return buf.Bytes(), nil
}

// ExportTrackingJSON renders the tracking list as JSON — the same enriched
// entries, for callers that would rather have structure than a spreadsheet.
func (s *Service) ExportTrackingJSON(ctx context.Context, id uuid.UUID) ([]byte, error) {
	entries, err := s.tracking.List(ctx, id, tracking.ListFilters{})
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []tracking.Entry{}
	}
	return json.MarshalIndent(entries, "", "  ")
}

// ExportAccountData is the GDPR bundle: everything held about the user in one
// JSON document — profile (which holds the notification preferences), tracking,
// the full interaction history, the notices, the import runs (unmatched titles
// included) and the taste profile. Each export writes one audit line.
func (s *Service) ExportAccountData(ctx context.Context, id uuid.UUID) ([]byte, error) {
	user, err := s.repo.GetUserByID(ctx, id)
	if err != nil {
		return nil, err
	}
	entries, err := s.tracking.List(ctx, id, tracking.ListFilters{})
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []tracking.Entry{}
	}
	events, err := s.interactions.ListByUser(ctx, id, 0)
	if err != nil {
		return nil, err
	}
	if events == nil {
		events = []interactions.Interaction{}
	}
	notices, err := s.notices.ListAll(ctx, id)
	if err != nil {
		return nil, err
	}
	if notices == nil {
		notices = []notifications.Notification{}
	}
	imports, taste, err := s.accountRecords(ctx, id)
	if err != nil {
		return nil, err
	}

	out, err := json.MarshalIndent(struct {
		User          *User                        `json:"user"`
		Tracking      []tracking.Entry             `json:"tracking"`
		Interactions  []interactions.Interaction   `json:"interactions"`
		Notifications []notifications.Notification `json:"notifications"`
		Imports       []ImportRecord               `json:"imports"`
		TasteProfile  *TasteProfileRecord          `json:"taste_profile"`
	}{user, entries, events, notices, imports, taste}, "", "  ")
	if err != nil {
		return nil, err
	}
	s.auditf("account export user=%s", id)
	return out, nil
}

// csvCell keeps a value from running as a formula when the file is opened in a
// spreadsheet. Titles come from TMDB, so the export can't trust them. A real
// title that starts this way, such as "+1", shows with a leading apostrophe.
func csvCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func intField(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}
