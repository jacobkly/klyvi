package main

import "klyvi-api/internal/reco"

// likedUnit is one film or series a user liked: the thing a leave-one-out
// trial hides. A season's interactions count towards its series, as the
// signal rolls them up.
type likedUnit struct {
	MediaID   int
	MediaType string // "movie" or "tv"
}

// feed is the ?media= filter that holds the unit's type.
func (u likedUnit) feed() reco.FeedMedia {
	if u.MediaType == "tv" {
		return reco.FeedTV
	}
	return reco.FeedMovies
}

// likedUnits returns each film and series the user rated 70 or more or
// logged, once, in the order the rows first reach it. A season with no
// indexed series is skipped, as the signal skips it.
func likedUnits(rows []reco.InteractionRow) []likedUnit {
	var units []likedUnit
	seen := map[int]bool{}
	for _, it := range rows {
		id, mediaType := it.MediaID, it.MediaType
		switch it.MediaType {
		case "movie", "tv":
		case "season":
			if it.SeriesMediaID == 0 {
				continue
			}
			id, mediaType = it.SeriesMediaID, "tv"
		default:
			continue
		}
		liked := it.Kind == "logged" || (it.Kind == "rated" && it.Rating != nil && *it.Rating >= 70)
		if !liked || seen[id] {
			continue
		}
		seen[id] = true
		units = append(units, likedUnit{id, mediaType})
	}
	return units
}

// withoutUnit is the history with every row of the unit hidden, its seasons
// included. A row left behind would keep the unit in the seen set, and the
// trial could never recover it.
func withoutUnit(rows []reco.InteractionRow, u likedUnit) []reco.InteractionRow {
	held := make([]reco.InteractionRow, 0, len(rows))
	for _, it := range rows {
		if it.MediaID == u.MediaID || (it.MediaType == "season" && it.SeriesMediaID == u.MediaID) {
			continue
		}
		held = append(held, it)
	}
	return held
}

// seenIn is the set of media the rows touch, each season adding its series,
// as the API's seen set does.
func seenIn(rows []reco.InteractionRow) map[int]bool {
	seen := make(map[int]bool, len(rows))
	for _, it := range rows {
		seen[it.MediaID] = true
		if it.SeriesMediaID != 0 {
			seen[it.SeriesMediaID] = true
		}
	}
	return seen
}

// historyGroup names what a user's likes hold: films, shows, or both.
func historyGroup(units []likedUnit) string {
	var films, shows bool
	for _, u := range units {
		if u.MediaType == "tv" {
			shows = true
		} else {
			films = true
		}
	}
	switch {
	case films && shows:
		return "mixed"
	case shows:
		return "shows"
	}
	return "films"
}

// trialResult is one hidden film or show's rank in the mixed feed and in the
// feed of its own type, the ?media= filter, with 0 a miss.
type trialResult struct {
	hiddenType string
	mixedRank  int
	ownRank    int
}

// feedStats is a set of trials counted in the mixed feed and in each hidden
// item's own-type feed.
type feedStats struct {
	mixed, own looStats
}

func (s *feedStats) add(tr trialResult) {
	s.mixed.add(tr.mixedRank)
	s.own.add(tr.ownRank)
}

// looStats accumulates leave-one-out trials. A rank is the hidden item's
// 1-based place in the feed, and 0 is a miss.
type looStats struct {
	trials, hits10, hits50 int
	rrSum                  float64
}

func (s *looStats) add(rank int) {
	s.trials++
	if rank <= 0 {
		return
	}
	if rank <= 10 {
		s.hits10++
	}
	if rank <= 50 {
		s.hits50++
	}
	s.rrSum += 1 / float64(rank)
}

func (s *looStats) merge(o looStats) {
	s.trials += o.trials
	s.hits10 += o.hits10
	s.hits50 += o.hits50
	s.rrSum += o.rrSum
}

func (s looStats) ratio(n float64) float64 {
	if s.trials == 0 {
		return 0
	}
	return n / float64(s.trials)
}

func (s looStats) recall10() float64 { return s.ratio(float64(s.hits10)) }
func (s looStats) recall50() float64 { return s.ratio(float64(s.hits50)) }
func (s looStats) mrr() float64      { return s.ratio(s.rrSum) }

// crossMediaHits counts the shows in a feed and returns the 1-based ranks of
// the ones in related.
func crossMediaHits(ranked []reco.Scored, related map[int]bool) (shows int, ranks []int) {
	for i, s := range ranked {
		if s.MediaType != "tv" {
			continue
		}
		shows++
		if related[s.MediaID] {
			ranks = append(ranks, i+1)
		}
	}
	return shows, ranks
}
