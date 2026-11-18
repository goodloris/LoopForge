package repo

import (
	"fmt"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// A work MR merged into the integrate branch and later promoted counts as a
// merged MR dated by the promote MR, not by its integrate-branch merge, and
// leaves integratedMrs. Without a promote MR it stays undated (integrated).
func TestPromotedIntegrationMRCountsAsMergedAtPromote(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	prefix := fmt.Sprintf("promoted-%d", time.Now().UnixNano())
	project := domain.Project{ID: prefix, Name: "Promoted"}
	if err := s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	repoKey := prefix + "/shop"
	if err := s.PutRepository(domain.Repository{ID: prefix + "-repo", ProjectID: prefix, Name: "shop", GongfengProjectID: repoKey, DefaultBranch: "main", IntegrateBranch: "integration"}); err != nil {
		t.Fatal(err)
	}
	workAt := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	promoteAt := workAt.Add(26 * time.Hour) // next day: the window decides which clock is used
	issue := domain.Issue{ID: prefix + "-issue", ProjectID: prefix, Status: "done", Kind: "requirement", CompletedAt: promoteAt}
	if err := s.IssuesRepo.writeIssue(issue); err != nil {
		t.Fatal(err)
	}
	put := func(iid int, issueID, target string, at time.Time) {
		t.Helper()
		if err := s.EnsureMRDeliveryState(repoKey, iid, domain.GongfengMRURL(repoKey, iid), issueID); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateMRDeliveryState(repoKey, iid, "merged", target, at, at.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	put(1, issue.ID, "integration", workAt)
	// Merged after the last promote MR, then pushed to main directly: no
	// promote MR dates it.
	put(2, issue.ID, "integration", promoteAt.Add(2*time.Hour))
	put(7, domain.BaselinePromoteIssueID, "main", workAt.Add(-time.Hour)) // earlier promote: did not carry the work
	put(8, domain.BaselinePromoteIssueID, "main", promoteAt)
	put(9, domain.BaselinePromoteIssueID, "main", promoteAt.Add(time.Hour))

	type counts struct{ merged, integrated, report, reportIntegrated int }
	measure := func(from, to time.Time) (counts, []domain.AnalyticsMRMerge) {
		t.Helper()
		filter := domain.AnalyticsFilter{ProjectID: prefix, From: from, To: to}
		got := s.Analytics(filter)
		report, err := s.BusinessDomainReport(filter, []domain.Project{project}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return counts{got.Summary.MergedMRs, got.Summary.IntegratedMRs, report.DeliveryMetrics.MergedMRs, report.DeliveryMetrics.IntegratedMRs}, got.MRMerges
	}
	workDay := func() (time.Time, time.Time) { return workAt.Add(-time.Hour), workAt.Add(time.Hour) }
	promoteDay := func() (time.Time, time.Time) { return promoteAt.Add(-time.Hour), promoteAt.Add(3 * time.Hour) }

	if c, _ := measure(workDay()); c != (counts{0, 1, 0, 1}) {
		t.Fatalf("before promotion, integrate-branch day: %+v", c)
	}
	if c, _ := measure(promoteDay()); c != (counts{0, 1, 0, 1}) {
		t.Fatalf("before promotion, promote day: %+v", c)
	}
	mrURL := domain.GongfengMRURL(repoKey, 1)
	if err := s.ApplyDefaultBranchLanding(issue.ID, mrURL, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyDefaultBranchLanding(issue.ID, domain.GongfengMRURL(repoKey, 2), true); err != nil {
		t.Fatal(err)
	}
	if c, _ := measure(workDay()); c != (counts{0, 0, 0, 0}) {
		t.Fatalf("a promoted MR is no longer counted on its integrate-branch day: %+v", c)
	}
	c, merges := measure(promoteDay())
	if c != (counts{1, 1, 1, 1}) || len(merges) != 1 || merges[0].MRIID != 1 || !merges[0].MergedAt.Equal(promoteAt) {
		t.Fatalf("promote day: promoted MR merged at the promote, undated one still integrated: %+v %+v", c, merges)
	}
	// Leaving the default branch again clears the landing.
	if err := s.ApplyDefaultBranchLanding(issue.ID, mrURL, false); err != nil {
		t.Fatal(err)
	}
	if c, _ := measure(promoteDay()); c != (counts{0, 1, 0, 1}) {
		t.Fatalf("demoted MR still dated by promote: %+v", c)
	}
	if c, _ := measure(workDay()); c != (counts{0, 1, 0, 1}) {
		t.Fatalf("demoted MR is integrated again: %+v", c)
	}
}
