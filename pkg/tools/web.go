package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/sipeed/picoclaw/pkg/utils"
)

const (
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

// Pre-compiled regexes for HTML text extraction
var (
	reScript     = regexp.MustCompile(`<script[\s\S]*?</script>`)
	reStyle      = regexp.MustCompile(`<style[\s\S]*?</style>`)
	reTags       = regexp.MustCompile(`<[^>]+>`)
	reWhitespace = regexp.MustCompile(`[^\S\n]+`)
	reBlankLines = regexp.MustCompile(`\n{3,}`)
	reNoScript   = regexp.MustCompile(`(?is)<noscript[\s\S]*?</noscript>`)
	reLayoutTags = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<nav[^>]*>[\s\S]*?</nav>`),
		regexp.MustCompile(`(?is)<header[^>]*>[\s\S]*?</header>`),
		regexp.MustCompile(`(?is)<footer[^>]*>[\s\S]*?</footer>`),
		regexp.MustCompile(`(?is)<aside[^>]*>[\s\S]*?</aside>`),
	}
	reBR         = regexp.MustCompile(`(?i)<br\s*/?>`)
	reCloseBlock = regexp.MustCompile(`(?i)</(p|div|h[1-6]|li|tr|td|th|section|article|header|footer|nav|aside)>`)
	reURL        = regexp.MustCompile(`https?://[^\s<>"')]+`)

	// DuckDuckGo result extraction
	reDDGLink    = regexp.MustCompile(`<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]+)"[^>]*>([\s\S]*?)</a>`)
	reDDGSnippet = regexp.MustCompile(`<a class="result__snippet[^"]*".*?>([\s\S]*?)</a>`)
)

// createHTTPClient creates an HTTP client with optional proxy support
func createHTTPClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			IdleConnTimeout:     30 * time.Second,
			DisableCompression:  false,
			TLSHandshakeTimeout: 15 * time.Second,
		},
	}

	if proxyURL != "" {
		proxy, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy URL: %w", err)
		}
		scheme := strings.ToLower(proxy.Scheme)
		switch scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return nil, fmt.Errorf(
				"unsupported proxy scheme %q (supported: http, https, socks5, socks5h)",
				proxy.Scheme,
			)
		}
		if proxy.Host == "" {
			return nil, fmt.Errorf("invalid proxy URL: missing host")
		}
		client.Transport.(*http.Transport).Proxy = http.ProxyURL(proxy)
	} else {
		client.Transport.(*http.Transport).Proxy = http.ProxyFromEnvironment
	}

	return client, nil
}

type SearchProvider interface {
	Search(ctx context.Context, query string, count int) (SearchProviderResult, error)
}

type SearchProviderResult struct {
	Text  string `json:"text"`
	KeyID string `json:"key_id,omitempty"`
}

type apiKeyEntry struct {
	Key string
	ID  string
}

type apiKeyPool struct {
	keys []apiKeyEntry
	idx  atomic.Uint64
}

func makeKeyID(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	// Use 8 hex chars of sha256 as an opaque, stable identifier (does not reveal the key).
	return "sha256:" + hex.EncodeToString(sum[:4])
}

func newAPIKeyPool(primary string, keys []string) *apiKeyPool {
	all := make([]string, 0, 1+len(keys))
	if strings.TrimSpace(primary) != "" {
		all = append(all, strings.TrimSpace(primary))
	}
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k != "" {
			all = append(all, k)
		}
	}

	seen := make(map[string]bool, len(all))
	entries := make([]apiKeyEntry, 0, len(all))
	for _, k := range all {
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		entries = append(entries, apiKeyEntry{Key: k, ID: makeKeyID(k)})
	}

	if len(entries) == 0 {
		return nil
	}
	return &apiKeyPool{keys: entries}
}

func (p *apiKeyPool) Next() (key string, keyID string, ok bool) {
	if p == nil || len(p.keys) == 0 {
		return "", "", false
	}
	i := p.idx.Add(1) - 1
	ent := p.keys[int(i)%len(p.keys)]
	return ent.Key, ent.ID, true
}

type BraveSearchProvider struct {
	keys  *apiKeyPool
	proxy string
}

func (p *BraveSearchProvider) Search(ctx context.Context, query string, count int) (SearchProviderResult, error) {
	apiKey, keyID, ok := p.keys.Next()
	if !ok {
		return SearchProviderResult{}, fmt.Errorf("brave api key not configured")
	}
	searchURL := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d",
		url.QueryEscape(query), count)

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return SearchProviderResult{}, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", apiKey)

	client, err := createHTTPClient(p.proxy, 10*time.Second)
	if err != nil {
		return SearchProviderResult{}, fmt.Errorf("failed to create HTTP client: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return SearchProviderResult{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return SearchProviderResult{}, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return SearchProviderResult{}, fmt.Errorf("brave api error (status %d): %s", resp.StatusCode, string(body))
	}

	var searchResp struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}

	if err := json.Unmarshal(body, &searchResp); err != nil {
		// Log error body for debugging
		fmt.Printf("Brave API Error Body: %s\n", string(body))
		return SearchProviderResult{}, fmt.Errorf("failed to parse response: %w", err)
	}

	results := searchResp.Web.Results
	if len(results) == 0 {
		return SearchProviderResult{Text: fmt.Sprintf("No results for: %s", query), KeyID: keyID}, nil
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Results for: %s", query))
	for i, item := range results {
		if i >= count {
			break
		}
		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", i+1, item.Title, item.URL))
		if item.Description != "" {
			lines = append(lines, fmt.Sprintf("   %s", item.Description))
		}
	}

	return SearchProviderResult{Text: strings.Join(lines, "\n"), KeyID: keyID}, nil
}

type TavilySearchProvider struct {
	keys    *apiKeyPool
	baseURL string
	proxy   string
}

