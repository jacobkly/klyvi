package tmdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"golang.org/x/time/rate"
)

// TMDB documents ~50 req/s; staying under it with a small burst keeps us
// well clear of throttling while still riding the cap during ingestion.
const (
	tmdbRatePerSec  = 40
	tmdbBurst       = 10
	tmdbMaxAttempts = 3
	tmdbBaseBackoff = 100 * time.Millisecond
)

type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
	limiter *rate.Limiter
}

func NewClient(apiKey string) *Client {
	baseURL := os.Getenv("TMDB_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.themoviedb.org/3"
	}

	return &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		http:    &http.Client{Timeout: 10 * time.Second},
		limiter: rate.NewLimiter(rate.Limit(tmdbRatePerSec), tmdbBurst),
	}
}

// newClientForTest constructs a Client with explicit baseURL and limiter
// parameters. Reserved for tests that need to point at httptest and use a
// tighter limiter than production.
func newClientForTest(apiKey, baseURL string, ratePerSec, burst int) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		http:    &http.Client{Timeout: 10 * time.Second},
		limiter: rate.NewLimiter(rate.Limit(ratePerSec), burst),
	}
}

func (c *Client) TMDBRequest(method, endpoint string, body interface{}) (map[string]interface{}, error) {
	var bodyBytes []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyBytes = b
	}

	var lastErr error
	for attempt := 1; attempt <= tmdbMaxAttempts; attempt++ {
		if err := c.limiter.Wait(context.Background()); err != nil {
			return nil, err
		}

		result, retriable, err := c.doOnce(method, endpoint, bodyBytes, body != nil)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !retriable {
			return nil, err
		}
		if attempt < tmdbMaxAttempts {
			time.Sleep(tmdbBaseBackoff * (1 << (attempt - 1)))
		}
	}
	return nil, lastErr
}

// doOnce performs a single HTTP attempt. The second return value indicates
// whether the failure (if any) is worth retrying: transport errors and 5xx
// responses are retriable; 4xx and decode errors are not.
func (c *Client) doOnce(method, endpoint string, bodyBytes []byte, hasBody bool) (map[string]interface{}, bool, error) {
	var reqBodyReader *bytes.Reader
	if hasBody {
		reqBodyReader = bytes.NewReader(bodyBytes)
	} else {
		reqBodyReader = bytes.NewReader(nil)
	}

	url := fmt.Sprintf("%s%s", c.baseURL, endpoint)
	req, err := http.NewRequest(method, url, reqBodyReader)
	if err != nil {
		return nil, false, err
	}

	req.Header.Add("accept", "application/json")
	req.Header.Add("Authorization", "Bearer "+c.apiKey)
	if hasBody {
		req.Header.Add("Content-Type", "application/json")
	}

	res, err := c.http.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer res.Body.Close()

	if res.StatusCode >= 500 {
		return nil, true, fmt.Errorf("TMDb returned status %d", res.StatusCode)
	}
	if res.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("TMDb returned status %d", res.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		return nil, false, err
	}
	return result, false, nil
}
