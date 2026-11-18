package tv_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"klyvi-api/internal/tv"
)

// namelessTMDB answers like fakeTMDB, except that series payloads lose the
// fields listed in drop and carry the ones in set, the way a partial TMDB
// answer would arrive.
type namelessTMDB struct {
	fakeTMDB
	drop []string
	set  map[string]any
}

func (f *namelessTMDB) TMDBRequest(ctx context.Context, method, endpoint string, body interface{}) (map[string]interface{}, error) {
	raw, err := f.fakeTMDB.TMDBRequest(ctx, method, endpoint, body)
	if err != nil || strings.Contains(endpoint, "/season/") {
		return raw, err
	}
	for _, k := range f.drop {
		delete(raw, k)
	}
	for k, v := range f.set {
		raw[k] = v
	}
	return raw, nil
}

func cachedSeriesName(t *testing.T, dbh *sqlx.DB, tvID int) (name sql.NullString, found bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := dbh.QueryRowContext(ctx, `select name from tv_series where tv_id = $1`, tvID).Scan(&name)
	if err == sql.ErrNoRows {
		return name, false
	}
	if err != nil {
		t.Fatalf("read cached name: %v", err)
	}
	return name, true
}

func seriesTitle(t *testing.T, got interface{}) string {
	t.Helper()
	series, ok := got.(*tv.TVSeries)
	if !ok {
		t.Fatalf("GetTvById returned %T, want *tv.TVSeries", got)
	}
	if series.Name == nil {
		return ""
	}
	return *series.Name
}

// A series cached without its name was served as is, and the web showed it
// as "Untitled". The API now refetches such a row and serves the name.
func TestGetTvById_RefetchesASeriesCachedWithoutItsName(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, _, dbh := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, tt := range []struct {
		label string
		tvID  int
		name  any
	}{
		{"null name", 2_100_000_070, nil},
		{"blank name", 2_100_000_071, "  "},
	} {
		t.Run(tt.label, func(t *testing.T) {
			resetShow(t, dbh, tt.tvID)
			if _, err := dbh.ExecContext(ctx, `
				insert into tv_series (tv_id, name, keywords, credits)
				values ($1, $2, '[{"id": 1}]', '{"cast": []}')`, tt.tvID, tt.name); err != nil {
				t.Fatalf("insert nameless series: %v", err)
			}

			client := &fakeTMDB{}
			got, err := tv.NewService(client, tv.NewRepository(dbh)).GetTvById(ctx, "external", tt.tvID, 0)
			if err != nil {
				t.Fatalf("GetTvById: %v", err)
			}

			if title := seriesTitle(t, got); title != "Klyvi test series" {
				t.Errorf("served name %q, want the refetched name", title)
			}
			if name, _ := cachedSeriesName(t, dbh, tt.tvID); name.String != "Klyvi test series" {
				t.Errorf("cached name %q, want the refetched name", name.String)
			}
			if n := client.seriesCalls(); n != 1 {
				t.Errorf("series fetched %d times, want 1", n)
			}
		})
	}
}

// A named row is served from the cache without asking TMDB again.
func TestGetTvById_ServesANamedSeriesFromTheCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, _, dbh := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const tvID = 2_100_000_072
	resetShow(t, dbh, tvID)
	client := &fakeTMDB{}
	svc := tv.NewService(client, tv.NewRepository(dbh))
	for range 2 {
		if _, err := svc.GetTvById(ctx, "external", tvID, 0); err != nil {
			t.Fatalf("GetTvById: %v", err)
		}
	}
	if n := client.seriesCalls(); n != 1 {
		t.Errorf("series fetched %d times, want 1", n)
	}
}

// TMDB answers without a display name in English fall back to the original
// name, and an answer with neither is an error that caches nothing.
func TestGetTvById_NamelessTMDBAnswer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, _, dbh := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("falls back to the original name", func(t *testing.T) {
		const tvID = 2_100_000_073
		resetShow(t, dbh, tvID)
		client := &namelessTMDB{
			drop: []string{"name"},
			set:  map[string]any{"original_name": "Original test name"},
		}
		got, err := tv.NewService(client, tv.NewRepository(dbh)).GetTvById(ctx, "external", tvID, 0)
		if err != nil {
			t.Fatalf("GetTvById: %v", err)
		}
		if title := seriesTitle(t, got); title != "Original test name" {
			t.Errorf("served name %q, want the original name", title)
		}
	})

	t.Run("no name at all caches nothing", func(t *testing.T) {
		const tvID = 2_100_000_074
		resetShow(t, dbh, tvID)
		client := &namelessTMDB{drop: []string{"name", "original_name"}}
		if _, err := tv.NewService(client, tv.NewRepository(dbh)).GetTvById(ctx, "external", tvID, 0); err == nil {
			t.Fatal("GetTvById served a series with no name")
		}
		if _, found := cachedSeriesName(t, dbh, tvID); found {
			t.Error("a series with no name was cached")
		}
	})
}

// The refresh job never overwrites a named row with a nameless answer.
func TestRefreshSeries_KeepsTheNameWhenTMDBSendsNone(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}
	_, _, dbh := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const tvID = 2_100_000_075
	resetShow(t, dbh, tvID)
	if _, err := tv.NewService(&fakeTMDB{}, tv.NewRepository(dbh)).GetTvById(ctx, "external", tvID, 0); err != nil {
		t.Fatalf("cache series: %v", err)
	}

	client := &namelessTMDB{drop: []string{"name", "original_name"}}
	if _, err := tv.NewService(client, tv.NewRepository(dbh)).RefreshSeries(ctx, tvID); err == nil {
		t.Error("RefreshSeries accepted a series with no name")
	}
	if name, _ := cachedSeriesName(t, dbh, tvID); name.String != "Klyvi test series" {
		t.Errorf("cached name %q after a nameless refresh, want it kept", name.String)
	}
}
