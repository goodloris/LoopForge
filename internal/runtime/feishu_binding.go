package runtime

import "strings"

// feishuEnvironmentNames are every lark-cli credential variable. With a
// platform Feishu app all of them are set, so a child never inherits the host
// process's or a sandbox's own app secret or another member's user token;
// lark-cli treats an empty variable as unset.
var feishuEnvironmentNames = []string{
	"LARKSUITE_CLI_APP_ID", "LARKSUITE_CLI_APP_SECRET", "LARKSUITE_CLI_BRAND",
	"LARKSUITE_CLI_TENANT_ACCESS_TOKEN", "LARKSUITE_CLI_USER_ACCESS_TOKEN",
}

// bindFeishuEnvironment gives lark-cli the bot identity (app id + tenant
// token, never the secret) and, when present, the user's access token, which
// lark-cli then uses by default. Without a platform app it leaves env alone.
func bindFeishuEnvironment(env map[string]string, credential FeishuCredential) {
	if strings.TrimSpace(credential.AppID) == "" {
		return
	}
	for _, name := range feishuEnvironmentNames {
		env[name] = ""
	}
	env["LARKSUITE_CLI_APP_ID"] = strings.TrimSpace(credential.AppID)
	env["LARKSUITE_CLI_BRAND"] = "feishu"
	env["LARKSUITE_CLI_TENANT_ACCESS_TOKEN"] = strings.TrimSpace(credential.TenantToken)
	env["LARKSUITE_CLI_USER_ACCESS_TOKEN"] = strings.TrimSpace(credential.UserToken)
	env["LARKSUITE_CLI_NO_UPDATE_NOTIFIER"] = "1"
	env["LARKSUITE_CLI_NO_SKILLS_NOTIFIER"] = "1"
}

// FeishuEnvironment is bindFeishuEnvironment for callers that build a process
// environment themselves (the execution machine's SOP task runner).
func FeishuEnvironment(credential FeishuCredential) map[string]string {
	env := map[string]string{}
	bindFeishuEnvironment(env, credential)
	return env
}
