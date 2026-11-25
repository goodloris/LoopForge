import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { loadApp, settle } from '../lib/load-app.mjs';
import { TOKEN_ONLY_SCENARIO } from '../lib/usage-token-only-scenario.mjs';

const json = async (url) => JSON.parse(await readFile(new URL(url, import.meta.url), 'utf8'));

test('token-only usage renders byte-for-byte as before credits existed', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const golden = await json('./fixtures/usage-token-only-golden.json');
  const got = JSON.parse(app.evaluate(TOKEN_ONLY_SCENARIO));
  assert.deepEqual(Object.keys(got).sort(), Object.keys(golden).sort());
  for (const key of Object.keys(golden)) assert.equal(got[key], golden[key], `${key} changed for token-only data`);
  for (const key of Object.keys(golden)) assert.doesNotMatch(got[key], /credits/i, `${key} mentions credits without credits data`);
  const lanes = JSON.parse(got.lanes);
  assert.equal('credits' in lanes[0], false, 'token-only lanes keep their shape');
  assert.equal(app.evaluate(`JSON.stringify(nodeUsageFact({totalTokens:1800}))`), JSON.stringify({ label: 'Token', value: '1.8k' }));
});

test('formatCredits uses the vectors shared with the chat client', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const vectors = await json('../../chatbot/tests/fixtures/credits-format-vectors.json');
  for (const { input, text } of vectors) {
    assert.equal(app.evaluate(`formatCredits(${JSON.stringify(input)})`), text, `formatCredits(${JSON.stringify(input)})`);
  }
  assert.equal(app.evaluate(`usageAmountText(2400,0)`), '2.4k tokens');
  assert.equal(app.evaluate(`usageAmountText(0,0)`), '0 tokens');
  assert.equal(app.evaluate(`usageAmountText(0,12.34)`), '12.34 credits');
  assert.equal(app.evaluate(`usageAmountText(2400,12.34)`), '2.4k tokens · 12.34 credits');
});

test('run usage puts credits first for Kiro and shows both for mixed executors', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const kiro = app.evaluate(`liveUsageHTML({usage:{credits:12.34},toolCalls:56})`);
  assert.match(kiro, /<b>12\.34 credits · 56 tools<\/b>/);
  assert.match(kiro, /按 credits 计量，未上报 tokens/);
  assert.doesNotMatch(kiro, /0 tokens|输入|\$0\.0000/);
  const mixed = app.evaluate(`liveUsageHTML({usage:{totalTokens:2400,inputTokens:1600,outputTokens:800,costUsd:0.24,credits:12.34},toolCalls:3})`);
  assert.match(mixed, /<b>2\.4k tokens · 12\.34 credits · 3 tools<\/b>/);
  assert.match(mixed, /1\.6k 输入 \/ 800 输出 · cache 0 命中 \/ 0 写入 · \$0\.2400<\/small>/);
  assert.equal((mixed.match(/credits/g) || []).length, 1, 'credits are not repeated in the detail line');
  assert.equal(app.evaluate(`JSON.stringify(nodeUsageFact({credits:0.5}))`), JSON.stringify({ label: 'Credits', value: '0.50' }));
  assert.equal(app.evaluate(`JSON.stringify(nodeUsageFact({totalTokens:1800,credits:12.34}))`), JSON.stringify({ label: 'Token / Credits', value: '1.8k / 12.34' }));
});

