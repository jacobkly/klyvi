package main

import (
	"context"
	"testing"

	"github.com/joeshaw/envdecode"

	"klyvi-api/config"
	"klyvi-api/internal/platform/db"
)

// Novelty looks picks up by media id, so popularity must be keyed by media
// id too. Keyed by TMDB id, a film and a series sharing an id collide and the
// lookups land on the wrong titles.
func TestLoadPopularity_KeysEveryTitleByMediaID(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB-dependent test in -short mode")
	}
	var cfg config.ConfDB
	if err := envdecode.StrictDecode(&cfg); err != nil {
		t.Skipf("DB env incomplete: %v", err)
	}
	dbConn, err := db.New(cfg)
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { _ = dbConn.Close() })
	ctx := context.Background()

	pop, err := loadPopularity(ctx, dbConn)
	if err != nil {
		t.Fatalf("loadPopularity: %v", err)
	}
	if pop.films == 0 || pop.series == 0 {
		t.Skipf("catalog holds %d films and %d series; need some of each", pop.films, pop.series)
	}
	if got, want := len(pop.place), pop.films+pop.series; got != want {
		t.Errorf("places = %d, want one per title (%d)", got, want)
	}

	var ids []int
	if err := dbConn.SelectContext(ctx, &ids, `
		select mi.media_id from movies m
		join media_index mi on mi.id = m.movie_id and mi.media_type = 'movie'
		where m.vote_count > 0
		union all
		select mi.media_id from tv_series s
		join media_index mi on mi.id = s.tv_id and mi.media_type = 'tv'
		where s.vote_count > 0
	`); err != nil {
		t.Fatalf("list media ids: %v", err)
	}
	for _, id := range ids {
		if _, ok := pop.place[id]; !ok {
			t.Fatalf("media id %d has no popularity place", id)
		}
	}
}
