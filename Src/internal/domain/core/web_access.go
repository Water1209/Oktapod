package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const WebAccessPolicyVersion = 1

type WebPrivateOrigin struct {
	ID     string `json:"id" yaml:"id"`
	Origin string `json:"origin" yaml:"origin"`
}

// WebAccessPolicy is non-secret bootstrap policy. It is injected into the
// application policy and HTTP adapter; models never receive or mutate it.
type WebAccessPolicy struct {
	PrivateFetchEnabled bool               `json:"private_fetch_enabled"`
	AllowedOrigins      []WebPrivateOrigin `json:"allowed_origins"`
	ListenOrigin        string             `json:"-"`
}

type WebAccessPolicySnapshot struct {
	Version             int      `json:"web_access_policy_version"`
	PrivateFetchEnabled bool     `json:"private_fetch_enabled"`
	AllowedOriginHashes []string `json:"allowed_origin_hashes"`
	PolicyHash          string   `json:"web_access_policy_hash"`
}

type WebFetchPolicyDecision struct {
	CanonicalURL    string
	CanonicalOrigin string
	NetworkClass    string
	RuleID          string
	RiskLevel       string
	Decision        string
	Reason          string
	PolicyHash      string
	GrantKey        string
}

type WebSearchRequest struct {
	Query          string
	MaxResults     int
	Freshness      string
	AllowedDomains []string
	BlockedDomains []string
}

type WebSearchSource struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
	Domain  string `json:"domain"`
}

type WebSearchResult struct {
	Query     string            `json:"query"`
	Provider  string            `json:"provider"`
	Results   []WebSearchSource `json:"results"`
	Duration  int64             `json:"duration_ms"`
	Truncated bool              `json:"truncated"`
}

type WebFetchRequest struct {
	URL            string
	PolicySnapshot string
}

type WebFetchResult struct {
	FinalURL           string `json:"final_url"`
	StatusCode         int    `json:"status_code"`
	MediaType          string `json:"media_type"`
	ContentFormat      string `json:"content_format"`
	Content            string `json:"content"`
	Truncated          bool   `json:"truncated"`
	ConversionDegraded bool   `json:"conversion_degraded,omitempty"`
	Duration           int64  `json:"duration_ms"`
	ResponseBytes      int64  `json:"response_bytes"`
	NetworkClass       string `json:"network_class"`
	MatchedRuleID      string `json:"matched_rule_id,omitempty"`
	PolicyHash         string `json:"policy_hash,omitempty"`
	GrantKey           string `json:"grant_key,omitempty"`
}

func (policy WebAccessPolicy) Validate() (WebAccessPolicy, error) {
	policy.ListenOrigin = strings.TrimSpace(policy.ListenOrigin)
	if len(policy.AllowedOrigins) > 64 {
		return WebAccessPolicy{}, errors.New("web_access.private_fetch.allowed_origins accepts at most 64 entries")
	}
	if policy.PrivateFetchEnabled && len(policy.AllowedOrigins) == 0 {
		return WebAccessPolicy{}, errors.New("web_access.private_fetch.enabled requires at least one allowed origin")
	}
	seenIDs := make(map[string]struct{}, len(policy.AllowedOrigins))
	seenOrigins := make(map[string]struct{}, len(policy.AllowedOrigins))
	normalized := make([]WebPrivateOrigin, 0, len(policy.AllowedOrigins))
	for _, candidate := range policy.AllowedOrigins {
		id := strings.TrimSpace(candidate.ID)
		if !validWebOriginID(id) {
			return WebAccessPolicy{}, fmt.Errorf("web private origin id %q must contain 1-64 lowercase letters, digits, hyphens, or underscores", id)
		}
		if _, exists := seenIDs[id]; exists {
			return WebAccessPolicy{}, fmt.Errorf("web private origin id %q is duplicated", id)
		}
		canonical, address, err := normalizeConfiguredPrivateOrigin(candidate.Origin)
		if err != nil {
			return WebAccessPolicy{}, fmt.Errorf("web private origin %q: %w", id, err)
		}
		if permanentWebAddressDenied(address) {
			return WebAccessPolicy{}, fmt.Errorf("web private origin %q uses a permanently blocked address", id)
		}
		if !nonPublicWebAddress(address) {
			return WebAccessPolicy{}, fmt.Errorf("web private origin %q must use a private, loopback, CGNAT, or IPv6 ULA address", id)
		}
		if policy.ListenOrigin != "" && isOktapodListenOrigin(canonical, policy.ListenOrigin) {
			return WebAccessPolicy{}, fmt.Errorf("web private origin %q cannot target the Oktapod listen origin", id)
		}
		if _, exists := seenOrigins[canonical]; exists {
			return WebAccessPolicy{}, fmt.Errorf("web private origin %q duplicates another configured origin", id)
		}
		seenIDs[id] = struct{}{}
		seenOrigins[canonical] = struct{}{}
		normalized = append(normalized, WebPrivateOrigin{ID: id, Origin: canonical})
	}
	sort.Slice(normalized, func(left, right int) bool { return normalized[left].ID < normalized[right].ID })
	policy.AllowedOrigins = normalized
	return policy, nil
}

