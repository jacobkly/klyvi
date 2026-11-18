package users_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"klyvi-api/internal/users"
)

// fakeRecords stands in for the import and taste profile reads.
type fakeRecords struct {
	imports []users.ImportRecord
	taste   *users.TasteProfileRecord
}

func (f *fakeRecords) ListImports(context.Context, uuid.UUID) ([]users.ImportRecord, error) {
	return f.imports, nil
}

func (f *fakeRecords) GetTasteProfile(context.Context, uuid.UUID) (*users.TasteProfileRecord, error) {
	return f.taste, nil
}

// The bundle holds the import results, unmatched titles included, and the
// taste profile, so "everything held about you" is true.
func TestExportAccountData_HoldsImportsAndTasteProfile(t *testing.T) {
	src := exportFixture()
	repo := &fakeUserRepo{user: &users.User{ID: uuid.New(), Username: "jacob"}}
	records := &fakeRecords{
		imports: []users.ImportRecord{{
			ID: uuid.New(), Source: "letterboxd", Status: "done", Total: 3, Matched: 2, Unmatched: 1,
			UnmatchedTitles: json.RawMessage(`[{"title":"A lost film","year":1931}]`),
		}},
		taste: &users.TasteProfileRecord{
			GenreWeights:   json.RawMessage(`{"18":0.7}`),
			KeywordWeights: json.RawMessage(`{}`),
			EraWeights:     json.RawMessage(`{"1990":0.4}`),
			LikedCount:     12,
		},
	}
	svc := users.NewService(repo).WithExportSources(src, src, src).WithAccountRecords(records)

	data, err := svc.ExportAccountData(context.Background(), repo.user.ID)
	if err != nil {
		t.Fatalf("ExportAccountData: %v", err)
	}
	var bundle struct {
		Imports []struct {
			Source          string            `json:"source"`
			Matched         int               `json:"matched"`
			UnmatchedTitles []json.RawMessage `json:"unmatched_titles"`
		} `json:"imports"`
		TasteProfile *struct {
			GenreWeights map[string]float64 `json:"genre_weights"`
			LikedCount   int                `json:"liked_count"`
		} `json:"taste_profile"`
	}
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(bundle.Imports) != 1 || bundle.Imports[0].Source != "letterboxd" || bundle.Imports[0].Matched != 2 {
		t.Fatalf("imports = %+v, want the one letterboxd import", bundle.Imports)
	}
	if len(bundle.Imports[0].UnmatchedTitles) != 1 {
		t.Errorf("unmatched titles = %d, want 1", len(bundle.Imports[0].UnmatchedTitles))
	}
	if bundle.TasteProfile == nil || bundle.TasteProfile.GenreWeights["18"] != 0.7 || bundle.TasteProfile.LikedCount != 12 {
		t.Errorf("taste profile = %+v", bundle.TasteProfile)
	}
}

// An account with no imports and no profile yet still gets both keys, as an
// empty list and null, so the file's shape never changes.
func TestExportAccountData_EmptyRecordsKeepTheirKeys(t *testing.T) {
	src := exportFixture()
	repo := &fakeUserRepo{user: &users.User{ID: uuid.New()}}
	svc := users.NewService(repo).WithExportSources(src, src, src).WithAccountRecords(&fakeRecords{})

	data, err := svc.ExportAccountData(context.Background(), repo.user.ID)
	if err != nil {
		t.Fatalf("ExportAccountData: %v", err)
	}
	if !strings.Contains(string(data), `"imports": []`) {
		t.Errorf("want an empty imports list in %s", data)
	}
	if !strings.Contains(string(data), `"taste_profile": null`) {
		t.Errorf("want a null taste profile in %s", data)
	}
}

func auditLines(buf *bytes.Buffer) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.Contains(l, "audit:") {
			out = append(out, l)
		}
	}
	return out
}

func TestExportAccountData_WritesOneAuditLine(t *testing.T) {
	var buf bytes.Buffer
	src := exportFixture()
	repo := &fakeUserRepo{user: &users.User{ID: uuid.New()}}
	svc := users.NewService(repo).WithExportSources(src, src, src).
		WithAccountRecords(&fakeRecords{}).
		WithAuditLog(log.New(&buf, "", 0))

	if _, err := svc.ExportAccountData(context.Background(), repo.user.ID); err != nil {
		t.Fatalf("ExportAccountData: %v", err)
	}
	lines := auditLines(&buf)
	if len(lines) != 1 {
		t.Fatalf("audit lines = %q, want exactly one", lines)
	}
	if !strings.Contains(lines[0], "export") || !strings.Contains(lines[0], repo.user.ID.String()) {
		t.Errorf("audit line %q should name the export and the user id", lines[0])
	}
}

func TestDeleteAccount_WritesOneAuditLine(t *testing.T) {
	var buf bytes.Buffer
	id := uuid.New()
	repo := &fakeDeleteRepo{fakeUserRepo: fakeUserRepo{user: &users.User{ID: id}}}
	svc := users.NewService(repo).
		WithAccountDeletion(&fakeAuthDeleter{}, "avatars").
		WithAuditLog(log.New(&buf, "", 0))

	if err := svc.DeleteAccount(context.Background(), id); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	lines := auditLines(&buf)
	if len(lines) != 1 {
		t.Fatalf("audit lines = %q, want exactly one", lines)
	}
	if !strings.Contains(lines[0], "delete") || !strings.Contains(lines[0], id.String()) {
		t.Errorf("audit line %q should name the delete and the user id", lines[0])
	}
}

// The real reads, on the real tables.
func TestRepository_ImportsAndTasteProfileForExport(t *testing.T) {
	conn := prefsTestDB(t)
	repo := users.NewRepository(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	id := uuid.New()
	if err := repo.EnsureUser(ctx, id); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = repo.DeleteAccountData(ctx, id)
	})

	none, err := repo.GetTasteProfile(ctx, id)
	if err != nil || none != nil {
		t.Fatalf("GetTasteProfile with no row = %+v, %v, want nil", none, err)
	}
	imps, err := repo.ListImports(ctx, id)
	if err != nil || len(imps) != 0 {
		t.Fatalf("ListImports with none = %+v, %v", imps, err)
	}

	if _, err := conn.ExecContext(ctx, `
		insert into imports (id, user_id, source, status, total, matched, unmatched, unmatched_titles)
		values ($1, $2, 'letterboxd', 'done', 3, 2, 1, '[{"title":"A lost film","year":1931}]')
	`, uuid.New(), id); err != nil {
		t.Fatalf("insert import: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `
		insert into taste_profiles (user_id, genre_weights, liked_count)
		values ($1, '{"18":0.7}', 12)
	`, id); err != nil {
		t.Fatalf("insert taste profile: %v", err)
	}

	imps, err = repo.ListImports(ctx, id)
	if err != nil || len(imps) != 1 {
		t.Fatalf("ListImports = %+v, %v, want 1", imps, err)
	}
	if imps[0].Matched != 2 || !strings.Contains(string(imps[0].UnmatchedTitles), "A lost film") {
		t.Errorf("import = %+v", imps[0])
	}
	tp, err := repo.GetTasteProfile(ctx, id)
	if err != nil || tp == nil {
		t.Fatalf("GetTasteProfile = %+v, %v", tp, err)
	}
	if tp.LikedCount != 12 || !strings.Contains(string(tp.GenreWeights), `"18"`) {
		t.Errorf("taste profile = %+v", tp)
	}
}
