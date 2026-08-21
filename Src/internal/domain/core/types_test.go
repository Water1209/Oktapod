package core

import "testing"

func TestModelRetryLimitSeparatesTransportFromSemanticRecovery(t *testing.T) {
	if got := ModelRetryLimit("PROVIDER_UNAVAILABLE"); got != 1 {
		t.Fatalf("transport retry limit=%d", got)
	}
	if got := ModelRetryLimit("PROVIDER_TOOL_ARGUMENTS_INVALID"); got != DefaultModelRetryLimit {
		t.Fatalf("semantic retry limit=%d", got)
	}
}
