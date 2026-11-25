package repo

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// Credits are Kiro CLI subscription usage: analytics sums them per period,
// trend bucket, SOP, node and project, and never prices them.

func creditsNear(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func creditsTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	prefix := fmt.Sprintf("credits-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		for _, table := range []string{"run_nodes", "run_events", "runs", "issues", "projects"} {
			column := "id"
			if table == "run_nodes" || table == "run_events" {
				column = "run_id"
			}
			s.DB.Table(table).Where(column+" LIKE ?", prefix+"%").Delete(nil)
		}
	})
	if err := s.PutProject(domain.Project{ID: prefix, Name: "Credits"}); err != nil {
		t.Fatal(err)
	}
	return s, prefix
}

// creditsRun is a finished SOP run whose nodes carry the given usages.
func creditsRun(id, projectID, workflowID string, at time.Time, nodes map[string]domain.TokenUsage) domain.Run {
	run := domain.Run{ID: id, WorkflowID: workflowID, Status: "succeeded", CreatedAt: at,
		WorkflowSnapshot: domain.WorkflowDefinition{ID: workflowID, Name: workflowID},
		ExecutionContext: domain.TelemetryContext{ProjectID: projectID}, Nodes: map[string]*domain.NodeRun{}}
	for nodeID, usage := range nodes {
		run.Nodes[nodeID] = &domain.NodeRun{NodeID: nodeID, Status: "succeeded", StartedAt: at, FinishedAt: at.Add(time.Minute), Usage: usage}
		run.Usage.TotalTokens += usage.TotalTokens
		run.Usage.InputTokens += usage.InputTokens
		run.Usage.OutputTokens += usage.OutputTokens
		run.Usage.Credits += usage.Credits
	}
	return run
}

