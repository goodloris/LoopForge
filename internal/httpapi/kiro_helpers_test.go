package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// fakeKiroCLI installs a kiro-cli that records its argv (one per line) and
// answers every prompt with finalText in Kiro's stream-json.
func fakeKiroCLI(t *testing.T, finalText string) (argvFile string) {
	t.Helper()
	dir := t.TempDir()
	argvFile = filepath.Join(dir, "argv")
	final, _ := json.Marshal(map[string]any{"type": "runFinished", "data": map[string]any{"sessionId": "sess_fake", "status": "success", "stopReason": "end_turn", "finalText": finalText}})
	stream := `{"type":"runStarted","data":{"payloadSchema":"acp","engine":"v3"}}` + "\n" + string(final) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "stream"), []byte(stream), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + argvFile + "\ncat " + filepath.Join(dir, "stream") + "\n"
	bin := filepath.Join(dir, "kiro-cli")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_KIRO_BIN", bin)
	t.Setenv("AGENTFLOW_KIRO_DATA_DIR", "")
	return argvFile
}

func readFakeArgv(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("kiro-cli did not run: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// D2: 「帮我批准」 in a Kiro conversation reviews on Kiro CLI with the
// conversation's model, tool-free, without any CodeBuddy key.
func TestApprovalReviewRunsOnKiroConversation(t *testing.T) {
	argvFile := fakeKiroCLI(t, `{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"用户明确要求保存该规则"}`)
	h := &assistantHandlers{reviewCatalog: pricedReviewCatalog}
	req := assistantRequest{Model: "kiro:claude-haiku-5.5", Messages: []assistantMessage{{Role: "user", AuthorID: 1, Content: "保存规则 demo"}}}
	result := h.approvalReviewer(req, domain.User{ID: 1}, nil)(context.Background(), "rules_save", "保存规则", map[string]any{"id": "demo"})
	if !result.allows() || result.Model != "kiro:claude-haiku-5.5" {
		t.Fatalf("review = %+v", result)
	}
	argv := readFakeArgv(t, argvFile)
	if !slices.Contains(argv, "--trust-tools=") || slices.Contains(argv, "--trust-all-tools") || !slices.Contains(argv, "claude-haiku-5.5") {
		t.Fatalf("review must be tool-free on the conversation model: %q", argv)
	}
}

// CodeBuddy conversations keep the cheapest-model catalog choice.
func TestApprovalReviewModelsKeepCodeBuddyCatalog(t *testing.T) {
	h := &assistantHandlers{reviewCatalog: pricedReviewCatalog}
	for _, req := range []assistantRequest{{}, {Model: "chat"}, {Executor: "codebuddy", Model: "chat"}} {
		if got := h.approvalReviewModels(req, domain.User{ID: 1}); !slices.Equal(got, []string{"free-review", "paid-review"}) {
			t.Fatalf("%+v: %v", req, got)
		}
	}
	if got := h.approvalReviewModels(assistantRequest{Executor: "kiro", Model: "auto"}, domain.User{ID: 1}); !slices.Equal(got, []string{"kiro:auto"}) {
		t.Fatal(got)
	}
}
