package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// D14: a run eval on a Kiro model runs on Kiro CLI, tool-free, without a
// CodeBuddy key; CodeBuddy still requires the user's own key.
func TestRunEvalOnKiroModel(t *testing.T) {
	defer func(previous func() bool) { kiroCLIInstalled = previous }(kiroCLIInstalled)
	kiroCLIInstalled = func() bool { return false }
	f := cvBNewHTTPFixture(t)
	run := domain.Run{ID: "kiro-eval-run", Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}, Nodes: map[string]*domain.NodeRun{}}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	post := func(model string) (int, string) {
		rec := cvBRequest(t, f, http.MethodPost, "/api/runs/"+run.ID+"/eval", map[string]any{"model": model}, true)
		return rec.Code, rec.Body.String()
	}
	if code, body := post("glm-5.1"); code != http.StatusBadRequest || !strings.Contains(body, "CodeBuddy API Key") {
		t.Fatalf("CodeBuddy without key = %d %s", code, body)
	}
	if code, body := post("kiro:auto"); code != http.StatusBadRequest || !strings.Contains(body, "kiro-cli") {
		t.Fatalf("Kiro without kiro-cli = %d %s", code, body)
	}
	if code, body := post("codex:gpt-5.5"); code != http.StatusBadRequest || !strings.Contains(body, "Codex API Key") {
		t.Fatalf("Codex without key = %d %s", code, body)
	}
	if _, busy := f.s.ActiveRunEval(run.ID); busy {
		t.Fatal("a refused eval must not start")
	}

	kiroCLIInstalled = func() bool { return true }
	dims := map[string]int{}
	for _, d := range domain.EffectiveRunEvalPolicy(domain.RunEvalPolicy{}).Dimensions {
		dims[d.ID] = 91
	}
	answer, _ := json.Marshal(map[string]any{"summary": "按计划交付", "dimensions": dims, "findings": []any{}, "recommendations": []any{}})
	argvFile := fakeKiroCLI(t, string(answer))
	if code, body := post("kiro:auto"); code != http.StatusAccepted {
		t.Fatalf("Kiro eval = %d %s", code, body)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		eval, ok := f.s.LatestRunEval(run.ID)
		if ok && eval.Status != domain.RunEvalRunning {
			if eval.Status != domain.RunEvalSucceeded || eval.Model != "kiro:auto" || eval.Score != 91 {
				t.Fatalf("eval = %+v", eval)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("eval did not finish: %+v", eval)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if argv := readFakeArgv(t, argvFile); !slices.Contains(argv, "--trust-tools=") || !slices.Contains(argv, "auto") {
		t.Fatalf("eval must run tool-free on the chosen model: %q", argv)
	}

	// CodeBuddy with a key still starts as before.
	if _, err := f.s.UpdateUserCredentials(f.user.ID, repo.UserCredentialUpdate{CodeBuddyAPIKey: "eval-key"}); err != nil {
		t.Fatal(err)
	}
	setRunEvalExecForTest(func(_ context.Context, rec domain.RunEvalRecord, _ domain.Run, _ domain.User, _ func(string, any)) (domain.RunEvalResult, error) {
		if !assistantCodeBuddyModel(rec.Model) {
			t.Errorf("CodeBuddy eval model = %q", rec.Model)
		}
		return domain.RunEvalResult{Score: 1}, nil
	})
	t.Cleanup(func() { setRunEvalExecForTest(nil) })
	if code, body := post(""); code != http.StatusAccepted {
		t.Fatalf("CodeBuddy eval = %d %s", code, body)
	}
}
