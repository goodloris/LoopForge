package httpapi

import (
	"net/http"
	"strings"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// verifyGateID is the command gate the built-in code SOPs give 测试验证: it runs
// .loopforge/verify from each repository's target branch, and without that
// file the test verdict stays the Agent's (migrations/038_generic_sop_gates.sql).
const verifyGateID = "loopforge-verify"

const verifyCommandPath = ".loopforge/verify"

type verifyGateRepository struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Branch string `json:"branch"`
	// Status is configured, missing or unknown (not a GitHub repository, or
	// the check failed). The check never fails the request.
	Status string `json:"status"`
}

// workflowVerifyGate answers GET /api/workflows/:id/verify-gate: whether the
// SOP uses the loopforge-verify gate and, for the requirement's repositories
// (issueId) or the project's (projectId, else the SOP's project), whether the
// target branch carries .loopforge/verify. Read-only.
func (h *catalogHandlers) workflowVerifyGate(c *gin.Context, wf domain.WorkflowDefinition) {
	if !h.workflowUsesVerifyGate(wf) {
		c.JSON(http.StatusOK, gin.H{"gate": false, "repositories": []verifyGateRepository{}})
		return
	}
	var repos []domain.Repository
	if issueID := strings.TrimSpace(c.Query("issueId")); issueID != "" {
		if issue, ok := h.repo.Issue(issueID); ok {
			for _, id := range issue.EffectiveRepositoryIDs() {
				if repository, found := h.repo.Repository(id); found {
					repos = append(repos, repository)
				}
			}
		}
	} else {
		projectID := strings.TrimSpace(c.Query("projectId"))
		if projectID == "" {
			projectID = wf.ProjectID
		}
		for _, repository := range h.repo.Repositories() {
			if projectID != "" && repository.ProjectID == projectID {
				repos = append(repos, repository)
			}
		}
	}
	out := make([]verifyGateRepository, 0, len(repos))
	for _, repository := range repos {
		out = append(out, h.verifyCommandStatus(repository))
	}
	c.JSON(http.StatusOK, gin.H{"gate": true, "repositories": out})
}

func (h *catalogHandlers) workflowUsesVerifyGate(wf domain.WorkflowDefinition) bool {
	for _, node := range wf.Nodes {
		id := strings.TrimSpace(node.AgentID)
		if id == "" {
			continue
		}
		agent, ok := h.repo.Agent(id)
		if !ok {
			agent, ok = h.repo.GenericAgentByID(id)
		}
		if !ok {
			continue
		}
		for _, gate := range agent.Verification {
			if gate.ID == verifyGateID {
				return true
			}
		}
	}
	return false
}

// verifyCommandStatus checks the branch the gate reads: the repository's main
// branch, as repositoryBaseBranch in the engine falls back to.
func (h *catalogHandlers) verifyCommandStatus(repository domain.Repository) verifyGateRepository {
	branch := strings.TrimSpace(repository.DefaultBranch)
	if branch == "" {
		branch = "master"
	}
	v := verifyGateRepository{ID: repository.ID, Name: repository.Name, Branch: branch, Status: "unknown"}
	if repository.Provider != "github" || h.sync == nil {
		return v
	}
	found, err := h.sync.GitHubFileOnBranch(repository.GongfengProjectID, branch, verifyCommandPath)
	switch {
	case err != nil:
	case found:
		v.Status = "configured"
	default:
		v.Status = "missing"
	}
	return v
}
