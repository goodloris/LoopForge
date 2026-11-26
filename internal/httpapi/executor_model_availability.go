package httpapi

import (
	"context"
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

// Verification states of the Codex / Claude Code role catalogs.
const (
	// executorModelsVerified: the list comes from the user's saved catalog or
	// a Base URL probe, so only those models are offered.
	executorModelsVerified = "verified"
	// executorModelsUnverified: the user has a credential (official-endpoint
	// key or a runner account login) but no list the server can check.
	executorModelsUnverified = "unverified"
	// executorModelsNone: no credential and no runner login at all.
	executorModelsNone = "none"
)

// availabilityEngine maps an executor catalog key to the engine whose
// availability is resolved. Only Codex and Claude Code are filtered.
func availabilityEngine(executorID string) (string, bool) {
	switch strings.TrimSpace(executorID) {
	case "codex":
		return assistantEngineCodex, true
	case "claude", assistantEngineClaude:
		return assistantEngineClaude, true
	}
	return "", false
}

type executorModelAvailability struct {
	State  string
	Models []assistantDiscoveredModel // only set when State is verified
}

// resolveExecutorModelAvailability is shared by the role catalog and the save
// check so both use the same rule. Order: saved catalog / Base URL probe >
// personal key (official endpoint) > one of the user's runners that reports
// the CLI and runs it on local account login. Runner status is not required:
// offline is temporary and the run reports it.
func resolveExecutorModelAvailability(ctx context.Context, user domain.User, runners interface{ Runners() []domain.Runner }, engine string) executorModelAvailability {
	if models, _ := assistantDiscoveredModelsFor(ctx, user, engine); len(models) > 0 {
		return executorModelAvailability{State: executorModelsVerified, Models: models}
	}
	if key, _ := assistantEngineCredential(user, engine); key != "" {
		return executorModelAvailability{State: executorModelsUnverified}
	}
	if user.ID != 0 && runners != nil {
		for _, runner := range runners.Runners() {
			if runner.OwnerUserID == user.ID && runner.SupportsProvider(engine) && runnerUsesLocalLogin(runner, engine) {
				return executorModelAvailability{State: executorModelsUnverified}
			}
		}
	}
	return executorModelAvailability{State: executorModelsNone}
}

// executorModelNotice is the picker hint for a state; it never names a key
// or Base URL.
func executorModelNotice(state, engine string) string {
	label := assistantEngineLabel(engine)
	switch state {
	case executorModelsUnverified:
		return fmt.Sprintf("未能验证这些 %s 模型在你的账号下可用（官方端点或执行机器账号登录）；运行时可能失败", label)
	case executorModelsNone:
		return fmt.Sprintf("未配置 %s 凭据：请在个人配置中填写 API Key（可同时保存可用模型），或在执行机器上启用账号登录", label)
	}
	return ""
}

func availabilityHasModel(models []assistantDiscoveredModel, id string) bool {
	for _, model := range models {
		if model.ID == id {
			return true
		}
	}
	return false
}

// checkAgentModelAvailability validates a Codex / Claude Code role model
// against the saver's availability. A hard error is returned only when the
// saver has a verified list that lacks the model and the request did not mark
// it as a manually entered ID; otherwise the save goes through, possibly with
// warnings. An unchanged executor + model on an existing role always passes,
// so stored SOPs keep working.
func checkAgentModelAvailability(ctx context.Context, user domain.User, runners interface{ Runners() []domain.Runner }, agent domain.AgentDefinition, previous *domain.AgentDefinition, customModel bool) ([]string, error) {
	engine, ok := availabilityEngine(agent.ExecutorID)
	model := strings.TrimSpace(agent.Model)
	if !ok || model == "" || model == assistantDefaultModel {
		return nil, nil
	}
	if previous != nil && strings.TrimSpace(previous.ExecutorID) == strings.TrimSpace(agent.ExecutorID) && strings.TrimSpace(previous.Model) == model {
		return nil, nil
	}
	label := assistantEngineLabel(engine)
	if !assistantValidEngineModel(model) {
		return nil, fmt.Errorf("模型 ID 无效：%s 模型 ID 不能为空、超过 128 个字符、以 - 开头或包含空白、引号、反斜杠", label)
	}
	availability := resolveExecutorModelAvailability(ctx, user, runners, engine)
	switch availability.State {
	case executorModelsVerified:
		if availabilityHasModel(availability.Models, model) {
			return nil, nil
		}
		if !customModel {
			return nil, fmt.Errorf("%s 模型 %s 不在当前可用模型内，请从列表选择，或确认后以手动输入方式保存", label, model)
		}
		return []string{fmt.Sprintf("手动输入的 %s 模型 %s 不在你的可用清单内，运行时可能失败", label, model)}, nil
	default:
		return []string{fmt.Sprintf("未能验证 %s 模型 %s 是否可用，运行时可能失败；运行时使用 SOP 责任人的凭据", label, model)}, nil
	}
}
