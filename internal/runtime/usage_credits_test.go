package runtime

import (
	"strings"
	"testing"
)

// Credits reported by a subscription CLI travel on the result line and parse
// back into TokenUsage, tokens or not.
func TestClaudeStreamCarriesCredits(t *testing.T) {
	s := &ClaudeStream{}
	s.SetCredits(0.25)
	lines := s.Result("ok", false)
	if !strings.Contains(lines[len(lines)-1], `"credits":0.25`) {
		t.Fatalf("result: %q", lines)
	}
	if got := ParseTokenUsage(map[string]any{"credits": 0.25}); got.Credits != 0.25 {
		t.Fatalf("credits = %v", got.Credits)
	}
}
