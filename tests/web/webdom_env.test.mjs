// webdom_env.test.mjs — the browser's script environment (player/webdom/60–90): the page phases, the event loop
// and virtual clock, document.write, dynamic and module scripts, fetch/XHR, location/history, storage,
// observers, encoding, URL, Intl.
//
// runPage() below drives the prelude's host API exactly the way player/webdom_runtime.go does: parser scripts
// one at a time, interactive, deferred scripts, DOMContentLoaded, the event loop until idle (fast-forwarding
// virtual time only while nothing is on the wire), load, the loop again, serialize. Network natives answer from
// a route table; the module rewrite is a small stand-in for webdom_module.go that keeps its output contract.

import test from 'node:test';
import assert from 'node:assert/strict';
import { loadPage, flush } from './webdom_harness.mjs';

const BASE = 'https://example.test/';

function bytes(s) { return new TextEncoder().encode(s); }

// A stand-in for webJSTransformModule: line-oriented, enough for the modules in these tests, and producing the
// same wrapper contract (DESIGN.md "M").
function mockTransform(src) {
  const imports = [];
  const idx = (spec, type = '') => {
    let i = imports.findIndex((x) => x.spec === spec && x.type === type);
    if (i < 0) {
      imports.push({ spec, type });
      i = imports.length - 1;
    }
    return i;
  };
  const exps = [], pre = [], binds = [];
  const names = (list) => list.split(',').map((s) => s.trim()).filter(Boolean).map((s) => s.split(/\s+as\s+/));
  let body = String(src);
  body = body.replace(/^[ \t]*import\s+([^'";]+?)\s+from\s+["']([^"']+)["'](?:\s+with\s*\{\s*type:\s*["'](\w+)["']\s*\})?\s*;?/gm, (m, clause, spec, type) => {
    const i = idx(spec, type || '');
    clause = clause.trim();
    const ns = /\*\s+as\s+(\w+)/.exec(clause);
    if (ns) binds.push(`const ${ns[1]} = __jh.imp(${i});`);
    const def = /^(\w+)\s*(,|$)/.exec(clause);
    if (def) binds.push(`const ${def[1]} = __jh.imp(${i}).default;`);
    const named = /\{([^}]*)\}/.exec(clause);
    if (named) binds.push(`const { ${names(named[1]).map((p) => `${JSON.stringify(p[0])}: ${p[1] || p[0]}`).join(', ')} } = __jh.imp(${i});`);
    return '';
  });
  body = body.replace(/^[ \t]*import\s+["']([^"']+)["']\s*;?/gm, (m, spec) => { idx(spec); return ''; });
  body = body.replace(/^[ \t]*export\s+\*\s+from\s+["']([^"']+)["']\s*;?/gm, (m, spec) => { pre.push(`__jh.star(${idx(spec)});`); return ''; });
  body = body.replace(/^[ \t]*export\s*\{([^}]*)\}\s*from\s+["']([^"']+)["']\s*;?/gm, (m, list, spec) => {
    const map = {};
    names(list).forEach((p) => { map[p[1] || p[0]] = p[0]; });
    pre.push(`__jh.reexp(${idx(spec)}, ${JSON.stringify(map)});`);
    return '';
  });
  body = body.replace(/^[ \t]*export\s*\{([^}]*)\}\s*;?/gm, (m, list) => {
    names(list).forEach((p) => exps.push(`${JSON.stringify(p[1] || p[0])}: () => ${p[0]}`));
    return '';
  });
  body = body.replace(/^([ \t]*)export\s+default\s+(function|class)\s+(\w+)/gm, (m, ws, kw, name) => {
    exps.push(`"default": () => ${name}`);
    return `${ws}${kw} ${name}`;
  });
  body = body.replace(/^([ \t]*)export\s+default\s+/gm, (m, ws) => { exps.push('"default": () => __jh_default'); return `${ws}const __jh_default = `; });
  body = body.replace(/^([ \t]*)export\s+(const|let|var|function|class)\s+(\w+)/gm, (m, ws, kw, name) => {
    exps.push(`${JSON.stringify(name)}: () => ${name}`);
    return `${ws}${kw} ${name}`;
  });
  body = body.replace(/\bimport\.meta\b/g, '__jh.meta').replace(/\bimport\s*\(/g, '__jh.dyn(');
  const prologue = (exps.length ? `__jh.exp({ ${exps.join(', ')} });` : '') + pre.join('') + binds.join('');
  return { code: `(function (__jh) { return (async function () { "use strict"; ${prologue}\n${body}\n})(); })`, imports, error: null };
}