func (p *TavilySearchProvider) Search(ctx context.Context, query string, count int) (SearchProviderResult, error) {
	apiKey, keyID, ok := p.keys.Next()
	if !ok {
		return SearchProviderResult{}, fmt.Errorf("tavily api key not configured")
	}
	searchURL := p.baseURL
	if searchURL == "" {
		searchURL = "https://api.tavily.com/search"
	}

	payload := map[string]any{
		"api_key":             apiKey,
		"query":               query,
		"search_depth":        "advanced",
		"include_answer":      false,
		"include_images":      false,
		"include_raw_content": false,
		"max_results":         count,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return SearchProviderResult{}, fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", searchURL, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return SearchProviderResult{}, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	client, err := createHTTPClient(p.proxy, 10*time.Second)
	if err != nil {
		return SearchProviderResult{}, fmt.Errorf("failed to create HTTP client: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return SearchProviderResult{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return SearchProviderResult{}, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return SearchProviderResult{}, fmt.Errorf("tavily api error (status %d): %s", resp.StatusCode, string(body))
	}

	var searchResp struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &searchResp); err != nil {
		return SearchProviderResult{}, fmt.Errorf("failed to parse response: %w", err)
	}

	results := searchResp.Results
	if len(results) == 0 {
		return SearchProviderResult{Text: fmt.Sprintf("No results for: %s", query), KeyID: keyID}, nil
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Results for: %s (via Tavily)", query))
	for i, item := range results {
		if i >= count {
			break
		}
		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", i+1, item.Title, item.URL))
		if item.Content != "" {
			lines = append(lines, fmt.Sprintf("   %s", item.Content))
		}
	}

	return SearchProviderResult{Text: strings.Join(lines, "\n"), KeyID: keyID}, nil
}

type DuckDuckGoSearchProvider struct {
	proxy string
}

func (p *DuckDuckGoSearchProvider) Search(ctx context.Context, query string, count int) (SearchProviderResult, error) {
	searchURL := fmt.Sprintf("https://html.duckduckgo.com/html/?q=%s", url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return SearchProviderResult{}, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", userAgent)

	client, err := createHTTPClient(p.proxy, 10*time.Second)
	if err != nil {
		return SearchProviderResult{}, fmt.Errorf("failed to create HTTP client: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return SearchProviderResult{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return SearchProviderResult{}, fmt.Errorf("failed to read response: %w", err)
	}

	text, err := p.extractResults(string(body), count, query)
	if err != nil {
		return SearchProviderResult{}, err
	}
	return SearchProviderResult{Text: text}, nil
}

func (p *DuckDuckGoSearchProvider) extractResults(html string, count int, query string) (string, error) {
	// Simple regex based extraction for DDG HTML
	// Strategy: Find all result containers or key anchors directly

	// Try finding the result links directly first, as they are the most critical
	// Pattern: <a class="result__a" href="...">Title</a>
	// The previous regex was a bit strict. Let's make it more flexible for attributes order/content
	matches := reDDGLink.FindAllStringSubmatch(html, count+5)

	if len(matches) == 0 {
		return fmt.Sprintf("No results found or extraction failed. Query: %s", query), nil
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Results for: %s (via DuckDuckGo)", query))

	// Pre-compile snippet regex to run inside the loop
	// We'll search for snippets relative to the link position or just globally if needed
	// But simple global search for snippets might mismatch order.
	// Since we only have the raw HTML string, let's just extract snippets globally and assume order matches (risky but simple for regex)
	// Or better: Let's assume the snippet follows the link in the HTML

	// A better regex approach: iterate through text and find matches in order
	// But for now, let's grab all snippets too
	snippetMatches := reDDGSnippet.FindAllStringSubmatch(html, count+5)

	maxItems := min(len(matches), count)

	for i := 0; i < maxItems; i++ {
		urlStr := matches[i][1]
		title := stripTags(matches[i][2])
		title = strings.TrimSpace(title)

		// URL decoding if needed
		if strings.Contains(urlStr, "uddg=") {
			if u, err := url.QueryUnescape(urlStr); err == nil {
				idx := strings.Index(u, "uddg=")
				if idx != -1 {
					urlStr = u[idx+5:]
				}
			}
		}

		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", i+1, title, urlStr))

		// Attempt to attach snippet if available and index aligns
		if i < len(snippetMatches) {
			snippet := stripTags(snippetMatches[i][1])
			snippet = strings.TrimSpace(snippet)
			if snippet != "" {
				lines = append(lines, fmt.Sprintf("   %s", snippet))
			}
		}
	}

	return strings.Join(lines, "\n"), nil
}

func stripTags(content string) string {
	return reTags.ReplaceAllString(content, "")
}

type GrokSearchProvider struct {
	keys     *apiKeyPool
	endpoint string
	model    string
	proxy    string
}

func (p *GrokSearchProvider) Search(ctx context.Context, query string, count int) (SearchProviderResult, error) {
	apiKey, keyID, ok := p.keys.Next()
	if !ok {
		return SearchProviderResult{}, fmt.Errorf("grok api key not configured")
	}
	searchURL := strings.TrimSpace(p.endpoint)
	if searchURL == "" {
		searchURL = "https://api.x.ai/v1/chat/completions"
	}
	model := strings.TrimSpace(p.model)
	if model == "" {
		model = "grok-4"
	}

	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You are a search assistant. Provide concise search results with titles, URLs, and brief descriptions in the following format:\n1. Title\n   URL\n   Description\n\nDo not add extra commentary.",
			},
			{
				"role":    "user",
				"content": fmt.Sprintf("Search for: %s. Provide up to %d relevant results.", query, count),
			},
		},
		"max_tokens": 1000,
		"stream":     false,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return SearchProviderResult{}, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", searchURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return SearchProviderResult{}, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("User-Agent", userAgent)

	client, err := createHTTPClient(p.proxy, 30*time.Second)
	if err != nil {
		return SearchProviderResult{}, fmt.Errorf("failed to create HTTP client: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return SearchProviderResult{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return SearchProviderResult{}, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return SearchProviderResult{}, fmt.Errorf("Grok API error: %s", string(body))
	}

	content, err := parseGrokResponseContent(body)
	if err != nil {
		return SearchProviderResult{}, fmt.Errorf("failed to parse response: %w", err)
	}
	if strings.TrimSpace(content) == "" {
		return SearchProviderResult{Text: fmt.Sprintf("No results for: %s", query), KeyID: keyID}, nil
	}

	return SearchProviderResult{Text: fmt.Sprintf("Results for: %s (via Grok)\n%s", query, content), KeyID: keyID}, nil
}

func parseGrokResponseContent(body []byte) (string, error) {
	// First try regular JSON completion response.
	var searchResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &searchResp); err == nil {
		if len(searchResp.Choices) > 0 {
			return strings.TrimSpace(searchResp.Choices[0].Message.Content), nil
		}
	}

	// Some OpenAI-compatible gateways return SSE chunks even when stream=false.
	// Parse lines in "data: {...}" format and stitch delta content.
	text := strings.TrimSpace(string(body))
	if !strings.Contains(text, "data:") {
		return "", fmt.Errorf("unexpected response format")
	}

	var merged strings.Builder
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		chunk := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if chunk == "" || chunk == "[DONE]" {
			continue
		}

		var sseChunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(chunk), &sseChunk); err != nil {
			continue
		}
		if len(sseChunk.Choices) == 0 {
			continue
		}

		part := strings.TrimSpace(sseChunk.Choices[0].Delta.Content)
		if part == "" {
			part = strings.TrimSpace(sseChunk.Choices[0].Message.Content)
		}
		if part == "" {
			continue
		}
		if merged.Len() > 0 {
			merged.WriteByte(' ')
		}
		merged.WriteString(part)
	}

	if merged.Len() == 0 {
		return "", fmt.Errorf("empty content in SSE response")
	}
	return merged.String(), nil
}

type WebSearchTool struct {
	provider      SearchProvider
	providerName  string
	secondary     SearchProvider
	secondaryName string

	maxResults int

	evidenceMode      bool
	evidenceMinDomain int
}

type WebSearchToolOptions struct {
	BraveAPIKey          string
	BraveAPIKeys         []string
	BraveMaxResults      int
	BraveEnabled         bool
	TavilyAPIKey         string
	TavilyAPIKeys        []string
	TavilyBaseURL        string
	TavilyMaxResults     int
	TavilyEnabled        bool
	DuckDuckGoMaxResults int
	DuckDuckGoEnabled    bool
	GrokAPIKey           string
	GrokAPIKeys          []string
	GrokEndpoint         string
	GrokModel            string
	GrokMaxResults       int
	GrokEnabled          bool
	Proxy                string

	EvidenceModeEnabled bool
	EvidenceMinDomains  int
}

func NewWebSearchTool(opts WebSearchToolOptions) *WebSearchTool {
	type candidate struct {
		name       string
		provider   SearchProvider
		maxResults int
	}

	maxResults := 5
	candidates := make([]candidate, 0, 4)

	// Priority: Grok > Brave > Tavily > DuckDuckGo
	if opts.GrokEnabled {
		if pool := newAPIKeyPool(opts.GrokAPIKey, opts.GrokAPIKeys); pool != nil {
			mr := maxResults
			if opts.GrokMaxResults > 0 {
				mr = opts.GrokMaxResults
			}
			candidates = append(candidates, candidate{
				name: "grok",
				provider: &GrokSearchProvider{
					keys:     pool,
					endpoint: opts.GrokEndpoint,
					model:    opts.GrokModel,
					proxy:    opts.Proxy,
				},
				maxResults: mr,
			})
		}
	}

	if opts.BraveEnabled {
		if pool := newAPIKeyPool(opts.BraveAPIKey, opts.BraveAPIKeys); pool != nil {
			mr := maxResults
			if opts.BraveMaxResults > 0 {
				mr = opts.BraveMaxResults
			}
			candidates = append(candidates, candidate{
				name:       "brave",
				provider:   &BraveSearchProvider{keys: pool, proxy: opts.Proxy},
				maxResults: mr,
			})
		}
	}

	if opts.TavilyEnabled {
		if pool := newAPIKeyPool(opts.TavilyAPIKey, opts.TavilyAPIKeys); pool != nil {
			mr := maxResults
			if opts.TavilyMaxResults > 0 {
				mr = opts.TavilyMaxResults
			}
			candidates = append(candidates, candidate{
				name: "tavily",
				provider: &TavilySearchProvider{
					keys:    pool,
					baseURL: opts.TavilyBaseURL,
					proxy:   opts.Proxy,
				},
				maxResults: mr,
			})
		}
	}

	if opts.DuckDuckGoEnabled {
		mr := maxResults
		if opts.DuckDuckGoMaxResults > 0 {
			mr = opts.DuckDuckGoMaxResults
		}
		candidates = append(candidates, candidate{
			name:       "duckduckgo",
			provider:   &DuckDuckGoSearchProvider{proxy: opts.Proxy},
			maxResults: mr,
		})
	}

	if len(candidates) == 0 {
		return nil
	}

	primary := candidates[0]
	secondary := candidate{}
	hasSecondary := len(candidates) > 1
	if hasSecondary {
		secondary = candidates[1]
	}

	minDomains := opts.EvidenceMinDomains
	if minDomains <= 0 {
		minDomains = 2
	}

	tool := &WebSearchTool{
		provider:          primary.provider,
		providerName:      primary.name,
		secondary:         nil,
		secondaryName:     "",
		maxResults:        primary.maxResults,
		evidenceMode:      opts.EvidenceModeEnabled,
		evidenceMinDomain: minDomains,
	}
	if tool.evidenceMode && hasSecondary {
		tool.secondary = secondary.provider
		tool.secondaryName = secondary.name
		// Use the smaller max results across providers as a safe default.
		if secondary.maxResults > 0 && secondary.maxResults < tool.maxResults {
			tool.maxResults = secondary.maxResults
		}
	}

	return tool
}

type WebSearchDualTool struct {
	base *WebSearchTool
}

func NewWebSearchDualTool(opts WebSearchToolOptions) *WebSearchDualTool {
	opts.EvidenceModeEnabled = true
	tool := NewWebSearchTool(opts)
	if tool == nil {
		return nil
	}
	return &WebSearchDualTool{base: tool}
}

func (t *WebSearchDualTool) Name() string {
	return "web_search_dual"
}

func (t *WebSearchDualTool) ParallelPolicy() ToolParallelPolicy {
	return ToolParallelReadOnly
}

func (t *WebSearchDualTool) Description() string {
	return "Search the web using two providers in parallel (when available) and return a structured JSON payload. " +
		"Input: query (string, required), count (integer, optional, 1-10, default 5). " +
		"Output: JSON including per-provider status, extracted sources, and an evidence summary."
}

func (t *WebSearchDualTool) Parameters() map[string]any {
	if t == nil || t.base == nil {
		return map[string]any{"type": "object"}
	}
	return t.base.Parameters()
}

func (t *WebSearchDualTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if t == nil || t.base == nil {
		return ErrorResult("web_search_dual tool is not configured")
	}
	return t.base.Execute(ctx, args)
}

func (t *WebSearchTool) Name() string {
	return "web_search"
}

func (t *WebSearchTool) ParallelPolicy() ToolParallelPolicy {
	return ToolParallelReadOnly
}

func (t *WebSearchTool) Description() string {
	desc := "Search the web for current information. " +
		"Input: query (string, required), count (integer, optional, 1-10, default 5). " +
		"Output: list of results with title, URL, and snippet for each. " +
		"Use this for questions about current events, recent data, or facts you are unsure about. " +
		"For reading a specific URL after searching, use the 'web_fetch' tool."
	if t != nil && t.evidenceMode {
		desc += " Evidence mode is enabled: the tool returns structured JSON with extracted sources and an evidence summary."
	}
	return desc
}

func (t *WebSearchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Search query",
			},
			"count": map[string]any{
				"type":        "integer",
				"description": "Number of results (1-10)",
				"minimum":     1.0,
				"maximum":     10.0,
			},
		},
		"required": []string{"query"},
	}
}

