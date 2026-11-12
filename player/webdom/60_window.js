// 60_window.js — the window around the document: location, history, navigator, screen, viewport, media
// queries, performance, crypto, console, structured clone, error reporting.
//
// The global object IS window (goja's global, given Window.prototype). Everything a page reads to learn where
// and on what it runs answers like a small landscape handheld browser: the viewport is the device screen,
// colours are light, motion is reduced (sites skip animations), the pointer is fine, there is no service
// worker, IndexedDB or Web Crypto. Navigation never happens here: N.navigate records the first real one and Go
// ends the pass (main follows it like a meta refresh).

var envConfig = {};
var envWidth = 1280;
var envHeight = 720;
var envLoc = null;           // the document URL's parts (urlParse JSON)
var envTimeOrigin = 0;
var envReporting = 0;

function envCfg(name, fallback) {
  var v = envConfig[name];
  return v === undefined || v === null || v === '' ? fallback : v;
}

function envApplyConfig(cfg) {
  envConfig = cfg && typeof cfg === 'object' ? cfg : {};
  var w = +envConfig.width, h = +envConfig.height;
  if (w > 0 && h > 0) {
    envWidth = Math.round(w);
    envHeight = Math.round(h);
  }
  envTimeOrigin = +N.now() - loopNow();
}

function envParseURL(input, base) {
  var j = N.urlParse(str(input), base === undefined || base === null ? '' : str(base));
  if (!j) return null;
  if (typeof j === 'string') {
    try { return JSON.parse(j); } catch (e) { return null; }
  }
  return j;
}

// envSetDocURL moves the document to a new URL without navigating (boot, pushState, a fragment change).
function envSetDocURL(href) {
  var p = envParseURL(href);
  if (!p) return false;
  envLoc = p;
  if (mainDoc) mainDoc.url = p.href;
  return true;
}

function envNoFragment(href) {
  var i = href.indexOf('#');
  return i < 0 ? href : href.slice(0, i);
}

// envNavigate is every way a script leaves the page: location.href/assign/replace, window.open(_self), a GET
// form submit, an anchor click(). A change of fragment only is a hashchange, not a navigation.
function envNavigate(url, replace) {
  var target = resolveURL(mainDoc, str(url));
  if (!target) throw domErr('SyntaxError', "Failed to set the 'href' property on 'Location': '" + str(url) + "' is not a valid URL.");
  var p = envParseURL(target);
  if (!p || p.protocol === 'javascript:') return;
  if (envLoc && envNoFragment(p.href) === envNoFragment(envLoc.href)) {
    if (p.hash !== envLoc.hash) {
      var oldURL = envLoc.href;
      envSetDocURL(p.href);
      loopQueueTask(function () {
        fireEvent(global, 'hashchange', { oldURL: oldURL, newURL: p.href }, HashChangeEvent);
      });
    }
    return;
  }
  N.navigate(p.href, !!replace);
}

hooks.navigate = function (url, replace) {
  try { envNavigate(url, replace); } catch (e) { reportException(e); }
};
hooks.viewport = function () { return { w: envWidth, h: envHeight }; };

// ---------------------------------------------------------------- Window

var Window = iface('Window', EventTarget);
var envProtoSet = false;
try {
  setProto(global, Window.prototype);
  envProtoSet = getProto(global) === Window.prototype;
} catch (e) {
  envProtoSet = false;
}
if (!envProtoSet) {
  // A host that will not re-parent its global still needs window.addEventListener and friends.
  ['addEventListener', 'removeEventListener', 'dispatchEvent'].forEach(function (name) {
    defineGlobal(name, function () { return EventTarget.prototype[name].apply(global, arguments); });
  });
}

function envReadonly(name, getter) {
  defineProperty(global, name, { get: getter, configurable: true, enumerable: true });
}

// "Replaceable" attributes: reading gives the live value, assigning replaces the property with plain data.
function envReplaceable(name, getter) {
  defineProperty(global, name, {
    get: getter,
    set: function (v) { defineProperty(global, name, { value: v, writable: true, configurable: true, enumerable: true }); },
    configurable: true,
    enumerable: true
  });
}

defineProperty(global, 'window', { value: global, enumerable: true });
['self', 'frames', 'parent', 'top'].forEach(function (name) {
  envReplaceable(name, function () { return global; });
});
defineProperty(global, 'opener', { value: null, writable: true, configurable: true, enumerable: true });
defineProperty(global, 'name', { value: '', writable: true, configurable: true, enumerable: true });
defineProperty(global, 'status', { value: '', writable: true, configurable: true, enumerable: true });
envReadonly('closed', function () { return false; });
envReplaceable('length', function () { return 0; });
envReadonly('frameElement', function () { return null; });
envReadonly('origin', function () { return envLoc ? envLoc.origin : 'null'; });
envReadonly('isSecureContext', function () {
  return !!envLoc && (envLoc.protocol === 'https:' || envLoc.hostname === 'localhost' || envLoc.hostname === '127.0.0.1');
});
envReadonly('crossOriginIsolated', function () { return false; });

