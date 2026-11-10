// webdom_harness.mjs — loads the browser's JavaScript DOM prelude (player/webdom/*.js) into a node `vm`
// context with mock natives, so the DOM the handheld runs page scripts against can be tested without goja.
//
// The Go side (player/webdom_runtime.go) concatenates the same files in file-name order inside the same
// wrapper, so a test here exercises the exact source the device runs. The natives below stand in for the Go
// functions on `__jhNative`: the HTML parser is replaced by parse5 (converted to the JSON tree format the Go
// parser emits, see outputs/webdom/DESIGN.md "P"), scripts run with vm.runInContext, and the clock is a
// virtual one that `advance` moves forward.
//
// Microtasks: goja drains promise jobs after every top-level Go→JS call. Here the context shares node's
// microtask queue, so `flush()` (one setImmediate) after each host call gives the same ordering. Do not use
// microtaskMode 'afterEvaluate': it would also drain inside nested runInContext calls, which goja never does.

import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import crypto from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { parse, parseFragment, defaultTreeAdapter } from 'parse5';

const here = path.dirname(fileURLToPath(import.meta.url));
export const webdomDir = process.env.WEBDOM_DIR || path.resolve(here, '../../player/webdom');

const NS_HTML = 'http://www.w3.org/1999/xhtml';
const NS_SVG = 'http://www.w3.org/2000/svg';
const NS_MATH = 'http://www.w3.org/1998/Math/MathML';

/** The prelude exactly as Go builds it. `filter(name)` keeps only some parts (e.g. the DOM core). */
export function preludeSource(dir = webdomDir, filter = null) {
  const files = fs.readdirSync(dir).filter((f) => f.endsWith('.js')).sort();
  const parts = files.filter((f) => !filter || filter(f)).map((f) => fs.readFileSync(path.join(dir, f), 'utf8'));
  return "(function (global, N) { 'use strict';\n" + parts.join('\n') + '\n})(this, this.__jhNative);';
}

/** Only the DOM core parts (10–59), for tests that must not depend on the environment parts. */
export const domPartsOnly = (name) => /^[1-5][0-9]_/.test(name);

function convertNodes(nodes) {
  const out = [];
  for (const n of nodes) {
    switch (n.nodeName) {
      case '#text':
        out.push(n.value);
        break;
      case '#comment':
        out.push([8, n.data]);
        break;
      case '#documentType':
        out.push([10, n.name || '', n.publicId || '', n.systemId || '']);
        break;
      default: {
        const attrs = n.attrs && n.attrs.length
          ? n.attrs.flatMap((a) => [a.prefix ? a.prefix + ':' + a.name : a.name, a.value])
          : 0;
        const kids = n.tagName === 'template' && n.content ? n.content.childNodes : n.childNodes;
        const children = kids && kids.length ? convertNodes(kids) : 0;
        const el = [n.tagName, attrs, children];
        if (n.namespaceURI === NS_SVG) el.push(1);
        else if (n.namespaceURI === NS_MATH) el.push(2);
        out.push(el);
      }
    }
  }
  return out;
}

/** HTML document → the JSON tree string the Go parser produces. */
export function htmlToTree(html) {
  return JSON.stringify(convertNodes(parse(String(html), { scriptingEnabled: true }).childNodes));
}

/** Fragment in the context of an element (ns 0 HTML, 1 SVG, 2 MathML) → JSON tree string. */
export function fragmentToTree(html, context = 'body', ns = 0) {
  const nsURI = ns === 1 ? NS_SVG : ns === 2 ? NS_MATH : NS_HTML;
  const ctxEl = defaultTreeAdapter.createElement(String(context), nsURI, []);
  const frag = parseFragment(ctxEl, String(html), { scriptingEnabled: true });
  return JSON.stringify(convertNodes(frag.childNodes));
}

function bytesOf(buf) {
  if (buf == null) return new Uint8Array(0);
  if (ArrayBuffer.isView(buf)) return new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength);
  // ArrayBuffer from either realm.
  return new Uint8Array(buf);
}

