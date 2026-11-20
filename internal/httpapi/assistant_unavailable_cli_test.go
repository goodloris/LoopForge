package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

// #37: every way into a turn (new generation, direct chat, steer) refuses an
// unavailable Kiro CLI with its own message instead of rewriting the turn,
// and the stored session, to CodeBuddy.
func TestUnavailableKiroRefusedAtEveryTurnEntry(t *testing.T) {
	defer func(previous func() bool) { kiroCLIInstalled = previous }(kiroCLIInstalled)
	body := func(session string) *bytes.Buffer {
		b, _ := json.Marshal(map[string]any{"projectId": "workbench", "sessionId": session, "executor": "kiro", "model": "kiro:auto", "messages": []map[string]string{{"role": "user", "content": "hi"}}})
		return bytes.NewBuffer(b)
	}
	for _, tc := range []struct {
		name string
		// run sends the refused turn; running is the generation already in
		// progress (steer only), "" otherwise.
		run func(t *testing.T, h *assistantHandlers, user domain.User, session string) (rec *httptest.ResponseRecorder, running string)
	}{
		{"generations", func(t *testing.T, h *assistantHandlers, user domain.User, session string) (*httptest.ResponseRecorder, string) {
			c, rec := workbenchContext(user, "POST", "/api/assistant/generations", body(session))
			h.startGeneration(c)
			return rec, ""
		}},
		{"chat", func(t *testing.T, h *assistantHandlers, user domain.User, session string) (*httptest.ResponseRecorder, string) {
			c, rec := workbenchContext(user, "POST", "/api/assistant/chat", body(session))
			h.chat(c)
			return rec, ""
		}},
		{"steer", func(t *testing.T, h *assistantHandlers, user domain.User, session string) (*httptest.ResponseRecorder, string) {
			g := startKiroGeneration(t, h, user, session)
			kiroCLIInstalled = func() bool { return false }
			c, rec := workbenchContext(user, "POST", "/api/assistant/sessions/"+session+"/generation/steer?generationId="+g.ID, bytes.NewBufferString(`{"content":"改一下","model":"kiro:auto"}`))
			c.Params = gin.Params{{Key: "id", Value: session}}
			h.steerGeneration(c)
			return rec, g.ID
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, user := workbenchFixture(t)
			// Only the steer case needs a turn in progress; a reply that
			// returns at once keeps a missing refusal from hanging chat().
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
				if tc.name != "steer" {
					return "ok", nil
				}
				select {
				case <-release:
				case <-ctx.Done():
				}
				return "", ctx.Err()
			}
			kiroCLIInstalled = func() bool { return false }
			session := "kiro-refused-" + tc.name
			rec, running := tc.run(t, h, user, session)
			var got struct{ Error string }
			_ = json.Unmarshal(rec.Body.Bytes(), &got)
			if rec.Code != 400 || got.Error != assistantEngineMissingKey(runtime.AgentCLIKiro) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if saved, ok := h.repo.AssistantSession(user.ID, session); ok && (saved.Executor == assistantEngineCodeBuddy || saved.Model == "default") {
				t.Fatalf("session fell back to CodeBuddy: executor=%q model=%q", saved.Executor, saved.Model)
			}
			g, ok := h.repo.AssistantGeneration(user.ID, session)
			if running == "" && ok {
				t.Fatalf("refused turn created a generation: %+v", g)
			}
			if running != "" && (g.ID != running || g.Steer != nil) {
				t.Fatalf("refused steer was queued: %+v", g)
			}
		})
	}
}

// A steer on an available Kiro CLI keeps the session on Kiro through the
// follow-up turn.
func TestKiroSteerKeepsSessionOnKiro(t *testing.T) {
	defer func(previous func() bool) { kiroCLIInstalled = previous }(kiroCLIInstalled)
	h, user := workbenchFixture(t)
	release := make(chan struct{})
	var calls atomic.Int32
	h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		if calls.Add(1) > 1 {
			return "改好了", nil
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return "", ctx.Err()
	}
	g := startKiroGeneration(t, h, user, "kiro-steer")
	c, rec := workbenchContext(user, "POST", "/api/assistant/sessions/kiro-steer/generation/steer?generationId="+g.ID, bytes.NewBufferString(`{"content":"改一下","model":"kiro:auto"}`))
	c.Params = gin.Params{{Key: "id", Value: "kiro-steer"}}
	h.steerGeneration(c)
	if rec.Code != 200 {
		t.Fatalf("steer: %d %s", rec.Code, rec.Body.String())
	}
	if current, _ := h.repo.AssistantGeneration(user.ID, "kiro-steer"); current.Steer == nil || current.Steer.Model != "kiro:auto" {
		t.Fatalf("steer = %+v, want kiro:auto", current.Steer)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		current, _ := h.repo.AssistantGeneration(user.ID, "kiro-steer")
		if current.ID != g.ID && current.Status == "completed" {
			saved, _ := h.repo.AssistantSession(user.ID, "kiro-steer")
			if saved.Executor != runtime.AgentCLIKiro || saved.Model != "kiro:auto" || current.Executor != runtime.AgentCLIKiro {
				t.Fatalf("after steer: session executor=%q model=%q generation executor=%q", saved.Executor, saved.Model, current.Executor)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("follow-up never finished: %+v", current)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startKiroGeneration starts a Kiro turn that runs until h.generate returns.
func startKiroGeneration(t *testing.T, h *assistantHandlers, user domain.User, session string) domain.AssistantGeneration {
	t.Helper()
	kiroCLIInstalled = func() bool { return true }
	c, rec := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBufferString(`{"projectId":"workbench","sessionId":"`+session+`","executor":"kiro","model":"kiro:auto","messages":[{"role":"user","content":"hi"}]}`))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	g, ok := h.repo.AssistantGeneration(user.ID, session)
	if !ok || g.Executor != runtime.AgentCLIKiro {
		t.Fatalf("kiro generation = %+v", g)
	}
	return g
}
