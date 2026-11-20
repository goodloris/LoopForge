import { test } from "vitest";
import assert from "node:assert/strict";
import { conversationEngine } from "../src/engines.js";

const reply = [{ role: "user", content: "hi" }, { role: "assistant", model: "gpt-5.4", content: "ok" }];

test("an unprefixed reply stays on the session executor saved before model prefixes", () => {
  assert.equal(conversationEngine(reply, [], "codex"), "codex");
  assert.equal(conversationEngine(reply, [], "claude"), "claude-code");
  assert.equal(conversationEngine(reply, [], "agy"), "agy");
  assert.equal(conversationEngine(reply, [], "codebuddy"), "codebuddy");
  assert.equal(conversationEngine(reply), "codebuddy");
  assert.equal(conversationEngine([{ role: "assistant", model: "codex:gpt-5.4", content: "ok" }], [], "codebuddy"), "codex");
  assert.equal(conversationEngine([{ role: "assistant", model: "gpt-5.4", speakerAgentId: "role", content: "ok" }], [], "codex"), "");
});

test("a browser without a pick starts on the user's last CLI, never an unconfigured CodeBuddy", async () => {
  const { defaultExecutor } = await import("../src/engines.js");
  const kiroOnly = [{ id: "codebuddy", configured: false }, { id: "kiro", configured: true }];
  assert.equal(defaultExecutor(kiroOnly, []), "kiro");
  const sessions = [
    { id: "old", userId: 1, executor: "codex", updatedAt: "2026-11-01T00:00:00Z" },
    { id: "new", userId: 1, executor: "kiro", model: "kiro:claude-sonnet-4.5", updatedAt: "2026-11-18T00:00:00Z" },
    { id: "peer", userId: 2, executor: "zcode", updatedAt: "2026-11-19T00:00:00Z" },
  ];
  const all = [{ id: "codebuddy", configured: true }, { id: "codex", configured: true }, { id: "kiro", configured: true }, { id: "zcode", configured: true }];
  assert.equal(defaultExecutor(all, sessions, 1), "kiro");
  // The last CLI is no longer configured: fall back to a configured one.
  assert.equal(defaultExecutor([{ id: "codebuddy", configured: false }, { id: "codex", configured: true }], sessions, 1), "codex");
  assert.equal(defaultExecutor(undefined, undefined), "codebuddy");
});

test("the server's default CLI preselects new conversations; codebuddy or none changes nothing", async () => {
  const { defaultExecutor } = await import("../src/engines.js");
  const all = [{ id: "codebuddy", configured: true }, { id: "codex", configured: true }, { id: "kiro", configured: true }];
  assert.equal(defaultExecutor(all, [], 1, [], "kiro"), "kiro");
  // An unusable server default yields to the first configured CLI.
  const noKiro = [{ id: "codebuddy", configured: false }, { id: "codex", configured: true }, { id: "kiro", configured: false }];
  assert.equal(defaultExecutor(noKiro, [], 1, [], "kiro"), "codex");
  // The user's last CLI still wins over the server default.
  const sessions = [{ id: "s", userId: 1, executor: "codex", updatedAt: "2026-11-18T00:00:00Z" }];
  assert.equal(defaultExecutor(all, sessions, 1, [], "kiro"), "codex");
  assert.equal(defaultExecutor(undefined, undefined, 0, [], "kiro"), "kiro");
  // Same answers as before for a CodeBuddy default or an older server.
  const inputs = [
    [[{ id: "codebuddy", configured: false }, { id: "kiro", configured: true }], []],
    [all, sessions, 1],
    [[{ id: "codebuddy", configured: false }, { id: "codex", configured: true }], sessions, 1],
    [all, [], 1],
    [undefined, undefined],
    [[], []],
  ];
  for (const [engines, rows, userId] of inputs) {
    const before = defaultExecutor(engines, rows, userId, []);
    assert.equal(defaultExecutor(engines, rows, userId, [], "codebuddy"), before);
    assert.equal(defaultExecutor(engines, rows, userId, [], undefined), before);
    assert.equal(defaultExecutor(engines, rows, userId, [], ""), before);
  }
});

test("a missing credential names the CLI the turn asked for", async () => {
  const { engineMissingKeyMessage, engineDetail, KIRO_UNAVAILABLE } = await import("../src/engines.js");
  assert.equal(engineMissingKeyMessage({ id: "codebuddy", name: "CodeBuddy" }), "请先在个人配置中配置自己的 CodeBuddy API Key");
  // Mirrors assistantEngineMissingKey(kiro) in internal/httpapi/assistant_engines.go.
  assert.equal(engineMissingKeyMessage({ id: "kiro", name: "Kiro CLI" }), "Kiro CLI 不可用：请在平台或默认执行机器上安装 kiro-cli 并运行 kiro-cli login（订阅登录）");
  assert.equal(KIRO_UNAVAILABLE, engineMissingKeyMessage({ id: "kiro", name: "Kiro CLI" }));
  assert.equal(engineMissingKeyMessage({ id: "codex", name: "Codex" }), "请先在个人配置中配置 Codex API Key 后再使用 Codex 模型");
  for (const id of ["kiro", "codex"]) assert.doesNotMatch(engineMissingKeyMessage({ id, name: id }), /CodeBuddy/);
  assert.doesNotMatch(engineDetail({ id: "kiro", name: "Kiro CLI", configured: false }, []), /API Key/);
  assert.match(engineDetail({ id: "kiro", name: "Kiro CLI", configured: false }, []), /kiro-cli login/);
  assert.equal(engineDetail({ id: "codex", name: "Codex", configured: false }, []), "未配置 Codex API Key，选择后去个人配置填写");
});
