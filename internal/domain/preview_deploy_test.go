package domain

import "testing"

func TestAttachPreviewDeploy(t *testing.T) {
	base := WorkflowDefinition{
		Nodes: []WorkflowNode{{ID: "impl", Type: "agent"}, {ID: "accept", Type: "approval"}},
		Edges: []WorkflowEdge{{ID: "e1", Source: "impl", Target: "accept"}},
	}
	compose := &PreviewContract{Enabled: true, Kind: PreviewKindCompose}

	// Appended after the success sink, like the static build node.
	wf := base
	wf.Preview = compose
	got, err := AttachPreviewBuild(wf)
	if err != nil || len(got.Nodes) != 3 || got.Nodes[2].ID != PreviewDeployNodeID {
		t.Fatalf("append: %v %+v", err, got.Nodes)
	}
	if last := got.Edges[len(got.Edges)-1]; last.Source != "accept" || last.Target != PreviewDeployNodeID {
		t.Fatalf("deploy edge = %+v", last)
	}
	if again, _ := AttachPreviewBuild(got); len(again.Nodes) != 3 || len(again.Edges) != len(got.Edges) {
		t.Fatal("attach must be idempotent")
	}

	// A user-placed static build node is replaced in place on a kind switch.
	wf = base
	wf.Preview = &PreviewContract{Enabled: true}
	static, err := AttachPreviewBuild(wf)
	if err != nil {
		t.Fatal(err)
	}
	static.Edges = append(static.Edges, WorkflowEdge{ID: "after", Source: PreviewBuildNodeID, Target: "accept"})
	static.Preview = compose
	switched, err := AttachPreviewBuild(static)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range switched.Nodes {
		if IsPreviewBuildNode(n.ID) {
			t.Fatal("static build node survived a switch to compose")
		}
	}
	retargeted := 0
	for _, e := range switched.Edges {
		if e.Source == PreviewDeployNodeID || e.Target == PreviewDeployNodeID {
			retargeted++
		}
	}
	if retargeted != 2 {
		t.Fatalf("edges not retargeted: %+v", switched.Edges)
	}

	// Switching back to static or disabling drops the deploy node.
	switched.Preview = &PreviewContract{Enabled: true}
	back, _ := AttachPreviewBuild(switched)
	if hasPreviewNode(back, IsPreviewDeployNode) || !hasPreviewNode(back, IsPreviewBuildNode) {
		t.Fatalf("static after compose = %+v", back.Nodes)
	}
	switched.Preview = nil
	if off, _ := AttachPreviewBuild(switched); hasPreviewNode(off, IsPreviewDeployNode) {
		t.Fatal("disabled preview kept the deploy node")
	}
}

func TestAttachPreviewDeployKeepsManualPlacement(t *testing.T) {
	compose := &PreviewContract{Enabled: true, Kind: PreviewKindCompose}
	// A → node → B, placed by hand, for both kinds of preview node.
	for _, placed := range []string{PreviewBuildNodeID, PreviewDeployNodeID} {
		wf := WorkflowDefinition{
			Preview: compose,
			Nodes:   []WorkflowNode{{ID: "a", Type: "agent"}, {ID: placed, Type: "script", Script: "x", Position: Position{X: 333, Y: 44}}, {ID: "b", Type: "approval"}},
			Edges:   []WorkflowEdge{{ID: "in", Source: "a", Target: placed}, {ID: "out", Source: placed, Target: "b"}},
		}
		got, err := AttachPreviewBuild(wf)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Nodes) != 3 || got.Nodes[1].ID != PreviewDeployNodeID || got.Nodes[1].Position != (Position{X: 333, Y: 44}) {
			t.Fatalf("%s: nodes = %+v", placed, got.Nodes)
		}
		if len(got.Edges) != 2 || got.Edges[0].Source != "a" || got.Edges[0].Target != PreviewDeployNodeID || got.Edges[1].Source != PreviewDeployNodeID || got.Edges[1].Target != "b" {
			t.Fatalf("%s: B lost its upstream: %+v", placed, got.Edges)
		}
	}
}
