package engine

import (
	"os"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// The workspace of a run lives only on the host (or execution machine) that
// checked it out. Nodes do not push intermediate commits: the remote work
// branch receives code only from delivery steps (MR, integration). A child
// SOP works in the checkout of the run that started its SOP chain.

// workspaceReclaimableHere reports a control-plane workspace whose bound host
// is gone, so this instance may take the checkout over locally. The checkpoint
// archive lives in the database, so a saved workspace is restored here without
// the old host; an unsaved one is reclaimed only when its files are still on
// local disk (the platform container was rebuilt but its data volume
// persisted). A live worker or instance that still advertises the host, a run
// on an execution machine, and a child SOP are never reclaimed, so a real
// multi-instance or split-worker owner keeps its workspace.
func (e *Engine) workspaceReclaimableHere(run domain.Run) bool {
	if e.defaultExecutionRunner(run) != "" || sharesWorkspace(run) {
		return false
	}
	if !e.foreignWorkspaceBind(run) {
		return false
	}
	host, _ := e.workspaceBind(run)
	if host == e.workspaceHost || e.repo.WorkspaceHostAlive(host) {
		return false
	}
	if meta, ok := e.repo.WorkspaceCheckpoint(run.ID); ok && meta.SavedAt != nil && meta.Error == "" {
		return true
	}
	root := e.sandboxes.RunWorkspace(run.ID)
	if info, err := os.Lstat(root); err == nil && info.IsDir() {
		if has, herr := runtime.WorkspaceDirHasContent(root); herr == nil && has {
			return true
		}
	}
	return false
}

// workspaceRunID returns the run whose workspace r executes in.
func workspaceRunID(r domain.Run) string {
	if id := strings.TrimSpace(r.ExecutionContext.WorkspaceRunID); id != "" {
		return id
	}
	return r.ID
}

// sharesWorkspace reports whether r is a child SOP working in another run's
// checkout.
func sharesWorkspace(r domain.Run) bool {
	return workspaceRunID(r) != r.ID
}

// nodeControlKey names a node's own control paths in the checkout (gate
// results, materialized inputs, runtime invocation directories). A child SOP
// runs in its parent's checkout, concurrently with the parent's other
// branches, and its workflow may reuse the parent's node IDs; its keys carry
// the child run so neither side clears or reads the other's files.
func nodeControlKey(r domain.Run, nodeID string) string {
	if !sharesWorkspace(r) {
		return nodeID
	}
	return r.ID + "--" + nodeID
}

// workspaceBind returns the (host, root) holding r's workspace. A child SOP
// follows the run that owns the checkout once that run has bound one.
func (e *Engine) workspaceBind(r domain.Run) (string, string) {
	if sharesWorkspace(r) {
		if owner, ok := e.repo.RunState(workspaceRunID(r)); ok && strings.TrimSpace(owner.WorkspaceRoot) != "" {
			return owner.WorkspaceHost, owner.WorkspaceRoot
		}
	}
	return r.WorkspaceHost, r.WorkspaceRoot
}

// hasCodeWorkspace reports whether losing r's workspace loses code changes:
// it checks out a repository, or works in another run's checkout.
func hasCodeWorkspace(r domain.Run) bool {
	if sharesWorkspace(r) {
		return true
	}
	for _, rc := range r.RepositoryContexts() {
		if strings.TrimSpace(rc.RepositoryID) != "" {
			return true
		}
	}
	return false
}

// hasWorkspaceProgress reports whether nodes other than nodeID already
// changed the workspace, so continuing on a fresh checkout would silently
// drop their work.
func hasWorkspaceProgress(r domain.Run, nodeID string) bool {
	if sharesWorkspace(r) {
		return true
	}
	for id, nr := range r.Nodes {
		if id != nodeID && nr != nil && nr.Status == "succeeded" {
			return true
		}
	}
	return false
}
