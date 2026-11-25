package tracker

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/repository/branches/feature%2Fwork":
			_, _ = w.Write([]byte(`{"name":"feature/work"}`))
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/repository/compare":
			if req.URL.Query().Get("from") != "main" || req.URL.Query().Get("to") != "feature/work" {
				t.Errorf("compare query: %s", req.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"commits":[{"id":"abc"}]}`))
		case req.Method == "POST" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests":
			var body map[string]any
			_ = json.NewDecoder(req.Body).Decode(&body)
			if body["remove_source_branch"] != false {
				t.Errorf("create must keep the source branch: remove_source_branch=%v", body["remove_source_branch"])
			}
			created = true
			_, _ = w.Write([]byte(`{"iid":7,"state":"opened","source_branch":"feature/work","target_branch":"main","detailed_merge_status":"mergeable","diff_refs":{"head_sha":"head1"}}`))
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests/7":
			_, _ = w.Write([]byte(`{"iid":7,"state":"opened","source_branch":"feature/work","target_branch":"main","detailed_merge_status":"mergeable","diff_refs":{"head_sha":"head1"}}`))
		case req.Method == "PUT" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests/7/merge":
			var body map[string]any
			_ = json.NewDecoder(req.Body).Decode(&body)
			if body["sha"] != "head1" {
				t.Errorf("merge must bind to the observed head, got %v", body["sha"])
			}
			if body["should_remove_source_branch"] != false {
				t.Errorf("merge must keep the source branch: should_remove_source_branch=%v", body["should_remove_source_branch"])
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

	var puts int
	refuse := 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests":
			_, _ = w.Write([]byte(`[{"iid":9,"state":"opened","source_branch":"work","target_branch":"integration"}]`))
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests/9":
			_, _ = w.Write([]byte(`{"iid":9,"state":"opened","source_branch":"work","target_branch":"integration","detailed_merge_status":"mergeable","diff_refs":{"head_sha":"head9"}}`))
		case req.Method == "PUT" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests/9/merge":
			var body map[string]any
			_ = json.NewDecoder(req.Body).Decode(&body)
			if body["should_remove_source_branch"] != false {
				t.Errorf("merge must keep the source branch: %v", body["should_remove_source_branch"])
			}
			puts++
			if refuse > 0 {
				refuse--
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"message":"SHA does not match HEAD of source branch"}`))
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
	if puts != 2 {
		t.Fatalf("merge attempts=%d want 2 (one 409 then success)", puts)
	}

	puts, refuse = 0, 99
	_, err = s.EnsureAndMergeBranchMR(r.GongfengProjectID, "work", "integration", "Title", "")
	if err == nil || puts != len(gitlabMergeRetryDelays)+1 {
		t.Fatalf("persistent refusal must stop after the retry budget: attempts=%d err=%v", puts, err)
	}
	if strings.Contains(err.Error(), "token and repository permissions") || !strings.Contains(err.Error(), "not a token problem") {
		t.Fatalf("merge race reported as a credential problem: %v", err)
	}
	if got := (&gitlabHTTPError{Status: 403, Host: "code.example.net"}).Error(); !strings.Contains(got, "access token") {
		t.Fatalf("403 must still point at the token: %s", got)
	}
}

// only_allow_merge_if_pipeline_succeeds: a running head pipeline queues the
// merge (merge_when_pipeline_succeeds) instead of failing; the caller gets a
// MergeQueuedError, not merged and not failed. Once the pipeline passes,
// polling sees the MR merged.
func TestGitLabMergeQueuesWhilePipelineRuns(t *testing.T) {
	s, _, r, _ := gitlabTrackerFixture(t)
	var queued bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests":
			_, _ = w.Write([]byte(`[{"iid":9,"state":"opened","source_branch":"work","target_branch":"main"}]`))
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests/9":
			_, _ = w.Write([]byte(`{"iid":9,"state":"opened","source_branch":"work","target_branch":"main","detailed_merge_status":"ci_still_running","diff_refs":{"head_sha":"h9"},"head_pipeline":{"status":"running","web_url":"https://code.example.net/example/shop/-/pipelines/42"}}`))
		case req.Method == "PUT" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests/9/merge":
			var body map[string]any
			_ = json.NewDecoder(req.Body).Decode(&body)
			if body["merge_when_pipeline_succeeds"] != true {
				t.Errorf("must set merge_when_pipeline_succeeds, got %v", body["merge_when_pipeline_succeeds"])
			}
			if body["should_remove_source_branch"] != false {
				t.Errorf("queued merge must keep the source branch: %v", body["should_remove_source_branch"])
			}
			queued = true
			_ = json.NewEncoder(w).Encode(map[string]any{"iid": 9, "state": "opened"})
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.EscapedPath())
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s.gitHostAPIBase = srv.URL
	ref, err := s.EnsureAndMergeBranchMR(r.GongfengProjectID, "work", "main", "Title", "")
	if !queued {
		t.Fatal("expected merge_when_pipeline_succeeds to be set")
	}
	if !errors.Is(err, domain.ErrMergeQueuedForPipeline) {
		t.Fatalf("queued merge must return ErrMergeQueuedForPipeline, got err=%v", err)
	}
	if ref != "!9" {
		t.Fatalf("queued merge must still report the MR ref, got %q", ref)
	}
	if !strings.Contains(err.Error(), "/-/pipelines/42") {
		t.Fatalf("queued error must carry the pipeline link: %v", err)
	}
}

