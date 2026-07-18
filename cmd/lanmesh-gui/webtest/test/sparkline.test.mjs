import { test } from 'node:test';
import assert from 'node:assert/strict';
import { scalePoints, sparklineSVG } from '../../web/lib/sparkline.js';

test('scalePoints кладёт точки в границы', () => {
  const pts = scalePoints([10, 20, 30], 40, 16);
  assert.equal(pts.length, 3);
  assert.equal(pts[0].x, 0);
  assert.equal(pts[2].x, 40);
  for (const p of pts) { assert.ok(p.y >= 0 && p.y <= 16); }
});

test('sparklineSVG возвращает svg с polyline', () => {
  const s = sparklineSVG([10, 20, 15], { w: 40, h: 16, stroke: '#46e6c0' });
  assert.match(s, /<svg[^>]*width="40"/);
  assert.match(s, /<polyline[^>]*points="/);
  assert.match(s, /#46e6c0/);
});

test('пустая строка при <2 точек', () => {
  assert.equal(sparklineSVG([5], { w: 40, h: 16, stroke: '#000' }), '');
});

test('scalePoints с константными значениями не производит NaN', () => {
  const pts = scalePoints([5, 5, 5], 40, 16);
  assert.equal(pts.length, 3);
  for (const p of pts) {
    assert.ok(Number.isFinite(p.y), `y должен быть конечным числом, получен ${p.y}`);
    assert.ok(p.y >= 0 && p.y <= 16, `y должен быть в [0, 16], получен ${p.y}`);
  }
});

test('scalePoints с одним значением', () => {
  const pts = scalePoints([10], 40, 16);
  assert.equal(pts.length, 1);
  assert.equal(pts[0].x, 0);
  assert.ok(Number.isFinite(pts[0].y), `y должен быть конечным числом, получен ${pts[0].y}`);
  assert.ok(pts[0].y >= 0 && pts[0].y <= 16, `y должен быть в [0, 16], получен ${pts[0].y}`);
});
