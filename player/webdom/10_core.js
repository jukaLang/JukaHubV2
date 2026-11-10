// 10_core.js — shared helpers for the JukaHub browser's DOM prelude.
//
// The prelude is a set of part files (this directory) that player/webdom_runtime.go concatenates in
// file-name order inside one function:
//
//   (function (global, N) { 'use strict'; <parts> })(this, this.__jhNative);
//
// `global` is goja's global object (it is `window`), `N` the Go natives. Every part shares that one function
// scope, so a name declared at the top of one part is visible in all of them. Parts therefore declare only
// `var`s and functions at top level (a duplicate let/const/class would be a SyntaxError for the whole
// prelude). Parts 10–59 are the DOM core; 60–99 the window environment, event loop and host API.
//
// The DOM is built for a slow interpreter on a 1 GB handheld: nodes are plain records (see 20_nodes.js) and
// the objects page scripts see are created only when a script touches a node.

var $ = Symbol('jukahub.node');           // wrapper[$] → record
var EV = Symbol('jukahub.listeners');     // EventTarget listener map on any object
var EH = Symbol('jukahub.handlers');      // on<event> handler values on any object
var hasOwn = Object.prototype.hasOwnProperty;
var defineProperty = Object.defineProperty;
var getOwnDesc = Object.getOwnPropertyDescriptor;
var objCreate = Object.create;
var setProto = Object.setPrototypeOf;
var getProto = Object.getPrototypeOf;
var arraySlice = Array.prototype.slice;

// The natives object is captured by the wrapper; page scripts must not see it.
try { delete global.__jhNative; } catch (e) { /* non-configurable in some hosts */ }

// Test hook: the node harness asks for internals with N.__test. Filled as parts load (function
// declarations are hoisted, so later parts' functions can be referenced from here).
var testHooks = (N && N.__test === true) ? (global.__jhTest = {}) : null;

// mainDoc is the page's document record (set by bootDocument in 20_nodes.js).
var mainDoc = null;

// hooks: the DOM core calls these; the environment parts (60–99) replace them.
var hooks = {
  connected: function (r) {},
  disconnected: function (r) {},
  attrChanged: function (r, name, oldValue, newValue) {},
  write: function (docRec, text) { return false; },
  queueTask: function (fn) { Promise.resolve().then(fn); },
  navigate: function (url, replace) {},
  viewport: function () { return { w: 1280, h: 720 }; }
};

function defineGlobal(name, value) {
  defineProperty(global, name, { value: value, writable: true, configurable: true, enumerable: false });
}

function str(v) { return typeof v === 'string' ? v : String(v); }

// WebIDL DOMString with [LegacyNullToEmptyString] (innerHTML, textContent setters …).
function strOrEmpty(v) { return v === null || v === undefined ? '' : String(v); }

var ASCII_UPPER = /[A-Z]/;
// Lower-casing names is on every getAttribute/createElement path; the distinct names a page uses are few,
// so they are cached (bounded).
var lowerCache = objCreate(null);
var lowerCacheSize = 0;
function asciiLower(s) {
  var hit = lowerCache[s];
  if (hit !== undefined) return hit;
  var out = ASCII_UPPER.test(s) ? s.replace(/[A-Z]+/g, function (m) { return m.toLowerCase(); }) : s;
  if (s.length <= 64) {
    if (lowerCacheSize >= 4096) {
      lowerCache = objCreate(null);
      lowerCacheSize = 0;
    }
    lowerCache[s] = out;
    lowerCacheSize++;
  }
  return out;
}
// Regex literals are hoisted out of the hot helpers: goja allocates a fresh RegExp each time a literal inside a
// function is evaluated, and converts the whole subject to runes on every exec, so the common inputs (no
// upper case, no surrounding white space) are answered before any regex runs.
var ASCII_LOWER_CHAR = /[a-z]/;
var ASCII_LOWER_RUN = /[a-z]+/g;
var upperCache = objCreate(null);
var upperCacheSize = 0;
function asciiUpper(s) {
  var hit = upperCache[s];
  if (hit !== undefined) return hit;
  var out = ASCII_LOWER_CHAR.test(s) ? s.replace(ASCII_LOWER_RUN, function (m) { return m.toUpperCase(); }) : s;
  if (s.length <= 64) {
    if (upperCacheSize >= 4096) {
      upperCache = objCreate(null);
      upperCacheSize = 0;
    }
    upperCache[s] = out;
    upperCacheSize++;
  }
  return out;
}

