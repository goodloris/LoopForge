import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// D25: SOPs created after page load (by a teammate or the clone API) appear in 默认 SOP.
test('trigger modal lists SOPs created after the page loaded', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const flows = [
    { id: 'flow-1', projectId: 'project-1', name: 'Delivery Flow', status: 'published', nodes: [], edges: [] },
    { id: 'sop-s4-cmdgate', projectId: 'project-1', name: '缺陷修复 SOP（命令门禁）', status: 'published', nodes: [], edges: [] },
  ];
  app.router.get('/api/schedule-jobs', []);
  app.router.get('/api/workflows', flows);
  app.evaluate(`
    state.currentUser={id:1,loginName:'owner',isPlatformAdmin:true};
    state.projectFilter='project-1';
    state.data.workflows=[${JSON.stringify(flows[0])}];
  `);
  await app.evaluate(`openScheduleJobModal('dispatch')`);
  await settle(6);
  const options = [...app.document.querySelector('#scheduleJobForm [name="workflowId"]').options].map(o => o.value);
  assert.ok(options.includes('sop-s4-cmdgate'), options.join(','));
});
