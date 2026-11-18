package tmdb

import (
	"encoding/json"
	"testing"
)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestMovieCertification(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "theatrical US rating wins over an earlier premiere",
			raw: `{"release_dates":{"results":[
				{"iso_3166_1":"GB","release_dates":[{"certification":"12A","type":3}]},
				{"iso_3166_1":"US","release_dates":[
					{"certification":"","type":1},
					{"certification":"NR","type":2},
					{"certification":"PG-13","type":3}
				]}
			]}}`,
			want: "PG-13",
		},
		{
			name: "falls back to any US rating",
			raw: `{"release_dates":{"results":[
				{"iso_3166_1":"US","release_dates":[{"certification":"","type":3},{"certification":"R","type":4}]}
			]}}`,
			want: "R",
		},
		{
			name: "no US release",
			raw:  `{"release_dates":{"results":[{"iso_3166_1":"FR","release_dates":[{"certification":"12","type":3}]}]}}`,
			want: "<nil>",
		},
		{name: "not appended", raw: `{}`, want: "<nil>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deref(MovieCertification(decode(t, tt.raw))); got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestTVRating(t *testing.T) {
	raw := decode(t, `{"content_ratings":{"results":[
		{"iso_3166_1":"DE","rating":"16"},
		{"iso_3166_1":"US","rating":"TV-MA"}
	]}}`)
	if got := deref(TVRating(raw)); got != "TV-MA" {
		t.Errorf("got %s, want TV-MA", got)
	}
	if got := TVRating(decode(t, `{"content_ratings":{"results":[{"iso_3166_1":"US","rating":""}]}}`)); got != nil {
		t.Errorf("blank rating: got %q, want nil", *got)
	}
}

func TestTrailerKey(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "official YouTube trailer, the first one released",
			raw: `{"videos":{"results":[
				{"site":"YouTube","type":"Teaser","official":true,"key":"teaser00001","published_at":"2010-01-01T00:00:00.000Z"},
				{"site":"YouTube","type":"Trailer","official":false,"key":"fanmade0001","published_at":"2009-01-01T00:00:00.000Z"},
				{"site":"Vimeo","type":"Trailer","official":true,"key":"123456","published_at":"2009-06-01T00:00:00.000Z"},
				{"site":"YouTube","type":"Trailer","official":true,"key":"final000001","published_at":"2010-06-01T00:00:00.000Z"},
				{"site":"YouTube","type":"Trailer","official":true,"key":"YoHD9XEInc0","published_at":"2010-05-01T00:00:00.000Z"}
			]}}`,
			want: "YoHD9XEInc0",
		},
		{
			name: "an unofficial trailer beats none",
			raw:  `{"videos":{"results":[{"site":"YouTube","type":"Trailer","official":false,"key":"fanmade0001"}]}}`,
			want: "fanmade0001",
		},
		{
			name: "a key that isn't a YouTube id is dropped",
			raw:  `{"videos":{"results":[{"site":"YouTube","type":"Trailer","official":true,"key":"x\" onload=\"y"}]}}`,
			want: "<nil>",
		},
		{name: "teasers only", raw: `{"videos":{"results":[{"site":"YouTube","type":"Teaser","key":"teaser00001"}]}}`, want: "<nil>"},
		{name: "not appended", raw: `{}`, want: "<nil>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deref(TrailerKey(decode(t, tt.raw))); got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestExternalIMDbID(t *testing.T) {
	if got := deref(ExternalIMDbID(decode(t, `{"external_ids":{"imdb_id":"tt11280740"}}`))); got != "tt11280740" {
		t.Errorf("got %s", got)
	}
	for _, raw := range []string{`{"external_ids":{"imdb_id":""}}`, `{"external_ids":{"imdb_id":"nope"}}`, `{}`} {
		if got := ExternalIMDbID(decode(t, raw)); got != nil {
			t.Errorf("%s: got %q, want nil", raw, *got)
		}
	}
}

func TestHasExtras(t *testing.T) {
	if !HasExtras(decode(t, `{"videos":{"results":[]}}`)) {
		t.Error("an appended detail answer should count as having extras")
	}
	if HasExtras(decode(t, `{"id":1}`)) {
		t.Error("a search hit has no extras")
	}
}