var WS_SPLIT = /[\t\n\f\r ]+/;
var WS_EDGES = /^[\t\n\f\r ]+|[\t\n\f\r ]+$/g;
var WS_RUNS = /[\t\n\f\r ]+/g;
// isHTMLSpace: tab, LF, FF, CR, space — the white space of HTML attribute and token syntax.
function isHTMLSpace(c) { return c === 32 || c === 9 || c === 10 || c === 12 || c === 13; }
function splitTokens(s) {
  if (!s) return [];
  var out = [];
  var n = s.length, i = 0;
  while (i < n) {
    while (i < n && isHTMLSpace(s.charCodeAt(i))) i++;
    if (i >= n) break;
    var start = i;
    while (i < n && !isHTMLSpace(s.charCodeAt(i))) i++;
    out.push(start === 0 && i === n ? s : s.slice(start, i));
  }
  return out;
}
function stripWS(s) {
  var n = s.length;
  if (n === 0 || (!isHTMLSpace(s.charCodeAt(0)) && !isHTMLSpace(s.charCodeAt(n - 1)))) return s;
  return s.replace(WS_EDGES, '');
}
function collapseWS(s) { return stripWS(s.replace(WS_RUNS, ' ')); }

// ---------------------------------------------------------------- interrupt recovery
//
// When the watchdog interrupts a call, goja unwinds without running finally blocks. Re-entrancy flags on
// records (an element mid-click, a style object writing its own attribute, a custom element mid-upgrade, a
// TreeWalker filter mid-call) are therefore set with flagOn and cleared with flagOff, which also keep a
// list; hostRecover (90_host.js) clears whatever an interrupted call left on it.
var flagStack = [];
function flagOn(obj, prop) {
  obj[prop] = true;
  flagStack.push(obj, prop);
}
function flagOff(obj, prop) {
  obj[prop] = false;
  for (var i = flagStack.length - 2; i >= 0; i -= 2) {
    if (flagStack[i] === obj && flagStack[i + 1] === prop) {
      flagStack.splice(i, 2);
      return;
    }
  }
}
function flagsReset() {
  for (var i = 0; i < flagStack.length; i += 2) flagStack[i][flagStack[i + 1]] = false;
  flagStack.length = 0;
}

// ---------------------------------------------------------------- DOMException

var DOMEXCEPTION_CODES = {
  IndexSizeError: 1, HierarchyRequestError: 3, WrongDocumentError: 4, InvalidCharacterError: 5,
  NoModificationAllowedError: 7, NotFoundError: 8, NotSupportedError: 9, InUseAttributeError: 10,
  InvalidStateError: 11, SyntaxError: 12, InvalidModificationError: 13, NamespaceError: 14,
  InvalidAccessError: 15, TypeMismatchError: 17, SecurityError: 18, NetworkError: 19, AbortError: 20,
  URLMismatchError: 21, QuotaExceededError: 22, TimeoutError: 23, InvalidNodeTypeError: 24, DataCloneError: 25
};

