import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import { JSDOM } from 'jsdom';
import { loadApp, settle, WEB_ROOT } from '../lib/load-app.mjs';

async function mountCard(t, reply) {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/users/me/feishu', reply);
  app.evaluate(`
    state.currentUser={id:1,loginName:'owner'};
    window.feishuToasts=[];toast=message=>window.feishuToasts.push(message);
    afConfirm=async()=>true;
    const host=document.createElement('div');host.id='feishu-test';document.body.append(host);
    mountFeishuConnect(host);
  `);
  await settle(5);
  return { app, panel: app.document.querySelector('#feishuConnectPanel') };
}

test('Feishu card stays hidden when the platform has no Feishu app', async (t) => {
  const { panel } = await mountCard(t, { enabled: false, connected: false });
  assert.equal(panel.hidden, true);
});

test('Feishu card connects read-only by default and read-write only when chosen', async (t) => {
  const { panel } = await mountCard(t, { enabled: true, connected: false, presets: {} });
  assert.equal(panel.hidden, false);
  assert.match(panel.textContent, /未连接/);
  assert.equal(panel.querySelector('[data-feishu-disconnect]'), null);
  const connect = panel.querySelector('[data-feishu-connect]');
  assert.equal(connect.textContent, '连接飞书');
  assert.equal(connect.getAttribute('href'), '/api/users/me/feishu/connect?preset=readonly');
  const [readonly, readwrite] = panel.querySelectorAll('input[name="feishuPreset"]');
  assert.equal(readonly.checked, true);
  readwrite.checked = true;
  readwrite.dispatchEvent(new readwrite.ownerDocument.defaultView.Event('change', { bubbles: true }));
  assert.equal(connect.getAttribute('href'), '/api/users/me/feishu/connect?preset=readwrite');
  assert.match(panel.textContent, /不会逐条请你确认/, 'read-write explains that commands run without confirmation');
});

test('connected Feishu card shows the grant and disconnects through DELETE', async (t) => {
  const { app, panel } = await mountCard(t, { enabled: true, connected: true, status: 'active', name: '张<三>', preset: 'readonly', scopes: ['offline_access', 'docx:document:readonly'] });
  assert.match(panel.textContent, /已连接/);
  assert.match(panel.textContent, /只读/);
  assert.match(panel.textContent, /docx:document:readonly/);
  assert.ok(!panel.innerHTML.includes('<三>'), 'the Feishu name is escaped');
  assert.equal(panel.querySelector('[data-feishu-connect]').textContent, '重新授权');
  app.router.delete('/api/users/me/feishu', { enabled: true, connected: false, status: 'revoked' });
  app.router.get('/api/users/me/feishu', { enabled: true, connected: false, status: 'revoked' });
  await panel.querySelector('[data-feishu-disconnect]').onclick();
  await settle(5);
  assert.ok(app.router.calls.some(call => call.method === 'DELETE' && call.path === '/api/users/me/feishu'));
  assert.equal(app.evaluate('window.feishuToasts.join()'), '已断开飞书');
  assert.match(panel.textContent, /未连接/);
});

test('expired Feishu grant asks to authorize again, keeping the chosen preset', async (t) => {
  const { panel } = await mountCard(t, { enabled: true, connected: false, status: 'expired', name: '张三', preset: 'readwrite' });
  assert.match(panel.textContent, /已过期/);
  assert.match(panel.textContent, /请重新授权/);
  const connect = panel.querySelector('[data-feishu-connect]');
  assert.equal(connect.textContent, '重新授权');
  assert.equal(connect.getAttribute('href'), '/api/users/me/feishu/connect?preset=readwrite');
});

test('returning from Feishu shows a toast and drops the query flag', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/users/me/feishu', { enabled: true, connected: true, status: 'active', name: '张三', preset: 'readonly' });
  app.evaluate(`
    history.replaceState(null,'','/?feishu=connected#/settings');
    state.currentUser={id:1,loginName:'owner'};
    window.feishuToasts=[];toast=message=>window.feishuToasts.push(message);
    const host=document.createElement('div');document.body.append(host);mountFeishuConnect(host);
  `);
  await settle(5);
  assert.equal(app.evaluate('window.feishuToasts.join()'), '飞书已连接');
  assert.equal(app.evaluate('location.search'), '');
  assert.equal(app.evaluate('location.hash'), '#/settings');
});

