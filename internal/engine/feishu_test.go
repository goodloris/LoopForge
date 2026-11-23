package engine

import (
	"context"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestResolveRunCredentialsAsksForResponsibleUsersFeishu(t *testing.T) {
	e, _ := cvEEngine(t)
	var asked []int64
	e.FeishuCredential = func(_ context.Context, userID int64) runtime.FeishuCredential {
		asked = append(asked, userID)
		return runtime.FeishuCredential{AppID: "cli_app", TenantToken: "t-tenant", UserToken: "u-owner"}
	}
	run := domain.Run{ID: "feishu-run", ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 42}}
	if got := e.resolveRunCredentials(run).Feishu; got.UserToken != "u-owner" || len(asked) != 1 || asked[0] != 42 {
		t.Fatalf("credentials %+v asked %v", got, asked)
	}
	e.FeishuCredential = nil
	if got := e.resolveRunCredentials(run).Feishu; got != (runtime.FeishuCredential{}) {
		t.Fatalf("without the hook = %+v", got)
	}
}

func TestFeishuBotOnlyNotice(t *testing.T) {
	if notice := feishuBotOnlyNotice(runtime.FeishuCredential{}); notice != "" {
		t.Fatalf("no platform app = %q", notice)
	}
	if notice := feishuBotOnlyNotice(runtime.FeishuCredential{AppID: "a", UserToken: "u"}); notice != "" {
		t.Fatalf("connected = %q", notice)
	}
	if notice := feishuBotOnlyNotice(runtime.FeishuCredential{AppID: "a"}); !strings.HasPrefix(notice, "负责人未连接飞书") || !strings.Contains(notice, "bot") {
		t.Fatalf("not connected = %q", notice)
	}
	if notice := feishuBotOnlyNotice(runtime.FeishuCredential{AppID: "a", Notice: "飞书授权已过期"}); !strings.HasPrefix(notice, "负责人飞书授权已过期") {
		t.Fatalf("expired = %q", notice)
	}
}

// An Agent node on an execution machine carries the responsible user's
// Feishu tokens in its invocation, or a run-log warning on an old runner.
func TestRunnerTaskCarriesFeishuForNewRunnersOnly(t *testing.T) {
	e, _ := cvEEngine(t)
	node := domain.WorkflowNode{ID: "agent", Type: "agent"}
	sb := &runEnvironment{credentials: runtime.RunCredentials{Feishu: runtime.FeishuCredential{AppID: "cli_app", TenantToken: "t-tenant", UserToken: "u-owner"}}}
	for _, tc := range []struct {
		version string
		carried bool
	}{{domain.RunnerVersionFeishu, true}, {domain.RunnerVersionCodeBuddyBaseURL, false}} {
		r := cvERunner(t, e, "feishu-"+strings.ReplaceAll(tc.version, ".", "-"), domain.RunnerStatusOnline, "kiro")
		r.RunnerVersion = tc.version
		if err := e.repo.PutRunner(r); err != nil {
			t.Fatal(err)
		}
		run := cvERunnerRun("feishu-run-"+r.ID, r.ID)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var warned bool
		_, _ = e.dispatchRunnerTask(ctx, r.ID, run, node, sb, "workspace", domain.ExecutorDefinition{ID: "kiro", Kind: "kiro"}, domain.AgentDefinition{ID: "a"}, domain.AgentAssetBundle{}, "prompt", "inv", runtime.InvocationOptions{}, func(event domain.TelemetryEvent) {
			warned = warned || event.Name == "feishu.unavailable"
		})
		var task *domain.RunnerTask
		for _, candidate := range e.repo.RunnerTasks() {
			if candidate.RunnerID == r.ID {
				candidate := candidate
				task = &candidate
			}
		}
		if task == nil || task.Invocation == nil {
			t.Fatalf("%s: no task queued", tc.version)
		}
		inv := task.Invocation
		if got := inv.FeishuAppID == "cli_app" && inv.FeishuTenantToken == "t-tenant" && inv.FeishuUserToken == "u-owner"; got != tc.carried || warned == tc.carried {
			t.Fatalf("%s: invocation %+v warned %v", tc.version, inv, warned)
		}
		if public := domain.PublicRunnerTask(*task).Invocation; public.FeishuTenantToken != "" || public.FeishuUserToken != "" {
			t.Fatalf("%s: public task leaks Feishu tokens", tc.version)
		}
	}
}

// An Agent node whose responsible user has no Feishu identity logs that it
// runs with the bot only; scripts get no notice.
func TestAgentNodeLogsFeishuBotOnly(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutAgent(domain.AgentDefinition{ID: "feishu-agent", Name: "feishu-agent", ExecutorID: "mock"}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "feishu-wf", Name: "Feishu", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "agent", Name: "Agent", Type: "agent", AgentID: "feishu-agent", ExecutorID: "mock", MaxAttempts: 1}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 1))
	e.FeishuCredential = func(context.Context, int64) runtime.FeishuCredential {
		return runtime.FeishuCredential{AppID: "cli_app", TenantToken: "t-tenant", Notice: "飞书授权已过期"}
	}
	run, err := e.StartWithExecutionContext(wf.ID, "", domain.RepositoryContext{}, domain.TelemetryContext{ResponsibleUserID: 7})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "succeeded")
	page, err := s.QueryRunTelemetry(run.ID, "agent", "", 0, 0, false, false, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range page.Events {
		if event.Name == "feishu.bot_only" && strings.Contains(event.Message, "负责人飞书授权已过期") {
			return
		}
	}
	t.Fatalf("no bot-only notice in %d events", len(page.Events))
}
