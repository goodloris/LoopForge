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
