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

// gitlabTestStore seeds a store with a gitlab repository owned by a user whose
// GitLab token the tracker must send as PRIVATE-TOKEN.
func gitlabTestStore(t *testing.T) (*repo.Store, domain.Repository) {
	t.Helper()
	domain.SetGitLabInstance("https://code.example.net")
	t.Cleanup(func() { domain.SetGitLabInstance("") })
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "gitlab-owner", GitLabToken: "owner-pat"}); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByLoginName("gitlab-owner")
	if err := st.PutProject(domain.Project{ID: "public", Name: "Public", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	r := domain.Repository{ID: "gitlab-repo", ProjectID: "public", Provider: "gitlab", Name: "proj", GongfengProjectID: "gitlab:group/sub/proj", DefaultBranch: "main", WebURL: "https://code.example.net/group/sub/proj"}
	if err := st.PutRepository(r); err != nil {
		t.Fatal(err)
	}
	return st, r
}

const gitlabEncodedPath = "group%2Fsub%2Fproj"

func TestGitLabMergeBranchCreatesThenMerges(t *testing.T) {
	st, r := gitlabTestStore(t)
	var created, merged bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("PRIVATE-TOKEN") != "owner-pat" {
			t.Errorf("wrong actor credential: %q", req.Header.Get("PRIVATE-TOKEN"))
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
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s := NewService()
	s.ConfigureWithStore(domain.SyncConfig{}, st)
	s.SetGitLabAPIBase(srv.URL)
	ref, err := s.EnsureAndMergeBranchMR(r.GongfengProjectID, "feature/work", "main", "Title", "")
	if err != nil || ref != "!7" {
		t.Fatalf("merge: ref=%q err=%v", ref, err)
	}
	if !created || !merged {
		t.Fatalf("expected create+merge, got created=%v merged=%v", created, merged)
	}
}

func TestGitLabMergeBranchShortCircuitsMergedMR(t *testing.T) {
	st, r := gitlabTestStore(t)
	var writes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests":
			_, _ = w.Write([]byte(`[{"iid":11,"state":"merged","source_branch":"done","target_branch":"main","merged_at":"2026-11-13T04:00:00Z"}]`))
		default:
			writes = append(writes, req.Method+" "+req.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s := NewService()
	s.ConfigureWithStore(domain.SyncConfig{}, st)
	s.SetGitLabAPIBase(srv.URL)
	ref, err := s.EnsureAndMergeBranchMR(r.GongfengProjectID, "done", "main", "Title", "")
	if err != nil || ref != "!11" {
		t.Fatalf("merged short-circuit: ref=%q err=%v", ref, err)
	}
	if len(writes) != 0 {
		t.Fatalf("already merged MR must not be re-created or re-merged: %v", writes)
	}
}

// The work branch is pushed right before the merge, so GitLab may report the
// previous head (409) or still be computing mergeability (405/406/422). The
// merge must refetch and retry, and these refusals are not token problems.
func TestGitLabMergeRetriesRace(t *testing.T) {
	st, r := gitlabTestStore(t)
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
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s := NewService()
	s.ConfigureWithStore(domain.SyncConfig{}, st)
	s.SetGitLabAPIBase(srv.URL)

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
	if got := (&gitlabHTTPError{Status: 403}).Error(); !strings.Contains(got, "GitLab token") {
		t.Fatalf("403 must still point at the token: %s", got)
	}
}

func TestGitLabDeliveryStateAndRefresh(t *testing.T) {
	st, r := gitlabTestStore(t)
	at := time.Date(2026, 11, 13, 4, 0, 0, 0, time.UTC)
	cases := []struct {
		mr     gitlabMR
		merged bool
		state  string
	}{
		{gitlabMR{IID: 7, State: "merged"}, true, "merged"},
		{gitlabMR{IID: 7, MergedAt: &at, State: "opened"}, true, "merged"},
		{gitlabMR{IID: 7, State: "closed"}, false, "closed"},
		{gitlabMR{IID: 7, State: "opened"}, false, "opened"},
	}
	for _, c := range cases {
		if got := c.mr.merged(); got != c.merged {
			t.Fatalf("merged()=%v want %v", got, c.merged)
		}
		d := c.mr.delivery("gitlab:group/sub/proj")
		if d.State != c.state || d.IID != 7 {
			t.Fatalf("delivery=%+v want state %q", d, c.state)
		}
		if c.mr.ref() != "!7" {
			t.Fatalf("ref()=%q", c.mr.ref())
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests/9" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"iid":9,"state":"merged","target_branch":"main","merged_at":"2026-11-13T04:00:00Z"}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	s := NewService()
	s.ConfigureWithStore(domain.SyncConfig{}, st)
	s.SetGitLabAPIBase(srv.URL)
	if err := st.EnsureMRDeliveryState(r.GongfengProjectID, 9, domain.GongfengMRURL(r.GongfengProjectID, 9), "issue-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.refreshGitLabDelivery(st, r.GongfengProjectID, 9); err != nil {
		t.Fatalf("refresh: %v", err)
	}
}
