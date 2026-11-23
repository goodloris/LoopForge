// CLIs that AI 对话 can run on. The list comes from GET /api/assistant/models
// (`engines`); models without `engine` run on CodeBuddy.
export const defaultEngine = "codebuddy";

// Main Bot can run on any configured agent CLI.
export const EXECUTORS = [
  { id: "codebuddy", icon: "sparkles", label: "CodeBuddy", detail: "使用 CodeBuddy 模型与个人 API Key。" },
  { id: "codex", icon: "terminal", label: "Codex", detail: "使用 Codex CLI 与个人 Codex API Key。" },
  { id: "claude-code", icon: "brain", label: "Claude Code", detail: "使用 Claude Code CLI 与个人 Claude Code API Key。" },
  { id: "opencode", icon: "terminal", label: "OpenCode", detail: "使用 OpenCode 与个人 API Key。" },
  { id: "cursor", icon: "terminal", label: "Cursor", detail: "使用 Cursor CLI 与个人 API Key。" },
  { id: "agy", icon: "sparkles", label: "Antigravity", detail: "使用 Antigravity 与个人 API Key。" },
  { id: "kimi", icon: "sparkles", label: "Kimi Code", detail: "使用 Kimi Code 与个人 API Key。" },
  { id: "pi", icon: "terminal", label: "Pi", detail: "使用 Pi 与个人 API Key。" },
  { id: "dsh", icon: "terminal", label: "DeepSeek Harness", detail: "使用 DeepSeek Harness 与个人 API Key。" },
  { id: "zcode", icon: "terminal", label: "ZCode", detail: "使用 ZCode 与个人 API Key。" },
  { id: "kiro", icon: "terminal", label: "Kiro CLI", detail: "使用 kiro-cli login 的订阅登录，无需 API Key。" },
];

// Bundled logo per CLI; an unknown CLI falls back to its model's brand.
const brands = {
  codebuddy: "codebuddy", codex: "codex", "claude-code": "claude",
  opencode: "opencode", cursor: "cursor", agy: "antigravity", kimi: "kimi-code", pi: "pi", dsh: "deepseek", zcode: "zhipu",
};
export const engineBrand = (id) => Object.hasOwn(brands, id) ? brands[id] : "";

export function personalEngineConfigured(user, engine) {
  switch (engine) {
    case "codebuddy": return Boolean(user?.codeBuddyApiKeyConfigured);
    case "codex": return Boolean(user?.codexApiKeyConfigured);
    case "claude-code": return Boolean(user?.claudeCodeApiKeyConfigured);
    default: return Boolean(user?.agentCliCredentials?.[engine]?.apiKeyConfigured);
  }
}

// Display names, mirroring assistantEngineLabel on the server.
const labels = {
  codebuddy: "CodeBuddy", codex: "Codex", "claude-code": "Claude Code", opencode: "OpenCode", cursor: "Cursor",
  agy: "Antigravity", kimi: "Kimi Code", pi: "Pi", dsh: "DeepSeek Harness", zcode: "ZCode", kiro: "Kiro CLI",
};
export const engineLabel = (id) => labels[id] || id || "";

export const modelEngine = (model) => model?.engine || defaultEngine;

const modelEnginePrefix = (id) => /^([a-z][a-z0-9-]*):./.exec(id || "")?.[1];

/** The CLI an ID runs on: the catalog entry, else the "cli:model" prefix. */
export function engineOfModel(id, models = []) {
  const listed = models.find((m) => m.id === id);
  if (listed) return modelEngine(listed);
  return modelEnginePrefix(id) || defaultEngine;
}

export const concreteModel = (model, engine = modelEngine(model)) =>
  !!model?.id && model.id !== "default" && model.id !== `${engine}:default` &&
  (engine !== "codebuddy" || (model.id !== "default-model" && model.id !== "codebuddy:default-model"));

export const engineModels = (models, engine) =>
  (models || []).filter((m) => modelEngine(m) === engine && concreteModel(m, engine));

