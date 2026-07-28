package websearch

import (
	"bytes"
	"context"
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
	DefaultEndpoint = "https://html.duckduckgo.com/html/"
	maxResponseSize = 2 << 20
)

type Input struct {
	Query          string
	MaxResults     int
	Freshness      string
	AllowedDomains []string
	BlockedDomains []string
}

type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
	Domain  string `json:"domain"`
}

type Response struct {
	Query    string   `json:"query"`
	Provider string   `json:"provider"`
	Results  []Result `json:"results"`
	Duration int64    `json:"duration_ms"`
}

type Client struct {
	endpoint   *url.URL
	httpClient *http.Client
}

func New(endpoint string) (*Client, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil {
		return nil, fmt.Errorf("web search endpoint must be an absolute HTTP(S) URL without credentials")
	}
	if parsed.Scheme != "https" && !isLoopbackHost(parsed.Hostname()) {
		return nil, fmt.Errorf("web search endpoint must use HTTPS unless it is loopback")
	}
	return &Client{
		endpoint: parsed,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("web search endpoint redirects are disabled")
			},
		},
	}, nil
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
	}
	return execution
}

func (c *Client) Search(ctx context.Context, input Input) (Response, error) {
	if c == nil || c.endpoint == nil || c.httpClient == nil {
		return Response{}, errors.New("web search client is not configured")
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
	requestURL := *c.endpoint
	values := requestURL.Query()
	values.Set("q", query)
	if code := freshnessCode(input.Freshness); code != "" {
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
	if err != nil && ctx.Err() == nil && retryableSearchError(err) {
		// Public HTML search endpoints occasionally drop a connection or delay
		// headers under burst traffic. One bounded retry keeps the user-facing
		// search reliable without hiding a persistent provider outage.
		retryRequest, retryErr := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
		if retryErr == nil {
			retryRequest.Header.Set("Accept", "text/html,application/xhtml+xml")
			retryRequest.Header.Set("User-Agent", "Oktapod/0.1 (+controlled-web-search)")
			response, err = c.httpClient.Do(retryRequest)
		}
	}
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
	results, err := parseResults(body, input.MaxResults, input.AllowedDomains, input.BlockedDomains)
	if err != nil {
		return Response{}, err
	}
	return Response{Query: input.Query, Provider: "duckduckgo_html", Results: results, Duration: time.Since(started).Milliseconds()}, nil
}

func retryableSearchError(err error) bool {
	if err == nil {
		return false
	}
	var networkError net.Error
	return errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary())
}

func inputFromArguments(arguments map[string]any) (Input, error) {
	input := Input{MaxResults: 5}
	query, ok := arguments["query"].(string)
	if !ok {
		return Input{}, errors.New("query is required")
	}
	input.Query = strings.TrimSpace(query)
	if value, ok := arguments["max_results"].(float64); ok {
		input.MaxResults = int(value)
	}
	input.Freshness, _ = arguments["freshness"].(string)
	var err error
	if input.AllowedDomains, err = stringList(arguments["allowed_domains"]); err != nil {
		return Input{}, fmt.Errorf("allowed_domains: %w", err)
	}
	if input.BlockedDomains, err = stringList(arguments["blocked_domains"]); err != nil {
		return Input{}, fmt.Errorf("blocked_domains: %w", err)
	}
	return input, nil
}

func stringList(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	raw, ok := value.([]any)
	if !ok {
		return nil, errors.New("must be an array")
	}
	values := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			return nil, errors.New("must contain strings")
		}
		values = append(values, strings.ToLower(strings.TrimSpace(text)))
	}
	return values, nil
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
