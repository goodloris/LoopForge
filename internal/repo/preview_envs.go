package repo

import (
	"errors"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// Compose preview environments (preview_environments), owned by PreviewsRepo.

func previewEnvFromRow(row previewEnvironmentRow) domain.PreviewEnvironment {
	return domain.PreviewEnvironment{
		ID: row.ID, ProjectID: row.ProjectID, IssueID: row.IssueID, RepositoryID: row.RepositoryID,
		RunnerID: row.RunnerID, LastRunID: row.LastRunID, Branch: row.Branch, SHA: row.SHA,
		Status: row.Status, URL: row.URL, AdminPassword: row.AdminPassword, LastError: row.LastError,
		DeployedAt: timeOrZero(row.DeployedAt), DestroyedAt: timeOrZero(row.DestroyedAt),
		CreatedAt: timeOrZero(row.Created), UpdatedAt: timeOrZero(row.Updated),
	}
}

// PutPreviewEnvironment inserts or replaces one environment. CreatedAt is
// kept from an existing row; UpdatedAt is always now.
func (r *PreviewsRepo) PutPreviewEnvironment(env domain.PreviewEnvironment) error {
	if strings.TrimSpace(env.ID) == "" {
		return errors.New("empty preview environment id")
	}
	now := time.Now().UTC()
	if env.CreatedAt.IsZero() {
		if old, ok := r.PreviewEnvironment(env.ID); ok {
			env.CreatedAt = old.CreatedAt
		} else {
			env.CreatedAt = now
		}
	}
	env.UpdatedAt = now
	return upsert(r.DB, "preview_environments", map[string]any{
		"id": env.ID, "project_id": env.ProjectID, "issue_id": env.IssueID, "repository_id": env.RepositoryID,
		"runner_id": env.RunnerID, "last_run_id": env.LastRunID, "branch": env.Branch, "sha": env.SHA,
		"status": env.Status, "url": env.URL, "admin_password": env.AdminPassword, "last_error": env.LastError,
		"deployed_at": timePtr(env.DeployedAt), "destroyed_at": timePtr(env.DestroyedAt),
		"created_at": timePtr(env.CreatedAt), "updated_at": timePtr(env.UpdatedAt),
	})
}

func (r *PreviewsRepo) PreviewEnvironment(id string) (domain.PreviewEnvironment, bool) {
	var row previewEnvironmentRow
	if err := r.DB.Where("id = ?", strings.TrimSpace(id)).Take(&row).Error; err != nil {
		return domain.PreviewEnvironment{}, false
	}
	return previewEnvFromRow(row), true
}

func (r *PreviewsRepo) previewEnvironmentsWhere(query string, args ...any) []domain.PreviewEnvironment {
	var rows []previewEnvironmentRow
	if err := r.DB.Where(query, args...).Order("created_at, id").Find(&rows).Error; err != nil {
		return nil
	}
	out := make([]domain.PreviewEnvironment, 0, len(rows))
	for _, row := range rows {
		out = append(out, previewEnvFromRow(row))
	}
	return out
}

// PreviewEnvironmentsByRun lists the environments last deployed by runID.
func (r *PreviewsRepo) PreviewEnvironmentsByRun(runID string) []domain.PreviewEnvironment {
	return r.previewEnvironmentsWhere("last_run_id = ?", strings.TrimSpace(runID))
}

func (r *PreviewsRepo) PreviewEnvironmentsByIssue(issueID string) []domain.PreviewEnvironment {
	return r.previewEnvironmentsWhere("issue_id = ?", strings.TrimSpace(issueID))
}

// LivePreviewEnvironments lists every environment not yet destroyed.
func (r *PreviewsRepo) LivePreviewEnvironments() []domain.PreviewEnvironment {
	return r.previewEnvironmentsWhere("status <> ?", domain.PreviewEnvDestroyed)
}

// ClaimPreviewEnvironmentTeardown moves a ready or failed environment, or one
// whose teardown has been stuck in destroying since before staleBefore, to
// destroying. Only one caller wins; deploying environments are never claimed.
func (r *PreviewsRepo) ClaimPreviewEnvironmentTeardown(id string, staleBefore time.Time) (domain.PreviewEnvironment, bool, error) {
	now := time.Now().UTC()
	result := r.DB.Model(&previewEnvironmentRow{}).
		Where("id = ? AND (status IN ? OR (status = ? AND updated_at < ?))", strings.TrimSpace(id),
			[]string{domain.PreviewEnvReady, domain.PreviewEnvFailed}, domain.PreviewEnvDestroying, staleBefore.UTC()).
		Updates(map[string]any{"status": domain.PreviewEnvDestroying, "updated_at": now})
	if result.Error != nil || result.RowsAffected != 1 {
		return domain.PreviewEnvironment{}, false, result.Error
	}
	env, ok := r.PreviewEnvironment(id)
	return env, ok, nil
}
