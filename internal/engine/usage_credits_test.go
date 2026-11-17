package engine

import (
	"testing"

	"agentflow/internal/domain"
)

// Credits accumulate per session cursor like tokens.
func TestApplyUsageAccumulatesCredits(t *testing.T) {
	run, nr := &domain.Run{}, &domain.NodeRun{}
	applyUsage(run, nr, "s", domain.TokenUsage{Credits: 0.5})
	applyUsage(run, nr, "s", domain.TokenUsage{Credits: 0.75})
	if run.Usage.Credits != 0.75 || nr.Usage.Credits != 0.75 {
		t.Fatalf("run=%v node=%v", run.Usage.Credits, nr.Usage.Credits)
	}
}
