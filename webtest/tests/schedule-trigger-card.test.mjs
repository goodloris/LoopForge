import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { loadApp } from '../lib/load-app.mjs';

// D24: the SOP 触发 card lays its summary out in separate lines with spaced actions.
// jsdom does not apply stylesheets, so the layout is checked through structure and the CSS scope.
test('SOP trigger card separates the summary fields and groups the actions', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    state.projectFilter='project-1';
    state.data.scheduleJobs=[{id:'trigger',projectId:'project-1',name:'触发配置',cron:'0 9 * * *',source:'github',
      issueStatuses:['backlog','todo'],enabled:true,workflowId:'flow-1'}];
    renderScheduleJobs();
  `);
  const card = app.document.querySelector('#aiScheduleTriggerPanel .schedule-config-runtime');
  assert.ok(card);
  const [summary, actions] = card.children;
  assert.deepEqual([...summary.children].map(n => n.tagName), ['B', 'SPAN', 'SMALL']);
  assert.match(summary.children[0].textContent, /已启用 · .*0 9 \* \* \*/);
  assert.match(summary.children[1].textContent, /只同步 GitHub · 待规划、待处理/);
  assert.ok(actions.classList.contains('schedule-config-actions'));
  assert.deepEqual([...actions.querySelectorAll('button')].map(b => b.textContent), ['执行记录', '立即运行']);

  const css = await readFile(new URL('../../web/sync.css', import.meta.url), 'utf8');
  // Upstream #1036 restyled the card (#aiScheduleTriggerList); the summary
  // still stacks its lines in a column.
  assert.match(css, /:is\(#aiScheduleTriggerList, #scheduleModal\) \.schedule-config-runtime>div \{ display:flex;flex-direction:column/);
});