func (policy WebAccessPolicy) Snapshot() WebAccessPolicySnapshot {
	validated, err := policy.Validate()
	if err != nil {
		validated = WebAccessPolicy{ListenOrigin: policy.ListenOrigin}
	}
	hashes := make([]string, 0, len(validated.AllowedOrigins))
	for _, item := range validated.AllowedOrigins {
		hashes = append(hashes, HashWebOrigin(item.Origin))
	}
	sort.Strings(hashes)
	payload := struct {
		Version int                `json:"version"`
		Enabled bool               `json:"enabled"`
		Origins []WebPrivateOrigin `json:"origins"`
	}{Version: WebAccessPolicyVersion, Enabled: validated.PrivateFetchEnabled, Origins: validated.AllowedOrigins}
	body, _ := json.Marshal(payload)
	return WebAccessPolicySnapshot{
		Version: WebAccessPolicyVersion, PrivateFetchEnabled: validated.PrivateFetchEnabled,
		AllowedOriginHashes: hashes, PolicyHash: sha256Reference(body),
	}
}

func (snapshot WebAccessPolicySnapshot) AddTo(target map[string]any) {
	if target == nil {
		return
	}
	target["web_access_policy_version"] = snapshot.Version
	target["private_fetch_enabled"] = snapshot.PrivateFetchEnabled
	target["allowed_origin_hashes"] = append([]string(nil), snapshot.AllowedOriginHashes...)
	target["web_access_policy_hash"] = snapshot.PolicyHash
}

func WebAccessPolicySnapshotFromJSON(raw string) WebAccessPolicySnapshot {
	var value struct {
		Version             int      `json:"web_access_policy_version"`
		PrivateFetchEnabled bool     `json:"private_fetch_enabled"`
		AllowedOriginHashes []string `json:"allowed_origin_hashes"`
		PolicyHash          string   `json:"web_access_policy_hash"`
	}
	if json.Unmarshal([]byte(raw), &value) != nil {
		return WebAccessPolicySnapshot{}
	}
	return WebAccessPolicySnapshot(value)
}

