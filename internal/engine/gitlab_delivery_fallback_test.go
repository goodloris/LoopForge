package engine

import (
	"errors"
	"testing"

	"agentflow/internal/domain"
)

// GitLab SOP delivery resolution: when no MR exists for the work branch, the
// engine opens one through EnsureBranchMR (the sandbox has no gh/glab). An
// existing MR is reused (EnsureBranchMR not called), and GitHub/Gongfeng never
// trigger the fallback.
func TestDeliveryMRsFromRunGitLabFallbackCreate(t *testing.T) {
	gitlabRun := domain.Run{ID: "r1", Repositories: []domain.RepositoryContext{{
		RepositoryID: "a", Name: "proj", Provider: domain.GitPlatformGitLab,
		GongfengProjectID: "gitlab:code.example.net/g/p", WorkBranch: "feature/x", BaseBranch: "main",
	}}}
	gitlabRun.ExecutionContext.ResponsibleUserID = 7

	cases := []struct {
		name       string
		find       func(string, string) (domain.DeliveryMR, bool, error)
		ensureIID  int
		ensureErr  error
		wantIID    int
		wantEnsure bool
		wantLen    int
	}{
		{
			name:       "created when missing",
			find:       func(string, string) (domain.DeliveryMR, bool, error) { return domain.DeliveryMR{}, false, nil },
			ensureIID:  9,
			wantIID:    9,
			wantEnsure: true,
			wantLen:    1,
		},
		{
			name:       "reused when present",
			find:       func(string, string) (domain.DeliveryMR, bool, error) { return domain.DeliveryMR{Project: "gitlab:code.example.net/g/p", IID: 3}, true, nil },
			wantIID:    3,
			wantEnsure: false,
			wantLen:    1,
		},
		{
			name:       "no credential surfaces as no MR",
			find:       func(string, string) (domain.DeliveryMR, bool, error) { return domain.DeliveryMR{}, false, nil },
			ensureErr:  errors.New("未找到可用的访问令牌"),
			wantEnsure: true,
			wantLen:    0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ensureCalled := false
			e := &Engine{
				FindBranchMR: c.find,
				EnsureBranchMR: func(project, src, target string, uid int64) (domain.DeliveryMR, bool, error) {
					ensureCalled = true
					if uid != 7 || target != "main" || src != "feature/x" {
						t.Fatalf("ensure args project=%s src=%s target=%s uid=%d", project, src, target, uid)
					}
					if c.ensureErr != nil {
						return domain.DeliveryMR{}, false, c.ensureErr
					}
					return domain.DeliveryMR{Project: project, IID: c.ensureIID, TargetBranch: target}, true, nil
				},
			}
			mrs := e.deliveryMRsFromRun(gitlabRun)
			if ensureCalled != c.wantEnsure {
				t.Fatalf("EnsureBranchMR called=%v want %v", ensureCalled, c.wantEnsure)
			}
			if len(mrs) != c.wantLen {
				t.Fatalf("mrs=%+v want len %d", mrs, c.wantLen)
			}
			if c.wantLen == 1 && mrs[0].IID != c.wantIID {
				t.Fatalf("mr iid=%d want %d", mrs[0].IID, c.wantIID)
			}
		})
	}

	// A GitHub repository never triggers the GitLab create fallback.
	ghRun := domain.Run{ID: "r2", Repositories: []domain.RepositoryContext{{
		RepositoryID: "b", Name: "repo", Provider: "github",
		GongfengProjectID: "github:owner/repo", WorkBranch: "feature/y", BaseBranch: "main",
	}}}
	ensureCalled := false
	e := &Engine{
		FindBranchMR: func(string, string) (domain.DeliveryMR, bool, error) { return domain.DeliveryMR{}, false, nil },
		EnsureBranchMR: func(string, string, string, int64) (domain.DeliveryMR, bool, error) {
			ensureCalled = true
			return domain.DeliveryMR{}, false, nil
		},
	}
	_ = e.deliveryMRsFromRun(ghRun)
	if ensureCalled {
		t.Fatal("GitHub run must not call the GitLab create fallback")
	}
}
