import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

// D5: a project's human gates reach every member's inbox, not only the responsible user's.
const gate = (id, project, rid) => ({
  id, status: 'waiting', workflowId: 'flow', createdAt: '2026-11-18T10:00:00Z',
  executionContext: { responsibleUserId: rid, projectId: project },
  workflowSnapshot: { id: 'flow', name: `SOP ${id}`, nodes: [{ id: 'gate', name: '验收', type: 'approval' }], edges: [] },
  nodes: { gate: { status: 'waiting_approval' } },
});

test('approval inbox lists project members\' gates, grouped, and viewers read only', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const runs = [gate('mine', 'shop', 7), gate('teammate', 'shop', 8), gate('viewed', 'docs', 8), gate('foreign', 'other', 8)];
  app.evaluate(`
    state.currentUser={id:7,loginName:'chenxi'};state.projectFilter='';
    state.data.projects=[
      {id:'shop',ownerId:1,members:[{userId:7,role:'member'},{userId:8,role:'member'}]},
      {id:'docs',ownerId:1,members:[{userId:7,role:'viewer'}]},
      {id:'other',ownerId:1,members:[]}];
    state.data.runs=${JSON.stringify(runs)};state.data.tasks=[];state.data.issues=[];
  `);
  assert.equal(app.evaluate(`JSON.stringify(approvalItems().map(r=>r.id))`), JSON.stringify(['mine', 'teammate', 'viewed']));
  await app.evaluate('renderApprovals(true)');
  const list = app.document.querySelector('#approvalList');
  assert.deepEqual([...list.querySelectorAll('.ap-group-title')].map(n => n.firstChild.textContent), ['指派给我', '项目内其他']);
  const cards = [...list.querySelectorAll('.ap-card')];
  assert.equal(cards.length, 3);
  assert.ok(cards[1].querySelector('[data-approve]'), 'members can approve a teammate\'s gate');
  assert.ok(cards[2].classList.contains('is-readonly'));
  assert.equal(cards[2].querySelector('[data-approve],[data-reject],[data-cancel-run]'), null, 'viewers cannot act');
  assert.match(cards[2].textContent, /只能查看/);
  // The badge and the stats count what the user can act on.
  assert.equal(app.document.querySelector('#approvalNavBadge').textContent, '2');
  assert.match(app.document.querySelector('#approvalStats').textContent, /2 条待确认.*1 条仅可查看/);
});
