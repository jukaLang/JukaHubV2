// bugreport_function.test.mjs — the bug-report collector (netlify/functions/bug-report.mjs)
// without Netlify or GitHub: fetch is a recorder that answers from a script, and the clock and
// the environment are injected through createHandler.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import handler, {
  config, createHandler, createLimiter, validateReport, scrub, escapeMarkdown, buildIssue, issueTitle,
} from '../../netlify/functions/bug-report.mjs';

const ENDPOINT = 'https://generate.jukalang.com/api/bug-report';
const ENV = { JUKAHUB_GITHUB_TOKEN: 'ghp_test_token', JUKAHUB_ISSUES_REPO: 'jukaLang/JukaHubV2' };
const ISSUE_URL = 'https://github.com/jukaLang/JukaHubV2/issues/42';

function report(over = {}) {
  return {
    schema: 1, kind: 'bug_report', app: 'JukaHub', id: 'JH-7K2Q9M', category: 'media',
    title: 'Video freezes after resume', description: 'Paused a video, slept the device, woke it: frozen.',
    version: '2026-11-01', release: 'JukaHub-20261101000000', device: 'TrimUI Smart Pro', platform: 'TrimUI',
    os: 'linux', arch: 'arm64', created: '2026-11-10T12:00:00Z', ...over,
  };
}

function post(body, headers = {}) {
  return new Request(ENDPOINT, {
    method: 'POST',
    headers: { 'content-type': 'application/json', 'user-agent': 'JukaHub-BugReport/test', ...headers },
    body: typeof body === 'string' ? body : JSON.stringify(body),
  });
}

// collector builds a handler whose fetch answers from `script`, one entry per call: an Error is
// thrown, anything else becomes a JSON Response.
function collector(envOver = {}, script = []) {
  const calls = [];
  let clock = Date.parse('2026-11-10T12:00:00Z');
  const env = { ...ENV, ...envOver };
  const handle = createHandler({
    env: name => env[name],
    now: () => clock,
    fetch: async (url, init = {}) => {
      calls.push({ url: String(url), init });
      const next = script.shift();
      if (!next) throw new Error(`unexpected fetch ${url}`);
      if (next instanceof Error) throw next;
      const status = next.status ?? 200;
      const empty = status === 204 || status === 205 || status === 304;
      return new Response(empty ? null : JSON.stringify(next.body ?? {}), {
        status,
        headers: { 'content-type': 'application/json', ...(next.headers || {}) },
      });
    },
  });
  return { handle, calls, script, advance: ms => { clock += ms; } };
}

const NOT_FOUND = { status: 200, body: { total_count: 0, items: [] } };
const CREATED = { status: 201, body: { number: 42, html_url: ISSUE_URL } };
const at = ip => ({ ip });

test('the function is a Functions 2.0 handler on /api/bug-report', () => {
  assert.equal(typeof handler, 'function');
  assert.deepEqual(config, { path: '/api/bug-report' });
});

test('a valid report becomes an issue and the device gets its number', async () => {
  const c = collector({}, [NOT_FOUND, CREATED]);
  const resp = await c.handle(post(report()), at('203.0.113.9'));
  assert.equal(resp.status, 201);
  assert.deepEqual(await resp.json(), { id: 'JH-7K2Q9M', issue: 42, url: ISSUE_URL });

  assert.equal(c.calls.length, 2);
  assert.match(c.calls[0].url, /^https:\/\/api\.github\.com\/search\/issues\?q=/);
  assert.ok(decodeURIComponent(c.calls[0].url).includes('"JH-7K2Q9M" in:title is:issue repo:jukaLang/JukaHubV2'));
  const create = c.calls[1];
  assert.equal(create.url, 'https://api.github.com/repos/jukaLang/JukaHubV2/issues');
  assert.equal(create.init.method, 'POST');
  assert.equal(create.init.headers.Authorization, 'Bearer ghp_test_token');
  assert.equal(create.init.headers.Accept, 'application/vnd.github+json');
  assert.equal(create.init.headers['X-GitHub-Api-Version'], '2022-11-28');
  const sent = JSON.parse(create.init.body);
  assert.equal(sent.title, '[Bug] Video freezes after resume (JH-7K2Q9M)');
  assert.deepEqual(sent.labels, ['bug', 'from-device']);
  for (const want of ['frozen', '| Device | TrimUI Smart Pro |', '| Reference | JH-7K2Q9M |', '2026-11-01 (JukaHub-20261101000000)']) {
    assert.ok(sent.body.includes(want), `issue body lacks ${want}:\n${sent.body}`);
  }
});

