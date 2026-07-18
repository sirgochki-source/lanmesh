import { test } from 'node:test';
import assert from 'node:assert/strict';
import { makeServer } from '../mock-server.mjs';

test('mock отдаёт /api/state и переключает сценарии', async () => {
  const srv = makeServer().listen(0);
  await new Promise(r => srv.once('listening', r));
  const port = srv.address().port;
  const base = `http://127.0.0.1:${port}`;
  const st = await (await fetch(`${base}/api/state`)).json();
  assert.equal(st.running, true);
  assert.ok(Array.isArray(st.networks));
  await fetch(`${base}/__scenario?name=disconnected`);
  const st2 = await (await fetch(`${base}/api/state`)).json();
  assert.equal(st2.running, false);
  await new Promise(r => srv.close(r));
});
