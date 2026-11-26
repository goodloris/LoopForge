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

// envReader decides who may see an environment's admin password and open it:
// readers of its project (owner, members including viewers, platform
// administrators). An environment whose project is gone is refused; one
// without a project is limited to platform administrators and the
// responsible user of its last run. Unauthenticated deployments and service
// tokens are allowed, as in requireProjectWriter.
func (h *previewEnvHandlers) envReader(c *gin.Context, env domain.PreviewEnvironment) bool {
	if !authRequired() {
		return true
	}
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		return true
	}
	admin := h.isPlatformAdmin(c.Request)
	if env.ProjectID == "" {
		if admin {
			return true
		}
		run, err := h.repo.RunHeader(env.LastRunID)
		return err == nil && run.ID != "" && user.ID > 0 && run.ExecutionContext.ResponsibleUserID == user.ID
	}
	p, found := h.repo.Project(env.ProjectID)
	return found && workbenchProjectAllowsUser(p, user.ID, admin)
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
	for i := range items {
		if !h.envReader(c, items[i]) {
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
	if !h.envReader(c, env) {
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

// destroy starts a teardown and answers 202 with the environment. Writers of
// its project may destroy it; an environment without a project follows
// envReader (platform administrators and the run's responsible user). When
// nothing was claimed it answers 409 with the reason: a deploy or a teardown
// still in progress, or already destroyed. A failed teardown and stale
// deploying/destroying claims are taken over.
func (h *previewEnvHandlers) destroy(c *gin.Context) {
	env, ok := h.repo.PreviewEnvironment(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "preview environment not found"})
		return
	}
	if env.ProjectID == "" {
		if !h.envReader(c, env) {
			c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员或运行责任人可以销毁该预览环境"})
			return
		}
	} else if !h.requireProjectWriter(c, env.ProjectID) {
		return
	}
	current, claimed, err := h.engine.StartPreviewTeardown(env.ID)
	if err != nil {
		respond(c, err)
		return
	}
	if !claimed {
		reason := map[string]string{
			domain.PreviewEnvDeploying:  "预览环境正在部署，完成后再销毁",
			domain.PreviewEnvDestroying: "预览环境正在销毁，请等待完成",
			domain.PreviewEnvDestroyed:  "预览环境已销毁",
		}[current.Status]
		if reason == "" {
			reason = "预览环境当前状态为 " + current.Status + "，无法销毁"
		}
		c.JSON(http.StatusConflict, gin.H{"error": reason, "status": current.Status})
		return
	}
	c.JSON(http.StatusAccepted, current)
}