test('a report the device would never send is refused without asking GitHub', async () => {
  const bad = [
    report({ kind: 'feedback' }),
    report({ schema: 2 }),
    report({ app: 'Other' }),
    report({ id: 'jh-123' }),
    report({ category: 'spam' }),
    report({ title: '', description: '  ' }),
    report({ title: 'x'.repeat(400) }),
    report({ log: 42 }),
    report({ device: undefined }),
    report({ created: 'yesterday' }),
    [report()],
  ];
  for (const body of bad) {
    const c = collector();
    const resp = await c.handle(post(body), at('203.0.113.9'));
    assert.equal(resp.status, 422, JSON.stringify(body).slice(0, 80));
    assert.equal(c.calls.length, 0);
  }
  const c = collector();
  assert.equal((await c.handle(post('{not json'), at('203.0.113.9'))).status, 400);
  assert.equal((await c.handle(post(report(), { 'user-agent': 'curl/8' }), at('203.0.113.9'))).status, 403);
  assert.equal((await c.handle(post(report(), { 'content-type': 'text/plain' }), at('203.0.113.9'))).status, 415);
  const huge = report({ description: 'y'.repeat(300 * 1024) });
  assert.equal((await c.handle(post(huge), at('203.0.113.9'))).status, 413);
  assert.equal((await c.handle(new Request(ENDPOINT), at('203.0.113.9'))).status, 405);
  assert.equal(c.calls.length, 0);
});

test('a full report written with \\u escapes still fits under the size cap', async () => {
  // An encoder may write every < of a log as a six-byte escape; the fields' own caps are what
  // bound the content, so such a report is filed rather than refused (and retired).
  const log = '<'.repeat(16 * 1024) + 'a'.repeat(32 * 1024);
  const text = JSON.stringify(report({ log })).replace(/</g, '\\u003c');
  assert.ok(text.length > 120 * 1024, String(text.length));
  const c = collector({}, [NOT_FOUND, CREATED]);
  assert.equal((await c.handle(post(text), at('203.0.113.9'))).status, 201);
});

test('without a token the service reports missing setup', async () => {
  for (const env of [{ JUKAHUB_GITHUB_TOKEN: '' }, { JUKAHUB_ISSUES_REPO: 'not a repo' }]) {
    const c = collector(env);
    const resp = await c.handle(post(report()), at('203.0.113.9'));
    assert.equal(resp.status, 503);
    assert.equal((await resp.json()).fallback, 'phone');
    assert.equal(c.calls.length, 0);
  }
  const get = await collector({ JUKAHUB_GITHUB_TOKEN: '' }).handle(new Request(ENDPOINT), at('1.2.3.4'));
  assert.equal(get.status, 405);
  assert.equal((await get.json()).configured, false);
});

test('a burst from one address is slowed down with Retry-After', async () => {
  const script = [];
  for (let i = 0; i < 5; i++) script.push(NOT_FOUND, { status: 201, body: { number: 100 + i, html_url: `${ISSUE_URL}0${i}` } });
  const c = collector({}, script);
  for (let i = 0; i < 5; i++) {
    const resp = await c.handle(post(report({ id: `JH-AAAAA${i}` })), at('198.51.100.7'));
    assert.equal(resp.status, 201);
  }
  const limited = await c.handle(post(report({ id: 'JH-BBBBBB' })), at('198.51.100.7'));
  assert.equal(limited.status, 429);
  assert.ok(Number(limited.headers.get('retry-after')) > 0);
  assert.equal(c.calls.length, 10, 'the limited report must not reach GitHub');

  // Another address is not affected, and the first one recovers after the window.
  c.script.push(NOT_FOUND, CREATED);
  assert.equal((await c.handle(post(report({ id: 'JH-CCCCCC' })), at('198.51.100.8'))).status, 201);
  c.advance(11 * 60 * 1000);
  c.script.push(NOT_FOUND, CREATED);
  assert.equal((await c.handle(post(report({ id: 'JH-DDDDDD' })), at('198.51.100.7'))).status, 201);
});

