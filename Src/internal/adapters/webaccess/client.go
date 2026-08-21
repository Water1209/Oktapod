package webaccess

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const (
	maxResponseBytes = 2 << 20
	maxConvertBytes  = 256 << 10
	maxContentBytes  = 24 << 10
	maxHTMLDepth     = 512
	maxRedirects     = 5
)

type Searcher interface {
	Search(context.Context, core.WebSearchRequest) (core.WebSearchResult, error)
}

type Client struct {
	search   Searcher
	policy   core.WebAccessPolicy
	lookupIP func(context.Context, string) ([]netip.Addr, error)
	dialer   net.Dialer
}

type Error struct {
	Code           string
	Reason         string
	RedirectTarget string
}

func (err *Error) Error() string {
	if err == nil {
		return ""
	}
	return err.Reason
}

func (err *Error) ErrorCode() string {
	if err == nil {
		return ""
	}
	return err.Code
}

func (err *Error) RedirectURL() string {
	if err == nil {
		return ""
	}
	return err.RedirectTarget
}

func New(search Searcher, policy core.WebAccessPolicy) (*Client, error) {
	validated, err := policy.Validate()
	if err != nil {
		return nil, err
	}
	return &Client{
		search: search, policy: validated,
		lookupIP: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		dialer: net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second},
	}, nil
}

func (client *Client) Search(ctx context.Context, request core.WebSearchRequest) (core.WebSearchResult, error) {
	if client == nil || client.search == nil {
		return core.WebSearchResult{}, &Error{Code: "WEB_SEARCH_NOT_CONFIGURED", Reason: "web search is not configured"}
	}
	return client.search.Search(ctx, request)
}

func (client *Client) EvaluateFetch(snapshotJSON, rawURL string) core.WebFetchPolicyDecision {
	if client == nil {
		return core.WebFetchPolicyDecision{RiskLevel: "L3", Decision: core.PolicyDeny, Reason: "WEB_FETCH_NOT_CONFIGURED: web access is not configured"}
	}
	return client.policy.EvaluateFetch(snapshotJSON, rawURL)
}

