import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const snapshot = {
  id: 'flow-gate', name: '平台验收', version: 1, revision: 1,
  nodes: [{ id: 'report', name: '报告', type: 'script' }, { id: 'approve', name: '确认', type: 'approval' }],
  edges: [{ source: 'report', target: 'approve' }],
};

test('approval inbox fetches full runs behind list summaries and only offers newer SOPs', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const full = {
    id: 'gate-run', projectId: 'project-1', status: 'waiting', workflowId: 'flow-gate', executionContext: { responsibleUserId: 7, projectId: 'project-1' }, createdAt: new Date(Date.now() - 90e3).toISOString(),
    workflowSnapshot: snapshot, nodes: { report: { status: 'succeeded' }, approve: { status: 'waiting_approval' } },
  };
  // /api/runs summaries only keep {id,name} of the snapshot.
  const summary = { ...full, workflowSnapshot: { id: 'flow-gate', name: '平台验收' } };
  app.router.get('/api/runs/gate-run', full);
  app.evaluate(`
    state.currentUser={id:7,loginName:'reviewer'};
    state.data.runs=${JSON.stringify([summary])};state.data.tasks=[];state.data.issues=[];
    state.data.workflows=[${JSON.stringify(snapshot)}];
  `);
  await app.evaluate('renderApprovals(true)');
  const card = app.document.querySelector('#approvalList .ap-card');
  assert.match(card.textContent, /为什么需要人工确认/);
  assert.doesNotMatch(card.textContent, /门禁未通过/);
  assert.doesNotMatch(card.textContent, /更新为最新 SOP/, 'run already uses the latest SOP');
  assert.match(card.textContent, /已等待 1 分钟(?!前)/);

  const result = app.evaluate(`(() => {
    const run=${JSON.stringify(full)},wf=${JSON.stringify(snapshot)};
    const button=latest=>{state.data.workflows=[{...wf,...latest}];return refreshRunSOPButtonHTML(run)};
    state.data.runs=[];state.activeRun=null;
    return {
      summary:(()=>{state.data.workflows=[{...wf,version:2}];return refreshRunSOPButtonHTML(${JSON.stringify(summary)})})(),
      same:button({}), newerRevision:button({revision:2}), newerVersion:button({version:2,revision:1}),
      older:button({version:1,revision:0}),
      approvalFallback:humanGateSectionsHTML({status:'waiting_approval'},null,{id:'approve',type:'approval'}),
      gateFallback:humanGateSectionsHTML({status:'waiting_approval'},null,{id:'build',type:'agent'}),
      justNow:approvalWaitedText(new Date().toISOString()),
      hours:approvalWaitedText(new Date(Date.now()-3*3600e3).toISOString()),
    };
  })()`);
  assert.equal(result.summary, '', 'a summary snapshot has no version to compare');
  assert.equal(result.same, '');
  assert.equal(result.older, '');
  assert.match(result.newerRevision, /更新为最新 SOP（v1 #2）/);
  assert.match(result.newerVersion, /更新为最新 SOP（v2 #1）/);
  assert.match(result.approvalFallback, /这是人工确认节点/);
  assert.doesNotMatch(result.approvalFallback, /门禁未通过/);
  assert.match(result.gateFallback, /门禁未通过/);
  assert.equal(result.justNow, '刚开始等待');
  assert.equal(result.hours, '已等待 3 小时');
});

test('run detail hides stale stage times, links requirements, and states the input once', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const r = app.evaluate(`(() => {
    const stage=[{id:'a'},{id:'b'}];
    const range=nodes=>stageTimeRange({nodes},stage);
    return {
      reset:range({a:{status:'pending',startedAt:'2026-10-21T10:00:00Z',finishedAt:'0001-01-01T00:00:00Z'}}),
      running:range({a:{status:'running',startedAt:'2026-10-21T10:00:00Z'}}),
      done:range({a:{status:'succeeded',startedAt:'2026-10-21T10:00:00Z',finishedAt:'2026-10-21T10:01:00Z'},
        b:{status:'pending',startedAt:'2026-10-21T09:00:00Z'}}),
      desc:[runHeaderDescription('修复登录','修复登录\\n\\n密码错误时提示'),runHeaderDescription('修复登录','修复登录\\n\\n修复登录接口的报错'),
        runHeaderDescription('修复登录','修复登录'),runHeaderDescription('SOP','另一段输入'),runHeaderDescription('SOP','')],
    };
  })()`);
  assert.equal(r.reset, null, 'a node sent back to pending has not started this round');
  assert.equal(r.running.running, true);
  assert.equal(r.done.running, false);
  assert.equal(r.done.start, Date.parse('2026-10-21T10:00:00Z'), 'stale startedAt of a pending node is ignored');
  assert.equal(JSON.stringify(r.desc), JSON.stringify(['密码错误时提示', null, null, '另一段输入', '']));

  const workflow = app.evaluate('structuredClone(state.data.workflows[0])');
  const run = {
    ...app.evaluate('structuredClone(state.data.runs[0])'), status: 'running', workflowSnapshot: workflow,
    input: '修复登录\n\n修复登录接口的报错', executionContext: { issueId: 'issue-not-loaded' }, nodes: {},
  };
  app.router.get(`/api/runs/${run.id}`, run);
  app.evaluate(`state.data.tasks=[];state.data.runs=[${JSON.stringify(run)}];state.selectedRun=${JSON.stringify(run.id)};state.activeRun=state.data.runs[0];switchView('runs');showRunDetail(${JSON.stringify(run.id)})`);
  await settle(6);
  const header = app.document.querySelector('#runDetail .run-detail-header');
  assert.match(header.querySelector('.run-breadcrumb').textContent, /关联需求/);
  assert.doesNotMatch(header.querySelector('.run-breadcrumb').textContent, /独立运行/);
});

test('run modal is a labelled dialog and its cancel button uses the plain button style', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const modal = app.document.querySelector('#runModal');
  const dialog = modal.querySelector('[role="dialog"]');
  assert.ok(dialog);
  assert.equal(dialog.getAttribute('aria-modal'), 'true');
  assert.equal(app.document.getElementById(dialog.getAttribute('aria-labelledby'))?.textContent, '发起流程运行');
  const cancel = [...modal.querySelectorAll('.modal-actions button')].find(b => b.textContent === '取消');
  assert.ok(!cancel.classList.contains('modal-close'), '.modal-close is the 22px × icon style');
  modal.classList.add('open');
  cancel.click();
  assert.ok(!modal.classList.contains('open'), 'cancel still closes the modal');
});

test('role usage ignores generic templates that share a copied role id', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const uses = app.evaluate(`(() => {
    state.data.workflows=[
      {id:'generic-backend',generic:true,nodes:[{id:'n',agentId:'tpl-review'}]},
      {id:'mine',projectId:'p1',nodes:[{id:'n',agentId:'tpl-review'}]},
      {id:'other',projectId:'p2',nodes:[{id:'n',agentId:'tpl-review'},{id:'m',agentId:'own-role'}]},
      {id:'mine2',projectId:'p1',nodes:[{id:'n',agentId:'own-role'}]},
    ];
    return [agentNodeUses({id:'tpl-review',projectId:'p1'}),agentNodeUses({id:'tpl-review',projectId:'p1',shared:true}),agentNodeUses({id:'own-role',projectId:'p1'})];
  })()`);
  assert.equal(JSON.stringify(uses), '[1,3,2]');
});
