package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
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
	Search(ctx context.Context, query string, count int) (string, error)
}

type BraveSearchProvider struct {
	apiKey string
	proxy  string
}

func (p *BraveSearchProvider) Search(ctx context.Context, query string, count int) (string, error) {
	searchURL := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d",
		url.QueryEscape(query), count)

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", p.apiKey)

	client, err := createHTTPClient(p.proxy, 10*time.Second)
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP client: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("brave api error (status %d): %s", resp.StatusCode, string(body))
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
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	results := searchResp.Web.Results
	if len(results) == 0 {
		return fmt.Sprintf("No results for: %s", query), nil
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

	return strings.Join(lines, "\n"), nil
}

type TavilySearchProvider struct {
	apiKey  string
	baseURL string
	proxy   string
}

func (p *TavilySearchProvider) Search(ctx context.Context, query string, count int) (string, error) {
	searchURL := p.baseURL
	if searchURL == "" {
		searchURL = "https://api.tavily.com/search"
	}

	payload := map[string]any{
		"api_key":             p.apiKey,
		"query":               query,
		"search_depth":        "advanced",
		"include_answer":      false,
		"include_images":      false,
		"include_raw_content": false,
		"max_results":         count,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", searchURL, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	client, err := createHTTPClient(p.proxy, 10*time.Second)
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP client: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tavily api error (status %d): %s", resp.StatusCode, string(body))
	}

	var searchResp struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &searchResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	results := searchResp.Results
	if len(results) == 0 {
		return fmt.Sprintf("No results for: %s", query), nil
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

	return strings.Join(lines, "\n"), nil
}

type DuckDuckGoSearchProvider struct {
	proxy string
}

func (p *DuckDuckGoSearchProvider) Search(ctx context.Context, query string, count int) (string, error) {
	searchURL := fmt.Sprintf("https://html.duckduckgo.com/html/?q=%s", url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", userAgent)

	client, err := createHTTPClient(p.proxy, 10*time.Second)
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP client: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	return p.extractResults(string(body), count, query)
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
	apiKey   string
	endpoint string
	model    string
	proxy    string
}

func (p *GrokSearchProvider) Search(ctx context.Context, query string, count int) (string, error) {
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
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", searchURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("User-Agent", userAgent)

	client, err := createHTTPClient(p.proxy, 30*time.Second)
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP client: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Grok API error: %s", string(body))
	}

	content, err := parseGrokResponseContent(body)
	if err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Sprintf("No results for: %s", query), nil
	}

	return fmt.Sprintf("Results for: %s (via Grok)\n%s", query, content), nil
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
	BraveMaxResults      int
	BraveEnabled         bool
	TavilyAPIKey         string
	TavilyBaseURL        string
	TavilyMaxResults     int
	TavilyEnabled        bool
	DuckDuckGoMaxResults int
	DuckDuckGoEnabled    bool
	GrokAPIKey           string
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
	if opts.GrokEnabled && strings.TrimSpace(opts.GrokAPIKey) != "" {
		mr := maxResults
		if opts.GrokMaxResults > 0 {
			mr = opts.GrokMaxResults
		}
		candidates = append(candidates, candidate{
			name: "grok",
			provider: &GrokSearchProvider{
				apiKey:   opts.GrokAPIKey,
				endpoint: opts.GrokEndpoint,
				model:    opts.GrokModel,
				proxy:    opts.Proxy,
			},
			maxResults: mr,
		})
	}

	if opts.BraveEnabled && strings.TrimSpace(opts.BraveAPIKey) != "" {
		mr := maxResults
		if opts.BraveMaxResults > 0 {
			mr = opts.BraveMaxResults
		}
		candidates = append(candidates, candidate{
			name:       "brave",
			provider:   &BraveSearchProvider{apiKey: opts.BraveAPIKey, proxy: opts.Proxy},
			maxResults: mr,
		})
	}

	if opts.TavilyEnabled && strings.TrimSpace(opts.TavilyAPIKey) != "" {
		mr := maxResults
		if opts.TavilyMaxResults > 0 {
			mr = opts.TavilyMaxResults
		}
		candidates = append(candidates, candidate{
			name: "tavily",
			provider: &TavilySearchProvider{
				apiKey:  opts.TavilyAPIKey,
				baseURL: opts.TavilyBaseURL,
				proxy:   opts.Proxy,
			},
			maxResults: mr,
		})
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
			ForLLM:  result,
			ForUser: result,
		}
	}

	type evidenceSource struct {
		URL      string `json:"url"`
		Domain   string `json:"domain,omitempty"`
		Provider string `json:"provider,omitempty"`
	}
	type providerEvidence struct {
		Name        string           `json:"name"`
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
		text, err := provider.Search(ctx, query, count)
		if err != nil {
			return providerEvidence{Name: name, OK: false, Error: err.Error()}
		}
		sources := extractSources(name, text)
		return providerEvidence{
			Name:        name,
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
		maxChars:        maxChars,
		proxy:           proxy,
		client:          client,
		fetchLimitBytes: fetchLimitBytes,
	}, nil
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

	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to create request: %v", err))
	}

	req.Header.Set("User-Agent", userAgent)

	client, err := createHTTPClient(t.proxy, 60*time.Second)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to create HTTP client: %v", err))
	}

	// Configure redirect handling
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("stopped after 5 redirects")
		}
		return nil
	}

	resp, err := client.Do(req)
	if err != nil {
		return ErrorResult(fmt.Sprintf("request failed: %v", err))
	}

	resp.Body = http.MaxBytesReader(nil, resp.Body, t.fetchLimitBytes)

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return ErrorResult(fmt.Sprintf("failed to read response: size exceeded %d bytes limit", t.fetchLimitBytes))
		}
		return ErrorResult(fmt.Sprintf("failed to read response: %v", err))
	}

	contentType := resp.Header.Get("Content-Type")

	var text, extractor string

	if strings.Contains(contentType, "application/json") {
		var jsonData any
		if err := json.Unmarshal(body, &jsonData); err == nil {
			formatted, _ := json.MarshalIndent(jsonData, "", "  ")
			text = string(formatted)
			extractor = "json"
		} else {
			text = string(body)
			extractor = "raw"
		}
	} else if strings.Contains(contentType, "text/html") || len(body) > 0 &&
		(strings.HasPrefix(string(body), "<!DOCTYPE") || strings.HasPrefix(strings.ToLower(string(body)), "<html")) {
		text = t.extractText(string(body))
		extractor = "text"
	} else {
		text = string(body)
		extractor = "raw"
	}

	truncated := len(text) > maxChars
	if truncated {
		text = text[:maxChars]
	}
	llmText := text
	llmTruncated := false
	if len(llmText) > llmMaxChars {
		llmText = llmText[:llmMaxChars]
		llmTruncated = true
	}
	llmPayload := map[string]any{
		"url":          urlStr,
		"status":       resp.StatusCode,
		"extractor":    extractor,
		"sourceLength": len(text),
		"truncated":    truncated || llmTruncated,
		"text":         llmText,
	}
	llmJSON, _ := json.MarshalIndent(llmPayload, "", "  ")

	return &ToolResult{
		ForLLM: string(llmJSON),
		ForUser: fmt.Sprintf(
			"Fetched %d bytes from %s (extractor: %s, truncated: %v)",
			len(text),
			urlStr,
			extractor,
			truncated,
		),
	}
}

func (t *WebFetchTool) extractText(htmlContent string) string {
	result := reScript.ReplaceAllLiteralString(htmlContent, "")
	result = reStyle.ReplaceAllLiteralString(result, "")
	result = reTags.ReplaceAllLiteralString(result, "")

	result = strings.TrimSpace(result)

	result = reWhitespace.ReplaceAllString(result, " ")
	result = reBlankLines.ReplaceAllString(result, "\n\n")

	lines := strings.Split(result, "\n")
	var cleanLines []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			cleanLines = append(cleanLines, line)
		}
	}

	return strings.Join(cleanLines, "\n")
}
