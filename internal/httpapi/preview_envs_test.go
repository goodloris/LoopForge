package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/previewenv"
	"agentflow/internal/repo"
	"agentflow/internal/tracker"

	"github.com/gin-gonic/gin"
)

func TestPreviewEnvHandlersAccessAndTicket(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	users := map[string]domain.User{}
	for _, name := range []string{"pe-owner", "pe-viewer", "pe-outsider"} {
		if err := st.PutUser(domain.User{LoginName: name}); err != nil {
			t.Fatal(err)
		}
		users[name], _ = st.UserByLoginName(name)
	}
	if err := st.PutProject(domain.Project{ID: "pe-proj", Name: "P", OwnerID: users["pe-owner"].ID, Members: []domain.ProjectMember{{UserID: users["pe-viewer"].ID, Role: "viewer"}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutIssue(domain.Issue{ID: "pe-issue", ProjectID: "pe-proj", Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRun(domain.Run{ID: "pe-run", Status: "waiting", ExecutionContext: domain.TelemetryContext{ProjectID: "pe-proj", IssueID: "pe-issue"}, Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	ready := domain.PreviewEnvironment{ID: domain.PreviewEnvID("pe-issue", "repo"), ProjectID: "pe-proj", IssueID: "pe-issue", RepositoryID: "repo", LastRunID: "pe-run", Status: domain.PreviewEnvReady, AdminPassword: "secret-pw"}
	deploying := domain.PreviewEnvironment{ID: domain.PreviewEnvID("pe-issue", "repo2"), ProjectID: "pe-proj", IssueID: "pe-issue", RepositoryID: "repo2", Status: domain.PreviewEnvDeploying}
	for _, env := range []domain.PreviewEnvironment{ready, deploying} {
		if err := st.PutPreviewEnvironment(env); err != nil {
			t.Fatal(err)
		}
	}
	eng := engine.New(st, nil)
	cfg := engine.ComposePreviewConfig{RunnerID: "preview-host", BaseURL: "http://{id}.10-0-0-1.sslip.io:8088", Secret: []byte(strings.Repeat("k", 32))}
	h := &previewEnvHandlers{deps: &deps{repo: st, engine: eng}}
	call := func(user string, handler gin.HandlerFunc, method, id string) *httptest.ResponseRecorder {
		c, rec := workbenchContext(users[user], method, "/", bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: id}}
		handler(c)
		return rec
	}
	list := func(user string, handler gin.HandlerFunc, id string) previewEnvList {
		rec := call(user, handler, http.MethodGet, id)
		var out previewEnvList
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("list %s: %d %s", user, rec.Code, rec.Body.String())
		}
		return out
	}

	// Members (viewers included) see the password; outsiders do not.
	if got := list("pe-viewer", h.runEnvs, "pe-run"); len(got.Items) != 1 || got.Items[0].AdminPassword != "secret-pw" || got.Enabled || got.AdminUser != "preview" {
		t.Fatalf("viewer run list = %+v", got)
	}
	if got := list("pe-outsider", h.issueEnvs, "pe-issue"); len(got.Items) != 2 || got.Items[0].AdminPassword != "" {
		t.Fatalf("outsider issue list = %+v", got)
	}
	if rec := call("pe-viewer", h.runEnvs, http.MethodGet, "missing"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing run = %d", rec.Code)
	}

	// Ticket: readers only, ready only, and only once configured.
	if rec := call("pe-viewer", h.ticket, http.MethodPost, ready.ID); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured ticket = %d %s", rec.Code, rec.Body.String())
	}
	eng.SetComposePreview(cfg)
	if rec := call("pe-outsider", h.ticket, http.MethodPost, ready.ID); rec.Code != http.StatusForbidden {
		t.Fatalf("outsider ticket = %d", rec.Code)
	}
	if rec := call("pe-viewer", h.ticket, http.MethodPost, deploying.ID); rec.Code != http.StatusConflict {
		t.Fatalf("deploying ticket = %d", rec.Code)
	}
	rec := call("pe-viewer", h.ticket, http.MethodPost, ready.ID)
	var body struct{ URL string }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		t.Fatalf("ticket = %d %s", rec.Code, rec.Body.String())
	}
	u, err := url.Parse(body.URL)
	if err != nil || u.Host != ready.ID+".10-0-0-1.sslip.io:8088" || u.Path != "/" {
		t.Fatalf("ticket url = %s", body.URL)
	}
	used := previewenv.NewNonces()
	if err := previewenv.VerifyTicket(cfg.Secret, u.Query().Get(previewenv.TicketParam), ready.ID, time.Now(), used); err != nil {
		t.Fatalf("ticket does not verify: %v", err)
	}
	if err := previewenv.VerifyTicket(cfg.Secret, u.Query().Get(previewenv.TicketParam), deploying.ID, time.Now(), previewenv.NewNonces()); err == nil {
		t.Fatal("ticket verified for another environment")
	}

	// Delete: writers only; deploying environments conflict.
	if rec := call("pe-viewer", h.destroy, http.MethodDelete, ready.ID); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer delete = %d", rec.Code)
	}
	if rec := call("pe-owner", h.destroy, http.MethodDelete, deploying.ID); rec.Code != http.StatusConflict {
		t.Fatalf("deploying delete = %d", rec.Code)
	}
	rec = call("pe-owner", h.destroy, http.MethodDelete, ready.ID)
	var destroyed domain.PreviewEnvironment
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &destroyed) != nil || destroyed.Status != domain.PreviewEnvDestroying {
		t.Fatalf("owner delete = %d %s", rec.Code, rec.Body.String())
	}
}

func TestPreviewEnvRoutesRegistered(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	router := New(st, engine.New(st, nil), tracker.NewService(), "", "")
	want := map[string]bool{
		"GET /api/runs/:id/preview-envs": false, "GET /api/issues/:id/preview-envs": false,
		"POST /api/preview-envs/:id/ticket": false, "DELETE /api/preview-envs/:id": false,
	}
	for _, route := range router.Routes() {
		if _, ok := want[route.Method+" "+route.Path]; ok {
			want[route.Method+" "+route.Path] = true
		}
	}
	for route, found := range want {
		if !found {
			t.Errorf("route %s not registered", route)
		}
	}
}
