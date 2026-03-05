package agent

import (
	"context"
	"fmt"
	"strings"
)

// MemoryVectorEmbeddingSettings configures how semantic vectors are generated for memory search.
//
// Default behavior is a fast local hashing embedder (no network). When Kind is set to
// "openai_compat", X-Claw will call an OpenAI-compatible embeddings endpoint.
type MemoryVectorEmbeddingSettings struct {
	Kind string

	APIKey  string
	APIBase string
	Model   string
	Proxy   string

	BatchSize             int
	RequestTimeoutSeconds int
}

func normalizeMemoryVectorEmbeddingSettings(s MemoryVectorEmbeddingSettings) MemoryVectorEmbeddingSettings {
	s.Kind = strings.ToLower(strings.TrimSpace(s.Kind))
	s.APIKey = strings.TrimSpace(s.APIKey)
	s.APIBase = strings.TrimRight(strings.TrimSpace(s.APIBase), "/")
	s.Model = strings.TrimSpace(s.Model)
	s.Proxy = strings.TrimSpace(s.Proxy)

	if s.BatchSize <= 0 {
		s.BatchSize = 64
	}
	if s.RequestTimeoutSeconds <= 0 {
		s.RequestTimeoutSeconds = 30
	}

	if s.Kind == "" {
		s.Kind = "hashed"
	}

	return s
}

type memoryVectorEmbedder interface {
	Kind() string
	// Signature returns a stable, non-secret identifier used for index fingerprinting.
	Signature() string
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
}

type hashedMemoryVectorEmbedder struct {
	dims int
}

func (e *hashedMemoryVectorEmbedder) Kind() string { return "hashed" }

func (e *hashedMemoryVectorEmbedder) Signature() string {
	return fmt.Sprintf("hashed:dims=%d", e.dims)
}

func (e *hashedMemoryVectorEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	_ = ctx
	if len(inputs) == 0 {
		return nil, nil
	}
	out := make([][]float32, 0, len(inputs))
	for _, input := range inputs {
		out = append(out, embedHashedText(input, e.dims))
	}
	return out, nil
}

type errorMemoryVectorEmbedder struct {
	kind string
	err  error
}

func (e *errorMemoryVectorEmbedder) Kind() string { return e.kind }

func (e *errorMemoryVectorEmbedder) Signature() string {
	if strings.TrimSpace(e.kind) == "" {
		return "error:unknown"
	}
	return "error:" + e.kind
}

func (e *errorMemoryVectorEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	_ = ctx
	_ = inputs
	if e.err != nil {
		return nil, e.err
	}
	return nil, fmt.Errorf("embedding unavailable")
}

func buildMemoryVectorEmbedder(settings MemoryVectorEmbeddingSettings, dims int) memoryVectorEmbedder {
	settings = normalizeMemoryVectorEmbeddingSettings(settings)

	switch settings.Kind {
	case "hashed":
		return &hashedMemoryVectorEmbedder{dims: dims}
	case "openai_compat":
		if settings.APIBase == "" || settings.Model == "" {
			return &errorMemoryVectorEmbedder{
				kind: settings.Kind,
				err:  fmt.Errorf("embedding config incomplete: api_base and model are required"),
			}
		}
		return newOpenAICompatMemoryVectorEmbedder(settings)
	default:
		return &errorMemoryVectorEmbedder{
			kind: settings.Kind,
			err:  fmt.Errorf("unknown embedding kind %q", settings.Kind),
		}
	}
}