function makeNet(routes) {
  const queue = [];
  const log = [];
  const respond = (req) => {
    let r = routes[req.url];
    if (typeof r === 'function') r = r(req);
    if (r === undefined || r === null) {
      return { err: 'not in the test routes', type: '', status: 0, statusText: '', url: req.url, redirected: false, headers: [], body: new ArrayBuffer(0) };
    }
    if (typeof r === 'string') r = { body: r };
    const body = typeof r.body === 'string' ? bytes(r.body) : (r.body || new Uint8Array(0));
    return {
      err: r.err || '', type: r.type || 'basic', status: r.status || 200, statusText: r.statusText || 'OK', url: r.url || req.url,
      redirected: !!r.redirected, headers: r.headers || [['content-type', r.contentType || 'text/plain']], body,
    };
  };
  const scriptOf = (url) => {
    let r = routes[url];
    if (typeof r === 'function') r = r({ url, method: 'GET' });
    if (r === undefined || r === null) return { code: null, url, err: 'not found' };
    return { code: typeof r === 'string' ? r : r.body, url, err: null };
  };
  const natives = {
    fetch: (id, req) => {
      log.push(['fetch', req.method, req.url, req.credentials, req.mode, req.body ? new TextDecoder().decode(req.body) : null, req.headers]);
      queue.push({ kind: 'fetch', id, resp: respond(req) });
      return true;
    },
    fetchSync: (req) => {
      log.push(['fetchSync', req.method, req.url]);
      return respond(req);
    },
    loadScript: (id, url, module) => {
      log.push(['loadScript', url, !!module]);
      const s = scriptOf(url);
      queue.push({ kind: 'script', id, code: s.code, url, err: s.err });
      return true;
    },
    prefetch: (urls) => { log.push(['prefetch', ...urls]); },
    fetchScriptSync: (url, module) => {
      log.push(['fetchScriptSync', url, !!module]);
      const s = scriptOf(url);
      return { code: s.code, url, error: s.err };
    },
    transformModule: (code) => mockTransform(code),
    rewriteDynamicImport: (code) => code.replace(/\bimport\s*\(/g, '__jh_import(').replace(/\bimport\.meta\b/g, '({url: document.baseURI})'),
  };
  return { natives, queue, log, scriptOf };
}

// runPage drives a page through every phase, like webdom_runtime.go.
async function runPage(html, options = {}) {
  const routes = {};
  for (const [k, v] of Object.entries(options.routes || {})) routes[new URL(k, BASE).href] = v;
  const net = makeNet(routes);
  const page = await loadPage(html, {
    url: options.url || BASE + 'page.html',
    natives: Object.assign({}, net.natives, options.natives || {}),
    config: Object.assign({ width: 1280, height: 720, timeZone: 'UTC', language: 'en-US', userAgent: 'JukaHub Browser/1.1' }, options.config || {}),
  });
  const { host } = page;
  const runScripts = async (next) => {
    for (let i = 0; i < 300; i++) {
      const info = next();
      await flush();
      if (!info) return;
      if (!info.src) host.execScript(info.id, null, null, null);
      else {
        const s = net.scriptOf(info.src);
        host.execScript(info.id, s.code, s.url, s.err);
      }
      await flush();
    }
  };
  const loop = async () => {
    for (let i = 0; i < 5000; i++) {
      while (net.queue.length) {
        const ev = net.queue.shift();
        if (ev.kind === 'fetch') host.fetchDone(ev.id, ev.resp);
        else host.scriptLoaded(ev.id, ev.code, ev.url, ev.err);
        await flush();
      }
      const w = host.runTask(net.queue.length === 0);
      await flush();
      if (w === -2 || net.queue.length) continue;
      return;
    }
  };
  await runScripts(() => host.nextParserScript());
  host.ready('interactive');
  await flush();
  await runScripts(() => host.nextDeferredScript());
  host.ready('domcontentloaded');
  await flush();
  await loop();
  host.ready('complete');
  await flush();
  await loop();
  const out = host.serialize();
  await flush();
  return { page, out, stats: JSON.parse(host.stats()), net, run: page.run, state: page.state };
}

// ---------------------------------------------------------------- phases and script order

test('scripts run in browser order: parser, written, deferred, DOMContentLoaded, load', async () => {
  const r = await runPage(`<!doctype html><html><head><script>window.order = ['head'];</script></head><body>
    <div id="app">Loading</div>
    <script>order.push('inline'); document.getElementById('app').textContent = 'hello'; document.write('<p id="w">written</p>');
      order.push(document.getElementById('w') ? 'write-visible' : 'write-missing');</script>
    <script src="/a.js"></script>
    <script defer src="/d.js"></script>
    <script async src="/async.js"></script>
    <script>document.addEventListener('DOMContentLoaded', function () { order.push('dcl'); });
      addEventListener('load', function () { order.push('load'); document.body.setAttribute('data-order', order.join(',')); });</script>
    </body></html>`, {
    routes: {
      '/a.js': "order.push('a'); document.write('<script src=\"/b.js\"><\\/script>');",
      '/b.js': "order.push('b');",
      '/d.js': "order.push('defer');",
      '/async.js': "order.push('async');",
    },
  });
  assert.match(r.out, /data-order="head,inline,write-visible,a,b,defer,dcl,async,load"/);
  assert.match(r.out, /<div id="app">hello<\/div>/);
  assert.match(r.out, /<p id="w">written<\/p>/);
  assert.equal(r.stats.scriptErrors, 0);
});

test('script bodies are dropped from the renderer output', async () => {
  const r = await runPage('<body><script>var secret = 1;</script><p>shown</p></body>');
  assert.doesNotMatch(r.out, /secret/);
  assert.match(r.out, /<p>shown<\/p>/);
});

test('document.write: a table written row by row stays one table', async () => {
  const r = await runPage(`<body><script>
    document.write('<table id="t">');
    for (var i = 0; i < 3; i++) document.write('<tr><td>r' + i + '</td></tr>');
    document.write('</table>');
  </script></body>`);
  const rows = r.run("document.querySelectorAll('#t tr').length");
  assert.equal(rows, 3);
});

test('document.write from <head> puts body content in the body, in write order', async () => {
  const r = await runPage('<html><head><script>document.write("<div id=\\"x\\">from head</div>"); document.write("<div>second</div><meta name=a>")</script></head><body><p>b</p></body></html>');
  assert.equal(r.run("document.getElementById('x').parentNode.tagName"), 'BODY');
  assert.match(r.out, /<body><div id="x">from head<\/div><div>second<\/div><p>b<\/p>/);
  assert.match(r.out, /<head><script><\/script><meta name="a"><\/head>/);
});

test('document.write after the parser finished is ignored (it would wipe the page)', async () => {
  const r = await runPage('<body><p id="keep">keep</p><script>setTimeout(function () { document.write("<h1>gone</h1>"); }, 10);</script></body>');
  assert.match(r.out, /keep/);
  assert.doesNotMatch(r.out, /gone/);
});

test('a script that removes itself can still write after where it was', async () => {
  const r = await runPage('<body><span>a</span><script>document.currentScript.remove(); document.write("<b>w</b>");</script><span>c</span></body>');
  assert.match(r.out, /<span>a<\/span><b>w<\/b><span>c<\/span>/);
});

test('scripts share one global lexical scope', async () => {
  const r = await runPage('<body><script>const shared = 41; let counter = 1;</script><script>document.body.dataset.v = shared + counter;</script></body>');
  assert.match(r.out, /data-v="42"/);
});

test('currentScript is the running script; nomodule scripts do not run', async () => {
  const r = await runPage(`<body><script id="me">document.body.dataset.cur = document.currentScript.id;</script>
    <script nomodule>document.body.dataset.legacy = 'ran';</script></body>`);
  assert.match(r.out, /data-cur="me"/);
  assert.doesNotMatch(r.out, /data-legacy/);
});

test('an uncaught error reaches window.onerror and the stats, and the page goes on', async () => {
  const r = await runPage(`<body><script>window.seen = []; window.addEventListener('error', function (e) { seen.push(e.message); });</script>
    <script>undefinedFunction();</script>
    <script>document.body.dataset.after = seen.length;</script></body>`);
  assert.match(r.out, /data-after="1"/);
  assert.equal(r.stats.scriptErrors, 1);
  assert.match(r.stats.errors[0], /undefinedFunction/);
});

// ---------------------------------------------------------------- dynamic scripts

test('dynamic scripts: onload fires, async=false keeps insertion order, inline ones run at insertion', async () => {
  const r = await runPage(`<body><script>
    window.log = [];
    function add(src, ordered) {
      var s = document.createElement('script');
      s.src = src;
      if (ordered) s.async = false;
      s.onload = function () { log.push('load ' + src); };
      document.head.appendChild(s);
    }
    add('/x1.js', true); add('/x2.js', true);
    var i = document.createElement('script'); i.textContent = "log.push('inline')"; document.body.appendChild(i);
    log.push('after-inline');
    var late = document.createElement('script'); document.body.appendChild(late); late.text = "log.push('late-text')";
    addEventListener('load', function () { document.body.dataset.log = log.join('|'); });
  </script></body>`, {
    routes: { '/x1.js': "log.push('x1')", '/x2.js': "log.push('x2')" },
  });
  assert.match(r.out, /data-log="inline\|after-inline\|late-text\|x1\|load \/x1\.js\|x2\|load \/x2\.js"/);
});

test('a dynamic script that fails to load fires error', async () => {
  const r = await runPage(`<body><script>
    var s = document.createElement('script'); s.src = '/missing.js';
    s.onerror = function () { document.body.dataset.err = 'yes'; };
    document.body.appendChild(s);
  </script></body>`);
  assert.match(r.out, /data-err="yes"/);
});

// ---------------------------------------------------------------- event loop and timers

test('timers fire in due order on the virtual clock; long delays are not fast-forwarded; intervals are capped', async () => {
  const r = await runPage(`<body><script>
    window.t = [];
    setTimeout(function () { t.push('b30s'); }, 30000);
    setTimeout(function () { t.push('a10'); }, 10);
    var gone = setTimeout(function () { t.push('cleared'); }, 5);
    clearTimeout(gone);
    var n = 0, iv = setInterval(function () { n++; }, 1000);
    setTimeout(function () { clearInterval(iv); t.push('ticks' + n); }, 5500);
    setTimeout(function (x, y) { t.push('args' + x + y); }, 1, 'p', 'q');
    setTimeout(function () { t.push('nine'); }, 9000);
    // A timer that asked for more than 10 s is not what a reader sees: it is never skipped to (b30s), while
    // the load handler's 2 s timer still runs.
    addEventListener('load', function () { setTimeout(function () { document.body.dataset.t = t.join(','); }, 2000); });
  </script></body>`);
  assert.match(r.out, /data-t="argspq,a10,ticks5,nine"/);
  assert.doesNotMatch(r.out, /cleared|b30s/);
});

test('requestAnimationFrame, requestIdleCallback and queueMicrotask run', async () => {
  const r = await runPage(`<body><script>
    window.got = [];
    queueMicrotask(function () { got.push('micro'); });
    requestAnimationFrame(function (ts) { got.push(typeof ts); });
    requestIdleCallback(function (d) { got.push('idle' + (d.timeRemaining() > 0)); });
    var loops = 0; (function spin() { if (++loops < 1000) requestAnimationFrame(spin); })();
    addEventListener('load', function () { setTimeout(function () { document.body.dataset.got = got.join(',') + ',' + (loops <= 61); }, 100); });
  </script></body>`);
  assert.match(r.out, /data-got="micro,(number,idletrue|idletrue,number),true"/);
});

test('MessageChannel posts run as tasks after the current microtasks', async () => {
  const r = await runPage(`<body><script>
    window.seq = [];
    var ch = new MessageChannel();
    ch.port1.onmessage = function (e) { seq.push('msg:' + e.data.n); };
    ch.port2.postMessage({ n: 1 });
    Promise.resolve().then(function () { seq.push('micro'); });
    seq.push('sync');
    addEventListener('load', function () { document.body.dataset.seq = seq.join(','); });
  </script></body>`);
  assert.match(r.out, /data-seq="sync,micro,msg:1"/);
});

test('window.postMessage delivers a MessageEvent to window', async () => {
  const r = await runPage(`<body><script>
    addEventListener('message', function (e) { document.body.dataset.m = e.data.hello + ':' + (e.source === window) + ':' + e.origin; });
    postMessage({ hello: 'world' }, '*');
  </script></body>`);
  assert.match(r.out, /data-m="world:true:https:\/\/example.test"/);
});

// ---------------------------------------------------------------- network

test('fetch: JSON from an API becomes content; credentials and mode reach Go', async () => {
  const r = await runPage(`<body><ul id="list"></ul><script>
    fetch('/api/items?x=1').then(function (res) {
      document.body.dataset.status = res.status + ' ' + res.ok + ' ' + res.headers.get('Content-Type');
      return res.json();
    }).then(function (items) {
      items.forEach(function (it) { var li = document.createElement('li'); li.textContent = it.name; document.getElementById('list').appendChild(li); });
    });
  </script></body>`, {
    routes: { '/api/items?x=1': { body: JSON.stringify([{ name: 'one' }, { name: 'two' }]), contentType: 'application/json' } },
  });
  assert.match(r.out, /<ul id="list"><li>one<\/li><li>two<\/li><\/ul>/);
  assert.match(r.out, /data-status="200 true application\/json"/);
  const call = r.net.log.find((l) => l[0] === 'fetch');
  assert.deepEqual(call.slice(1, 5), ['GET', 'https://example.test/api/items?x=1', 'same-origin', 'cors']);
});

test('fetch: POST bodies, headers, failures and aborts', async () => {
  const r = await runPage(`<body><script>
    window.res = [];
    fetch('/post', { method: 'post', headers: { 'X-Test': 'yes' }, body: JSON.stringify({ a: 1 }) })
      .then(function (r) { return r.text(); }).then(function (t) { res.push('post:' + t); });
    fetch('/nope').then(function () { res.push('nope-ok'); }, function (e) { res.push('nope:' + e.name); });
    var ac = new AbortController();
    fetch('/slow', { signal: ac.signal }).then(function () { res.push('slow-ok'); }, function (e) { res.push('abort:' + e.name); });
    ac.abort();
    fetch('/form', { method: 'POST', body: new URLSearchParams({ q: 'a b', n: '1' }) });
    addEventListener('load', function () { setTimeout(function () { document.body.dataset.res = res.sort().join('|'); }, 10); });
  </script></body>`, {
    routes: { '/post': (req) => ({ body: req.method + ' ' + new TextDecoder().decode(req.body) }), '/slow': 'late', '/form': 'ok' },
  });
  assert.match(r.out, /data-res="abort:AbortError\|nope:TypeError\|post:POST \{&quot;a&quot;:1\}"/);
  const post = r.net.log.find((l) => l[0] === 'fetch' && l[2].endsWith('/post'));
  assert.ok(post[6].some((h) => h[0] === 'x-test' && h[1] === 'yes'));
  assert.ok(post[6].some((h) => h[0] === 'content-type' && /^text\/plain/.test(h[1])));
  const form = r.net.log.find((l) => l[0] === 'fetch' && l[2].endsWith('/form'));
  assert.equal(form[5], 'q=a+b&n=1');
});

test('XMLHttpRequest: async with events, JSON responseType, sync, abort, headers', async () => {
  const r = await runPage(`<body><script>
    window.ev = [];
    var x = new XMLHttpRequest();
    x.onreadystatechange = function () { ev.push('rs' + x.readyState); };
    x.open('GET', '/data.json');
    x.responseType = 'json';
    x.onload = function () { ev.push('load:' + x.status + ':' + x.response.v + ':' + x.getResponseHeader('content-type')); };
    x.onloadend = function () { ev.push('end'); };
    x.send();
    var s = new XMLHttpRequest();
    s.open('GET', '/text.txt', false);
    s.send();
    ev.push('sync:' + s.status + ':' + s.responseText);
    var a = new XMLHttpRequest();
    a.open('GET', '/data.json');
    a.onabort = function () { ev.push('aborted'); };
    a.send();
    a.abort();
    addEventListener('load', function () { document.body.dataset.ev = ev.join(','); });
  </script></body>`, {
    routes: { '/data.json': { body: '{"v":7}', contentType: 'application/json' }, '/text.txt': 'plain' },
  });
  assert.match(r.out, /data-ev="rs1,sync:200:plain,aborted,rs2,rs3,rs4,load:200:7:application\/json,end"/);
});

test('scripts see navigator, screen and the viewport of the handheld', async () => {
  const r = await runPage(`<body><script>
    document.body.dataset.ua = navigator.userAgent;
    document.body.dataset.size = innerWidth + 'x' + innerHeight + ':' + screen.width + ':' + devicePixelRatio;
    document.body.dataset.lang = navigator.language + ':' + navigator.languages.join('+') + ':' + navigator.onLine + ':' + navigator.webdriver;
  </script></body>`, { config: { width: 1024, height: 768, languages: ['en-US', 'en'] } });
  assert.match(r.out, /data-ua="JukaHub Browser\/1\.1"/);
  assert.match(r.out, /data-size="1024x768:1024:1"/);
  assert.match(r.out, /data-lang="en-US:en-US\+en:true:false"/);
});

test('matchMedia answers like a light, reduced-motion landscape screen', async () => {
  const r = await runPage('<body></body>');
  const q = (s) => r.run(`matchMedia(${JSON.stringify(s)}).matches`);
  assert.equal(q('(min-width: 600px)'), true);
  assert.equal(q('(max-width: 600px)'), false);
  assert.equal(q('screen and (min-width: 40em) and (max-width: 100em)'), true);
  assert.equal(q('(width >= 1000px)'), true);
  assert.equal(q('(400px <= width <= 700px)'), false);
  assert.equal(q('(prefers-color-scheme: dark)'), false);
  assert.equal(q('(prefers-color-scheme: light)'), true);
  assert.equal(q('(prefers-reduced-motion: reduce)'), true);
  assert.equal(q('(orientation: landscape)'), true);
  assert.equal(q('print'), false);
  assert.equal(q('not print'), true);
  assert.equal(q('(hover: hover) and (pointer: fine)'), true);
  assert.equal(q('(min-resolution: 2dppx)'), false);
  assert.equal(q(''), true);
});

// ---------------------------------------------------------------- location and history

test('location: parts, fragment changes are hashchange, real moves are recorded for Go', async () => {
  const r = await runPage(`<body><script>
    document.body.dataset.loc = [location.protocol, location.host, location.pathname, location.search, location.origin].join(' ');
    addEventListener('hashchange', function (e) { document.body.dataset.hash = location.hash + ' ' + e.newURL; });
    location.hash = 'part';
    setTimeout(function () { location.href = '/next?x=1'; }, 5);
  </script></body>`, { url: 'https://example.test/dir/page.html?q=1' });
  assert.match(r.out, /data-loc="https: example\.test \/dir\/page\.html \?q=1 https:\/\/example\.test"/);
  assert.match(r.out, /data-hash="#part https:\/\/example\.test\/dir\/page\.html\?q=1#part"/);
  assert.deepEqual(r.state.navigations, [['https://example.test/next?x=1', false]]);
});

test('history.pushState moves the document URL without navigating; cross-origin URLs throw', async () => {
  const r = await runPage(`<body><script>
    history.pushState({ a: 1 }, '', '/app/route?tab=2');
    document.body.dataset.r = location.pathname + location.search + ' ' + document.URL + ' ' + history.state.a + ' ' + history.length;
    try { history.replaceState(null, '', 'https://other.test/'); } catch (e) { document.body.dataset.err = e.name; }
  </script></body>`);
  assert.match(r.out, /data-r="\/app\/route\?tab=2 https:\/\/example\.test\/app\/route\?tab=2 1 2"/);
  assert.match(r.out, /data-err="SecurityError"/);
  assert.deepEqual(r.state.navigations, []);
});

test('window.open(_self) and anchor-style navigation; reload is ignored', async () => {
  const r = await runPage('<body><script>location.reload(); location.replace(location.href + "#x"); window.open("/self", "_self"); var w = window.open("/popup"); document.body.dataset.w = String(w);</script></body>');
  assert.deepEqual(r.state.navigations, [['https://example.test/self', false]]);
  assert.match(r.out, /data-w="null"/);
});

// ---------------------------------------------------------------- storage

test('localStorage: methods, property access, enumeration, quota, persistence through Go', async () => {
  const store = new Map();
  const natives = {
    storageGet: (kind) => store.get(kind) || '',
    storageSet: (kind, json) => { store.set(kind, json); return true; },
  };
  const r = await runPage(`<body><script>
    localStorage.setItem('a', 1);
    localStorage.b = 'two';
    var keys = Object.keys(localStorage).sort().join();
    document.body.dataset.s = [localStorage.getItem('a'), localStorage.b, localStorage.length, keys, 'a' in localStorage, localStorage.key(5)].join('|');
    try { localStorage.setItem('big', new Array(600001).join('x')); } catch (e) { document.body.dataset.q = e.name; }
    sessionStorage.setItem('s', 'v');
    delete localStorage.b;
  </script></body>`, { natives });
  assert.match(r.out, /data-s="1\|two\|2\|a,b\|true\|"/);
  assert.match(r.out, /data-q="QuotaExceededError"/);
  assert.deepEqual(JSON.parse(store.get('local')), { a: '1' });
  assert.deepEqual(JSON.parse(store.get('session')), { s: 'v' });
  const again = await runPage('<body><script>document.body.dataset.a = localStorage.getItem("a");</script></body>', { natives });
  assert.match(again.out, /data-a="1"/);
});

// ---------------------------------------------------------------- observers and lazy content

test('IntersectionObserver reports every target as visible, so lazy images load', async () => {
  const r = await runPage(`<body><img class="lazy" data-src="/real.jpg" src="data:image/gif;base64,R0lGODlhAQABAAAAACw="><script>
    var io = new IntersectionObserver(function (entries, obs) {
      entries.forEach(function (e) {
        if (e.isIntersecting) { e.target.src = e.target.dataset.src; obs.unobserve(e.target); }
      });
      document.body.dataset.ratio = entries[0].intersectionRatio;
    }, { rootMargin: '200px' });
    document.querySelectorAll('img.lazy').forEach(function (img) { io.observe(img); });
    var ro = new ResizeObserver(function (entries) { document.body.dataset.ro = entries.length + ':' + (entries[0].contentRect.width >= 0); });
    ro.observe(document.body);
  </script></body>`);
  assert.match(r.out, /src="\/real\.jpg"/);
  assert.match(r.out, /data-ratio="1"/);
  assert.match(r.out, /data-ro="1:true"/);
});

test('images report load to scripts (parsed after DOMContentLoaded, new ones when src is set)', async () => {
  const r = await runPage(`<body><img id="p" src="/a.png" onload="this.dataset.loaded = 'yes'"><script>
    var img = new Image();
    img.onload = function () { document.body.dataset.pre = 'loaded'; };
    img.src = '/b.png';
  </script></body>`);
  assert.match(r.out, /<img id="p" src="\/a\.png"[^>]*data-loaded="yes"/);
  assert.match(r.out, /data-pre="loaded"/);
});

// ---------------------------------------------------------------- modules

test('module scripts: static imports, live exports, re-exports, import maps, JSON, dynamic import', async () => {
  const r = await runPage(`<head><script type="importmap">{"imports": {"lib": "/js/lib.js", "util/": "/js/util/"}}</script></head>
    <body><div id="out"></div>
    <script type="module">
      import greet, { count, bump } from 'lib';
      import * as all from './js/all.js';
      import data from './js/data.json' with { type: 'json' };
      bump(); bump();
      document.getElementById('out').textContent = greet('x') + ' ' + count + ' ' + all.count + ' ' + all.helper() + ' ' + data.title + ' ' + import.meta.url.slice(-6);
      import('util/late.js').then(function (m) { document.body.dataset.late = m.late; });
    </script>
    <script>document.body.dataset.classicFirst = document.getElementById('out').textContent === '' ? 'yes' : 'no';</script></body>`, {
    routes: {
      '/js/lib.js': 'export let count = 0;\nexport function bump() { count++; }\nexport default function greet(n) { return "hi " + n; }',
      '/js/all.js': "export * from './lib.js';\nexport { helper } from './util/h.js';",
      '/js/util/h.js': 'export const helper = () => "help";',
      '/js/data.json': '{"title": "T"}',
      '/js/util/late.js': 'export const late = "dyn";',
    },
  });
  // Bindings are copied when the module starts (a stand-in limitation), the namespace stays live.
  assert.match(r.out, /<div id="out">hi x 0 2 help T [^<]*<\/div>/);
  assert.match(r.out, /data-classic-first="yes"/);
  assert.match(r.out, /data-late="dyn"/);
  assert.equal(r.stats.scriptErrors, 0);
});

test('a module that fails to load fires error and does not stop the page', async () => {
  const r = await runPage(`<body><script type="module" src="/missing.mjs" onerror="document.body.dataset.e = 'err'"></script>
    <script type="module">import { x } from '/also-missing.mjs'; document.body.dataset.ran = 'yes';</script>
    <script>document.body.dataset.classic = 'ok';</script></body>`);
  assert.match(r.out, /data-e="err"/);
  assert.match(r.out, /data-classic="ok"/);
  assert.doesNotMatch(r.out, /data-ran/);
});

test('classic scripts may use import()', async () => {
  const r = await runPage('<body><script>import("/m.js").then(function (m) { document.body.dataset.v = m.v; });</script></body>', {
    routes: { '/m.js': 'export const v = 5;' },
  });
  assert.match(r.out, /data-v="5"/);
});

// ---------------------------------------------------------------- APIs

test('URL and URLSearchParams', async () => {
  const r = await runPage('<body></body>');
  const v = r.run(`(function () {
    var u = new URL('../a/b?x=1&y=2#h', 'https://Example.test:443/dir/page.html');
    var out = [u.href, u.host, u.pathname, u.searchParams.get('y')];
    u.searchParams.append('z', 'a b&c');
    u.pathname = '/new';
    u.hash = '';
    out.push(u.href);
    var p = new URLSearchParams('?b=2&a=1&a=3');
    p.sort();
    out.push(p.toString(), p.getAll('a').join('+'), String(p.size));
    out.push(String(URL.canParse('nope')), String(new URL('http://h/') instanceof URL), new URLSearchParams({ q: "it's ~ok!" }).toString());
    return out.join(' | ');
  })()`);
  assert.equal(v, 'https://example.test/a/b?x=1&y=2#h | example.test | /a/b | 2 | https://example.test/new?x=1&y=2&z=a+b%26c | a=1&a=3&b=2 | 1+3 | 3 | false | true | q=it%27s+%7Eok%21');
});

test('TextEncoder/TextDecoder, atob/btoa, structuredClone, crypto', async () => {
  const r = await runPage('<body></body>');
  const v = r.run(`(function () {
    var enc = new TextEncoder().encode('héllo €');
    var out = [enc.length, new TextDecoder().decode(enc), new TextDecoder('latin1').decode(new Uint8Array([233])), btoa('hi'), atob('aGk=')];
    try { atob('*'); } catch (e) { out.push(e.name); }
    try { new TextDecoder('utf-8', { fatal: true }).decode(new Uint8Array([0xff])); } catch (e) { out.push(e.name); }
    var dec = new TextDecoder(), parts = '';
    var euro = new TextEncoder().encode('€');
    parts += dec.decode(euro.subarray(0, 1), { stream: true });
    parts += dec.decode(euro.subarray(1));
    out.push(parts);
    var src = { d: new Date(0), m: new Map([[1, { x: 2 }]]), s: new Set([3]), a: [1, 2] };
    src.self = src;
    var c = structuredClone(src);
    out.push(c !== src && c.self === c && c.m.get(1).x === 2 && c.s.has(3) && c.d.getTime() === 0);
    try { structuredClone(function () {}); } catch (e) { out.push(e.name); }
    out.push(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(crypto.randomUUID()));
    out.push(crypto.getRandomValues(new Uint8Array(8)).length);
    return out.join('|');
  })()`);
  assert.equal(v, '10|héllo €|é|aGk=|hi|InvalidCharacterError|TypeError|€|true|DataCloneError|true|8');
});

test('Blob, File, FileReader, FormData from a form, multipart bodies', async () => {
  const r = await runPage(`<body><form id="f"><input name="q" value="hello"><input type="checkbox" name="c" checked>
    <select name="s"><option>a</option><option selected>b</option></select></form><script>
    window.out = [];
    var b = new Blob(['ab', new Uint8Array([99])], { type: 'Text/Plain' });
    out.push(b.size + ':' + b.type);
    b.text().then(function (t) { out.push('text:' + t); });
    var fr = new FileReader();
    fr.onload = function () { out.push('reader:' + fr.result); };
    fr.readAsDataURL(new File(['hi'], 'h.txt', { type: 'text/plain' }));
    var fd = new FormData(document.getElementById('f'));
    out.push(Array.from(fd.entries()).map(function (e) { return e.join('='); }).join('&'));
    fetch('/up', { method: 'POST', body: fd });
    addEventListener('load', function () { setTimeout(function () { document.body.dataset.out = out.join(' '); }, 5); });
  </script></body>`, { routes: { '/up': 'ok' } });
  assert.match(r.out, /data-out="3:text\/plain q=hello&amp;c=on&amp;s=b text:abc reader:data:text\/plain;base64,aGk="/);
  const up = r.net.log.find((l) => l[0] === 'fetch' && l[2].endsWith('/up'));
  assert.match(up[5], /Content-Disposition: form-data; name="q"\r\n\r\nhello\r\n/);
  assert.ok(up[6].some((h) => h[0] === 'content-type' && /^multipart\/form-data; boundary=/.test(h[1])));
});

test('performance, console and WebSocket stubs', async () => {
  const r = await runPage(`<body><script>
    var t0 = performance.now();
    performance.mark('a');
    console.log('hello %s', 'world', { n: 1 });
    console.error(new Error('boom'));
    var ws = new WebSocket('wss://example.test/live');
    ws.onclose = function (e) { document.body.dataset.ws = ws.readyState + ':' + e.code; };
    setTimeout(function () { performance.mark('b'); document.body.dataset.p = (performance.now() >= t0) + ':' + performance.getEntriesByType('mark').length; }, 1000);
  </script></body>`);
  assert.match(r.out, /data-ws="3:1006"/);
  assert.match(r.out, /data-p="true:2"/);
  assert.ok(r.state.logs.some((l) => l[0] === 'log' && l[1] === 'hello world {n: 1}'));
  assert.ok(r.state.logs.some((l) => l[0] === 'error' && /boom/.test(l[1])));
});

test('stats report scripts, timers, fetches and text before and after', async () => {
  const r = await runPage('<body><p>before</p><script>setTimeout(function () { document.body.insertAdjacentHTML("beforeend", "<p>after the timer</p>"); }, 50); fetch("/x").catch(function () {});</script></body>');
  assert.equal(r.stats.scripts, 1);
  assert.equal(r.stats.timers, 1);
  assert.equal(r.stats.fetches, 1);
  assert.equal(r.stats.textBefore, 'before'.length);
  assert.equal(r.stats.textAfter, 'before'.length + 'after the timer'.length);
});

// ---------------------------------------------------------------- Intl (goja has none: force the shim here)

test('the Intl shim formats like en-US ICU', async () => {
  const page = await loadPage('<p></p>', { natives: { __forceIntl: true }, config: { timeZone: 'UTC' } });
  const cases = [
    "new Intl.NumberFormat('en-US').format(1234567.891)",
    "new Intl.NumberFormat('en-US', {maximumFractionDigits: 2}).format(1.005)",
    "new Intl.NumberFormat('en-US', {style: 'currency', currency: 'USD'}).format(-1234.5)",
    "new Intl.NumberFormat('en-US', {style: 'currency', currency: 'CHF'}).format(10)",
    "new Intl.NumberFormat('en-US', {style: 'percent'}).format(0.256)",
    "new Intl.NumberFormat('en-US', {notation: 'compact'}).format(1250000)",
    "new Intl.NumberFormat('en-US', {notation: 'compact'}).format(999999)",
    "new Intl.NumberFormat('en-US', {maximumSignificantDigits: 2}).format(0.0012345)",
    "new Intl.NumberFormat('en-US', {style: 'unit', unit: 'kilometer', unitDisplay: 'long'}).format(1)",
    "(1234.5).toLocaleString()",
    "new Intl.DateTimeFormat('en-US', {dateStyle: 'full', timeZone: 'UTC'}).format(Date.UTC(2026, 10, 9, 20, 5, 7))",
    "new Intl.DateTimeFormat('en-US', {timeStyle: 'short', timeZone: 'UTC'}).format(Date.UTC(2026, 10, 9, 20, 5, 7))",
    "new Intl.DateTimeFormat('en-US', {weekday: 'short', month: 'short', day: 'numeric', timeZone: 'UTC'}).format(Date.UTC(2026, 10, 9))",
    "new Intl.DateTimeFormat('en-US', {hour: '2-digit', minute: '2-digit', hour12: false, timeZone: 'UTC'}).format(Date.UTC(2026, 10, 9, 8, 5))",
    "new Date(Date.UTC(2026, 10, 9, 20, 5, 7)).toLocaleString('en-US', {timeZone: 'UTC'})",
    "new Intl.RelativeTimeFormat('en', {numeric: 'auto'}).format(-1, 'day')",
    "new Intl.RelativeTimeFormat('en').format(2, 'hours')",
    "new Intl.ListFormat('en').format(['a', 'b', 'c'])",
    "new Intl.PluralRules('en', {type: 'ordinal'}).select(22)",
    "['b', 'a', 'C', 'á', '10', '9'].sort(new Intl.Collator('en').compare).join()",
    "['item10', 'item9', 'item1'].sort(new Intl.Collator('en', {numeric: true}).compare).join()",
  ];
  for (const c of cases) {
    const real = (0, eval)(c);
    assert.equal(page.run(c), real, c);
  }
  assert.equal(page.run('new Intl.DateTimeFormat().resolvedOptions().timeZone'), 'UTC');
});

// ---------------------------------------------------------------- whole-app scenarios

test('scenario: a client-rendered app boots on DOMContentLoaded, fetches, renders from a template', async () => {
  const r = await runPage(`<!doctype html><html><head><title>App</title>
    <script src="/bundle.js" defer></script></head>
    <body><noscript>You need to enable JavaScript to run this app.</noscript><div id="root"></div>
    <template id="card"><article class="card"><h2></h2><p class="body"></p></article></template></body></html>`, {
    routes: {
      '/bundle.js': `
        (function () {
          function render(posts) {
            var root = document.getElementById('root');
            var tpl = document.getElementById('card');
            posts.forEach(function (p) {
              var node = tpl.content.cloneNode(true);
              node.querySelector('h2').textContent = p.title;
              node.querySelector('.body').textContent = p.body;
              root.appendChild(node);
            });
            document.title = posts.length + ' posts';
            history.replaceState(null, '', '/posts');
          }
          document.addEventListener('DOMContentLoaded', function () {
            var ch = new MessageChannel();
            ch.port1.onmessage = function () {
              fetch('/api/posts').then(function (r) { return r.json(); }).then(render);
            };
            ch.port2.postMessage(null);
          });
        })();`,
      '/api/posts': { body: JSON.stringify([{ title: 'First', body: 'Hello' }, { title: 'Second', body: 'World' }]), contentType: 'application/json' },
    },
  });
  assert.match(r.out, /<div id="root"><article class="card"><h2>First<\/h2><p class="body">Hello<\/p><\/article><article class="card"><h2>Second<\/h2>/);
  assert.match(r.out, /<title>2 posts<\/title>/);
  assert.equal(r.run('location.pathname'), '/posts');
});

test('scenario: webpack-style JSONP chunks loaded through dynamic script tags', async () => {
  const r = await runPage(`<body><div id="app"></div><script>
    var chunks = (self.webpackChunk = self.webpackChunk || []);
    var modules = {};
    chunks.push = function (c) { Object.assign(modules, c[1]); };
    function loadChunk(name) {
      return new Promise(function (resolve, reject) {
        var s = document.createElement('script');
        s.src = '/chunks/' + name + '.js';
        s.onload = resolve;
        s.onerror = reject;
        document.head.appendChild(s);
      });
    }
    Promise.all([loadChunk('a'), loadChunk('b')]).then(function () {
      document.getElementById('app').textContent = modules.a() + modules.b();
    });
  </script></body>`, {
    routes: {
      '/chunks/a.js': 'self.webpackChunk.push([["a"], { a: function () { return "A"; } }]);',
      '/chunks/b.js': 'self.webpackChunk.push([["b"], { b: function () { return "B"; } }]);',
    },
  });
  assert.match(r.out, /<div id="app">AB<\/div>/);
});

test('scenario: Vite-style entry waits for modulepreload links, then imports a lazy chunk', async () => {
  const r = await runPage(`<head><link rel="modulepreload" href="/assets/vendor.js">
    <script type="module" src="/assets/index.js"></script></head><body><div id="app"></div></body>`, {
    routes: {
      '/assets/index.js': `import { h } from './vendor.js';
const preload = (deps, load) => Promise.all(deps.map((d) => new Promise((ok, fail) => {
  const l = document.createElement('link'); l.rel = 'modulepreload'; l.href = d;
  l.addEventListener('load', ok); l.addEventListener('error', fail); document.head.appendChild(l);
}))).then(load);
preload(['/assets/page.js'], () => import('./page.js')).then((m) => {
  document.getElementById('app').appendChild(h('p', m.text));
});`,
      '/assets/vendor.js': 'export function h(tag, text) { const e = document.createElement(tag); e.textContent = text; return e; }',
      '/assets/page.js': 'export const text = "lazy page";',
    },
  });
  assert.match(r.out, /<div id="app"><p>lazy page<\/p><\/div>/);
  assert.ok(r.net.log.some((l) => l[0] === 'prefetch' && l.includes('https://example.test/assets/vendor.js')));
});

test('document.write of thousands of rows stays fast', async () => {
  const t0 = Date.now();
  const r = await runPage(`<body><script>
    document.write('<table id="big">');
    for (var i = 0; i < 3000; i++) document.write('<tr><td>' + i + '</td><td>row ' + i + '</td></tr>');
    document.write('</table>');
  </script></body>`);
  assert.equal(r.run("document.getElementById('big').rows.length"), 3000);
  assert.ok(Date.now() - t0 < 5000, 'took ' + (Date.now() - t0) + ' ms');
});

// ---------------------------------------------------------------- fixes after review

test('the next host call repairs what an interrupted one left (MutationObserver, error events, timers, storage)', async () => {
  const r = await runPage(`<body><div id="a"></div><script>
    window.seen = 0; window.errs = 0;
    new MutationObserver(function (recs) { seen += recs.length; }).observe(document.body, { childList: true, subtree: true });
    addEventListener('error', function () { errs++; });
    localStorage.setItem('k', 'v1');
  </script></body>`);
  const { page } = r;
  page.T.host.breakState();
  page.run("document.body.appendChild(document.createElement('p'))");
  await flush();
  assert.equal(page.run('seen'), 0, 'the delivery job was "dropped"');
  page.host.runTask(true);
  await flush();
  assert.equal(page.run('seen'), 1);
  assert.equal(page.T.host.busy(), false);
  page.run("setTimeout(function () { throw new Error('boom'); }, 0)");
  for (let i = 0; i < 3; i++) { page.host.runTask(true); await flush(); }
  assert.equal(page.run('errs'), 1, 'error events flow again');
  page.run("localStorage.setItem('k', 'v2')");
  await flush();
  assert.match(page.state.storage.get('local'), /v2/);
});

test('budgets start again at load: an interval before load does not starve the load handler', async () => {
  const r = await runPage(`<body><main id="m" style="visibility:hidden">Main</main><script>
    setInterval(function () {}, 1000);
    addEventListener('load', function () { setTimeout(function () { m.style.visibility = 'visible'; }, 400); });
  </script></body>`);
  assert.match(r.out, /<main id="m" style="visibility: visible;">/);
});

test('a poll that never succeeds wears out its own chain, not the page', async () => {
  const r = await runPage(`<body><main id="m" style="opacity:0">Main</main><script>
    (function wait() { if (!window.ga) { setTimeout(wait, 10); return; } })();
    addEventListener('load', function () { setTimeout(function () { m.style.opacity = 1; }, 0); });
  </script></body>`);
  assert.match(r.out, /<main id="m" style="opacity: 1;">/);
  assert.ok(r.stats.timers < 400, 'timers ' + r.stats.timers);
});

test('an endless requestAnimationFrame loop does not keep a later timer from running', async () => {
  const r = await runPage(`<body><div id="box">x</div><script>
    (function tick() { requestAnimationFrame(tick); })();
    setTimeout(function () { document.getElementById('box').dataset.shown = 'yes'; }, 2000);
    requestAnimationFrame(function () { requestAnimationFrame(function () { document.body.dataset.double = 'raf'; }); });
  </script></body>`);
  assert.match(r.out, /data-shown="yes"/);
  assert.match(r.out, /data-double="raf"/);
});

test('what a page does long after load is not fast-forwarded (a 20 s redirect, a 45 s swap)', async () => {
  const r = await runPage(`<body><article><p>Article</p></article><script>
    setTimeout(function () { location.href = '/next-article'; }, 20000);
    setTimeout(function () { document.body.innerHTML = '<h1>Your session has expired</h1>'; }, 45000);
  </script></body>`);
  assert.match(r.out, /<p>Article<\/p>/);
  assert.equal(r.state.navigations.length, 0);
});

test('modules: a dependency that throws keeps its importers from running', async () => {
  const r = await runPage(`<body><script type="module">import '/dep.js'; document.body.dataset.ran = 'yes';</script></body>`, {
    routes: { '/dep.js': "document.body.dataset.dep = 'started'; throw new Error('dep failed');" },
  });
  assert.match(r.out, /data-dep="started"/);
  assert.doesNotMatch(r.out, /data-ran/);
  assert.ok(r.stats.errors.some((e) => /dep failed/.test(e)), JSON.stringify(r.stats.errors));
});

test('modules: a cycle back to the entry (Vite preload helper) gets a forwarding binding', async () => {
  const r = await runPage(`<body><script type="module" src="/assets/index.js"></script></body>`, {
    routes: {
      '/assets/index.js': `import { v } from './vendor.js';
export const _ = function preload(fn) { return fn(); };
document.body.dataset.v = v();`,
      '/assets/vendor.js': `import { _ as e } from './index.js';
export function v() { return e(function () { return 'via helper'; }); }`,
    },
  });
  assert.match(r.out, /data-v="via helper"/);
});

test('modules: dynamic import with a JSON type, and import.meta.url after a redirect', async () => {
  const routes = {
    '/data.json': '{"a": 42}',
    '/m.js': 'document.body.dataset.u = import.meta.url;',
  };
  const r = await runPage(`<body><script type="module">
    import '/m.js';
    import('/data.json', { with: { type: 'json' } }).then(function (m) { document.body.dataset.j = m.default.a; });
  </script></body>`, {
    routes,
    natives: {
      fetchScriptSync: (url) => {
        const p = new URL(url).pathname;
        if (p === '/m.js') return { code: routes['/m.js'], url: 'https://example.test/final/m.js', error: null };
        if (p === '/data.json') return { code: routes['/data.json'], url, error: null };
        return { code: null, url, error: 'not found' };
      },
    },
  });
  assert.match(r.out, /data-u="https:\/\/example.test\/final\/m.js"/);
  assert.match(r.out, /data-j="42"/);
});

// SEC-25: a classic script's error is the page's to read only when the script
// came from its own origin; another origin's script reports "Script error."
// with no file, line or column, while the debug log keeps the real text.
test('a script error from another origin is redacted, a same-origin one is not', async () => {
  const r = await runPage(`<!doctype html><html><body>
    <script>window.seen = []; window.addEventListener('error', function (e) { seen.push({ m: e.message, f: e.filename, l: e.lineno, c: e.colno }); });</script>
    <script src="/own.js"></script>
    <script async src="https://cdn.example/remote.js"></script>
  </body></html>`, {
    routes: {
      '/own.js': { body: 'throw new Error("same origin detail")' },
      'https://cdn.example/remote.js': { body: 'throw new Error("remote detail")' },
    },
  });
  const seen = JSON.parse(r.run('JSON.stringify(seen)'));
  assert.equal(seen.length, 2);
  assert.equal(seen[0].m, 'Uncaught same origin detail');
  assert.equal(seen[0].f, BASE + 'own.js');
  // goja reports the script's first line as 0; the same-origin error keeps
  // whatever position the runtime gave it, the opaque one is pinned to 0.
  assert.equal(seen[0].l, 0);
  assert.ok(seen[0].c >= 0);
  assert.equal(seen[1].m, 'Script error.');
  assert.equal(seen[1].f, '');
  assert.equal(seen[1].l, 0);
  assert.equal(seen[1].c, 0);
});
