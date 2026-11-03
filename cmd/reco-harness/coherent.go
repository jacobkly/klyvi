package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// coherentTaste is a curated, thematically-tight set of films, shows, or
// both. Unlike the random synthetic users (whose "taste" is noise), these
// exercise the keyword lever: a slow-burn-thriller fan should get slow-burn
// thrillers, not popular action. Leave-one-out over these is the tuning
// signal — hide one title, see if the recommender recovers it from the rest —
// so the weights can be swept toward keyword-driven recall without needing a
// real user's ratings.
type coherentTaste struct {
	name  string
	films []string
	shows []string
	// related names the shows-only taste a fan of these films would likely
	// share. The cross-media check looks for its shows in this taste's feed.
	related string
}

// coherentTastes are distinct tastes so tuning does not overfit one genre:
// three of films, four of shows and one of both. Titles are matched
// case-insensitively against the cached catalog; any that don't resolve are
// logged and skipped (leave-one-out only needs a handful).
var coherentTastes = []coherentTaste{
	{
		name:    "slowburn",
		related: "prestige-crime",
		films: []string{
			"Se7en", "Zodiac", "Prisoners", "Nightcrawler", "Gone Girl",
			"No Country for Old Men", "Memories of Murder", "The Silence of the Lambs",
			"Mystic River", "Shutter Island", "Oldboy", "Sicario", "Wind River",
			"The Girl with the Dragon Tattoo", "Blade Runner 2049", "Enemy",
			"The Departed", "Chinatown", "Fargo", "Heat",
		},
	},
	{
		name:    "animation",
		related: "animated-series",
		films: []string{
			"Toy Story", "Up", "WALL·E", "Inside Out", "Spirited Away",
			"Coco", "Ratatouille", "Finding Nemo", "How to Train Your Dragon",
			"Zootopia", "Moana", "The Incredibles", "Spider-Man: Into the Spider-Verse",
			"Kung Fu Panda", "Monsters, Inc.", "Big Hero 6", "Frozen",
			"Princess Mononoke", "Your Name.", "The Lion King",
		},
	},
	{
		name:    "cerebral-scifi",
		related: "scifi-series",
		films: []string{
			"Inception", "Interstellar", "Arrival", "Blade Runner", "The Matrix",
			"Ex Machina", "2001: A Space Odyssey", "Looper", "Edge of Tomorrow",
			"Minority Report", "District 9", "Children of Men", "Annihilation",
			"Dune", "Gattaca", "Moon", "Predestination", "Source Code",
			"Her", "The Prestige",
		},
	},
	{
		name: "prestige-crime",
		shows: []string{
			"Breaking Bad", "The Wire", "The Sopranos", "True Detective", "Mindhunter",
			"Ozark", "Better Call Saul", "Narcos", "Fargo", "Peaky Blinders",
			"Mare of Easttown", "Boardwalk Empire", "Line of Duty", "Broadchurch",
			"The Night Of", "Sharp Objects", "Bosch", "Happy Valley", "The Killing",
			"Luther",
		},
	},
	{
		name: "sitcom",
		shows: []string{
			"The Office", "Parks and Recreation", "Brooklyn Nine-Nine", "Friends",
			"Seinfeld", "Community", "Arrested Development",
			"It's Always Sunny in Philadelphia", "Schitt's Creek", "The Good Place",
			"How I Met Your Mother", "Modern Family", "Superstore", "New Girl",
			"Scrubs", "30 Rock", "Abbott Elementary", "What We Do in the Shadows",
			"Veep", "Curb Your Enthusiasm",
		},
	},
	{
		name: "scifi-series",
		shows: []string{
			"Black Mirror", "Westworld", "Severance", "Dark", "The Expanse", "Devs",
			"Battlestar Galactica", "Stranger Things", "Fringe", "Station Eleven",
			"Foundation", "Altered Carbon", "Humans", "Orphan Black", "Mr. Robot",
			"Silo", "3 Body Problem", "Lost", "The Leftovers", "Counterpart",
		},
	},
	{
		name: "animated-series",
		shows: []string{
			"Avatar: The Last Airbender", "Gravity Falls", "The Legend of Korra",
			"Adventure Time", "Over the Garden Wall", "Hilda", "Steven Universe",
			"Bluey", "Arcane", "She-Ra and the Princesses of Power", "Amphibia",
			"The Owl House", "Kipo and the Age of Wonderbeasts",
			"Star Wars: The Clone Wars", "Phineas and Ferb", "DuckTales",
			"The Dragon Prince", "Trollhunters: Tales of Arcadia",
			"Dragons: Race to the Edge", "The Amazing World of Gumball",
		},
	},
	{
		name: "fantasy-epic",
		films: []string{
			"The Lord of the Rings: The Fellowship of the Ring",
			"The Lord of the Rings: The Two Towers",
			"The Lord of the Rings: The Return of the King",
			"The Hobbit: An Unexpected Journey",
			"Harry Potter and the Prisoner of Azkaban", "Dune: Part Two",
			"Star Wars", "The Empire Strikes Back", "Pan's Labyrinth", "Stardust",
			"The Princess Bride", "Willow",
		},
		shows: []string{
			"Game of Thrones", "House of the Dragon", "The Witcher",
			"The Lord of the Rings: The Rings of Power", "Shōgun", "The Mandalorian",
			"Andor", "His Dark Materials", "The Wheel of Time", "Shadow and Bone",
			"Vikings", "The Last Kingdom",
		},
	},
}