func (t *WebSearchTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	query, ok := args["query"].(string)
	if !ok {
		return ErrorResult("query is required")
	}

	count := t.maxResults
	if c, ok := args["count"].(float64); ok {
		if int(c) > 0 && int(c) <= 10 {
			count = int(c)
		}
	}

	if t == nil || t.provider == nil {
		return ErrorResult("web_search tool is not configured")
	}

	if !t.evidenceMode {
		result, err := t.provider.Search(ctx, query, count)
		if err != nil {
			return ErrorResult(fmt.Sprintf("search failed: %v", err))
		}

		return &ToolResult{
			ForLLM:  result.Text,
			ForUser: result.Text,
		}
	}

	type evidenceSource struct {
		URL      string `json:"url"`
		Domain   string `json:"domain,omitempty"`
		Provider string `json:"provider,omitempty"`
	}
	type providerEvidence struct {
		Name        string           `json:"name"`
		KeyID       string           `json:"key_id,omitempty"`
		OK          bool             `json:"ok"`
		Error       string           `json:"error,omitempty"`
		SourceCount int              `json:"source_count,omitempty"`
		Sources     []evidenceSource `json:"sources,omitempty"`
	}
	type evidenceSummary struct {
		Enabled         bool `json:"enabled"`
		MinDomains      int  `json:"min_domains"`
		DistinctDomains int  `json:"distinct_domains"`
		Satisfied       bool `json:"satisfied"`
	}
	type payload struct {
		Kind      string             `json:"kind"`
		Query     string             `json:"query"`
		Count     int                `json:"count"`
		Providers []providerEvidence `json:"providers,omitempty"`
		Sources   []evidenceSource   `json:"sources,omitempty"`
		Evidence  evidenceSummary    `json:"evidence"`
	}

	extractSources := func(providerName, text string) []evidenceSource {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		seen := make(map[string]bool)
		matches := reURL.FindAllString(text, -1)
		out := make([]evidenceSource, 0, len(matches))
		for _, raw := range matches {
			raw = strings.TrimSpace(raw)
			raw = strings.TrimRight(raw, ".,;:)]}\"'")
			if raw == "" || seen[raw] {
				continue
			}
			seen[raw] = true

			host := ""
			if u, err := url.Parse(raw); err == nil {
				host = strings.TrimSpace(u.Host)
				if host != "" {
					host = strings.ToLower(strings.TrimSpace(strings.Split(host, ":")[0]))
				}
			}
			out = append(out, evidenceSource{
				URL:      raw,
				Domain:   host,
				Provider: providerName,
			})
		}
		return out
	}

	runProvider := func(name string, provider SearchProvider) providerEvidence {
		if provider == nil {
			return providerEvidence{Name: name, OK: false, Error: "provider not configured"}
		}
		res, err := provider.Search(ctx, query, count)
		if err != nil {
			return providerEvidence{Name: name, OK: false, Error: err.Error()}
		}
		sources := extractSources(name, res.Text)
		return providerEvidence{
			Name:        name,
			KeyID:       strings.TrimSpace(res.KeyID),
			OK:          true,
			SourceCount: len(sources),
			Sources:     sources,
		}
	}

	type toRun struct {
		name     string
		provider SearchProvider
	}
	toRunList := []toRun{
		{name: t.providerName, provider: t.provider},
	}
	if t.secondary != nil {
		toRunList = append(toRunList, toRun{name: t.secondaryName, provider: t.secondary})
	}

	var wg sync.WaitGroup
	results := make([]providerEvidence, len(toRunList))
	for i, item := range toRunList {
		wg.Add(1)
		go func(i int, item toRun) {
			defer wg.Done()
			results[i] = runProvider(item.name, item.provider)
		}(i, item)
	}
	wg.Wait()

	merged := make([]evidenceSource, 0, 16)
	seenURL := make(map[string]bool)
	distinctDomains := make(map[string]bool)
	for _, p := range results {
		for _, s := range p.Sources {
			u := strings.TrimSpace(s.URL)
			if u == "" || seenURL[u] {
				continue
			}
			seenURL[u] = true
			merged = append(merged, s)
			if s.Domain != "" {
				distinctDomains[s.Domain] = true
			}
		}
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Domain == merged[j].Domain {
			return merged[i].URL < merged[j].URL
		}
		return merged[i].Domain < merged[j].Domain
	})

	minDomains := t.evidenceMinDomain
	if minDomains <= 0 {
		minDomains = 2
	}
	summary := evidenceSummary{
		Enabled:         true,
		MinDomains:      minDomains,
		DistinctDomains: len(distinctDomains),
		Satisfied:       len(distinctDomains) >= minDomains,
	}

	out := payload{
		Kind:      "web_search_result",
		Query:     query,
		Count:     count,
		Providers: results,
		Sources:   merged,
		Evidence:  summary,
	}
	data, _ := json.MarshalIndent(out, "", "  ")

	providerNames := make([]string, 0, len(toRunList))
	for _, item := range toRunList {
		if strings.TrimSpace(item.name) != "" {
			providerNames = append(providerNames, strings.TrimSpace(item.name))
		}
	}
	userSummary := fmt.Sprintf(
		"Web search (evidence_mode=true): %q (providers: %s; sources=%d; distinct_domains=%d; satisfied=%v)",
		query,
		strings.Join(providerNames, ", "),
		len(merged),
		len(distinctDomains),
		summary.Satisfied,
	)

	return &ToolResult{
		ForLLM:  string(data),
		ForUser: userSummary,
	}
}

