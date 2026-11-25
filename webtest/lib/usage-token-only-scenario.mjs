// Token-only (CodeBuddy / Codex) usage rendered on every page that now also
// shows Kiro CLI credits. tests/fixtures/usage-token-only-golden.json holds
// the output of this scenario on main before credits were added; the
// credits change must keep it byte-for-byte identical.
export const TOKEN_ONLY_SCENARIO = `(() => {
  obsEChartsFailed=true;
  const res={};
  res.live=liveUsageHTML({id:'r',usage:{totalTokens:2400,inputTokens:1600,outputTokens:800,cacheReadTokens:100,cacheWriteTokens:0,costUsd:0.24},toolCalls:3});
  res.liveEmpty=liveUsageHTML({usage:{},toolCalls:0});
  state.data.workflows=[{id:'wf',name:'交付',nodes:[{id:'n1',name:'实现'}],edges:[]}];
  const issue={id:'i1',projectId:'project-1',kind:'requirement',source:'local',status:'in_progress',title:'需求',workflowId:'wf'};
  state.data.issues=[issue];state.data.runs=[];state.data.tasks=[];
  const snap={issue,overall:{runCount:2,tokens:2400,cycleDurationMs:0,status:'active'},lanes:[{lane:'backend',label:'交付',workflowId:'wf',status:'succeeded',started:true,runCount:2,tokens:2400}]};
  res.sop=sopPane(issue,{issue,run:null,requirement:snap});
  state.data.runs=[{id:'run-a',workflowId:'wf',status:'succeeded',createdAt:'2026-10-01',executionContext:{issueId:'i1'},workflowSnapshot:state.data.workflows[0],usage:{totalTokens:500},nodes:{}}];
  res.lanes=JSON.stringify(clientIssueLanes(issue));
  res.sopClient=sopPane(issue,{issue,run:null,requirement:null});
  state.analytics={summary:{totalRuns:2,succeededRuns:2,totalTokens:3000,inputTokens:2000,outputTokens:1000,totalCostCny:1.5,costUnknownRuns:0,conversationWorkspaces:1,conversationTokens:500,conversationCostCny:0.2},
    trend:[{date:'2026-09',tokens:1000,runs:1,costCny:0.5,succeeded:1},{date:'2026-10',tokens:2000,runs:1,costCny:1,succeeded:1,conversationTokens:500}],
    runTraces:[{runId:'t1',status:'succeeded',tokens:2000,costCny:1,createdAt:'2026-10-05T00:00:00Z',finishedAt:'2026-10-05T01:00:00Z'}],
    workflows:[{key:'wf',name:'交付',runs:2,tokens:3000,costCny:1.5}],nodes:[{key:'wf/n1',name:'实现',runs:2,tokens:3000}],projectUsage:[{projectId:'project-1',runs:2,tokens:3000}],
    activity:{sessions:1,sessionUsage:{tokens:10,reportedReplies:1},collaborativeUsage:{}},activityTrend:[]};
  for(const g of ['all','month']){
    state.obsTrendGrain=g;state.obsPeriodKey=g==='month'?'2026-10':'';
    const v=obsScopedView();
    res['kpis_'+g]=obsUsageKpis(v);
    res['trend_'+g]=obsTrendCard();
    res['overview_'+g]=obsOverview();
    res['detail_'+g]=JSON.stringify(obsUsageDetailGroups(v).map(gr=>gr.items.map(i=>(i.tables||[]).map(t=>[t.headers,t.rows]))));
    res['activity_'+g]=obsActivityKpis();
  }
  state.obsScope='user';state.obsUserFilter='1';state.obsCrossProject={projectUsage:[{projectId:'project-1',runs:2,tokens:3000},{projectId:'p2',runs:1,tokens:5}]};
  res.cross=JSON.stringify(obsCrossProjectTable());
  res.report=buildObsReportHTML().replace(/生成于 [^<]*/,'');
  return JSON.stringify(res);
})()`;