test('node details show credits in the initial render and after a live update', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const workflow = app.evaluate('structuredClone(state.data.workflows[0])');
  const firstNode = workflow.nodes[0].id;
  const run = {
    ...app.evaluate('structuredClone(state.data.runs[0])'), status: 'running', workflowSnapshot: workflow,
    usage: { credits: 12.34 }, toolCalls: 2,
    nodes: { [firstNode]: { status: 'running', attempt: 1, usage: { credits: 12.34 } } },
  };
  app.router.get(`/api/runs/${run.id}`, run);
  app.evaluate(`state.data.runs=[${JSON.stringify(run)}];state.selectedRun=${JSON.stringify(run.id)};state.activeRun=state.data.runs[0];state.selectedRunNode=${JSON.stringify(firstNode)}`);
  app.evaluate(`switchView('runs');showRunDetail(${JSON.stringify(run.id)})`);
  await settle(6);
  app.evaluate(`state.selectedRunNode=${JSON.stringify(firstNode)};renderRunDetail(state.activeRun)`);
  const cell = () => [...app.document.querySelectorAll('#runDetail .node-facts > div')][2];
  assert.equal(cell().querySelector('span').textContent, 'Credits');
  assert.equal(cell().querySelector('b').textContent, '12.34');
  assert.match(app.document.querySelector('#runDetail [data-run-usage] b').textContent, /^12\.34 credits · 2 tools$/);
  const initial = cell().outerHTML;
  // Live update: the node starts reporting tokens too (mixed executor).
  app.evaluate(`(() => {const r=structuredClone(state.activeRun);r.nodes[${JSON.stringify(firstNode)}].usage={totalTokens:1800,credits:13.5};r.usage={totalTokens:1800,credits:13.5};state.activeRun=r;patchRunLive(r)})()`);
  assert.equal(cell().querySelector('span').textContent, 'Token / Credits');
  assert.equal(cell().querySelector('b').textContent, '1.8k / 13.50');
  assert.match(app.document.querySelector('#runDetail [data-run-usage] b').textContent, /^1\.8k tokens · 13\.50 credits · 2 tools$/);
  // Live update back to credits-only matches the initial render.
  app.evaluate(`(() => {const r=structuredClone(state.activeRun);r.nodes[${JSON.stringify(firstNode)}].usage={credits:12.34};state.activeRun=r;patchRunLive(r)})()`);
  assert.equal(cell().outerHTML, initial);
});

test('requirement progress sums credits from the server snapshot and the client fallback', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const out = app.evaluate(`(() => {
    state.data.workflows=[{id:'wf',name:'交付',nodes:[{id:'n1',name:'实现'}],edges:[]}];
    const issue={id:'i1',projectId:'project-1',kind:'requirement',source:'local',status:'in_progress',title:'需求',workflowId:'wf'};
    state.data.issues=[issue];state.data.runs=[];state.data.tasks=[];
    const lane={lane:'backend',label:'交付',workflowId:'wf',status:'succeeded',started:true,runCount:2};
    const kiro=sopPane(issue,{issue,run:null,requirement:{issue,overall:{runCount:2,tokens:0,credits:12.34,status:'active'},lanes:[{...lane,tokens:0,credits:12.34}]}});
    const mixed=sopPane(issue,{issue,run:null,requirement:{issue,overall:{runCount:2,tokens:2400,credits:0.5,status:'active'},lanes:[{...lane,tokens:2400,credits:0.5}]}});
    const run=(id,usage)=>({id,workflowId:'wf',status:'succeeded',createdAt:'2026-10-01',executionContext:{issueId:'i1'},workflowSnapshot:state.data.workflows[0],usage,nodes:{}});
    state.data.runs=[run('a',{credits:1.25}),run('b',{totalTokens:500,credits:2.5})];
    const lanes=clientIssueLanes(issue);
    const client=sopPane(issue,{issue,run:null,requirement:null});
    return JSON.stringify({kiro,mixed,lanes,client});
  })()`);
  const { kiro, mixed, lanes, client } = JSON.parse(out);
  assert.match(kiro, /整体 2 次执行 · 12\.34 credits · 周期/);
  assert.match(kiro, /2 次 · 12\.34 credits/);
  assert.doesNotMatch(kiro, /0 tokens/);
  assert.match(mixed, /整体 2 次执行 · 2\.4k tokens · 0\.50 credits · 周期/);
  assert.match(mixed, /2 次 · 2\.4k tokens · 0\.50 credits/);
  assert.equal(lanes[0].credits, 3.75);
  assert.equal(lanes[0].tokens, 500);
  assert.match(client, /整体 0 次执行 · 3\.75 credits · 周期/);
  assert.match(client, /2 次 · 500 tokens · 3\.75 credits/);
});

