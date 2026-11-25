import test from 'node:test';
import assert from 'node:assert/strict';
import { loadLogin } from '../lib/load-login.mjs';

test('password sign-up closed with Feishu available hides 注册 and keeps the Feishu button', async (t) => {
  const page = await loadLogin({ setup: { needsSetup: false, signupEnabled: true, passwordSignupEnabled: false, feishuEnabled: true } });
  t.after(page.close);
  assert.equal(page.$('loginSwitch').hidden, true, 'sign-up entry hidden');
  assert.equal(page.$('feishuBlock').hidden, false, 'Feishu entry shown');
  assert.equal(page.$('feishuLogin').getAttribute('href'), '/api/auth/feishu/start');
  assert.equal(page.$('setupNotice').hidden, true);
});

test('the Feishu button carries return_url while password sign-up is closed', async (t) => {
  const page = await loadLogin({
    search: '?return_url=%2Fprojects%2Fp1',
    setup: { needsSetup: false, signupEnabled: true, passwordSignupEnabled: false, feishuEnabled: true },
  });
  t.after(page.close);
  assert.equal(page.$('loginSwitch').hidden, true);
  assert.equal(page.$('feishuLogin').getAttribute('href'), '/api/auth/feishu/start?return_url=%2Fprojects%2Fp1');
});

test('#register does not open the sign-up form when password sign-up is closed', async (t) => {
  const page = await loadLogin({ hash: 'register', setup: { needsSetup: false, signupEnabled: true, passwordSignupEnabled: false, feishuEnabled: true } });
  t.after(page.close);
  assert.equal(page.$('confirmField').hidden, true, 'stays in sign-in mode');
  assert.equal(page.$('loginSubmit').textContent, '登录');
});

test('password sign-up open shows 注册 and #register opens the form', async (t) => {
  const page = await loadLogin({ hash: 'register', setup: { needsSetup: false, signupEnabled: true, passwordSignupEnabled: true, feishuEnabled: true } });
  t.after(page.close);
  assert.equal(page.$('loginSwitch').hidden, false);
  assert.equal(page.$('confirmField').hidden, false);
  assert.equal(page.$('feishuBlock').hidden, false);
});

test('an older server without passwordSignupEnabled falls back to signupEnabled', async (t) => {
  const open = await loadLogin({ setup: { needsSetup: false, signupEnabled: true, feishuEnabled: false } });
  t.after(open.close);
  assert.equal(open.$('loginSwitch').hidden, false);
  assert.equal(open.$('feishuBlock').hidden, true);
  const closed = await loadLogin({ setup: { needsSetup: false, signupEnabled: false, feishuEnabled: false } });
  t.after(closed.close);
  assert.equal(closed.$('loginSwitch').hidden, true);
  assert.equal(closed.$('loginContact').hidden, false);
});

test('a fresh deployment asks for the administrator account', async (t) => {
  const page = await loadLogin({ setup: { needsSetup: true, signupEnabled: true, passwordSignupEnabled: true, feishuEnabled: false } });
  t.after(page.close);
  assert.equal(page.$('setupNotice').hidden, false);
  assert.equal(page.$('confirmField').hidden, false);
  assert.equal(page.$('loginSwitch').hidden, true);
});

test('English mode labels the Feishu button while password sign-up is closed', async (t) => {
  const page = await loadLogin({ lang: 'en', setup: { needsSetup: false, signupEnabled: true, passwordSignupEnabled: false, feishuEnabled: true } });
  t.after(page.close);
  assert.equal(page.$('feishuLogin').textContent, 'Sign in with Feishu');
  assert.equal(page.$('loginSwitch').hidden, true);
});