func putCreditsRuns(t *testing.T, s *Store, runs ...domain.Run) {
	t.Helper()
	for _, run := range runs {
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAnalyticsCreditsPeriodFilterTrendAndTraces(t *testing.T) {
	s, prefix := creditsTestStore(t)
	inside := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	outside := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	putCreditsRuns(t, s,
		creditsRun(prefix+"-in-1", prefix, "wf-kiro", inside, map[string]domain.TokenUsage{"dev": {Credits: 44.77}}),
		creditsRun(prefix+"-in-2", prefix, "wf-kiro", inside.AddDate(0, 0, 8), map[string]domain.TokenUsage{"dev": {Credits: 10.5}}),
		creditsRun(prefix+"-out", prefix, "wf-kiro", outside, map[string]domain.TokenUsage{"dev": {Credits: 113.72}}),
	)
	window := domain.AnalyticsFilter{ProjectID: prefix, From: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)}
	for _, grain := range []string{"day", "week", "month"} {
		window.Grain = grain
		out := s.Analytics(window)
		if !creditsNear(out.Summary.TotalCredits, 55.27) {
			t.Fatalf("%s: window credits = %v", grain, out.Summary.TotalCredits)
		}
		buckets := map[string]float64{}
		total := 0.0
		for _, p := range out.Trend {
			if p.Credits > 0 {
				buckets[p.Date] = p.Credits
			}
			total += p.Credits
		}
		if !creditsNear(total, 55.27) {
			t.Fatalf("%s: trend credits = %v (%v)", grain, total, buckets)
		}
		want := map[string]map[string]float64{
			"day":   {"2026-10-05": 44.77, "2026-10-13": 10.5},
			"week":  {"2026-W41": 44.77, "2026-W42": 10.5},
			"month": {"2026-10": 55.27},
		}[grain]
		if len(buckets) != len(want) {
			t.Fatalf("%s: buckets = %v", grain, buckets)
		}
		for k, v := range want {
			if !creditsNear(buckets[k], v) {
				t.Fatalf("%s: bucket %s = %v, want %v (%v)", grain, k, buckets[k], v, buckets)
			}
		}
		traceCredits := 0.0
		for _, trace := range out.RunTraces {
			if trace.RunID == prefix+"-out" {
				t.Fatalf("%s: out-of-window trace %+v", grain, trace)
			}
			traceCredits += trace.Credits
		}
		if len(out.RunTraces) != 2 || !creditsNear(traceCredits, 55.27) {
			t.Fatalf("%s: traces = %+v", grain, out.RunTraces)
		}
	}
	all := s.Analytics(domain.AnalyticsFilter{ProjectID: prefix})
	if !creditsNear(all.Summary.TotalCredits, 168.99) {
		t.Fatalf("unfiltered credits = %v", all.Summary.TotalCredits)
	}
}

func TestAnalyticsCreditsByWorkflowNodeAndProjectWithMixedExecutors(t *testing.T) {
	s, prefix := creditsTestStore(t)
	other := prefix + "-p2"
	if err := s.PutProject(domain.Project{ID: other, Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	tokenUsage := domain.TokenUsage{TotalTokens: 1000, InputTokens: 900, OutputTokens: 100}
	putCreditsRuns(t, s,
		// Mixed: one token-metered node and one Kiro node in the same run.
		creditsRun(prefix+"-mixed", prefix, "wf-mixed", at, map[string]domain.TokenUsage{"plan": tokenUsage, "dev": {Credits: 12.34}}),
		creditsRun(prefix+"-kiro", other, "wf-kiro", at, map[string]domain.TokenUsage{"dev": {Credits: 0.5}}),
		creditsRun(prefix+"-tokens", prefix, "wf-tokens", at, map[string]domain.TokenUsage{"dev": tokenUsage}),
	)
	out := s.Analytics(domain.AnalyticsFilter{ProjectIDs: []string{prefix, other}})
	if len(out.DataErrors) > 0 {
		t.Fatal(out.DataErrors)
	}
	if !creditsNear(out.Summary.TotalCredits, 12.84) || out.Summary.TotalTokens != 2000 {
		t.Fatalf("summary credits=%v tokens=%d", out.Summary.TotalCredits, out.Summary.TotalTokens)
	}
	workflows := map[string]domain.AnalyticsBreakdown{}
	for _, w := range out.Workflows {
		workflows[w.Key] = w
	}
	if w := workflows["wf-mixed"]; !creditsNear(w.Credits, 12.34) || w.Tokens != 1000 {
		t.Fatalf("mixed workflow = %+v", w)
	}
	if w := workflows["wf-kiro"]; !creditsNear(w.Credits, 0.5) || w.Tokens != 0 {
		t.Fatalf("kiro workflow = %+v", w)
	}
	if w := workflows["wf-tokens"]; w.Credits != 0 || w.Tokens != 1000 {
		t.Fatalf("token workflow = %+v", w)
	}
	nodes := map[string]domain.AnalyticsBreakdown{}
	for _, n := range out.Nodes {
		nodes[n.Key] = n
	}
	if n := nodes["wf-mixed/dev"]; !creditsNear(n.Credits, 12.34) || n.Tokens != 0 {
		t.Fatalf("mixed kiro node = %+v", n)
	}
	if n := nodes["wf-mixed/plan"]; n.Credits != 0 || n.Tokens != 1000 {
		t.Fatalf("mixed token node = %+v", n)
	}
	if n := nodes["wf-kiro/dev"]; !creditsNear(n.Credits, 0.5) {
		t.Fatalf("kiro node = %+v", n)
	}
	projects := map[string]domain.AnalyticsProjectUsage{}
	for _, p := range out.ProjectUsage {
		projects[p.ProjectID] = p
	}
	if p := projects[prefix]; !creditsNear(p.Credits, 12.34) || p.Tokens != 2000 {
		t.Fatalf("project usage = %+v", p)
	}
	if p := projects[other]; !creditsNear(p.Credits, 0.5) || p.Tokens != 0 {
		t.Fatalf("other project usage = %+v", p)
	}
}

// Credits must not change any token or cost value, nor enter cost.
func TestAnalyticsCreditsLeaveTokensAndCostUntouched(t *testing.T) {
	at := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	tokenUsage := domain.TokenUsage{TotalTokens: 1000, InputTokens: 900, OutputTokens: 100}
	snapshot := func(credits float64) domain.AnalyticsSnapshot {
		s, prefix := creditsTestStore(t)
		putCreditsRuns(t, s,
			creditsRun(prefix+"-mixed", prefix, "wf-mixed", at, map[string]domain.TokenUsage{"plan": tokenUsage, "dev": {Credits: credits}}),
			creditsRun(prefix+"-kiro", prefix, "wf-kiro", at, map[string]domain.TokenUsage{"dev": {Credits: credits}}),
		)
		return s.Analytics(domain.AnalyticsFilter{ProjectID: prefix})
	}
	with, without := snapshot(25.5), snapshot(0)
	if !creditsNear(with.Summary.TotalCredits, 51) || without.Summary.TotalCredits != 0 {
		t.Fatalf("credits with=%v without=%v", with.Summary.TotalCredits, without.Summary.TotalCredits)
	}
	ws, wo := with.Summary, without.Summary
	if ws.TotalTokens != wo.TotalTokens || ws.TotalCostUSD != wo.TotalCostUSD || ws.TotalCostCNY != wo.TotalCostCNY || ws.CostUnknownRuns != wo.CostUnknownRuns || ws.CostCoveragePct != wo.CostCoveragePct {
		t.Fatalf("summary changed by credits:\nwith    %+v\nwithout %+v", ws, wo)
	}
	byKey := func(list []domain.AnalyticsBreakdown) map[string]domain.AnalyticsBreakdown {
		out := map[string]domain.AnalyticsBreakdown{}
		for _, item := range list {
			out[item.Key] = item
		}
		return out
	}
	for name, pair := range map[string][2][]domain.AnalyticsBreakdown{"workflows": {with.Workflows, without.Workflows}, "nodes": {with.Nodes, without.Nodes}} {
		w, o := byKey(pair[0]), byKey(pair[1])
		if len(w) != len(o) {
			t.Fatalf("%s: %d vs %d", name, len(w), len(o))
		}
		for key, a := range w {
			b := o[key]
			if a.Tokens != b.Tokens || a.CostUSD != b.CostUSD || a.CostCNY != b.CostCNY || a.TokenSharePct != b.TokenSharePct || a.CostSharePct != b.CostSharePct || a.CostUnknownRuns != b.CostUnknownRuns {
				t.Fatalf("%s %s changed by credits:\nwith    %+v\nwithout %+v", name, key, a, b)
			}
		}
	}
	if len(with.Trend) != len(without.Trend) {
		t.Fatalf("trend length %d vs %d", len(with.Trend), len(without.Trend))
	}
	for i := range with.Trend {
		a, b := with.Trend[i], without.Trend[i]
		if a.Tokens != b.Tokens || a.CostUSD != b.CostUSD || a.CostCNY != b.CostCNY || a.CostUnknownRuns != b.CostUnknownRuns {
			t.Fatalf("trend %d changed by credits: %+v vs %+v", i, a, b)
		}
	}
}

// Token-only data keeps its analytics JSON free of credits keys.
func TestAnalyticsTokenOnlySnapshotHasNoCreditsKeys(t *testing.T) {
	s, prefix := creditsTestStore(t)
	at := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	putCreditsRuns(t, s, creditsRun(prefix+"-tokens", prefix, "wf-tokens", at, map[string]domain.TokenUsage{"dev": {TotalTokens: 1000, InputTokens: 900, OutputTokens: 100}}))
	if err := s.PutIssue(domain.Issue{ID: prefix + "-issue", ProjectID: prefix, Title: "需求", Status: "todo", Kind: "requirement", Source: "local"}); err != nil {
		t.Fatal(err)
	}
	out := s.Analytics(domain.AnalyticsFilter{ProjectID: prefix})
	if out.Summary.TotalTokens != 1000 {
		t.Fatalf("tokens = %d", out.Summary.TotalTokens)
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"credits"`, `"conversationCredits"`, `"totalCredits"`} {
		if strings.Contains(string(body), key) {
			t.Fatalf("token-only analytics JSON contains %s", key)
		}
	}
	snap := BuildRequirementSnapshot(domain.Issue{ID: "iss", Kind: "requirement", WorkflowID: "wf-tokens"}, domain.Project{},
		[]domain.Run{creditsRun("run-tokens", prefix, "wf-tokens", at, map[string]domain.TokenUsage{"dev": {TotalTokens: 10}})}, nil, nil, at.Add(time.Hour))
	body, _ = json.Marshal(snap)
	if strings.Contains(string(body), `"credits"`) {
		t.Fatalf("token-only requirement snapshot contains credits: %s", body)
	}
}

func TestBuildRequirementSnapshotSumsCreditsWithChildRuns(t *testing.T) {
	now := time.Now().UTC()
	issue := domain.Issue{ID: "iss-c", ProjectID: "p1", Kind: "requirement", WorkflowID: "wf-parent"}
	child := domain.WorkflowDefinition{ID: "wf-child", Name: "子 SOP", Nodes: []domain.WorkflowNode{{ID: "dev", Name: "开发"}}}
	parent := domain.WorkflowDefinition{ID: "wf-parent", Name: "父 SOP", Nodes: []domain.WorkflowNode{{ID: "sub", Name: "子 SOP", Type: "sop", SOPWorkflowID: child.ID}}}
	runs := []domain.Run{
		{ID: "run-parent", WorkflowID: parent.ID, WorkflowSnapshot: parent, Status: "running", CreatedAt: now,
			Usage: domain.TokenUsage{TotalTokens: 5, Credits: 1.25},
			Nodes: map[string]*domain.NodeRun{"sub": {NodeID: "sub", Status: "running", StartedAt: now, ChildRunID: "run-child"}}},
		{ID: "run-child", WorkflowID: child.ID, WorkflowSnapshot: child, Status: "running", CreatedAt: now.Add(time.Second),
			ExecutionContext: domain.TelemetryContext{IssueID: issue.ID, ParentRunID: "run-parent", ParentNodeID: "sub"},
			Usage:            domain.TokenUsage{Credits: 2.5},
			Nodes:            map[string]*domain.NodeRun{"dev": {NodeID: "dev", Status: "running"}}},
	}
	snap := BuildRequirementSnapshot(issue, domain.Project{}, runs, nil, map[string]domain.WorkflowDefinition{parent.ID: parent, child.ID: child}, now.Add(time.Minute))
	if len(snap.Lanes) != 1 {
		t.Fatalf("lanes = %#v", snap.Lanes)
	}
	if !creditsNear(snap.Lanes[0].Credits, 3.75) || !creditsNear(snap.Overall.Credits, 3.75) {
		t.Fatalf("lane credits=%v overall credits=%v", snap.Lanes[0].Credits, snap.Overall.Credits)
	}
	if snap.Lanes[0].Tokens != 5 || snap.Overall.Tokens != 5 || snap.Overall.CostUSD != 0 {
		t.Fatalf("tokens/cost changed: lane=%d overall=%+v", snap.Lanes[0].Tokens, snap.Overall)
	}
}

// Kiro conversation turns report only credits: they must reach the
// conversation workspace usage (defect 1) and, without a linked requirement,
// still join the project breakdown (defect 2).
func TestConversationCreditsReachWorkspaceAndAnalytics(t *testing.T) {
	s, prefix := creditsTestStore(t)
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	workspace := domain.Run{ID: prefix + "-ws", Kind: domain.RunKindWorkspace, WorkflowID: domain.ConversationWorkspaceWorkflowID, Status: "waiting", Input: "对话改代码", CreatedAt: at,
		WorkflowSnapshot: domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID, Name: "对话工作区"}, ExecutionContext: domain.TelemetryContext{ProjectID: prefix, ResponsibleUserID: 7}, Nodes: map[string]*domain.NodeRun{}}
	putCreditsRuns(t, s, workspace)
	for _, credits := range []float64{1.5, 0.25} {
		if err := s.AddConversationRunUsage(workspace.ID, "kiro", domain.TokenUsage{Credits: credits}); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := s.RunState(workspace.ID)
	if !ok {
		t.Fatal("workspace run missing")
	}
	if !creditsNear(got.Usage.Credits, 1.75) || got.Usage.TotalTokens != 0 {
		t.Fatalf("workspace usage = %+v", got.Usage)
	}
	out := s.Analytics(domain.AnalyticsFilter{ProjectID: prefix})
	if !creditsNear(out.Summary.ConversationCredits, 1.75) || !creditsNear(out.Summary.TotalCredits, 1.75) {
		t.Fatalf("summary conversation credits=%v total=%v", out.Summary.ConversationCredits, out.Summary.TotalCredits)
	}
	if out.Summary.TotalCostUSD != 0 || out.Summary.ConversationCostCNY != 0 {
		t.Fatalf("credits must not be priced: %+v", out.Summary)
	}
	trendCredits, trendConversation := 0.0, 0.0
	for _, p := range out.Trend {
		trendCredits += p.Credits
		trendConversation += p.ConversationCredits
	}
	if !creditsNear(trendCredits, 1.75) || !creditsNear(trendConversation, 1.75) {
		t.Fatalf("trend credits=%v conversation=%v", trendCredits, trendConversation)
	}
	if len(out.ProjectUsage) != 1 || !creditsNear(out.ProjectUsage[0].Credits, 1.75) {
		t.Fatalf("project usage = %+v", out.ProjectUsage)
	}
}

func TestAnalyticsSessionUsageCountsCredits(t *testing.T) {
	var usage domain.AnalyticsSessionUsage
	accumulateSessionUsage(&usage, &domain.TokenUsage{Credits: 0.75}, false)
	accumulateSessionUsage(&usage, &domain.TokenUsage{TotalTokens: 10, Credits: 0.5}, false)
	accumulateSessionUsage(&usage, nil, false)
	if !creditsNear(usage.Credits, 1.25) || usage.Tokens != 10 || usage.ReportedReplies != 2 || usage.MissingReplies != 1 {
		t.Fatalf("session usage = %+v", usage)
	}
}