func (client *Client) Fetch(ctx context.Context, request core.WebFetchRequest) (core.WebFetchResult, error) {
	if client == nil {
		return core.WebFetchResult{}, &Error{Code: "WEB_FETCH_NOT_CONFIGURED", Reason: "web access is not configured"}
	}
	decision := client.policy.EvaluateFetch(request.PolicySnapshot, request.URL)
	if decision.Decision == core.PolicyDeny {
		return core.WebFetchResult{}, policyError(decision.Reason)
	}
	target, err := url.Parse(decision.CanonicalURL)
	if err != nil {
		return core.WebFetchResult{}, &Error{Code: "WEB_FETCH_INPUT_INVALID", Reason: "web fetch URL could not be normalized"}
	}
	boundedCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	httpClient := client.httpClient(request.PolicySnapshot, decision)
	httpRequest, err := http.NewRequestWithContext(boundedCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return core.WebFetchResult{}, &Error{Code: "WEB_FETCH_INPUT_INVALID", Reason: "web fetch request could not be created"}
	}
	httpRequest.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json,application/xml,text/xml;q=0.9,*/*;q=0.1")
	httpRequest.Header.Set("User-Agent", "Oktapod/1.0 (+controlled-web-fetch)")
	started := time.Now()
	response, err := httpClient.Do(httpRequest)
	if err != nil {
		return core.WebFetchResult{}, classifyTransportError(boundedCtx, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return core.WebFetchResult{}, classifyTransportError(boundedCtx, err)
	}
	if len(body) > maxResponseBytes {
		return core.WebFetchResult{}, &Error{Code: "WEB_FETCH_TOO_LARGE", Reason: "web response exceeded the 2 MiB decompressed limit"}
	}
	mediaType, parameters := parseMediaType(response.Header.Get("Content-Type"), body)
	if !supportedMediaType(mediaType) {
		return core.WebFetchResult{}, &Error{Code: "WEB_FETCH_UNSUPPORTED_CONTENT_TYPE", Reason: "web response content type is not supported"}
	}
	decoded, err := decodeText(bytes.NewReader(body), mediaType, parameters)
	if err != nil {
		return core.WebFetchResult{}, &Error{Code: "WEB_FETCH_DECODE_FAILED", Reason: "web response character encoding is invalid"}
	}
	conversionInput, inputTruncated := truncateUTF8(decoded, maxConvertBytes, "")
	content, format, degraded, renderErr := renderContent(conversionInput, mediaType, response.Request.URL.String())
	if renderErr != nil {
		degraded = true
		content = extractPlainText(conversionInput)
		format = "text"
	}
	content, outputTruncated := truncateUTF8(content, maxContentBytes, "\n\n[内容已截断]")
	finalDecision := client.policy.EvaluateFetch(request.PolicySnapshot, response.Request.URL.String())
	if finalDecision.Decision == core.PolicyDeny {
		return core.WebFetchResult{}, policyError(finalDecision.Reason)
	}
	return core.WebFetchResult{
		FinalURL: response.Request.URL.String(), StatusCode: response.StatusCode, MediaType: mediaType,
		ContentFormat: format, Content: content, Truncated: inputTruncated || outputTruncated,
		ConversionDegraded: degraded, Duration: time.Since(started).Milliseconds(), ResponseBytes: int64(len(body)),
		NetworkClass: finalDecision.NetworkClass, MatchedRuleID: finalDecision.RuleID,
		PolicyHash: finalDecision.PolicyHash, GrantKey: finalDecision.GrantKey,
	}, nil
}

func (client *Client) httpClient(snapshotJSON string, initial core.WebFetchPolicyDecision) *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   2,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, &Error{Code: "WEB_FETCH_BLOCKED_URL", Reason: "web destination address is invalid"}
			}
			ips, err := client.resolve(ctx, host)
			if err != nil {
				return nil, &Error{Code: "WEB_FETCH_UNAVAILABLE", Reason: "web destination could not be resolved"}
			}
			if len(ips) == 0 {
				return nil, &Error{Code: "WEB_FETCH_UNAVAILABLE", Reason: "web destination has no IP address"}
			}
			for _, ip := range ips {
				candidate := initial.CanonicalOrigin
				if initial.NetworkClass == "public" {
					scheme := "http"
					if port == "443" {
						scheme = "https"
					}
					candidate = scheme + "://" + net.JoinHostPort(ip.String(), port)
				}
				checked := client.policy.EvaluateFetch(snapshotJSON, candidate)
				if checked.Decision == core.PolicyDeny || checked.NetworkClass != initial.NetworkClass {
					return nil, &Error{Code: "WEB_FETCH_BLOCKED_URL", Reason: "web destination resolved to an address outside the approved network class"}
				}
			}
			var lastErr error
			for _, ip := range ips {
				connection, dialErr := client.dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
					return connection, nil
				}
				lastErr = dialErr
			}
			return nil, lastErr
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(request *http.Request, previous []*http.Request) error {
			request.Header.Del("Authorization")
			request.Header.Del("Cookie")
			request.Header.Del("Referer")
			if len(previous) > maxRedirects {
				return &Error{Code: "WEB_FETCH_REDIRECT_LIMIT", Reason: "web response exceeded the five redirect limit"}
			}
			canonicalURL, canonicalOrigin, _, _, normalizeErr := core.NormalizeWebFetchURL(request.URL.String())
			if normalizeErr != nil {
				return &Error{Code: "WEB_FETCH_BLOCKED_URL", Reason: "redirect target URL is invalid"}
			}
			if canonicalOrigin != initial.CanonicalOrigin {
				return &Error{Code: "WEB_FETCH_REDIRECT_BLOCKED", Reason: "cross-origin redirect requires a new governed fetch", RedirectTarget: canonicalURL}
			}
			decision := client.policy.EvaluateFetch(snapshotJSON, canonicalURL)
			if decision.Decision == core.PolicyDeny {
				return policyError(decision.Reason)
			}
			return nil
		},
	}
}

func (client *Client) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{address.Unmap()}, nil
	}
	return client.lookupIP(ctx, host)
}

func renderContent(input, mediaType, finalURL string) (content, format string, degraded bool, err error) {
	if mediaType != "text/html" && mediaType != "application/xhtml+xml" {
		switch {
		case mediaType == "application/json" || strings.HasSuffix(mediaType, "+json"):
			return strings.TrimSpace(input), "json", false, nil
		case mediaType == "application/xml" || mediaType == "text/xml" || strings.HasSuffix(mediaType, "+xml"):
			return strings.TrimSpace(input), "xml", false, nil
		default:
			return strings.TrimSpace(input), "text", false, nil
		}
	}
	if htmlDepth(input) > maxHTMLDepth {
		return extractPlainText(input), "text", true, nil
	}
	conv := converter.NewConverter(converter.WithPlugins(
		base.NewBasePlugin(), commonmark.NewCommonmarkPlugin(), table.NewTablePlugin(),
	))
	markdown, err := conv.ConvertString(input, converter.WithDomain(finalURL))
	if err != nil {
		return "", "", true, err
	}
	return strings.TrimSpace(markdown), "markdown", false, nil
}

func htmlDepth(input string) int {
	tokenizer := html.NewTokenizer(strings.NewReader(input))
	depth, peak := 0, 0
	void := map[string]bool{"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true, "input": true, "link": true, "meta": true, "param": true, "source": true, "track": true, "wbr": true}
	for {
		token := tokenizer.Next()
		switch token {
		case html.ErrorToken:
			return peak
		case html.StartTagToken:
			name, _ := tokenizer.TagName()
			if !void[strings.ToLower(string(name))] {
				depth++
				if depth > peak {
					peak = depth
				}
			}
		case html.EndTagToken:
			if depth > 0 {
				depth--
			}
		}
	}
}

func extractPlainText(input string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(input))
	var output strings.Builder
	skipDepth := 0
	for {
		tokenType := tokenizer.Next()
		if tokenType == html.ErrorToken {
			break
		}
		token := tokenizer.Token()
		if tokenType == html.StartTagToken && (token.Data == "script" || token.Data == "style" || token.Data == "noscript") {
			skipDepth++
			continue
		}
		if tokenType == html.EndTagToken && skipDepth > 0 && (token.Data == "script" || token.Data == "style" || token.Data == "noscript") {
			skipDepth--
			continue
		}
		if skipDepth == 0 && tokenType == html.TextToken {
			text := strings.Join(strings.Fields(token.Data), " ")
			if text != "" {
				if output.Len() > 0 {
					output.WriteByte(' ')
				}
				output.WriteString(text)
			}
		}
	}
	return strings.TrimSpace(output.String())
}

func parseMediaType(header string, body []byte) (string, map[string]string) {
	mediaType, parameters, err := mime.ParseMediaType(header)
	if err == nil && mediaType != "" {
		return strings.ToLower(mediaType), parameters
	}
	detected, _, _ := mime.ParseMediaType(http.DetectContentType(body))
	return strings.ToLower(detected), map[string]string{}
}

func supportedMediaType(mediaType string) bool {
	return mediaType == "text/html" || mediaType == "application/xhtml+xml" || strings.HasPrefix(mediaType, "text/") ||
		mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") ||
		mediaType == "application/xml" || strings.HasSuffix(mediaType, "+xml")
}

func decodeText(reader io.Reader, mediaType string, parameters map[string]string) (string, error) {
	contentType := mediaType
	if encoding := strings.TrimSpace(parameters["charset"]); encoding != "" {
		contentType += "; charset=" + encoding
	}
	decoded, err := charset.NewReader(reader, contentType)
	if err != nil {
		return "", err
	}
	body, err := io.ReadAll(io.LimitReader(decoded, maxResponseBytes*4+1))
	if err != nil || len(body) > maxResponseBytes*4 || !utf8.Valid(body) {
		return "", errors.New("decoded text exceeds safety bound or is not UTF-8")
	}
	return string(body), nil
}

func truncateUTF8(value string, limit int, suffix string) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	keep := limit - len(suffix)
	if keep < 0 {
		keep = 0
	}
	for keep > 0 && !utf8.RuneStart(value[keep]) {
		keep--
	}
	return value[:keep] + suffix, true
}

func policyError(reason string) *Error {
	code := "WEB_FETCH_BLOCKED_URL"
	if prefix, _, ok := strings.Cut(reason, ":"); ok && strings.HasPrefix(prefix, "WEB_FETCH_") {
		code = prefix
	}
	return &Error{Code: code, Reason: reason}
}

func classifyTransportError(ctx context.Context, err error) error {
	var webErr *Error
	if errors.As(err, &webErr) {
		return webErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return &Error{Code: "TOOL_CANCELED", Reason: "web fetch was canceled"}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{Code: "WEB_FETCH_TIMEOUT", Reason: "web fetch exceeded the 30 second timeout"}
	}
	return &Error{Code: "WEB_FETCH_UNAVAILABLE", Reason: "web destination is unavailable"}
}

func SafeError(err error) (code, reason, redirectTarget string) {
	var webErr *Error
	if errors.As(err, &webErr) {
		return webErr.Code, webErr.Reason, webErr.RedirectTarget
	}
	return "WEB_FETCH_UNAVAILABLE", fmt.Sprint(err), ""
}
