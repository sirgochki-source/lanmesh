import { test } from 'node:test';
import assert from 'node:assert/strict';
import { esc, dispName } from '../../web/lib/sanitize.js';

test('esc экранирует html-мета', () => {
  assert.equal(esc(`<b>&"'`), '&lt;b&gt;&amp;&quot;&#39;');
});
test('dispName вычищает bidi-override и zero-width, затем экранирует', () => {
  assert.equal(dispName('a\u202Eb'), 'ab');          // bidi-override удалён
  assert.equal(dispName('x\u200By'), 'xy');          // zero-width удалён
  assert.equal(dispName('<i>'), '&lt;i&gt;');        // html экранирован
});
