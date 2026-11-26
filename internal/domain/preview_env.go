package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// Compose preview environments: one isolated `docker compose` project per
// requirement × repository on a dedicated preview runner. Design:
// docs/proposals/compose-preview-environments.md.

const (
	PreviewKindCompose = "compose"

	// PreviewDefaultComposeFile is relative to the repository root.
	PreviewDefaultComposeFile = ".loopforge/preview/compose.yaml"

	PreviewEnvDeploying  = "deploying"
	PreviewEnvReady      = "ready"
	PreviewEnvFailed     = "failed"
	PreviewEnvDestroying = "destroying"
	PreviewEnvDestroyed  = "destroyed"
)

// PreviewEnvID is the stable environment id for an issue and repository, so
// a rerun keeps the same URL. Callers without an issue pass the run ID.
func PreviewEnvID(issueID, repositoryID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(issueID) + "\x00" + strings.TrimSpace(repositoryID)))
	return "pe-" + hex.EncodeToString(sum[:])[:10]
}

// PreviewEnvironment is the control-plane record of one compose preview.
// ponytail: AdminPassword is stored in plain text; acceptable only for
// sandbox previews until credentials are stored encrypted.
type PreviewEnvironment struct {
	ID            string    `json:"id"` // PreviewEnvID
	ProjectID     string    `json:"projectId"`
	IssueID       string    `json:"issueId,omitempty"`
	RepositoryID  string    `json:"repositoryId"`
	RunnerID      string    `json:"runnerId"`
	LastRunID     string    `json:"lastRunId,omitempty"`
	Branch        string    `json:"branch,omitempty"`
	SHA           string    `json:"sha,omitempty"`
	Status        string    `json:"status"`
	URL           string    `json:"url,omitempty"`
	AdminPassword string    `json:"adminPassword,omitempty"`
	LastError     string    `json:"lastError,omitempty"`
	DeployedAt    time.Time `json:"deployedAt,omitempty"`
	DestroyedAt   time.Time `json:"destroyedAt,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// RunnerPreviewSpec is the payload of preview_deploy / preview_teardown.
// Teardown only needs EnvID.
type RunnerPreviewSpec struct {
	EnvID       string            `json:"envId"`
	ComposeFile string            `json:"composeFile,omitempty"` // relative to the repository root; default PreviewDefaultComposeFile
	Host        string            `json:"host,omitempty"`        // URL host without port, used for the Caddy route
	Env         map[string]string `json:"env,omitempty"`         // compose interpolation variables (LF_PREVIEW_ID, PREVIEW_ADMIN_PASSWORD)
	Ref         string            `json:"ref,omitempty"`         // branch
	SHA         string            `json:"sha,omitempty"`
}

// ComposeFileOrDefault returns the compose file path relative to the repository root.
func (s RunnerPreviewSpec) ComposeFileOrDefault() string {
	if file := strings.TrimSpace(s.ComposeFile); file != "" {
		return file
	}
	return PreviewDefaultComposeFile
}

// RunnerPreviewResult is the JSON Result of a preview task. The URL is not
// reported: the control plane derives it from the env id.
type RunnerPreviewResult struct {
	Status   string `json:"status"` // PreviewEnvReady after deploy, PreviewEnvDestroyed after teardown
	SHA      string `json:"sha,omitempty"`
	Seconds  int    `json:"seconds,omitempty"`
	LogsTail string `json:"logsTail,omitempty"`
	// Leftover is set by teardown: resources still labelled lf.preview=<envId>.
	Leftover *RunnerPreviewLeftover `json:"leftover,omitempty"`
}

type RunnerPreviewLeftover struct {
	Containers int `json:"containers"`
	Volumes    int `json:"volumes"`
	Networks   int `json:"networks"`
	// Images built by the preview (labelled by the runner's override).
	Images int `json:"images"`
}