envReplaceable('innerWidth', function () { return envWidth; });
envReplaceable('innerHeight', function () { return envHeight; });
envReplaceable('outerWidth', function () { return envWidth; });
envReplaceable('outerHeight', function () { return envHeight; });
envReplaceable('devicePixelRatio', function () { return 1; });
['screenX', 'screenY', 'screenLeft', 'screenTop', 'scrollX', 'scrollY', 'pageXOffset', 'pageYOffset'].forEach(function (name) {
  envReplaceable(name, function () { return 0; });
});

var BarProp = iface('BarProp', null);
mix(BarProp.prototype, { get visible() { return true; } });
var envBar = objCreate(BarProp.prototype);
['locationbar', 'menubar', 'personalbar', 'scrollbars', 'statusbar', 'toolbar'].forEach(function (name) {
  envReplaceable(name, function () { return envBar; });
});

['alert', 'print', 'focus', 'blur', 'stop', 'close', 'moveTo', 'moveBy', 'resizeTo', 'resizeBy', 'scroll', 'scrollTo',
  'scrollBy', 'captureEvents', 'releaseEvents'].forEach(function (name) {
  defineGlobal(name, function () {});
});
defineGlobal('confirm', function confirm() { return false; });
defineGlobal('prompt', function prompt() { return null; });
defineGlobal('find', function find() { return false; });

defineGlobal('open', function open(url, target) {
  var t = target === undefined ? '_blank' : str(target).toLowerCase();
  if (url !== undefined && url !== '' && (t === '_self' || t === '_top' || t === '_parent')) {
    envNavigate(url, false);
    return global;
  }
  // A new window is a popup the reader cannot show.
  return null;
});

defineGlobal('postMessage', function postMessage(message, targetOrigin) {
  var to = targetOrigin;
  if (to && typeof to === 'object') to = to.targetOrigin;
  to = to === undefined ? '/' : str(to);
  var origin = envLoc ? envLoc.origin : 'null';
  if (to !== '*' && to !== '/') {
    var p = envParseURL(to);
    if (!p) throw domErr('SyntaxError', "Failed to execute 'postMessage' on 'Window': Invalid target origin '" + to + "'.");
    if (p.origin !== origin) return;
  }
  var data = envStructuredClone(message);
  loopQueueTask(function () {
    fireEvent(global, 'message', { data: data, origin: origin, lastEventId: '', source: global, ports: [] }, MessageEvent);
  });
});

defineGlobal('getSelection', function getSelection() {
  var doc = global.document;
  return doc && typeof doc.getSelection === 'function' ? doc.getSelection() : null;
});

defineGlobal('atob', function atob(data) {
  if (arguments.length < 1) throw new TypeError("Failed to execute 'atob' on 'Window': 1 argument required, but only 0 present.");
  var out = N.atob(str(data));
  if (out === null || out === undefined) {
    throw domErr('InvalidCharacterError', "Failed to execute 'atob' on 'Window': The string to be decoded is not correctly encoded.");
  }
  return out;
});
defineGlobal('btoa', function btoa(data) {
  if (arguments.length < 1) throw new TypeError("Failed to execute 'btoa' on 'Window': 1 argument required, but only 0 present.");
  var out = N.btoa(str(data));
  if (out === null || out === undefined) {
    throw domErr('InvalidCharacterError', "Failed to execute 'btoa' on 'Window': The string to be encoded contains characters outside of the Latin1 range.");
  }
  return out;
});

defineGlobal('structuredClone', function structuredClone(value) {
  if (arguments.length < 1) throw new TypeError("Failed to execute 'structuredClone' on 'Window': 1 argument required, but only 0 present.");
  return envStructuredClone(value);
});

function envErrorText(e) {
  try {
    if (e && typeof e === 'object') {
      if (e.stack && typeof e.stack === 'string') return e.stack;
      if (e.message !== undefined) return (e.name ? e.name + ': ' : '') + e.message;
    }
    return String(e);
  } catch (x) {
    return 'error';
  }
}

// envReportError is "report the exception": an ErrorEvent at window (window.onerror and error listeners see
// it), then the debug log unless a handler cancelled it. Errors thrown by an error handler are logged only.
function envReportError(e, filename, lineno, colno) {
  var msg = envErrorText(e);
  hostNoteError(msg);
  if (envReporting > 0) {
    try { N.log('error', 'uncaught in error handler: ' + msg); } catch (x) { /* nothing */ }
    return;
  }
  envReporting++;
  try {
    var short = e && typeof e === 'object' && e.message !== undefined ? (e.name ? e.name + ': ' : '') + e.message : String(e);
    var ev = fireEvent(global, 'error', {
      message: 'Uncaught ' + short, filename: filename || '', lineno: lineno | 0, colno: colno | 0, error: e, cancelable: true
    }, ErrorEvent);
    if (!ev || !ev.defaultPrevented) N.log('error', 'uncaught: ' + msg);
  } catch (x) {
    try { N.log('error', 'uncaught: ' + msg); } catch (y) { /* nothing */ }
  } finally {
    envReporting--;
  }
}
defineGlobal('reportError', function reportError(e) { envReportError(e); });

// ---------------------------------------------------------------- structured clone

function envCloneError(what) {
  return domErr('DataCloneError', "Failed to execute 'structuredClone' on 'Window': " + what + ' could not be cloned.');
}

