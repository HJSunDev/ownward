import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {stepIndex, typeaheadIndex} from './static/ui.js';

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