test('a retried report returns the issue it already became', async () => {
  const c = collector({}, [NOT_FOUND, CREATED]);
  assert.equal((await c.handle(post(report()), at('203.0.113.9'))).status, 201);
  const again = await c.handle(post(report()), at('203.0.113.9'));
  assert.equal(again.status, 200);
  assert.deepEqual(await again.json(), { id: 'JH-7K2Q9M', issue: 42, url: ISSUE_URL, duplicate: true });
  assert.equal(c.calls.length, 2);

  // A new instance finds it through the search instead.
  const filedIssue = {
    number: 7,
    html_url: `${ISSUE_URL}7`,
    title: '[Bug] Video freezes after resume (JH-7K2Q9M)',
    repository_url: 'https://api.github.com/repos/jukaLang/JukaHubV2',
    body: 'Paused a video…\n\n| Field | Value |\n|---|---|\n| Created | 2026-11-10T12:00:00Z |',
  };
  const fresh = collector({}, [{ status: 200, body: { total_count: 1, items: [filedIssue] } }]);
  const found = await fresh.handle(post(report()), at('203.0.113.9'));
  assert.equal(found.status, 200);
  assert.equal((await found.json()).issue, 7);
  assert.equal(fresh.calls.length, 1, 'a found report is not filed twice');
});

test('a report that only shares its six-letter reference is a new report', async () => {
  // Same reference, another creation time: someone else's report (or a new draft), not a retry.
  const c = collector({}, [NOT_FOUND, CREATED, NOT_FOUND, { status: 201, body: { number: 43, html_url: `${ISSUE_URL}3` } }]);
  assert.equal((await c.handle(post(report()), at('203.0.113.9'))).status, 201);
  const other = await c.handle(post(report({ created: '2026-11-10T12:30:00Z' })), at('203.0.113.9'));
  assert.equal(other.status, 201);
  assert.equal((await other.json()).issue, 43);

  // The search only counts an issue of this repository with this report's creation time.
  const lookalikes = [
    { number: 7, title: '[Bug] Other (JH-7K2Q9M)', repository_url: 'https://api.github.com/repos/jukaLang/JukaHubV2', body: '| Created | 2025-03-01T08:00:00Z |' },
    { number: 8, title: '[Bug] Same (JH-7K2Q9M)', repository_url: 'https://api.github.com/repos/someone/else', body: '| Created | 2026-11-10T12:00:00Z |' },
  ];
  const d = collector({}, [{ status: 200, body: { total_count: 2, items: lookalikes } }, CREATED]);
  const resp = await d.handle(post(report()), at('203.0.113.9'));
  assert.equal(resp.status, 201);
  assert.equal((await resp.json()).issue, 42);
});

test('a failed search never blocks the report', async () => {
  const c = collector({}, [new Error('search timed out'), CREATED]);
  assert.equal((await c.handle(post(report()), at('203.0.113.9'))).status, 201);
  const d = collector({}, [{ status: 403, body: {} }, CREATED]);
  assert.equal((await d.handle(post(report()), at('203.0.113.9'))).status, 201);
});

test('GitHub failures map to what the device should do', async () => {
  const cases = [
    { answer: { status: 500 }, want: 502 },
    { answer: new Error('connect ECONNREFUSED'), want: 502 },
    { answer: { status: 401 }, want: 503, fallback: 'phone' },
    { answer: { status: 404 }, want: 503, fallback: 'phone' },
    { answer: { status: 403, headers: { 'x-ratelimit-remaining': '0', 'x-ratelimit-reset': String(Date.parse('2026-11-10T12:05:00Z') / 1000) } }, want: 429 },
    { answer: { status: 403 }, want: 503, fallback: 'phone' },
  ];
  for (const { answer, want, fallback } of cases) {
    const c = collector({}, [NOT_FOUND, answer]);
    const resp = await c.handle(post(report()), at('203.0.113.9'));
    assert.equal(resp.status, want, JSON.stringify(answer));
    if (fallback) assert.equal((await resp.json()).fallback, fallback);
    if (want === 429) assert.ok(Number(resp.headers.get('retry-after')) > 0);
  }
});

test('an issue GitHub refuses for good is refused to the device, not retried', async () => {
  // 422 with the labels and again without them, or a 400: the device keeps a copy and moves on.
  for (const script of [
    [NOT_FOUND, { status: 422, body: { message: 'Validation Failed' } }, { status: 422, body: { message: 'Validation Failed' } }],
    [NOT_FOUND, { status: 400, body: { message: 'Problems parsing JSON' } }],
  ]) {
    const c = collector({}, script);
    const resp = await c.handle(post(report()), at('203.0.113.9'));
    assert.equal(resp.status, 422);
    assert.equal((await resp.json()).error, 'GitHub refused the issue');
  }
  const bare = collector({ JUKAHUB_ISSUE_LABELS: '' }, [NOT_FOUND, { status: 422, body: {} }]);
  assert.equal((await bare.handle(post(report()), at('203.0.113.9'))).status, 422);
});

