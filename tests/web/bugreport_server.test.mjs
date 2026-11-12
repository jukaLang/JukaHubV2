import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createBugReportServer } from '../../server/bug-report-server.mjs';

async function runningServer(t, collector) {
  const server = createBugReportServer(collector);
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  t.after(() => new Promise(resolve => server.close(resolve)));
  return `http://127.0.0.1:${server.address().port}`;
}

test('the self-hosted listener passes a report and socket address to the collector', async t => {
  let received;
  const base = await runningServer(t, async (request, context) => {
    received = {
      method: request.method,
      body: await request.text(),
      ip: context.ip,
      agent: request.headers.get('user-agent'),
    };
    return Response.json({ issue: 42 }, { status: 201 });
  });
  const response = await fetch(`${base}/api/bug-report`, {
    method: 'POST', headers: { 'user-agent': 'JukaHub-BugReport/test' }, body: '{"id":"JH-TEST01"}',
  });
  assert.equal(response.status, 201);
  assert.deepEqual(await response.json(), { issue: 42 });
  assert.equal(response.headers.get('cache-control'), 'no-store');
  assert.deepEqual(received, {
    method: 'POST', body: '{"id":"JH-TEST01"}', ip: '127.0.0.1', agent: 'JukaHub-BugReport/test',
  });
  await fetch(`${base}/api/bug-report`, {
    method: 'POST', headers: { 'x-real-ip': '198.51.100.7' }, body: '{}',
  });
  assert.equal(received.ip, '198.51.100.7');
  const other = await fetch(`${base}/other`);
  assert.equal(other.status, 404);
});

test('the self-hosted listener rejects an oversized request before the collector', async t => {
  let calls = 0;
  const base = await runningServer(t, async () => {
    calls++;
    return Response.json({ issue: 42 });
  });
  const response = await fetch(`${base}/api/bug-report`, { method: 'POST', body: 'x'.repeat(256 * 1024 + 1) });
  assert.equal(response.status, 413);
  assert.equal(calls, 0);
});