function DOMException(message, name) {
  if (!(this instanceof DOMException)) throw new TypeError("Failed to construct 'DOMException': Please use the 'new' operator.");
  var n = name === undefined ? 'Error' : String(name);
  defineProperty(this, 'message', { value: message === undefined ? '' : String(message), writable: true, configurable: true });
  defineProperty(this, 'name', { value: n, writable: true, configurable: true });
  defineProperty(this, 'code', { value: DOMEXCEPTION_CODES[n] || 0, writable: true, configurable: true });
  try {
    var e = new Error(this.message);
    if (e.stack) defineProperty(this, 'stack', { value: e.stack, writable: true, configurable: true });
  } catch (x) { /* no stack */ }
}
DOMException.prototype = objCreate(Error.prototype);
defineProperty(DOMException.prototype, 'constructor', { value: DOMException, writable: true, configurable: true });
defineProperty(DOMException.prototype, Symbol.toStringTag, { value: 'DOMException', configurable: true });
(function () {
  var legacy = {
    INDEX_SIZE_ERR: 1, DOMSTRING_SIZE_ERR: 2, HIERARCHY_REQUEST_ERR: 3, WRONG_DOCUMENT_ERR: 4,
    INVALID_CHARACTER_ERR: 5, NO_DATA_ALLOWED_ERR: 6, NO_MODIFICATION_ALLOWED_ERR: 7, NOT_FOUND_ERR: 8,
    NOT_SUPPORTED_ERR: 9, INUSE_ATTRIBUTE_ERR: 10, INVALID_STATE_ERR: 11, SYNTAX_ERR: 12,
    INVALID_MODIFICATION_ERR: 13, NAMESPACE_ERR: 14, INVALID_ACCESS_ERR: 15, VALIDATION_ERR: 16,
    TYPE_MISMATCH_ERR: 17, SECURITY_ERR: 18, NETWORK_ERR: 19, ABORT_ERR: 20, URL_MISMATCH_ERR: 21,
    QUOTA_EXCEEDED_ERR: 22, TIMEOUT_ERR: 23, INVALID_NODE_TYPE_ERR: 24, DATA_CLONE_ERR: 25
  };
  for (var k in legacy) {
    defineProperty(DOMException, k, { value: legacy[k], enumerable: true });
    defineProperty(DOMException.prototype, k, { value: legacy[k], enumerable: true });
  }
})();
defineGlobal('DOMException', DOMException);

function domErr(name, message) { return new DOMException(message || name, name); }

// Reports an exception thrown by a callback (listener, observer, custom element reaction) without letting
// it unwind the DOM operation that invoked it — the way browsers "report the exception".
function reportException(e) {
  try {
    if (typeof global.reportError === 'function') { global.reportError(e); return; }
  } catch (x) { /* fall through */ }
  try {
    if (N && typeof N.log === 'function') N.log('error', 'uncaught: ' + (e && e.stack ? e.stack : String(e)));
  } catch (x) { /* nothing left to do */ }
}

// ---------------------------------------------------------------- interfaces

function illegalConstructor() { throw new TypeError('Illegal constructor'); }

// iface creates (or completes) a WebIDL-style interface object: constructor, prototype chain, toStringTag,
// and the global binding. ctor defaults to one that throws "Illegal constructor".
function iface(name, parent, ctor) {
  var C = ctor || function () { illegalConstructor(); };
  try { defineProperty(C, 'name', { value: name, configurable: true }); } catch (e) { /* older engines */ }
  if (parent) {
    C.prototype = objCreate(parent.prototype);
    setProto(C, parent);
  }
  defineProperty(C.prototype, 'constructor', { value: C, writable: true, configurable: true });
  defineProperty(C.prototype, Symbol.toStringTag, { value: name, configurable: true });
  defineGlobal(name, C);
  return C;
}

// mix copies the property descriptors of src (an object literal with methods and get/set accessors)
// onto proto as enumerable, configurable members — how WebIDL attributes and operations look.
function mix(proto, src) {
  var keys = Object.getOwnPropertyNames(src);
  for (var i = 0; i < keys.length; i++) {
    var d = getOwnDesc(src, keys[i]);
    d.enumerable = true;
    d.configurable = true;
    if ('value' in d) d.writable = true;
    defineProperty(proto, keys[i], d);
  }
  return proto;
}

function constants(target, table) {
  for (var k in table) defineProperty(target, k, { value: table[k], enumerable: true });
}

// The record behind a wrapper, or a TypeError like a browser's "Illegal invocation".
function R(o) {
  var r = o !== null && o !== undefined ? o[$] : undefined;
  if (r === undefined) throw new TypeError('Illegal invocation');
  return r;
}

function unwrap(o) {
  if (o === null || o === undefined || (typeof o !== 'object' && typeof o !== 'function')) return null;
  var r = o[$];
  return r === undefined ? null : r;
}

// A node argument or a TypeError naming the method, as WebIDL conversion does.
function nodeArg(o, what) {
  var r = unwrap(o);
  if (!r) throw new TypeError("Failed to execute '" + what + "': parameter is not of type 'Node'.");
  return r;
}

function requiredArgs(args, n, what) {
  if (args.length < n) {
    throw new TypeError("Failed to execute '" + what + "': " + n + ' argument' + (n > 1 ? 's' : '') +
      ' required, but only ' + args.length + ' present.');
  }
}

if (testHooks) {
  testHooks.hooks = hooks;
  defineProperty(testHooks, 'mainDoc', { get: function () { return mainDoc; } });
}
