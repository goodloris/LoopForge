package engine

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// A GitLab SOP run has no agent-opened MR (the sandbox has no GitLab CLI), so
// the engine opens it through EnsureDeliveryMR into the work branch's base, and
// that MR becomes the run's delivery. A GitHub run never calls the hook.
func TestDeliveryMRsFromRunOpensGitLabMR(t *testing.T) {
	run := domain.Run{
		ID: "run-1",
		Repositories: []domain.RepositoryContext{{
			RepositoryID: "r1", Name: "proj", Provider: "gitlab",
			GongfengProjectID: "gitlab:group/sub/proj", WorkBranch: "feature/x", BaseBranch: "main",
		}},
	}
	run.ExecutionContext.ResponsibleUserID = 7

	var ensureCalls []string
	var findCalls int
	e := &Engine{
		EnsureDeliveryMR: func(project, source, target string, uid int64) (domain.DeliveryMR, bool, error) {
			ensureCalls = append(ensureCalls, project+" "+source+"->"+target)
			if uid != 7 {
				t.Fatalf("responsible user = %d, want 7", uid)
			}
			return domain.DeliveryMR{Project: project, IID: 5, URL: "https://code.example.net/group/sub/proj/-/merge_requests/5", TargetBranch: target}, true, nil
		},
		FindBranchMR: func(project, source string) (domain.DeliveryMR, bool, error) {
			findCalls++
			return domain.DeliveryMR{}, false, nil
		},
	}
	mrs := e.deliveryMRsFromRun(run)
	if len(ensureCalls) != 1 || ensureCalls[0] != "gitlab:group/sub/proj feature/x->main" {
		t.Fatalf("EnsureDeliveryMR calls = %v", ensureCalls)
	}
	if findCalls != 0 {
		t.Fatalf("FindBranchMR must not be consulted once the platform opened the MR; calls=%d", findCalls)
	}
	if len(mrs) != 1 || mrs[0].IID != 5 || mrs[0].Project != "gitlab:group/sub/proj" {
		t.Fatalf("delivery MRs = %+v", mrs)
	}

	// A GitHub run keeps the agent-opened flow and never calls EnsureDeliveryMR.
	ensureCalls = nil
	findCalls = 0
	gh := domain.Run{ID: "run-2", Repositories: []domain.RepositoryContext{{
		RepositoryID: "r2", Name: "repo", Provider: "github",
		GongfengProjectID: "github:owner/repo", WorkBranch: "feature/y", BaseBranch: "main",
	}}}
	_ = e.deliveryMRsFromRun(gh)
	if len(ensureCalls) != 0 {
		t.Fatalf("GitHub run must not call EnsureDeliveryMR: %v", ensureCalls)
	}
	if findCalls != 1 {
		t.Fatalf("GitHub run must resolve via FindBranchMR; calls=%d", findCalls)
	}
}

// The SOP merge-request step tells a GitLab agent to push only and never run
// gh/glab, while GitHub keeps the create-the-MR instruction.
func TestHostedMRBlurbIsPushOnlyForGitLab(t *testing.T) {
	gitlab := domain.Run{Repositories: []domain.RepositoryContext{{
		Name: "proj", Provider: "gitlab", GongfengProjectID: "gitlab:group/proj", WorkBranch: "feature/x", BaseBranch: "main",
	}}}
	blurb := hostedMRBlurb(nil, gitlab)
	if !strings.Contains(blurb, "git push") || !strings.Contains(blurb, "不要运行 gh pr create") {
		t.Fatalf("GitLab blurb must be push-only and forbid gh pr create: %q", blurb)
	}
	if !strings.Contains(blurb, "平台会自动创建或更新") {
		t.Fatalf("GitLab blurb must say the platform opens the MR: %q", blurb)
	}

	github := domain.Run{Repositories: []domain.RepositoryContext{{
		Name: "repo", Provider: "github", GongfengProjectID: "github:owner/repo", WorkBranch: "feature/y", BaseBranch: "main",
	}}}
	if got := hostedMRBlurb(nil, github); strings.Contains(got, "平台会自动创建或更新") {
		t.Fatalf("GitHub blurb must keep the agent-opened flow: %q", got)
	}
}
