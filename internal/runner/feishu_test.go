package runner

import (
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestRunnerTaskExportsFeishuTokens(t *testing.T) {
	inv := &domain.RunnerInvocation{CredentialSource: domain.RuntimeAuthLocal, FeishuAppID: "cli_app", FeishuTenantToken: "t-tenant", FeishuUserToken: "u-owner"}
	_, env, err := bindInvocationCredentials(inv, []runtime.CredentialBinding{runtime.BindFeishu}, []string{"kiro-cli"})
	if err != nil {
		t.Fatal(err)
	}
	if env["LARKSUITE_CLI_APP_ID"] != "cli_app" || env["LARKSUITE_CLI_TENANT_ACCESS_TOKEN"] != "t-tenant" || env["LARKSUITE_CLI_USER_ACCESS_TOKEN"] != "u-owner" || env["LARKSUITE_CLI_APP_SECRET"] != "" {
		t.Fatalf("env = %v", env)
	}
	if _, env, _ := bindInvocationCredentials(&domain.RunnerInvocation{}, nil, []string{"kiro-cli"}); len(env) != 0 {
		t.Fatalf("no Feishu app exported %v", env)
	}
}
