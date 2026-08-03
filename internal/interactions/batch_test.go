package interactions_test

import (
	"context"
	"testing"
	"time"

	"klyvi-api/internal/interactions"
)

// The batch endpoint exists to stop N POSTs per screenful of impressions.
// It is not all-or-nothing: a bad row is rejected and the good ones still
// land, because dropping a whole batch for one malformed impression would
// lose real signal.
func TestRecordBatch_MixedValidity(t *testing.T) {
	env := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	reqs := []interactions.RecordRequest{
		{TMDBID: 550, MediaType: "movie", Kind: "impression"},   // ok
		{TMDBID: 603, MediaType: "movie", Kind: "clicked"},      // ok
		{TMDBID: 0, MediaType: "movie", Kind: "impression"},     // bad tmdb_id
		{TMDBID: 680, MediaType: "movie", Kind: "not_a_kind"},   // bad kind
		{TMDBID: 13, MediaType: "movie", Kind: "rated", Rating: intPtr(88)}, // ok
	}

	res, err := env.service.RecordBatch(ctx, env.userID, reqs)
	if err != nil {
		t.Fatalf("RecordBatch: %v", err)
	}
	if res.Accepted != 3 {
		t.Errorf("accepted = %d, want 3", res.Accepted)
	}
	if res.Rejected != 2 {
		t.Errorf("rejected = %d, want 2", res.Rejected)
	}

	// The three good rows are actually persisted.
	rows, err := env.service.ListByUser(ctx, env.userID, 0)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("persisted rows = %d, want 3", len(rows))
	}
}

func TestRecordBatch_RejectsOversizeAndEmpty(t *testing.T) {
	env := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := env.service.RecordBatch(ctx, env.userID, nil); err == nil {
		t.Error("empty batch should be rejected")
	}

	over := make([]interactions.RecordRequest, interactions.MaxBatchSize+1)
	for i := range over {
		over[i] = interactions.RecordRequest{TMDBID: 550, MediaType: "movie", Kind: "impression"}
	}
	if _, err := env.service.RecordBatch(ctx, env.userID, over); err == nil {
		t.Errorf("batch over %d should be rejected", interactions.MaxBatchSize)
	}
}

func intPtr(v int) *int { return &v }