function envStructuredClone(value) {
  var seen = new Map();
  function clone(v) {
    var t = typeof v;
    if (v === null || (t !== 'object' && t !== 'function')) {
      if (t === 'symbol') throw envCloneError('Symbol');
      return v;
    }
    if (t === 'function') throw envCloneError(v.name ? 'function ' + v.name : 'function');
    if (seen.has(v)) return seen.get(v);
    if (unwrap(v)) throw envCloneError(v.nodeName || 'Node');
    var out, i, keys;
    if (Array.isArray(v)) {
      out = [];
      seen.set(v, out);
      for (i = 0; i < v.length; i++) out[i] = clone(v[i]);
      return out;
    }
    if (v instanceof Date) out = new Date(v.getTime());
    else if (v instanceof RegExp) out = new RegExp(v.source, v.flags);
    else if (v instanceof Map) {
      out = new Map();
      seen.set(v, out);
      v.forEach(function (val, key) { out.set(clone(key), clone(val)); });
      return out;
    } else if (v instanceof Set) {
      out = new Set();
      seen.set(v, out);
      v.forEach(function (val) { out.add(clone(val)); });
      return out;
    } else if (v instanceof ArrayBuffer) out = v.slice(0);
    else if (ArrayBuffer.isView(v)) {
      var buf = clone(v.buffer);
      out = v instanceof DataView ? new DataView(buf, v.byteOffset, v.byteLength) : new v.constructor(buf, v.byteOffset, v.length);
    } else if (v instanceof Error) {
      var Ctor = { EvalError: EvalError, RangeError: RangeError, ReferenceError: ReferenceError, SyntaxError: SyntaxError,
        TypeError: TypeError, URIError: URIError }[v.name] || Error;
      out = new Ctor(v.message);
      if (typeof v.stack === 'string') {
        try { out.stack = v.stack; } catch (x) { /* read-only */ }
      }
    } else if (v instanceof Boolean || v instanceof Number || v instanceof String) out = Object(v.valueOf());
    else if (typeof Blob === 'function' && v instanceof Blob) out = v;
    else if (v instanceof Promise || v instanceof WeakMap || v instanceof WeakSet) throw envCloneError('#<' + (v[Symbol.toStringTag] || 'Object') + '>');
    else {
      out = {};
      seen.set(v, out);
      keys = Object.keys(v);
      for (i = 0; i < keys.length; i++) out[keys[i]] = clone(v[keys[i]]);
      return out;
    }
    seen.set(v, out);
    return out;
  }
  return clone(value);
}

// ---------------------------------------------------------------- Location

var Location = iface('Location', null);
var envLocation = objCreate(Location.prototype);

function envLocSet(part, value) {
  var u = new URL(envLoc ? envLoc.href : 'about:blank');
  u[part] = value;
  envNavigate(u.href, false);
}

function envLocGet(part, fallback) { return envLoc ? envLoc[part] : fallback; }

mix(Location.prototype, {
  get href() { return envLocGet('href', 'about:blank'); },
  set href(v) { envNavigate(v, false); },
  get origin() { return envLocGet('origin', 'null'); },
  get protocol() { return envLocGet('protocol', 'about:'); },
  set protocol(v) { envLocSet('protocol', v); },
  get host() { return envLocGet('host', ''); },
  set host(v) { envLocSet('host', v); },
  get hostname() { return envLocGet('hostname', ''); },
  set hostname(v) { envLocSet('hostname', v); },
  get port() { return envLocGet('port', ''); },
  set port(v) { envLocSet('port', v); },
  get pathname() { return envLocGet('pathname', 'blank'); },
  set pathname(v) { envLocSet('pathname', v); },
  get search() { return envLocGet('search', ''); },
  set search(v) { envLocSet('search', v); },
  get hash() { return envLocGet('hash', ''); },
  set hash(v) { envLocSet('hash', v); },
  get ancestorOrigins() { return envStringList([]); },
  assign: function (url) { envNavigate(url, false); },
  replace: function (url) { envNavigate(url, true); },
  reload: function () { /* a reload would run the same page again: ignored */ },
  toString: function () { return this.href; },
  valueOf: function () { return this; }
});
defineProperty(global, 'location', {
  get: function () { return envLocation; },
  set: function (v) { envNavigate(v, false); },
  enumerable: true
});

function envStringList(items) {
  var list = items.slice();
  list.item = function (i) { return list[i] === undefined ? null : list[i]; };
  list.contains = function (s) { return list.indexOf(str(s)) >= 0; };
  return list;
}

// ---------------------------------------------------------------- History

var History = iface('History', null);
var envHistory = objCreate(History.prototype);
var envHistState = null;
var envHistLength = 1;
var envScrollRestoration = 'auto';

function envHistoryUpdate(state, url, push, what) {
  var cloned = envStructuredClone(state);
  if (url !== undefined && url !== null) {
    var target = resolveURL(mainDoc, str(url));
    var p = target ? envParseURL(target) : null;
    if (!p) throw domErr('SyntaxError', "Failed to execute '" + what + "' on 'History': A history state object with URL '" + str(url) + "' cannot be created.");
    if (envLoc && p.origin !== envLoc.origin) {
      throw domErr('SecurityError', "Failed to execute '" + what + "' on 'History': A history state object with URL '" + p.href +
        "' cannot be created in a document with origin '" + envLoc.origin + "'.");
    }
    envSetDocURL(p.href);
  }
  envHistState = cloned;
  if (push) envHistLength++;
}

