import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// 通道管理（企业微信 Bot 通知通道）在 channelsEnabled=false 时从界面移除：导航、命令
// 面板没有入口，直接访问路由跳回工作台，SOP/定时任务的通道选择器按历史配置显示或隐藏。

test('the 通道管理 entry is gone from the sidebar, command palette and view titles', async (t) => {
  const app = await loadApp();
  t.after(app.close);

  const navBtn = app.document.querySelector('.nav[data-view="bots"]');
  assert.ok(navBtn, 'the bots nav button still exists in the DOM');
  assert.equal(navBtn.hidden, true, 'the bots nav button is hidden');
  assert.equal(navBtn.style.display, 'none', 'the bots nav button is display:none');

  assert.equal(app.evaluate('!!viewTitles().bots'), false, 'bots has no routable title');
  assert.equal(
    app.evaluate('commandCatalog().some(c => c.title === "通道管理")'),
    false,
    'the command palette lists no 通道管理 page',
  );
});

test('a direct #/bots route falls back to the workbench instead of a blank page', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const view = app.evaluate("switchView('bots'); activeViewName()");
  assert.equal(view, 'collaboration', 'navigating to bots lands on the workbench');
  assert.equal(
    app.document.querySelector('#botsView')?.classList.contains('active'),
    false,
    'the bots view is not activated',
  );
});

test('the notification channel selector hides when no Bot is configured', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/notification-bots', []);
  app.evaluate(`
    nodeBotCache.clear();
    window.__notif=document.createElement('div');
    window.__notif.id='notifTest';
    document.body.appendChild(window.__notif);
    mountNotificationEditor(window.__notif, [], {sourceType:'schedule_job', projectId:'project-empty'});
  `);
  await settle(8);
  const host = app.document.querySelector('#notifTest');
  assert.equal(host.hidden, true, 'the selector is hidden when the project has no Bot');
  assert.equal(host.querySelector('select'), null, 'no channel selector is rendered');
});

test('an existing Bot config stays visible but read-only when channels are off', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/notification-bots', []); // the bound Bot was removed upstream
  app.evaluate(`
    nodeBotCache.clear();
    window.__notif=document.createElement('div');
    window.__notif.id='notifTest';
    document.body.appendChild(window.__notif);
    mountNotificationEditor(window.__notif,
      [{botId:'bot-legacy', events:['succeeded'], template:'done', contentMode:'ai'}],
      {sourceType:'schedule_job', projectId:'project-legacy'});
  `);
  await settle(8);
  const host = app.document.querySelector('#notifTest');
  assert.equal(host.hidden, false, 'historical config is still shown');
  const select = host.querySelector('select[data-notification-bot]');
  assert.ok(select, 'the historical binding renders its selector');
  assert.equal(select.disabled, true, 'the selector is read-only');
  assert.equal(host.querySelector('[data-notification-manage]'), null, 'the Bot 管理 link is removed');
  assert.match(host.textContent, /仅供查看/, 'a read-only notice is shown');
});

test('a configured project keeps the channel selector read-only rather than hiding it', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/notification-bots', [
    { id: 'bot-1', name: 'Release Bot', enabled: true, target: { id: 'c1', name: '研发群', type: 'group' } },
  ]);
  app.evaluate(`
    nodeBotCache.clear();
    window.__notif=document.createElement('div');
    window.__notif.id='notifTest';
    document.body.appendChild(window.__notif);
    mountNotificationEditor(window.__notif, [], {sourceType:'schedule_job', projectId:'project-has-bot'});
  `);
  await settle(8);
  const host = app.document.querySelector('#notifTest');
  assert.equal(host.hidden, false, 'a project with a configured Bot keeps the selector visible');
  const addBtn = host.querySelector('[data-notification-add]');
  assert.ok(addBtn, 'the editor renders its add-channel control');
  assert.equal(addBtn.disabled, true, 'but every control is read-only while channels are off');
  assert.equal(host.querySelector('[data-notification-manage]'), null, 'the Bot 管理 link is removed');
});
