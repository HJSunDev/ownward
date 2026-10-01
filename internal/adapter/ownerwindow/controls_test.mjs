import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {stepIndex, typeaheadIndex, selectPlacement, createPageNavigation} from './static/ui.js';

test('paged views reject late responses and refresh the newest requested position',async()=>{
  const pending=[],shown=[];
  const pages=createPageNavigation(after=>new Promise(resolve=>pending.push({after,resolve})),(page,after)=>shown.push([page,after]));
  const first=pages.load('one'),second=pages.load('two'),refresh=pages.load();
  assert.deepEqual(pending.map(p=>p.after),['one','two','two']);
  pending[2].resolve('fresh');await refresh;
  pending[0].resolve('old');pending[1].resolve('old');await Promise.all([first,second]);
  assert.deepEqual(shown,[['fresh','two']]);
  const closing=pages.load();pages.cancel();pending[3].resolve('closed');await closing;
  assert.equal(shown.length,1);
});

test('a rejected list position recovers once; failures and stale views cannot loop',async()=>{
  const calls=[],shown=[];let live=true;
  const pages=createPageNavigation(async after=>{calls.push(after);if(after)throw Object.assign(new Error('expired'),{status:409});return 'current';},p=>shown.push(p),()=>live);
  await pages.load('expired');assert.deepEqual(calls,['expired','']);assert.deepEqual(shown,['current']);
  live=false;await pages.load('expired');assert.equal(shown.length,1);
});

// v3 control-layer acceptance floor: each control keeps one mechanical assertion.
// The DOM combobox behaviour is exercised in a real browser by the acceptance
// run; here we assert the pure keyboard model plus the CSS contract tokens.

test('Select: stepIndex wraps and clamps', () => {
  assert.equal(stepIndex(3, 2, 1), 0);
  assert.equal(stepIndex(3, 0, -1), 2);
  assert.equal(stepIndex(3, 1, 1, false), 2);
  assert.equal(stepIndex(3, 2, 1, false), 2);
  assert.equal(stepIndex(0, 0, 1), -1);
});

test('Select: typeahead matches cyclically and ignores misses', () => {
  assert.equal(typeaheadIndex(['Alpha', 'Beta', 'Gamma'], 'g'), 2);
  assert.equal(typeaheadIndex(['Alpha', 'Beta', 'Gamma'], 'b', 2), 1);
  assert.equal(typeaheadIndex(['Alpha', 'Beta'], 'z'), -1);
});

test('Select: popup stays within its scroll container, flips when needed and bounds long lists',()=>{
  assert.deepEqual(selectPlacement({top:100,bottom:140},{top:0,bottom:700},180),{upward:false,maxHeight:180});
  assert.deepEqual(selectPlacement({top:340,bottom:380},{top:80,bottom:430},180),{upward:true,maxHeight:180});
  assert.deepEqual(selectPlacement({top:220,bottom:260},{top:120,bottom:360},320),{upward:false,maxHeight:96});
});

test('window.css: spacing/type tokens and the control contract exist', async () => {
  const css = await readFile(new URL('./static/window.css', import.meta.url), 'utf8');
  for (const token of ['--space-4:', '--space-8:', '--gutter-page:', '--font-body:', '--font-title:']) {
    assert.ok(css.includes(token), `missing token ${token}`);
  }
  assert.ok(css.includes('.button.align-start'), 'button alignment context variant');
  assert.ok(css.includes('.button.compact'), 'button compact variant');
  assert.ok(css.includes('.select-trigger'), 'self-drawn combobox trigger');
  assert.ok(css.includes('.select-option'), 'self-drawn combobox options');
});

test('window.css: motion acceptance floor (reduced-motion, no infinite animation)', async () => {
  const css = await readFile(new URL('./static/window.css', import.meta.url), 'utf8');
  assert.ok(css.includes('prefers-reduced-motion'), 'reduced-motion rule');
  assert.ok(!/animation:[^;}]*\binfinite\b/.test(css), 'no infinite animation');
});

test('window.css: pending entry is a canvas-top strip with an immersive hairline', async () => {
  const css = await readFile(new URL('./static/window.css', import.meta.url), 'utf8');
  assert.ok(/\.pending-strip\{[^}]*width:100%/.test(css), 'pending strip spans the canvas');
  assert.ok(/\.immersive \.pending-strip\{[^}]*height:2px/.test(css), 'immersive hairline');
  assert.ok(/\.immersive \.pending-strip::after\{[^}]*height:40px/.test(css), 'hairline hit area is 40px');
});