mix(History.prototype, {
  get length() { return envHistLength; },
  get state() { return envHistState; },
  get scrollRestoration() { return envScrollRestoration; },
  set scrollRestoration(v) { if (v === 'auto' || v === 'manual') envScrollRestoration = v; },
  pushState: function (state, unused, url) { envHistoryUpdate(state, url, true, 'pushState'); },
  replaceState: function (state, unused, url) { envHistoryUpdate(state, url, false, 'replaceState'); },
  back: function () {},
  forward: function () {},
  go: function () {}
});
envReadonly('history', function () { return envHistory; });

// ---------------------------------------------------------------- Navigator

var Navigator = iface('Navigator', null);
var envNavigator = objCreate(Navigator.prototype);
var envLanguages = null;

var PermissionStatus = iface('PermissionStatus', EventTarget);
envOnProp(PermissionStatus.prototype, 'change');
var Permissions = iface('Permissions', null);
mix(Permissions.prototype, {
  query: function (desc) {
    var name = desc && desc.name ? str(desc.name) : '';
    var st = envTarget(PermissionStatus.prototype);
    defineProperty(st, 'name', { value: name, enumerable: true });
    defineProperty(st, 'state', { value: name === 'notifications' || name === 'geolocation' || name === 'push' ? 'denied' : 'prompt', enumerable: true });
    return Promise.resolve(st);
  }
});
var envPermissions = objCreate(Permissions.prototype);

var Geolocation = iface('Geolocation', null);
var envGeoWatch = 1;
function envGeoDenied(error) {
  if (typeof error !== 'function') return;
  loopQueueTask(function () {
    error({ code: 1, message: 'User denied Geolocation', PERMISSION_DENIED: 1, POSITION_UNAVAILABLE: 2, TIMEOUT: 3 });
  });
}
mix(Geolocation.prototype, {
  getCurrentPosition: function (success, error) { envGeoDenied(error); },
  watchPosition: function (success, error) { envGeoDenied(error); return envGeoWatch++; },
  clearWatch: function () {}
});
var envGeolocation = objCreate(Geolocation.prototype);

var Clipboard = iface('Clipboard', EventTarget);
mix(Clipboard.prototype, {
  readText: function () { return Promise.reject(domErr('NotAllowedError', 'Read permission denied.')); },
  read: function () { return Promise.reject(domErr('NotAllowedError', 'Read permission denied.')); },
  writeText: function () { return Promise.resolve(); },
  write: function () { return Promise.resolve(); }
});
var envClipboard = envTarget(Clipboard.prototype);

var NetworkInformation = iface('NetworkInformation', EventTarget);
mix(NetworkInformation.prototype, {
  get effectiveType() { return '4g'; },
  get type() { return 'wifi'; },
  get rtt() { return 100; },
  get downlink() { return 10; },
  get saveData() { return false; }
});
envOnProp(NetworkInformation.prototype, 'change');
var envConnection = envTarget(NetworkInformation.prototype);

var MediaSession = iface('MediaSession', null);
mix(MediaSession.prototype, {
  metadata: null,
  playbackState: 'none',
  setActionHandler: function () {},
  setPositionState: function () {},
  setMicrophoneActive: function () {},
  setCameraActive: function () {}
});
var envMediaSession = objCreate(MediaSession.prototype);

var envEmptyPlugins = (function () {
  var list = [];
  list.item = function () { return null; };
  list.namedItem = function () { return null; };
  list.refresh = function () {};
  return list;
})();

var envStorageManager = {
  estimate: function () { return Promise.resolve({ quota: 524288, usage: 0 }); },
  persist: function () { return Promise.resolve(false); },
  persisted: function () { return Promise.resolve(false); }
};

