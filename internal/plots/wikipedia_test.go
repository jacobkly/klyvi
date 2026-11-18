package plots

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTitleFromURL(t *testing.T) {
	tests := []struct {
		url    string
		want   string
		wantOK bool
	}{
		{"https://en.wikipedia.org/wiki/The_Matrix", "The Matrix", true},
		{"https://en.wikipedia.org/wiki/Am%C3%A9lie", "Amélie", true},
		{"https://en.wikipedia.org/wiki/Se7en_(film)", "Se7en (film)", true},
		{"https://de.wikipedia.org/wiki/Matrix", "", false},
		{"https://en.wikipedia.org/w/index.php?title=X", "", false},
		{"://bad", "", false},
	}
	for _, tt := range tests {
		got, ok := TitleFromURL(tt.url)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("TitleFromURL(%q) = %q, %v, want %q, %v", tt.url, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestWikipediaRevisions(t *testing.T) {
	var form string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm.Encode()
		if ua := r.Header.Get("User-Agent"); ua != "test-agent/1.0" {
			t.Errorf("user agent %q", ua)
		}
		_, _ = w.Write([]byte(`{"query":{
			"normalized":[{"from":"the Matrix","to":"The Matrix"}],
			"redirects":[{"from":"Se7en","to":"Seven (1995 film)"}],
			"pages":[
				{"title":"The Matrix","revisions":[{"revid":101}]},
				{"title":"Seven (1995 film)","revisions":[{"revid":202}]},
				{"title":"Gone Page","missing":true}
			]}}`))
	}))
	defer srv.Close()

	got, err := NewWikipedia(srv.URL, "test-agent/1.0").Revisions(context.Background(),
		[]string{"the Matrix", "Se7en", "Gone Page"})
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if len(got) != 2 || got["the Matrix"] != 101 || got["Se7en"] != 202 {
		t.Errorf("Revisions = %v, want the Matrix 101 and Se7en 202", got)
	}
	for _, p := range []string{"redirects=1", "rvprop=ids", "maxlag=5", "titles=the+Matrix%7CSe7en%7CGone+Page"} {
		if !strings.Contains(form, p) {
			t.Errorf("request %q lacks %q", form, p)
		}
	}
}

func TestWikipediaTexts_FollowsContinuations(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		calls++
		if r.PostForm.Get("rvcontinue") == "" {
			_, _ = w.Write([]byte(`{"continue":{"rvcontinue":"2","continue":"||"},"query":{"pages":[
				{"title":"A","revisions":[{"revid":1,"slots":{"main":{"content":"one"}}}]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"query":{"pages":[
			{"title":"B","revisions":[{"revid":2,"slots":{"main":{"content":"two"}}}]}]}}`))
	}))
	defer srv.Close()

	got, err := NewWikipedia(srv.URL, "t").Texts(context.Background(), []int64{1, 2})
	if err != nil {
		t.Fatalf("Texts: %v", err)
	}
	if calls != 2 || got[1] != "one" || got[2] != "two" {
		t.Errorf("Texts = %v after %d calls", got, calls)
	}
}

func TestWikipedia_RetriesWhileLagging(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			_, _ = w.Write([]byte(`{"error":{"code":"maxlag","info":"lagging"}}`))
		case 2:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			_, _ = w.Write([]byte(`{"query":{"pages":[{"title":"A","revisions":[{"revid":7}]}]}}`))
		}
	}))
	defer srv.Close()

	wp := NewWikipedia(srv.URL, "t")
	wp.backoff = time.Millisecond
	got, err := wp.Revisions(context.Background(), []string{"A"})
	if err != nil || got["A"] != 7 || calls != 3 {
		t.Errorf("Revisions = %v, %v after %d calls", got, err, calls)
	}
}

func TestWikipedia_ReportsAnAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":{"code":"toomanyvalues","info":"Too many values"}}`))
	}))
	defer srv.Close()
	_, err := NewWikipedia(srv.URL, "t").Revisions(context.Background(), []string{"A"})
	if err == nil || !strings.Contains(err.Error(), "toomanyvalues") {
		t.Errorf("err = %v, want the API error", err)
	}
}

func TestWikipedia_RetriesWhenRateLimited(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"query":{"pages":[{"title":"A","revisions":[{"revid":7}]}]}}`))
	}))
	defer srv.Close()

	wp := NewWikipedia(srv.URL, "t")
	wp.backoff = time.Millisecond
	got, err := wp.Revisions(context.Background(), []string{"A"})
	if err != nil || got["A"] != 7 || calls != 3 {
		t.Errorf("Revisions = %v, %v after %d calls", got, err, calls)
	}
}

// Waits double from the backoff, never fall short of what the server asked
// for, and stop growing at the cap.
func TestRetryDelay(t *testing.T) {
	cases := []struct {
		attempt int
		asked   time.Duration
		want    time.Duration
	}{
		{1, 0, 5 * time.Second},
		{2, 0, 10 * time.Second},
		{4, 0, 40 * time.Second},
		{1, 30 * time.Second, 30 * time.Second},
		{8, 0, maxBackoff},
		{1, time.Hour, maxBackoff},
	}
	for _, c := range cases {
		if got := retryDelay(c.attempt, 5*time.Second, c.asked); got != c.want {
			t.Errorf("retryDelay(%d, 5s, %v) = %v, want %v", c.attempt, c.asked, got, c.want)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	cases := map[string]time.Duration{"": 0, "12": 12 * time.Second, "-3": 0, "soon": 0, "0": 0}
	for header, want := range cases {
		if got := retryAfter(header); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", header, got, want)
		}
	}
}
