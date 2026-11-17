package opendata

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

// WikidataEndpoint is the public Wikidata query service.
const WikidataEndpoint = "https://query.wikidata.org/sparql"

// Wikidata reads facts from a SPARQL endpoint.
type Wikidata struct {
	http      *http.Client
	endpoint  string
	userAgent string
	retries   int
	backoff   time.Duration
}

// NewWikidata builds a client for the endpoint. Wikimedia asks every client to
// send a descriptive user agent.
func NewWikidata(endpoint, userAgent string) *Wikidata {
	return &Wikidata{
		http:      &http.Client{Timeout: 90 * time.Second},
		endpoint:  endpoint,
		userAgent: userAgent,
		retries:   3,
		backoff:   5 * time.Second,
	}
}

// Facts returns what Wikidata holds about the titles with these TMDB ids, one
// entry per id it matches. When several items claim one id, the oldest (the
// lowest Q number) wins, so the match is the same every run.
func (w *Wikidata) Facts(ctx context.Context, mediaType string, tmdbIDs []int) ([]Facts, error) {
	if len(tmdbIDs) == 0 {
		return nil, nil
	}
	values := tmdbValues(tmdbIDs)
	prop := tmdbProperty(mediaType)

	items, err := w.query(ctx, itemsQuery(prop, values))
	if err != nil {
		return nil, fmt.Errorf("wikidata items: %w", err)
	}
	byID := map[int]*Facts{}
	for _, b := range items {
		tmdb, err := strconv.Atoi(b["tmdb"].Value)
		if err != nil {
			continue
		}
		item, ok := itemNumber(b["item"].Value)
		if !ok {
			continue
		}
		f := byID[tmdb]
		if f == nil || item < f.WikidataID {
			f = &Facts{MediaType: mediaType, TMDBID: tmdb, WikidataID: item}
			byID[tmdb] = f
		}
		if article := b["article"].Value; item == f.WikidataID && article != "" {
			if f.WikipediaURL == nil || article < *f.WikipediaURL {
				f.WikipediaURL = &article
			}
		}
	}
	if len(byID) == 0 {
		return nil, nil
	}

	statements, err := w.query(ctx, statementsQuery(prop, values))
	if err != nil {
		return nil, fmt.Errorf("wikidata statements: %w", err)
	}
	lists := map[string]func(*Facts) *[]int{}
	for _, p := range properties {
		lists[p.id] = p.list
	}
	for _, b := range statements {
		tmdb, err := strconv.Atoi(b["tmdb"].Value)
		if err != nil {
			continue
		}
		f := byID[tmdb]
		item, ok := itemNumber(b["item"].Value)
		if f == nil || !ok || item != f.WikidataID {
			continue
		}
		list, ok := lists[b["p"].Value]
		value, isItem := itemNumber(b["v"].Value)
		if !ok || !isItem {
			continue
		}
		*list(f) = append(*list(f), value)
	}

	out := make([]Facts, 0, len(byID))
	for _, id := range tmdbIDs {
		if f := byID[id]; f != nil {
			f.normalize()
			out = append(out, *f)
			delete(byID, id) // an id asked for twice still yields one entry
		}
	}
	return out, nil
}

// tmdbValues is the ids as a SPARQL VALUES list. TMDB ids are stored in
// Wikidata as strings.
func tmdbValues(ids []int) string {
	var b strings.Builder
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(`"` + strconv.Itoa(id) + `"`)
	}
	return b.String()
}

// itemsQuery finds the item for each id and its English Wikipedia article.
func itemsQuery(prop, values string) string {
	return `SELECT ?tmdb ?item ?article WHERE {
  VALUES ?tmdb { ` + values + ` }
  ?item wdt:` + prop + ` ?tmdb .
  OPTIONAL { ?article schema:about ?item ; schema:isPartOf <https://en.wikipedia.org/> . }
}`
}

// statementsQuery reads every property Klyvi keeps for each id's items. The
// properties go in a VALUES table rather than a UNION, which the query service
// cannot plan around the id lookup and times out on.
func statementsQuery(prop, values string) string {
	var pairs strings.Builder
	for _, p := range properties {
		pairs.WriteString(` (wdt:` + p.id + ` "` + p.id + `")`)
	}
	return `SELECT ?tmdb ?item ?p ?v WHERE {
  VALUES ?tmdb { ` + values + ` }
  ?item wdt:` + prop + ` ?tmdb .
  ?item ?wdt ?v .
  VALUES (?wdt ?p) {` + pairs.String() + ` }
}`
}

type binding map[string]struct {
	Value string `json:"value"`
}

// errRetry marks a response worth trying again: the service is busy or rate
// limiting.
var errRetry = errors.New("query service busy")

// query runs one SPARQL query, retrying while the service is busy.
func (w *Wikidata) query(ctx context.Context, sparql string) ([]binding, error) {
	var err error
	for attempt := 0; attempt <= w.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(w.backoff * time.Duration(attempt)):
			}
		}
		var rows []binding
		if rows, err = w.queryOnce(ctx, sparql); !errors.Is(err, errRetry) {
			return rows, err
		}
	}
	return nil, err
}

func (w *Wikidata) queryOnce(ctx context.Context, sparql string) ([]binding, error) {
	form := url.Values{"query": {sparql}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/sparql-results+json")
	req.Header.Set("User-Agent", w.userAgent)

	resp, err := w.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("%w: status %d", errRetry, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		Results struct {
			Bindings []binding `json:"bindings"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode results: %w", err)
	}
	return out.Results.Bindings, nil
}
