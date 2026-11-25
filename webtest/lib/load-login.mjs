import { readFile } from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import { JSDOM } from 'jsdom';
import { WEB_ROOT, createFetchRouter, settle } from './load-app.mjs';

// loadLogin renders web/login.html with i18n-en.js, i18n.js and login.js, the
// same scripts the page loads, against a mocked /api/auth/setup.
export async function loadLogin({ setup = {}, hash = '', search = '', lang = 'zh' } = {}) {
  let html = await readFile(path.join(WEB_ROOT, 'login.html'), 'utf8');
  if (lang === 'en') html = html.replace('<html', '<html data-lang="en"');
  const dom = new JSDOM(html, {
    runScripts: 'outside-only',
    pretendToBeVisual: true,
    url: `http://127.0.0.1/login.html${search}${hash ? `#${hash.replace(/^#/, '')}` : ''}`,
  });
  const { window } = dom;
  const router = createFetchRouter([{ path: '/api/auth/setup', body: setup }]);
  window.fetch = router.fetch;
  window.matchMedia = (query) => ({ matches: false, media: query, addListener() {}, removeListener() {},
    addEventListener() {}, removeEventListener() {} });
  const context = dom.getInternalVMContext();
  for (const file of ['i18n-en.js', 'i18n.js', 'login.js']) {
    new vm.Script(await readFile(path.join(WEB_ROOT, file), 'utf8'), { filename: file }).runInContext(context);
  }
  await settle(6);
  const $ = (id) => window.document.getElementById(id);
  return { window, document: window.document, router, $, close: () => window.close() };
}
