import React from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import ArtifactPanel from "../src/ArtifactPanel.jsx";
import WorkspaceIDE, { workspaceIDEURL } from "../src/WorkspaceIDE.jsx";

afterEach(() => vi.unstubAllGlobals());

describe("side panel IDE tab", () => {
  const chat = { session: { id: "s1", projectId: "p1" }, projectId: "p1", projects: [] };
  const panel = (props) => (
    <div className="chat-shell">
      <header className="conversation-header" />
      <main className="conversation-main" />
      <ArtifactPanel artifacts={[]} runs={[]} selected={null} setSelected={vi.fn()} chat={chat} onClose={vi.fn()} open {...props} />
    </div>
  );

  test("needs a conversation workspace", () => {
    render(panel({ view: "artifacts" }));
    expect(screen.getByRole("tab", { name: /IDE/ })).toBeDisabled();
  });

  test("the changes tab counts uncommitted files and never claims 0", () => {
    // chat-e7c1b6b903da779f474ce3131f3990a8 committed and opened PR #1, so its
    // working tree was clean and the tab read "改动 0".
    const { rerender } = render(panel({ view: "artifacts", workspaceRunId: "run-9", workspaceChanges: { files: [] } }));
    expect(screen.getByRole("tab", { name: "改动" })).toBeEnabled();
    rerender(panel({ view: "artifacts", workspaceRunId: "run-9", workspaceChanges: { files: [{ path: "a.py" }, { path: "b.py" }] } }));
    expect(screen.getByRole("tab", { name: "改动 2" })).toBeInTheDocument();
  });

  test("embeds the workspace in VS Code and follows workspace switches", () => {
    const onViewChange = vi.fn();
    const { rerender } = render(panel({ view: "artifacts", workspaceRunId: "run-9", onViewChange }));
    fireEvent.click(screen.getByRole("tab", { name: /IDE/ }));
    expect(onViewChange).toHaveBeenCalledWith("ide");
    rerender(panel({ view: "ide", workspaceRunId: "run-9", onViewChange }));
    expect(screen.getByRole("complementary", { name: "工作区 IDE" })).toBeInTheDocument();
    const frame = screen.getByLabelText("VS Code");
    expect(frame).toHaveAttribute("src", "/vscode.html?run=run-9&embed=1");
    // A title would show a hover tip over the whole editor.
    expect(frame).not.toHaveAttribute("title");
    rerender(panel({ view: "ide", workspaceRunId: "run-10", onViewChange }));
    expect(screen.getByLabelText("VS Code")).toHaveAttribute("src", "/vscode.html?run=run-10&embed=1");
  });
});

describe("WorkspaceIDE", () => {
  const post = (data, origin = window.location.origin) =>
    window.dispatchEvent(new MessageEvent("message", { data, origin }));

  test("handles popout and maximize only for its own run", () => {
    const open = vi.fn(() => ({ focus() {} }));
    vi.stubGlobal("open", open);
    const onMaximize = vi.fn();
    render(<WorkspaceIDE runId="run-9" onMaximize={onMaximize} />);
    post({ type: "agentflow-vscode", runId: "other", action: "popout" });
    post({ type: "agentflow-vscode", runId: "run-9", action: "popout" }, "https://evil.example");
    expect(open).not.toHaveBeenCalled();
    post({ type: "agentflow-vscode", runId: "run-9", action: "popout" });
    expect(open).toHaveBeenCalledWith(workspaceIDEURL("run-9"), "agentflow-vscode-run-9", expect.any(String));
    post({ type: "agentflow-vscode", runId: "run-9", action: "maximize" });
    expect(onMaximize).toHaveBeenCalledTimes(1);
  });
});
