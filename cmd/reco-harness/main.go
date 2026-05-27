// reco-harness runs the leave-one-out evaluation harness against the
// recommender (ARCHITECTURE §5.7). For each user with sufficient signal it
// hides one liked item at a time, runs the cascade against the rest, and
// records whether the hidden item is recovered in the top-K (recall@K)
// and at what rank (MRR — mean reciprocal rank).
//
// Two modes:
//
//	go run ./cmd/reco-harness                   # evaluates real users in the DB
//	go run ./cmd/reco-harness -synthetic-users 5  # seeds N synthetic users
//	                                              # first, then evaluates them
//
// Synthetic mode is for end-to-end smoke testing of the cascade plumbing.
// The numbers it produces are NOT a signal of recommender quality — only
// real human ratings give that. With real ratings, recall@10 above ~0.10
// for a content-only Tier 1 is the rough order of magnitude to beat.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"

	"klyvi-api/config"
	"klyvi-api/internal/movies"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/reco"
)

func main() {
	syntheticUsers := flag.Int("synthetic-users", 0, "if >0, seed N synthetic users with random liked movies before evaluating")
	syntheticLikes := flag.Int("synthetic-likes", 15, "number of liked items per synthetic user")
	feedSize := flag.Int("feed-size", 50, "feed K used by the harness; recall@K/MRR computed up to K")
	flag.Parse()

	if err := godotenv.Load(".env.dev"); err != nil {
		log.Printf("godotenv: %v (continuing — env may already be set)", err)
	}

	cfg := config.New()
	dbConn, err := db.New(cfg.DB)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer dbConn.Close()

	movieRepo := movies.NewRepository(dbConn)
	recoCfg := reco.DefaultConfig()
	recoCfg.FeedSize = *feedSize

	catalog := newCatalogAdapter(movieRepo)

	if *syntheticUsers > 0 {
		ids, err := seedSyntheticUsers(context.Background(), dbConn, catalog, *syntheticUsers, *syntheticLikes)
		if err != nil {
			log.Fatalf("seed synthetic: %v", err)
		}
		log.Printf("seeded %d synthetic users", len(ids))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	users, err := listUsersWithSignal(ctx, dbConn, 10)
	if err != nil {
		log.Fatalf("list users: %v", err)
	}
	if len(users) == 0 {
		log.Println("no users with >=10 interactions; nothing to evaluate")
		os.Exit(0)
	}

	var (
		totalHits10 int
		totalHits50 int
		totalMRR    float64
		totalTrials int
	)

	for _, uid := range users {
		hits10, hits50, mrr, trials, err := runUserLOO(ctx, dbConn, catalog, recoCfg, uid)
		if err != nil {
			log.Printf("user %s LOO: %v", uid, err)
			continue
		}
		if trials == 0 {
			continue
		}
		fmt.Printf("user=%s trials=%d recall@10=%.3f recall@50=%.3f MRR=%.3f\n",
			uid.String()[:8], trials, hits10, hits50, mrr)

		totalHits10 += int(hits10 * float64(trials))
		totalHits50 += int(hits50 * float64(trials))
		totalMRR += mrr * float64(trials)
		totalTrials += trials
	}

	if totalTrials == 0 {
		log.Println("no trials run")
		os.Exit(0)
	}
	fmt.Println("---")
	fmt.Printf("AGGREGATE: trials=%d recall@10=%.3f recall@50=%.3f MRR=%.3f\n",
		totalTrials,
		float64(totalHits10)/float64(totalTrials),
		float64(totalHits50)/float64(totalTrials),
		totalMRR/float64(totalTrials))

	// Sample feed for the first user — gives the human something to eyeball
	// alongside the numbers (per BUILD_NOTES.md Phase 4 caveat: numbers AND
	// sample outputs are the gate).
	if len(users) > 0 {
		fmt.Println("---")
		fmt.Println("Sample top-10 feed for user", users[0].String()[:8])
		printSampleFeed(ctx, dbConn, catalog, recoCfg, users[0], 10)
	}
}

// listUsersWithSignal returns user UUIDs with at least minInteractions rows.
func listUsersWithSignal(ctx context.Context, dbConn *sqlx.DB, minInteractions int) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := dbConn.SelectContext(ctx, &ids, `
		select user_id from interactions
		group by user_id
		having count(*) >= $1
		order by count(*) desc
	`, minInteractions)
	return ids, err
}

