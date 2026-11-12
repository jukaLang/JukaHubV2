// 85_scripts.js — running the page's scripts.
//
// Parser-inserted classic scripts run in document order (Go fetches the external ones side by side, then
// hands them over one at a time). document.write from such a script is buffered and inserted after it —
// complete markup at once, so "write a <div id=x>, then fill #x" works; an open <table> waits for its rows —
// and the scripts it writes run next. Deferred classic scripts and module scripts run after the parser phase,
// in order; async and dynamically inserted scripts run when their download lands (in insertion order when a
// script asked for async=false). Each script element runs at most once.
//
// Classic code always runs through N.runScript (Go's RunScript), so every script shares the one global lexical
// scope a browser gives them. Modules go through webdom_module.go, which turns import/export (goja has
// neither) into a function the loader below calls with live export getters; graphs are fetched synchronously
// (N.fetchScriptSync, prefetched breadth-first) and evaluated dependencies first.

var SCR_JS_TYPES = objCreate(null);
['application/ecmascript', 'application/javascript', 'application/x-ecmascript', 'application/x-javascript', 'text/ecmascript',
  'text/javascript', 'text/javascript1.0', 'text/javascript1.1', 'text/javascript1.2', 'text/javascript1.3',
  'text/javascript1.4', 'text/javascript1.5', 'text/jscript', 'text/livescript', 'text/x-ecmascript',
  'text/x-javascript'].forEach(function (t) { SCR_JS_TYPES[t] = true; });

var SCR_WRITE_MAX = 1 << 20;   // document.write output one script may add

var scrById = [];
var scrParserQueue = [];
var scrParserPos = 0;
var scrDeferQueue = [];
var scrDeferPos = 0;
var scrParsing = true;
var scrWrite = null;
var scrOrdered = [];
var scrStats = { run: 0, failed: 0 };
var scrPrefetch = { scripts: [], modules: [] };

function scrKind(r) {
  var type = getAttr(r, 'type');
  var t;
  if (type === null) {
    var lang = getAttr(r, 'language');
    t = lang !== null && lang !== '' ? 'text/' + asciiLower(lang) : '';
  } else {
    t = asciiLower(stripWS(type));
  }
  if (t === '' || SCR_JS_TYPES[t]) return 'classic';
  if (t === 'module') return 'module';
  if (t === 'importmap') return 'importmap';
  return null;
}

function scrId(r) {
  if (r.sid === undefined) {
    r.sid = scrById.length;
    scrById.push(r);
  }
  return r.sid;
}

function scrQueueEvent(r, type) {
  loopQueueTask(function () { fireEvent(wrap(r), type, {}, Event); });
}

// scrIsError recognises the descriptor N.runScript returns for an exception ({jhError, error, message}).
// Only the marker counts: a script whose completion value happens to be {error, message} ran fine.
function scrIsError(res) {
  return !!res && typeof res === 'object' && res.jhError === true;
}

function scrReportResult(res, name, r) {
  scrStats.failed++;
  var err = res.error;
  var msg = str(res.message || (err && err.message) || 'Script error.');
  // The debug log keeps the real text, the page does not: a classic script
  // from another origin reports "Script error." with no file, line or column,
  // as browsers do (SEC-25). The error's text and the file's address are the
  // other origin's data.
  hostNoteError(msg);
  if (envReporting > 0) return;
  // The URL the runtime named the script with is enough on its own: an inline
  // script reports the document's URL and stays readable, a downloaded one
  // from another origin does not.
  var opaque = scrCrossOrigin(str(name || '')) || !!(r && r.xo);
  var shown = opaque ? 'Script error.' : 'Uncaught ' + msg;
  envReporting++;
  try {
    var ev = fireEvent(global, 'error', {
      message: shown, filename: opaque ? '' : str(name || ''), lineno: opaque ? 0 : res.line | 0,
      colno: opaque ? 0 : res.col | 0, error: opaque ? null : err, cancelable: true
    }, ErrorEvent);
    if (!ev || !ev.defaultPrevented) N.log('error', msg);
  } catch (e) {
    try { N.log('error', msg); } catch (x) { /* nothing */ }
  } finally {
    envReporting--;
  }
}

// scrCrossOrigin reports whether a script URL is served by another origin
// than the document, which is what makes its errors opaque (SEC-25).
function scrCrossOrigin(u) {
  if (!u) return false;
  // Scheme and authority only: the prelude's URL parser is a host call, and an
  // error report must not depend on one succeeding.
  return scrOriginOf(mainDoc.url) !== scrOriginOf(u);
}

