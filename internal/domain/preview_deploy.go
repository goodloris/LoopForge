package domain

import "fmt"

const (
	// PreviewDeployNodeID is the synthetic node that deploys compose preview
	// environments. It takes the static build node's place in the graph.
	PreviewDeployNodeID = "af-preview-deploy"
	PreviewDeployEdgeID = "af-preview-deploy-edge"
)

func IsPreviewDeployNode(id string) bool { return id == PreviewDeployNodeID }

// PreviewDeployNode is executed by the engine itself (never as a shell
// script): it pushes each work branch and dispatches preview_deploy. The
// script body only satisfies script-node validation and the canvas.
func PreviewDeployNode(position Position) WorkflowNode {
	return WorkflowNode{
		ID:             PreviewDeployNodeID,
		Name:           "部署预览环境",
		Type:           "script",
		ScriptLanguage: "shell",
		Script:         "# 平台步骤：部署 Compose 预览环境（由 LoopForge 执行，不运行此脚本）",
		ExecutionScope: ExecutionScopeRun,
		OnFailure:      "fail",
		MaxAttempts:    1,
		Position:       position,
	}
}

// attachPreviewDeploy guarantees the compose deploy node. An existing deploy
// node is kept; a static build node left from a kind switch is replaced in
// place (same position and edges); otherwise the node is appended after every
// success sink, exactly where the static build node would go.
func attachPreviewDeploy(wf WorkflowDefinition) WorkflowDefinition {
	for _, node := range wf.Nodes {
		if IsPreviewDeployNode(node.ID) {
			return wf
		}
	}
	nodes := make([]WorkflowNode, 0, len(wf.Nodes)+1)
	replaced := false
	for _, node := range wf.Nodes {
		if IsPreviewBuildNode(node.ID) {
			nodes = append(nodes, PreviewDeployNode(node.Position))
			replaced = true
			continue
		}
		nodes = append(nodes, node)
	}
	if replaced {
		edges := make([]WorkflowEdge, 0, len(wf.Edges))
		for _, edge := range wf.Edges {
			if IsPreviewBuildNode(edge.Source) {
				edge.Source = PreviewDeployNodeID
			}
			if IsPreviewBuildNode(edge.Target) {
				edge.Target = PreviewDeployNodeID
			}
			edges = append(edges, edge)
		}
		wf.Nodes, wf.Edges = nodes, edges
		return wf
	}
	sinks := previewSuccessSinks(wf)
	x, y := previewBuildPosition(wf.Nodes)
	wf.Nodes = append(nodes, PreviewDeployNode(Position{X: x, Y: y}))
	wf.Edges = append([]WorkflowEdge(nil), wf.Edges...)
	for i, source := range sinks {
		wf.Edges = append(wf.Edges, WorkflowEdge{
			ID:     fmt.Sprintf("%s-%d", PreviewDeployEdgeID, i+1),
			Source: source,
			Target: PreviewDeployNodeID,
			When:   "success",
		})
	}
	return wf
}
