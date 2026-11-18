import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

const DOC = 'https://github.com/Tencent/LoopForge/blob/main/docs/deployment.md#test-command-gate';

// #25 follow-up: SOPs gated on .loopforge/verify say when the target branch has no such file.
test('designer and requirement detail warn when .loopforge/verify is missing', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.get(/^\/api\/workflows\/flow-1\/verify-gate\?projectId=project-1$/, { gate: true, repositories: [{ id: 'repo-1', name: 'atlas', branch: 'main', status: 'missing' }] });
  app.router.get(/^\/api\/workflows\/flow-1\/verify-gate\?issueId=issue-1$/, { gate: true, repositories: [{ id: 'repo-1', name: 'atlas', branch: 'main', status: 'unknown' }] });
  // Boot already rendered the header once with the default mock; start from a clean cache.
  app.evaluate(`verifyGateCache.clear();delete $('#verifyGateHint').dataset.verifyDone;state.projectFilter='project-1';state.workflow=clone(state.data.workflows[0]);renderFlowHeader()`);
  await settle(6);
  const banner = app.document.querySelector('#verifyGateHint');
  assert.equal(banner.hidden, false);
  assert.match(banner.textContent, /未配置验证命令（\.loopforge\/verify），测试结论由 Agent 判定/);
  assert.equal(banner.querySelector('a').getAttribute('href'), DOC);

  const hint = app.evaluate(`(() => {
    const host=document.createElement('div');host.innerHTML='<div data-verify-gate data-workflow="flow-1" hidden></div>';document.body.append(host);
    const el=host.firstChild;mountVerifyGateHint(el,'flow-1',{issueId:'issue-1'});return el;
  })()`);
  await settle(6);
  assert.equal(hint.hidden, false);
  assert.match(hint.textContent, /无法确认是否配置了验证命令/);

  // SOPs without the gate and configured repositories show nothing.
  assert.equal(app.evaluate(`verifyGateHintHTML({gate:false,repositories:[]})`), '');
  assert.equal(app.evaluate(`verifyGateHintHTML({gate:true,repositories:[{id:'r',status:'configured'}]})`), '');
});

test('requirement 执行进展 tab mounts the verify gate hint for its SOP', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  let asked = '';
  app.router.get(/^\/api\/workflows\/flow-1\/verify-gate/, (call) => { asked = call.search; return { gate: true, repositories: [{ id: 'repo-1', name: 'atlas', status: 'missing' }] }; });
  app.evaluate(`
    const issue={...state.data.issues[0],workflowId:'flow-1'};
    state.issueDetailData={issue,task:null,run:null,requirement:{lanes:[{workflowId:'flow-1',workflowName:'Delivery Flow'}]}};
    state.issueDetailTab='sop';renderIssueDetailTab();
  `);
  await settle(6);
  assert.equal(asked, '?issueId=issue-1');
  const el = app.document.querySelector('#issueDetailBody [data-verify-gate]');
  assert.ok(el && !el.hidden);
  assert.match(el.textContent, /测试结论由 Agent 判定/);
});