function scrOriginOf(u) {
  var m = /^([a-z][a-z0-9+.-]*:)(\/\/[^\/?#]*)?/i.exec(str(u));
  return m ? asciiLower(m[1] + (m[2] || '')) : 'null';
}

// ---------------------------------------------------------------- classification

function scrStartAsync(r, module) {
  var url = resolveURL(mainDoc, getAttr(r, 'src'));
  r.st0 = true;
  if (!url) {
    scrQueueEvent(r, 'error');
    return;
  }
  if (!N.loadScript(scrId(r), url, module)) scrQueueEvent(r, 'error');
}

// scrPlace files a parser-inserted script (from the page or from document.write) where it belongs: the
// parser queue (at `at`, so written scripts run right after their writer), the deferred list, or an async
// download. It reports whether it went into the parser queue.
function scrPlace(r, at) {
  var kind = scrKind(r);
  if (!kind) {
    r.st0 = true;
    return false;
  }
  if (kind === 'importmap') {
    r.st0 = true;
    if (getAttr(r, 'src') === null) scrImportMap(childText(r));
    return false;
  }
  var src = getAttr(r, 'src');
  if (kind === 'module') {
    if (src !== null && hasAttr(r, 'async')) {
      scrStartAsync(r, true);
      return false;
    }
    scrDeferQueue.push(r);
    if (src !== null) {
      var mu = resolveURL(mainDoc, src);
      if (mu) scrPrefetch.modules.push(mu);
    }
    return false;
  }
  if (hasAttr(r, 'nomodule')) {
    // Module scripts run here, so the legacy bundle a page ships for old browsers stays unused.
    r.st0 = true;
    return false;
  }
  if (src !== null && hasAttr(r, 'async')) {
    scrStartAsync(r, false);
    return false;
  }
  if (src !== null) {
    var u = resolveURL(mainDoc, src);
    if (u) scrPrefetch.scripts.push(u);
    if (hasAttr(r, 'defer')) {
      scrDeferQueue.push(r);
      return false;
    }
  }
  scrParserQueue.splice(at, 0, r);
  return true;
}

// scrBoot files every script the parser met, collects the modulepreload hints, and returns nothing: the
// prefetch lists go back to Go from host.boot.
function scrBoot() {
  var d = mainDoc;
  for (var r = d.f; r; r = nextNode(r, d)) {
    if (r.t !== 1 || r.ns !== 0) continue;
    if (r.ln === 'script') {
      if (r.pi && !r.st0) scrPlace(r, scrParserQueue.length);
    } else if (r.ln === 'link') {
      var rel = ' ' + asciiLower(str(getAttr(r, 'rel') || '')) + ' ';
      var href = getAttr(r, 'href');
      if (href && rel.indexOf(' modulepreload ') >= 0) {
        var u = resolveURL(mainDoc, href);
        if (u) scrPrefetch.modules.push(u);
      }
    }
  }
}

function scrNextFrom(queue, posName) {
  for (;;) {
    var pos = posName === 'parser' ? scrParserPos : scrDeferPos;
    if (pos >= queue.length) return null;
    var r = queue[pos];
    if (posName === 'parser') scrParserPos++;
    else scrDeferPos++;
    if (r.st0) continue;
    // A parser-blocking script a script removed before the parser reached it never runs.
    if (posName === 'parser' && !r.c) continue;
    var src = getAttr(r, 'src');
    var url = '';
    if (src !== null) {
      url = resolveURL(mainDoc, src);
      if (!url) {
        r.st0 = true;
        scrQueueEvent(r, 'error');
        continue;
      }
    }
    return { id: scrId(r), src: url, module: scrKind(r) === 'module' };
  }
}

function scrNextParser() { return scrNextFrom(scrParserQueue, 'parser'); }

function scrNextDeferred() {
  scrParsing = false;
  return scrNextFrom(scrDeferQueue, 'defer');
}

// host.execScript: a script from either queue, with its code (null for an inline script) or the reason the
// download failed.
function scrExec(id, code, finalURL, errText) {
  var r = scrById[id];
  if (!r || r.st0) return;
  r.st0 = true;
  var external = getAttr(r, 'src') !== null;
  if (external) r.xo = scrCrossOrigin(str(finalURL || ''));
  if (errText !== null && errText !== undefined) {
    fireEvent(wrap(r), 'error', {}, Event);
    return;
  }
  if (scrKind(r) === 'module') {
    scrRunModuleElement(r, code, finalURL);
    return;
  }
  var text = code === null || code === undefined ? childText(r) : str(code);
  scrRunClassic(r, text, external ? str(finalURL || '') : mainDoc.url, true);
  if (external) fireEvent(wrap(r), 'load', {}, Event);
}

// ---------------------------------------------------------------- classic scripts and document.write

function scrRunClassic(r, code, name, viaParser) {
  if (code === '') return;
  var d = mainDoc;
  var prevCur = d.cur;
  var prevWrite = scrWrite;
  var writes = viaParser && scrParsing ? { r: r, text: '', total: 0, depth: 0, carry: '', raw: '', cursor: r, parent: r.p, next: r.n } : null;
  scrWrite = writes;
  d.cur = r;
  if (scrUsesImport(code)) {
    try { code = scrBindImport(str(N.rewriteDynamicImport(code)), name || d.url); } catch (e) { /* run it as it is */ }
  }
  var res;
  try {
    res = N.runScript(code, name || d.url);
  } finally {
    d.cur = prevCur;
    scrWrite = prevWrite;
  }
  scrStats.run++;
  if (scrIsError(res)) scrReportResult(res, name, r);
  if (writes && writes.text) scrFlushWrites(writes);
}

// scrUsesImport is a cheap pre-check for import() / import.meta in a classic script: goja cannot parse either,
// so such a script goes through webdom_module.go's rewrite first. indexOf keeps it linear and fast on a
// multi-megabyte bundle; the rewrite itself lexes properly (strings and comments stay untouched).
function scrUsesImport(code) {
  for (var at = code.indexOf('import'); at >= 0; at = code.indexOf('import', at + 6)) {
    var before = at > 0 ? code.charCodeAt(at - 1) : 32;
    if ((before >= 48 && before <= 57) || (before >= 65 && before <= 90) || (before >= 97 && before <= 122) || before === 36 || before === 95) continue;
    var i = scrSkipSpace(code, at + 6);
    if (code.charAt(i) === '(' || code.substr(i, 5) === '.meta') return true;
  }
  return false;
}
// scrSkipSpace steps over white space and comments (`import /* lazy */ ("./x.js")`).
function scrSkipSpace(code, i) {
  for (;;) {
    var c = code.charCodeAt(i);
    if (c === 32 || c === 9 || c === 10 || c === 13) {
      i++;
    } else if (c === 47 && code.charCodeAt(i + 1) === 42) {
      var end = code.indexOf('*/', i + 2);
      if (end < 0) return code.length;
      i = end + 2;
    } else if (c === 47 && code.charCodeAt(i + 1) === 47) {
      var nl = code.indexOf('\n', i + 2);
      if (nl < 0) return code.length;
      i = nl + 1;
    } else {
      return i;
    }
  }
}

hooks.write = function (docRec, text) {
  var w = scrWrite;
  if (docRec !== mainDoc || !w) return false;
  var s = str(text);
  if (w.total + s.length > SCR_WRITE_MAX) return true;
  w.text += s;
  w.total += s.length;
  scrBalanceFeed(w, s);
  if (w.depth <= 0 && !w.carry && !w.raw) scrFlushWrites(w);
  return true;
};

var SCR_VOID = objCreate(null);
['area', 'base', 'basefont', 'bgsound', 'br', 'col', 'embed', 'frame', 'hr', 'image', 'img', 'input', 'keygen', 'link',
  'meta', 'param', 'source', 'track', 'wbr'].forEach(function (t) { SCR_VOID[t] = true; });

// scrBalanceFeed tracks, write by write, whether the markup written so far closes every element it opens, so
// it can be parsed now rather than together with the writes that follow. Only the new text is scanned (a table
// written cell by cell stays linear); a tag split across two writes is carried over.
function scrBalanceFeed(w, s) {
  var text = w.carry + s;
  w.carry = '';
  var i = 0;
  while (i < text.length) {
    if (w.raw) {
      var close = text.toLowerCase().indexOf('</' + w.raw, i);
      if (close < 0) {
        w.carry = text.slice(Math.max(i, text.length - w.raw.length - 1));
        return;
      }
      w.raw = '';
      i = close;
      continue;
    }
    var lt = text.indexOf('<', i);
    if (lt < 0) return;
    if (text.substr(lt, 4) === '<!--') {
      var ce = text.indexOf('-->', lt + 4);
      if (ce < 0) {
        w.carry = text.slice(lt);
        return;
      }
      i = ce + 3;
      continue;
    }
    var gt = text.indexOf('>', lt);
    if (gt < 0) {
      w.carry = text.slice(lt);
      return;
    }
    var m = /^<(\/?)([a-zA-Z][^\s\/>]*)[^>]*?(\/?)>$/.exec(text.slice(lt, gt + 1));
    if (m) {
      var tag = m[2].toLowerCase();
      if (m[1]) {
        w.depth--;
      } else if (!m[3] && !SCR_VOID[tag]) {
        w.depth++;
        if (tag === 'script' || tag === 'style' || tag === 'textarea' || tag === 'title' || tag === 'xmp') w.raw = tag;
      }
    }
    i = gt + 1;
  }
}

function scrBalanced(html) {
  var w = { carry: '', raw: '', depth: 0 };
  scrBalanceFeed(w, html);
  return w.depth <= 0 && !w.carry && !w.raw;
}

var SCR_HEAD_TAGS = { meta: 1, link: 1, script: 1, style: 1, title: 1, base: 1, noscript: 1, template: 1 };

function scrHeadContent(rec) {
  if (rec.t === 8) return true;
  if (rec.t === 3) return /^[\t\n\f\r ]*$/.test(rec.x);
  return rec.t === 1 && rec.ns === 0 && SCR_HEAD_TAGS[rec.ln] === 1;
}

function scrBody() {
  var html = mainDoc.f;
  while (html && html.t !== 1) html = html.n;
  if (!html) return null;
  for (var k = html.f; k; k = k.n) if (k.t === 1 && k.ns === 0 && k.ln === 'body') return k;
  return null;
}

function scrFlushWrites(w) {
  var html = w.text;
  w.text = '';
  w.depth = 0;
  w.carry = '';
  w.raw = '';
  if (!html) return;
  var parent, before;
  if (w.cursor && w.cursor.p) {
    parent = w.cursor.p;
    before = w.cursor.n;
  } else {
    parent = w.parent;
    before = w.next && w.next.p === parent ? w.next : null;
  }
  if (!parent || !parent.c) return;
  var ctx = parent.t === 1 ? parent : scrBody();
  if (!ctx) return;
  var recs = parseFragmentRecs(ctx, html, 'write');
  var inHead = parent.t === 1 && parent.ns === 0 && parent.ln === 'head';
  var body = inHead ? scrBody() : null;
  // Body content written from <head> goes to the start of <body>, after what earlier writes put there.
  var bodyRef = body ? (w.bodyCursor && w.bodyCursor.p === body ? w.bodyCursor.n : body.f) : null;
  var written = [];
  for (var i = 0; i < recs.length; i++) {
    var rec = recs[i];
    if (inHead && body && !scrHeadContent(rec)) {
      insertNode(body, rec, bodyRef);
      w.bodyCursor = rec;
    } else {
      insertNode(parent, rec, before);
      w.cursor = rec;
    }
    written.push(rec);
  }
  // The scripts the writes added run next, in order, ahead of the rest of the page.
  var at = scrParserPos;
  for (var j = 0; j < written.length; j++) {
    for (var n = written[j]; n; n = nextNode(n, written[j])) {
      if (n.t === 1 && n.ns === 0 && n.ln === 'script' && n.pi && !n.st0) {
        if (scrPlace(n, at)) at++;
      }
    }
  }
}

// ---------------------------------------------------------------- dynamic scripts

function scrPrepareDynamic(r) {
  if (!r.c || r.st0 || r.pi) return;
  var kind = scrKind(r);
  if (!kind) return;
  var src = getAttr(r, 'src');
  if (src === null) {
    var code = childText(r);
    if (code === '') return; // it runs when its text arrives (hooks.childrenChanged)
    r.st0 = true;
    if (kind === 'importmap') {
      scrImportMap(code);
      return;
    }
    if (kind === 'module') {
      loopQueueTask(function () { scrRunModuleElement(r, code, null); });
      return;
    }
    if (hasAttr(r, 'nomodule')) return;
    scrRunClassic(r, code, mainDoc.url, false);
    return;
  }
  r.st0 = true;
  if (kind === 'importmap' || (kind === 'classic' && hasAttr(r, 'nomodule'))) return;	var url = resolveURL(mainDoc, src);
  if (!url) {
    scrQueueEvent(r, 'error');
    return;
  }
  r.xo = scrCrossOrigin(url);
  var ordered = kind === 'classic' && !r.fa && !hasAttr(r, 'async');
  if (ordered) {
    r.ord = true;
    scrOrdered.push(r);
  }
  if (!N.loadScript(scrId(r), url, kind === 'module')) {
    if (ordered) {
      r.loaded = { code: null, url: url, err: 'refused' };
      scrDrainOrdered();
    } else {
      scrQueueEvent(r, 'error');
    }
  }
}

function scrDrainOrdered() {
  while (scrOrdered.length && scrOrdered[0].loaded) {
    var r = scrOrdered.shift();
    var l = r.loaded;
    r.loaded = null;
    scrRunLoaded(r, l.code, l.url, l.err);
  }
}

// host.scriptLoaded: an async or dynamic script's download landed (or failed).
function scrLoaded(id, code, finalURL, errText) {
  var r = scrById[id];
  if (!r) return;
  r.xo = scrCrossOrigin(str(finalURL || ''));
  if (r.ord) {
    r.loaded = { code: code, url: finalURL, err: errText };
    scrDrainOrdered();
    return;
  }
  scrRunLoaded(r, code, finalURL, errText);
}

function scrRunLoaded(r, code, finalURL, errText) {
  if ((errText !== null && errText !== undefined) || code === null || code === undefined) {
    fireEvent(wrap(r), 'error', {}, Event);
    return;
  }
  if (scrKind(r) === 'module') {
    scrRunModuleElement(r, str(code), str(finalURL || ''));
    return;
  }
  scrRunClassic(r, str(code), str(finalURL || ''), false);
  fireEvent(wrap(r), 'load', {}, Event);
}

// ---------------------------------------------------------------- element hooks

// Images, frames and stylesheet links "load" as far as the page can tell: the reader fetches images itself
// after the pass, and a page that waits for an onload (lazy loaders, Vite's preload helper, loadCSS) must not
// wait forever.
function scrImageLoad(r) {
  var src = getAttr(r, 'src'), srcset = getAttr(r, 'srcset');
  if (!src && !srcset) return;
  var key = str(src || '') + ' ' + str(srcset || '');
  if (r.ild === key) return;
  r.ild = key;
  loopQueueTask(function () {
    if (r.ild === key) fireEvent(wrap(r), 'load', {}, Event);
  });
}

function scrOnceLoad(r) {
  if (r.eld) return;
  r.eld = true;
  scrQueueEvent(r, 'load');
}

function scrLinkConnected(r) {
  var href = getAttr(r, 'href');
  if (!href) return;
  var rel = ' ' + asciiLower(str(getAttr(r, 'rel') || '')) + ' ';
  if (rel.indexOf(' modulepreload ') >= 0) {
    var u = resolveURL(mainDoc, href);
    if (u) N.prefetch([u], true);
  }
  if (/ (stylesheet|preload|modulepreload|prefetch|icon) /.test(rel)) scrOnceLoad(r);
}

hooks.connected = function (r) {
  if (r.ns !== 0) return;
  switch (r.ln) {
    case 'script':
      if (!r.pi && !r.st0) scrPrepareDynamic(r);
      break;
    case 'img':
      scrImageLoad(r);
      break;
    case 'iframe':
    case 'frame':
    case 'object':
    case 'embed':
      scrOnceLoad(r);
      break;
    case 'link':
      scrLinkConnected(r);
      break;
  }
};

hooks.attrChanged = function (r, name, oldValue, value) {
  if (r.ns !== 0 || value === null || value === undefined) return;
  if (r.ln === 'script') {
    if (name === 'src' && r.c && !r.st0 && !r.pi) scrPrepareDynamic(r);
  } else if (r.ln === 'img') {
    if (name === 'src' || name === 'srcset') scrImageLoad(r);
  } else if (r.ln === 'link' && (name === 'href' || name === 'rel') && r.c) {
    scrLinkConnected(r);
  }
};

hooks.childrenChanged = function (parent) {
  if (parent.t === 1 && parent.ns === 0 && parent.ln === 'script' && parent.c && !parent.st0 && !parent.pi &&
    getAttr(parent, 'src') === null) {
    scrPrepareDynamic(parent);
  }
};

// The images, frames and links the parser made, "loaded" in one task after DOMContentLoaded.
function scrParsedLoads() {
  var list = [];
  var d = mainDoc;
  for (var r = d.f; r; r = nextNode(r, d)) {
    if (r.t !== 1 || r.ns !== 0) continue;
    if (r.ln === 'img') {
      var src = getAttr(r, 'src'), srcset = getAttr(r, 'srcset');
      if (src || srcset) {
        r.ild = str(src || '') + ' ' + str(srcset || '');
        list.push(r);
      }
    } else if (r.ln === 'iframe' || r.ln === 'link' && /\b(stylesheet|preload|modulepreload)\b/i.test(str(getAttr(r, 'rel') || ''))) {
      if (!r.eld) {
        r.eld = true;
        list.push(r);
      }
    }
  }
  if (!list.length) return;
  loopQueueTask(function () {
    for (var i = 0; i < list.length; i++) {
      try { fireEvent(wrap(list[i]), 'load', {}, Event); } catch (e) { reportException(e); }
    }
  });
}

// ---------------------------------------------------------------- import maps and modules

var modRecords = objCreate(null);
var modImportMap = { imports: objCreate(null), scopes: objCreate(null) };
var modInline = 0;

function modNormalizeKey(k) {
  if (/^(\.{0,2}\/|[a-z][a-z0-9+.-]*:)/i.test(k)) return resolveURL(mainDoc, k) || k;
  return k;
}

function modMergeMap(dst, src) {
  if (!src || typeof src !== 'object') return;
  var keys = Object.keys(src);
  for (var i = 0; i < keys.length; i++) {
    var v = src[keys[i]];
    if (typeof v !== 'string') continue;
    var key = modNormalizeKey(keys[i]);
    var u = resolveURL(mainDoc, v);
    if (u && !(key in dst)) dst[key] = u;
  }
}

function scrImportMap(text) {
  var obj;
  try {
    obj = JSON.parse(str(text));
  } catch (e) {
    reportException(new SyntaxError('Invalid import map: ' + e.message));
    return;
  }
  if (!obj || typeof obj !== 'object') return;
  modMergeMap(modImportMap.imports, obj.imports);
  if (obj.scopes && typeof obj.scopes === 'object') {
    var scopes = Object.keys(obj.scopes);
    for (var i = 0; i < scopes.length; i++) {
      var su = resolveURL(mainDoc, scopes[i]);
      if (!su) continue;
      var m = modImportMap.scopes[su] || (modImportMap.scopes[su] = objCreate(null));
      modMergeMap(m, obj.scopes[scopes[i]]);
    }
  }
}

function modMapLookup(map, key) {
  if (key in map) return map[key];
  var best = null;
  for (var k in map) {
    if (k.charAt(k.length - 1) === '/' && key.indexOf(k) === 0 && (best === null || k.length > best.length)) best = k;
  }
  if (best === null) return null;
  var p = envParseURL(key.slice(best.length), map[best]);
  return p ? p.href : null;
}

// modResolve resolves a module specifier against a module (or the document): relative and absolute URLs
// directly, bare names ("react", "lit/decorators.js") only through the import map.
function modResolve(spec, base) {
  var s = str(spec);
  var p = /^(\/|\.\/|\.\.\/)/.test(s) ? envParseURL(s, base) : envParseURL(s);
  var key = p ? p.href : s;
  var scopes = Object.keys(modImportMap.scopes).filter(function (sc) {
    return base === sc || (sc.charAt(sc.length - 1) === '/' && base.indexOf(sc) === 0);
  }).sort(function (a, b) { return b.length - a.length; });
  for (var i = 0; i < scopes.length; i++) {
    var hit = modMapLookup(modImportMap.scopes[scopes[i]], key);
    if (hit) return hit;
  }
  var top = modMapLookup(modImportMap.imports, key);
  if (top) return top;
  return p ? p.href : null;
}

function modNamespace() {
  var ns = objCreate(null);
  defineProperty(ns, Symbol.toStringTag, { value: 'Module' });
  return ns;
}

// modNamespaceFor wraps a module's namespace object. Once the module's body has run (m.bodyRan) every read is
// the live binding. Before that — an importer in a cycle reading it, the Vite pattern where a vendor chunk
// imports the preload helper back from the entry chunk that imports it — a name that has no value yet reads
// as a forwarding stub instead of undefined: calling it, constructing it or reading a property of it reaches
// the real binding at that later moment. Import bindings are copies taken when the importer starts (goja has
// no live module bindings), so without the stub such an importer would keep undefined for good.
function modNamespaceFor(m) {
  var target = modNamespace();
  m.nsTarget = target;
  var stubs = objCreate(null);
  return new Proxy(target, {
    get: function (t, k, recv) {
      var v = Reflect.get(t, k, recv);
      if (m.bodyRan || typeof k === 'symbol' || v !== undefined || k === 'then') return v;
      return stubs[k] || (stubs[k] = modForwardStub(t, k));
    }
  });
}

function modForwardStub(target, name) {
  function live() { return Reflect.get(target, name); }
  return new Proxy(function () {}, {
    apply: function (t, thisArg, args) {
      var f = live();
      if (typeof f !== 'function') throw new TypeError(name + ' is not a function (its module has not run)');
      return Reflect.apply(f, thisArg, args);
    },
    construct: function (t, args) {
      var C = live();
      if (typeof C !== 'function') throw new TypeError(name + ' is not a constructor (its module has not run)');
      return Reflect.construct(C, args);
    },
    get: function (t, k) {
      var v = live();
      return v === null || v === undefined ? undefined : v[k];
    },
    has: function (t, k) {
      var v = live();
      return v !== null && v !== undefined && typeof v === 'object' ? k in v : false;
    }
  });
}

function modGet(url, type) {
  var key = (type ? type + ' ' : '') + url;
  var m = modRecords[key];
  if (!m) {
    m = modRecords[key] = {
      url: url, base: '', type: type || '', state: 'new', ns: null, nsTarget: null, deps: [], code: null,
      done: false, evaluating: false, bodyRan: false, error: null, promise: null, factory: null, evalPromise: null
    };
    m.ns = modNamespaceFor(m);
  }
  return m;
}

// A namespace binding reads the module's own binding live. Reading one before its module initialised it (a
// cycle) gives undefined instead of failing the importer.
function modExport(m, name, getter) {
  if (hasOwn.call(m.nsTarget, name)) return;
  defineProperty(m.nsTarget, name, {
    enumerable: true,
    get: function () {
      try {
        return getter();
      } catch (e) {
        if (e instanceof ReferenceError) return undefined;
        throw e;
      }
    }
  });
}

function modReexport(d, name) { return function () { return d.nsTarget[name]; }; }

// modImportType reads the type of a dynamic import's options ({with: {type: "json"}}, or the older assert).
function modImportType(opts) {
  if (!opts || typeof opts !== 'object') return '';
  var w = opts['with'] || opts.assert;
  var t = w && typeof w === 'object' ? str(w.type === undefined ? '' : w.type) : '';
  return t === 'json' || t === 'css' ? t : '';
}

function modContext(m) {
  return {
    imp: function (i) { var d = m.deps[i]; return d ? d.ns : modNamespace(); },
    exp: function (map) {
      var keys = Object.keys(map);
      for (var i = 0; i < keys.length; i++) modExport(m, keys[i], map[keys[i]]);
    },
    reexp: function (i, map) {
      var d = m.deps[i];
      if (!d) return;
      var keys = Object.keys(map);
      for (var j = 0; j < keys.length; j++) modExport(m, keys[j], modReexport(d, map[keys[j]]));
    },
    star: function (i) {
      var d = m.deps[i];
      if (!d) return;
      var keys = Object.keys(d.nsTarget);
      for (var j = 0; j < keys.length; j++) {
        if (keys[j] !== 'default') modExport(m, keys[j], modReexport(d, keys[j]));
      }
    },
    meta: {
      // import.meta.url is where the module came from after redirects (its base), and the document base
      // for an inline module — not the internal key an inline module is filed under.
      url: m.base || m.url,
      resolve: function (spec) {
        var u = modResolve(spec, m.base || m.url);
        if (!u) throw new TypeError('Failed to resolve module specifier "' + str(spec) + '"');
        return u;
      }
    },
    dyn: function (spec, opts) { return modDynamicImport(spec, m.base || m.url, modImportType(opts)); }
  };
}

// modFetch loads one module's source (unless given), rewrites it, and walks its imports: the imports of a
// module are prefetched together, then loaded depth first.
function modFetch(m, code, finalURL) {
  if (m.state !== 'new') return;
  m.state = 'loading';
  if (code === null || code === undefined) {
    var res = N.fetchScriptSync(m.url, true);
    if (!res || (res.error !== null && res.error !== undefined) || res.code === null || res.code === undefined) {
      m.state = 'error';
      // The network layer's reason ("blocked: private network address", "CORS", …) goes to the debug
      // log only: shown to the page, it would let a public site probe which internal names resolve.
      m.error = new TypeError('Failed to fetch module ' + m.url);
      if (res && res.error) N.log('warn', 'module ' + m.url + ' not loaded: ' + str(res.error));
      return;
    }
    code = str(res.code);
    if (res.url) m.base = str(res.url);
  }
  if (!m.base) m.base = finalURL || m.url;
  if (m.type === 'json') {
    try {
      var value = JSON.parse(code);
      modExport(m, 'default', function () { return value; });
      m.state = 'linked';
      m.bodyRan = true;
    } catch (e) {
      m.state = 'error';
      m.error = e;
    }
    return;
  }
  if (m.type === 'css') {
    var sheet = null;
    try {
      sheet = new CSSStyleSheet();
      sheet.replaceSync(code);
    } catch (e) { /* an unusable sheet still imports */ }
    modExport(m, 'default', function () { return sheet; });
    m.state = 'linked';
    m.bodyRan = true;
    return;
  }
  var t = N.transformModule(code);
  if (!t || (t.error !== null && t.error !== undefined) || typeof t.code !== 'string') {
    m.state = 'error';
    m.error = new SyntaxError((t && t.error) || 'the module could not be rewritten');
    return;
  }
  m.code = t.code;
  var imports = t.imports || [];
  for (var i = 0; i < imports.length; i++) {
    var spec = str(imports[i].spec), type = str(imports[i].type || '');
    var u = modResolve(spec, m.base);
    if (!u) {
      m.state = 'error';
      m.error = new TypeError('Failed to resolve module specifier "' + spec + '". Relative references must start with either "/", "./", or "../".');
      return;
    }
    m.deps.push(modGet(u, type === 'json' || type === 'css' ? type : ''));
  }
  var wanted = [];
  for (var j = 0; j < m.deps.length; j++) if (m.deps[j].state === 'new') wanted.push(m.deps[j].url);
  if (wanted.length) N.prefetch(wanted, true);
  m.state = 'linked';
  for (var k = 0; k < m.deps.length; k++) modFetch(m.deps[k]);
}

// Errors are reported once each, even when several importers fail because of the same dependency.
var modReported = [];
function modReport(e, url) {
  if (e && typeof e === 'object') {
    if (modReported.indexOf(e) >= 0) return;
    if (modReported.length < 64) modReported.push(e);
  }
  envReportError(e, url);
}

// modEvaluate runs a linked graph, dependencies first, as a promise chain, and returns the promise of m's
// evaluation. A dependency whose body threw, or that awaits at its top level, settles before its importers
// run, and an importer whose dependency failed does not run — as in a browser. Promise jobs drain before the
// host call returns, so a graph without top-level await still finishes inside the task that started it. A
// dependency already on the current path (a cycle) is not waited for: its importers see its namespace before
// it ran, through the forwarding stubs of modNamespaceFor.
function modEvaluate(m, path) {
  if (m.evalPromise) return m.evalPromise;
  m.evaluating = true;
  var here = (path || []).concat([m]);
  var chain = Promise.resolve();
  m.deps.forEach(function (dep) {
    chain = chain.then(function () {
      if (here.indexOf(dep) >= 0) return null;
      return modEvaluate(dep, here);
    }).then(function () {
      if (dep.state === 'error' && m.state !== 'error') {
        m.state = 'error';
        m.error = dep.error;
      }
    });
  });
  m.evalPromise = chain.then(function () { return modRunBody(m); });
  return m.evalPromise;
}

function modRunBody(m) {
  m.done = true;
  if (m.state !== 'linked' || m.code === null) {
    m.evaluating = false;
    if (m.state === 'error' && m.error) modReport(m.error, m.url);
    return null;
  }
  var res = N.runScript(m.code, m.url);
  if (scrIsError(res)) {
    m.state = 'error';
    m.error = res.error;
    m.evaluating = false;
    if (res.error && typeof res.error === 'object') modReported.push(res.error);
    scrReportResult(res, m.url, null);
    return null;
  }
  if (typeof res !== 'function') {
    m.state = 'error';
    m.error = new TypeError('module ' + m.url + ' did not load');
    m.evaluating = false;
    return null;
  }
  m.factory = res;
  try {
    m.promise = res(modContext(m));
  } catch (e) {
    m.state = 'error';
    m.error = e;
    m.evaluating = false;
    modReport(e, m.url);
    return null;
  }
  m.bodyRan = true;
  if (m.promise && typeof m.promise.then === 'function') {
    return m.promise.then(function () {
      m.evaluating = false;
    }, function (e) {
      m.state = 'error';
      m.error = e;
      m.evaluating = false;
      modReport(e, m.url);
    });
  }
  m.evaluating = false;
  return null;
}

function modDynamicImport(spec, base, type) {
  return new Promise(function (resolve, reject) {
    loopQueueTask(function () {
      var u = modResolve(spec, base);
      if (!u) {
        reject(new TypeError('Failed to resolve module specifier "' + str(spec) + '"'));
        return;
      }
      var m = modGet(u, type || '');
      modFetch(m);
      modEvaluate(m, null).then(function () {
        if (m.state === 'error') reject(m.error || new TypeError('Failed to fetch dynamically imported module: ' + u));
        else resolve(m.ns);
      }, reject);
    });
  });
}

// Classic scripts' import() is rewritten to this (webdom_module.go): it resolves against the script that is
// running, or the document. scrRunClassic also binds each rewriting script to its own URL (__jh_importAt), so
// an import() made later — from a click handler or a timer — still resolves against the script that wrote it.
defineGlobal('__jh_import', function (spec, opts) {
  var cur = mainDoc && mainDoc.cur;
  var src = cur ? getAttr(cur, 'src') : null;
  var base = (src && resolveURL(mainDoc, src)) || documentBaseURL(mainDoc);
  return modDynamicImport(spec, base, modImportType(opts));
});
var scrImportAt = [];
defineGlobal('__jh_importAt', scrImportAt);
function scrBindImport(code, url) {
  if (code.indexOf('__jh_import(') < 0 || scrImportAt.length >= 256) return code;
  var base = url || documentBaseURL(mainDoc);
  var n = scrImportAt.length;
  scrImportAt.push(function (spec, opts) { return modDynamicImport(spec, base, modImportType(opts)); });
  return code.split('__jh_import(').join('__jh_importAt[' + n + '](');
}

function scrRunModuleElement(r, code, finalURL) {
  var src = getAttr(r, 'src');
  var m;
  if (src === null) {
    m = modGet(mainDoc.url + '#jukahub-inline-module-' + (++modInline), '');
    var text = code === null || code === undefined ? childText(r) : str(code);
    modFetch(m, text, documentBaseURL(mainDoc));
  } else {
    var url = finalURL ? str(finalURL) : resolveURL(mainDoc, src);
    m = modGet(url, '');
    modFetch(m, code === null || code === undefined ? undefined : str(code), url);
  }
  scrStats.run++;
  var p = modEvaluate(m, null);
  if (src !== null) {
    p.then(function () {
      fireEvent(wrap(r), m.state === 'error' && !m.factory ? 'error' : 'load', {}, Event);
    });
  }
}
