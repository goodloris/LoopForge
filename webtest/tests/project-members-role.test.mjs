import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

async function wizardAtMembers(t) {
  const app = await loadApp();
  t.after(app.close);
  app.router.post('/api/projects', (call) => ({ ...call.body, id: call.body.id, key: call.body.key }));
  app.router.put('/api/projects', (call) => call.body);
  app.evaluate(`
    state.currentUser={id:1,loginName:'owner',chineseName:'Owner'};
    state.data.users=[state.currentUser,{id:2,loginName:'chenxi',chineseName:'陈曦'},{id:3,loginName:'lixiang',chineseName:'李响'}];
    state.data.workflows.push({id:'generic-backend-delivery',name:'通用后端交付 SOP',generic:true});
    state.data.mcpServers=[];state.data.syncConfig={};
    openProjectWizard();
  `);
  const host = () => app.document.querySelector('#projectWizard');
  const name = host().querySelector('[name="name"]');
  name.value = 'Shop';
  name.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  host().querySelector('[data-pw-action="next"]').click();
  await settle(8);
  await app.evaluate(`projectWizardNext()`);
  assert.equal(host().querySelector('.pw-dialog').dataset.pwStep, 'members');
  return { app, host };
}

// D9: Escape inside an open member picker closes only that picker.
test('Escape in the wizard member picker closes the picker, not the wizard', async (t) => {
  const { app, host } = await wizardAtMembers(t);
  host().querySelector('[data-pw-action="add-member"]').click();
  await settle(4);
  const box = host().querySelector('[data-pw-member] [data-user-combobox]');
  assert.ok(box.classList.contains('is-open'), 'adding a member opens its picker');
  box.querySelector('[data-user-combobox-search]').dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
  await settle(4);
  assert.equal(box.classList.contains('is-open'), false);
  assert.equal(app.document.querySelector('#confirmModal').classList.contains('open'), false, 'no discard prompt');
  assert.ok(host().querySelector('.pw-dialog'), 'wizard stays open');
  assert.equal(app.document.activeElement, box.querySelector('[data-user-combobox-trigger]'), 'focus returns to the picker');

  // Focus outside the panel (e.g. after clicking its padding) must not leave the picker open.
  box.querySelector('[data-user-combobox-trigger]').click();
  assert.ok(box.classList.contains('is-open'));
  app.document.activeElement.blur();
  app.document.body.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
  await settle(4);
  assert.equal(box.classList.contains('is-open'), false);
  assert.equal(app.document.querySelector('#confirmModal').classList.contains('open'), false);
  assert.ok(host().querySelector('.pw-dialog'));
});

// D8: the wizard and project settings let owners choose a member's role.
test('wizard saves the chosen member role', async (t) => {
  const { app, host } = await wizardAtMembers(t);
  assert.match(host().textContent, /查看者只能查看/);
  host().querySelector('[data-pw-action="add-member"]').click();
  await settle(4);
  const row = host().querySelector('[data-pw-member]');
  row.querySelector('[data-user-option="2"]').click();
  const role = host().querySelector('[data-pw-member] select.member-role');
  assert.deepEqual([...role.options].map(o => o.value), ['member', 'maintainer', 'viewer']);
  role.value = 'viewer';
  role.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  await app.evaluate(`projectWizardNext()`);
  await settle(6);
  const put = app.router.calls.filter(c => c.method === 'PUT' && c.path === '/api/projects').pop();
  assert.equal(JSON.stringify(put.body.members), JSON.stringify([{ userId: 2, role: 'viewer' }]));
});

test('project settings keep and change member roles', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.router.put('/api/projects', (call) => call.body);
  app.evaluate(`
    state.currentUser={id:1,loginName:'owner'};
    state.data.users=[state.currentUser,{id:2,loginName:'chenxi'},{id:3,loginName:'lixiang'}];
    state.data.workflows.push({id:'generic-backend-delivery',name:'Generic',generic:true});
    state.data.projects=[{id:'shop',key:'SHOP',name:'Shop',ownerId:1,workflowId:'generic-backend-delivery',
      members:[{userId:2,role:'viewer'},{userId:3,role:'member'}]}];
    state.projectAdminDetailId='shop';state.projectAdminTab='members';state.projectDraft=null;
    renderProjectDetail({force:true});
  `);
  const form = app.document.querySelector('#projectAdminForm');
  assert.match(form.textContent, /查看者只能查看/);
  const roles = [...form.querySelectorAll('#memberRows select.member-role')];
  assert.deepEqual(roles.map(s => s.value), ['viewer', 'member']);
  roles[1].value = 'maintainer';
  roles[1].dispatchEvent(new app.window.Event('change', { bubbles: true }));
  form.dispatchEvent(new app.window.Event('submit', { bubbles: true, cancelable: true }));
  await settle(8);
  const put = app.router.calls.find(c => c.method === 'PUT' && c.path === '/api/projects');
  assert.equal(JSON.stringify(put.body.members), JSON.stringify([{ userId: 2, role: 'viewer' }, { userId: 3, role: 'maintainer' }]));
});
