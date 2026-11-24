package tracker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// gitlabTrackerFixture seeds a store with a GitLab repository whose owner holds
// a per-host Git HTTP credential, and points the tracker at a fake GitLab API.
func gitlabTrackerFixture(t *testing.T) (*Service, *repo.Store, domain.Repository, *[]string) {
	t.Helper()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "gitlab-owner"}); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByLoginName("gitlab-owner")
	if err := st.SaveGitHTTPCredential(u.ID, "", domain.GitHTTPCredentialSecret{
		GitHTTPCredential: domain.GitHTTPCredential{Name: "GitLab", Host: "code.example.net", Platform: domain.GitPlatformGitLab},
		Token:             "owner-pat",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutProject(domain.Project{ID: "public", Name: "Public", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	r := domain.Repository{ID: "gl-repo", ProjectID: "public", Provider: domain.GitPlatformGitLab, Name: "proj", GongfengProjectID: "gitlab:code.example.net/group/sub/proj", DefaultBranch: "main", WebURL: "https://code.example.net/group/sub/proj"}
	if err := st.PutRepository(r); err != nil {
		t.Fatal(err)
	}
	seen := &[]string{}
	s := NewService()
	s.ConfigureWithStore(domain.SyncConfig{}, st)
	return s, st, r, seen
}

const gitlabEncodedPath = "group%2Fsub%2Fproj"

func TestGitLabMergeBranchCreatesThenMerges(t *testing.T) {
	s, _, r, _ := gitlabTrackerFixture(t)
	var created, merged bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("PRIVATE-TOKEN") != "owner-pat" {
			t.Errorf("wrong credential: %q", req.Header.Get("PRIVATE-TOKEN"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests":
			if req.URL.Query().Get("source_branch") != "feature/work" || req.URL.Query().Get("target_branch") != "main" {
				t.Errorf("find must filter by source+target: %q", req.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[]`))
		case req.Method == "POST" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests":
			created = true
			_, _ = w.Write([]byte(`{"iid":7,"state":"opened","source_branch":"feature/work","target_branch":"main","diff_refs":{"head_sha":"head1"}}`))
		case req.Method == "PUT" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests/7/merge":
			var body map[string]any
			_ = json.NewDecoder(req.Body).Decode(&body)
			if body["sha"] != "head1" {
				t.Errorf("merge must bind to the observed head, got %v", body["sha"])
			}
			merged = true
			_ = json.NewEncoder(w).Encode(map[string]any{"iid": 7, "state": "merged"})
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.EscapedPath())
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s.gitHostAPIBase = srv.URL
	ref, err := s.EnsureAndMergeBranchMR(r.GongfengProjectID, "feature/work", "main", "Title", "")
	if err != nil || ref != "!7" {
		t.Fatalf("merge: ref=%q err=%v", ref, err)
	}
	if !created || !merged {
		t.Fatalf("expected create+merge, got created=%v merged=%v", created, merged)
	}
}

func TestGitLabMergeBranchShortCircuitsMergedMR(t *testing.T) {
	s, _, r, _ := gitlabTrackerFixture(t)
	var writes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests":
			_, _ = w.Write([]byte(`[{"iid":11,"state":"merged","source_branch":"done","target_branch":"main","merged_at":"2026-11-13T04:00:00Z"}]`))
		default:
			writes = append(writes, req.Method+" "+req.URL.EscapedPath())
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s.gitHostAPIBase = srv.URL
	ref, err := s.EnsureAndMergeBranchMR(r.GongfengProjectID, "done", "main", "Title", "")
	if err != nil || ref != "!11" {
		t.Fatalf("merged short-circuit: ref=%q err=%v", ref, err)
	}
	if len(writes) != 0 {
		t.Fatalf("already merged MR must not be re-created or re-merged: %v", writes)
	}
}

func TestGitLabMergeRetriesRace(t *testing.T) {
	s, _, r, _ := gitlabTrackerFixture(t)
	old := gitlabMergeRetryDelays
	gitlabMergeRetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { gitlabMergeRetryDelays = old })

	var puts []string
	refuse := map[string]int{"after-push": 1}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests":
			_, _ = w.Write([]byte(`[{"iid":9,"state":"opened","source_branch":"work","target_branch":"integration","diff_refs":{"head_sha":"before-push"}}]`))
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests/9":
			_, _ = w.Write([]byte(`{"iid":9,"state":"opened","source_branch":"work","target_branch":"integration","diff_refs":{"head_sha":"after-push"}}`))
		case req.Method == "PUT" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests/9/merge":
			var body map[string]any
			_ = json.NewDecoder(req.Body).Decode(&body)
			sha, _ := body["sha"].(string)
			puts = append(puts, sha)
			if sha == "before-push" {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"message":"SHA does not match HEAD of source branch"}`))
				return
			}
			if refuse[sha] > 0 {
				refuse[sha]--
				w.WriteHeader(http.StatusNotAcceptable)
				_, _ = w.Write([]byte(`{"message":"Branch cannot be merged"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"iid": 9, "state": "merged"})
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.EscapedPath())
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s.gitHostAPIBase = srv.URL

	ref, err := s.EnsureAndMergeBranchMR(r.GongfengProjectID, "work", "integration", "Title", "")
	if err != nil || ref != "!9" {
		t.Fatalf("merge after race: ref=%q err=%v", ref, err)
	}
	if want := "before-push,after-push,after-push"; strings.Join(puts, ",") != want {
		t.Fatalf("merge attempts=%v want %v", puts, want)
	}

	puts = nil
	refuse["after-push"] = 99
	_, err = s.EnsureAndMergeBranchMR(r.GongfengProjectID, "work", "integration", "Title", "")
	if err == nil || len(puts) != len(gitlabMergeRetryDelays)+1 {
		t.Fatalf("persistent refusal must stop after the retry budget: attempts=%d err=%v", len(puts), err)
	}
	if strings.Contains(err.Error(), "token and repository permissions") || !strings.Contains(err.Error(), "not a token problem") {
		t.Fatalf("merge race reported as a credential problem: %v", err)
	}
	if got := (&gitlabHTTPError{Status: 403, Host: "code.example.net"}).Error(); !strings.Contains(got, "access token") {
		t.Fatalf("403 must still point at the token: %s", got)
	}
}

func TestGitLabFindBranchAndInto(t *testing.T) {
	s, _, r, _ := gitlabTrackerFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.EscapedPath() != "/projects/"+gitlabEncodedPath+"/merge_requests" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"iid":5,"state":"opened","source_branch":"feature/x","target_branch":"main","diff_refs":{"head_sha":"abc"}}]`))
	}))
	defer srv.Close()
	s.gitHostAPIBase = srv.URL

	mr, ok, err := s.FindBranchMR(r.GongfengProjectID, "feature/x")
	if err != nil || !ok || mr.IID != 5 || mr.URL != "https://code.example.net/group/sub/proj/-/merge_requests/5" {
		t.Fatalf("FindBranchMR = %+v ok=%v err=%v", mr, ok, err)
	}
	into, err := s.FindBranchMRsInto(domain.BranchMRQuery{ProjectID: "public", RemoteProject: r.GongfengProjectID, SourceBranch: "feature/x", TargetBranch: "main", ResponsibleUserID: 0})
	if err != nil || len(into) != 1 || into[0].IID != 5 || into[0].HeadSHA != "abc" {
		t.Fatalf("FindBranchMRsInto = %+v err=%v", into, err)
	}
}
