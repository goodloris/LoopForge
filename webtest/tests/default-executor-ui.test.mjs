import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// With AGENTFLOW_DEFAULT_EXECUTOR=kiro (bootstrap defaultExecutor) the
// CodeBuddy-only setup disappears; with codebuddy (or an older server that
// omits it) the upstream UI is unchanged.
async function adminApp(t, defaultExecutor) {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.currentUser={...state.data.users[0],isGuest:false,isPlatformAdmin:true};state.data.defaultExecutor=${JSON.stringify(defaultExecutor)}`);
  app.router.get('/api/platform/admins', []);
  app.router.get('/api/platform/accounts', { signupEnabled: true, accounts: [{ userId: 1, loginName: 'admin', admin: true, password: true, codeBuddyApiKeyConfigured: false }] });
  app.router.get(/^\/api\/platform\/codebuddy-key/, { configured: false, models: [], catalog: [] });
  return app;
}

const sop = (executorId) => ({
  id: 'generic-frontend-delivery', name: '通用前端交付 SOP', generic: true,
  nodes: [{ id: 'build', name: '实现', type: 'agent', agentId: 'fe-build' }],
  agent: { id: 'fe-build', name: '实现', ...(executorId ? { executorId } : {}) },
});

test('kiro default: no shared CodeBuddy key tab, SOP needs Kiro login not a CodeBuddy key', async (t) => {
  const app = await adminApp(t, 'kiro');
  app.evaluate(`renderPlatformAdminPanel()`);
  await settle(8);
  const host = app.document.querySelector('#platformAdminPanel');
  assert.equal(host.querySelector('[data-platform-tab="sharedKey"]'), null);
  assert.equal(host.querySelector('[data-platform-panel="sharedKey"]'), null);
  assert.doesNotMatch(host.textContent, /CodeBuddy/);

  const w = sop('');
  app.evaluate(`state.data.agents=[${JSON.stringify(w.agent)}]`);
  const chips = app.evaluate(`sopRunRequirementChips(${JSON.stringify(w)})`);
  assert.doesNotMatch(chips, /CodeBuddy/);
  assert.match(chips, /Kiro CLI/);
  const rows = app.evaluate(`sopRunRequirements(${JSON.stringify(w)})`);
  assert.ok(rows.every((row) => row.ok), 'Kiro CLI needs no personal key');

  app.evaluate(`state.data.users=[{...state.currentUser,gitTokenConfigured:true}];state.currentUser=state.data.users[0];renderPersonalSettings()`);
  await settle(4);
  // 下一步 never points at CodeBuddy; with Git set up nothing else is required.
  const progress = app.evaluate(`credentialProgressHTML(state.currentUser)`);
  assert.doesNotMatch(progress, /CodeBuddy/);
  assert.match(progress, /推荐凭证已全部就绪/);
});

test('codebuddy default keeps the shared key tab and the CodeBuddy key dependency', async (t) => {
  const app = await adminApp(t, 'codebuddy');
  app.evaluate(`renderPlatformAdminPanel()`);
  await settle(8);
  assert.ok(app.document.querySelector('#platformAdminPanel [data-platform-tab="sharedKey"]'));
  const w = sop('');
  app.evaluate(`state.data.agents=[${JSON.stringify(w.agent)}];state.data.users=[{...state.currentUser,codeBuddyApiKeyConfigured:false}]`);
  assert.match(app.evaluate(`sopRunRequirementChips(${JSON.stringify(w)})`), /需要 CodeBuddy API Key/);
  assert.match(app.evaluate(`credentialProgressHTML({gitTokenConfigured:true})`), /data-credential-setup="codeBuddyApiKey"/);
});
