package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// coherentTaste is a curated, thematically-tight set of films. Unlike the
// random synthetic users (whose "taste" is noise), these exercise the keyword
// lever: a slow-burn-thriller fan should get slow-burn thrillers, not popular
// action. Leave-one-out over these is the tuning signal — hide one film, see
// if the recommender recovers it from the rest — so the weights can be swept
// toward keyword-driven recall without needing a real user's ratings.
type coherentTaste struct {
	name   string
	titles []string
}

// coherentTastes are three distinct tastes so tuning does not overfit one
// genre. Titles are matched case-insensitively against the cached catalog;
// any that don't resolve are logged and skipped (leave-one-out only needs a
// handful).
var coherentTastes = []coherentTaste{
	{
		name: "slowburn",
		titles: []string{
			"Se7en", "Zodiac", "Prisoners", "Nightcrawler", "Gone Girl",
			"No Country for Old Men", "Memories of Murder", "The Silence of the Lambs",
			"Mystic River", "Shutter Island", "Oldboy", "Sicario", "Wind River",
			"The Girl with the Dragon Tattoo", "Blade Runner 2049", "Enemy",
			"The Departed", "Chinatown", "Fargo", "Heat",
		},
	},
	{
		name: "animation",
		titles: []string{
			"Toy Story", "Up", "WALL·E", "Inside Out", "Spirited Away",
			"Coco", "Ratatouille", "Finding Nemo", "How to Train Your Dragon",
			"Zootopia", "Moana", "The Incredibles", "Spider-Man: Into the Spider-Verse",
			"Kung Fu Panda", "Monsters, Inc.", "Big Hero 6", "Frozen",
			"Princess Mononoke", "Your Name.", "The Lion King",
		},
	},
	{
		name: "cerebral-scifi",
		titles: []string{
			"Inception", "Interstellar", "Arrival", "Blade Runner", "The Matrix",
			"Ex Machina", "2001: A Space Odyssey", "Looper", "Edge of Tomorrow",
			"Minority Report", "District 9", "Children of Men", "Annihilation",
			"Dune", "Gattaca", "Moon", "Predestination", "Source Code",
			"Her", "The Prestige",
		},
	},
}

// seedCoherentTastes inserts each curated taste as a user (username
// "harness_taste_<name>") with 'rated' interactions at 90. Titles are resolved
// to media_index ids by case-insensitive title match. Returns the created
// user ids. Remove them afterwards with -clean-synthetic.
func seedCoherentTastes(ctx context.Context, dbConn *sqlx.DB) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	for _, taste := range coherentTastes {
		userID := uuid.New()
		username := "harness_taste_" + taste.name
		if _, err := dbConn.ExecContext(ctx,
			`insert into users (id, username) values ($1, $2) on conflict (id) do nothing`,
			userID, username); err != nil {
			return nil, fmt.Errorf("create taste user %s: %w", taste.name, err)
		}

		var resolved, missed int
		var missing []string
		for _, title := range taste.titles {
			mediaID, ok, err := resolveMediaIDByTitle(ctx, dbConn, title)
			if err != nil {
				return nil, fmt.Errorf("resolve %q: %w", title, err)
			}
			if !ok {
				missed++
				missing = append(missing, title)
				continue
			}
			if _, err := dbConn.ExecContext(ctx, `
				insert into interactions (user_id, media_id, media_type, kind, rating)
				values ($1, $2, 'movie', 'rated', 90)
			`, userID, mediaID); err != nil {
				return nil, fmt.Errorf("seed interaction for %q: %w", title, err)
			}
			resolved++
		}
		fmt.Printf("taste %-14s resolved %d/%d", taste.name, resolved, len(taste.titles))
		if missed > 0 {
			fmt.Printf(" (missing: %s)", strings.Join(missing, ", "))
		}
		fmt.Println()
		ids = append(ids, userID)
	}
	return ids, nil
}

// resolveMediaIDByTitle finds the media_index id for a movie by exact
// case-insensitive title. Highest vote_count wins when a title recurs.
func resolveMediaIDByTitle(ctx context.Context, dbConn *sqlx.DB, title string) (int, bool, error) {
	var mediaID int
	err := dbConn.GetContext(ctx, &mediaID, `
		select mi.media_id
		from movies m
		join media_index mi on mi.id = m.movie_id and mi.media_type = 'movie'
		where lower(m.title) = lower($1)
		order by m.vote_count desc
		limit 1
	`, title)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, false, nil
		}
		return 0, false, err
	}
	return mediaID, true, nil
}