const creditsAnalytics = `{
  summary:{totalRuns:2,succeededRuns:2,totalTokens:3000,inputTokens:2000,outputTokens:1000,totalCostCny:1.5,costUnknownRuns:0,totalCredits:60.27,conversationCredits:5,conversationWorkspaces:1},
  trend:[{date:'2026-09',tokens:1000,runs:1,costCny:0.5,succeeded:1,credits:10},{date:'2026-10',tokens:2000,runs:1,costCny:1,succeeded:1,credits:50.27,conversationCredits:5}],
  runTraces:[{runId:'t1',status:'succeeded',tokens:2000,costCny:1,credits:45.27,createdAt:'2026-10-05T00:00:00Z',finishedAt:'2026-10-05T01:00:00Z'},
             {runId:'t0',status:'succeeded',tokens:1000,costCny:0.5,credits:10,createdAt:'2026-09-05T00:00:00Z',finishedAt:'2026-09-05T01:00:00Z'}],
  workflows:[{key:'wf-kiro',name:'Kiro SOP',runs:1,tokens:0,costCny:0,credits:44.77},{key:'wf-mix',name:'混合 SOP',runs:1,tokens:3000,costCny:1.5,credits:10.5}],
  nodes:[{key:'wf-kiro/dev',name:'开发',runs:1,tokens:0,credits:44.77},{key:'wf-mix/plan',name:'规划',runs:1,tokens:3000},{key:'wf-mix/dev',name:'实现',runs:1,tokens:0,credits:10.5}],
  projectUsage:[{projectId:'project-1',runs:2,tokens:3000,credits:55.27},{projectId:'p2',runs:1,tokens:0,credits:5}],
  activity:{sessions:1,sessionUsage:{tokens:0,credits:1.5,reportedReplies:1},collaborativeUsage:{tokens:10,credits:0.25,reportedReplies:1}},activityTrend:[]
}`;

