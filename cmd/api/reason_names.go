package main

import (
	"context"
	"sync"
)

// nameLookup is the database side of reasonNames. Satisfied by
// *movies.Repository and *tv.Repository.
type nameLookup interface {
	LookupGenreNames(ctx context.Context, ids []int) (map[int]string, error)
	LookupKeywordNames(ctx context.Context, ids []int) (map[int]string, error)
}

// reasonNames remembers genre and keyword names so a feed only queries for
// ids it has never seen. TMDB never renames a feature id, and the set of ids
// is bounded by the catalog (about 19k keywords), so entries never expire.
type reasonNames struct {
	// sources are asked in order, each only for the ids the ones before it
	// could not name: the film cache first, then the series cache for the
	// genres and keywords only series carry.
	sources []nameLookup

	mu sync.RWMutex
	// An empty name marks an id no source names, so it is not queried
	// again.
	genres, keywords map[int]string
}

func newReasonNames(sources ...nameLookup) *reasonNames {
	return &reasonNames{sources: sources, genres: map[int]string{}, keywords: map[int]string{}}
}

// Lookup returns names for the ids it can resolve. Unknown ids are absent
// from the maps, never an error.
func (n *reasonNames) Lookup(ctx context.Context, genreIDs, keywordIDs []int) (map[int]string, map[int]string, error) {
	genres, err := n.resolve(ctx, n.genres, genreIDs, nameLookup.LookupGenreNames)
	if err != nil {
		return nil, nil, err
	}
	keywords, err := n.resolve(ctx, n.keywords, keywordIDs, nameLookup.LookupKeywordNames)
	if err != nil {
		return nil, nil, err
	}
	return genres, keywords, nil
}

func (n *reasonNames) resolve(ctx context.Context, known map[int]string, ids []int,
	query func(nameLookup, context.Context, []int) (map[int]string, error)) (map[int]string, error) {
	out := make(map[int]string, len(ids))
	var missing []int

	n.mu.RLock()
	for _, id := range ids {
		name, ok := known[id]
		switch {
		case !ok:
			missing = append(missing, id)
		case name != "":
			out[id] = name
		}
	}
	n.mu.RUnlock()

	if len(missing) == 0 {
		return out, nil
	}
	found := make(map[int]string, len(missing))
	ask := missing
	for _, src := range n.sources {
		if len(ask) == 0 {
			break
		}
		names, err := query(src, ctx, ask)
		if err != nil {
			return nil, err
		}
		var unnamed []int
		for _, id := range ask {
			if name := names[id]; name != "" {
				found[id] = name
			} else {
				unnamed = append(unnamed, id)
			}
		}
		ask = unnamed
	}

	n.mu.Lock()
	for _, id := range missing {
		known[id] = found[id]
		if name := found[id]; name != "" {
			out[id] = name
		}
	}
	n.mu.Unlock()
	return out, nil
}