func (policy WebAccessPolicy) EvaluateFetch(snapshotJSON, rawURL string) WebFetchPolicyDecision {
	canonicalURL, canonicalOrigin, address, explicitIP, err := NormalizeWebFetchURL(rawURL)
	if err != nil {
		return deniedWebFetch("WEB_FETCH_INPUT_INVALID: " + err.Error())
	}
	if policy.ListenOrigin != "" && isOktapodListenOrigin(canonicalOrigin, policy.ListenOrigin) {
		return deniedWebFetch("WEB_FETCH_BLOCKED_URL: destination is the Oktapod listen origin")
	}
	port := originPort(canonicalOrigin)
	if !explicitIP {
		if port != "80" && port != "443" {
			return deniedWebFetch("WEB_FETCH_BLOCKED_URL: public host names are limited to ports 80 and 443")
		}
		return WebFetchPolicyDecision{CanonicalURL: canonicalURL, CanonicalOrigin: canonicalOrigin, NetworkClass: "public", RiskLevel: "L0", Decision: PolicyAllow, Reason: "Bounded public HTTP fetch is read-only and uses the public network guard"}
	}
	if permanentWebAddressDenied(address) {
		return deniedWebFetch("WEB_FETCH_BLOCKED_URL: destination address is permanently blocked")
	}
	if publicWebAddress(address) {
		if port != "80" && port != "443" {
			return deniedWebFetch("WEB_FETCH_BLOCKED_URL: public addresses are limited to ports 80 and 443")
		}
		return WebFetchPolicyDecision{CanonicalURL: canonicalURL, CanonicalOrigin: canonicalOrigin, NetworkClass: "public", RiskLevel: "L0", Decision: PolicyAllow, Reason: "Bounded public HTTP fetch is read-only and uses the public network guard"}
	}
	if !nonPublicWebAddress(address) {
		return deniedWebFetch("WEB_FETCH_BLOCKED_URL: destination address is not globally routable")
	}
	validated, err := policy.Validate()
	if err != nil || !validated.PrivateFetchEnabled {
		return deniedWebFetch("WEB_FETCH_BLOCKED_URL: private fetch is disabled")
	}
	currentSnapshot := validated.Snapshot()
	runSnapshot := WebAccessPolicySnapshotFromJSON(snapshotJSON)
	if runSnapshot.Version != WebAccessPolicyVersion || !runSnapshot.PrivateFetchEnabled || runSnapshot.PolicyHash == "" {
		return deniedWebFetch("WEB_FETCH_BLOCKED_URL: private fetch was not frozen into this Run")
	}
	originHash := HashWebOrigin(canonicalOrigin)
	if !containsString(runSnapshot.AllowedOriginHashes, originHash) {
		return deniedWebFetch("WEB_FETCH_BLOCKED_URL: private origin was not allowed when this Run started")
	}
	for _, item := range validated.AllowedOrigins {
		if item.Origin != canonicalOrigin {
			continue
		}
		grantPayload, _ := json.Marshal(map[string]any{"kind": "run_origin", "origin": canonicalOrigin, "policy_hash": currentSnapshot.PolicyHash, "tool": ToolWebFetch, "version": 1})
		return WebFetchPolicyDecision{
			CanonicalURL: canonicalURL, CanonicalOrigin: canonicalOrigin, NetworkClass: "private", RuleID: item.ID,
			RiskLevel: "L2", Decision: PolicyAsk, Reason: "Private network reads require one human approval per Run and origin",
			PolicyHash: currentSnapshot.PolicyHash, GrantKey: sha256Reference(grantPayload),
		}
	}
	return deniedWebFetch("WEB_FETCH_BLOCKED_URL: private origin is not in the current allowlist")
}

