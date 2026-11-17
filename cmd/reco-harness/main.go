// reco-harness runs the leave-one-out evaluation harness against the
// recommender. For each user with sufficient signal it hides one liked film
// or show at a time, runs the cascade against the rest, and records whether
// the hidden item is recovered in the top-K (recall@K) and at what rank
// (MRR — mean reciprocal rank). Each trial ranks the mixed feed and the feed
// of the hidden item's own type (?media=movie or tv), and the report breaks
// the numbers down by what the user's likes hold: films, shows, or both.
//
// Three modes:
//
//	go run ./cmd/reco-harness                   # evaluates real users in the DB
//	go run ./cmd/reco-harness -synthetic-users 5  # seeds N synthetic users
//	                                              # first, then evaluates them
//	go run ./cmd/reco-harness -coherent-tastes  # seeds curated film, show and
//	                                            # mixed tastes, then evaluates
//	                                            # them and checks cross-media
//
// Under the aggregate it prints NDCG@10, coverage (the share of the catalog
// that ever reaches a top 10) and novelty (the picks' mean popularity place,
// 0 the most voted title) for the mixed, films and shows feeds.
// -report FILE also runs each scorer variant and writes a Markdown table.
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
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/joho/godotenv"

	"klyvi-api/config"
	"klyvi-api/internal/movies"
	"klyvi-api/internal/opendata"
	"klyvi-api/internal/platform/db"
	"klyvi-api/internal/reco"
	"klyvi-api/internal/tv"
)

