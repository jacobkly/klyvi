package interactions_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"klyvi-api/internal/interactions"
	"klyvi-api/internal/platform/http/middleware"
)

// getList calls GET /v1/interactions with a raw query.
func getList(t *testing.T, env *interactionsTestEnv, query string) (int, []interactions.Interaction) {
	t.Helper()
	api := interactions.NewAPI(env.service)
	req := httptest.NewRequest("GET", "/v1/interactions"+query, nil)
	req = req.WithContext(middleware.WithUserUUID(req.Context(), env.userID))
	rec := httptest.NewRecorder()
	api.List(rec, req)
	var body struct {
		Data []interactions.Interaction `json:"data"`
	}
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return rec.Code, body.Data
}

// seedHistory records one rating, then adds n older impressions of the same
// film straight into the table.
func seedHistory(t *testing.T, env *interactionsTestEnv, n int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rating := 90
	row, err := env.service.Record(ctx, env.userID, interactions.RecordRequest{
		TMDBID:    550,
		MediaType: interactions.MediaTypeMovie,
		Kind:      interactions.KindRated,
		Rating:    &rating,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := env.db.ExecContext(ctx, `
		insert into interactions (user_id, media_id, media_type, kind, created_at)
		select $1, $2, 'movie', 'impression', now() - make_interval(mins => g)
		from generate_series(1, $3) g
	`, env.userID, row.MediaID, n); err != nil {
		t.Fatalf("seed impressions: %v", err)
	}
}

func TestInteractions_List_IsCapped(t *testing.T) {
	env := setup(t)
	seedHistory(t, env, interactions.DefaultListCap+5)

	code, rows := getList(t, env, "")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if len(rows) != interactions.DefaultListCap {
		t.Errorf("no limit: got %d rows, want the cap of %d", len(rows), interactions.DefaultListCap)
	}

	if _, rows := getList(t, env, "?limit=3"); len(rows) != 3 {
		t.Errorf("limit=3: got %d rows", len(rows))
	}
	if _, rows := getList(t, env, "?limit=100000"); len(rows) != interactions.DefaultListCap+6 {
		t.Errorf("limit=100000: got %d rows, want all %d", len(rows), interactions.DefaultListCap+6)
	}
}

// The web counts ratings. Under the cap, a long tail of impressions must not
// push them out of the list, so it asks for the kind it counts.
func TestInteractions_List_FiltersByKind(t *testing.T) {
	env := setup(t)
	seedHistory(t, env, 10)

	code, rows := getList(t, env, "?kind=rated&limit=5")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if len(rows) != 1 || rows[0].Kind != interactions.KindRated {
		t.Errorf("kind=rated: got %+v, want the one rating", rows)
	}
}

func TestInteractions_List_RejectsABadLimitOrKind(t *testing.T) {
	env := setup(t)
	for _, q := range []string{"?limit=0", "?limit=x", "?kind=liked"} {
		if code, _ := getList(t, env, q); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, code)
		}
	}
}
