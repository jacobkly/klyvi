// Package embed turns English text into sentence vectors with a small local
// model, bge-small-en-v1.5 (MIT), run in pure Go through hugot. It reads the
// model from a directory and never calls out to a hosted service.
//
// Only the plots job uses it, so the API binary never loads the model.
package embed

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/pipelines"
)

// ModelID names the model and the exact revision the image ships. A stored
// vector carries it, so a model change re-embeds every title.
const ModelID = "bge-small-en-v1.5@5c38ec7"

// Dims is the length of every vector the model returns.
const Dims = 384

// Model is a loaded embedding model. It is not safe for concurrent use.
type Model struct {
	session  *hugot.Session
	pipeline *pipelines.FeatureExtractionPipeline
}

// Load reads the model from dir, which holds model.onnx, tokenizer.json and a
// config.json that sets max_position_embeddings, the token limit inputs are
// truncated to.
func Load(ctx context.Context, dir string) (*Model, error) {
	session, err := hugot.NewGoSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("embed session: %w", err)
	}
	p, err := hugot.NewPipeline(session, hugot.FeatureExtractionConfig{ModelPath: dir, Name: "plots"})
	if err != nil {
		_ = session.Destroy()
		return nil, fmt.Errorf("embed model %s: %w", dir, err)
	}
	return &Model{session: session, pipeline: p}, nil
}

// Embed returns the unit-length vector of text: the model's CLS token, which is
// the pooling bge models are trained for.
func (m *Model) Embed(ctx context.Context, text string) ([]float32, error) {
	out, err := m.pipeline.RunRaw(ctx, []string{text})
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	if len(out.HiddenStates) != 1 || len(out.HiddenStates[0]) == 0 || len(out.HiddenStates[0][0]) != Dims {
		return nil, errors.New("embed: unexpected model output shape")
	}
	return Normalize(out.HiddenStates[0][0]), nil
}

// Close frees the model.
func (m *Model) Close() error {
	return m.session.Destroy()
}

// Normalize returns v scaled to unit length, or v unchanged when it is all
// zero.
func Normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	out := make([]float32, len(v))
	if sum == 0 {
		copy(out, v)
		return out
	}
	n := math.Sqrt(sum)
	for i, x := range v {
		out[i] = float32(float64(x) / n)
	}
	return out
}
