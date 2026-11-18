package repo

import (
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// mergedMRLanding classifies merged MR rows the way delivery labels do and
// returns when the work reached the default branch: "merged" at merged_at
// when the target is the default branch, or at the promote time (landed_at)
// when an integrate-branch merge was promoted; "integrated" at merged_at for
// an integrate-branch merge that has not landed; "unknown" when the target is
// not recorded. Repositories are read once per call.
func (r *AnalyticsRepo) mergedMRLanding() func(mrDeliveryStateRow) (string, *time.Time) {
	repositories := r.catalog.Repositories()
	return func(row mrDeliveryStateRow) (string, *time.Time) {
		def, integ := "", domain.DefaultIntegrateBranch
		for _, repository := range repositories {
			if strings.EqualFold(strings.TrimSpace(repository.GongfengProjectID), strings.TrimSpace(row.MRProject)) {
				def, integ = strings.TrimSpace(repository.DefaultBranch), strings.TrimSpace(repository.IntegrateBranch)
				break
			}
		}
		state := domain.ClassifyMRDeliveryState(row.State, row.TargetBranch, def, integ)
		if state == "integrated" && row.LandedAt != nil && !row.LandedAt.IsZero() {
			return "merged", row.LandedAt
		}
		return state, row.MergedAt
	}
}

// mrMergeAnalytics uses the MR clock independently of the Run creation window.
// The primary key (mr_project, mr_iid) counts each MR once, including multiple
// repositories for one requirement. Baseline promotion MRs have no work issue.
// Only MRs merged into the default branch are returned; integrated counts
// those merged into the integrate branch inside the window.
func (r *AnalyticsRepo) mrMergeAnalytics(filter domain.AnalyticsFilter, issues map[string]domain.Issue) (items []domain.AnalyticsMRMerge, missingTime, integrated int) {
	items = []domain.AnalyticsMRMerge{}
	if filter.ProjectIDs != nil && len(filter.ProjectIDs) == 0 {
		return items, 0, 0
	}
	matchingRuns := map[string]bool{}
	if filter.WorkflowID != "" || filter.UserID != "" {
		runFilter := filter
		runFilter.From, runFilter.To = time.Time{}, time.Time{}
		runs, err := r.runs.RunSummariesFor(runFilter, "")
		if err != nil {
			return items, 0, 0
		}
		for _, run := range runs {
			issueID := run.ExecutionContext.IssueID
			if analyticsRunMatchesUser(run, filter.UserID, issues[issueID]) {
				matchingRuns[issueID] = true
			}
		}
	}

	q := r.DB.Where("state = ? AND issue_id <> '' AND issue_id <> ?", "merged", domain.BaselinePromoteIssueID)
	// A promoted MR is dated by landed_at, so either clock may fall in the
	// window; the exact window is applied below on the effective time.
	if !filter.From.IsZero() {
		q = q.Where("merged_at IS NULL OR merged_at >= ? OR landed_at >= ?", filter.From.UTC(), filter.From.UTC())
	}
	if !filter.To.IsZero() {
		q = q.Where("merged_at IS NULL OR merged_at <= ? OR landed_at <= ?", filter.To.UTC(), filter.To.UTC())
	}
	var rows []mrDeliveryStateRow
	if err := q.Order("merged_at DESC, mr_project ASC, mr_iid ASC").Find(&rows).Error; err != nil {
		return items, 0, 0
	}
	landing := r.mergedMRLanding()
	inWindow := func(at time.Time) bool {
		return (filter.From.IsZero() || !at.Before(filter.From)) && (filter.To.IsZero() || !at.After(filter.To))
	}
	for _, row := range rows {
		issue, ok := issues[row.IssueID]
		if !ok || !filter.MatchesProject(issue.ProjectID) ||
			!analyticsIssueMatchesSource(issue, filter.IssueSource) ||
			(filter.RequirementsOnly && !analyticsIsRequirementIssue(issue)) {
			continue
		}
		if filter.WorkflowID != "" || filter.UserID != "" {
			if !matchingRuns[issue.ID] && !(filter.WorkflowID == "" && analyticsRunMatchesUser(domain.Run{}, filter.UserID, issue)) {
				continue
			}
		}
		state, at := landing(row)
		switch state {
		case "merged":
		case "integrated":
			if at != nil && !at.IsZero() && inWindow(*at) {
				integrated++
			}
			continue
		default:
			// The target is not recorded yet; the MR sync refreshes it.
			continue
		}
		if at == nil || at.IsZero() {
			missingTime++
			continue
		}
		if !inWindow(*at) {
			continue
		}
		url := row.MRURL
		if url == "" {
			url = domain.GongfengMRURL(row.MRProject, row.MRIID)
		}
		items = append(items, domain.AnalyticsMRMerge{
			MRProject: row.MRProject, MRIID: row.MRIID, MRURL: url,
			TargetBranch: row.TargetBranch, IssueID: issue.ID, Title: issue.Title,
			MergedAt: at.UTC(),
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].MergedAt.Equal(items[j].MergedAt) {
			return items[i].MergedAt.After(items[j].MergedAt)
		}
		if items[i].MRProject != items[j].MRProject {
			return items[i].MRProject < items[j].MRProject
		}
		return items[i].MRIID < items[j].MRIID
	})
	return items, missingTime, integrated
}
