import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

// D17: a project SOP card's ⋯ menu copies the SOP through the clone API.
test('project SOP ⋯ menu copies the SOP', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.post('/api/workflows/flow-1/clone', (call) => ({ ...call.body, nodes: [], edges: [], version: 1 }));
  app.evaluate(`
    state.currentUser={id:1,loginName:'owner',isPlatformAdmin:true};
    state.projectFilter='project-1';state.sopScope='project';
    afPrompt=async({defaultValue}={})=>defaultValue;
    renderSOPCatalog();
  `);
  app.document.querySelector('[data-sop-menu="flow-1"]').click();
  const copy = app.document.querySelector('#sopActionPopover [data-sop-action="copy"]');
  assert.ok(copy, 'the ⋯ menu offers 复制');
  assert.match(copy.textContent, /复制/);
  copy.click();
  await settle(8);
  const call = app.router.calls.find(c => c.method === 'POST' && c.path === '/api/workflows/flow-1/clone');
  assert.ok(call, 'clone API called');
  assert.equal(call.body.projectId, 'project-1');
  assert.match(call.body.name, /副本/);
});
