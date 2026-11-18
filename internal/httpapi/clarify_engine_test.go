package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// Issue #228: clarification runs on CodeBuddy only (run evals: TestRunEvalOnKiroModel). A model of
// another CLI ("codex:…") must be refused up front, not silently swapped for
// the CodeBuddy default.
func TestClarifyModelEngineError(t *testing.T) {
	for _, id := range []string{"", "default", "glm-5.1", "public-model"} {
		if err := clarifyModelEngineError(id); err != nil {
			t.Fatalf("CodeBuddy model %q refused: %v", id, err)
		}
	}
	for _, id := range []string{"codex:gpt-5.5", "claude-code:sonnet"} {
		if err := clarifyModelEngineError(id); err != errClarifyNeedsCodeBuddy {
			t.Fatalf("model %q = %v, want errClarifyNeedsCodeBuddy", id, err)
		}
	}
}

func TestChatClarifyRefusesOtherCLIModel(t *testing.T) {
	called := false
	setClarifyExecForTest(func(context.Context, domain.Issue, chatClarifyRequest, func(string, any)) (any, string, map[string]any, error) {
		called = true
		return map[string]any{}, "{}", map[string]any{}, nil
	})
	t.Cleanup(func() { setClarifyExecForTest(nil) })

	_, h := clarifyTestServer(t)
	body, _ := json.Marshal(map[string]any{"projectId": "platform", "message": "加个徽章", "model": "codex:gpt-5.5"})
	req := httptest.NewRequest(http.MethodPost, "/api/chat-clarify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "只支持 CodeBuddy") {
		t.Fatalf("error does not explain the CodeBuddy limit: %s", rec.Body.String())
	}
	if called {
		t.Fatal("clarification started with a Codex model")
	}
}

// Clarification has no Kiro CLI path: a responsible user without a CodeBuddy
// key is refused before any session or checkout, with the reason spelled out.
func TestClarifyRefusesResponsibleWithoutCodeBuddyKey(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.PutUser(domain.User{ID: 7, LoginName: "kiro-only"})
	st.PutProject(domain.Project{ID: "kiro-p", OwnerID: 7})
	h := &streamHandlers{deps: &deps{repo: st}}
	_, _, _, err = h.executeClarify(context.Background(), domain.Issue{ProjectID: "kiro-p", Title: "x"}, chatClarifyRequest{Message: "加个徽章"}, nil)
	if err != errClarifyResponsibleKey || !strings.Contains(err.Error(), "只支持 CodeBuddy") {
		t.Fatalf("err = %v", err)
	}
}