test('only an issue with a number counts as filed', async () => {
  // The device deletes a report it is told was filed.
  for (const answer of [
    { status: 200, body: [{ number: 1 }] },
    { status: 201, body: {} },
    { status: 201, body: { number: 0, html_url: ISSUE_URL } },
    { status: 201, body: { number: '42' } },
  ]) {
    const c = collector({}, [NOT_FOUND, answer]);
    const resp = await c.handle(post(report()), at('203.0.113.9'));
    assert.equal(resp.status, 502, JSON.stringify(answer));
  }
});

test('labels GitHub will not take do not cost the report', async () => {
  const c = collector({}, [NOT_FOUND, { status: 422, body: { message: 'Validation Failed' } }, CREATED]);
  assert.equal((await c.handle(post(report()), at('203.0.113.9'))).status, 201);
  assert.deepEqual(JSON.parse(c.calls[1].init.body).labels, ['bug', 'from-device']);
  assert.equal(JSON.parse(c.calls[2].init.body).labels, undefined);
});

test('a configured Discord webhook hears about the issue, and its failure is ignored', async () => {
  const hook = 'https://discord.com/api/webhooks/1/abc';
  const c = collector({ JUKAHUB_DISCORD_WEBHOOK: hook }, [NOT_FOUND, CREATED, { status: 204 }]);
  assert.equal((await c.handle(post(report()), at('203.0.113.9'))).status, 201);
  assert.equal(c.calls[2].url, hook);
  const notice = JSON.parse(c.calls[2].init.body);
  assert.deepEqual(notice.allowed_mentions, { parse: [] });
  assert.ok(notice.content.includes(ISSUE_URL));

  const d = collector({ JUKAHUB_DISCORD_WEBHOOK: hook }, [NOT_FOUND, CREATED, new Error('discord down')]);
  assert.equal((await d.handle(post(report()), at('203.0.113.9'))).status, 201);
  const e = collector({ JUKAHUB_DISCORD_WEBHOOK: 'https://evil.example/hook' }, [NOT_FOUND, CREATED]);
  assert.equal((await e.handle(post(report()), at('203.0.113.9'))).status, 201);
  assert.equal(e.calls.length, 2, 'only a Discord webhook address is called');
});

test('the Discord notice waits for the answer and cannot carry a masked link', async () => {
  const hook = 'https://discord.com/api/webhooks/1/abc';
  const c = collector({ JUKAHUB_DISCORD_WEBHOOK: hook }, [NOT_FOUND, CREATED, { status: 204 }]);
  const pending = [];
  const resp = await c.handle(post(report({ title: 'Look [here](https://evil.example/x) *now*' })), {
    ip: '203.0.113.9',
    waitUntil: p => pending.push(p),
  });
  assert.equal(resp.status, 201);
  assert.equal(pending.length, 1, 'the notice is handed to waitUntil');
  await Promise.all(pending);
  const notice = JSON.parse(c.calls[2].init.body);
  assert.ok(notice.content.includes('\\[here\\]\\('), notice.content);
  assert.ok(notice.content.includes('\\*now\\*'), notice.content);
  assert.ok(!notice.content.includes('https://evil'), notice.content);
});

test('the rate limiter does not grow with addresses it turns away', () => {
  let t = Date.parse('2026-11-10T12:00:00Z');
  const limiter = createLimiter(() => t);
  for (let i = 0; i < 60; i++) assert.equal(limiter.take(`10.0.0.${i}`), 0);
  // The instance's hourly cap is reached: every new address is turned away and none is kept.
  for (let i = 0; i < 20000; i++) assert.ok(limiter.take(`172.16.${i >> 8}.${i & 255}`) > 0);
  assert.equal(limiter.size(), 60);
  // Over days, the oldest addresses make room.
  for (let i = 0; i < 6000; i++) {
    t += 61 * 1000;
    limiter.take(`192.168.${i >> 8}.${i & 255}`);
  }
  assert.ok(limiter.size() <= 5000, String(limiter.size()));
});

test('mentions, references, links and HTML in a report are defused', () => {
  const r = report({
    title: 'Ping @octocat about #12',
    description: '@octocat <img src=x onerror=alert(1)> see #12 [x](https://evil.example/a/b?c=d)\n# heading',
  });
  const { title, body } = buildIssue(r);
  assert.ok(title.includes('@\u200boctocat') && title.includes('#\u200b12'), title);
  assert.ok(body.includes('@\u200boctocat'));
  assert.ok(body.includes('\\<img'));
  assert.ok(!/(^|[^\\])<img/.test(body), 'no raw HTML survives');
  assert.ok(body.includes('#\u200b12'));
  assert.ok(body.includes('\\[x\\]'));
  assert.ok(!body.includes('/a/b'), 'link paths are dropped');
  assert.ok(body.includes('\\# heading'));
  assert.equal(escapeMarkdown('a|b'), 'a\\|b');
});

