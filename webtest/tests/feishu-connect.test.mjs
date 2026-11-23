import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

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
