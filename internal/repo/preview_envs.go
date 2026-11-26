package repo

import (
	"errors"
	"strings"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
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

// ClaimPreviewEnvironmentDeploy moves env.ID to deploying for env.LastRunID
// and the attempt identified by nonce. A missing row is inserted; a duplicate
// key means the row exists, and it is claimed only from ready, failed or
// destroyed, from a deploying claim older than deployStaleBefore (its
// deployer died), or from a deploying claim of the same run (a new attempt
// of that run takes over its own earlier attempt). Concurrent first deploys
// therefore create exactly one row and one password; the stored admin
// password wins over env.AdminPassword. On success the stored row is
// returned; otherwise the current row with ok=false.
func (r *PreviewsRepo) ClaimPreviewEnvironmentDeploy(env domain.PreviewEnvironment, nonce string, deployStaleBefore time.Time) (domain.PreviewEnvironment, bool, error) {
	id := strings.TrimSpace(env.ID)
	if id == "" || strings.TrimSpace(env.LastRunID) == "" || nonce == "" {
		return domain.PreviewEnvironment{}, false, errors.New("preview environment id, run and claim nonce are required")
	}
	now := time.Now().UTC()
	row := previewEnvironmentRow{
		ID: id, ProjectID: env.ProjectID, IssueID: env.IssueID, RepositoryID: env.RepositoryID, RunnerID: env.RunnerID,
		LastRunID: env.LastRunID, Branch: env.Branch, Status: domain.PreviewEnvDeploying, URL: env.URL,
		AdminPassword: env.AdminPassword, ClaimNonce: nonce, Created: &now, Updated: &now,
	}
	err := r.DB.Create(&row).Error
	if err == nil {
		stored, ok := r.PreviewEnvironment(id)
		return stored, ok, nil
	}
	if !errors.Is(err, gorm.ErrDuplicatedKey) {
		return domain.PreviewEnvironment{}, false, err
	}
	claimed := r.DB.Model(&previewEnvironmentRow{}).
		Where("id = ? AND (status IN ? OR (status = ? AND (updated_at < ? OR last_run_id = ?)))", id,
			[]string{domain.PreviewEnvReady, domain.PreviewEnvFailed, domain.PreviewEnvDestroyed}, domain.PreviewEnvDeploying, deployStaleBefore.UTC(), env.LastRunID).
		Updates(map[string]any{
			"project_id": env.ProjectID, "issue_id": env.IssueID, "repository_id": env.RepositoryID, "runner_id": env.RunnerID,
			"last_run_id": env.LastRunID, "branch": env.Branch, "url": env.URL, "status": domain.PreviewEnvDeploying,
			"claim_nonce": nonce, "last_error": "", "destroyed_at": nil, "updated_at": now,
			"admin_password": gorm.Expr("CASE WHEN admin_password = '' THEN ? ELSE admin_password END", env.AdminPassword),
		})
	if claimed.Error != nil {
		return domain.PreviewEnvironment{}, false, claimed.Error
	}
	stored, _ := r.PreviewEnvironment(id)
	return stored, claimed.RowsAffected == 1, nil
}

// FinishPreviewEnvironmentDeploy records a deploy outcome (ready with sha,
// or failed with lastError) only while the deploy attempt identified by nonce
// still holds the deploying claim. It reports false when another deploy or a
// teardown took the row meanwhile.
func (r *PreviewsRepo) FinishPreviewEnvironmentDeploy(id, nonce, status, sha, lastError string) (bool, error) {
	now := time.Now().UTC()
	values := map[string]any{"status": status, "last_error": lastError, "updated_at": now}
	if status == domain.PreviewEnvReady {
		values["sha"], values["deployed_at"] = sha, now
	}
	result := r.DB.Model(&previewEnvironmentRow{}).
		Where("id = ? AND status = ? AND claim_nonce = ?", strings.TrimSpace(id), domain.PreviewEnvDeploying, nonce).
		Updates(values)
	return result.RowsAffected == 1, result.Error
}

// ClaimPreviewEnvironmentTeardown moves an environment to destroying for the
// teardown attempt identified by nonce: from ready or failed, from a
// deploying claim older than deployStaleBefore, from a teardown that failed
// (destroying with last_error, retried at once), or from a destroying claim
// older than teardownStaleBefore. Only one caller wins; fresh deploys and
// teardowns in progress are left alone.
func (r *PreviewsRepo) ClaimPreviewEnvironmentTeardown(id, nonce string, teardownStaleBefore, deployStaleBefore time.Time) (domain.PreviewEnvironment, bool, error) {
	if nonce == "" {
		return domain.PreviewEnvironment{}, false, errors.New("claim nonce is required")
	}
	now := time.Now().UTC()
	result := r.DB.Model(&previewEnvironmentRow{}).
		Where("id = ? AND (status IN ? OR (status = ? AND (last_error <> '' OR updated_at < ?)) OR (status = ? AND updated_at < ?))", strings.TrimSpace(id),
			[]string{domain.PreviewEnvReady, domain.PreviewEnvFailed},
			domain.PreviewEnvDestroying, teardownStaleBefore.UTC(), domain.PreviewEnvDeploying, deployStaleBefore.UTC()).
		Updates(map[string]any{"status": domain.PreviewEnvDestroying, "claim_nonce": nonce, "last_error": "", "updated_at": now})
	if result.Error != nil || result.RowsAffected != 1 {
		return domain.PreviewEnvironment{}, false, result.Error
	}
	env, ok := r.PreviewEnvironment(id)
	return env, ok, nil
}

// FinishPreviewEnvironmentTeardown marks the environment destroyed, or keeps
// it destroying with lastError so it can be retried, only while the teardown
// attempt identified by nonce still holds the claim.
func (r *PreviewsRepo) FinishPreviewEnvironmentTeardown(id, nonce string, destroyed bool, lastError string) (bool, error) {
	now := time.Now().UTC()
	values := map[string]any{"last_error": lastError, "updated_at": now}
	if destroyed {
		values["status"], values["destroyed_at"], values["last_error"] = domain.PreviewEnvDestroyed, now, ""
	}
	result := r.DB.Model(&previewEnvironmentRow{}).
		Where("id = ? AND status = ? AND claim_nonce = ?", strings.TrimSpace(id), domain.PreviewEnvDestroying, nonce).
		Updates(values)
	return result.RowsAffected == 1, result.Error
}
