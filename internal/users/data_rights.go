package users

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
)

// ImportRecord is one import run as the account export shows it, the titles
// that didn't match included.
type ImportRecord struct {
	ID              uuid.UUID       `json:"id" db:"id"`
	Source          string          `json:"source" db:"source"`
	Status          string          `json:"status" db:"status"`
	Total           int             `json:"total" db:"total"`
	Matched         int             `json:"matched" db:"matched"`
	Unmatched       int             `json:"unmatched" db:"unmatched"`
	UnmatchedTitles json.RawMessage `json:"unmatched_titles" db:"-"`
	Error           *string         `json:"error" db:"error"`
	CreatedAt       time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at" db:"updated_at"`
}

// TasteProfileRecord is the stored taste profile as the account export shows
// it. The weights stay as the JSON the recommender wrote.
type TasteProfileRecord struct {
	GenreWeights       json.RawMessage `json:"genre_weights"`
	KeywordWeights     json.RawMessage `json:"keyword_weights"`
	EraWeights         json.RawMessage `json:"era_weights"`
	QualitySensitivity float64         `json:"quality_sensitivity"`
	LikedCount         int             `json:"liked_count"`
	DislikedCount      int             `json:"disliked_count"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

// AccountRecordReader reads the rows the account export holds beyond the
// other domains' data. *Repository satisfies it.
type AccountRecordReader interface {
	ListImports(ctx context.Context, userID uuid.UUID) ([]ImportRecord, error)
	GetTasteProfile(ctx context.Context, userID uuid.UUID) (*TasteProfileRecord, error)
}

// WithAccountRecords wires the import and taste profile reads into the
// account export.
func (s *Service) WithAccountRecords(r AccountRecordReader) *Service {
	s.records = r
	return s
}

// WithAuditLog sends the export and delete audit lines to l rather than the
// default logger.
func (s *Service) WithAuditLog(l *log.Logger) *Service {
	s.audit = l
	return s
}

// auditf writes one audit line. Exports and deletions each leave exactly one,
// with the user id, so a request about an account can be traced afterwards.
func (s *Service) auditf(format string, args ...any) {
	l := s.audit
	if l == nil {
		l = log.Default()
	}
	l.Printf("audit: "+format, args...)
}

// accountRecords reads the imports and taste profile for the export. Without
// a reader the sections come out empty, never missing.
func (s *Service) accountRecords(ctx context.Context, id uuid.UUID) ([]ImportRecord, *TasteProfileRecord, error) {
	if s.records == nil {
		return []ImportRecord{}, nil, nil
	}
	imports, err := s.records.ListImports(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if imports == nil {
		imports = []ImportRecord{}
	}
	taste, err := s.records.GetTasteProfile(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return imports, taste, nil
}

// ListImports returns the user's import runs, oldest first.
func (r *Repository) ListImports(ctx context.Context, userID uuid.UUID) ([]ImportRecord, error) {
	rows, err := r.db.QueryxContext(ctx, `
		select id, source, status, total, matched, unmatched,
		       unmatched_titles::text, error, created_at, updated_at
		from imports
		where user_id = $1
		order by created_at, id
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list imports: %w", err)
	}
	defer rows.Close()

	out := []ImportRecord{}
	for rows.Next() {
		var rec ImportRecord
		var titles string
		if err := rows.Scan(&rec.ID, &rec.Source, &rec.Status, &rec.Total, &rec.Matched,
			&rec.Unmatched, &titles, &rec.Error, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan import: %w", err)
		}
		rec.UnmatchedTitles = json.RawMessage(titles)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list imports: %w", err)
	}
	return out, nil
}

// GetTasteProfile returns the user's stored taste profile, or nil when the
// recommender hasn't built one yet.
func (r *Repository) GetTasteProfile(ctx context.Context, userID uuid.UUID) (*TasteProfileRecord, error) {
	var rec TasteProfileRecord
	var genre, keyword, era string
	err := r.db.QueryRowxContext(ctx, `
		select coalesce(genre_weights, '{}')::text,
		       coalesce(keyword_weights, '{}')::text,
		       coalesce(era_weights, '{}')::text,
		       coalesce(quality_sensitivity, 0), coalesce(liked_count, 0),
		       coalesce(disliked_count, 0), coalesce(updated_at, now())
		from taste_profiles
		where user_id = $1
	`, userID).Scan(&genre, &keyword, &era, &rec.QualitySensitivity,
		&rec.LikedCount, &rec.DislikedCount, &rec.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get taste profile: %w", err)
	}
	rec.GenreWeights = json.RawMessage(genre)
	rec.KeywordWeights = json.RawMessage(keyword)
	rec.EraWeights = json.RawMessage(era)
	return &rec, nil
}
