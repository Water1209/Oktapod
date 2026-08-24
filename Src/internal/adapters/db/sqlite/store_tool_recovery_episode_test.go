package sqlite

import "testing"

func TestToolRecoveryEpisodeCountResetsOnOrdinaryContinuation(t *testing.T) {
	if got := toolRecoveryEpisodeCount(`{"stage":"tool_recovery","tool_recovery_count":3}`); got != 3 {
		t.Fatalf("same recovery episode count=%d want 3", got)
	}
	if got := toolRecoveryEpisodeCount(`{"stage":"continue","source_step_id":"successful-tool"}`); got != 0 {
		t.Fatalf("ordinary continuation inherited recovery count=%d want 0", got)
	}
	if got := toolRecoveryEpisodeCount(`not-json`); got != 0 {
		t.Fatalf("invalid input count=%d want 0", got)
	}
}