func TestGitLabMergeFailsOnFailedPipeline(t *testing.T) {
	s, _, r, _ := gitlabTrackerFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests":
			_, _ = w.Write([]byte(`[{"iid":9,"state":"opened","source_branch":"work","target_branch":"main"}]`))
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests/9":
			_, _ = w.Write([]byte(`{"iid":9,"state":"opened","source_branch":"work","target_branch":"main","detailed_merge_status":"ci_must_pass","diff_refs":{"head_sha":"h9"},"head_pipeline":{"status":"failed","web_url":"https://code.example.net/example/shop/-/pipelines/43"}}`))
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.EscapedPath())
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s.gitHostAPIBase = srv.URL
	_, err := s.EnsureAndMergeBranchMR(r.GongfengProjectID, "work", "main", "Title", "")
	if err == nil || errors.Is(err, domain.ErrMergeQueuedForPipeline) {
		t.Fatalf("failed pipeline must be a clear error, got %v", err)
	}
	if !strings.Contains(err.Error(), "/-/pipelines/43") || !strings.Contains(err.Error(), "pipeline") {
		t.Fatalf("error must name the failed pipeline with its link: %v", err)
	}
}

// After the pipeline passes, the queued MR shows merged; delivery refresh
// (project Owner credential) advances it.
func TestGitLabRefreshSeesMergedAfterPipeline(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "gl-owner"}); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByLoginName("gl-owner")
	if err := st.SaveGitHTTPCredential(u.ID, "", domain.GitHTTPCredentialSecret{
		GitHTTPCredential: domain.GitHTTPCredential{Name: "GitLab", Host: "code.example.net", Platform: domain.GitPlatformGitLab},
		Token:             "owner-pat",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutProject(domain.Project{ID: "shop", Name: "Shop", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	const proj = "gitlab:code.example.net/example/shop"
	if err := st.PutRepository(domain.Repository{ID: "gl", ProjectID: "shop", Provider: domain.GitPlatformGitLab, Name: "shop", GongfengProjectID: proj, DefaultBranch: "main", WebURL: "https://code.example.net/example/shop"}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.EscapedPath() == "/projects/example%2Fshop/merge_requests/9" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"iid":9,"state":"merged","target_branch":"main","merged_at":"2026-11-18T10:00:00Z"}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	s := NewService()
	s.ConfigureWithStore(domain.SyncConfig{}, st)
	s.gitHostAPIBase = srv.URL
	if err := st.EnsureMRDeliveryState(proj, 9, domain.GongfengMRURL(proj, 9), "issue-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.refreshGitLabDelivery(st, proj, 9); err != nil {
		t.Fatalf("refresh: %v", err)
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

func TestGitLabEnsureBranchMRCreatesReusesAndErrors(t *testing.T) {
	s, _, r, _ := gitlabTrackerFixture(t)
	var creates int
	existing := "[]"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests":
			_, _ = w.Write([]byte(existing))
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/repository/branches/feature%2Fx":
			_, _ = w.Write([]byte(`{"name":"feature/x"}`))
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/repository/compare":
			if req.URL.Query().Get("from") != "main" || req.URL.Query().Get("to") != "feature/x" {
				t.Errorf("compare query: %s", req.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"commits":[{"id":"abc"}]}`))
		case req.Method == "POST" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests":
			var body map[string]any
			_ = json.NewDecoder(req.Body).Decode(&body)
			if body["target_branch"] != "main" || body["source_branch"] != "feature/x" {
				t.Errorf("create into base branch: %+v", body)
			}
			creates++
			_, _ = w.Write([]byte(`{"iid":7,"state":"opened","source_branch":"feature/x","target_branch":"main"}`))
		default:
			t.Errorf("unexpected %s %s", req.Method, req.URL.EscapedPath())
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s.gitHostAPIBase = srv.URL

	// 1. No MR for the work branch → create one into the base branch.
	mr, ok, err := s.EnsureBranchMR(r.GongfengProjectID, "feature/x", "main", 0)
	if err != nil || !ok || mr.IID != 7 || creates != 1 {
		t.Fatalf("create path: mr=%+v ok=%v err=%v creates=%d", mr, ok, err, creates)
	}
	// 2. An open MR already exists → reuse it, no second create.
	existing = `[{"iid":7,"state":"opened","source_branch":"feature/x","target_branch":"main"}]`
	mr, ok, err = s.EnsureBranchMR(r.GongfengProjectID, "feature/x", "main", 0)
	if err != nil || !ok || mr.IID != 7 || creates != 1 {
		t.Fatalf("reuse path: mr=%+v ok=%v err=%v creates=%d", mr, ok, err, creates)
	}

	// 3. No credential for the host → a clear error, no API call.
	if _, ok, err := s.EnsureBranchMR("gitlab:other.example.net/g/p", "feature/x", "main", 0); ok || err == nil {
		t.Fatalf("missing credential must error: ok=%v err=%v", ok, err)
	} else if !strings.Contains(err.Error(), "访问令牌") {
		t.Fatalf("error must name the missing token: %v", err)
	}

	// A non-GitLab project passes through (ok=false, no error): GitHub/Gongfeng
	// keep their find-only behaviour.
	if mr, ok, err := s.EnsureBranchMR("github:example/repo", "feature/x", "main", 0); ok || err != nil || mr.IID != 0 {
		t.Fatalf("non-gitlab must pass through: mr=%+v ok=%v err=%v", mr, ok, err)
	}
}

// A pure-script SOP whose work branch was never pushed (or has no commits
// ahead of the target) must not get an empty MR: EnsureBranchMR returns a
// BranchNotDeliverableError and never POSTs merge_requests.
func TestGitLabEnsureBranchMRSkipsEmptyBranch(t *testing.T) {
	for _, tc := range []struct {
		name          string
		branchStatus  int
		compareBody   string
		wantReasonSub string
	}{
		{"missing branch", 404, "", "未推送"},
		{"no commits ahead", 200, `{"commits":[]}`, "没有新提交"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, r, _ := gitlabTrackerFixture(t)
			var posts int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests":
					_, _ = w.Write([]byte("[]"))
				case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/repository/branches/feature%2Fx":
					if tc.branchStatus != 200 {
						w.WriteHeader(tc.branchStatus)
						_, _ = w.Write([]byte(`{"message":"404 Branch Not Found"}`))
						return
					}
					_, _ = w.Write([]byte(`{"name":"feature/x"}`))
				case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/repository/compare":
					_, _ = w.Write([]byte(tc.compareBody))
				case req.Method == "POST" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests":
					posts++
					_, _ = w.Write([]byte(`{"iid":99}`))
				default:
					t.Errorf("unexpected %s %s", req.Method, req.URL.EscapedPath())
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			s.gitHostAPIBase = srv.URL

			mr, ok, err := s.EnsureBranchMR(r.GongfengProjectID, "feature/x", "main", 0)
			var notDeliverable *domain.BranchNotDeliverableError
			if ok || mr.IID != 0 || !errors.As(err, &notDeliverable) {
				t.Fatalf("expected BranchNotDeliverableError, got mr=%+v ok=%v err=%v", mr, ok, err)
			}
			if !strings.Contains(notDeliverable.Reason, tc.wantReasonSub) {
				t.Fatalf("reason %q should mention %q", notDeliverable.Reason, tc.wantReasonSub)
			}
			if posts != 0 {
				t.Fatalf("must not create an MR: posts=%d", posts)
			}
		})
	}
}

// The integration merge path (EnsureAndMergeBranchMR → gitlabMergeBranch) must
// reuse the same deliverable-branch gate: a scheduled run that pushed the work
// branch but produced no commits ahead of the integrate branch must not get a
// 0-commit work→int MR that then fails to merge. It returns a
// BranchNotDeliverableError and never POSTs merge_requests.
func TestGitLabMergeBranchSkipsEmptyBranch(t *testing.T) {
	s, _, r, _ := gitlabTrackerFixture(t)
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests":
			_, _ = w.Write([]byte("[]"))
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/repository/branches/feature%2Fwork":
			_, _ = w.Write([]byte(`{"name":"feature/work"}`))
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/repository/compare":
			_, _ = w.Write([]byte(`{"commits":[]}`))
		case req.Method == "POST" && req.URL.EscapedPath() == "/projects/"+gitlabEncodedPath+"/merge_requests":
			posts++
			_, _ = w.Write([]byte(`{"iid":4}`))
		default:
			t.Errorf("unexpected %s %s", req.Method, req.URL.EscapedPath())
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s.gitHostAPIBase = srv.URL

	ref, err := s.EnsureAndMergeBranchMR(r.GongfengProjectID, "feature/work", "main", "Title", "")
	var notDeliverable *domain.BranchNotDeliverableError
	if ref != "" || !errors.As(err, &notDeliverable) {
		t.Fatalf("expected BranchNotDeliverableError, got ref=%q err=%v", ref, err)
	}
	if !strings.Contains(notDeliverable.Reason, "没有新提交") {
		t.Fatalf("reason %q should mention no commits", notDeliverable.Reason)
	}
	if posts != 0 {
		t.Fatalf("must not create an MR: posts=%d", posts)
	}
}

// D16/D29 for GitLab: an MR merged into the integrate branch is "integrated",
// not delivered; only when the integrate branch is promoted (its commit is on
// the default branch) does the work count as merged, dated by the promote MR —
// so mergedMrs rises and integratedMrs falls.
func TestGitLabDeliveryWaitsForDefaultBranch(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "gl-owner"}); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByLoginName("gl-owner")
	if err := st.SaveGitHTTPCredential(u.ID, "", domain.GitHTTPCredentialSecret{
		GitHTTPCredential: domain.GitHTTPCredential{Name: "GitLab", Host: "code.example.net", Platform: domain.GitPlatformGitLab},
		Token:             "owner-pat",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutProject(domain.Project{ID: "shop", Name: "Shop", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	const proj = "gitlab:code.example.net/example/shop"
	const enc = "example%2Fshop"
	if err := st.PutRepository(domain.Repository{ID: "gl", ProjectID: "shop", Provider: domain.GitPlatformGitLab, Name: "shop", GongfengProjectID: proj, DefaultBranch: "main", IntegrateBranch: "integration", WebURL: "https://code.example.net/example/shop"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutIssue(domain.Issue{ID: "shop-1", ProjectID: "shop", Title: "fix", Status: "in_progress"}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	workMR := `{"iid":9,"state":"merged","merged_at":"2026-11-18T09:34:42Z","source_branch":"work","target_branch":"integration","diff_refs":{"head_sha":"work-head"}}`
	refs := `[]`
	promotes := `[]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if req.Header.Get("PRIVATE-TOKEN") != "owner-pat" {
			t.Errorf("wrong credential")
		}
		switch {
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+enc+"/merge_requests/9":
			_, _ = w.Write([]byte(workMR))
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+enc+"/repository/commits/work-head/refs":
			_, _ = w.Write([]byte(refs))
		case req.Method == "GET" && req.URL.EscapedPath() == "/projects/"+enc+"/merge_requests":
			if req.URL.Query().Get("source_branch") == "integration" {
				_, _ = w.Write([]byte(promotes))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected %s %s", req.Method, req.URL.EscapedPath())
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s := NewService()
	s.ConfigureWithStore(domain.SyncConfig{}, st)
	s.gitHostAPIBase = srv.URL

	url := domain.GongfengMRURL(proj, 9)
	if err := st.MarkIssueDeliveredFromSOPMRs("shop-1", []domain.DeliveryMR{{Project: proj, IID: 9, URL: url, GongfengState: "merged", TargetBranch: "integration"}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	poll := func() {
		_ = st.UpdateMRDeliveryState(proj, 9, "merged", "integration", time.Time{}, time.Now().UTC().Add(-time.Hour))
		s.SyncMRDeliveryStatesOnce(st)
	}
	metrics := func(step string, merged, integrated int) {
		t.Helper()
		got := st.Analytics(domain.AnalyticsFilter{ProjectID: "shop"})
		if got.Summary.MergedMRs != merged || got.Summary.IntegratedMRs != integrated {
			t.Fatalf("%s: mergedMrs=%d integratedMrs=%d, want %d/%d", step, got.Summary.MergedMRs, got.Summary.IntegratedMRs, merged, integrated)
		}
	}

	// Merged into integration, main does not contain it yet → integrated.
	poll()
	if got, _ := st.Issue("shop-1"); got.DeliveryMRState != "integrated" {
		t.Fatalf("merged into integration: mrState=%q want integrated", got.DeliveryMRState)
	}
	metrics("integrated", 0, 1)

	// Integration promoted to main (PR !20 merged) and work-head now on main.
	mu.Lock()
	refs = `[{"name":"main","type":"branch"},{"name":"integration","type":"branch"}]`
	promotes = `[{"iid":20,"state":"merged","merged_at":"2026-11-18T10:00:00Z","source_branch":"integration","target_branch":"main"}]`
	mu.Unlock()
	poll()
	if got, _ := st.Issue("shop-1"); got.DeliveryMRState != "merged" || got.Status != "done" {
		t.Fatalf("promoted: status=%q mrState=%q want done/merged", got.Status, got.DeliveryMRState)
	}
	metrics("promoted", 1, 0)
}
