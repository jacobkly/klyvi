package users

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/google/uuid"

	"klyvi-api/internal/interactions"
	"klyvi-api/internal/tracking"
)

// Export composes data owned by other domains (tracking, interactions) into
// user-facing downloads. The readers are consumer interfaces so this package
// depends on behaviour, not on those services' construction. Import direction
// is one-way — neither tracking nor interactions imports users.

// TrackingReader is the enriched tracking list the export needs.
type TrackingReader interface {
	List(ctx context.Context, userID uuid.UUID, filters tracking.ListFilters) ([]tracking.Entry, error)
}

// InteractionReader is the interaction history the export needs. sinceDays<=0
// means "all history" in the interactions service.
type InteractionReader interface {
	ListByUser(ctx context.Context, userID uuid.UUID, sinceDays int) ([]interactions.Interaction, error)
}

// WithExportSources wires the data sources the export endpoints read from.
func (s *Service) WithExportSources(tr TrackingReader, ir InteractionReader) *Service {
	s.tracking = tr
	s.interactions = ir
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
			derefStr(e.Title),
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
// JSON document — profile, tracking, and the full interaction history.
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

	return json.MarshalIndent(struct {
		User         *User                      `json:"user"`
		Tracking     []tracking.Entry           `json:"tracking"`
		Interactions []interactions.Interaction `json:"interactions"`
	}{user, entries, events}, "", "  ")
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
