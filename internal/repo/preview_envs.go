package repo

import (
	"errors"
	"strings"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// LivePreviewEnvironments lists every environment the teardown sweep may act on.
// ponytail: the sweep reads all of them each minute and looks up each run and
// issue one by one; fine for tens of environments, batch the issue lookups if
// previews grow into the hundreds.
func (r *PreviewsRepo) LivePreviewEnvironments() []domain.PreviewEnvironment {
	return r.previewEnvironmentsWhere("status IN ?", []string{domain.PreviewEnvReady, domain.PreviewEnvFailed, domain.PreviewEnvDeploying, domain.PreviewEnvDestroying})
}

// ClaimPreviewEnvironmentDeploy moves env.ID to deploying for env.LastRunID.
// A missing row is inserted (insert-if-absent, so concurrent first deploys
// create exactly one row and one password); an existing row is claimed only
// from ready, failed or destroyed, or from a deploying claim older than
// deployStaleBefore (its deployer died). The stored admin password wins over
// env.AdminPassword. On success the stored row is returned; otherwise the
// current row with ok=false.
func (r *PreviewsRepo) ClaimPreviewEnvironmentDeploy(env domain.PreviewEnvironment, deployStaleBefore time.Time) (domain.PreviewEnvironment, bool, error) {
	id := strings.TrimSpace(env.ID)
	if id == "" || strings.TrimSpace(env.LastRunID) == "" {
		return domain.PreviewEnvironment{}, false, errors.New("preview environment id and run are required")
	}
	now := time.Now().UTC()
	row := previewEnvironmentRow{
		ID: id, ProjectID: env.ProjectID, IssueID: env.IssueID, RepositoryID: env.RepositoryID, RunnerID: env.RunnerID,
		LastRunID: env.LastRunID, Branch: env.Branch, Status: domain.PreviewEnvDeploying, URL: env.URL,
		AdminPassword: env.AdminPassword, Created: &now, Updated: &now,
	}
	inserted := r.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if inserted.Error != nil {
		return domain.PreviewEnvironment{}, false, inserted.Error
	}
	if inserted.RowsAffected == 0 {
		claimed := r.DB.Model(&previewEnvironmentRow{}).
			Where("id = ? AND (status IN ? OR (status = ? AND updated_at < ?))", id,
				[]string{domain.PreviewEnvReady, domain.PreviewEnvFailed, domain.PreviewEnvDestroyed}, domain.PreviewEnvDeploying, deployStaleBefore.UTC()).
			Updates(map[string]any{
				"project_id": env.ProjectID, "issue_id": env.IssueID, "repository_id": env.RepositoryID, "runner_id": env.RunnerID,
				"last_run_id": env.LastRunID, "branch": env.Branch, "url": env.URL, "status": domain.PreviewEnvDeploying,
				"last_error": "", "destroyed_at": nil, "updated_at": now,
				"admin_password": gorm.Expr("CASE WHEN admin_password = '' THEN ? ELSE admin_password END", env.AdminPassword),
			})
		if claimed.Error != nil {
			return domain.PreviewEnvironment{}, false, claimed.Error
		}
		if claimed.RowsAffected != 1 {
			current, _ := r.PreviewEnvironment(id)
			return current, false, nil
		}
	}
	stored, ok := r.PreviewEnvironment(id)
	return stored, ok, nil
}

// FinishPreviewEnvironmentDeploy records a deploy outcome (ready with sha,
// or failed with lastError) only while the row is still deploying for runID.
// It reports false when another deploy or a teardown took the row meanwhile.
func (r *PreviewsRepo) FinishPreviewEnvironmentDeploy(id, runID, status, sha, lastError string) (bool, error) {
	now := time.Now().UTC()
	values := map[string]any{"status": status, "last_error": lastError, "updated_at": now}
	if status == domain.PreviewEnvReady {
		values["sha"], values["deployed_at"] = sha, now
	}
	result := r.DB.Model(&previewEnvironmentRow{}).
		Where("id = ? AND status = ? AND last_run_id = ?", strings.TrimSpace(id), domain.PreviewEnvDeploying, runID).
		Updates(values)
	return result.RowsAffected == 1, result.Error
}

// ClaimPreviewEnvironmentTeardown moves an environment to destroying from
// ready or failed, from a deploying claim older than deployStaleBefore, or
// from a destroying claim older than teardownStaleBefore. Only one caller
// wins; fresh deploying and destroying environments are left alone.
func (r *PreviewsRepo) ClaimPreviewEnvironmentTeardown(id string, teardownStaleBefore, deployStaleBefore time.Time) (domain.PreviewEnvironment, bool, error) {
	now := time.Now().UTC()
	result := r.DB.Model(&previewEnvironmentRow{}).
		Where("id = ? AND (status IN ? OR (status = ? AND updated_at < ?) OR (status = ? AND updated_at < ?))", strings.TrimSpace(id),
			[]string{domain.PreviewEnvReady, domain.PreviewEnvFailed},
			domain.PreviewEnvDestroying, teardownStaleBefore.UTC(), domain.PreviewEnvDeploying, deployStaleBefore.UTC()).
		Updates(map[string]any{"status": domain.PreviewEnvDestroying, "updated_at": now})
	if result.Error != nil || result.RowsAffected != 1 {
		return domain.PreviewEnvironment{}, false, result.Error
	}
	env, ok := r.PreviewEnvironment(id)
	return env, ok, nil
}

// FinishPreviewEnvironmentTeardown marks a destroying environment destroyed,
// or keeps it destroying with lastError for a later retry. A row that left
// destroying meanwhile is not touched.
func (r *PreviewsRepo) FinishPreviewEnvironmentTeardown(id string, destroyed bool, lastError string) (bool, error) {
	now := time.Now().UTC()
	values := map[string]any{"last_error": lastError, "updated_at": now}
	if destroyed {
		values["status"], values["destroyed_at"], values["last_error"] = domain.PreviewEnvDestroyed, now, ""
	}
	result := r.DB.Model(&previewEnvironmentRow{}).
		Where("id = ? AND status = ?", strings.TrimSpace(id), domain.PreviewEnvDestroying).
		Updates(values)
	return result.RowsAffected == 1, result.Error
}