// seededTaste is a coherent taste seeded as a user, with the media ids of the
// shows it resolved to.
type seededTaste struct {
	userID uuid.UUID
	taste  coherentTaste
	shows  []int
}

// seedCoherentTastes inserts each curated taste as a user (username
// "harness_taste_<name>") with 'rated' interactions at 90, on the film or on
// the series. Titles are resolved to media_index ids by case-insensitive
// title match. Remove the users afterwards with -clean-synthetic.
func seedCoherentTastes(ctx context.Context, dbConn *sqlx.DB) ([]seededTaste, error) {
	var seeded []seededTaste
	for _, taste := range coherentTastes {
		userID := uuid.New()
		username := "harness_taste_" + taste.name
		if _, err := dbConn.ExecContext(ctx,
			`insert into users (id, username) values ($1, $2) on conflict (id) do nothing`,
			userID, username); err != nil {
			return nil, fmt.Errorf("create taste user %s: %w", taste.name, err)
		}

		var missing []string
		seed := func(titles []string, mediaType, query string) ([]int, error) {
			var ids []int
			for _, title := range titles {
				mediaID, ok, err := resolveMediaID(ctx, dbConn, query, title)
				if err != nil {
					return nil, fmt.Errorf("resolve %q: %w", title, err)
				}
				if !ok {
					missing = append(missing, title)
					continue
				}
				if _, err := dbConn.ExecContext(ctx, `
					insert into interactions (user_id, media_id, media_type, kind, rating)
					values ($1, $2, $3, 'rated', 90)
				`, userID, mediaID, mediaType); err != nil {
					return nil, fmt.Errorf("seed interaction for %q: %w", title, err)
				}
				ids = append(ids, mediaID)
			}
			return ids, nil
		}
		films, err := seed(taste.films, "movie", filmByTitle)
		if err != nil {
			return nil, err
		}
		shows, err := seed(taste.shows, "tv", seriesByName)
		if err != nil {
			return nil, err
		}

		fmt.Printf("taste %-15s resolved %d/%d", taste.name,
			len(films)+len(shows), len(taste.films)+len(taste.shows))
		if len(missing) > 0 {
			fmt.Printf(" (missing: %s)", strings.Join(missing, ", "))
		}
		fmt.Println()
		seeded = append(seeded, seededTaste{userID: userID, taste: taste, shows: shows})
	}
	return seeded, nil
}

// filmByTitle and seriesByName find a film's or a series' media_index id by
// exact case-insensitive title ($1). Highest vote_count wins when a title
// recurs.
const (
	filmByTitle = `
		select mi.media_id
		from movies m
		join media_index mi on mi.id = m.movie_id and mi.media_type = 'movie'
		where lower(m.title) = lower($1)
		order by m.vote_count desc
		limit 1`
	seriesByName = `
		select mi.media_id
		from tv_series s
		join media_index mi on mi.id = s.tv_id and mi.media_type = 'tv'
		where lower(s.name) = lower($1)
		order by s.vote_count desc
		limit 1`
)

// resolveMediaID runs filmByTitle or seriesByName for one title.
func resolveMediaID(ctx context.Context, dbConn *sqlx.DB, query, title string) (int, bool, error) {
	var mediaID int
	err := dbConn.GetContext(ctx, &mediaID, query, title)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, false, nil
		}
		return 0, false, err
	}
	return mediaID, true, nil
}
