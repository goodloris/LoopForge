package repo

import (
	"strings"
	"time"

	"agentflow/internal/domain"
)

// mergedMRClassifier classifies merged MR rows the way delivery labels do:
// "merged" only when the target is the repository default branch,
// "integrated" for the integrate (or another non-default) branch, "unknown"
// when the target is not recorded. Repositories are read once per call.
func (r *AnalyticsRepo) mergedMRClassifier() func(mrDeliveryStateRow) string {
	repositories := r.catalog.Repositories()
	return func(row mrDeliveryStateRow) string {
		def, integ := "", domain.DefaultIntegrateBranch
		for _, repository := range repositories {
			if strings.EqualFold(strings.TrimSpace(repository.GongfengProjectID), strings.TrimSpace(row.MRProject)) {
				def, integ = strings.TrimSpace(repository.DefaultBranch), strings.TrimSpace(repository.IntegrateBranch)
				break
			}
		}
		return domain.ClassifyMRDeliveryState(row.State, row.TargetBranch, def, integ)
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
	if !filter.From.IsZero() {
		q = q.Where("merged_at IS NULL OR merged_at >= ?", filter.From.UTC())
	}
	if !filter.To.IsZero() {
		q = q.Where("merged_at IS NULL OR merged_at <= ?", filter.To.UTC())
	}
	var rows []mrDeliveryStateRow
	if err := q.Order("merged_at DESC, mr_project ASC, mr_iid ASC").Find(&rows).Error; err != nil {
		return items, 0, 0
	}
	classify := r.mergedMRClassifier()
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
		switch classify(row) {
		case "merged":
		case "integrated":
			if row.MergedAt != nil && !row.MergedAt.IsZero() {
				integrated++
			}
			continue
		default:
			// The target is not recorded yet; the MR sync refreshes it.
			continue
		}
		if row.MergedAt == nil || row.MergedAt.IsZero() {
			missingTime++
			continue
		}
		url := row.MRURL
		if url == "" {
			url = domain.GongfengMRURL(row.MRProject, row.MRIID)
		}
		items = append(items, domain.AnalyticsMRMerge{
			MRProject: row.MRProject, MRIID: row.MRIID, MRURL: url,
			TargetBranch: row.TargetBranch, IssueID: issue.ID, Title: issue.Title,
			MergedAt: row.MergedAt.UTC(),
		})
	}
	return items, missingTime, integrated
}