mix(Navigator.prototype, {
  get userAgent() { return str(envCfg('userAgent', 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36')); },
  get appCodeName() { return 'Mozilla'; },
  get appName() { return 'Netscape'; },
  get appVersion() {
    var ua = this.userAgent;
    return ua.indexOf('Mozilla/') === 0 ? ua.slice(8) : '5.0';
  },
  get platform() { return str(envCfg('platform', 'Linux')); },
  get product() { return 'Gecko'; },
  get productSub() { return '20030107'; },
  get vendor() { return ''; },
  get vendorSub() { return ''; },
  get language() { return str(envCfg('language', 'en-US')); },
  get languages() {
    if (!envLanguages) {
      var l = envConfig.languages;
      envLanguages = Object.freeze(Array.isArray(l) && l.length ? l.map(str) : [this.language]);
    }
    return envLanguages;
  },
  get onLine() { return true; },
  get cookieEnabled() { return true; },
  get doNotTrack() { return null; },
  get hardwareConcurrency() { return 4; },
  get deviceMemory() { return 1; },
  get maxTouchPoints() { return 0; },
  get webdriver() { return false; },
  get pdfViewerEnabled() { return false; },
  get plugins() { return envEmptyPlugins; },
  get mimeTypes() { return envEmptyPlugins; },
  get connection() { return envConnection; },
  get permissions() { return envPermissions; },
  get geolocation() { return envGeolocation; },
  get clipboard() { return envClipboard; },
  get mediaSession() { return envMediaSession; },
  get storage() { return envStorageManager; },
  get userActivation() { return { hasBeenActive: false, isActive: false }; },
  javaEnabled: function () { return false; },
  // Beacons are analytics: report success, send nothing.
  sendBeacon: function () { return true; },
  vibrate: function () { return false; },
  registerProtocolHandler: function () {},
  unregisterProtocolHandler: function () {},
  getGamepads: function () { return []; }
});
envReadonly('navigator', function () { return envNavigator; });
envReplaceable('clientInformation', function () { return envNavigator; });

// ---------------------------------------------------------------- Screen

var ScreenOrientation = iface('ScreenOrientation', EventTarget);
mix(ScreenOrientation.prototype, {
  get type() { return envWidth >= envHeight ? 'landscape-primary' : 'portrait-primary'; },
  get angle() { return 0; },
  lock: function () { return Promise.reject(domErr('NotSupportedError', 'screen.orientation.lock() is not available on this device.')); },
  unlock: function () {}
});
envOnProp(ScreenOrientation.prototype, 'change');
var envOrientation = envTarget(ScreenOrientation.prototype);

var Screen = iface('Screen', EventTarget);
mix(Screen.prototype, {
  get width() { return envWidth; },
  get height() { return envHeight; },
  get availWidth() { return envWidth; },
  get availHeight() { return envHeight; },
  get availLeft() { return 0; },
  get availTop() { return 0; },
  get colorDepth() { return 24; },
  get pixelDepth() { return 24; },
  get isExtended() { return false; },
  get orientation() { return envOrientation; }
});
envOnProp(Screen.prototype, 'change');
var envScreen = envTarget(Screen.prototype);
envReplaceable('screen', function () { return envScreen; });

var VisualViewport = iface('VisualViewport', EventTarget);
mix(VisualViewport.prototype, {
  get width() { return envWidth; },
  get height() { return envHeight; },
  get offsetLeft() { return 0; },
  get offsetTop() { return 0; },
  get pageLeft() { return 0; },
  get pageTop() { return 0; },
  get scale() { return 1; }
});
envOnProps(VisualViewport.prototype, ['resize', 'scroll', 'scrollend']);
var envVisualViewport = envTarget(VisualViewport.prototype);
envReplaceable('visualViewport', function () { return envVisualViewport; });

// ---------------------------------------------------------------- matchMedia

var MQ = Symbol('jukahub.mq');
var MediaQueryList = iface('MediaQueryList', EventTarget);
mix(MediaQueryList.prototype, {
  get media() { return this[MQ]; },
  get matches() { return envEvalMedia(this[MQ]); },
  addListener: function (fn) { if (fn) this.addEventListener('change', fn); },
  removeListener: function (fn) { if (fn) this.removeEventListener('change', fn); }
});
envOnProp(MediaQueryList.prototype, 'change');

defineGlobal('matchMedia', function matchMedia(query) {
  if (arguments.length < 1) throw new TypeError("Failed to execute 'matchMedia' on 'Window': 1 argument required, but only 0 present.");
  var mql = envTarget(MediaQueryList.prototype);
  defineProperty(mql, MQ, { value: collapseWS(str(query)) });
  return mql;
});

function envLength(v) {
  var m = /^(-?[\d.]+)\s*([a-z%]*)$/.exec(stripWS(v));
  if (!m) return NaN;
  var n = parseFloat(m[1]);
  switch (m[2]) {
    case '': case 'px': return n;
    case 'em': case 'rem': return n * 16;
    case 'vw': return n * envWidth / 100;
    case 'vh': return n * envHeight / 100;
    case 'vmin': return n * Math.min(envWidth, envHeight) / 100;
    case 'vmax': return n * Math.max(envWidth, envHeight) / 100;
    case 'in': return n * 96;
    case 'cm': return n * 96 / 2.54;
    case 'mm': return n * 96 / 25.4;
    case 'pt': return n * 96 / 72;
    case 'pc': return n * 16;
    case 'q': return n * 96 / 101.6;
  }
  return NaN;
}

function envResolution(v) {
  var m = /^([\d.]+)\s*(dppx|x|dpi|dpcm)?$/.exec(stripWS(v));
  if (!m) return NaN;
  var n = parseFloat(m[1]);
  if (m[2] === 'dpi') return n / 96;
  if (m[2] === 'dpcm') return n * 2.54 / 96;
  return n;
}

function envRatio(v) {
  var m = /^([\d.]+)\s*(?:\/\s*([\d.]+))?$/.exec(stripWS(v));
  if (!m) return NaN;
  return parseFloat(m[1]) / (m[2] ? parseFloat(m[2]) : 1);
}

// The device's answer to one media feature.
function envFeatureValue(name) {
  switch (name) {
    case 'width': case 'device-width': return envWidth;
    case 'height': case 'device-height': return envHeight;
    case 'aspect-ratio': case 'device-aspect-ratio': return envWidth / envHeight;
    case 'resolution': case 'device-pixel-ratio': case '-webkit-device-pixel-ratio': return 1;
    case 'color': return 8;
    case 'color-index': case 'monochrome': case 'grid': return 0;
  }
  return undefined;
}

function envFeatureParse(name, value) {
  switch (name) {
    case 'width': case 'device-width': case 'height': case 'device-height': return envLength(value);
    case 'aspect-ratio': case 'device-aspect-ratio': return envRatio(value);
    case 'resolution': return envResolution(value);
    default: return parseFloat(value);
  }
}

function envCompare(a, op, b) {
  if (isNaN(a) || isNaN(b)) return false;
  switch (op) {
    case '<': return a < b;
    case '<=': return a <= b;
    case '>': return a > b;
    case '>=': return a >= b;
  }
  return Math.abs(a - b) < 1e-9;
}

function envEvalFeature(text) {
  var inner = stripWS(text.replace(/^\(/, '').replace(/\)$/, ''));
  if (/^not\s+\(/.test(inner)) return !envEvalFeature(stripWS(inner.slice(3)));
  var colon = inner.indexOf(':');
  if (colon < 0) {
    // Range syntax: (width >= 600px), (400px < width <= 900px), or a boolean feature (hover, color).
    var parts = inner.split(/\s*(<=|>=|<|>|=)\s*/);
    if (parts.length === 1) {
      switch (inner) {
        case 'hover': case 'any-hover': case 'pointer': case 'any-pointer': case 'color': case 'update':
          return true;
      }
      return !!envFeatureValue(inner);
    }
    if (parts.length === 3) {
      if (envFeatureValue(parts[0]) !== undefined) return envCompare(envFeatureValue(parts[0]), parts[1], envFeatureParse(parts[0], parts[2]));
      if (envFeatureValue(parts[2]) !== undefined) return envCompare(envFeatureParse(parts[2], parts[0]), parts[1], envFeatureValue(parts[2]));
      return false;
    }
    if (parts.length === 5 && envFeatureValue(parts[2]) !== undefined) {
      var fv = envFeatureValue(parts[2]);
      return envCompare(envFeatureParse(parts[2], parts[0]), parts[1], fv) && envCompare(fv, parts[3], envFeatureParse(parts[2], parts[4]));
    }
    return false;
  }
  var name = stripWS(inner.slice(0, colon)), value = stripWS(inner.slice(colon + 1));
  var prefix = '';
  if (/^(min|max)-/.test(name)) {
    prefix = name.slice(0, 3);
    name = name.slice(4);
  }
  name = name.replace(/^-webkit-/, '');
  if (name === 'device-pixel-ratio') name = 'resolution';
  var have = envFeatureValue(name);
  if (have !== undefined) {
    var want = name === 'resolution' && /^[\d.]+$/.test(value) ? parseFloat(value) : envFeatureParse(name, value);
    return envCompare(have, prefix === 'min' ? '>=' : prefix === 'max' ? '<=' : '=', want);
  }
  switch (name) {
    case 'orientation': return value === (envWidth >= envHeight ? 'landscape' : 'portrait');
    case 'prefers-color-scheme': return value === 'light';
    case 'prefers-reduced-motion': return value === 'reduce';
    case 'prefers-contrast': case 'prefers-reduced-transparency': case 'prefers-reduced-data': return value === 'no-preference';
    case 'forced-colors': case 'inverted-colors': return value === 'none';
    case 'hover': case 'any-hover': return value === 'hover';
    case 'pointer': case 'any-pointer': return value === 'fine';
    case 'display-mode': return value === 'browser';
    case 'scripting': return value === 'enabled';
    case 'update': return value === 'fast';
    case 'color-gamut': return value === 'srgb';
    case 'dynamic-range': case 'video-dynamic-range': return value === 'standard';
    case 'overflow-block': return value === 'scroll';
    case 'overflow-inline': return value === 'scroll';
  }
  return false;
}

function envEvalQuery(q) {
  q = stripWS(q);
  if (!q) return false;
  var negate = false;
  var m = /^(not|only)\s+/.exec(q);
  if (m) {
    negate = m[1] === 'not';
    q = q.slice(m[0].length);
  }
  var terms = q.split(/\s+and\s+/);
  var ok = true;
  for (var i = 0; i < terms.length && ok; i++) {
    var t = stripWS(terms[i]);
    if (t.charAt(0) === '(') {
      var alts = t.split(/\)\s+or\s+\(/);
      var any = false;
      for (var j = 0; j < alts.length && !any; j++) any = envEvalFeature(alts[j]);
      ok = any;
    } else {
      ok = t === 'all' || t === 'screen';
    }
  }
  return negate ? !ok : ok;
}

function envEvalMedia(text) {
  var q = stripWS(asciiLower(str(text)));
  if (q === '') return true;
  var list = q.split(',');
  for (var i = 0; i < list.length; i++) {
    if (envEvalQuery(list[i])) return true;
  }
  return false;
}

// ---------------------------------------------------------------- performance

var PerformanceEntry = iface('PerformanceEntry', null);
mix(PerformanceEntry.prototype, {
  toJSON: function () {
    return { name: this.name, entryType: this.entryType, startTime: this.startTime, duration: this.duration };
  }
});
var PerformanceMark = iface('PerformanceMark', PerformanceEntry);
var PerformanceMeasure = iface('PerformanceMeasure', PerformanceEntry);
var envPerfEntries = [];

function envPerfEntry(proto, name, type, start, duration, detail) {
  var e = objCreate(proto);
  defineProperty(e, 'name', { value: str(name), enumerable: true });
  defineProperty(e, 'entryType', { value: type, enumerable: true });
  defineProperty(e, 'startTime', { value: start, enumerable: true });
  defineProperty(e, 'duration', { value: duration, enumerable: true });
  defineProperty(e, 'detail', { value: detail === undefined ? null : detail, enumerable: true });
  if (envPerfEntries.length < 500) envPerfEntries.push(e);
  return e;
}

function envMarkTime(v) {
  if (typeof v === 'number') return v;
  if (v === undefined || v === null) return undefined;
  for (var i = envPerfEntries.length - 1; i >= 0; i--) {
    if (envPerfEntries[i].entryType === 'mark' && envPerfEntries[i].name === str(v)) return envPerfEntries[i].startTime;
  }
  throw domErr('SyntaxError', "Failed to execute 'measure' on 'Performance': The mark '" + str(v) + "' does not exist.");
}

var envTiming = null;
var Performance = iface('Performance', EventTarget);
mix(Performance.prototype, {
  now: function () { return loopNow(); },
  get timeOrigin() { return envTimeOrigin; },
  get timing() {
    if (!envTiming) {
      var t0 = Math.round(envTimeOrigin);
      envTiming = {};
      ['navigationStart', 'fetchStart', 'domainLookupStart', 'domainLookupEnd', 'connectStart', 'connectEnd',
        'requestStart', 'responseStart', 'responseEnd', 'domLoading', 'domInteractive', 'domContentLoadedEventStart',
        'domContentLoadedEventEnd', 'domComplete', 'loadEventStart', 'loadEventEnd'].forEach(function (k) { envTiming[k] = t0; });
      envTiming.unloadEventStart = envTiming.unloadEventEnd = envTiming.redirectStart = envTiming.redirectEnd = 0;
      envTiming.secureConnectionStart = 0;
      envTiming.toJSON = function () { return this; };
    }
    return envTiming;
  },
  get navigation() { return { type: 0, redirectCount: 0, TYPE_NAVIGATE: 0, TYPE_RELOAD: 1, TYPE_BACK_FORWARD: 2 }; },
  mark: function (name, options) {
    var start = options && options.startTime !== undefined ? +options.startTime : loopNow();
    return envPerfEntry(PerformanceMark.prototype, name, 'mark', start, 0, options ? options.detail : undefined);
  },
  measure: function (name, start, end) {
    var s = 0, e = loopNow(), detail;
    if (start && typeof start === 'object') {
      detail = start.detail;
      if (start.start !== undefined) s = envMarkTime(start.start);
      if (start.end !== undefined) e = envMarkTime(start.end);
      if (start.duration !== undefined) e = s + +start.duration;
    } else {
      if (start !== undefined) s = envMarkTime(start);
      if (end !== undefined) e = envMarkTime(end);
    }
    return envPerfEntry(PerformanceMeasure.prototype, name, 'measure', s, e - s, detail);
  },
  getEntries: function () { return envPerfEntries.slice(); },
  getEntriesByType: function (type) {
    return envPerfEntries.filter(function (e) { return e.entryType === str(type); });
  },
  getEntriesByName: function (name, type) {
    return envPerfEntries.filter(function (e) { return e.name === str(name) && (type === undefined || e.entryType === str(type)); });
  },
  clearMarks: function (name) {
    envPerfEntries = envPerfEntries.filter(function (e) { return e.entryType !== 'mark' || (name !== undefined && e.name !== str(name)); });
  },
  clearMeasures: function (name) {
    envPerfEntries = envPerfEntries.filter(function (e) { return e.entryType !== 'measure' || (name !== undefined && e.name !== str(name)); });
  },
  clearResourceTimings: function () {},
  setResourceTimingBufferSize: function () {},
  toJSON: function () { return { timeOrigin: envTimeOrigin }; }
});
envOnProp(Performance.prototype, 'resourcetimingbufferfull');
var envPerformance = envTarget(Performance.prototype);
envReplaceable('performance', function () { return envPerformance; });

// ---------------------------------------------------------------- crypto

var Crypto = iface('Crypto', null);
mix(Crypto.prototype, {
  getRandomValues: function (array) {
    if (!ArrayBuffer.isView(array) || array instanceof DataView || array instanceof Float32Array || array instanceof Float64Array) {
      throw domErr('TypeMismatchError', "Failed to execute 'getRandomValues' on 'Crypto': The provided ArrayBufferView is of type '" +
        (array && array.constructor ? array.constructor.name : typeof array) + "', which is not an integer array type.");
    }
    if (array.byteLength > 65536) {
      throw domErr('QuotaExceededError', "Failed to execute 'getRandomValues' on 'Crypto': The ArrayBufferView's byte length (" +
        array.byteLength + ') exceeds the number of bytes of entropy available via this API (65536).');
    }
    var bytes = new Uint8Array(N.randomBytes(array.byteLength));
    new Uint8Array(array.buffer, array.byteOffset, array.byteLength).set(bytes);
    return array;
  },
  randomUUID: function () {
    var b = new Uint8Array(N.randomBytes(16));
    b[6] = (b[6] & 0x0f) | 0x40;
    b[8] = (b[8] & 0x3f) | 0x80;
    var hex = '';
    for (var i = 0; i < 16; i++) {
      hex += (b[i] < 16 ? '0' : '') + b[i].toString(16);
      if (i === 3 || i === 5 || i === 7 || i === 9) hex += '-';
    }
    return hex;
  }
});
var envCrypto = objCreate(Crypto.prototype);
envReadonly('crypto', function () { return envCrypto; });

// ---------------------------------------------------------------- console

var envConsoleLines = 0;
var envConsoleCounts = objCreate(null);
var envConsoleTimers = objCreate(null);
var envConsoleIndent = '';

function envInspect(v, depth) {
  var t = typeof v;
  if (t === 'string') return depth ? JSON.stringify(v) : v;
  if (v === null || t === 'undefined' || t === 'number' || t === 'boolean' || t === 'bigint') return String(v);
  if (t === 'symbol') return v.toString();
  if (t === 'function') return '[Function: ' + (v.name || 'anonymous') + ']';
  try {
    var r = unwrap(v);
    if (r) return r.t === 1 ? '<' + r.ln + '>' : str(v.nodeName);
    if (v instanceof Error) return envErrorText(v);
    if (depth > 1) return Array.isArray(v) ? '[Array]' : '[Object]';
    if (Array.isArray(v)) {
      var items = [];
      for (var i = 0; i < v.length && i < 10; i++) items.push(envInspect(v[i], depth + 1));
      if (v.length > 10) items.push('… ' + (v.length - 10) + ' more');
      return '[' + items.join(', ') + ']';
    }
    var keys = Object.keys(v), parts = [];
    for (var k = 0; k < keys.length && k < 10; k++) parts.push(keys[k] + ': ' + envInspect(v[keys[k]], depth + 1));
    if (keys.length > 10) parts.push('…');
    return '{' + parts.join(', ') + '}';
  } catch (e) {
    return '[object]';
  }
}

function envFormatArgs(args) {
  var parts = [];
  var start = 0;
  if (typeof args[0] === 'string' && args[0].indexOf('%') >= 0) {
    var i = 1;
    parts.push(args[0].replace(/%[sdifoOc%]/g, function (m) {
      if (m === '%%') return '%';
      if (i >= args.length) return m;
      var a = args[i++];
      switch (m) {
        case '%s': return typeof a === 'string' ? a : envInspect(a, 1);
        case '%d': case '%i': return String(parseInt(a, 10));
        case '%f': return String(parseFloat(a));
        case '%c': return '';
      }
      return envInspect(a, 1);
    }));
    start = i;
  }
  for (var j = start; j < args.length && j < 12; j++) parts.push(envInspect(args[j], 0));
  return envConsoleIndent + parts.join(' ');
}

function envLog(level, args) {
  if (envConsoleLines >= 60) return;
  envConsoleLines++;
  var text;
  try { text = envFormatArgs(args); } catch (e) { text = '[unprintable]'; }
  if (text.length > 600) text = text.slice(0, 600) + '…';
  try { N.log(level, text); } catch (e) { /* the log is best effort */ }
}

var envConsole = {};
['log', 'info', 'debug', 'warn', 'error', 'trace', 'dir', 'dirxml', 'table'].forEach(function (name) {
  var level = name === 'warn' || name === 'error' || name === 'info' || name === 'debug' ? name : 'log';
  envConsole[name] = function () { envLog(level, arguments); };
});
mix(envConsole, {
  assert: function (cond) {
    if (cond) return;
    var rest = arraySlice.call(arguments, 1);
    rest.unshift('Assertion failed:');
    envLog('error', rest);
  },
  count: function (label) {
    var l = label === undefined ? 'default' : str(label);
    envConsoleCounts[l] = (envConsoleCounts[l] || 0) + 1;
    envLog('log', [l + ': ' + envConsoleCounts[l]]);
  },
  countReset: function (label) { envConsoleCounts[label === undefined ? 'default' : str(label)] = 0; },
  group: function () { if (arguments.length) envLog('log', arguments); envConsoleIndent += '  '; },
  groupCollapsed: function () { if (arguments.length) envLog('log', arguments); envConsoleIndent += '  '; },
  groupEnd: function () { envConsoleIndent = envConsoleIndent.slice(2); },
  time: function (label) { envConsoleTimers[label === undefined ? 'default' : str(label)] = loopNow(); },
  timeLog: function (label) {
    var l = label === undefined ? 'default' : str(label);
    if (envConsoleTimers[l] !== undefined) envLog('log', [l + ': ' + (loopNow() - envConsoleTimers[l]).toFixed(1) + ' ms']);
  },
  timeEnd: function (label) {
    var l = label === undefined ? 'default' : str(label);
    if (envConsoleTimers[l] !== undefined) {
      envLog('log', [l + ': ' + (loopNow() - envConsoleTimers[l]).toFixed(1) + ' ms']);
      delete envConsoleTimers[l];
    }
  },
  timeStamp: function () {},
  profile: function () {},
  profileEnd: function () {},
  clear: function () {}
});
defineGlobal('console', envConsole);

// ---------------------------------------------------------------- Notification and friends

var Notification = iface('Notification', EventTarget, function Notification() {
  throw new TypeError("Failed to construct 'Notification': Notifications are not available in this browser.");
});
defineProperty(Notification, 'permission', { get: function () { return 'denied'; }, enumerable: true, configurable: true });
defineProperty(Notification, 'maxActions', { value: 0, enumerable: true, configurable: true });
Notification.requestPermission = function (callback) {
  if (typeof callback === 'function') loopQueueTask(function () { callback('denied'); });
  return Promise.resolve('denied');
};

defineGlobal('external', { AddSearchProvider: function () {}, IsSearchProviderInstalled: function () { return 0; } });
