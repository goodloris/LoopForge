package runtime

import (
	"context"
	"errors"
	"slices"
	"testing"

	"agentflow/internal/domain"
)

func TestBindFeishuExportsTokensAndClearsInheritedOnes(t *testing.T) {
	workspace := t.TempDir()
	// A sandbox profile (or the host) carries its own lark-cli secret and a
	// stale user token; the platform's binding must replace both.
	command := WithCommandEnvironment(CommandSpec{Argv: []string{"true"}, CredentialBindings: []CredentialBinding{BindFeishu}},
		map[string]string{"LARKSUITE_CLI_APP_SECRET": "sandbox-secret", "LARKSUITE_CLI_USER_ACCESS_TOKEN": "someone-else"})
	bound, err := bindCommand(t.Context(), &localSandbox{workspace: workspace}, workspace, command, RunCredentials{Feishu: FeishuCredential{AppID: "cli_app", TenantToken: "t-tenant", UserToken: "u-user"}})
	if err != nil {
		t.Fatal(err)
	}
	env := bound.environment
	want := map[string]string{
		"LARKSUITE_CLI_APP_ID": "cli_app", "LARKSUITE_CLI_BRAND": "feishu", "LARKSUITE_CLI_APP_SECRET": "",
		"LARKSUITE_CLI_TENANT_ACCESS_TOKEN": "t-tenant", "LARKSUITE_CLI_USER_ACCESS_TOKEN": "u-user",
		"LARKSUITE_CLI_NO_UPDATE_NOTIFIER": "1", "LARKSUITE_CLI_NO_SKILLS_NOTIFIER": "1",
	}
	for key, value := range want {
		if env[key] != value {
			t.Fatalf("%s = %q want %q (env %v)", key, env[key], value, env)
		}
	}
	// Bot only: the user token is cleared, not inherited.
	bot, err := bindCommand(t.Context(), &localSandbox{workspace: workspace}, workspace, command, RunCredentials{Feishu: FeishuCredential{AppID: "cli_app", TenantToken: "t-tenant"}})
	if err != nil || bot.environment["LARKSUITE_CLI_USER_ACCESS_TOKEN"] != "" {
		t.Fatalf("bot-only user token = %q %v", bot.environment["LARKSUITE_CLI_USER_ACCESS_TOKEN"], err)
	}
	// No platform app: the binding leaves the command's own variables alone.
	plain, err := bindCommand(t.Context(), &localSandbox{workspace: workspace}, workspace, command, RunCredentials{})
	if err != nil || plain.environment["LARKSUITE_CLI_APP_SECRET"] != "sandbox-secret" || plain.environment["LARKSUITE_CLI_APP_ID"] != "" {
		t.Fatalf("unconfigured binding changed env: %v %v", plain.environment, err)
	}
	if got := FeishuEnvironment(FeishuCredential{}); len(got) != 0 {
		t.Fatalf("FeishuEnvironment without an app = %v", got)
	}
}

type captureSandbox struct {
	*localSandbox
	command *CommandSpec
}

func (c captureSandbox) Exec(_ context.Context, command CommandSpec, _ EventSink) (string, error) {
	*c.command = command
	return "", errors.New("captured")
}

// SOP Agent nodes run through ExecutorRunner.Run, which requests the Feishu
// binding for every agent CLI; the run sandbox supplies the responsible
// user's tokens.
func TestAgentNodeCommandGetsResponsibleUsersFeishu(t *testing.T) {
	workspace := t.TempDir()
	var got CommandSpec
	inner := captureSandbox{localSandbox: &localSandbox{id: "x", workspace: workspace}, command: &got}
	sb := WithSandboxCredentials(inner, RunCredentials{Codex: ProviderCredential{APIKey: "k"}, Feishu: FeishuCredential{AppID: "cli_app", TenantToken: "t-tenant", UserToken: "u-owner"}})
	_, _ = NewExecutorRunner().Run(context.Background(), sb, "invoke-1", domain.ExecutorDefinition{Kind: "codex", Binary: "codex"}, domain.AgentDefinition{ID: "probe", Name: "Probe"}, domain.AgentAssetBundle{}, "probe", InvocationOptions{RunID: "run-f"}, nil)
	if !slices.Contains(got.CredentialBindings, BindFeishu) || got.environment["LARKSUITE_CLI_USER_ACCESS_TOKEN"] != "u-owner" {
		t.Fatalf("agent command bindings %v env %v", got.CredentialBindings, got.environment)
	}
}
