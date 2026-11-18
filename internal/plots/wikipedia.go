package plots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// WikipediaEndpoint is the English Wikipedia action API.
const WikipediaEndpoint = "https://en.wikipedia.org/w/api.php"

// MaxPerRequest is the most titles or revisions one API request may name.
const MaxPerRequest = 50

// Wikipedia reads article revisions from the MediaWiki action API.
type Wikipedia struct {
	http      *http.Client
	endpoint  string
	userAgent string
	retries   int
	backoff   time.Duration
}

// NewWikipedia builds a client for the endpoint. Wikimedia asks every client to
// send a descriptive user agent.
func NewWikipedia(endpoint, userAgent string) *Wikipedia {
	return &Wikipedia{
		http:      &http.Client{Timeout: 90 * time.Second},
		endpoint:  endpoint,
		userAgent: userAgent,
		retries:   6,
		backoff:   5 * time.Second,
	}
}

// TitleFromURL returns the article title of an English Wikipedia URL.
func TitleFromURL(articleURL string) (string, bool) {
	u, err := url.Parse(articleURL)
	if err != nil || u.Host != "en.wikipedia.org" || !strings.HasPrefix(u.Path, "/wiki/") {
		return "", false
	}
	title := strings.ReplaceAll(strings.TrimPrefix(u.Path, "/wiki/"), "_", " ")
	return title, title != ""
}

type apiResponse struct {
	Error *struct {
		Code string `json:"code"`
		Info string `json:"info"`
	} `json:"error"`
	Continue map[string]string `json:"continue"`
	Query    struct {
		Normalized []struct{ From, To string } `json:"normalized"`
		Redirects  []struct{ From, To string } `json:"redirects"`
		Pages      []struct {
			Title     string `json:"title"`
			Missing   bool   `json:"missing"`
			Revisions []struct {
				RevID int64 `json:"revid"`
				Slots struct {
					Main struct {
						Content string `json:"content"`
					} `json:"main"`
				} `json:"slots"`
			} `json:"revisions"`
		} `json:"pages"`
	} `json:"query"`
}

// Revisions returns the current revision id of each article, keyed by the
// title asked for. Redirects are followed. Missing articles are left out.
func (w *Wikipedia) Revisions(ctx context.Context, titles []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(titles) == 0 {
		return out, nil
	}
	if len(titles) > MaxPerRequest {
		return nil, fmt.Errorf("revisions: %d titles, at most %d", len(titles), MaxPerRequest)
	}
	resp, err := w.query(ctx, url.Values{
		"prop": {"revisions"}, "rvprop": {"ids"}, "redirects": {"1"},
		"titles": {strings.Join(titles, "|")},
	})
	if err != nil {
		return nil, fmt.Errorf("revisions: %w", err)
	}
	rename := map[string]string{}
	for _, n := range resp.Query.Normalized {
		rename[n.From] = n.To
	}
	redirect := map[string]string{}
	for _, r := range resp.Query.Redirects {
		redirect[r.From] = r.To
	}
	current := map[string]int64{}
	for _, p := range resp.Query.Pages {
		if !p.Missing && len(p.Revisions) > 0 {
			current[p.Title] = p.Revisions[0].RevID
		}
	}
	for _, asked := range titles {
		t := asked
		if to, ok := rename[t]; ok {
			t = to
		}
		for range 5 { // follow a short chain of redirects, never a loop
			to, ok := redirect[t]
			if !ok {
				break
			}
			t = to
		}
		if rev, ok := current[t]; ok {
			out[asked] = rev
		}
	}
	return out, nil
}

// Texts returns the wikitext of each revision, keyed by revision id.
// Revisions that no longer exist are left out.
func (w *Wikipedia) Texts(ctx context.Context, revisionIDs []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(revisionIDs) == 0 {
		return out, nil
	}
	if len(revisionIDs) > MaxPerRequest {
		return nil, fmt.Errorf("texts: %d revisions, at most %d", len(revisionIDs), MaxPerRequest)
	}
	ids := make([]string, len(revisionIDs))
	for i, id := range revisionIDs {
		ids[i] = strconv.FormatInt(id, 10)
	}
	params := url.Values{
		"prop": {"revisions"}, "rvprop": {"ids|content"}, "rvslots": {"main"},
		"revids": {strings.Join(ids, "|")},
	}
	// A large batch can come back in parts. The API then says where to go on.
	for range 20 {
		resp, err := w.query(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("texts: %w", err)
		}
		for _, p := range resp.Query.Pages {
			for _, r := range p.Revisions {
				out[r.RevID] = r.Slots.Main.Content
			}
		}
		if len(resp.Continue) == 0 {
			return out, nil
		}
		for k, v := range resp.Continue {
			params.Set(k, v)
		}
	}
	return nil, errors.New("texts: too many continuations")
}

// errRetry marks a response worth trying again: the service is busy, lagging
// or rate limiting.
var errRetry = errors.New("wikipedia busy")

// busyError is an errRetry that may carry how long the server asked us to wait.
type busyError struct {
	status int
	after  time.Duration
}

func (e *busyError) Error() string { return fmt.Sprintf("%v: status %d", errRetry, e.status) }
func (e *busyError) Unwrap() error { return errRetry }

// maxBackoff caps one wait between retries. Six retries from a 5 s backoff
// wait 5, 10, 20, 40, 80 and 120 s, about 4.5 minutes, before a batch is
// given up.
const maxBackoff = 2 * time.Minute

// retryDelay is the wait before retry number attempt (from 1): the backoff
// doubled each time, at least what the server asked for, at most maxBackoff.
func retryDelay(attempt int, backoff, asked time.Duration) time.Duration {
	d := backoff
	for i := 1; i < attempt && d < maxBackoff; i++ {
		d *= 2
	}
	return min(max(d, asked), maxBackoff)
}

// retryAfter reads a Retry-After header given in seconds. Anything else is 0.
func retryAfter(header string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

func (w *Wikipedia) query(ctx context.Context, params url.Values) (*apiResponse, error) {
	form := url.Values{"action": {"query"}, "format": {"json"}, "formatversion": {"2"}, "maxlag": {"5"}}
	for k, v := range params {
		form[k] = v
	}
	var err error
	for attempt := 0; attempt <= w.retries; attempt++ {
		if attempt > 0 {
			var asked time.Duration
			var busy *busyError
			if errors.As(err, &busy) {
				asked = busy.after
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryDelay(attempt, w.backoff, asked)):
			}
		}
		var resp *apiResponse
		if resp, err = w.queryOnce(ctx, form); !errors.Is(err, errRetry) {
			return resp, err
		}
	}
	return nil, err
}

func (w *Wikipedia) queryOnce(ctx context.Context, form url.Values) (*apiResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", w.userAgent)

	resp, err := w.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, &busyError{status: resp.StatusCode, after: retryAfter(resp.Header.Get("Retry-After"))}
	case resp.StatusCode != http.StatusOK:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if out.Error != nil {
		if out.Error.Code == "maxlag" || out.Error.Code == "ratelimited" {
			return nil, fmt.Errorf("%w: %s", errRetry, out.Error.Code)
		}
		return nil, fmt.Errorf("api error %s: %s", out.Error.Code, out.Error.Info)
	}
	return &out, nil
}