func NormalizeWebFetchURL(raw string) (canonicalURL, canonicalOrigin string, address netip.Addr, explicitIP bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 || strings.ContainsRune(raw, 0) {
		return "", "", netip.Addr{}, false, errors.New("url must contain 1 to 2048 characters")
	}
	parsed, parseErr := url.Parse(raw)
	if parseErr != nil {
		return "", "", netip.Addr{}, false, errors.New("url must be an absolute HTTP(S) URL without credentials")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", "", netip.Addr{}, false, errors.New("url must be an absolute HTTP(S) URL without credentials")
	}
	if parsed.Opaque != "" {
		return "", "", netip.Addr{}, false, errors.New("opaque URLs are not supported")
	}
	parsed.Fragment = ""
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	number, portErr := strconv.Atoi(port)
	if portErr != nil || number < 1 || number > 65535 {
		return "", "", netip.Addr{}, false, errors.New("url port must be an integer from 1 to 65535")
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	parsed.Host = net.JoinHostPort(host, port)
	if candidate, parseIPErr := netip.ParseAddr(host); parseIPErr == nil {
		if candidate.Zone() != "" {
			return "", "", netip.Addr{}, false, errors.New("scoped IPv6 addresses are not supported")
		}
		address = candidate.Unmap()
		explicitIP = true
	}
	canonicalOrigin = parsed.Scheme + "://" + parsed.Host
	return parsed.String(), canonicalOrigin, address, explicitIP, nil
}

func HashWebOrigin(origin string) string {
	return sha256Reference([]byte(strings.TrimSpace(origin)))
}

func normalizeConfiguredPrivateOrigin(raw string) (string, netip.Addr, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", netip.Addr{}, errors.New("origin must be an absolute HTTP(S) origin without credentials")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", netip.Addr{}, errors.New("origin must be an absolute HTTP(S) origin without credentials")
	}
	if parsed.Port() == "" {
		return "", netip.Addr{}, errors.New("origin must include an explicit port")
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return "", netip.Addr{}, errors.New("origin cannot contain a path, query, or fragment")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	address, err := netip.ParseAddr(host)
	if err != nil {
		return "", netip.Addr{}, errors.New("origin host must be a literal IPv4 or IPv6 address")
	}
	if address.Zone() != "" {
		return "", netip.Addr{}, errors.New("origin cannot use a scoped IPv6 address")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", netip.Addr{}, errors.New("origin port must be an integer from 1 to 65535")
	}
	address = address.Unmap()
	return strings.ToLower(parsed.Scheme) + "://" + net.JoinHostPort(address.String(), strconv.Itoa(port)), address, nil
}

func nonPublicWebAddress(address netip.Addr) bool {
	if !address.IsValid() {
		return false
	}
	cgnat := netip.MustParsePrefix("100.64.0.0/10")
	return address.IsPrivate() || address.IsLoopback() || cgnat.Contains(address)
}

// publicWebAddress is intentionally stricter than netip.Addr.IsGlobalUnicast:
// that method also accepts documentation and benchmarking ranges. Fetch may
// use only globally routable destinations, never local or synthetic aliases.
func publicWebAddress(address netip.Addr) bool {
	if !address.IsValid() {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || nonPublicWebAddress(address) || permanentWebAddressDenied(address) {
		return false
	}
	prefixes := []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("192.88.99.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("100::/64"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("3fff::/20"),
	}
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func permanentWebAddressDenied(address netip.Addr) bool {
	if !address.IsValid() {
		return true
	}
	if address.IsUnspecified() || address.IsMulticast() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() {
		return true
	}
	for _, blocked := range []netip.Addr{
		netip.MustParseAddr("255.255.255.255"),
		netip.MustParseAddr("169.254.169.254"),
		netip.MustParseAddr("100.100.100.200"),
		netip.MustParseAddr("fd00:ec2::254"),
	} {
		if address == blocked {
			return true
		}
	}
	return false
}

func sameCanonicalOrigin(left, right string) bool {
	leftURL, leftErr := url.Parse(strings.TrimSpace(left))
	rightURL, rightErr := url.Parse(strings.TrimSpace(right))
	if leftErr != nil || rightErr != nil {
		return false
	}
	return strings.EqualFold(leftURL.Scheme, rightURL.Scheme) && strings.EqualFold(leftURL.Host, rightURL.Host)
}

func isOktapodListenOrigin(candidate, listen string) bool {
	candidateURL, candidateErr := url.Parse(strings.TrimSpace(candidate))
	listenURL, listenErr := url.Parse(strings.TrimSpace(listen))
	if candidateErr != nil || listenErr != nil || !strings.EqualFold(candidateURL.Scheme, listenURL.Scheme) || candidateURL.Port() != listenURL.Port() {
		return false
	}
	listenAddress, err := netip.ParseAddr(listenURL.Hostname())
	if err == nil && listenAddress.IsUnspecified() {
		return true
	}
	return strings.EqualFold(candidateURL.Hostname(), listenURL.Hostname())
}

func originPort(origin string) string {
	parsed, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	return parsed.Port()
}

func deniedWebFetch(reason string) WebFetchPolicyDecision {
	return WebFetchPolicyDecision{RiskLevel: "L3", Decision: PolicyDeny, Reason: reason}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func sha256Reference(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validWebOriginID(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || (index > 0 && (char == '-' || char == '_')) {
			continue
		}
		return false
	}
	return true
}
