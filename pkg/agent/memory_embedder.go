package agent

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
)

func buildMemoryVectorID(parts ...string) string {
	h := sha1.New()
	for _, part := range parts {
		if part == "" {
			continue
		}
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func embedHashedText(text string, dims int) []float32 {
	if dims <= 0 {
		return nil
	}

	tokens := tokenizeForEmbedding(text)
	if len(tokens) == 0 {
		return nil
	}

	tf := make(map[string]int, len(tokens))
	for _, token := range tokens {
		tf[token]++
	}

	vec := make([]float64, dims)
	for token, count := range tf {
		h := fnv.New32a()
		_, _ = h.Write([]byte(token))
		sum := h.Sum32()

		index := int(sum % uint32(dims))
		sign := 1.0
		if (sum>>31)&1 == 1 {
			sign = -1
		}

		weight := 1.0 + math.Log(float64(count))
		vec[index] += sign * weight
	}

	norm := 0.0
	for _, v := range vec {
		norm += v * v
	}
	if norm == 0 {
		return nil
	}
	norm = math.Sqrt(norm)

	out := make([]float32, dims)
	for i, v := range vec {
		out[i] = float32(v / norm)
	}
	return out
}

func tokenizeForEmbedding(text string) []string {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return nil
	}

	tokens := make([]string, 0, 32)
	var current []rune
	flush := func() {
		if len(current) == 0 {
			return
		}
		tokens = append(tokens, string(current))
		current = current[:0]
	}

	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current = append(current, r)
			continue
		}
		flush()
	}
	flush()

	if len(tokens) > 0 {
		expanded := make([]string, 0, len(tokens)*3)
		for _, token := range tokens {
			expanded = append(expanded, token)
			runes := []rune(token)
			if len(runes) < 4 {
				continue
			}
			for i := 0; i+3 <= len(runes); i++ {
				expanded = append(expanded, string(runes[i:i+3]))
			}
		}
		return expanded
	}

	// For very short non-word strings, fall back to rune-level tokens.
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		tokens = append(tokens, string(r))
	}
	return tokens
}

func cosineSimilarity(a, b []float32) float64 {
	n := minInt(len(a), len(b))
	if n == 0 {
		return 0
	}

	dot := 0.0
	normA := 0.0
	normB := 0.0
	for i := 0; i < n; i++ {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}

	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / math.Sqrt(normA*normB)
}

func compactWhitespace(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func uniqueTokenSet(tokens []string) map[string]struct{} {
	if len(tokens) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		if token == "" {
			continue
		}
		out[token] = struct{}{}
	}
	return out
}

func lexicalSimilarity(queryTokens map[string]struct{}, docTokens []string) float64 {
	if len(queryTokens) == 0 || len(docTokens) == 0 {
		return 0
	}

	docSet := uniqueTokenSet(docTokens)
	if len(docSet) == 0 {
		return 0
	}

	overlap := 0
	for token := range queryTokens {
		if _, ok := docSet[token]; ok {
			overlap++
		}
	}
	if overlap == 0 {
		return 0
	}

	denom := len(queryTokens)
	if denom == 0 {
		return 0
	}
	return float64(overlap) / float64(denom)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

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

type openAICompatMemoryVectorEmbedder struct {
	apiKey    string
	apiBase   string
	model     string
	batchSize int
	timeout   time.Duration
	client    *http.Client
}

func newOpenAICompatMemoryVectorEmbedder(settings MemoryVectorEmbeddingSettings) memoryVectorEmbedder {
	settings = normalizeMemoryVectorEmbeddingSettings(settings)

	timeout := time.Duration(settings.RequestTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	client := &http.Client{Timeout: timeout}
	if settings.Proxy != "" {
		if parsed, err := url.Parse(settings.Proxy); err == nil {
			client.Transport = &http.Transport{
				Proxy: http.ProxyURL(parsed),
			}
		}
	}

	return &openAICompatMemoryVectorEmbedder{
		apiKey:    settings.APIKey,
		apiBase:   strings.TrimRight(settings.APIBase, "/"),
		model:     settings.Model,
		batchSize: settings.BatchSize,
		timeout:   timeout,
		client:    client,
	}
}

func (e *openAICompatMemoryVectorEmbedder) Kind() string { return "openai_compat" }

func (e *openAICompatMemoryVectorEmbedder) Signature() string {
	base := strings.TrimRight(strings.TrimSpace(e.apiBase), "/")
	model := strings.TrimSpace(e.model)
	return fmt.Sprintf("openai_compat:model=%s;base=%s", model, base)
}

func (e *openAICompatMemoryVectorEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	if e == nil || e.client == nil {
		return nil, fmt.Errorf("embedding client not configured")
	}
	if strings.TrimSpace(e.apiBase) == "" {
		return nil, fmt.Errorf("embedding api_base not configured")
	}
	if strings.TrimSpace(e.model) == "" {
		return nil, fmt.Errorf("embedding model not configured")
	}

	batchSize := e.batchSize
	if batchSize <= 0 {
		batchSize = 64
	}

	out := make([][]float32, 0, len(inputs))
	for start := 0; start < len(inputs); start += batchSize {
		end := start + batchSize
		if end > len(inputs) {
			end = len(inputs)
		}

		vecs, err := e.embedBatch(ctx, inputs[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (e *openAICompatMemoryVectorEmbedder) embedBatch(ctx context.Context, inputs []string) ([][]float32, error) {
	type embeddingRequest struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	reqBody := embeddingRequest{
		Model: e.model,
		Input: inputs,
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal embeddings request: %w", err)
	}

	if _, ok := ctx.Deadline(); !ok && e.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.timeout)
		defer cancel()
	}

	endpoint := strings.TrimRight(e.apiBase, "/") + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("create embeddings request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(e.apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(e.apiKey))
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embeddings request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read embeddings response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := string(body)
		if len(msg) > 1200 {
			msg = msg[:1200] + "... (truncated)"
		}
		return nil, fmt.Errorf("embeddings API error: status=%d body=%s", resp.StatusCode, msg)
	}

	type embeddingItem struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	}
	var parsed struct {
		Data []embeddingItem `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal embeddings response: %w", err)
	}
	if len(parsed.Data) == 0 {
		return nil, fmt.Errorf("embeddings response missing data")
	}

	// Some providers may return items out of order. Preserve original input order via index.
	items := make([]embeddingItem, 0, len(parsed.Data))
	for _, item := range parsed.Data {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Index < items[j].Index })

	vecs := make([][]float32, len(inputs))
	for _, item := range items {
		if item.Index < 0 || item.Index >= len(vecs) {
			continue
		}
		if len(item.Embedding) == 0 {
			continue
		}
		out := make([]float32, len(item.Embedding))
		for i := range item.Embedding {
			out[i] = float32(item.Embedding[i])
		}
		vecs[item.Index] = out
	}

	for i := range vecs {
		if len(vecs[i]) == 0 {
			return nil, fmt.Errorf("embeddings response missing item for index %d", i)
		}
	}

	return vecs, nil
}
