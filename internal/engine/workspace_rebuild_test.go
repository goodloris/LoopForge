package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/repo"
)

// setWorkspaceBind rewrites the persisted (host, root) bind of a run. The bind
// columns are owned by lease operations and PutRun cannot change them, so the
// two-host fixtures set them directly in the ephemeral test DB, as the
// split-worker checkpoint test does.
func setWorkspaceBind(t *testing.T, e *Engine, id, host, root string) {
	t.Helper()
	if err := e.repo.DB.Table("runs").Where("id = ?", id).Updates(map[string]any{"workspace_host": host, "workspace_root": root}).Error; err != nil {
		t.Fatal(err)
	}
}

// D33: the platform container is rebuilt from source, so its hostname changes.
// A saved (reclaimed) chat workspace still names the old hostname. Sending a
// message used to fail with a control-token error; now this instance takes the
// bind over locally and restores the checkout from the database-stored
// checkpoint. Single-container deployments configure no control token.
func TestRebuiltHostReclaimsSavedForeignWorkspaceAndRestores(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	e.reconcileTerminalWorkspaceCleanup()
	if _, err := os.Stat(e.RunWorkspace(id)); !os.IsNotExist(err) {
		t.Fatal("precondition: workspace not saved and reclaimed")
	}
	// The old container's hostname; the data volume (sandbox root) persisted.
	setWorkspaceBind(t, e, id, "475fdb7087fb", e.sandboxes.Root())
	run, _ := e.repo.RunState(id)
	if !e.foreignWorkspaceBind(run) {
		t.Fatal("precondition: rebuilt host must look foreign")
	}
	if !e.workspaceReclaimableHere(run) {
		t.Fatal("a saved workspace on a gone host must be reclaimable")
	}

	token, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.EnsureConversationWorkspace(context.Background(), id, token); err != nil {
		t.Fatalf("rebuilt host could not re-attach history: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(e.RunWorkspace(id), "README.md")); err != nil || string(body) != "uncommitted" {
		t.Fatalf("restore after rebuild lost changes: %q %v", body, err)
	}
	if err = e.EndConversationWorkspaceUse(context.Background(), id, token); err != nil {
		t.Fatal(err)
	}
	run, _ = e.repo.RunState(id)
	if run.WorkspaceHost != e.workspaceHost || run.WorkspaceRoot != e.sandboxes.Root() {
		t.Fatalf("bind not moved to the rebuilt host: %s:%s", run.WorkspaceHost, run.WorkspaceRoot)
	}
}

// A workspace a still-alive worker/instance advertises must never be stolen,
// even when this instance holds the lease and the checkout is elsewhere.
func TestLiveOwnerWorkspaceIsNotReclaimed(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	e.reconcileTerminalWorkspaceCleanup()
	setWorkspaceBind(t, e, id, "owner-host", "/owner/workspaces")
	// A live worker still advertises owner-host.
	if err := e.repo.HeartbeatEngineInstance(repo.EngineInstance{InstanceID: "owner-instance", Role: "worker", WorkspaceHost: "owner-host"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	run, _ := e.repo.RunState(id)
	if e.workspaceReclaimableHere(run) {
		t.Fatal("a live owner's workspace was reclaimed")
	}
	token, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	defer e.EndConversationWorkspaceUse(context.Background(), id, token)
	// With a live owner that publishes no control URL and no shared token, the
	// attempt reports the owner unreachable rather than rebinding locally.
	if err = e.EnsureConversationWorkspace(context.Background(), id, token); err == nil {
		t.Fatal("foreign workspace served without reaching its live owner")
	}
	run, _ = e.repo.RunState(id)
	if run.WorkspaceHost != "owner-host" {
		t.Fatalf("live owner's bind was moved: %s", run.WorkspaceHost)
	}
}

// A legacy row names a retired host and its checkout is still on local disk
// (the container was rebuilt but its volume persisted, and the workspace was
// never saved). The runtime takes it over in place, keeping the files, with no
// migration required.
func TestLegacyForeignHostWithLocalCheckoutIsReclaimedInPlace(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	// No reclaim: files are still on local disk, no checkpoint saved yet.
	setWorkspaceBind(t, e, id, "legacy-host", e.sandboxes.Root())
	run, _ := e.repo.RunState(id)
	if meta, ok := e.repo.WorkspaceCheckpoint(id); ok && meta.SavedAt != nil {
		t.Fatal("precondition: expected no saved checkpoint")
	}
	if !e.workspaceReclaimableHere(run) {
		t.Fatal("legacy row with local files must be reclaimable")
	}
	token, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	defer e.EndConversationWorkspaceUse(context.Background(), id, token)
	if err = e.EnsureConversationWorkspace(context.Background(), id, token); err != nil {
		t.Fatalf("legacy row could not be adopted: %v", err)
	}
	run, _ = e.repo.RunState(id)
	if run.WorkspaceHost != e.workspaceHost {
		t.Fatalf("legacy bind not moved locally: %s", run.WorkspaceHost)
	}
	if body, err := os.ReadFile(filepath.Join(e.RunWorkspace(id), "README.md")); err != nil || string(body) != "uncommitted" {
		t.Fatalf("in-place adoption lost local files: %q %v", body, err)
	}
}
