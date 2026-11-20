// CLIs that AI 对话 can run on. The list comes from GET /api/assistant/models
// (`engines`); models without `engine` run on CodeBuddy.
export const defaultEngine = "codebuddy";

// Bundled logo per CLI; an unknown CLI falls back to its model's brand.
const brands = {
  codebuddy: "codebuddy", codex: "codex", "claude-code": "claude",
  opencode: "opencode", cursor: "cursor", agy: "gemini", kimi: "kimi", pi: "pi", dsh: "deepseek", zcode: "zhipu",
};
export const engineBrand = (id) => brands[id] || "";

// Display names, mirroring assistantEngineLabel on the server.
const labels = {
  codebuddy: "CodeBuddy", codex: "Codex", "claude-code": "Claude Code", opencode: "OpenCode", cursor: "Cursor",
  agy: "Antigravity", kimi: "Kimi Code", pi: "Pi", dsh: "DeepSeek Harness", zcode: "ZCode", kiro: "Kiro CLI",
};
export const engineLabel = (id) => labels[id] || id || "";

export const modelEngine = (model) => model?.engine || defaultEngine;

/** The CLI an ID runs on: the catalog entry, else the "cli:model" prefix. */
export function engineOfModel(id, models = []) {
  const listed = models.find((m) => m.id === id);
  if (listed) return modelEngine(listed);
  const prefix = /^([a-z][a-z0-9-]*):./.exec(id || "")?.[1];
  return prefix || defaultEngine;
}

export const engineModels = (models, engine) =>
  (models || []).filter((m) => modelEngine(m) === engine);

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

// CLIs a session Executor can name when the reply was saved before model prefixes.
const legacyEngines = new Set(["codex", "claude-code", "opencode", "cursor", "agy", "kimi", "pi", "dsh", "zcode", "kiro"]);

function modelHasEnginePrefix(id) {
  const prefix = /^([a-z][a-z0-9-]*):./.exec(id || "")?.[1];
  return prefix === defaultEngine || legacyEngines.has(prefix);
}

function legacyEngine(id) {
  if (id === "claude") return "claude-code";
  return legacyEngines.has(id) ? id : "";
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
    if (modelHasEnginePrefix(m.model)) return engineOfModel(m.model, models);
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
