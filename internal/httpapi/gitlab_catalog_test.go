package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

func TestCatalogAddGitLabRepositoryViaAuthorizedHost(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutProject(domain.Project{ID: "p1", Name: "P1"}); err != nil {
		t.Fatal(err)
	}
	// Administrator authorizes the self-hosted GitLab instance. This, not a
	// deployment-wide URL, is what tells the platform "this host is GitLab".
	if err := st.CreatePlatformGitHTTPSHost(context.Background(), &repo.PlatformGitHTTPSHost{
		Host: "code.example.net", Platform: domain.GitPlatformGitLab,
		Addresses: []netip.Addr{netip.MustParseAddr("198.19.2.89")},
	}); err != nil {
		t.Fatal(err)
	}
	h := &catalogHandlers{deps: &deps{repo: st}}
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/repositories", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.repositories(c)
		return w
	}

	w := post(`{"projectId":"p1","repoUrl":"https://code.example.net/group/sub/proj"}`)
	if w.Code != 200 {
		t.Fatalf("add gitlab repo: code=%d body=%s", w.Code, w.Body.String())
	}
	var saved domain.Repository
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Provider != domain.GitPlatformGitLab || saved.GongfengProjectID != "gitlab:code.example.net/group/sub/proj" {
		t.Fatalf("saved repo = %+v", saved)
	}
	if saved.CloneURL != "https://code.example.net/group/sub/proj.git" || saved.DefaultBranch != "main" {
		t.Fatalf("saved URLs/branch = %q / %q", saved.CloneURL, saved.DefaultBranch)
	}

	// An explicit gitlab provider on an unauthorized host is rejected with a
	// clear message, so a token is never sent to an unvetted host.
	w = post(`{"projectId":"p1","provider":"gitlab","repoUrl":"https://evil.example.net/group/proj"}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "未被授权为 GitLab") {
		t.Fatalf("unauthorized gitlab host must be rejected: code=%d body=%s", w.Code, w.Body.String())
	}
}
