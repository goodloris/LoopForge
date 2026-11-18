import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// D20: project and SOP counts agree with the lists they summarise.
test('project settings count requirements even before the requirement list was opened', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get('/api/issues', [
    { id: 'i1', projectId: 'project-1', title: 'A', status: 'todo' },
    { id: 'i2', projectId: 'project-1', title: 'B', status: 'todo' },
  ]);
  app.evaluate(`
    state.data.issues=[];
    state.projectAdminDetailId='project-1';state.projectAdminTab='basic';
    switchView('projects');
  `);
  await settle(8);
  assert.match(app.document.querySelector('#projectAdminHead').textContent, /2 个需求/);
});

test('SOP overview says how many runs the listed SOPs account for', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    state.projectFilter='project-1';state.sopScope='project';
    state.data.workflowMetrics=[
      {workflowId:'flow-1',totalRuns:3,succeededRuns:3,failedRuns:0,totalTokens:0},
      {workflowId:'generic-issue-fix',totalRuns:1,succeededRuns:0,failedRuns:1,totalTokens:0}];
    renderSOPCatalog();
  `);
  const headline = app.document.querySelector('#sopOverview .overview-metric.primary');
  assert.match(headline.textContent, /本项目运行4/);
  assert.match(headline.textContent, /下方 1 条项目 SOP 共 3 次/);
  assert.match(app.document.querySelector('[data-sop="flow-1"] .sop-kpis').textContent, /3运行/);
});