// runUserLOO runs leave-one-out for one user. For each item the user
// "liked" (rated >=70 or logged), hide it, run the recommender against
// the rest, and check if the hidden item came back in the top-K.
func runUserLOO(ctx context.Context, dbConn *sqlx.DB, catalog *catalogAdapter, cfg reco.Config, userID uuid.UUID) (recall10, recall50, mrr float64, trials int, err error) {
	interactions, err := loadInteractions(ctx, dbConn, userID)
	if err != nil {
		return 0, 0, 0, 0, err
	}

	// Identify "positive" items: rated>=70 OR logged.
	type liked struct {
		MediaID int
		IdxInArr int
	}
	var likes []liked
	for i, it := range interactions {
		if it.MediaType != "movie" {
			continue
		}
		if it.Kind == "rated" && it.Rating != nil && *it.Rating >= 70 {
			likes = append(likes, liked{it.MediaID, i})
		} else if it.Kind == "logged" {
			likes = append(likes, liked{it.MediaID, i})
		}
	}

	if len(likes) < 5 {
		return 0, 0, 0, 0, nil
	}

	scorer := reco.NewTier1(cfg)

	var hits10, hits50 int
	var mrrSum float64
	for _, hidden := range likes {
		held := append([]reco.InteractionRow{}, interactions[:hidden.IdxInArr]...)
		held = append(held, interactions[hidden.IdxInArr+1:]...)

		signal := reco.AggregateSignal(cfg, held)
		likedMap, dislikedMap := reco.SplitLikedDisliked(signal)

		ids := make([]int, 0, len(likedMap)+len(dislikedMap))
		for id := range likedMap {
			ids = append(ids, id)
		}
		for id := range dislikedMap {
			ids = append(ids, id)
		}
		feats, err := catalog.CandidatesByMediaIDs(ctx, ids)
		if err != nil {
			return 0, 0, 0, 0, err
		}
		featByID := map[int]*reco.MediaFeatures{}
		for _, c := range feats {
			if c.Features != nil {
				featByID[c.MediaID] = c.Features
			}
		}

		user := &reco.UserContext{UserID: userID}
		for id, w := range likedMap {
			user.Liked = append(user.Liked, reco.SignalItem{MediaID: id, Weight: w, Features: featByID[id]})
		}
		for id, w := range dislikedMap {
			user.Disliked = append(user.Disliked, reco.SignalItem{MediaID: id, Weight: w, Features: featByID[id]})
		}

		cands, err := catalog.SampleMovieCandidates(ctx, cfg.FeedSize*20)
		if err != nil {
			return 0, 0, 0, 0, err
		}
		seenIDs := map[int]bool{}
		for _, it := range held {
			seenIDs[it.MediaID] = true
		}
		filtered := cands[:0]
		for _, c := range cands {
			if !seenIDs[c.MediaID] {
				filtered = append(filtered, c)
			}
		}

		scored, err := scorer.Score(ctx, user, filtered)
		if err != nil {
			return 0, 0, 0, 0, err
		}
		sort.Slice(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
		ranked := reco.MMRReorder(scored, cfg.FeedSize, cfg.MMRLambda)

		rank := -1
		for i, s := range ranked {
			if s.MediaID == hidden.MediaID {
				rank = i + 1
				break
			}
		}
		if rank > 0 && rank <= 10 {
			hits10++
		}
		if rank > 0 && rank <= 50 {
			hits50++
		}
		if rank > 0 {
			mrrSum += 1.0 / float64(rank)
		}
	}

	trials = len(likes)
	recall10 = float64(hits10) / float64(trials)
	recall50 = float64(hits50) / float64(trials)
	mrr = mrrSum / float64(trials)
	return
}

// loadInteractions queries the interactions table for one user, computing
// the age_days projection the signal pipeline needs.
func loadInteractions(ctx context.Context, dbConn *sqlx.DB, userID uuid.UUID) ([]reco.InteractionRow, error) {
	var rows []struct {
		MediaID   int     `db:"media_id"`
		MediaType string  `db:"media_type"`
		Kind      string  `db:"kind"`
		Rating    *int    `db:"rating"`
		AgeDays   float64 `db:"age_days"`
	}
	err := dbConn.SelectContext(ctx, &rows, `
		select media_id, media_type, kind, rating,
		       extract(epoch from (now() - created_at)) / 86400.0 as age_days
		from interactions
		where user_id = $1
	`, userID)
	if err != nil {
		return nil, err
	}
	out := make([]reco.InteractionRow, len(rows))
	for i, r := range rows {
		out[i] = reco.InteractionRow{
			MediaID:   r.MediaID,
			MediaType: r.MediaType,
			Kind:      r.Kind,
			Rating:    r.Rating,
			AgeDays:   r.AgeDays,
		}
	}
	return out, nil
}

func printSampleFeed(ctx context.Context, dbConn *sqlx.DB, catalog *catalogAdapter, cfg reco.Config, userID uuid.UUID, k int) {
	interactions, err := loadInteractions(ctx, dbConn, userID)
	if err != nil {
		log.Println("load interactions:", err)
		return
	}
	scorer := reco.NewTier1(cfg)
	signal := reco.AggregateSignal(cfg, interactions)
	likedMap, _ := reco.SplitLikedDisliked(signal)
	if len(likedMap) == 0 {
		log.Println("no positive signal — sample feed would be tier 0 only")
		return
	}

	ids := make([]int, 0, len(likedMap))
	for id := range likedMap {
		ids = append(ids, id)
	}
	feats, _ := catalog.CandidatesByMediaIDs(ctx, ids)
	featByID := map[int]*reco.MediaFeatures{}
	for _, c := range feats {
		if c.Features != nil {
			featByID[c.MediaID] = c.Features
		}
	}
	user := &reco.UserContext{UserID: userID}
	for id, w := range likedMap {
		user.Liked = append(user.Liked, reco.SignalItem{MediaID: id, Weight: w, Features: featByID[id]})
	}

	cands, _ := catalog.SampleMovieCandidates(ctx, cfg.FeedSize*20)
	scored, _ := scorer.Score(ctx, user, cands)
	sort.Slice(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	ranked := reco.MMRReorder(scored, k, cfg.MMRLambda)

	for i, s := range ranked {
		title, _ := titleForMediaID(ctx, dbConn, s.MediaID)
		fmt.Printf("  %2d. media_id=%d score=%.3f  %s  reasons=%v\n", i+1, s.MediaID, s.Score, title, s.Reasons)
	}
}

func titleForMediaID(ctx context.Context, dbConn *sqlx.DB, mediaID int) (string, error) {
	var title sql.NullString
	err := dbConn.GetContext(ctx, &title, `
		select m.title from movies m
		join media_index mi on mi.id = m.movie_id and mi.media_type = 'movie'
		where mi.media_id = $1
	`, mediaID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if !title.Valid {
		return "", nil
	}
	return title.String, err
}

// seedSyntheticUsers creates N test users with random "liked" movie
// interactions pulled from the catalog. The numbers from this mode are
// only useful as plumbing smoke tests, not quality signal.
func seedSyntheticUsers(ctx context.Context, dbConn *sqlx.DB, catalog *catalogAdapter, n, likesEach int) ([]uuid.UUID, error) {
	cands, err := catalog.SampleMovieCandidates(ctx, 500)
	if err != nil {
		return nil, err
	}
	if len(cands) < likesEach {
		return nil, fmt.Errorf("only %d candidates available, need %d", len(cands), likesEach)
	}

	ids := make([]uuid.UUID, 0, n)
	for range n {
		userID := uuid.New()
		// Insert a users row so the FK on interactions holds.
		username := fmt.Sprintf("harness_%s", userID.String()[:8])
		if _, err := dbConn.ExecContext(ctx,
			`insert into users (id, username) values ($1, $2) on conflict (id) do nothing`,
			userID, username); err != nil {
			return nil, fmt.Errorf("create synthetic user: %w", err)
		}

		shuffled := append([]reco.Candidate{}, cands...)
		rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		for _, c := range shuffled[:likesEach] {
			rating := 70 + rand.Intn(31) // 70-100
			if _, err := dbConn.ExecContext(ctx, `
				insert into interactions (user_id, media_id, media_type, kind, rating)
				values ($1, $2, 'movie', 'rated', $3)
			`, userID, c.MediaID, rating); err != nil {
				return nil, fmt.Errorf("seed interaction: %w", err)
			}
		}
		ids = append(ids, userID)
	}
	return ids, nil
}
