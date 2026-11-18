import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp } from '../lib/load-app.mjs';

// D16/D29: an SOP that succeeded with its PR still open (or only on the
// integration branch) waits for the merge; it is not 交付闭环.
test('SOP requirement waits for its MR to reach the default branch', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = structuredClone(app.evaluate(`(() => {
    state.data.workflows=[{id:'wf-fix',name:'缺陷修复',nodes:[{id:'f1',name:'修复'}],edges:[]}];
    state.data.projects=[{id:'p1',name:'项目',workflowId:'wf-fix'}];
    const task={id:'t1',issueId:'i1',workflowId:'wf-fix',status:'completed',runId:'r1'};
    const run={id:'r1',workflowId:'wf-fix',status:'succeeded',createdAt:'2026-11-18',
      executionContext:{issueId:'i1',taskId:'t1'},workflowSnapshot:state.data.workflows[0],nodes:{f1:{status:'succeeded'}}};
    state.data.tasks=[task];state.data.runs=[run];
    const pr=state=>({url:'https://github.com/o/shop/pull/9',state});
    const view=issue=>{state.data.issues=[issue];const s=issueCurrentStage(issue),p=conversationIssueProgress(issue,run),o=issueOutcome(issue);return{stage:s.label,key:s.key,progress:p&&p.text,done:p&&p.done,outcome:o.short}};
    const base={id:'i1',projectId:'p1',kind:'issue',status:'in_review',title:'修复',workflowId:'wf-fix',latestTaskId:'t1'};
    return {
      open:view({...base,deliveryState:'pending',deliveryMrs:[pr('opened')]}),
      integrated:view({...base,deliveryState:'pending',deliveryMrs:[pr('integrated')]}),
      merged:view({...base,status:'done',deliveryState:'merged',deliveryMrs:[pr('merged')],deliveryMrState:'merged'}),
      legacy:view({...base,status:'done'}),
    };
  })()`));
  assert.deepEqual(result.open, { stage: 'MR 待合入', key: 'active', progress: '待合入', done: false, outcome: '待合入' });
  assert.deepEqual(result.integrated, { stage: '已合入集成分支', key: 'active', progress: '已合入集成分支', done: false, outcome: '已合入集成分支' });
  assert.equal(result.merged.stage, 'MR 已合入');
  assert.equal(result.merged.key, 'closed');
  assert.equal(result.legacy.stage, '交付闭环');
});

// D28: every node succeeded but the integration merge failed; the run page
// must say why and what the retry does.
test('run page explains a failure that happened after every node succeeded', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = structuredClone(app.evaluate(`(() => {
    const nodes={a:{status:'succeeded'},b:{status:'succeeded'}};
    const merge={type:'mr.auto_merge_failed',message:'基线 MR 自动合并失败: GitHub refused the merge with HTTP 409'};
    const failed={type:'run.failed',message:'合入 integration 失败'};
    return {
      merge:runFailureReason({status:'failed',nodes,events:[merge,failed]}),
      html:runFailureCalloutHTML({status:'failed',nodes,events:[merge,failed]}),
      retried:runFailureReason({status:'failed',nodes:{a:{status:'failed'}},events:[merge,{type:'node.retry_requested'},{type:'run.failed',message:'x'}]}),
      succeeded:runFailureReason({status:'succeeded',nodes,events:[merge]}),
    };
  })()`));
  assert.equal(result.merge.title, '合入集成分支失败');
  assert.match(result.merge.message, /HTTP 409/);
  assert.match(result.merge.hint, /重试运行/);
  assert.match(result.html, /role="alert"/);
  assert.equal(result.retried, null, 'a merge failure before the last retry is stale');
  assert.equal(result.succeeded, null);
});