test('insights show a separate credits card with period value, delta, trend and breakdowns', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const out = JSON.parse(app.evaluate(`(() => {
    obsEChartsFailed=true;
    state.analytics=${creditsAnalytics};
    const res={};
    state.obsTrendGrain='all';state.obsPeriodKey='';
    res.allView=obsScopedView();res.allKpis=obsUsageKpis(res.allView);
    state.obsTrendGrain='month';state.obsPeriodKey='2026-10';
    res.octView=obsScopedView();res.octKpis=obsUsageKpis(res.octView);
    state.obsPeriodKey='2026-09';
    res.sepView=obsScopedView();
    state.obsPeriodKey='2026-10';
    res.trend=obsTrendCard();
    setObsTrendMetric('credits');res.metric=obsTrendMetric();
    res.trendValue=obsTrendValue(state.analytics.trend[1],'credits');res.trendText=obsFormatTrendValue(50.27,'credits');
    res.chartValue=obsTrendModeValue(state.analytics.trend[1],'credits');
    res.overview=obsOverview();
    res.dims={};
    for(const dim of ['sop','node','project']){state.obsCreditDim=dim;res.dims[dim]=obsCreditRows(dim)}
    res.card=obsCreditsBreakdownCard();
    res.pie=obsBreakdownOption(res.dims.sop,'credits',obsChartFormatters(undefined,true)).series[0].data.map(d=>[d.name,d.value]);
    res.detail=obsUsageDetailGroups(res.octView).map(g=>g.items.map(i=>(i.tables||[]).map(t=>t.headers)));
    res.session=obsActivityUsageCard(state.analytics.activity,false);
    res.collab=obsActivityUsageCard(state.analytics.activity,true);
    res.report=buildObsReportHTML();
    return JSON.stringify(res);
  })()`));
  // Period values come from the selected bucket, not the window total.
  assert.equal(out.allView.credits, 60.27);
  assert.equal(out.octView.credits, 50.27);
  assert.equal(out.sepView.credits, 10);
  assert.match(out.allKpis, /累计 Credits/);
  assert.match(out.allKpis, /60\.27/);
  assert.match(out.allKpis, /订阅计量 · 不折算金额/);
  assert.match(out.octKpis, /10月 Credits<\/div><div class="obs-kpi-value"><b class="num" data-obs-count>50\.27<\/b><span class="obs-dl bad">↑403%<\/span>/);
  assert.match(out.octKpis, /50\.27/);
  assert.match(out.octKpis, /累计 60\.27/);
    assert.equal(out.metric, 'credits');
  assert.equal(out.trendValue, 50.27);
  assert.equal(out.chartValue, 50.27);
  assert.equal(out.trendText, '50.27');
  assert.match(out.trend, /data-obs-metric="credits"/);
  assert.match(out.overview, /data-obs-card="credits"/);
  assert.match(out.overview, /Credits 去向/);
  assert.deepEqual(out.dims.sop.map(r => [r.name, r.credits]), [['Kiro SOP', 44.77], ['混合 SOP', 10.5], ['对话改代码', 5]]);
  assert.deepEqual(out.dims.node.map(r => [r.name, r.credits]), [['开发', 44.77], ['实现', 10.5], ['对话改代码', 5]]);
  assert.deepEqual(out.dims.project.map(r => r.credits), [55.27, 5]);
  assert.match(out.card, /60\.27/);
  assert.deepEqual(out.pie, [['Kiro SOP', 44.77], ['混合 SOP', 10.5], ['对话改代码', 5]]);
  const headers = JSON.stringify(out.detail);
  assert.match(headers, /"Tokens","Credits","输入 \/ 输出"/);
  assert.match(headers, /"Tokens","Credits","工具调用"/);
  assert.equal(out.session.value, '1.50 credits');
  assert.match(out.collab.foot, /0\.25 credits/);
  assert.match(out.report, /<span>Credits<\/span><b>60\.27<\/b>/);
});

test('credits never enter cost, cost trend or cost shares', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const out = JSON.parse(app.evaluate(`(() => {
    obsEChartsFailed=true;
    const strip=a=>JSON.parse(JSON.stringify(a,(k,v)=>/^(credits|totalCredits|conversationCredits)$/.test(k)?undefined:v));
    const withCredits=${creditsAnalytics};
    const res={};
    for(const [name,data] of [['with',withCredits],['without',strip(withCredits)]]){
      state.analytics=data;state.obsTrendGrain='month';state.obsPeriodKey='2026-10';
      const view=obsScopedView();
      res[name]={cost:view.cost,costValues:state.analytics.trend.map(p=>obsTrendValue(p,'cost')),
        shares:obsBreakdownRows('sop','cost').map(r=>[r.name,obsBreakdownValue(r,'cost')]),kpis:obsUsageKpis(view)};
    }
    return JSON.stringify(res);
  })()`));
  assert.equal(out.with.cost, out.without.cost);
  assert.deepEqual(out.with.costValues, out.without.costValues);
  assert.deepEqual(out.with.shares, out.without.shares);
  const costCard = html => html.match(/10月成本<\/div>[\s\S]*?环比对象/)?.[0] || '';
  assert.notEqual(costCard(out.with.kpis), '');
  assert.equal(costCard(out.with.kpis), costCard(out.without.kpis));
  assert.doesNotMatch(out.with.kpis, /¥50\.27|¥60\.27/);
  assert.doesNotMatch(out.without.kpis, /Credits/);
});