function parseCookieLine(line) {
  const first = String(line).split(';')[0];
  const eq = first.indexOf('=');
  if (eq < 0) return null;
  const name = first.slice(0, eq).trim();
  const value = first.slice(eq + 1).trim();
  const expired = /;\s*(max-age\s*=\s*(0|-\d+)|expires\s*=\s*thu, 01 jan 1970)/i.test(line);
  return name ? { name, value, expired } : null;
}

/**
 * Mock natives. Everything the Go side provides on __jhNative, minus the network/module parts F2's tests
 * override through `natives`. `state` is returned for assertions (logs, navigations, cookies, storage).
 */
export function makeNatives() {
  const state = {
    offset: 0,
    t0: performance.now(),
    logs: [],
    cookies: new Map(),
    storage: new Map(),
    navigations: [],
    ctx: null,
    realmAB: null,
    realmU8: null,
  };
  const toAB = (bytes) => {
    const AB = state.realmAB || ArrayBuffer;
    const U8 = state.realmU8 || Uint8Array;
    const ab = new AB(bytes.length);
    new U8(ab).set(bytes);
    return ab;
  };
  const N = {
    __test: true,
    __state: state,
    parseDocument: (html) => htmlToTree(String(html)),
    parseFragment: (html, context, ns) => fragmentToTree(String(html), String(context || 'body'), ns | 0),
    // Success → the script's completion value; exception → { jhError: true, error, message } (notes-F2.md).
    runScript: (code, name) => {
      try {
        return vm.runInContext(String(code), state.ctx, { filename: String(name || 'script.js') });
      } catch (e) {
        return { jhError: true, error: e, message: String(e && e.message !== undefined ? e.message : e) };
      }
    },
    perfNow: () => performance.now() - state.t0 + state.offset,
    now: () => Date.now() + state.offset,
    advance: (ms) => {
      state.offset += Math.max(0, Number(ms) || 0);
      return performance.now() - state.t0 + state.offset;
    },
    log: (level, msg) => {
      state.logs.push([String(level), String(msg)]);
    },
    utf8Encode: (s) => toAB(Buffer.from(String(s), 'utf8')),
    // (buf, label, ignoreBOM, fatal) → string, or null when fatal and the bytes are malformed.
    utf8Decode: (buf, label, ignoreBOM, fatal) => {
      const enc = /^(latin1|iso-8859-1|windows-1252|ascii|us-ascii)$/i.test(String(label || '')) ? 'latin1'
        : /^utf-16(le)?$/i.test(String(label || '')) ? 'utf-16le' : 'utf-8';
      try {
        return new TextDecoder(enc, { fatal: !!fatal, ignoreBOM: !!ignoreBOM }).decode(bytesOf(buf));
      } catch {
        return null;
      }
    },
    // null = InvalidCharacterError (the prelude throws).
    atob: (s) => {
      const t = String(s).replace(/[\t\n\f\r ]/g, '');
      if (/[^A-Za-z0-9+/=]/.test(t) || t.length % 4 === 1 || /=[^=]/.test(t)) return null;
      return Buffer.from(t, 'base64').toString('latin1');
    },
    btoa: (s) => {
      const t = String(s);
      if (/[^\u0000-\u00ff]/.test(t)) return null;
      return Buffer.from(t, 'latin1').toString('base64');
    },
    randomBytes: (n) => toAB(crypto.randomBytes(Math.max(0, Math.min(65536, n | 0)))),
    urlParse: (input, base) => {
      try {
        const u = base === undefined || base === null || base === '' ? new URL(String(input)) : new URL(String(input), String(base));
        return JSON.stringify({
          href: u.href, protocol: u.protocol, username: u.username, password: u.password, host: u.host,
          hostname: u.hostname, port: u.port, pathname: u.pathname, search: u.search, hash: u.hash, origin: u.origin,
        });
      } catch {
        return '';
      }
    },
    cookieGet: () => [...state.cookies].map(([k, v]) => k + '=' + v).join('; '),
    cookieSet: (line) => {
      const c = parseCookieLine(line);
      if (!c) return;
      if (c.expired) state.cookies.delete(c.name);
      else state.cookies.set(c.name, c.value);
    },
    storageGet: (kind) => state.storage.get(String(kind)) || '',
    storageSet: (kind, json) => {
      state.storage.set(String(kind), String(json));
      return true;
    },
    navigate: (url, replace) => {
      state.navigations.push([String(url), !!replace]);
      return true;
    },
  };
  return N;
}

