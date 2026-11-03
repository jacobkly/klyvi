package reco

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"klyvi-api/internal/platform/http/middleware"
)

// feedAPI serves a cold user's feed over ten films and ten shows. The shows
// outscore the films, so only the TV share keeps films in the mixed feed.
func feedAPI() *API {
	cat := &stubCatalog{}
	for i := range 10 {
		cat.cands = append(cat.cands, Candidate{MediaID: 1 + i, MediaType: "movie", VoteAverage: float64(100 - i)})
		cat.series = append(cat.series, Candidate{MediaID: 1001 + i, MediaType: "tv", VoteAverage: float64(200 - i)})
	}
	return NewAPI(NewOrchestrator(cat, stubSignal{}, stubSeen{}, DefaultConfig()).WithTier0(voteScorer{}))
}

func getFeed(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/reco/feed"+query, nil)
	req = req.WithContext(middleware.WithUserUUID(req.Context(), uuid.New()))
	rec := httptest.NewRecorder()
	feedAPI().Feed(rec, req)
	return rec
}

// ?media= narrows the feed to films or shows. Without it, or with all, the
// feed mixes both.
func TestFeedHandler_FiltersByMedia(t *testing.T) {
	cases := []struct {
		query                string
		wantFilms, wantShows bool
	}{
		{"?limit=10", true, true},
		{"?limit=10&media=all", true, true},
		{"?limit=10&media=tv", false, true},
		{"?limit=10&media=movie", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			rec := getFeed(t, tc.query)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
			}
			var body struct {
				Data []struct {
					MediaID   int    `json:"media_id"`
					MediaType string `json:"media_type"`
				} `json:"data"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(body.Data) != 10 {
				t.Fatalf("page holds %d items, want 10", len(body.Data))
			}
			var films, shows int
			for _, it := range body.Data {
				switch it.MediaType {
				case "movie":
					films++
				case "tv":
					shows++
				default:
					t.Errorf("media %d has media_type %q", it.MediaID, it.MediaType)
				}
			}
			if (films > 0) != tc.wantFilms || (shows > 0) != tc.wantShows {
				t.Errorf("page holds %d films and %d shows, want films %v and shows %v",
					films, shows, tc.wantFilms, tc.wantShows)
			}
		})
	}
}

// A media value the feed does not know is the caller's mistake, not an
// empty feed. Seasons are tracked, never recommended on their own.
func TestFeedHandler_RejectsAnUnknownMedia(t *testing.T) {
	for _, media := range []string{"season", "movies", "TV", "both"} {
		t.Run(media, func(t *testing.T) {
			rec := getFeed(t, "?media="+media)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}
