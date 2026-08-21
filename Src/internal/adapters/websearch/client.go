package websearch

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const (
	DefaultEndpoint  = "https://html.duckduckgo.com/html/"
	FallbackEndpoint = "https://www.bing.com/search"
	maxResponseSize  = 2 << 20
)

type Input = core.WebSearchRequest
type Result = core.WebSearchSource
type Response = core.WebSearchResult

type Client struct {
	endpoints  []*url.URL
	httpClient *http.Client
	searchSlot chan struct{}
}

func New(endpoint string) (*Client, error) {
	endpoint = strings.TrimSpace(endpoint)
	rawEndpoints := []string{endpoint}
	if endpoint == "" {
		rawEndpoints = []string{DefaultEndpoint, FallbackEndpoint}
	}
	endpoints := make([]*url.URL, 0, len(rawEndpoints))
	for _, raw := range rawEndpoints {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil {
			return nil, fmt.Errorf("web search endpoint must be an absolute HTTP(S) URL without credentials")
		}
		if parsed.Scheme != "https" && !isLoopbackHost(parsed.Hostname()) {
			return nil, fmt.Errorf("web search endpoint must use HTTPS unless it is loopback")
		}
		endpoints = append(endpoints, parsed)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	client := &Client{
		endpoints: endpoints,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   12 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("web search endpoint redirects are disabled")
			},
		},
		searchSlot: make(chan struct{}, 1),
	}
	client.searchSlot <- struct{}{}
	return client, nil
}

func (c *Client) Execute(ctx context.Context, call core.ToolCall) core.ToolExecution {
	execution := core.ToolExecution{ToolCallID: call.ID, Status: core.StatusSucceeded, Idempotent: true, Output: map[string]any{}}
	input, err := inputFromArguments(call.Arguments)
	if err != nil {
		execution.Status = core.StatusFailed
		execution.ErrorCode = "WEB_SEARCH_INPUT_INVALID"
		execution.Output["reason"] = err.Error()
		return execution
	}
	response, err := c.Search(ctx, input)
	if err != nil {
		execution.Status = core.StatusFailed
		execution.ErrorCode = "WEB_SEARCH_UNAVAILABLE"
		if errors.Is(err, context.Canceled) {
			execution.ErrorCode = "TOOL_CANCELED"
		} else if errors.Is(err, context.DeadlineExceeded) {
			execution.ErrorCode = "WEB_SEARCH_TIMEOUT"
		}
		execution.Output["query"] = input.Query
		execution.Output["reason"] = safeError(err)
		return execution
	}
	execution.Output = map[string]any{
		"query":        response.Query,
		"provider":     response.Provider,
		"result_count": len(response.Results),
		"results":      response.Results,
		"duration_ms":  response.Duration,
		"truncated":    response.Truncated,
	}
	return execution
}

func (c *Client) Search(ctx context.Context, input Input) (Response, error) {
	if c == nil || len(c.endpoints) == 0 || c.httpClient == nil {
		return Response{}, errors.New("web search client is not configured")
	}
	// Public HTML search endpoints rate-limit or serve challenge pages when a
	// tool batch starts several requests at once. Keep the Agent's tool batch
	// parallelism, but serialize this shared external adapter so every query
	// receives a parseable provider response.
	if c.searchSlot != nil {
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-c.searchSlot:
		}
		defer func() { c.searchSlot <- struct{}{} }()
	}
	input.Query = strings.TrimSpace(input.Query)
	if len([]rune(input.Query)) < 2 || len([]rune(input.Query)) > 500 {
		return Response{}, errors.New("query must contain 2 to 500 characters")
	}
	if input.MaxResults == 0 {
		input.MaxResults = 5
	}
	if input.MaxResults < 1 || input.MaxResults > 10 {
		return Response{}, errors.New("max_results must be from 1 to 10")
	}
	query := input.Query
	for _, domain := range input.AllowedDomains {
		query += " site:" + domain
	}
	for _, domain := range input.BlockedDomains {
		query += " -site:" + domain
	}
	var failures []string
	for _, endpoint := range c.endpoints {
		response, err := c.searchEndpoint(ctx, endpoint, input, query)
		if err == nil {
			return response, nil
		}
		failures = append(failures, endpoint.Hostname()+": "+safeError(err))
		if ctx.Err() != nil {
			break
		}
	}
	return Response{}, fmt.Errorf("all configured search providers failed: %s", strings.Join(failures, "; "))
}