export function flush() {
  return new Promise((resolve) => setImmediate(resolve));
}

/**
 * Builds a page: creates the context, runs the prelude, boots the document from `html`.
 * options: url, natives (overrides merged into the mocks), config (object → JSON for host.boot),
 * filter (part-name filter, e.g. domPartsOnly), useHost (default true: host.boot when __jhHost exists,
 * else the test hook bootDocument), boot (false → do not boot at all).
 */
export async function loadPage(html, options = {}) {
  const url = options.url || 'https://example.test/dir/page.html';
  const N = makeNatives();
  Object.assign(N, options.natives || {});
  const sandbox = { __jhNative: N };
  const ctx = vm.createContext(sandbox);
  // goja has no Intl, WeakRef, FinalizationRegistry or Array.fromAsync (and no SharedArrayBuffer, Atomics or
  // WebAssembly). The prelude ships stand-ins for the first four; removing V8's copies here makes every test
  // run against the global set the handheld really has, so a part that leans on a V8-only built-in fails in
  // node instead of on the device. options.v8Globals keeps them (for comparisons with node's own output).
  if (!options.v8Globals) {
    vm.runInContext(`(function () {
      var names = ['Intl', 'WeakRef', 'FinalizationRegistry', 'SharedArrayBuffer', 'Atomics', 'WebAssembly'];
      for (var i = 0; i < names.length; i++) { try { delete globalThis[names[i]]; } catch (e) { /* keep */ } }
      try { delete Array.fromAsync; } catch (e) { /* keep */ }
    })()`, ctx);
  }
  N.__state.ctx = ctx;
  N.__state.realmAB = vm.runInContext('ArrayBuffer', ctx);
  N.__state.realmU8 = vm.runInContext('Uint8Array', ctx);
  // The Go side runs Date on the virtual clock (goja's SetTimeSource), so a timer the loop skipped to sees
  // the time move. The context's Date gets the same offset here, or timing code (jQuery animations, "did
  // 300 ms pass?") would look stuck at its first frame in node only.
  vm.runInContext(`(function (off) {
    var RealDate = Date;
    class VDate extends RealDate {
      constructor(...a) { if (a.length === 0) super(RealDate.now() + off()); else super(...a); }
      static now() { return RealDate.now() + off(); }
    }
    var P = new Proxy(VDate, { apply: function () { return new VDate().toString(); } });
    Object.defineProperty(globalThis, 'Date', { value: P, writable: true, configurable: true });
  })`, ctx)(() => N.__state.offset);
  vm.runInContext(preludeSource(options.dir || webdomDir, options.filter || null), ctx, { filename: 'webdom.js' });
  const host = sandbox.__jhHost || null;
  const T = sandbox.__jhTest || null;
  let bootResult = null;
  if (options.boot !== false) {
    const tree = htmlToTree(html);
    if (host && typeof host.boot === 'function' && options.useHost !== false) {
      bootResult = host.boot(tree, url, JSON.stringify(options.config || {}));
    } else if (T && T.bootDocument) {
      T.bootDocument(tree, url);
    }
  }
  await flush();
  return {
    ctx,
    N,
    host,
    T,
    state: N.__state,
    bootResult,
    run: (code) => vm.runInContext(String(code), ctx, { filename: 'test.js' }),
    flush,
    serialize: () => (host && typeof host.serialize === 'function' && options.useHost !== false
      ? host.serialize()
      : T.serializeForRenderer(T.mainDoc)),
  };
}
