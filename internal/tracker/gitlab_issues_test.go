package tracker

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// issuesPage builds a GitLab /issues JSON page with iids [from, to], all in
// the given state, so paging and state mapping can be exercised precisely.
func issuesPage(from, to int, state string) string {
	parts := make([]string, 0, to-from+1)
	for i := from; i <= to; i++ {
		parts = append(parts, fmt.Sprintf(`{"id":%d,"iid":%d,"title":"issue %d","description":"body %d","state":%q,"web_url":"https://code.example.net/group/sub/proj/-/issues/%d","created_at":"2026-01-02T03:04:05Z"}`, 9000+i, i, i, i, state, i))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func gitlabIssuesServer(t *testing.T, s *Service, pages map[string]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("PRIVATE-TOKEN") != "owner-pat" {
			t.Errorf("wrong credential: %q", req.Header.Get("PRIVATE-TOKEN"))
		}
		w.Header().Set("Content-Type", "application/json")
		if req.URL.EscapedPath() != "/projects/"+gitlabEncodedPath+"/issues" {
			t.Errorf("unexpected path %s", req.URL.EscapedPath())
			w.WriteHeader(404)
			return
		}
		page := req.URL.Query().Get("page")
		if body, ok := pages[page]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(srv.Close)
	s.gitHostAPIBase = srv.URL
}

// A full import maps opened→todo and closed→closed+CloseSource, keeps the iid
// as the external id, lets the store assign non-colliding project numbers, and
// follows pagination to the last page.
func TestPullGitLabIssuesMapsStatesAndPaginates(t *testing.T) {
	s, st, r, _ := gitlabTrackerFixture(t)
	gitlabIssuesServer(t, s, map[string]string{
		"1": issuesPage(1, 100, "opened"), // full page → paginator must fetch page 2
		"2": issuesPage(101, 101, "closed"),
	})
	n, err := s.pullGitLabIssues(st, r)
	if err != nil || n != 101 {
		t.Fatalf("import: n=%d err=%v", n, err)
	}
	open1, ok := st.Issue("gl-gl-repo-1")
	if !ok {
		t.Fatal("open issue missing")
	}
	if open1.Source != "gitlab" || open1.Kind != "issue" || open1.Status != "todo" || open1.CloseSource != "" {
		t.Fatalf("open mapping: %+v", open1)
	}
	if open1.ExternalRef == nil || open1.ExternalRef.ExternalIID != 1 || open1.ExternalRef.Source != "gitlab" || open1.ExternalRef.ExternalURL != "https://code.example.net/group/sub/proj/-/issues/1" {
		t.Fatalf("external ref: %+v", open1.ExternalRef)
	}
	if open1.Number == 0 {
		t.Fatal("store must assign a project number")
	}
	closed, ok := st.Issue("gl-gl-repo-101")
	if !ok || closed.Status != "closed" || closed.CloseSource != "external" {
		t.Fatalf("closed mapping: %+v", closed)
	}
	// Project numbers are assigned by the store and must not collide.
	seen := map[int]bool{}
	for _, i := range st.Issues() {
		if strings.HasPrefix(i.ID, "gl-gl-repo-") {
			if i.Number == 0 || seen[i.Number] {
				t.Fatalf("colliding/zero number %d on %s", i.Number, i.ID)
			}
			seen[i.Number] = true
		}
	}
	if len(seen) != 101 {
		t.Fatalf("expected 101 distinct numbers, got %d", len(seen))
	}
}

// A second pull refreshes an untouched description but keeps one the user
// edited (hash no longer matches the recorded source hash).
func TestPullGitLabIssuesPreservesEditedDescription(t *testing.T) {
	s, st, r, _ := gitlabTrackerFixture(t)
	gitlabIssuesServer(t, s, map[string]string{"1": issuesPage(1, 2, "opened")})
	if _, err := s.pullGitLabIssues(st, r); err != nil {
		t.Fatal(err)
	}
	// User edits issue #1's description; #2 is left as imported.
	edited, _ := st.Issue("gl-gl-repo-1")
	edited.Description = "human edited"
	if err := st.PutIssue(edited); err != nil {
		t.Fatal(err)
	}
	// Upstream changes both descriptions on the next pull.
	gitlabIssuesServer(t, s, map[string]string{"1": `[{"id":9001,"iid":1,"title":"issue 1","description":"upstream changed","state":"opened","web_url":"https://code.example.net/group/sub/proj/-/issues/1","created_at":"2026-01-02T03:04:05Z"},{"id":9002,"iid":2,"title":"issue 2","description":"upstream changed 2","state":"opened","web_url":"https://code.example.net/group/sub/proj/-/issues/2","created_at":"2026-01-02T03:04:05Z"}]`})
	if _, err := s.pullGitLabIssues(st, r); err != nil {
		t.Fatal(err)
	}
	got1, _ := st.Issue("gl-gl-repo-1")
	if got1.Description != "human edited" {
		t.Fatalf("edited description was overwritten: %q", got1.Description)
	}
	got2, _ := st.Issue("gl-gl-repo-2")
	if got2.Description != "upstream changed 2" {
		t.Fatalf("untouched description should refresh: %q", got2.Description)
	}
}

// A GitLab repository without a project-owner credential fails with a clear
// error rather than silently importing nothing.
func TestPullGitLabIssuesMissingCredentialErrors(t *testing.T) {
	s, st, _, _ := gitlabTrackerFixture(t)
	other := domain.Repository{ID: "gl-noauth", ProjectID: "public", Provider: domain.GitPlatformGitLab, Name: "noauth", GongfengProjectID: "gitlab:other.example.net/g/p", DefaultBranch: "main"}
	if err := st.PutRepository(other); err != nil {
		t.Fatal(err)
	}
	_, err := s.pullGitLabIssues(st, other)
	if err == nil || !strings.Contains(err.Error(), "访问令牌") {
		t.Fatalf("missing credential must error clearly: %v", err)
	}
}

// PullScheduleJob only pulls GitLab when the job source is "gitlab": an empty
// source pulls nothing (and never hits the API) and a different source skips
// the GitLab repository.
func TestPullScheduleJobGitLabSourceGating(t *testing.T) {
	s, st, _, _ := gitlabTrackerFixture(t)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(issuesPage(1, 1, "opened")))
	}))
	t.Cleanup(srv.Close)
	s.gitHostAPIBase = srv.URL

	// Empty source: pull nothing, no API call.
	if n, _, err := s.PullScheduleJob(st, domain.ScheduleJob{ProjectID: "public", Source: ""}); err != nil || n != 0 || hits != 0 {
		t.Fatalf("empty source: n=%d hits=%d err=%v", n, hits, err)
	}
	// github source: the gitlab repo is skipped, no API call.
	if n, _, err := s.PullScheduleJob(st, domain.ScheduleJob{ProjectID: "public", Source: "github"}); err != nil || n != 0 || hits != 0 {
		t.Fatalf("github source must skip gitlab: n=%d hits=%d err=%v", n, hits, err)
	}
	// gitlab source: the gitlab repo is pulled.
	if n, _, err := s.PullScheduleJob(st, domain.ScheduleJob{ProjectID: "public", Source: "gitlab"}); err != nil || n != 1 || hits == 0 {
		t.Fatalf("gitlab source must pull: n=%d hits=%d err=%v", n, hits, err)
	}
}

// The schedule-source whitelist accepts gitlab (so a trigger job can name it).
func TestNormalizeScheduleSourceAcceptsGitLab(t *testing.T) {
	got, err := domain.NormalizeScheduleSource("GitLab")
	if err != nil || got != "gitlab" {
		t.Fatalf("gitlab source: %q %v", got, err)
	}
}
