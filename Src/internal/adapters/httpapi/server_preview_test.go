package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTMLPreviewContentPolicyAllowsInlineScriptsOnly(t *testing.T) {
	recorder := httptest.NewRecorder()
	allowHTMLPreviewContent(recorder)

	policy := recorder.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "default-src 'none'") || !strings.Contains(policy, "script-src 'unsafe-inline'") {
		t.Fatalf("HTML preview policy does not allow isolated inline scripts: %q", policy)
	}
	for _, directive := range []string{"connect-src 'none'", "frame-src 'none'", "form-action 'none'", "object-src 'none'"} {
		if !strings.Contains(policy, directive) {
			t.Fatalf("HTML preview policy missing %q: %q", directive, policy)
		}
	}
	if recorder.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Fatalf("unexpected HTML preview frame policy: %q", recorder.Header().Get("X-Frame-Options"))
	}
}

func TestIsHTMLPreviewContent(t *testing.T) {
	for _, test := range []struct {
		name      string
		mediaType string
		want      bool
	}{
		{name: "game.html", mediaType: "text/html; charset=utf-8", want: true},
		{name: "game.htm", mediaType: "application/octet-stream", want: true},
		{name: "notes.md", mediaType: "text/plain; charset=utf-8", want: false},
		{name: "archive.zip", mediaType: "application/zip", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isHTMLPreviewContent(test.name, test.mediaType); got != test.want {
				t.Fatalf("isHTMLPreviewContent(%q, %q) = %v, want %v", test.name, test.mediaType, got, test.want)
			}
		})
	}
}
