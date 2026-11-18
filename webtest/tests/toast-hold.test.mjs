import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { loadApp } from '../lib/load-app.mjs';

// D18: a toast under a resting pointer still goes away, and toasts stay clear of the heading actions.
test('hovered toasts still close after the hold cap', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const result = app.evaluate(`(() => {
    const timers=[],real=window.setTimeout;
    window.setTimeout=(fn,ms)=>{timers.push({fn,ms});return timers.length};
    try{toast('已发布 v2','info')}finally{window.setTimeout=real}
    const item=$('#toastContainer .toast-item:last-child');
    item.dispatchEvent(new MouseEvent('mouseenter'));
    const cap=timers.find(x=>x.ms===2600+TOAST_MAX_HOLD_MS);
    if(cap)cap.fn();
    return {cap:!!cap,leaving:item.classList.contains('leaving')};
  })()`);
  assert.equal(result.cap, true, 'a hard stop is scheduled');
  assert.equal(result.leaving, true, 'the hard stop closes a hovered toast');

  const css = await readFile(new URL('../../web/preferences.css', import.meta.url), 'utf8');
  assert.match(css, /\.toast-container \{ top:64px;right:auto;left:50%;transform:translateX\(-50%\)/);
});
