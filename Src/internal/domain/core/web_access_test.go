package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWebAccessPolicyValidation(t *testing.T) {
	validOrigins := []WebPrivateOrigin{
		{ID: "rfc1918", Origin: "http://192.168.10.20:8080"},
		{ID: "loopback", Origin: "http://127.0.0.1:3000"},
		{ID: "cgnat", Origin: "http://100.64.10.20:9000"},
		{ID: "ipv6-ula", Origin: "https://[fd00::20]:8443"},
	}
	policy, err := (WebAccessPolicy{PrivateFetchEnabled: true, AllowedOrigins: validOrigins, ListenOrigin: "http://127.0.0.1:8787"}).Validate()
	if err != nil || len(policy.AllowedOrigins) != len(validOrigins) {
		t.Fatalf("valid policy rejected: policy=%#v err=%v", policy, err)
	}
	if _, err := (WebAccessPolicy{PrivateFetchEnabled: true}).Validate(); err == nil {
		t.Fatal("enabled private fetch without origins was accepted")
	}

	invalid := []WebPrivateOrigin{
		{ID: "domain", Origin: "http://intranet.local:8080"},
		{ID: "cidr", Origin: "http://192.168.0.0/24:8080"},
		{ID: "path", Origin: "http://192.168.0.2:8080/docs"},
		{ID: "no-port", Origin: "http://192.168.0.2"},
		{ID: "metadata", Origin: "http://169.254.169.254:80"},
		{ID: "metadata-cgnat", Origin: "http://100.100.100.200:80"},
		{ID: "link-local-v6", Origin: "http://[fe80::1]:8080"},
		{ID: "multicast", Origin: "http://224.0.0.1:8080"},
		{ID: "unspecified", Origin: "http://0.0.0.0:8080"},
		{ID: "public", Origin: "http://8.8.8.8:8080"},
		{ID: "self", Origin: "http://127.0.0.1:8787"},
	}
	for _, origin := range invalid {
		t.Run(origin.ID, func(t *testing.T) {
			_, err := (WebAccessPolicy{PrivateFetchEnabled: true, AllowedOrigins: []WebPrivateOrigin{origin}, ListenOrigin: "http://127.0.0.1:8787"}).Validate()
			if err == nil {
				t.Fatalf("invalid origin accepted: %#v", origin)
			}
		})
	}
	if _, err := (WebAccessPolicy{PrivateFetchEnabled: true, AllowedOrigins: []WebPrivateOrigin{{ID: "same", Origin: "http://10.0.0.1:80"}, {ID: "same", Origin: "http://10.0.0.2:80"}}}).Validate(); err == nil {
		t.Fatal("duplicate IDs were accepted")
	}
	if _, err := (WebAccessPolicy{PrivateFetchEnabled: true, AllowedOrigins: []WebPrivateOrigin{{ID: "one", Origin: "http://10.0.0.1:80"}, {ID: "two", Origin: "http://10.0.0.1:80"}}}).Validate(); err == nil {
		t.Fatal("duplicate origins were accepted")
	}
}

func TestWebAccessPolicyRunSnapshotIntersection(t *testing.T) {
	policy, err := (WebAccessPolicy{PrivateFetchEnabled: true, AllowedOrigins: []WebPrivateOrigin{
		{ID: "wiki", Origin: "http://192.168.10.20:8080"},
		{ID: "preview", Origin: "https://[fd00::20]:8443"},
	}}).Validate()
	if err != nil {
		t.Fatal(err)
	}
	snapshotMap := map[string]any{}
	policy.Snapshot().AddTo(snapshotMap)
	snapshotJSON := mustWebPolicyJSON(t, snapshotMap)

	decision := policy.EvaluateFetch(snapshotJSON, "http://192.168.10.20:8080/docs?q=1#part")
	if decision.Decision != PolicyAsk || decision.RiskLevel != "L2" || decision.NetworkClass != "private" || decision.RuleID != "wiki" || !strings.HasPrefix(decision.GrantKey, "sha256:") {
		t.Fatalf("private decision=%#v", decision)
	}
	if decision.CanonicalOrigin != "http://192.168.10.20:8080" || strings.Contains(decision.CanonicalURL, "#") {
		t.Fatalf("canonical target=%#v", decision)
	}
	for _, rawURL := range []string{"https://example.com/docs", "http://8.8.8.8/path"} {
		public := policy.EvaluateFetch(snapshotJSON, rawURL)
		if public.Decision != PolicyAllow || public.RiskLevel != "L0" || public.NetworkClass != "public" {
			t.Fatalf("public decision for %s: %#v", rawURL, public)
		}
	}
	for _, rawURL := range []string{"http://192.0.0.192/metadata", "http://192.0.2.1/", "https://[2001:db8::1]/"} {
		if got := policy.EvaluateFetch(snapshotJSON, rawURL); got.Decision != PolicyDeny {
			t.Fatalf("special-use address was treated as public for %s: %#v", rawURL, got)
		}
	}
	if got := policy.EvaluateFetch(snapshotJSON, "https://example.com:8443/"); got.Decision != PolicyDeny {
		t.Fatalf("public nonstandard port was not denied: %#v", got)
	}
	selfPolicy := WebAccessPolicy{ListenOrigin: "http://8.8.8.8:80"}
	if got := selfPolicy.EvaluateFetch(`{}`, "HTTP://8.8.8.8/path"); got.Decision != PolicyDeny || !strings.Contains(got.Reason, "listen origin") {
		t.Fatalf("public listen origin was not denied: %#v", got)
	}
	if got := policy.EvaluateFetch(snapshotJSON, "http://192.168.10.20:8081/"); got.Decision != PolicyDeny {
		t.Fatalf("different private port reused permission: %#v", got)
	}

	expanded, _ := (WebAccessPolicy{PrivateFetchEnabled: true, AllowedOrigins: append(append([]WebPrivateOrigin(nil), policy.AllowedOrigins...), WebPrivateOrigin{ID: "new", Origin: "http://10.0.0.8:8080"})}).Validate()
	if got := expanded.EvaluateFetch(snapshotJSON, "http://10.0.0.8:8080/"); got.Decision != PolicyDeny || !strings.Contains(got.Reason, "Run started") {
		t.Fatalf("new origin expanded an old Run: %#v", got)
	}
	if got := expanded.EvaluateFetch(snapshotJSON, "http://192.168.10.20:8080/changed-policy"); got.Decision != PolicyAsk || got.PolicyHash == policy.Snapshot().PolicyHash {
		t.Fatalf("intersection did not retain an old origin under a fresh policy hash: %#v", got)
	}
	removed, _ := (WebAccessPolicy{PrivateFetchEnabled: true, AllowedOrigins: []WebPrivateOrigin{{ID: "preview", Origin: "https://[fd00::20]:8443"}}}).Validate()
	if got := removed.EvaluateFetch(snapshotJSON, "http://192.168.10.20:8080/"); got.Decision != PolicyDeny {
		t.Fatalf("removed origin remained available: %#v", got)
	}
	if got := (WebAccessPolicy{}).EvaluateFetch(snapshotJSON, "http://192.168.10.20:8080/"); got.Decision != PolicyDeny {
		t.Fatalf("disabled current policy remained available: %#v", got)
	}
}

func mustWebPolicyJSON(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
