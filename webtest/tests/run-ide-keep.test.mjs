import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// D19: browsers reload an iframe that leaves the document, so re-rendering a run (node switch,
// status change) must keep the task IDE attached instead of detaching and re-inserting it.
test('re-rendering a run keeps the task IDE iframe attached', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const run = {
    id: 'run-ide', status: 'running', workflowId: 'flow-1', createdAt: '2026-11-18T08:00:00Z',
    executionContext: { projectId: 'project-1' },
    workflowSnapshot: { id: 'flow-1', name: 'Fix', nodes: [{ id: 'a', name: 'Implement', type: 'agent' }, { id: 'b', name: 'Review', type: 'agent' }], edges: [] },
    nodes: { a: { status: 'running' }, b: { status: 'pending' } },
  };
  app.evaluate(`
    state.runDetailTab='workspace';state.selectedRun='run-ide';
    state.runWorkspace={runId:'run-ide',cleaned:false,info:{available:true}};
    state.activeRun=${JSON.stringify(run)};
    renderRunDetail(state.activeRun);
  `);
  const host = app.document.querySelector('#runDetail');
  const frame = host.querySelector('iframe[data-vscode-frame]');
  assert.ok(frame, 'IDE rendered');
  // jsdom, like browsers, gives a re-inserted iframe a fresh browsing context.
  // The next node starts: full re-render, then a workspace refresh.
  const kept = app.evaluate(`(() => {
    const frame=$('#runDetail iframe[data-vscode-frame]'),context=frame.contentWindow;
    state.activeRun={...state.activeRun,status:'waiting',nodes:{a:{status:'succeeded'},b:{status:'running'}}};
    renderRunDetail(state.activeRun);
    const afterDetail=$('#runDetail iframe[data-vscode-frame]')===frame&&frame.contentWindow===context;
    renderWorkspacePane(state.activeRun);
    const afterPane=$('#runDetail iframe[data-vscode-frame]')===frame&&frame.contentWindow===context;
    return {afterDetail,afterPane};
  })()`);
  assert.equal(kept.afterDetail, true, 'run re-render did not reload the IDE');
  assert.equal(kept.afterPane, true, 'workspace refresh did not reload the IDE');
  assert.equal(host.querySelector('[data-run-pane="workspace"]').hidden, false);
  assert.match(host.querySelector('[data-run-node="b"]').textContent, /Review/);
  // Switching tabs still works on the kept pane.
  app.evaluate(`state.runDetailTab='overview';renderRunDetail(state.activeRun)`);
  assert.equal(host.querySelector('[data-run-pane="workspace"]').hidden, true);
  assert.equal(host.querySelector('iframe[data-vscode-frame]'), frame);
});