test('nothing in a report stays a clickable link or a cross-reference', () => {
  const r = report({
    title: 'See https://evil.example/x and GH-12',
    description: 'bare https://evil.example/path?q=1 then www.evil.example/phish?token=abc and HTTPS://UPPER.example/path?q=1 and GH-12',
    device: 'www.device.example/x',
  });
  const { title, body } = buildIssue(r);
  // GitHub autolinks "://" and "www." even after markdown escaping, and cross-links GH-123.
  assert.ok(!body.includes('://'), 'every scheme is broken by a zero-width space');
  assert.ok(!/www\./i.test(body), 'every www. is broken by a zero-width space');
  assert.ok(body.includes('www\u200b.evil.example/…'), body);
  assert.ok(body.includes('UPPER.example/…') && !body.includes('/path') && !body.includes('token=abc'), body);
  assert.ok(body.includes('GH-\u200b12') && !/GH-12/.test(body), body);
  assert.ok(title.includes('GH-\u200b12') && !title.includes('://'), title);
  assert.ok(body.includes('| Device | www\u200b.device.example/… |'), body);
  // Scrubbing alone keeps a link's host and drops the rest, in any letter case.
  assert.equal(scrub('HTTPS://Host.example/Secret?k=v'), 'HTTPS://Host.example/…');
  assert.equal(scrub('www.host.example/secret?token=abc'), 'www.host.example/…');
});

test('personal data the device missed is scrubbed again', () => {
  const out = scrub('mail bob@example.com from 10.1.2.3 on aa:bb:cc:dd:ee:ff via https://alice:pw@media.example/private/x.m3u8 dns 8.8.8.8');
  for (const gone of ['bob@example.com', '10.1.2.3', 'aa:bb:cc:dd:ee:ff', 'alice', 'pw@', '/private']) {
    assert.ok(!out.includes(gone), `${gone} survived: ${out}`);
  }
  for (const kept of ['https://media.example/…', '8.8.8.8', '[email]', '[ip]', '[mac]']) {
    assert.ok(out.includes(kept), `${kept} missing: ${out}`);
  }
  assert.equal(scrub(out), out, 'scrubbing is idempotent');
});

test('diagnostics and log are fenced, collapsed and kept under the issue limit', () => {
  const r = report({
    description: 'd'.repeat(4000),
    diagnostics: 'fps 60 ``` not a fence\n' + 'D'.repeat(16 * 1024),
    log: 'oldest line\n' + 'L'.repeat(48 * 1024) + '\nnewest line',
  });
  const full = buildIssue(r);
  assert.ok(full.body.length <= 60000);
  assert.ok(full.body.includes('<details>\n<summary>Diagnostics (redacted)</summary>\n\n````text\n'), 'a fence longer than the backticks inside');
  assert.ok(full.body.includes('<summary>Recent log (redacted)</summary>'));

  const small = buildIssue(r, 30000);
  assert.ok(small.body.length <= 30000, String(small.body.length));
  assert.ok(small.body.includes('newest line'), 'the newest log lines are kept');
  assert.ok(!small.body.includes('oldest line'), 'the oldest log lines go first');
  assert.ok(small.body.includes('earlier lines cut'));
  assert.ok(small.body.includes('fps 60'), 'diagnostics survive while the log can give way');

  const tiny = buildIssue(r, 9000);
  assert.ok(tiny.body.length <= 9000, String(tiny.body.length));
  assert.ok(!tiny.body.includes('Recent log'), 'the log is dropped before the description is touched');
});

test('the title names the category when the report has none', () => {
  assert.equal(issueTitle(report({ title: '' , category: 'controls' })), '[Bug] Controls problem (JH-7K2Q9M)');
  assert.ok(issueTitle(report({ title: 'z'.repeat(150) })).length < 140);
  assert.equal(validateReport(report()), '');
  assert.equal(validateReport(report({ release: undefined })), '');
});

test('a hostile wall of text costs one pass, not a backtrack per character', () => {
  const start = performance.now();
  const wall = 'L'.repeat(200 * 1024);
  scrub(`${wall} tail@ 1.2.3`);
  scrub('www.'.repeat(12 * 1024));
  scrub('https://'.repeat(6 * 1024) + 'a'.repeat(4096));
  buildIssue(report({ log: wall, diagnostics: wall.slice(0, 16 * 1024) }));
  assert.ok(performance.now() - start < 2000, `took ${Math.round(performance.now() - start)} ms`);
});