func main() {
	syntheticUsers := flag.Int("synthetic-users", 0, "if >0, seed N synthetic users with random liked movies before evaluating")
	syntheticLikes := flag.Int("synthetic-likes", 15, "number of liked items per synthetic user")
	feedSize := flag.Int("feed-size", 50, "feed K used by the harness; recall@K/MRR computed up to K")
	maxTrials := flag.Int("max-trials", 60, "cap leave-one-out trials per user; larger histories are evenly subsampled (per-trial MMR is the cost)")
	cleanSynthetic := flag.Bool("clean-synthetic", false, "delete synthetic harness users (username like 'harness_%') and their interactions, then exit")
	coherent := flag.Bool("coherent-tastes", false, "seed curated coherent-taste users (films, shows and both) before evaluating")
	sampleMedia := flag.String("sample-media", "all", "the feed the sample top-10s show, as the ?media= filter: all, movie or tv")
	// Config overrides for recompile-free weight sweeps (<0 = keep default).
	kwWeight := flag.Float64("keyword-weight", -1, "override reco KeywordWeight")
	gWeight := flag.Float64("genre-weight", -1, "override reco GenreWeight")
	eWeight := flag.Float64("era-weight", -1, "override reco EraWeight")
	negWeight := flag.Float64("neg-weight", -1, "override reco NegativeSimWeight")
	qFloor := flag.Int("quality-floor", -1, "override reco QualityFloorVotes")
	tvShare := flag.Float64("tv-share", -1, "override reco TVShare, the pool share kept for series (0 is the films-only pool)")
	useIDF := flag.Bool("idf", true, "apply global IDF feature weighting (set -idf=false to A/B against the raw-sum baseline)")
	calibration := flag.Float64("calibration", -1, "override reco CalibrationWeight, the genre calibration cost (0 is off)")
	factWeight := flag.Float64("facts", -1, "override reco FactWeight, the Wikidata fact overlap term (0 is off)")
	crewWeight := flag.Float64("crew", -1, "override reco CrewWeight, the crew overlap term (0 is off)")
	report := flag.String("report", "", "also run each scorer variant and write a Markdown table of their numbers to this file")
	flag.Parse()
	sampleFeed, ok := reco.ParseFeedMedia(*sampleMedia)
	if !ok {
		log.Fatalf("-sample-media %q: want all, movie or tv", *sampleMedia)
	}

	if err := godotenv.Load(".env.dev"); err != nil {
		log.Printf("godotenv: %v (continuing — env may already be set)", err)
	}

	cfg := config.New()
	dbConn, err := db.New(cfg.DB)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer dbConn.Close()

	if *cleanSynthetic {
		n, err := deleteSyntheticUsers(context.Background(), dbConn)
		if err != nil {
			log.Fatalf("clean synthetic: %v", err)
		}
		log.Printf("deleted %d synthetic users", n)
		os.Exit(0)
	}

	movieRepo := movies.NewRepository(dbConn)
	recoCfg := reco.DefaultConfig()
	recoCfg.FeedSize = *feedSize
	if *kwWeight >= 0 {
		recoCfg.KeywordWeight = *kwWeight
	}
	if *gWeight >= 0 {
		recoCfg.GenreWeight = *gWeight
	}
	if *eWeight >= 0 {
		recoCfg.EraWeight = *eWeight
	}
	if *negWeight >= 0 {
		recoCfg.NegativeSimWeight = *negWeight
	}
	if *qFloor >= 0 {
		recoCfg.QualityFloorVotes = *qFloor
	}
	if *tvShare >= 0 {
		recoCfg.TVShare = *tvShare
	}
	if *calibration >= 0 {
		recoCfg.CalibrationWeight = *calibration
	}
	if *factWeight >= 0 {
		recoCfg.FactWeight = *factWeight
	}
	if *crewWeight >= 0 {
		recoCfg.CrewWeight = *crewWeight
	}
	log.Printf("config: keyword=%.2f genre=%.2f era=%.2f neg=%.2f quality_floor=%d tv_share=%.2f calibration=%.2f facts=%.2f crew=%.2f",
		recoCfg.KeywordWeight, recoCfg.GenreWeight, recoCfg.EraWeight,
		recoCfg.NegativeSimWeight, recoCfg.QualityFloorVotes, recoCfg.TVShare, recoCfg.CalibrationWeight,
		recoCfg.FactWeight, recoCfg.CrewWeight)

	// The Wikidata facts load whatever the weight, so -report can sweep it.
	// A weight of 0 leaves every score as it was.
	titleFacts, err := opendata.NewRepository(dbConn).RecoFacts(context.Background())
	if err != nil {
		log.Fatalf("load wikidata facts: %v", err)
	}
	facts := reco.NewFactIndex(titleFacts)
	log.Printf("facts: %d titles carry wikidata facts (run go run ./cmd/opendata if 0)", facts.Len())

	// The crew loads the same way, for the crew term.
	tvRepo := tv.NewRepository(dbConn)
	crew, err := reco.LoadCrewIndex(context.Background(), movieRepo, tvRepo)
	if err != nil {
		log.Fatalf("load crew: %v", err)
	}
	log.Printf("crew: %d titles carry directors, writers, composers or creators", crew.Len())

	catalog := newCatalogAdapter(movieRepo, tvRepo, facts, crew)

	// Diagnostics: the size of the eligible candidate universe. A small or
	// mainstream-only catalog caps recommendation quality regardless of the
	// scorer, so this number is the first thing to read alongside recall@K.
	if films, series, err := catalogSize(context.Background(), dbConn); err != nil {
		log.Printf("catalog size: %v", err)
	} else {
		log.Printf("catalog: %d eligible movies, %d eligible series (vote_count > 0)", films, series)
	}

	// Build the same global IDF tables the API loads, once, held fixed across
	// all users, trials, and weight configs — exactly what a fair comparison of
	// scorer weights needs. -idf=false runs the raw-sum baseline for A/B.
	var featureIDF *reco.MediaIDF
	if *useIDF {
		gdf, kdf, corpus, err := movieRepo.LoadFeatureStats(context.Background(), "movie")
		if err != nil {
			log.Fatalf("load feature stats: %v", err)
		}
		featureIDF = &reco.MediaIDF{Movie: reco.NewFeatureIDF(gdf, kdf, corpus)}
		if featureIDF.Movie == nil {
			log.Println("idf: feature_stats empty — running raw-sum (run seed-catalog -refresh-feature-stats)")
		} else {
			log.Printf("idf: active (corpus=%d genres=%d keywords=%d)", corpus, len(gdf), len(kdf))
		}
		tvGDF, tvKDF, tvCorpus, err := movieRepo.LoadFeatureStats(context.Background(), "tv")
		if err != nil {
			log.Fatalf("load tv feature stats: %v", err)
		}
		featureIDF.TV = reco.NewTVFeatureIDF(tvGDF, tvKDF, tvCorpus)
		if featureIDF.TV == nil {
			log.Println("idf: tv feature_stats empty, series score raw-sum")
		} else {
			log.Printf("idf: tv active (corpus=%d genres=%d keywords=%d)", tvCorpus, len(tvGDF), len(tvKDF))
		}
	} else {
		log.Println("idf: disabled by flag (raw-sum baseline)")
	}

	if *syntheticUsers > 0 {
		ids, err := seedSyntheticUsers(context.Background(), dbConn, catalog, *syntheticUsers, *syntheticLikes)
		if err != nil {
			log.Fatalf("seed synthetic: %v", err)
		}
		log.Printf("seeded %d synthetic users", len(ids))
	}

	var seeded []seededTaste
	if *coherent {
		seeded, err = seedCoherentTastes(context.Background(), dbConn)
		if err != nil {
			log.Fatalf("seed coherent tastes: %v", err)
		}
		log.Printf("seeded %d coherent-taste users", len(seeded))
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

	pop, err := loadPopularity(ctx, dbConn)
	if err != nil {
		log.Fatalf("%v", err)
	}

	base := evaluate(ctx, dbConn, catalog, featureIDF, recoCfg, users, *maxTrials, pop, true)
	total, breakdown := base.total, base.breakdown
	if total.trials == 0 {
		log.Println("no trials run")
		os.Exit(0)
	}
	fmt.Println("---")
	fmt.Printf("AGGREGATE: trials=%d recall@10=%.3f recall@50=%.3f MRR=%.3f\n",
		total.trials, total.recall10(), total.recall50(), total.mrr())

	// The same trials by what the user's likes hold and what was hidden, in
	// the mixed feed and in the hidden item's own-type feed. The gap between
	// the two is what mixing costs each type.
	fmt.Println("likes/hidden  trials | mixed: recall@10 recall@50   MRR | own-type: recall@10 recall@50   MRR")
	for _, key := range breakdownRows {
		s := breakdown[key]
		if s == nil {
			continue
		}
		fmt.Printf("%-12s %7d | %16.3f %9.3f %5.3f | %19.3f %9.3f %5.3f\n", key, s.mixed.trials,
			s.mixed.recall10(), s.mixed.recall50(), s.mixed.mrr(),
			s.own.recall10(), s.own.recall50(), s.own.mrr())
	}

	// NDCG@10, coverage and novelty for the mixed feed and each type's own
	// feed. Every trial ranks the feed's whole candidate pool, never a sample
	// of negatives, since sampled metrics can flip which method looks better.
	fmt.Println("---")
	printViews(os.Stdout, base.views, pop)

	if *report != "" {
		results := []variantResult{{"current", base.views}}
		for _, v := range reportVariants(recoCfg, featureIDF) {
			log.Printf("report: running variant %q", v.name)
			ev := evaluate(ctx, dbConn, catalog, v.idf, v.cfg, users, *maxTrials, pop, false)
			results = append(results, variantResult{v.name, ev.views})
		}
		if err := saveReport(*report, results, pop); err != nil {
			log.Fatalf("report: %v", err)
		}
		log.Printf("report: wrote %d variants to %s", len(results), *report)
	}

	if len(seeded) > 0 {
		printCrossMedia(ctx, dbConn, catalog, featureIDF, recoCfg, seeded)
	}

	// Sample feeds for every user give the human something to eyeball
	// alongside the numbers. Recommender quality is a judgment call, not a
	// pass/fail on recall@K alone: numbers AND sample outputs are the gate.
	// Synthetic users get fresh random ids each run, so printing only the
	// first would show a different taste from run to run.
	for _, uid := range users {
		fmt.Println("---")
		fmt.Printf("Sample top-10 %s feed for user %s %s\n", sampleFeed, uid.String()[:8], userLabel(ctx, dbConn, uid))
		printSampleFeed(ctx, dbConn, catalog, featureIDF, recoCfg.ForMedia(sampleFeed), uid, 10)
	}
}

// evaluation is one scorer variant's leave-one-out run over every user.
type evaluation struct {
	total     looStats // the mixed feed over every trial
	breakdown map[string]*feedStats
	views     views
}

// evaluate runs leave-one-out for every user under one config. verbose prints
// a line per user, as the main run does.
func evaluate(ctx context.Context, dbConn *sqlx.DB, catalog *catalogAdapter, idf *reco.MediaIDF, cfg reco.Config, users []uuid.UUID, maxTrials int, pop popularity, verbose bool) evaluation {
	ev := evaluation{breakdown: map[string]*feedStats{}, views: newViews()}
	for _, uid := range users {
		res, err := runUserLOO(ctx, dbConn, catalog, idf, cfg, uid, maxTrials)
		if err != nil {
			log.Printf("user %s LOO: %v", uid, err)
			continue
		}
		if len(res.trials) == 0 {
			continue
		}
		var user feedStats
		for _, tr := range res.trials {
			user.add(tr)
			ev.views.add(tr, pop)
			key := breakdownKey(res.group, tr.hiddenType)
			if ev.breakdown[key] == nil {
				ev.breakdown[key] = &feedStats{}
			}
			ev.breakdown[key].add(tr)
		}
		if verbose {
			fmt.Printf("user=%s %-38s trials=%d recall@10=%.3f recall@50=%.3f MRR=%.3f  own-type: recall@10=%.3f recall@50=%.3f MRR=%.3f\n",
				uid.String()[:8], userLabel(ctx, dbConn, uid)+" ("+res.group+")", user.mixed.trials,
				user.mixed.recall10(), user.mixed.recall50(), user.mixed.mrr(),
				user.own.recall10(), user.own.recall50(), user.own.mrr())
		}
		ev.total.merge(user.mixed)
	}
	return ev
}

// variant is one scorer setup the report runs beside the current one.
type variant struct {
	name string
	cfg  reco.Config
	idf  *reco.MediaIDF
}

// calibrationSweep is the calibration weights -report compares, 0 being off.
var calibrationSweep = []float64{0, 0.25, 0.5, 1, 2}

// factSweep is the Wikidata fact weights -report compares, 0 being off.
var factSweep = []float64{0, 0.2, 0.4, 0.8, 1.6, 3.2}

// crewSweep is the crew weights -report compares, 0 being off.
var crewSweep = []float64{0, 0.1, 0.2, 0.4, 0.8, 1.6, 3.2}

// reportVariants are the setups -report runs beside the current config. Each
// later scoring change adds its own on and off pair here.
func reportVariants(cfg reco.Config, idf *reco.MediaIDF) []variant {
	var out []variant
	if idf != nil {
		out = append(out, variant{"no IDF", cfg, nil})
	}
	noMMR := cfg
	noMMR.MMRLambda = 1
	out = append(out, variant{"no MMR", noMMR, idf})
	for _, w := range calibrationSweep {
		if w == cfg.CalibrationWeight {
			continue
		}
		cal := cfg
		cal.CalibrationWeight = w
		out = append(out, variant{fmt.Sprintf("calibration %.2f", w), cal, idf})
	}
	for _, w := range factSweep {
		if w == cfg.FactWeight {
			continue
		}
		f := cfg
		f.FactWeight = w
		out = append(out, variant{fmt.Sprintf("facts %.2f", w), f, idf})
	}
	for _, w := range crewSweep {
		if w == cfg.CrewWeight {
			continue
		}
		c := cfg
		c.CrewWeight = w
		out = append(out, variant{fmt.Sprintf("crew %.2f", w), c, idf})
	}
	return out
}

func saveReport(path string, results []variantResult, pop popularity) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := writeReport(f, results, pop); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// breakdownRows are the per-media aggregate rows in print order: what the
// user's likes hold, then the hidden item's type.
var breakdownRows = []string{"films/film", "shows/show", "mixed/film", "mixed/show"}

func breakdownKey(group, hiddenType string) string {
	if hiddenType == "tv" {
		return group + "/show"
	}
	return group + "/film"
}

// userLabel is the user's username, which names a harness fixture, or ""
// when none is set.
func userLabel(ctx context.Context, dbConn *sqlx.DB, userID uuid.UUID) string {
	var name sql.NullString
	if err := dbConn.GetContext(ctx, &name, `select username from users where id = $1`, userID); err != nil {
		return ""
	}
	return name.String
}

// catalogSize counts the eligible candidate universe — the filters
// ListCandidatesForReco applies to films and to series — so the harness can
// report how many titles the recommender actually has to choose from.
func catalogSize(ctx context.Context, dbConn *sqlx.DB) (films, series int, err error) {
	err = dbConn.QueryRowxContext(ctx, `
		select
			(select count(*) from movies m
			 join media_index mi on mi.id = m.movie_id and mi.media_type = 'movie'
			 where m.vote_count > 0),
			(select count(*) from tv_series s
			 join media_index mi on mi.id = s.tv_id and mi.media_type = 'tv'
			 where s.vote_count > 0)
	`).Scan(&films, &series)
	return films, series, err
}

// deleteSyntheticUsers removes users seeded by -synthetic-users (username
// prefix "harness_") and their interactions, so they don't pollute a real
// evaluation. Interactions are deleted first in case the FK has no cascade.
// The underscore in the prefix is escaped so LIKE treats it literally.
func deleteSyntheticUsers(ctx context.Context, dbConn *sqlx.DB) (int64, error) {
	if _, err := dbConn.ExecContext(ctx, `
		delete from interactions
		where user_id in (select id from users where username like 'harness\_%')
	`); err != nil {
		return 0, err
	}
	res, err := dbConn.ExecContext(ctx, `delete from users where username like 'harness\_%'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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

// userLOO is one user's leave-one-out run: what their likes hold, and where
// each hidden film or show came back.
type userLOO struct {
	group  string
	trials []trialResult
}

// runUserLOO runs leave-one-out for one user. For each film or series the
// user liked (rated >=70 or logged), it hides every row of it, ranks the mixed
// feed and the feed of the hidden item's own type against the rest, and
// records where the hidden item came back in each.
func runUserLOO(ctx context.Context, dbConn *sqlx.DB, catalog *catalogAdapter, idf *reco.MediaIDF, cfg reco.Config, userID uuid.UUID, maxTrials int) (userLOO, error) {
	interactions, err := loadInteractions(ctx, dbConn, userID)
	if err != nil {
		return userLOO{}, err
	}
	likes := likedUnits(interactions)
	if len(likes) < 5 {
		return userLOO{}, nil
	}
	res := userLOO{group: historyGroup(likes)}

	// Cap trials for large histories: each trial runs an MMR rerank (O(k²·n)),
	// so a several-hundred-film taste is intractable in full. An even sample
	// gives a fair recall@K estimate; the candidate pools below still use the
	// full taste.
	if maxTrials > 0 && len(likes) > maxTrials {
		step := len(likes) / maxTrials
		sampled := make([]likedUnit, 0, maxTrials)
		for i := 0; i < len(likes) && len(sampled) < maxTrials; i += step {
			sampled = append(sampled, likes[i])
		}
		likes = sampled
	}

	scorer := reco.NewTier1(cfg).WithIDF(idf).WithFacts(catalog.facts).WithCrew(catalog.crew)

	// Preload features for everything in the signal, and build each feed's
	// candidate pool once from the full taste. Both are (near-)constant across
	// the leave-one-out trials, so rebuilding them per trial just multiplies DB
	// round-trips. Holding the pools fixed across trials — and across weight
	// configs — is exactly what a fair comparison of scorer weights needs.
	fullSignal := reco.AggregateSignal(cfg, interactions)
	history, err := signalHistory(ctx, catalog, fullSignal)
	if err != nil {
		return userLOO{}, err
	}
	featByID := featuresOf(history)
	fullLiked, _ := reco.SplitLikedDisliked(fullSignal)
	feeds := map[reco.FeedMedia]reco.Config{reco.FeedAll: cfg}
	for _, u := range likes {
		feeds[u.feed()] = cfg.ForMedia(u.feed())
	}
	pools := make(map[reco.FeedMedia][]reco.Candidate, len(feeds))
	for media, feedCfg := range feeds {
		if pools[media], err = reco.BuildCandidatePool(ctx, catalog, signalItems(fullLiked, featByID), feedCfg); err != nil {
			return userLOO{}, err
		}
	}

	for _, hidden := range likes {
		held := withoutUnit(interactions, hidden)
		liked, disliked := reco.SplitLikedDisliked(reco.AggregateSignal(cfg, held))
		user := &reco.UserContext{
			UserID:   userID,
			Liked:    signalItems(liked, featByID),
			Disliked: signalItems(disliked, featByID),
		}

		// Filter held-in items from the fixed pools; the hidden item stays
		// and must be recovered by scoring alone.
		seen := seenIn(held)
		own := hidden.feed()
		tr := trialResult{hiddenType: hidden.MediaType}
		if tr.mixedRank, tr.mixedTop, tr.mixedCalib, err = rankOf(ctx, scorer, feeds[reco.FeedAll], user, pools[reco.FeedAll], seen, hidden.MediaID); err != nil {
			return userLOO{}, err
		}
		if tr.ownRank, tr.ownTop, tr.ownCalib, err = rankOf(ctx, scorer, feeds[own], user, pools[own], seen, hidden.MediaID); err != nil {
			return userLOO{}, err
		}
		res.trials = append(res.trials, tr)
	}
	return res, nil
}

// rankOf is target's 1-based rank in the feed cfg ranks from the pool, or 0
// when it misses the feed, the media ids of the feed's top 10, and how far
// the top 10's genre mix sits from the mix of the user's likes.
func rankOf(ctx context.Context, scorer *reco.Tier1, cfg reco.Config, user *reco.UserContext, pool []reco.Candidate, seen map[int]bool, target int) (int, []int, float64, error) {
	ranked, err := rankPool(ctx, scorer, cfg, user, pool, seen, cfg.FeedSize)
	if err != nil {
		return 0, nil, 0, err
	}
	calib := reco.CalibrationError(reco.GenreTarget(user.Liked), ranked[:min(10, len(ranked))])
	top := make([]int, 0, 10)
	rank := 0
	for i, s := range ranked {
		if i < 10 {
			top = append(top, s.MediaID)
		}
		if s.MediaID == target && rank == 0 {
			rank = i + 1
		}
	}
	return rank, top, calib, nil
}

// rankPool ranks the pool's unseen candidates as the orchestrator does: score,
// sort, then the MMR rerank that holds films and series to their shares of
// the feed and, with calibration on, the genre mix near the likes, down to
// depth.
func rankPool(ctx context.Context, scorer *reco.Tier1, cfg reco.Config, user *reco.UserContext, pool []reco.Candidate, seen map[int]bool, depth int) ([]reco.Scored, error) {
	unseen := make([]reco.Candidate, 0, len(pool))
	for _, c := range pool {
		if !seen[c.MediaID] {
			unseen = append(unseen, c)
		}
	}
	scored, err := scorer.Score(ctx, user, unseen)
	if err != nil {
		return nil, err
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	return reco.MMRReorderCalibrated(scored, depth, cfg.MMRLambda, reco.FeedTypeBound(cfg, user.Liked), reco.FeedCalibration(cfg, user.Liked)), nil
}

// signalHistory looks up the catalog row of every film and series in the
// signal, keyed by media id.
func signalHistory(ctx context.Context, catalog *catalogAdapter, signal map[int]float64) (map[int]reco.Candidate, error) {
	history := map[int]reco.Candidate{}
	if len(signal) == 0 {
		return history, nil
	}
	ids := make([]int, 0, len(signal))
	for id := range signal {
		ids = append(ids, id)
	}
	cands, err := catalog.CandidatesByMediaIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, c := range cands {
		history[c.MediaID] = c
	}
	return history, nil
}

// featuresOf keys the features of a history's titles by media id, leaving out
// titles without features.
func featuresOf(history map[int]reco.Candidate) map[int]*reco.MediaFeatures {
	feats := make(map[int]*reco.MediaFeatures, len(history))
	for id, c := range history {
		if c.Features != nil {
			feats[id] = c.Features
		}
	}
	return feats
}

// signalItems pairs each weight with its features, nil when they did not
// resolve, as the orchestrator's user context does.
func signalItems(weights map[int]float64, feats map[int]*reco.MediaFeatures) []reco.SignalItem {
	items := make([]reco.SignalItem, 0, len(weights))
	for id, w := range weights {
		items = append(items, reco.SignalItem{MediaID: id, Weight: w, Features: feats[id]})
	}
	return items
}

// loadInteractions queries the interactions table for one user, computing
// the age_days projection the signal pipeline needs and each season's series,
// as the API's signal adapter does.
func loadInteractions(ctx context.Context, dbConn *sqlx.DB, userID uuid.UUID) ([]reco.InteractionRow, error) {
	var rows []struct {
		MediaID       int     `db:"media_id"`
		MediaType     string  `db:"media_type"`
		Kind          string  `db:"kind"`
		Rating        *int    `db:"rating"`
		AgeDays       float64 `db:"age_days"`
		SeriesMediaID int     `db:"series_media_id"`
	}
	err := dbConn.SelectContext(ctx, &rows, `
		select i.media_id, i.media_type, i.kind, i.rating,
		       extract(epoch from (now() - i.created_at)) / 86400.0 as age_days,
		       coalesce(series.media_id, 0) as series_media_id
		from interactions i
		left join media_index season on season.media_id = i.media_id and season.media_type = 'season'
		left join media_index series on series.id = season.id and series.media_type = 'tv'
		where i.user_id = $1
		order by i.created_at desc, i.id
	`, userID)
	if err != nil {
		return nil, err
	}
	out := make([]reco.InteractionRow, len(rows))
	for i, r := range rows {
		out[i] = reco.InteractionRow{
			MediaID:       r.MediaID,
			MediaType:     r.MediaType,
			Kind:          r.Kind,
			Rating:        r.Rating,
			AgeDays:       r.AgeDays,
			SeriesMediaID: r.SeriesMediaID,
		}
	}
	return out, nil
}

// seenMediaIDs unions the user's interactions + media_list rows, each season
// adding its series — the same "already-touched" set the live orchestrator's
// seenAdapter filters on. The sample feed applies it so the printed feed
// matches what /v1/reco/feed would actually serve (which never recommends
// something the user has engaged with), rather than padding the list with the
// user's own logged films.
func seenMediaIDs(ctx context.Context, dbConn *sqlx.DB, userID uuid.UUID) (map[int]bool, error) {
	var ids []int
	err := dbConn.SelectContext(ctx, &ids, `
		with touched as (
			select media_id from interactions where user_id = $1
			union
			select media_id from media_list where user_id = $1
		)
		select media_id from touched
		union
		select series.media_id
		from touched t
		join media_index season on season.media_id = t.media_id and season.media_type = 'season'
		join media_index series on series.id = season.id and series.media_type = 'tv'
	`, userID)
	if err != nil {
		return nil, err
	}
	out := make(map[int]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// feedDiag is how much of a user's signal resolved to features and how big
// the candidate pool was before and after the seen filter.
type feedDiag struct {
	liked, resolved int
	pool, unseen    int
	series          int // unseen series in the pool
}

// liveFeed ranks the user's mixed feed from their whole history, as
// /v1/reco/feed would serve it, down to depth. A user with no positive signal
// gets no feed.
func liveFeed(ctx context.Context, dbConn *sqlx.DB, catalog *catalogAdapter, idf *reco.MediaIDF, cfg reco.Config, userID uuid.UUID, depth int) ([]reco.Scored, feedDiag, error) {
	var diag feedDiag
	interactions, err := loadInteractions(ctx, dbConn, userID)
	if err != nil {
		return nil, diag, fmt.Errorf("load interactions: %w", err)
	}
	signal := reco.AggregateSignal(cfg, interactions)
	history, err := signalHistory(ctx, catalog, signal)
	featByID := featuresOf(history)
	if err != nil {
		return nil, diag, fmt.Errorf("signal features: %w", err)
	}
	liked, disliked := reco.SplitLikedDisliked(signal)
	user := &reco.UserContext{
		UserID:   userID,
		Liked:    signalItems(liked, featByID),
		Disliked: signalItems(disliked, featByID),
	}
	diag.liked = len(user.Liked)
	for _, li := range user.Liked {
		if li.Features != nil {
			diag.resolved++
		}
	}
	if diag.liked == 0 {
		return nil, diag, nil
	}

	pool, err := reco.BuildCandidatePool(ctx, catalog, user.Liked, cfg)
	if err != nil {
		return nil, diag, fmt.Errorf("candidate pool: %w", err)
	}
	// Filter already-seen candidates, exactly as the live orchestrator does,
	// so the feed is what the user would actually be served — new titles —
	// not their own back-catalog re-scored.
	seen, err := seenMediaIDs(ctx, dbConn, userID)
	if err != nil {
		return nil, diag, fmt.Errorf("seen media ids: %w", err)
	}
	diag.pool = len(pool)
	for _, c := range pool {
		if seen[c.MediaID] {
			continue
		}
		diag.unseen++
		if c.MediaType == "tv" {
			diag.series++
		}
	}

	ranked, err := rankPool(ctx, reco.NewTier1(cfg).WithIDF(idf).WithFacts(catalog.facts).WithCrew(catalog.crew), cfg, user, pool, seen, depth)
	if err != nil {
		return nil, diag, err
	}
	// The live feed names reasons with IDF whether or not it scores with it.
	reco.AddBecauseReasons(ranked, reco.Anchors(interactions, user.Liked, history), idf)
	return ranked, diag, nil
}

func printSampleFeed(ctx context.Context, dbConn *sqlx.DB, catalog *catalogAdapter, idf *reco.MediaIDF, cfg reco.Config, userID uuid.UUID, k int) {
	ranked, diag, err := liveFeed(ctx, dbConn, catalog, idf, cfg, userID, k)
	if err != nil {
		log.Println("sample feed:", err)
		return
	}
	if diag.liked == 0 {
		log.Println("no positive signal — sample feed would be tier 0 only")
		return
	}

	// Diagnostics: how much signal actually resolved to features, and how big
	// the candidate pool is. Low feature-resolution means the user's liked
	// items aren't in the cache (weak signal); a small pool means the feed is
	// scored against too few titles to feel personal.
	fmt.Printf("  [diag] liked=%d features_resolved=%d candidate_pool=%d (seen-filtered from %d, %d series)\n",
		diag.liked, diag.resolved, diag.unseen, diag.pool, diag.series)

	for i, s := range ranked {
		fmt.Printf("  %2d. media_id=%d score=%.3f  %s  reasons=%v\n", i+1, s.MediaID, s.Score, displayTitle(ctx, dbConn, s), s.Reasons)
	}
}

// printCrossMedia checks that a films-only taste reaches the shows its fans
// would likely watch: how many of the related shows-only fixture's shows land
// in the taste's live mixed feed, and which shows the feed does hold, for the
// human to judge.
func printCrossMedia(ctx context.Context, dbConn *sqlx.DB, catalog *catalogAdapter, idf *reco.MediaIDF, cfg reco.Config, seeded []seededTaste) {
	showsOf := map[string][]int{}
	for _, s := range seeded {
		showsOf[s.taste.name] = s.shows
	}
	fmt.Println("---")
	fmt.Printf("Cross-media: each films-only taste's mixed top-%d against its related shows-only taste\n", cfg.FeedSize)
	for _, s := range seeded {
		related := showsOf[s.taste.related]
		if len(s.taste.shows) > 0 || len(related) == 0 {
			continue
		}
		ranked, _, err := liveFeed(ctx, dbConn, catalog, idf, cfg, s.userID, cfg.FeedSize)
		if err != nil {
			log.Printf("cross-media %s: %v", s.taste.name, err)
			continue
		}
		relatedSet := make(map[int]bool, len(related))
		for _, id := range related {
			relatedSet[id] = true
		}
		shows, ranks := crossMediaHits(ranked, relatedSet)
		fmt.Printf("taste %-15s %2d shows, %d of %d %s shows (recall@%d=%.3f)\n",
			s.taste.name, shows, len(ranks), len(related), s.taste.related,
			cfg.FeedSize, float64(len(ranks))/float64(len(related)))
		fmt.Println("  related:  ", rankedTitles(ctx, dbConn, ranked, ranks))

		var showRanks []int
		for i, it := range ranked {
			if it.MediaType == "tv" && len(showRanks) < 8 {
				showRanks = append(showRanks, i+1)
			}
		}
		fmt.Println("  top shows:", rankedTitles(ctx, dbConn, ranked, showRanks))
	}
}

// rankedTitles lists the feed's items at the 1-based ranks as "#3 Title", or
// "none".
func rankedTitles(ctx context.Context, dbConn *sqlx.DB, ranked []reco.Scored, ranks []int) string {
	if len(ranks) == 0 {
		return "none"
	}
	parts := make([]string, len(ranks))
	for i, r := range ranks {
		title, _ := titleForMediaID(ctx, dbConn, ranked[r-1].MediaID)
		parts[i] = fmt.Sprintf("#%d %s", r, title)
	}
	return strings.Join(parts, ", ")
}

// displayTitle names a feed item, marking a series.
func displayTitle(ctx context.Context, dbConn *sqlx.DB, s reco.Scored) string {
	title, _ := titleForMediaID(ctx, dbConn, s.MediaID)
	if s.MediaType == "tv" {
		title += " (tv)"
	}
	return title
}

// titleForMediaID names a film by its title or a series by its name.
func titleForMediaID(ctx context.Context, dbConn *sqlx.DB, mediaID int) (string, error) {
	var title sql.NullString
	err := dbConn.GetContext(ctx, &title, `
		select coalesce(m.title, s.name) from media_index mi
		left join movies m on mi.media_type = 'movie' and m.movie_id = mi.id
		left join tv_series s on mi.media_type = 'tv' and s.tv_id = mi.id
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
