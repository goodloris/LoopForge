const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const code = fs.readFileSync(path.join(__dirname, 'app/preview-envs.js'), 'utf8');
function load(extra = {}) {
  const context = {
    state: {data: {repositories: [{id: 'repo-a', name: 'merchant-hub'}]}},
    escapeHTML: value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('"', '&quot;'),
    toasts: [],
    calls: [],
    ...extra
  };
  context.toast = (...args) => context.toasts.push(args);
  vm.createContext(context);
  vm.runInContext(code, context);
  return context;
}

(async () => {
  const ctx = load();
  const ready = {id: 'pe-0123456789', repositoryId: 'repo-a', status: 'ready', sha: 'abcdef0123456789', branch: 'feat/x', url: 'http://pe-0123456789.example.test:8088', adminPassword: 'pw<1>'};
  let html = ctx.previewEnvCardsHTML({enabled: true, adminUser: 'preview', items: [ready]});
  assert.match(html, /merchant-hub/);
  assert.match(html, /abcdef01/);
  assert.doesNotMatch(html, /abcdef0123456789</, 'SHA is shortened in the cell');
  assert.match(html, /data-preview-env-open="pe-0123456789"/);
  assert.match(html, /data-preview-env-destroy="pe-0123456789"/);
  assert.match(html, /pw&lt;1>/, 'password is escaped');
  assert.match(html, /data-tone="success"/);

  // Outsiders get no password: no open button, a member-only hint instead.
  html = ctx.previewEnvCardsHTML({adminUser: 'preview', items: [{...ready, adminPassword: undefined}]});
  assert.doesNotMatch(html, /data-preview-env-open/);
  assert.match(html, /仅项目成员可见/);

  // Destroyed: no URL, no actions. Deploying: no destroy.
  html = ctx.previewEnvCardsHTML({items: [{...ready, status: 'destroyed'}]});
  assert.doesNotMatch(html, /data-preview-env-(open|destroy)|example\.test/);
  html = ctx.previewEnvCardsHTML({items: [{...ready, status: 'deploying'}]});
  assert.doesNotMatch(html, /data-preview-env-(open|destroy)/);
  assert.match(ctx.previewEnvCardsHTML({enabled: false, reason: '缺少 X', items: []}), /缺少 X/);
  // A failed teardown offers a retry; a deploy stuck past the takeover window can be destroyed.
  html = ctx.previewEnvCardsHTML({items: [{...ready, status: 'destroying', lastError: '销毁失败：x'}]});
  assert.match(html, /data-preview-env-destroy="pe-0123456789">重试销毁</);
  assert.doesNotMatch(ctx.previewEnvCardsHTML({items: [{...ready, status: 'destroying'}]}), /data-preview-env-destroy/);
  html = ctx.previewEnvCardsHTML({items: [{...ready, status: 'deploying', updatedAt: new Date(Date.now() - 41 * 60 * 1000).toISOString()}]});
  assert.match(html, /data-preview-env-destroy/);
  assert.match(ctx.previewEnvCardsHTML(null), /workspace-spinner/);
  assert.match(ctx.previewEnvCardsHTML({error: 'boom'}), /boom/);

  // Open: the tab opens before the ticket request, then navigates to the ticket URL.
  const tab = {opener: 'x', location: {href: ''}, closed: false, close() { this.closed = true; }};
  const opened = [];
  const openCtx = load({
    window: {open: (...args) => { opened.push(args); return tab; }},
    api: async (url, opts) => { assert.equal(opened.length, 1, 'tab opened first'); openCtx.calls.push([url, opts.method]); return {url: 'http://pe.example.test/?__lf=t'}; }
  });
  const button = {disabled: false};
  await openCtx.openPreviewEnv('pe-0123456789', button);
  assert.deepEqual(openCtx.calls, [['/api/preview-envs/pe-0123456789/ticket', 'POST']]);
  assert.equal(tab.opener, null);
  assert.equal(tab.location.href, 'http://pe.example.test/?__lf=t');
  assert.equal(button.disabled, false);

  // A failed ticket closes the blank tab and reports the error.
  const failTab = {location: {}, close() { this.closed = true; }};
  const failCtx = load({window: {open: () => failTab}, api: async () => { throw new Error('HTTP 403'); }});
  await failCtx.openPreviewEnv('pe-0123456789');
  assert.equal(failTab.closed, true);
  assert.match(failCtx.toasts[0][0], /HTTP 403/);

  // Destroy asks first; cancelling sends nothing, confirming deletes and reloads.
  let confirmed = false, reloads = 0;
  const delCtx = load({afConfirm: async () => confirmed, api: async (url, opts) => { delCtx.calls.push([url, opts.method]); return {}; }});
  await delCtx.destroyPreviewEnv('pe-0123456789', () => reloads++);
  assert.equal(delCtx.calls.length, 0);
  confirmed = true;
  await delCtx.destroyPreviewEnv('pe-0123456789', () => reloads++);
  assert.deepEqual(delCtx.calls, [['/api/preview-envs/pe-0123456789', 'DELETE']]);
  assert.equal(reloads, 1);
})().catch(err => { console.error(err); process.exit(1); });
