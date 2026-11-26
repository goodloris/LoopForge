const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// The preview contract helpers sit at the top of flows.js, before flowReadOnly.
const code = fs.readFileSync(path.join(__dirname, 'app/flows.js'), 'utf8');
const head = code.slice(0, code.indexOf('function flowReadOnly('));
const context = {state: {data: {}, selectedNode: ''}, executorModelCatalogs: () => ({}), escapeHTML: String};
vm.createContext(context);
vm.runInContext(head + '\nthis.api={previewDeployParallelAgents,workflowPreviewKind,setWorkflowPreviewKind,ensurePreviewContract,syncPreviewBuildGraph,isPreviewBuildNode};', context);
const api = context.api;

function placed(kind) {
  // A → preview → B with a user-chosen position.
  const preview = kind === 'compose'
    ? {id: 'af-preview-deploy', type: 'script', position: {x: 400, y: 50}}
    : {id: 'af-preview-build', type: 'agent', position: {x: 400, y: 50}};
  return {
    preview: kind === 'compose' ? {enabled: true, kind: 'compose', composeFile: 'deploy/p.yaml'} : {enabled: true, kind: 'static'},
    nodes: [{id: 'a', position: {x: 0, y: 0}}, preview, {id: 'b', position: {x: 800, y: 0}, rejectTo: preview.id}],
    edges: [{id: 'e1', source: 'a', target: preview.id}, {id: 'e2', source: preview.id, target: 'b'}]
  };
}

// ensurePreviewContract keeps a compose kind and its compose file.
let wf = placed('compose');
context.state.workflow = wf;
api.ensurePreviewContract();
assert.equal(wf.preview.kind, 'compose');
assert.equal(wf.preview.composeFile, 'deploy/p.yaml');
assert.equal(wf.preview.sourcePath, undefined, 'compose does not gain static defaults');

// Static → compose swaps the node in place: position, both edges and reject routes kept.
wf = placed('static');
context.state.workflow = wf;
context.state.selectedNode = 'af-preview-build';
api.setWorkflowPreviewKind(wf, 'compose');
assert.equal(wf.preview.kind, 'compose');
assert.equal(wf.preview.composeFile, '.loopforge/preview/compose.yaml');
const deploy = wf.nodes.find(n => n.id === 'af-preview-deploy');
assert.ok(deploy && deploy.type === 'script' && deploy.script, 'deploy node is a valid script node');
assert.deepEqual(JSON.parse(JSON.stringify(deploy.position)), {x: 400, y: 50});
assert.ok(!wf.nodes.some(n => n.id === 'af-preview-build'));
assert.deepEqual(wf.edges.map(e => `${e.source}>${e.target}`), ['a>af-preview-deploy', 'af-preview-deploy>b']);
assert.equal(wf.nodes.find(n => n.id === 'b').rejectTo, 'af-preview-deploy');
assert.equal(context.state.selectedNode, 'af-preview-deploy');

// And back: static defaults return, composeFile is dropped, B stays connected.
api.setWorkflowPreviewKind(wf, 'static');
assert.equal(wf.preview.kind, 'static');
assert.equal(wf.preview.sourcePath, 'dist');
assert.equal('composeFile' in wf.preview, false);
assert.deepEqual(wf.edges.map(e => `${e.source}>${e.target}`), ['a>af-preview-build', 'af-preview-build>b']);

// A missing node is appended after the sinks with the kind's node.
wf = {preview: {enabled: true, kind: 'compose'}, nodes: [{id: 'a', position: {x: 0, y: 0}}], edges: []};
context.state.workflow = wf;
api.syncPreviewBuildGraph();
assert.equal(wf.nodes[1].id, 'af-preview-deploy');
assert.deepEqual(wf.edges.map(e => `${e.id}:${e.source}>${e.target}`), ['af-preview-deploy-edge-1:a>af-preview-deploy']);
assert.equal(api.workflowPreviewKind({preview: {enabled: true}}), 'static');
assert.ok(api.isPreviewBuildNode('af-preview-deploy') && api.isPreviewBuildNode('af-preview-build'));

// Designer diagnostics: agents beside the deploy node (neither up- nor downstream) are flagged.
const graphNodes = [{id: 'plan', type: 'agent'}, {id: 'fe', type: 'agent'}, {id: 'be', type: 'agent'}, {id: 'af-preview-deploy', type: 'script'}, {id: 'review', type: 'agent'}];
const joined = [{source: 'plan', target: 'fe'}, {source: 'plan', target: 'be'}, {source: 'fe', target: 'af-preview-deploy'}, {source: 'be', target: 'af-preview-deploy'}, {source: 'af-preview-deploy', target: 'review'}];
assert.deepEqual(api.previewDeployParallelAgents(graphNodes, joined, 'af-preview-deploy').map(n => n.id), []);
const forked = joined.filter(e => !(e.source === 'be' && e.target === 'af-preview-deploy'));
assert.deepEqual(api.previewDeployParallelAgents(graphNodes, forked, 'af-preview-deploy').map(n => n.id), ['be']);
