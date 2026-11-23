import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// 个人配置 lists Kiro CLI as a machine login (no key). Its state comes from the
// model catalog: once this account can run kiro-cli it reads 已登录 and can be
// saved as the personal default executor, even with no API key at all.
function kiroCatalog(configured) {
  return {
    default: 'default',
    models: configured ? [{ id: 'kiro:auto', name: 'Auto', engine: 'kiro' }, { id: 'kiro:claude-sonnet-5.5', name: 'Sonnet', engine: 'kiro' }] : [],
    engines: [
      { id: 'codebuddy', name: 'CodeBuddy', configured: false, personal: false, credential: 'codeBuddyApiKey' },
      { id: 'kiro', name: 'Kiro CLI', configured, personal: configured, credential: 'agentCli.kiro' },
    ],
  };
}

async function settingsApp(t, configured) {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.data.users[0].id=1;state.currentUser={...state.data.users[0],id:1,isGuest:false,codeBuddyApiKeyConfigured:false,gitHubCredentialConfigured:false};state.data.users[0]=state.currentUser`);
  app.router.get('/api/assistant/models', kiroCatalog(configured));
  app.router.get('/api/users/1/git-http-credentials', []);
  return app;
}

test('a Kiro login without any key is listed and can become the personal default', async (t) => {
  const app = await settingsApp(t, true);
  app.router.put('/api/users/1/credentials', (call) => ({ id: 1, name: 'Test User', assistantDefaults: call.body.assistantDefaults }));
  await app.evaluate(`renderPersonalSettings()`);
  await settle(10);
  const panel = app.document.querySelector('#credentialPanel');
  const row = panel.querySelector('[data-kiro-login-row]');
  assert.ok(row, 'Kiro CLI row is shown');
  assert.match(row.textContent, /Kiro CLI · 本机登录/);
  assert.match(row.textContent, /已登录/);
  const form = panel.querySelector('[data-ai-defaults-form]');
  assert.ok(form, 'AI 默认设置 appears for a Kiro-only account');
  assert.deepEqual([...form.elements.executor.options].map(o => o.value), ['kiro']);
  assert.equal(form.elements.model.value, 'kiro:auto');
  // Pick another Kiro model and save.
  app.evaluate(`(()=>{const f=document.querySelector('[data-ai-defaults-form]');const sel=f.elements.model;[...sel.options].find(o=>o.value==='kiro:claude-sonnet-5.5').selected=true;f.dataset.initial='{}'})()`);
  await app.evaluate(`saveCredentialDefaults(document.querySelector('[data-ai-defaults-form]'),document.querySelector('[data-credential-defaults]'))`);
  const put = app.router.calls.filter(c => c.method === 'PUT' && c.path === '/api/users/1/credentials').at(-1);
  assert.equal(put.body.assistantDefaults.executor, 'kiro');
  assert.equal(put.body.assistantDefaults.model, 'claude-sonnet-5.5');
  await settle(4);
  assert.match(panel.querySelector('[data-kiro-login-row]').textContent, /默认/);
});

test('without a Kiro login the row says 未登录 and offers no defaults', async (t) => {
  const app = await settingsApp(t, false);
  await app.evaluate(`renderPersonalSettings()`);
  await settle(10);
  const panel = app.document.querySelector('#credentialPanel');
  const row = panel.querySelector('[data-kiro-login-row]');
  assert.match(row.textContent, /未登录/);
  assert.ok(row.querySelector('a[href="#/runners"]'), 'points at execution machines');
  assert.equal(panel.querySelector('[data-ai-defaults-form]'), null);
});

// Built-in SOP cards show the executor the server resolves for the roles
// (the same /executor-requirements result the 使用 dialog shows), fetched once.
test('built-in SOP cards show the server executor requirement, shared with the 使用 dialog', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const template = {
    id: 'generic-kiro-demo', name: '通用演示 SOP', generic: true, version: 1,
    nodes: [{ id: 'build', name: '实现', type: 'agent', agentId: 'generic-kiro-demo-build' }],
  };
  app.router.get(/^\/api\/workflows\/generic-kiro-demo\/executor-requirements/, [{ kind: 'kiro', name: 'Kiro CLI', configured: true }]);
  app.router.get('/api/workflows', [template]);
  app.evaluate(`switchView('sops')`);
  await settle(10);
  app.evaluate(`state.data.workflows=[${JSON.stringify(template)}];state.sopScope='generic';renderSOPCatalog()`);
  await settle(10);
  await new Promise(resolve => setTimeout(resolve, 80));
  await settle(4);
  const card = app.document.querySelector('[data-generic-id="generic-kiro-demo"]');
  assert.ok(card);
  assert.match(card.querySelector('.sop-requirements')?.textContent || '', /Kiro CLI 本机登录/);
  assert.ok(card.querySelector('.sop-badge.tone-success'), 'configured shows ✓');
  await app.evaluate(`openGenericTemplateModal('generic-kiro-demo')`);
  await settle(6);
  assert.match(app.document.querySelector('#genericTemplateRequirements').textContent, /执行器 · Kiro CLI/);
  const calls = app.router.calls.filter(c => c.path.startsWith('/api/workflows/generic-kiro-demo/executor-requirements'));
  assert.ok(calls.length >= 1 && calls.length <= 2, `requirements fetched ${calls.length} times`);

  // An unconfigured CLI reads 需要 … on the card.
  app.router.get(/^\/api\/workflows\/generic-kiro-demo\/executor-requirements/, [{ kind: 'codex', name: 'Codex', configured: false }]);
  app.evaluate(`genericExecutorRequirements.clear();renderSOPCatalog()`);
  await settle(10);
  await new Promise(resolve => setTimeout(resolve, 80));
  await settle(4);
  assert.match(app.document.querySelector('[data-generic-id="generic-kiro-demo"] .sop-requirements').textContent, /需要 Codex/);
});
