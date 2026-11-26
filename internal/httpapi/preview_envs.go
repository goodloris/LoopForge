package httpapi

import (
	"net/http"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"

	"github.com/gin-gonic/gin"
)

// previewEnvHandlers owns compose preview environments: listing them per run
// or requirement, minting the one-time ticket that opens one, and manual
// teardown (docs/proposals/compose-preview-environments.md §7). Any
// signed-in user may list environments; the admin password and tickets are
// limited to readers of the owning project (owner, members including
// viewers, platform administrators).
type previewEnvHandlers struct {
	*deps
}

// projectReader mirrors requireProjectWriter's identity rules with the
// membership read predicate: service tokens, unauthenticated deployments and
// unowned projects are allowed.
func (h *previewEnvHandlers) projectReader(c *gin.Context, projectID string) bool {
	if !authRequired() || projectID == "" {
		return true
	}
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		return true
	}
	p, found := h.repo.Project(projectID)
	return !found || workbenchProjectAllowsUser(p, user.ID, h.isPlatformAdmin(c.Request))
}

type previewEnvList struct {
	Enabled   bool                        `json:"enabled"`
	Reason    string                      `json:"reason,omitempty"`
	AdminUser string                      `json:"adminUser"`
	Items     []domain.PreviewEnvironment `json:"items"`
}

func (h *previewEnvHandlers) list(c *gin.Context, items []domain.PreviewEnvironment) {
	if items == nil {
		items = []domain.PreviewEnvironment{}
	}
	readable := map[string]bool{}
	for i := range items {
		project := items[i].ProjectID
		if _, seen := readable[project]; !seen {
			readable[project] = h.projectReader(c, project)
		}
		if !readable[project] {
			items[i].AdminPassword = ""
		}
	}
	reason := h.engine.ComposePreview().Problem()
	c.JSON(http.StatusOK, previewEnvList{Enabled: reason == "", Reason: reason, AdminUser: engine.PreviewAdminUser, Items: items})
}

func (h *previewEnvHandlers) runEnvs(c *gin.Context) {
	runID := c.Param("id")
	if !h.repo.RunExists(runID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
		return
	}
	h.list(c, h.repo.PreviewEnvironmentsByRun(runID))
}

func (h *previewEnvHandlers) issueEnvs(c *gin.Context) {
	issueID := c.Param("id")
	if _, ok := h.repo.Issue(issueID); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "issue not found"})
		return
	}
	h.list(c, h.repo.PreviewEnvironmentsByIssue(issueID))
}

// ticket returns {url}: the environment URL carrying a one-time ticket that
// the preview gateway exchanges for its cookie.
func (h *previewEnvHandlers) ticket(c *gin.Context) {
	env, ok := h.repo.PreviewEnvironment(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "preview environment not found"})
		return
	}
	if !h.projectReader(c, env.ProjectID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有项目成员、项目 Owner 或平台管理员可以打开该预览环境"})
		return
	}
	if env.Status != domain.PreviewEnvReady {
		c.JSON(http.StatusConflict, gin.H{"error": "预览环境当前状态为 " + env.Status + "，部署完成后才能打开"})
		return
	}
	url, err := h.engine.ComposePreview().TicketURL(env.ID, time.Now())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": url})
}

// destroy starts a teardown and answers 202 with the environment. A
// deploying environment answers 409; one already destroying or destroyed is
// returned as it is.
func (h *previewEnvHandlers) destroy(c *gin.Context) {
	env, ok := h.repo.PreviewEnvironment(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "preview environment not found"})
		return
	}
	if !h.requireProjectWriter(c, env.ProjectID) {
		return
	}
	if env.Status == domain.PreviewEnvDeploying {
		c.JSON(http.StatusConflict, gin.H{"error": "预览环境正在部署，完成后再销毁"})
		return
	}
	current, _, err := h.engine.StartPreviewTeardown(env.ID)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusAccepted, current)
}
