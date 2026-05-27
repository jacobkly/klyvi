package reco

import "testing"

func TestMMRReorder_DiversifiesByGenre(t *testing.T) {
	// Three high-scoring items in the same genre + one slightly lower in a
	// different genre. With λ < 1, the different-genre item must appear in
	// the top two — proves MMR actually swaps in a more diverse pick over
	// a marginally higher score.
	action := func(id int, score float64) Scored {
		return Scored{
			Candidate: Candidate{
				MediaID:   id,
				MediaType: "movie",
				Features: &MediaFeatures{
					GenreIDs:   []int{28},      // action
					KeywordIDs: []int{1, 2, 3}, // shared keywords
				},
			},
			Score: score,
		}
	}
	comedy := func(id int, score float64) Scored {
		return Scored{
			Candidate: Candidate{
				MediaID:   id,
				MediaType: "movie",
				Features: &MediaFeatures{
					GenreIDs:   []int{35}, // comedy
					KeywordIDs: []int{10, 11, 12},
				},
			},
			Score: score,
		}
	}

	scored := []Scored{
		action(1, 0.9),
		action(2, 0.85),
		action(3, 0.8),
		comedy(4, 0.7), // lower score but very different
	}

	picked := MMRReorder(scored, 3, 0.5)
	if len(picked) != 3 {
		t.Fatalf("expected 3 picks, got %d", len(picked))
	}
	if picked[0].MediaID != 1 {
		t.Errorf("first pick should be top score; got id %d", picked[0].MediaID)
	}

	// At λ=0.5, the comedy at score 0.7 should outrank the action at 0.85
	// for slot 2 because its similarity to the picked action is ~0.
	if picked[1].MediaID != 4 {
		t.Errorf("MMR did not diversify: slot 2 = id %d, expected comedy (id 4)", picked[1].MediaID)
	}
}

func TestMMRReorder_LambdaOneEqualsRelevanceOrder(t *testing.T) {
	// λ=1 turns off the novelty term, so order must match raw score order.
	make := func(id int, score float64) Scored {
		return Scored{Candidate: Candidate{MediaID: id, Features: &MediaFeatures{}}, Score: score}
	}
	scored := []Scored{make(1, 0.9), make(2, 0.7), make(3, 0.5)}
	picked := MMRReorder(scored, 3, 1.0)
	if picked[0].MediaID != 1 || picked[1].MediaID != 2 || picked[2].MediaID != 3 {
		t.Errorf("λ=1.0 should preserve score order; got ids %d, %d, %d",
			picked[0].MediaID, picked[1].MediaID, picked[2].MediaID)
	}
}