type WebFetchTool struct {
	maxChars        int
	proxy           string
	client          *http.Client
	fetchLimitBytes int64

	cacheEnabled       bool
	cacheTTL           time.Duration
	cacheMaxEntries    int
	cacheMaxEntryChars int

	cacheMu sync.Mutex
	cache   map[string]webFetchCacheEntry

	sf singleflight.Group
}

type webFetchFailure struct {
	Kind       string `json:"kind,omitempty"`
	Message    string `json:"message,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type webFetchSource struct {
	URL           string `json:"url"`
	RetrievedAtMS int64  `json:"retrieved_at_ms,omitempty"`
	Status        int    `json:"status,omitempty"`
	ContentType   string `json:"content_type,omitempty"`
}

type webFetchQuote struct {
	SourceURL string `json:"source_url,omitempty"`
	Text      string `json:"text"`
}

type webFetchRawResult struct {
	URL         string
	Status      int
	ContentType string

	Extractor string
	Tried     []string

	SourceLength int
	Text         string

	RetrievedAtMS int64

	Failure *webFetchFailure
}

type webFetchCacheEntry struct {
	ExpiresAt time.Time
	Value     webFetchRawResult
}

func NewWebFetchTool(maxChars int, fetchLimitBytes int64) (*WebFetchTool, error) {
	// createHTTPClient cannot fail with an empty proxy string.
	return NewWebFetchToolWithProxy(maxChars, "", fetchLimitBytes)
}

func NewWebFetchToolWithProxy(maxChars int, proxy string, fetchLimitBytes int64) (*WebFetchTool, error) {
	if maxChars <= 0 {
		maxChars = 50000
	}

	client, err := createHTTPClient(proxy, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client for web fetch: %w", err)
	}

	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("stopped after 5 redirects")
		}
		return nil
	}

	if fetchLimitBytes <= 0 {
		fetchLimitBytes = 10 * 1024 * 1024 // Security Fallback
	}

	return &WebFetchTool{
		maxChars:           maxChars,
		proxy:              proxy,
		client:             client,
		fetchLimitBytes:    fetchLimitBytes,
		cacheEnabled:       true,
		cacheTTL:           120 * time.Second,
		cacheMaxEntries:    32,
		cacheMaxEntryChars: 80_000,
		cache:              make(map[string]webFetchCacheEntry),
	}, nil
}

// ConfigureCache configures the in-memory fetch cache for this tool.
// It is safe to call at startup (recommended) and is thread-safe.
func (t *WebFetchTool) ConfigureCache(enabled bool, ttl time.Duration, maxEntries int, maxEntryChars int) {
	if t == nil {
		return
	}
	if ttl <= 0 {
		ttl = 120 * time.Second
	}
	if maxEntries <= 0 {
		maxEntries = 32
	}
	if maxEntryChars <= 0 {
		maxEntryChars = 80_000
	}

	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()

	t.cacheEnabled = enabled
	t.cacheTTL = ttl
	t.cacheMaxEntries = maxEntries
	t.cacheMaxEntryChars = maxEntryChars

	if !t.cacheEnabled {
		t.cache = nil
		return
	}
	if t.cache == nil {
		t.cache = make(map[string]webFetchCacheEntry)
	}
}

func (t *WebFetchTool) Name() string {
	return "web_fetch"
}

func (t *WebFetchTool) ParallelPolicy() ToolParallelPolicy {
	return ToolParallelReadOnly
}

func (t *WebFetchTool) Description() string {
	return "Fetch a URL and extract readable content (HTML to text). " +
		"Input: url (string, required), maxChars (integer, optional — max chars to extract). " +
		"Output: extracted text content with metadata (status code, content type, length). " +
		"Supports HTML pages, JSON APIs, and plain text. " +
		"Use this to read specific web pages, API endpoints, or articles found via 'web_search'."
}

func (t *WebFetchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "URL to fetch",
			},
			"maxChars": map[string]any{
				"type":        "integer",
				"description": "Maximum characters to extract",
				"minimum":     100.0,
			},
			"llmMaxChars": map[string]any{
				"type":        "integer",
				"description": "Maximum characters included in LLM-facing content (defaults to maxChars)",
				"minimum":     100.0,
			},
		},
		"required": []string{"url"},
	}
}

func (t *WebFetchTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	urlStr, ok := args["url"].(string)
	if !ok {
		return ErrorResult("url is required")
	}
	urlStr = strings.TrimSpace(urlStr)

	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return ErrorResult(fmt.Sprintf("invalid URL: %v", err))
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return ErrorResult("only http/https URLs are allowed")
	}

	if parsedURL.Host == "" {
		return ErrorResult("missing domain in URL")
	}

	maxChars := t.maxChars
	if raw, ok := args["maxChars"]; ok {
		if mc, err := toInt(raw); err == nil && mc > 100 {
			maxChars = mc
		}
	}
	llmMaxChars := maxChars
	if raw, ok := args["llmMaxChars"]; ok {
		if mc, err := toInt(raw); err == nil && mc > 100 {
			llmMaxChars = mc
		}
	}

	// Cache fast-path.
	if cached, ok := t.getCached(urlStr); ok {
		return t.buildWebFetchResult(urlStr, cached, maxChars, llmMaxChars, true)
	}

	rawAny, _, _ := t.sf.Do(urlStr, func() (any, error) {
		if cached, ok := t.getCached(urlStr); ok {
			return cached, nil
		}
		raw := t.fetchAndExtract(ctx, urlStr)
		t.putCache(urlStr, raw)
		return raw, nil
	})
	raw, _ := rawAny.(webFetchRawResult)
	return t.buildWebFetchResult(urlStr, raw, maxChars, llmMaxChars, false)
}

func (t *WebFetchTool) getCached(urlStr string) (webFetchRawResult, bool) {
	if t == nil {
		return webFetchRawResult{}, false
	}

	urlStr = strings.TrimSpace(urlStr)
	if urlStr == "" {
		return webFetchRawResult{}, false
	}

	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()

	if !t.cacheEnabled || t.cache == nil || t.cacheTTL < 0 {
		return webFetchRawResult{}, false
	}

	now := time.Now()
	// Opportunistic pruning.
	for k, v := range t.cache {
		if now.After(v.ExpiresAt) {
			delete(t.cache, k)
		}
	}

	ent, ok := t.cache[urlStr]
	if !ok {
		return webFetchRawResult{}, false
	}
	if now.After(ent.ExpiresAt) {
		delete(t.cache, urlStr)
		return webFetchRawResult{}, false
	}
	return ent.Value, true
}

func (t *WebFetchTool) putCache(urlStr string, raw webFetchRawResult) {
	if t == nil {
		return
	}

	urlStr = strings.TrimSpace(urlStr)
	if urlStr == "" {
		return
	}

	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()

	if !t.cacheEnabled || t.cacheTTL < 0 {
		return
	}
	if t.cache == nil {
		t.cache = make(map[string]webFetchCacheEntry)
	}
	if t.cacheTTL <= 0 {
		t.cacheTTL = 120 * time.Second
	}
	if t.cacheMaxEntries <= 0 {
		t.cacheMaxEntries = 32
	}
	if t.cacheMaxEntryChars <= 0 {
		t.cacheMaxEntryChars = 80_000
	}

	if strings.TrimSpace(raw.Text) != "" && t.cacheMaxEntryChars > 0 {
		raw.Text = utils.Truncate(raw.Text, t.cacheMaxEntryChars)
	}

	now := time.Now()
	// Prune expired first.
	for k, v := range t.cache {
		if now.After(v.ExpiresAt) {
			delete(t.cache, k)
		}
	}
	// Evict oldest (earliest expiry) if needed.
	for len(t.cache) >= t.cacheMaxEntries {
		var oldestKey string
		var oldest time.Time
		for k, v := range t.cache {
			if oldestKey == "" || v.ExpiresAt.Before(oldest) {
				oldestKey = k
				oldest = v.ExpiresAt
			}
		}
		if oldestKey == "" {
			break
		}
		delete(t.cache, oldestKey)
	}

	t.cache[urlStr] = webFetchCacheEntry{
		ExpiresAt: now.Add(t.cacheTTL),
		Value:     raw,
	}
}

func (t *WebFetchTool) fetchAndExtract(ctx context.Context, urlStr string) webFetchRawResult {
	now := time.Now()
	out := webFetchRawResult{
		URL:           strings.TrimSpace(urlStr),
		RetrievedAtMS: now.UnixMilli(),
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if t == nil || t.client == nil {
		out.Failure = &webFetchFailure{
			Kind:    "internal",
			Message: "web_fetch is not configured",
		}
		return out
	}

	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		out.Failure = &webFetchFailure{
			Kind:    "invalid_url",
			Message: err.Error(),
		}
		return out
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/json,text/plain,*/*")

	resp, err := t.client.Do(req)
	if err != nil {
		out.Failure = classifyWebFetchError(err)
		return out
	}
	defer resp.Body.Close()

	out.Status = resp.StatusCode
	out.ContentType = strings.TrimSpace(resp.Header.Get("Content-Type"))

	limit := t.fetchLimitBytes
	if limit <= 0 {
		limit = 10 * 1024 * 1024
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if readErr != nil {
		out.Failure = &webFetchFailure{
			Kind:       "read_error",
			Message:    readErr.Error(),
			HTTPStatus: resp.StatusCode,
		}
		return out
	}
	if int64(len(body)) > limit {
		out.Failure = &webFetchFailure{
			Kind:       "oversize",
			Message:    fmt.Sprintf("size exceeded %d bytes limit", limit),
			HTTPStatus: resp.StatusCode,
		}
		return out
	}

	out.SourceLength = len(body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		out.Failure = classifyWebFetchHTTPStatus(resp.StatusCode, string(body))
	}

	text, extractor, tried, extractErr := extractWebContent(string(body), out.ContentType)
	out.Text = text
	out.Extractor = extractor
	out.Tried = tried
	if extractErr != nil && out.Failure == nil {
		out.Failure = &webFetchFailure{
			Kind:       "extract_error",
			Message:    extractErr.Error(),
			HTTPStatus: resp.StatusCode,
		}
	}

	return out
}

func classifyWebFetchHTTPStatus(status int, bodyPreview string) *webFetchFailure {
	bodyPreview = strings.TrimSpace(bodyPreview)
	if len(bodyPreview) > 600 {
		bodyPreview = utils.Truncate(bodyPreview, 600)
	}

	switch status {
	case http.StatusUnauthorized:
		return &webFetchFailure{Kind: "unauthorized", Message: "http 401 unauthorized", HTTPStatus: status}
	case http.StatusForbidden:
		return &webFetchFailure{Kind: "forbidden", Message: "http 403 forbidden", HTTPStatus: status}
	case http.StatusTooManyRequests:
		return &webFetchFailure{Kind: "rate_limited", Message: "http 429 rate limited", HTTPStatus: status}
	case http.StatusNotFound:
		return &webFetchFailure{Kind: "not_found", Message: "http 404 not found", HTTPStatus: status}
	default:
		msg := fmt.Sprintf("http status %d", status)
		if bodyPreview != "" {
			msg = msg + ": " + bodyPreview
		}
		return &webFetchFailure{Kind: "http_error", Message: msg, HTTPStatus: status}
	}
}

func classifyWebFetchError(err error) *webFetchFailure {
	if err == nil {
		return &webFetchFailure{Kind: "network_error", Message: "unknown error"}
	}
	if errors.Is(err, context.Canceled) {
		return &webFetchFailure{Kind: "canceled", Message: err.Error()}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &webFetchFailure{Kind: "timeout", Message: err.Error()}
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &webFetchFailure{Kind: "timeout", Message: err.Error()}
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "no such host"):
		return &webFetchFailure{Kind: "dns", Message: err.Error()}
	case strings.Contains(msg, "tls"):
		return &webFetchFailure{Kind: "tls", Message: err.Error()}
	default:
		return &webFetchFailure{Kind: "network_error", Message: err.Error()}
	}
}

func extractWebContent(body string, contentType string) (text string, extractor string, tried []string, err error) {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	bodyTrim := strings.TrimSpace(body)

	isJSON := strings.Contains(contentType, "application/json") ||
		strings.Contains(contentType, "+json") ||
		strings.Contains(contentType, "text/json")
	isHTML := strings.Contains(contentType, "text/html") || strings.Contains(contentType, "application/xhtml+xml")
	isText := strings.HasPrefix(contentType, "text/") || strings.Contains(contentType, "application/xml") || strings.Contains(contentType, "text/xml")

	if !isJSON && !isHTML && !isText {
		// Sniff when server does not provide useful content-type.
		if strings.HasPrefix(strings.ToLower(bodyTrim), "<!doctype html") || strings.HasPrefix(strings.ToLower(bodyTrim), "<html") {
			isHTML = true
		} else if strings.HasPrefix(bodyTrim, "{") || strings.HasPrefix(bodyTrim, "[") {
			isJSON = true
		} else if contentType == "" {
			isText = true
		}
	}

	switch {
	case isJSON:
		tried = []string{"json.pretty", "json.raw"}
		var buf bytes.Buffer
		if json.Indent(&buf, []byte(bodyTrim), "", "  ") == nil {
			return buf.String(), "json.pretty", tried, nil
		}
		return bodyTrim, "json.raw", tried, nil
	case isHTML:
		tried = []string{"html.readability_like", "html.strip_tags", "text.raw"}
		if candidate := extractHTMLReadabilityLike(body); strings.TrimSpace(candidate) != "" {
			return candidate, "html.readability_like", tried, nil
		}
		if candidate := extractHTMLStripTags(body); strings.TrimSpace(candidate) != "" {
			return candidate, "html.strip_tags", tried, nil
		}
		return bodyTrim, "text.raw", tried, nil
	case isText:
		tried = []string{"text.raw"}
		return bodyTrim, "text.raw", tried, nil
	default:
		return "", "", nil, fmt.Errorf("unsupported content type: %q", contentType)
	}
}

func extractHTMLReadabilityLike(htmlContent string) string {
	// Super lightweight readability-ish extraction:
	// - remove script/style/noscript blocks
	// - drop common layout blocks (nav/header/footer/aside)
	// - preserve newlines for common block tags
	s := htmlContent

	// Remove non-content blocks first.
	s = reScript.ReplaceAllString(s, "")
	s = reStyle.ReplaceAllString(s, "")

	s = reNoScript.ReplaceAllString(s, "")
	for _, re := range reLayoutTags {
		s = re.ReplaceAllString(s, "")
	}

	s = normalizeHTMLNewlines(s)
	s = stripTags(s)
	s = html.UnescapeString(s)

	s = strings.TrimSpace(s)
	s = reWhitespace.ReplaceAllString(s, " ")
	s = reBlankLines.ReplaceAllString(s, "\n\n")

	lines := strings.Split(s, "\n")
	cleanLines := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Ignore very short "chrome" lines.
		if len([]rune(line)) < 3 {
			continue
		}
		cleanLines = append(cleanLines, line)
	}

	out := strings.Join(cleanLines, "\n")
	if len([]rune(out)) < 40 {
		// Too small to be useful; likely missed. Let caller fall back.
		return ""
	}
	return out
}

func extractHTMLStripTags(htmlContent string) string {
	s := htmlContent
	s = reScript.ReplaceAllString(s, "")
	s = reStyle.ReplaceAllString(s, "")
	s = normalizeHTMLNewlines(s)
	s = stripTags(s)
	s = html.UnescapeString(s)

	s = strings.TrimSpace(s)
	s = reWhitespace.ReplaceAllString(s, " ")
	s = reBlankLines.ReplaceAllString(s, "\n\n")

	lines := strings.Split(s, "\n")
	cleanLines := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			cleanLines = append(cleanLines, line)
		}
	}
	return strings.Join(cleanLines, "\n")
}

func normalizeHTMLNewlines(s string) string {
	s = reBR.ReplaceAllString(s, "\n")
	s = reCloseBlock.ReplaceAllString(s, "\n")

	return s
}

func (t *WebFetchTool) buildWebFetchResult(urlStr string, raw webFetchRawResult, maxChars int, llmMaxChars int, cacheHit bool) *ToolResult {
	if t == nil {
		return ErrorResult("web_fetch tool is not configured")
	}
	if maxChars <= 0 {
		maxChars = t.maxChars
	}
	if llmMaxChars <= 0 {
		llmMaxChars = maxChars
	}

	fullText := strings.TrimSpace(raw.Text)
	userText := utils.Truncate(fullText, maxChars)
	llmText := utils.Truncate(userText, llmMaxChars)

	truncated := false
	if len([]rune(fullText)) > len([]rune(llmText)) {
		truncated = true
	}

	payload := struct {
		Kind            string           `json:"kind"`
		URL             string           `json:"url"`
		OK              bool             `json:"ok"`
		Status          int              `json:"status,omitempty"`
		ContentType     string           `json:"content_type,omitempty"`
		RetrievedAtMS   int64            `json:"retrieved_at_ms,omitempty"`
		Extractor       string           `json:"extractor,omitempty"`
		TriedExtractors []string         `json:"tried_extractors,omitempty"`
		SourceLength    int              `json:"source_length,omitempty"`
		Text            string           `json:"text,omitempty"`
		TextChars       int              `json:"text_chars,omitempty"`
		MaxChars        int              `json:"max_chars,omitempty"`
		LLMMaxChars     int              `json:"llm_max_chars,omitempty"`
		Truncated       bool             `json:"truncated,omitempty"`
		CacheHit        bool             `json:"cache_hit,omitempty"`
		Failure         *webFetchFailure `json:"failure,omitempty"`
		Sources         []webFetchSource `json:"sources,omitempty"`
		Quotes          []webFetchQuote  `json:"quotes,omitempty"`
	}{
		Kind:            "web_fetch_result",
		URL:             urlStr,
		OK:              raw.Failure == nil && raw.Status >= 200 && raw.Status < 300,
		Status:          raw.Status,
		ContentType:     raw.ContentType,
		RetrievedAtMS:   raw.RetrievedAtMS,
		Extractor:       raw.Extractor,
		TriedExtractors: raw.Tried,
		SourceLength:    raw.SourceLength,
		Text:            llmText,
		TextChars:       len([]rune(fullText)),
		MaxChars:        maxChars,
		LLMMaxChars:     llmMaxChars,
		Truncated:       truncated,
		CacheHit:        cacheHit,
		Failure:         raw.Failure,
		Sources: []webFetchSource{
			{
				URL:           urlStr,
				RetrievedAtMS: raw.RetrievedAtMS,
				Status:        raw.Status,
				ContentType:   raw.ContentType,
			},
		},
		Quotes: buildWebFetchQuotes(urlStr, llmText),
	}

	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to encode web_fetch result: %v", err))
	}

	userSummary := fmt.Sprintf(
		"Fetched %s (status=%d, bytes=%d, extractor=%s, truncated=%v, cache_hit=%v)",
		urlStr,
		raw.Status,
		raw.SourceLength,
		strings.TrimSpace(raw.Extractor),
		truncated,
		cacheHit,
	)

	res := &ToolResult{
		ForLLM:  string(encoded),
		ForUser: userSummary,
		IsError: raw.Failure != nil || raw.Status >= 400 || raw.Status == 0,
	}
	if raw.Failure != nil && strings.TrimSpace(raw.Failure.Message) != "" {
		res.ForUser = fmt.Sprintf("web_fetch failed (%s): %s", raw.Failure.Kind, raw.Failure.Message)
	}
	return res
}

func buildWebFetchQuotes(urlStr string, text string) []webFetchQuote {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	lines := strings.Split(text, "\n")
	out := make([]webFetchQuote, 0, 3)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, webFetchQuote{
			SourceURL: urlStr,
			Text:      utils.Truncate(line, 240),
		})
		if len(out) >= 3 {
			break
		}
	}
	if len(out) == 0 {
		out = append(out, webFetchQuote{SourceURL: urlStr, Text: utils.Truncate(text, 240)})
	}
	return out
}

func (t *WebFetchTool) extractText(htmlContent string) string {
	return extractHTMLStripTags(htmlContent)
}
