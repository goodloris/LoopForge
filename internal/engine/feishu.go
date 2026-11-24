package engine

import "agentflow/internal/runtime"

// feishuBotOnlyNotice is the run-log line for an Agent node that gets only
// the Feishu app (bot) identity because the responsible user has none: not
// connected, authorization expired, or Feishu was unreachable. Empty when the
// platform has no Feishu app or the user identity is present.
func feishuBotOnlyNotice(credential runtime.FeishuCredential) string {
	if credential.AppID == "" || credential.UserToken != "" {
		return ""
	}
	reason := credential.Notice
	if reason == "" {
		reason = "未连接飞书"
	}
	return "负责人" + reason + "：本节点的 lark-cli 只有飞书应用（bot）身份。负责人可在 个人配置 → 飞书 连接或重新授权。"
}