func (c *Client) searchEndpoint(ctx context.Context, endpoint *url.URL, input Input, query string) (Response, error) {
	requestURL := *endpoint
	values := requestURL.Query()
	values.Set("q", query)
	provider := "duckduckgo_html"
	if strings.EqualFold(endpoint.Hostname(), "www.bing.com") {
		values.Set("format", "rss")
		provider = "bing_rss"
	} else if code := freshnessCode(input.Freshness); code != "" {
		values.Set("df", code)
	}
	requestURL.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return Response{}, err
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.Header.Set("User-Agent", "Oktapod/0.1 (+controlled-web-search)")
	started := time.Now()
	response, err := c.httpClient.Do(request)
	if err != nil {
		return Response{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Response{}, fmt.Errorf("search provider returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return Response{}, err
	}
	if len(body) > maxResponseSize {
		return Response{}, errors.New("search provider response exceeded 2 MiB")
	}
	var results []Result
	parseLimit := input.MaxResults + 1
	if provider == "bing_rss" {
		results, err = parseRSSResults(body, parseLimit, input.AllowedDomains, input.BlockedDomains)
	} else {
		results, err = parseResults(body, parseLimit, input.AllowedDomains, input.BlockedDomains)
	}
	if err != nil {
		return Response{}, err
	}
	if len(results) == 0 {
		return Response{}, errors.New("search provider returned no usable results")
	}
	truncated := len(results) > input.MaxResults
	if truncated {
		results = results[:input.MaxResults]
	}
	return Response{Query: input.Query, Provider: provider, Results: results, Duration: time.Since(started).Milliseconds(), Truncated: truncated}, nil
}

func parseRSSResults(body []byte, limit int, allowed, blocked []string) ([]Result, error) {
	var feed struct {
		Channel struct {
			Items []struct {
				Title       string `xml:"title"`
				Link        string `xml:"link"`
				Description string `xml:"description"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, errors.New("search provider returned invalid RSS")
	}
	results := make([]Result, 0, limit)
	seen := make(map[string]struct{})
	for _, item := range feed.Channel.Items {
		target, err := normalizeResultURL(item.Link)
		if err != nil || !domainAllowed(target.Hostname(), allowed, blocked) {
			continue
		}
		canonical := target.String()
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		results = append(results, Result{
			Title: boundedText(item.Title, 300), URL: canonical,
			Snippet: boundedText(item.Description, 800), Domain: strings.ToLower(target.Hostname()),
		})
		if len(results) >= limit {
			break
		}
	}
	return results, nil
}

func inputFromArguments(arguments map[string]any) (Input, error) {
	input := Input{MaxResults: 5}
	if len(arguments) != 1 {
		return Input{}, errors.New("web_search accepts only query")
	}
	query, ok := arguments["query"].(string)
	if !ok {
		return Input{}, errors.New("query is required")
	}
	input.Query = strings.TrimSpace(query)
	return input, nil
}

func parseResults(body []byte, limit int, allowed, blocked []string) ([]Result, error) {
	document, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("search provider returned invalid HTML")
	}
	results := make([]Result, 0, limit)
	seen := make(map[string]struct{})
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if len(results) >= limit {
			return
		}
		if node.Type == html.ElementNode && node.Data == "div" && hasClass(node, "result") {
			titleNode := findByClass(node, "result__a")
			if titleNode != nil {
				href := attribute(titleNode, "href")
				target, normalizeErr := normalizeResultURL(href)
				if normalizeErr == nil && domainAllowed(target.Hostname(), allowed, blocked) {
					canonical := target.String()
					if _, exists := seen[canonical]; !exists {
						seen[canonical] = struct{}{}
						snippet := ""
						if snippetNode := findByClass(node, "result__snippet"); snippetNode != nil {
							snippet = boundedText(textContent(snippetNode), 800)
						}
						results = append(results, Result{Title: boundedText(textContent(titleNode), 300), URL: canonical, Snippet: snippet, Domain: strings.ToLower(target.Hostname())})
					}
				}
			}
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
			if len(results) >= limit {
				return
			}
		}
	}
	walk(document)
	return results, nil
}

func normalizeResultURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(parsed.Hostname(), "duckduckgo.com") && parsed.Query().Get("uddg") != "" {
		parsed, err = url.Parse(parsed.Query().Get("uddg"))
		if err != nil {
			return nil, err
		}
	}
	if parsed.User != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, errors.New("result URL is not public HTTP(S)")
	}
	parsed.Fragment = ""
	return parsed, nil
}

func domainAllowed(host string, allowed, blocked []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, domain := range blocked {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return false
		}
	}
	if len(allowed) == 0 {
		return true
	}
	for _, domain := range allowed {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func freshnessCode(value string) string {
	switch value {
	case "day":
		return "d"
	case "week":
		return "w"
	case "month":
		return "m"
	case "year":
		return "y"
	default:
		return ""
	}
}

func findByClass(node *html.Node, class string) *html.Node {
	if node.Type == html.ElementNode && hasClass(node, class) {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := findByClass(child, class); found != nil {
			return found
		}
	}
	return nil
}

func hasClass(node *html.Node, class string) bool {
	for _, attribute := range node.Attr {
		if attribute.Key == "class" {
			for _, value := range strings.Fields(attribute.Val) {
				if value == class {
					return true
				}
			}
		}
	}
	return false
}

func attribute(node *html.Node, name string) string {
	for _, item := range node.Attr {
		if item.Key == name {
			return item.Val
		}
	}
	return ""
}

func textContent(node *html.Node) string {
	if node.Type == html.TextNode {
		return node.Data
	}
	var builder strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		builder.WriteString(textContent(child))
		builder.WriteByte(' ')
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

func boundedText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func safeError(err error) string {
	message := err.Error()
	if len([]rune(message)) > 300 {
		return string([]rune(message)[:300])
	}
	return message
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
