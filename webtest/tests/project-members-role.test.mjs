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