/**
 * The CLI a new conversation starts on when this browser has no pick: the
 * one the user's latest own conversation ran on, else the first CLI the
 * user has configured, else CodeBuddy. A deployment with only Kiro CLI
 * therefore never defaults to an unconfigured CodeBuddy.
 */
export function defaultExecutor(engines = [], sessions = [], userId = 0, models = [], serverDefault = defaultEngine) {
  // serverDefault is /api/assistant/models' defaultExecutor (the deployment's
  // AGENTFLOW_DEFAULT_EXECUTOR); older servers omit it, meaning CodeBuddy.
  const fallback = serverDefault || defaultEngine;
  const configured = (engines || []).filter((engine) => engine.configured).map((engine) => engine.id);
  const usable = (id) => id && (!engines?.length || configured.includes(id));
  const latest = [...(sessions || [])]
    .filter((session) => !userId || !session.userId || session.userId === userId)
    .sort((a, b) => String(b.updatedAt || "").localeCompare(String(a.updatedAt || "")))
    .map((session) => legacyEngine(session.executor) || (session.model ? engineOfModel(session.model, models) : ""))
    .find(usable);
  return latest || (usable(fallback) ? fallback : "") || configured[0] || fallback;
}

// Mirrors assistantEngineMissingKey in internal/httpapi/assistant_engines.go;
// credentialHint turns it into a "去配置" button.
const CODEBUDDY_MISSING_KEY = "请先在个人配置中配置自己的 CodeBuddy API Key";
export const KIRO_UNAVAILABLE = "Kiro CLI 不可用：请在平台或默认执行机器上安装 kiro-cli 并运行 kiro-cli login（订阅登录）";
export function engineMissingKeyMessage(engine) {
  // CodeBuddy itself, not whichever CLI the deployment defaults to.
  if (engine?.id === "codebuddy") return CODEBUDDY_MISSING_KEY;
  // Kiro CLI has no API key: it needs a kiro-cli login where the turn runs.
  if (engine?.id === "kiro") return KIRO_UNAVAILABLE;
  return `请先在个人配置中配置 ${engine.name} API Key 后再使用 ${engine.name} 模型`;
}

const executorLabels = new Map(EXECUTORS.map(({ id, label }) => [id, label]));

function legacyEngine(id) {
  id = String(id ?? "").trim().toLowerCase();
  if (id === "claude") return "claude-code";
  return id !== defaultEngine && executorLabels.has(id) ? id : "";
}

/** CLI label for a saved reply, including messages from before model prefixes. */
export function replyEngineLabel(model, legacyExecutor, speakerAgentId) {
  const label = executorLabels.get(modelEnginePrefix(model));
  if (label) return label;
  if (!model || speakerAgentId) return "";
  return executorLabels.get(legacyEngine(legacyExecutor) || defaultEngine);
}

/**
 * The CLI that wrote a conversation's latest own reply, "" before the first.
 * Mirrors assistantConversationEngine: a conversation stays on that CLI,
 * because its native session holds tool calls another CLI cannot resume.
 * An unprefixed model falls back to the session's stored executor.
 */
export function conversationEngine(messages, models = [], legacyExecutor = "") {
  for (let i = (messages?.length || 0) - 1; i >= 0; i--) {
    const m = messages[i];
    if (m?.role !== "assistant" || m.speakerAgentId || !m.model) continue;
    if (executorLabels.has(modelEnginePrefix(m.model))) return engineOfModel(m.model, models);
    const legacy = legacyEngine(legacyExecutor);
    if (legacy) return legacy;
    return engineOfModel(m.model, models);
  }
  return "";
}

export function engineDetail(engine, models) {
  if (!engine.configured && engine.id === "kiro") return "Kiro CLI 未就绪：请在平台或执行机器上安装 kiro-cli 并运行 kiro-cli login";
  if (!engine.configured) return `未配置 ${engine.name} API Key，选择后去个人配置填写`;
  const count = engineModels(models, engine.id).length;
  return count ? `${count} 个可用模型` : "暂无可用模型";
}
