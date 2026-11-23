package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// Feishu reasons a CLI process has only the bot identity, shown to the agent
// (conversation) or in the run log (SOP, prefixed with 负责人).
const (
	feishuNoticeNotConnected = "未连接飞书"
	feishuNoticeExpired      = "飞书授权已过期"
	feishuNoticeUnavailable  = "飞书用户身份暂时不可用"
)

// feishuRunCredential returns the lark-cli tokens for one CLI process run on
// behalf of userID: the app's tenant token (bot identity) always, and the
// user's access token when they connected Feishu, refreshed when fewer than
// feishuRoundMinValid are left. Without a platform Feishu app it returns the
// zero value, so nothing is injected. Never returns the app secret or a
// refresh token.
func (d *deps) feishuRunCredential(ctx context.Context, userID int64) runtime.FeishuCredential {
	cfg := feishuConfigFromEnv()
	if !cfg.enabled() {
		return runtime.FeishuCredential{}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*feishuHTTPTimeout)
	defer cancel()
	out := runtime.FeishuCredential{AppID: cfg.AppID}
	tenant, err := newFeishuClient(cfg).tenantAccessToken(ctx)
	if err != nil {
		log.Printf("[feishu] tenant token failed: %v", err)
	}
	out.TenantToken = tenant
	if userID <= 0 || d.repo == nil {
		out.Notice = feishuNoticeNotConnected
		return out
	}
	access, err := d.feishuUserAccess(ctx, cfg, userID)
	switch {
	case err == nil:
		out.UserToken, out.UserName, out.Preset = access.Token, access.Name, access.Preset
	case errors.Is(err, repo.ErrFeishuNotConnected):
		out.Notice = feishuNoticeNotConnected
		if grant, ok := d.repo.FeishuGrant(userID); ok && grant.Status == repo.FeishuGrantExpired {
			out.Notice = feishuNoticeExpired
		}
	default:
		log.Printf("[feishu] user token for user %d failed: %v", userID, err)
		out.Notice = feishuNoticeUnavailable
	}
	return out
}

// bindWorkspaceFeishu gives the workspace CLI this turn's speaker's Feishu
// identity, like bindWorkspaceGit: each turn starts the CLI again, so in a
// shared conversation lark-cli acts as whoever sent the current message, and
// a speaker without a connection gets the bot identity only. Without a
// platform Feishu app nothing changes.
func (h *assistantHandlers) bindWorkspaceFeishu(ctx context.Context, s *AgentToolSession, command runtime.CommandSpec, credentials runtime.RunCredentials) (runtime.CommandSpec, runtime.RunCredentials) {
	credentials.Feishu = runtime.FeishuCredential{}
	if !feishuConfigFromEnv().enabled() {
		return command, credentials
	}
	if s.ExecutionRunnerID != "" {
		// An older execution machine rejects an unknown binding outright.
		if runner, ok := h.repo.Runner(s.ExecutionRunnerID); !ok || !runner.SupportsFeishu() {
			assistantDiagnosticsFrom(ctx).platform("feishu.unavailable", fmt.Sprintf("执行机器需升级至 %s 才能在工作区使用飞书身份", domain.RunnerVersionFeishu), s.User)
			return command, credentials
		}
	}
	credentials.Feishu = h.feishuRunCredential(ctx, s.User.ID)
	if credentials.Feishu.Notice != "" && credentials.Feishu.Notice != feishuNoticeNotConnected {
		assistantDiagnosticsFrom(ctx).platform("feishu.bot_only", credentials.Feishu.Notice+"，本轮只有飞书应用（bot）身份", s.User)
	}
	if !slices.Contains(command.CredentialBindings, runtime.BindFeishu) {
		command.CredentialBindings = append(append([]runtime.CredentialBinding(nil), command.CredentialBindings...), runtime.BindFeishu)
	}
	return command, credentials
}

// feishuWorkspaceNote tells the workspace agent which Feishu identity this
// turn's lark-cli has (design §7). Empty without a platform Feishu app.
func (d *deps) feishuWorkspaceNote(userID int64) string {
	if !feishuConfigFromEnv().enabled() || d.repo == nil {
		return ""
	}
	grant, ok := d.repo.FeishuGrant(userID)
	if userID > 0 && ok && grant.Status == repo.FeishuGrantActive {
		level := "只读"
		if grant.Preset == "readwrite" {
			level = "读写"
		}
		return fmt.Sprintf("飞书：本轮 lark-cli 已配置，bot 身份可用；用户身份为 %s（权限：%s），不带 --as 时默认用用户身份。不要运行 lark-cli config init 或 auth login。\n", grant.Name, level)
	}
	return "飞书：本轮 lark-cli 只有 bot 身份。需要用户身份时，请让用户到 个人配置 → 飞书 连接或重新授权，不要尝试 lark-cli auth login、config init 或其他授权方式。\n"
}
