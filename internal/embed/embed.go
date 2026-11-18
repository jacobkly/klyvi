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

// reloadEvery is how many texts a loaded model embeds before it is loaded
// again. hugot's pure Go backend keeps about 0.4 MiB after every text, which
// only a new session frees. A full catalog run would otherwise pass the
// container's 512 MB. A reload takes about 0.3 s.
const reloadEvery = 50

// Model is a loaded embedding model. It is not safe for concurrent use.
type Model struct {
	dir      string
	session  *hugot.Session
	pipeline *pipelines.FeatureExtractionPipeline
	runs     int
}

// Load reads the model from dir, which holds model.onnx, tokenizer.json and a
// config.json that sets max_position_embeddings, the token limit inputs are
// truncated to.
func Load(ctx context.Context, dir string) (*Model, error) {
	m := &Model{dir: dir}
	if err := m.load(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Model) load(ctx context.Context) error {
	dir := m.dir
	session, err := hugot.NewGoSession(ctx)
	if err != nil {
		return fmt.Errorf("embed session: %w", err)
	}
	p, err := hugot.NewPipeline(session, hugot.FeatureExtractionConfig{ModelPath: dir, Name: "plots"})
	if err != nil {
		_ = session.Destroy()
		return fmt.Errorf("embed model %s: %w", dir, err)
	}
	m.session, m.pipeline, m.runs = session, p, 0
	return nil
}

// Embed returns the unit-length vector of text: the model's CLS token, which is
// the pooling bge models are trained for.
func (m *Model) Embed(ctx context.Context, text string) ([]float32, error) {
	if m.runs >= reloadEvery || m.session == nil {
		if m.session != nil {
			err := m.session.Destroy()
			m.session = nil
			if err != nil {
				return nil, fmt.Errorf("embed reload: %w", err)
			}
		}
		if err := m.load(ctx); err != nil {
			return nil, fmt.Errorf("embed reload: %w", err)
		}
	}
	m.runs++
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
	if m.session == nil {
		return nil
	}
	err := m.session.Destroy()
	m.session = nil
	return err
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
