package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

func TestVerifyGitLabToken(t *testing.T) {
	cases := []struct {
		name       string
		userStatus int
		userBody   string
		selfStatus int
		selfBody   string
		valid      bool
		login      string
		missing    []string
		errContains string
	}{
		{name: "api scope accepted", userStatus: 200, userBody: `{"username":"alice"}`, selfStatus: 200, selfBody: `{"active":true,"revoked":false,"scopes":["api","read_user"]}`, valid: true, login: "alice"},
		{name: "missing api scope", userStatus: 200, userBody: `{"username":"alice"}`, selfStatus: 200, selfBody: `{"active":true,"scopes":["read_repository"]}`, login: "alice", missing: []string{"api"}},
		{name: "self unavailable keeps identity", userStatus: 200, userBody: `{"username":"alice"}`, selfStatus: 404, valid: true, login: "alice"},
		{name: "invalid token", userStatus: 401, errContains: "无效或已过期"},
		{name: "revoked token", userStatus: 200, userBody: `{"username":"alice"}`, selfStatus: 200, selfBody: `{"active":true,"revoked":true,"scopes":["api"]}`, valid: true, login: "alice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("PRIVATE-TOKEN") != "pat-token" {
					t.Fatalf("missing PRIVATE-TOKEN header")
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/user":
					w.WriteHeader(tc.userStatus)
					_, _ = w.Write([]byte(tc.userBody))
				case "/personal_access_tokens/self":
					w.WriteHeader(tc.selfStatus)
					_, _ = w.Write([]byte(tc.selfBody))
				default:
					t.Fatalf("unexpected path %s", r.URL.Path)
				}
			}))
			defer srv.Close()
			got := verifyGitLabToken(context.Background(), srv.URL, "pat-token")
			if got.Valid != tc.valid || got.Login != tc.login {
				t.Fatalf("result=%+v want valid=%v login=%q", got, tc.valid, tc.login)
			}
			if tc.errContains != "" && !strings.Contains(got.Error, tc.errContains) {
				t.Fatalf("error=%q want contains %q", got.Error, tc.errContains)
			}
			if len(tc.missing) == 1 && (len(got.Missing) != 1 || got.Missing[0] != tc.missing[0]) {
				t.Fatalf("missing=%v want %v", got.Missing, tc.missing)
			}
			if strings.Contains(got.Error, "pat-token") {
				t.Fatal("token leaked in error")
			}
		})
	}
}

func TestCatalogAddGitLabRepositoryAndRejectForeignHost(t *testing.T) {
	domain.SetGitLabInstance("https://code.example.net")
	t.Cleanup(func() { domain.SetGitLabInstance("") })
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutProject(domain.Project{ID: "p1", Name: "P1"}); err != nil {
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
	if saved.Provider != "gitlab" || saved.GongfengProjectID != "gitlab:group/sub/proj" || saved.DefaultBranch != "main" {
		t.Fatalf("saved repo = %+v", saved)
	}
	if saved.WebURL != "https://code.example.net/group/sub/proj" || saved.CloneURL != "https://code.example.net/group/sub/proj.git" {
		t.Fatalf("saved URLs = %q / %q", saved.WebURL, saved.CloneURL)
	}

	// A GitLab-shaped URL on a different host must be rejected so the token is
	// never sent elsewhere.
	if w := post(`{"projectId":"p1","repoUrl":"https://evil.example.net/group/proj"}`); w.Code != 400 {
		t.Fatalf("foreign host must be rejected, got code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestCatalogRejectsGitLabRepoWhenDisabled(t *testing.T) {
	domain.SetGitLabInstance("")
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutProject(domain.Project{ID: "p1", Name: "P1"}); err != nil {
		t.Fatal(err)
	}
	h := &catalogHandlers{deps: &deps{repo: st}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/repositories", strings.NewReader(`{"projectId":"p1","repoUrl":"https://code.example.net/group/proj"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.repositories(c)
	if w.Code != 400 {
		t.Fatalf("gitlab repo must be rejected when disabled, code=%d body=%s", w.Code, w.Body.String())
	}
}
