package httpapi

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/notification"
	"agentflow/internal/repo"
)

// kiroTrust returns the --trust-tools value of a recorded kiro-cli argv.
func kiroTrust(t *testing.T, argv []string) string {
	t.Helper()
	for _, arg := range argv {
		if value, ok := strings.CutPrefix(arg, "--trust-tools="); ok {
			return value
		}
	}
	t.Fatalf("no --trust-tools in %q", argv)
	return ""
}

// A Bot on a Kiro CLI model replies for an account without a CodeBuddy key;
// Kiro runs MCP-only (no built-in file tools on the server).
func TestBotReplyRunsOnKiroCLI(t *testing.T) {
	defer func(previous func() bool) { kiroCLIInstalled = previous }(kiroCLIInstalled)
	s, h, bot := botFixture(t)
	s.UpdateUserCredentials(43, repo.UserCredentialUpdate{CodeBuddyAPIKeyClear: true})
	bot.Model = "kiro:auto"
	item := domain.BotInteraction{SenderID: "new-wecom-user", Content: "现在有什么进展"}

	kiroCLIInstalled = func() bool { return false }
	if reply, err := h.generateBotReply(context.Background(), bot, domain.BotConversation{ChatType: "single"}, nil, item); err != nil || !strings.Contains(reply, "kiro-cli") {
		t.Fatalf("missing kiro-cli must be explained: %q %v", reply, err)
	}

	kiroCLIInstalled = func() bool { return true }
	argvFile := fakeKiroCLI(t, "没有提交回复")
	// Since upstream v0.4.1 a plain-text answer is a valid Bot reply.
	reply, err := h.generateBotReply(context.Background(), bot, domain.BotConversation{ChatType: "single"}, nil, item)
	if err != nil || reply != "没有提交回复" {
		t.Fatalf("plain Kiro answer = %q %v", reply, err)
	}
	argv := readFakeArgv(t, argvFile)
	if trust := kiroTrust(t, argv); trust == "" || strings.Contains(trust, "read") || strings.Contains(trust, "grep") || !strings.HasPrefix(trust, "@") {
		t.Fatalf("Bot reply must trust only MCP servers: %q", trust)
	}
	if !slices.Contains(argv, "auto") {
		t.Fatalf("model not passed: %q", argv)
	}
}

// An SOP completion report on a Kiro CLI model needs no CodeBuddy key and
// runs MCP-only on Kiro CLI.
func TestSOPReportRunsOnKiroCLI(t *testing.T) {
	defer func(previous func() bool) { kiroCLIInstalled = previous }(kiroCLIInstalled)
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	store.PutUser(domain.User{ID: 42, LoginName: "executor"})
	h := &assistantHandlers{deps: &deps{repo: store}}
	run := domain.Run{ID: "run-kiro-report", ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 42, ProjectID: "p"}}

	if _, err := h.generateSOPReport(context.Background(), run, domain.DefaultNotificationReportModel, "资料"); !errors.Is(err, notification.ErrReportCredentials) {
		t.Fatalf("CodeBuddy report without key = %v", err)
	}
	kiroCLIInstalled = func() bool { return false }
	if _, err := h.generateSOPReport(context.Background(), run, "kiro:auto", "资料"); !errors.Is(err, notification.ErrReportKiro) {
		t.Fatalf("Kiro report without kiro-cli = %v", err)
	}
	kiroCLIInstalled = func() bool { return true }
	argvFile := fakeKiroCLI(t, "普通文本不是报告")
	if _, err := h.generateSOPReport(context.Background(), run, "kiro:auto", "资料"); !errors.Is(err, notification.ErrReportFormat) {
		t.Fatalf("fake CLI never submits: %v", err)
	}
	if trust := kiroTrust(t, readFakeArgv(t, argvFile)); trust == "" || strings.Contains(trust, "read") || !strings.HasPrefix(trust, "@") {
		t.Fatalf("report must trust only MCP servers: %q", trust)
	}
}