test('「同时用于登录」 adds login=1 to the connect link', async (t) => {
  const { panel } = await mountCard(t, { enabled: true, connected: false });
  const connect = panel.querySelector('[data-feishu-connect]');
  const login = panel.querySelector('[data-feishu-login]');
  assert.ok(login, 'a not-yet-bound account is offered the login option');
  login.checked = true;
  login.dispatchEvent(new login.ownerDocument.defaultView.Event('change', { bubbles: true }));
  assert.equal(connect.getAttribute('href'), '/api/users/me/feishu/connect?preset=readonly&login=1');
  const readwrite = panel.querySelectorAll('input[name="feishuPreset"]')[1];
  readwrite.checked = true;
  readwrite.dispatchEvent(new readwrite.ownerDocument.defaultView.Event('change', { bubbles: true }));
  assert.equal(connect.getAttribute('href'), '/api/users/me/feishu/connect?preset=readwrite&login=1', 'the preset keeps the login choice');
});

test('a connected account can bind Feishu login on its own', async (t) => {
  const { panel } = await mountCard(t, { enabled: true, connected: true, status: 'active', name: '张三', preset: 'readonly' });
  assert.equal(panel.querySelector('[data-feishu-login]'), null);
  assert.equal(panel.querySelector('[data-feishu-bind]').getAttribute('href'), '/api/users/me/feishu/bind');
});

test('a bound account unbinds through DELETE, unless Feishu is its only sign-in', async (t) => {
  const { app, panel } = await mountCard(t, { enabled: true, connected: true, status: 'active', name: '张三', loginBound: true, loginName: '张三', canUnbindLogin: true });
  assert.match(panel.textContent, /已用于登录 · 张三/);
  assert.equal(panel.querySelector('[data-feishu-bind]'), null);
  app.router.delete('/api/users/me/feishu/bind', { enabled: true, connected: true, status: 'active' });
  app.router.get('/api/users/me/feishu', { enabled: true, connected: true, status: 'active' });
  await panel.querySelector('[data-feishu-unbind]').onclick();
  await settle(5);
  assert.ok(app.router.calls.some(call => call.method === 'DELETE' && call.path === '/api/users/me/feishu/bind'));
  assert.equal(app.evaluate('window.feishuToasts.join()'), '已解绑飞书登录');
  assert.ok(panel.querySelector('[data-feishu-bind]'), 'after unbinding the bind link is back');

  const only = await mountCard(t, { enabled: true, connected: false, loginBound: true, loginName: '张三', canUnbindLogin: false });
  const unbind = only.panel.querySelector('[data-feishu-unbind]');
  assert.equal(unbind.disabled, true);
  assert.match(only.panel.textContent, /唯一的登录方式/);
  assert.equal(only.panel.querySelector('[data-feishu-login]'), null, 'already bound: no login option on connect');
});

test('returning from a login binding shows its toast', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/users/me/feishu', { enabled: true, connected: true, status: 'active', loginBound: true });
  app.evaluate(`
    history.replaceState(null,'','/?feishu=connected-bound#/settings');
    state.currentUser={id:1,loginName:'owner'};
    window.feishuToasts=[];toast=message=>window.feishuToasts.push(message);
    const host=document.createElement('div');document.body.append(host);mountFeishuConnect(host);
  `);
  await settle(5);
  assert.equal(app.evaluate('window.feishuToasts.join()'), '飞书已连接，并已用于登录');
  assert.equal(app.evaluate('location.search'), '');
});

test('「同时用于登录」 warns that skipping it makes Feishu sign-in create another account', async (t) => {
  const { app, panel } = await mountCard(t, { enabled: true, connected: false });
  const label = panel.querySelector('[data-feishu-login]').closest('label');
  assert.match(label.textContent, /不勾选时，之后用飞书登录会新建另一个账号/);

  // English mode translates the whole label instead of falling back to Chinese.
  const dom = new JSDOM('<html data-lang="en"><body></body></html>', { runScripts: 'outside-only', url: 'http://127.0.0.1/' });
  t.after(() => dom.window.close());
  for (const file of ['i18n-en.js', 'i18n.js']) {
    new vm.Script(await readFile(path.join(WEB_ROOT, file), 'utf8'), { filename: file }).runInContext(dom.getInternalVMContext());
  }
  const host = dom.window.document.createElement('div');
  host.innerHTML = app.evaluate(`feishuConnectBodyHTML({enabled:true,connected:false})`);
  dom.window.document.body.append(host);
  await new Promise((resolve) => dom.window.setTimeout(resolve, 0));
  const english = host.querySelector('.feishu-login-opt').textContent;
  assert.match(english, /If unchecked, signing in with Feishu later creates a separate account/);
  assert.doesNotMatch(english, /[\u3400-\u9fff]/);
});
