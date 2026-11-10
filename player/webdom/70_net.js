// 70_net.js — fetch and the objects around it: Headers, Request, Response, Blob, File, FileReader, FormData,
// URL, URLSearchParams, TextEncoder/TextDecoder, AbortController/AbortSignal, XMLHttpRequest, and stubs for
// WebSocket and EventSource.
//
// Every request goes out through Go (N.fetch, N.fetchSync), where webdom_net.go applies the browser's rules:
// the ad/tracker filter, no requests from a public page into the LAN or loopback, cookies for the page's own
// site only, and CORS for reading a cross-origin response. Responses come back as tasks (host.fetchDone).
// Bodies are whole byte arrays — there are no streams; Response.body is a one-chunk reader for code that
// insists on one.

var BLOB = Symbol('jukahub.blob');
var BODY = Symbol('jukahub.body');
var HDR = Symbol('jukahub.headers');
var REQ = Symbol('jukahub.request');
var RES = Symbol('jukahub.response');
var SIG = Symbol('jukahub.signal');
var CTRL = Symbol('jukahub.controller');
var XHR = Symbol('jukahub.xhr');
var USP = Symbol('jukahub.searchParams');
var URLI = Symbol('jukahub.url');
var FD = Symbol('jukahub.formData');
var FR = Symbol('jukahub.fileReader');
var TD = Symbol('jukahub.textDecoder');
var WS = Symbol('jukahub.socket');

var netPending = objCreate(null);   // request id → fetch entry or XHR entry
var netNextId = 1;
var netBlobURLs = objCreate(null);
var netStats = { fetches: 0, failed: 0 };

function netIterator(items, tag) {
  var i = 0;
  var it = {
    next: function () {
      return i < items.length ? { value: items[i++], done: false } : { value: undefined, done: true };
    }
  };
  it[Symbol.iterator] = function () { return this; };
  defineProperty(it, Symbol.toStringTag, { value: tag, configurable: true });
  return it;
}

// ---------------------------------------------------------------- bytes

function netUTF8(s) { return new Uint8Array(N.utf8Encode(str(s))); }

function netDecode(bytes, label) {
  var out = N.utf8Decode(bytes, label || 'utf-8', false, false);
  return out === null || out === undefined ? '' : str(out);
}

function netConcat(chunks) {
  var total = 0, i;
  for (i = 0; i < chunks.length; i++) total += chunks[i].length;
  var out = new Uint8Array(total);
  var off = 0;
  for (i = 0; i < chunks.length; i++) {
    out.set(chunks[i], off);
    off += chunks[i].length;
  }
  return out;
}

function netCopyBuffer(bytes) {
  var ab = new ArrayBuffer(bytes.length);
  new Uint8Array(ab).set(bytes);
  return ab;
}

// netBufferBytes views an ArrayBuffer or ArrayBufferView as bytes (no copy); null for anything else.
function netBufferBytes(v) {
  if (v instanceof ArrayBuffer) return new Uint8Array(v);
  if (ArrayBuffer.isView(v)) return new Uint8Array(v.buffer, v.byteOffset, v.byteLength);
  return null;
}

function netBinaryString(bytes) {
  var parts = [];
  for (var i = 0; i < bytes.length; i += 8192) {
    parts.push(String.fromCharCode.apply(null, Array.prototype.slice.call(bytes.subarray(i, Math.min(bytes.length, i + 8192)))));
  }
  return parts.join('');
}

function netRandomToken() {
  var b = new Uint8Array(N.randomBytes(12));
  var s = '';
  for (var i = 0; i < b.length; i++) s += (b[i] < 16 ? '0' : '') + b[i].toString(16);
  return s;
}

// ---------------------------------------------------------------- Blob, File, FileReader

function netPartBytes(p) {
  if (p instanceof Blob) return p[BLOB].data;
  var b = netBufferBytes(p);
  if (b) return new Uint8Array(b);
  return netUTF8(p);
}

function netMakeBlob(bytes, type, proto) {
  var b = objCreate(proto || Blob.prototype);
  defineProperty(b, BLOB, { value: { data: bytes, type: netBlobType(type), name: '', modified: 0 } });
  return b;
}

function netBlobType(type) {
  var t = type === undefined || type === null ? '' : str(type);
  return /[^ -~]/.test(t) ? '' : t.toLowerCase();
}

function netRelIndex(v, n) {
  var i = Math.trunc(+v) || 0;
  return i < 0 ? Math.max(n + i, 0) : Math.min(i, n);
}

var Blob = iface('Blob', null, function Blob(parts, options) {
  if (!(this instanceof Blob)) throw new TypeError("Failed to construct 'Blob': Please use the 'new' operator.");
  var chunks = [];
  if (parts !== undefined && parts !== null) {
    if (typeof parts !== 'object' || typeof parts[Symbol.iterator] !== 'function') {
      throw new TypeError("Failed to construct 'Blob': The provided value cannot be converted to a sequence.");
    }
    var arr = Array.from(parts);
    for (var i = 0; i < arr.length; i++) chunks.push(netPartBytes(arr[i]));
  }
  defineProperty(this, BLOB, {
    value: { data: netConcat(chunks), type: netBlobType(options && options.type), name: '', modified: 0 }
  });
});
mix(Blob.prototype, {
  get size() { return this[BLOB].data.length; },
  get type() { return this[BLOB].type; },
  slice: function (start, end, type) {
    var d = this[BLOB].data, n = d.length;
    var s = start === undefined ? 0 : netRelIndex(start, n);
    var e = end === undefined ? n : netRelIndex(end, n);
    return netMakeBlob(d.slice(s, Math.max(s, e)), type);
  },
  text: function () { return Promise.resolve(netDecode(this[BLOB].data)); },
  arrayBuffer: function () { return Promise.resolve(netCopyBuffer(this[BLOB].data)); },
  bytes: function () { return Promise.resolve(new Uint8Array(this[BLOB].data)); },
  stream: function () { return netStream(this[BLOB].data, null); }
});

var File = iface('File', Blob, function File(bits, name, options) {
  if (!(this instanceof File)) throw new TypeError("Failed to construct 'File': Please use the 'new' operator.");
  if (arguments.length < 2) throw new TypeError("Failed to construct 'File': 2 arguments required, but only " + arguments.length + ' present.');
  Blob.call(this, bits, options);
  this[BLOB].name = str(name);
  this[BLOB].modified = options && options.lastModified !== undefined ? +options.lastModified : Math.round(+N.now());
});
mix(File.prototype, {
  get name() { return this[BLOB].name; },
  get lastModified() { return this[BLOB].modified; },
  get webkitRelativePath() { return ''; }
});

var FileReader = iface('FileReader', EventTarget, function FileReader() {
  if (!(this instanceof FileReader)) throw new TypeError("Failed to construct 'FileReader': Please use the 'new' operator.");
  defineProperty(this, FR, { value: { state: 0, result: null, error: null, gen: 0 } });
});
constants(FileReader, { EMPTY: 0, LOADING: 1, DONE: 2 });
constants(FileReader.prototype, { EMPTY: 0, LOADING: 1, DONE: 2 });

function netFileRead(reader, blob, kind, label) {
  var st = reader[FR];
  if (!(blob instanceof Blob)) throw new TypeError("Failed to execute 'read' on 'FileReader': parameter 1 is not of type 'Blob'.");
  if (st.state === 1) throw domErr('InvalidStateError', "Failed to execute 'read' on 'FileReader': The object is already busy reading Blobs.");
  st.state = 1;
  st.result = null;
  var gen = ++st.gen;
  var data = blob[BLOB].data, type = blob[BLOB].type;
  loopQueueTask(function () {
    if (gen !== st.gen) return;
    var total = { lengthComputable: true, loaded: 0, total: data.length };
    fireEvent(reader, 'loadstart', total, ProgressEvent);
    if (gen !== st.gen) return;
    var result;
    switch (kind) {
      case 'text': result = netDecode(data, label); break;
      case 'dataurl': result = 'data:' + (type || 'application/octet-stream') + ';base64,' + N.btoa(netBinaryString(data)); break;
      case 'binary': result = netBinaryString(data); break;
      default: result = netCopyBuffer(data);
    }
    st.state = 2;
    st.result = result;
    var done = { lengthComputable: true, loaded: data.length, total: data.length };
    fireEvent(reader, 'progress', done, ProgressEvent);
    fireEvent(reader, 'load', done, ProgressEvent);
    if (st.state === 2) fireEvent(reader, 'loadend', done, ProgressEvent);
  });
}

mix(FileReader.prototype, {
  get readyState() { return this[FR].state; },
  get result() { return this[FR].result; },
  get error() { return this[FR].error; },
  readAsArrayBuffer: function (blob) { netFileRead(this, blob, 'buffer'); },
  readAsBinaryString: function (blob) { netFileRead(this, blob, 'binary'); },
  readAsDataURL: function (blob) { netFileRead(this, blob, 'dataurl'); },
  readAsText: function (blob, encoding) { netFileRead(this, blob, 'text', encoding === undefined ? 'utf-8' : str(encoding)); },
  abort: function () {
    var st = this[FR];
    if (st.state !== 1) return;
    st.gen++;
    st.state = 2;
    st.result = null;
    fireEvent(this, 'abort', { lengthComputable: false, loaded: 0, total: 0 }, ProgressEvent);
    if (st.state === 2) fireEvent(this, 'loadend', { lengthComputable: false, loaded: 0, total: 0 }, ProgressEvent);
  }
});
envOnProps(FileReader.prototype, ['loadstart', 'progress', 'load', 'abort', 'error', 'loadend']);

// A one-chunk stand-in for ReadableStream (Response.body, Blob.stream()).
function netStream(bytes, owner) {
  var done = false;
  var stream = {
    locked: false,
    getReader: function () {
      stream.locked = true;
      return {
        closed: Promise.resolve(undefined),
        read: function () {
          if (owner) owner[BODY].used = true;
          if (done) return Promise.resolve({ value: undefined, done: true });
          done = true;
          return Promise.resolve({ value: new Uint8Array(bytes), done: false });
        },
        cancel: function () { done = true; return Promise.resolve(undefined); },
        releaseLock: function () { stream.locked = false; }
      };
    },
    cancel: function () { done = true; return Promise.resolve(undefined); }
  };
  return stream;
}

// ---------------------------------------------------------------- Headers

var NET_TOKEN = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;

function netHeaderName(name, what) {
  var n = str(name);
  if (!NET_TOKEN.test(n)) throw new TypeError("Failed to execute '" + what + "' on 'Headers': Invalid name");
  return n.toLowerCase();
}

function netHeaderValue(value, what) {
  var v = str(value).replace(/^[\t\n\r ]+|[\t\n\r ]+$/g, '');
  if (/[\0\r\n]/.test(v)) throw new TypeError("Failed to execute '" + what + "' on 'Headers': Invalid value");
  return v;
}

function netHeadersState(h) {
  var st = h[HDR];
  if (!st) throw new TypeError('Illegal invocation');
  return st;
}

function netGuard(st, what) {
  if (st.guard === 'immutable') throw new TypeError("Failed to execute '" + what + "' on 'Headers': Headers are immutable");
}

function netFillHeaders(h, init) {
  if (init === undefined || init === null) return;
  if (init instanceof Headers) {
    var src = init[HDR].list;
    for (var i = 0; i < src.length; i++) h.append(src[i][0], src[i][1]);
    return;
  }
  if (typeof init !== 'object' && typeof init !== 'function') {
    throw new TypeError("Failed to construct 'Headers': The provided value is not of type '(record<ByteString, ByteString> or sequence<sequence<ByteString>>)'.");
  }
  if (typeof init[Symbol.iterator] === 'function') {
    var pairs = Array.from(init);
    for (var j = 0; j < pairs.length; j++) {
      var p = pairs[j];
      if (!p || typeof p !== 'object' || p.length !== 2) throw new TypeError("Failed to construct 'Headers': Invalid value");
      h.append(p[0], p[1]);
    }
    return;
  }
  var keys = Object.keys(init);
  for (var k = 0; k < keys.length; k++) h.append(keys[k], init[keys[k]]);
}

// The sorted-and-combined view the iterators and forEach expose.
function netHeaderView(st) {
  var names = [], seen = objCreate(null), i;
  for (i = 0; i < st.list.length; i++) {
    var n = st.list[i][0];
    if (!seen[n]) {
      seen[n] = [];
      names.push(n);
    }
    seen[n].push(st.list[i][1]);
  }
  names.sort();
  var out = [];
  for (i = 0; i < names.length; i++) {
    if (names[i] === 'set-cookie') {
      for (var j = 0; j < seen[names[i]].length; j++) out.push([names[i], seen[names[i]][j]]);
    } else {
      out.push([names[i], seen[names[i]].join(', ')]);
    }
  }
  return out;
}

var Headers = iface('Headers', null, function Headers(init) {
  if (!(this instanceof Headers)) throw new TypeError("Failed to construct 'Headers': Please use the 'new' operator.");
  defineProperty(this, HDR, { value: { list: [], guard: 'none' } });
  netFillHeaders(this, init);
});
mix(Headers.prototype, {
  append: function (name, value) {
    var st = netHeadersState(this);
    netGuard(st, 'append');
    st.list.push([netHeaderName(name, 'append'), netHeaderValue(value, 'append')]);
  },
  delete: function (name) {
    var st = netHeadersState(this);
    netGuard(st, 'delete');
    var n = netHeaderName(name, 'delete');
    st.list = st.list.filter(function (p) { return p[0] !== n; });
  },
  get: function (name) {
    var st = netHeadersState(this);
    var n = netHeaderName(name, 'get');
    var vals = [];
    for (var i = 0; i < st.list.length; i++) if (st.list[i][0] === n) vals.push(st.list[i][1]);
    return vals.length ? vals.join(', ') : null;
  },
  getSetCookie: function () { return []; },
  has: function (name) {
    var st = netHeadersState(this);
    var n = netHeaderName(name, 'has');
    for (var i = 0; i < st.list.length; i++) if (st.list[i][0] === n) return true;
    return false;
  },
  set: function (name, value) {
    var st = netHeadersState(this);
    netGuard(st, 'set');
    var n = netHeaderName(name, 'set'), v = netHeaderValue(value, 'set');
    var out = [], done = false;
    for (var i = 0; i < st.list.length; i++) {
      if (st.list[i][0] !== n) out.push(st.list[i]);
      else if (!done) {
        out.push([n, v]);
        done = true;
      }
    }
    if (!done) out.push([n, v]);
    st.list = out;
  },
  forEach: function (callback, thisArg) {
    var view = netHeaderView(netHeadersState(this));
    for (var i = 0; i < view.length; i++) callback.call(thisArg, view[i][1], view[i][0], this);
  },
  entries: function () { return netIterator(netHeaderView(netHeadersState(this)), 'Headers Iterator'); },
  keys: function () {
    return netIterator(netHeaderView(netHeadersState(this)).map(function (p) { return p[0]; }), 'Headers Iterator');
  },
  values: function () {
    return netIterator(netHeaderView(netHeadersState(this)).map(function (p) { return p[1]; }), 'Headers Iterator');
  }
});
defineProperty(Headers.prototype, Symbol.iterator, { value: Headers.prototype.entries, writable: true, configurable: true });

function netImmutableHeaders(pairs) {
  var h = new Headers();
  var list = h[HDR].list;
  for (var i = 0; i < pairs.length; i++) list.push([str(pairs[i][0]).toLowerCase(), str(pairs[i][1])]);
  h[HDR].guard = 'immutable';
  return h;
}

function netCharsetOf(contentType) {
  var m = /;\s*charset\s*=\s*"?([^";\s]+)/i.exec(contentType || '');
  return m ? m[1] : 'utf-8';
}

// ---------------------------------------------------------------- bodies

function netExtractBody(body) {
  if (body === undefined || body === null) return { bytes: null, type: null };
  if (typeof body === 'string') return { bytes: netUTF8(body), type: 'text/plain;charset=UTF-8' };
  if (body instanceof URLSearchParams) return { bytes: netUTF8(body.toString()), type: 'application/x-www-form-urlencoded;charset=UTF-8' };
  if (body instanceof FormData) return netMultipart(body);
  if (body instanceof Blob) return { bytes: new Uint8Array(body[BLOB].data), type: body[BLOB].type || null };
  var b = netBufferBytes(body);
  if (b) return { bytes: new Uint8Array(b), type: null };
  if (unwrap(body) && body.nodeType === 9) {
    return { bytes: netUTF8(body.documentElement ? body.documentElement.outerHTML : ''), type: 'text/html;charset=UTF-8' };
  }
  return { bytes: netUTF8(str(body)), type: 'text/plain;charset=UTF-8' };
}

function netConsume(o, what, iname) {
  var b = o[BODY];
  if (!b) return Promise.reject(new TypeError('Illegal invocation'));
  if (b.used) return Promise.reject(new TypeError("Failed to execute '" + what + "' on '" + iname + "': body stream already read"));
  b.used = true;
  return Promise.resolve(b.bytes || new Uint8Array(0));
}

function netBodyMixin(iname) {
  return {
    get body() { var b = this[BODY]; return b && b.bytes ? netStream(b.bytes, this) : null; },
    get bodyUsed() { var b = this[BODY]; return !!(b && b.used); },
    text: function () {
      var self = this;
      return netConsume(this, 'text', iname).then(function (bytes) {
        return netDecode(bytes, netCharsetOf(self.headers.get('content-type')));
      });
    },
    json: function () {
      return this.text().then(function (t) { return JSON.parse(t); });
    },
    arrayBuffer: function () { return netConsume(this, 'arrayBuffer', iname).then(netCopyBuffer); },
    bytes: function () { return netConsume(this, 'bytes', iname).then(function (b) { return new Uint8Array(b); }); },
    blob: function () {
      var self = this;
      return netConsume(this, 'blob', iname).then(function (bytes) {
        return netMakeBlob(new Uint8Array(bytes), self.headers.get('content-type') || '');
      });
    },
    formData: function () {
      var self = this;
      return this.text().then(function (t) {
        var ct = self.headers.get('content-type') || '';
        if (!/application\/x-www-form-urlencoded/i.test(ct)) {
          throw new TypeError("Failed to execute 'formData' on '" + iname + "': Invalid MIME type");
        }
        var fd = new FormData();
        netParseQuery(t).forEach(function (p) { fd.append(p[0], p[1]); });
        return fd;
      });
    }
  };
}

// ---------------------------------------------------------------- Request, Response

function netMethod(m, what) {
  var s = str(m);
  if (!NET_TOKEN.test(s)) throw new TypeError("Failed to execute '" + what + "': '" + s + "' is not a valid HTTP method.");
  var u = s.toUpperCase();
  if (u === 'CONNECT' || u === 'TRACE' || u === 'TRACK') throw new TypeError("Failed to execute '" + what + "': '" + s + "' HTTP method is unsupported.");
  return /^(DELETE|GET|HEAD|OPTIONS|POST|PUT|PATCH)$/.test(u) ? u : s;
}

var Request = iface('Request', null, function Request(input, init) {
  if (!(this instanceof Request)) throw new TypeError("Failed to construct 'Request': Please use the 'new' operator.");
  if (arguments.length < 1) throw new TypeError("Failed to construct 'Request': 1 argument required, but only 0 present.");
  init = init || {};
  var src = input instanceof Request ? input[REQ] : null;
  var url;
  if (src) {
    url = src.url;
  } else {
    url = resolveURL(mainDoc, input instanceof URL ? input.href : str(input));
    if (!url) throw new TypeError("Failed to construct 'Request': Failed to parse URL from " + str(input));
  }
  var method = init.method !== undefined ? netMethod(init.method, 'Request') : (src ? src.method : 'GET');
  var headers = new Headers(init.headers !== undefined ? init.headers : (src ? src.headers : undefined));
  var bytes = null;
  if (init.body !== undefined && init.body !== null) {
    if (method === 'GET' || method === 'HEAD') throw new TypeError("Failed to construct 'Request': Request with GET/HEAD method cannot have body.");
    var ex = netExtractBody(init.body);
    bytes = ex.bytes;
    if (ex.type && !headers.has('content-type')) headers.append('content-type', ex.type);
  } else if (src && input[BODY]) {
    if (input[BODY].used) throw new TypeError("Failed to construct 'Request': Cannot construct a Request with a Request object that has already been used.");
    bytes = input[BODY].bytes;
  }
  var st = {
    url: url, method: method, headers: headers,
    mode: str(init.mode || (src ? src.mode : 'cors')),
    credentials: str(init.credentials || (src ? src.credentials : 'same-origin')),
    cache: str(init.cache || (src ? src.cache : 'default')),
    redirect: str(init.redirect || (src ? src.redirect : 'follow')),
    referrer: init.referrer !== undefined ? str(init.referrer) : (src ? src.referrer : 'about:client'),
    referrerPolicy: str(init.referrerPolicy || (src ? src.referrerPolicy : '')),
    integrity: str(init.integrity || (src ? src.integrity : '')),
    keepalive: !!(init.keepalive !== undefined ? init.keepalive : (src && src.keepalive)),
    signal: init.signal !== undefined ? init.signal : (src ? src.signal : null)
  };
  if (!st.signal) st.signal = netNewSignal();
  defineProperty(this, REQ, { value: st });
  defineProperty(this, BODY, { value: { bytes: bytes, used: false } });
});
mix(Request.prototype, {
  get url() { return this[REQ].url; },
  get method() { return this[REQ].method; },
  get headers() { return this[REQ].headers; },
  get mode() { return this[REQ].mode; },
  get credentials() { return this[REQ].credentials; },
  get cache() { return this[REQ].cache; },
  get redirect() { return this[REQ].redirect; },
  get referrer() { return this[REQ].referrer; },
  get referrerPolicy() { return this[REQ].referrerPolicy; },
  get integrity() { return this[REQ].integrity; },
  get keepalive() { return this[REQ].keepalive; },
  get signal() { return this[REQ].signal; },
  get destination() { return ''; },
  get isHistoryNavigation() { return false; },
  get duplex() { return 'half'; },
  clone: function () {
    if (this[BODY].used) throw new TypeError("Failed to execute 'clone' on 'Request': Request body is already used");
    return new Request(this);
  }
});
mix(Request.prototype, netBodyMixin('Request'));

function netInitResponse(o, st, bytes) {
  defineProperty(o, RES, { value: st });
  defineProperty(o, BODY, { value: { bytes: bytes, used: false } });
  return o;
}

var Response = iface('Response', null, function Response(body, init) {
  if (!(this instanceof Response)) throw new TypeError("Failed to construct 'Response': Please use the 'new' operator.");
  init = init || {};
  var status = init.status === undefined ? 200 : init.status | 0;
  if (status < 200 || status > 599) {
    throw new RangeError("Failed to construct 'Response': The status provided (" + status + ') is outside the range [200, 599].');
  }
  var headers = new Headers(init.headers);
  var ex = netExtractBody(body);
  if (ex.bytes && (status === 101 || status === 204 || status === 205 || status === 304)) {
    throw new TypeError("Failed to construct 'Response': Response with null body status cannot have body");
  }
  if (ex.type && !headers.has('content-type')) headers.append('content-type', ex.type);
  netInitResponse(this, {
    type: 'default', url: '', status: status, statusText: init.statusText !== undefined ? str(init.statusText) : '',
    headers: headers, redirected: false
  }, ex.bytes);
});
mix(Response.prototype, {
  get type() { return this[RES].type; },
  get url() { return this[RES].url; },
  get redirected() { return this[RES].redirected; },
  get status() { return this[RES].status; },
  get ok() { var s = this[RES].status; return s >= 200 && s < 300; },
  get statusText() { return this[RES].statusText; },
  get headers() { return this[RES].headers; },
  clone: function () {
    if (this[BODY].used) throw new TypeError("Failed to execute 'clone' on 'Response': Response body is already used");
    var st = this[RES];
    var copy = {};
    for (var k in st) copy[k] = st[k];
    copy.headers = new Headers(st.headers);
    copy.headers[HDR].guard = st.headers[HDR].guard;
    return netInitResponse(objCreate(Response.prototype), copy, this[BODY].bytes);
  }
});
mix(Response.prototype, netBodyMixin('Response'));
Response.error = function () {
  return netInitResponse(objCreate(Response.prototype), {
    type: 'error', url: '', status: 0, statusText: '', headers: netImmutableHeaders([]), redirected: false
  }, null);
};
Response.redirect = function (url, status) {
  var s = status === undefined ? 302 : status | 0;
  if ([301, 302, 303, 307, 308].indexOf(s) < 0) throw new RangeError("Failed to execute 'redirect' on 'Response': Invalid status code");
  var abs = resolveURL(mainDoc, str(url));
  if (!abs) throw new TypeError("Failed to execute 'redirect' on 'Response': Invalid URL");
  return netInitResponse(objCreate(Response.prototype), {
    type: 'default', url: '', status: s, statusText: '', headers: netImmutableHeaders([['location', abs]]), redirected: false
  }, null);
};
Response.json = function (data, init) {
  var text = JSON.stringify(data);
  if (text === undefined) throw new TypeError("Failed to execute 'json' on 'Response': The data is not JSON serializable");
  var r = new Response(text, init);
  r[RES].headers.set('content-type', 'application/json');
  return r;
};

function netMakeResponse(resp) {
  var type = str(resp.type || 'basic');
  var opaque = type === 'opaque' || type === 'opaqueredirect';
  var bytes = opaque ? null : (resp.body ? new Uint8Array(resp.body) : new Uint8Array(0));
  return netInitResponse(objCreate(Response.prototype), {
    type: type, url: str(resp.url || ''), status: resp.status | 0, statusText: str(resp.statusText || ''),
    headers: netImmutableHeaders(opaque ? [] : (resp.headers || [])), redirected: !!resp.redirected
  }, bytes);
}

// ---------------------------------------------------------------- AbortController, AbortSignal

var AbortSignal = iface('AbortSignal', EventTarget);

function netNewSignal() {
  var s = envTarget(AbortSignal.prototype);
  defineProperty(s, SIG, { value: { aborted: false, reason: undefined } });
  return s;
}

function netAbortSignal(sig, reason) {
  var st = sig[SIG];
  if (!st || st.aborted) return;
  st.aborted = true;
  st.reason = reason === undefined ? domErr('AbortError', 'signal is aborted without reason') : reason;
  fireEvent(sig, 'abort', {}, Event);
}

mix(AbortSignal.prototype, {
  get aborted() { return !!(this[SIG] && this[SIG].aborted); },
  get reason() { return this[SIG] ? this[SIG].reason : undefined; },
  throwIfAborted: function () { if (this[SIG] && this[SIG].aborted) throw this[SIG].reason; }
});
envOnProp(AbortSignal.prototype, 'abort');
AbortSignal.abort = function (reason) {
  var s = netNewSignal();
  netAbortSignal(s, reason);
  return s;
};
AbortSignal.timeout = function (ms) {
  var s = netNewSignal();
  loopAddTimer(function () { netAbortSignal(s, domErr('TimeoutError', 'signal timed out')); }, ms, null, false);
  return s;
};
AbortSignal.any = function (signals) {
  var s = netNewSignal();
  var list = Array.from(signals || []);
  for (var i = 0; i < list.length; i++) {
    if (list[i] && list[i].aborted) {
      netAbortSignal(s, list[i].reason);
      return s;
    }
  }
  list.forEach(function (sig) {
    if (sig && typeof sig.addEventListener === 'function') {
      sig.addEventListener('abort', function () { netAbortSignal(s, sig.reason); });
    }
  });
  return s;
};

var AbortController = iface('AbortController', null, function AbortController() {
  if (!(this instanceof AbortController)) throw new TypeError("Failed to construct 'AbortController': Please use the 'new' operator.");
  defineProperty(this, CTRL, { value: netNewSignal() });
});
mix(AbortController.prototype, {
  get signal() { return this[CTRL]; },
  abort: function (reason) { netAbortSignal(this[CTRL], reason); }
});

// ---------------------------------------------------------------- fetch

function netHeaderPairs(h) {
  var list = h[HDR].list, out = [];
  for (var i = 0; i < list.length; i++) out.push([list[i][0], list[i][1]]);
  return out;
}

function netFailed(why, url) {
  netStats.failed++;
  if (netStats.failed <= 10) {
    try { N.log('warn', 'request failed: ' + str(why) + ' (' + envNoFragment(str(url)).split('?')[0] + ')'); } catch (e) { /* best effort */ }
  }
}

defineGlobal('fetch', function fetch(input, init) {
  var req;
  try {
    req = new Request(input, init);
  } catch (e) {
    return Promise.reject(e);
  }
  var st = req[REQ];
  if (st.signal && st.signal.aborted) return Promise.reject(st.signal.reason);
  return new Promise(function (resolve, reject) {
    var u = st.url;
    if (/^blob:/i.test(u)) {
      var blob = netBlobURLs[envNoFragment(u)];
      if (!blob || st.method !== 'GET') {
        reject(new TypeError('Failed to fetch'));
        return;
      }
      resolve(netInitResponse(objCreate(Response.prototype), {
        type: 'basic', url: u, status: 200, statusText: 'OK', redirected: false,
        headers: netImmutableHeaders([['content-type', blob[BLOB].type], ['content-length', String(blob[BLOB].data.length)]])
      }, blob[BLOB].data));
      return;
    }
    if (!/^(https?|data):/i.test(u)) {
      reject(new TypeError('Failed to fetch'));
      return;
    }
    var id = netNextId++;
    var entry = { resolve: resolve, reject: reject, signal: st.signal, onabort: null, url: u };
    netPending[id] = entry;
    if (st.signal) {
      entry.onabort = function () {
        if (!netPending[id]) return;
        delete netPending[id];
        reject(st.signal.reason);
      };
      st.signal.addEventListener('abort', entry.onabort);
    }
    netStats.fetches++;
    var ok = false;
    try {
      ok = N.fetch(id, {
        method: st.method, url: u, headers: netHeaderPairs(st.headers), body: req[BODY].bytes || null,
        credentials: st.credentials, mode: st.mode === 'navigate' ? 'same-origin' : st.mode, redirect: st.redirect
      });
    } catch (e) {
      ok = false;
    }
    if (!ok) {
      delete netPending[id];
      netFailed('refused', u);
      reject(new TypeError('Failed to fetch'));
    }
  });
});

// host.fetchDone: a fetch() or XMLHttpRequest completion, delivered by Go as its own task.
function netFetchDone(id, resp) {
  var entry = netPending[id];
  if (!entry) return;
  delete netPending[id];
  if (entry.xhr) {
    netXHRDone(entry.xhr, resp, entry.gen);
    return;
  }
  if (entry.signal && entry.onabort) entry.signal.removeEventListener('abort', entry.onabort);
  if (!resp || resp.err) {
    netFailed(resp ? resp.err : 'no response', entry.url);
    entry.reject(new TypeError('Failed to fetch'));
    return;
  }
  entry.resolve(netMakeResponse(resp));
}

// ---------------------------------------------------------------- XMLHttpRequest

var XMLHttpRequestEventTarget = iface('XMLHttpRequestEventTarget', EventTarget);
envOnProps(XMLHttpRequestEventTarget.prototype, ['loadstart', 'progress', 'abort', 'error', 'load', 'timeout', 'loadend']);
var XMLHttpRequestUpload = iface('XMLHttpRequestUpload', XMLHttpRequestEventTarget);

var XMLHttpRequest = iface('XMLHttpRequest', XMLHttpRequestEventTarget, function XMLHttpRequest() {
  if (!(this instanceof XMLHttpRequest)) throw new TypeError("Failed to construct 'XMLHttpRequest': Please use the 'new' operator.");
  defineProperty(this, XHR, {
    value: {
      state: 0, method: 'GET', url: '', async: true, headers: [], sent: false, status: 0, statusText: '', respURL: '',
      respHeaders: [], body: null, responseType: '', withCredentials: false, timeout: 0, timer: 0, id: 0, gen: 0,
      mime: null, error: false, upload: envTarget(XMLHttpRequestUpload.prototype), text: null, value: undefined
    }
  });
});
var XHR_STATES = { UNSENT: 0, OPENED: 1, HEADERS_RECEIVED: 2, LOADING: 3, DONE: 4 };
constants(XMLHttpRequest, XHR_STATES);
constants(XMLHttpRequest.prototype, XHR_STATES);
envOnProp(XMLHttpRequest.prototype, 'readystatechange');

function netXHR(o) {
  var x = o && o[XHR];
  if (!x) throw new TypeError('Illegal invocation');
  return x;
}

function netXHRFire(o, type, progress) {
  var x = o[XHR];
  if (progress) {
    var n = x.body ? x.body.length : 0;
    fireEvent(o, type, { lengthComputable: n > 0, loaded: n, total: n }, ProgressEvent);
  } else {
    fireEvent(o, type, {}, Event);
  }
}

function netXHRCancel(x) {
  x.gen++;
  if (x.id && netPending[x.id]) delete netPending[x.id];
  x.id = 0;
  if (x.timer) loopClear(x.timer);
  x.timer = 0;
}

function netXHRReset(x) {
  x.status = 0;
  x.statusText = '';
  x.respURL = '';
  x.respHeaders = [];
  x.body = null;
  x.text = null;
  x.value = undefined;
  x.error = false;
}

function netXHRDone(o, resp, gen) {
  var x = o[XHR];
  if (gen !== undefined && gen !== x.gen) return;
  var g = x.gen;
  if (x.timer) {
    loopClear(x.timer);
    x.timer = 0;
  }
  x.id = 0;
  if (!resp || resp.err) {
    netFailed(resp ? resp.err : 'no response', x.url);
    netXHRReset(x);
    x.error = true;
    x.state = 4;
    x.sent = false;
    netXHRFire(o, 'readystatechange');
    if (x.gen !== g || !x.async) return;
    netXHRFire(o, 'error', true);
    if (x.gen === g) netXHRFire(o, 'loadend', true);
    return;
  }
  x.status = resp.status | 0;
  x.statusText = str(resp.statusText || '');
  x.respURL = str(resp.url || '');
  var hs = resp.headers || [];
  x.respHeaders = [];
  for (var i = 0; i < hs.length; i++) x.respHeaders.push([str(hs[i][0]).toLowerCase(), str(hs[i][1])]);
  x.body = resp.body ? new Uint8Array(resp.body) : new Uint8Array(0);
  x.text = null;
  x.value = undefined;
  x.state = 2;
  netXHRFire(o, 'readystatechange');
  if (x.gen !== g) return;
  x.state = 3;
  netXHRFire(o, 'readystatechange');
  if (x.gen !== g) return;
  if (x.async) netXHRFire(o, 'progress', true);
  if (x.gen !== g) return;
  x.state = 4;
  x.sent = false;
  netXHRFire(o, 'readystatechange');
  if (x.gen !== g) return;
  netXHRFire(o, 'load', true);
  if (x.gen === g) netXHRFire(o, 'loadend', true);
}

function netXHRTimeout(o, gen) {
  var x = o[XHR];
  if (gen !== x.gen) return;
  netXHRCancel(x);
  netXHRReset(x);
  x.error = true;
  x.state = 4;
  x.sent = false;
  var g = x.gen;
  netXHRFire(o, 'readystatechange');
  if (x.gen !== g) return;
  netXHRFire(o, 'timeout', true);
  if (x.gen === g) netXHRFire(o, 'loadend', true);
}

function netXHRHeader(x, name) {
  var vals = [];
  for (var i = 0; i < x.respHeaders.length; i++) if (x.respHeaders[i][0] === name) vals.push(x.respHeaders[i][1]);
  return vals.length ? vals.join(', ') : null;
}

function netXHRText(x) {
  if (x.text === null) {
    var ct = x.mime || netXHRHeader(x, 'content-type') || '';
    x.text = x.body ? netDecode(x.body, netCharsetOf(ct)) : '';
  }
  return x.text;
}

function netXHRDocument(x) {
  if (x.state !== 4 || x.error) return null;
  var ct = (x.mime || netXHRHeader(x, 'content-type') || '').toLowerCase();
  if (x.responseType === '' && ct.indexOf('html') < 0 && ct.indexOf('xml') < 0) return null;
  if (x.value === undefined) {
    try {
      x.value = new DOMParser().parseFromString(netXHRText(x), ct.indexOf('xml') >= 0 && ct.indexOf('html') < 0 ? 'text/xml' : 'text/html');
    } catch (e) {
      x.value = null;
    }
  }
  return x.value;
}

mix(XMLHttpRequest.prototype, {
  get readyState() { return netXHR(this).state; },
  get status() { return netXHR(this).status; },
  get statusText() { return netXHR(this).statusText; },
  get responseURL() { return netXHR(this).respURL; },
  get upload() { return netXHR(this).upload; },
  get withCredentials() { return netXHR(this).withCredentials; },
  set withCredentials(v) {
    var x = netXHR(this);
    if (x.state !== 0 && x.state !== 1 || x.sent) throw domErr('InvalidStateError', "Failed to set the 'withCredentials' property on 'XMLHttpRequest': The value may only be set if the object's state is UNSENT or OPENED.");
    x.withCredentials = !!v;
  },
  get timeout() { return netXHR(this).timeout; },
  set timeout(v) { netXHR(this).timeout = Math.max(0, +v || 0); },
  get responseType() { return netXHR(this).responseType; },
  set responseType(v) {
    var x = netXHR(this);
    var t = str(v);
    if (['', 'text', 'json', 'arraybuffer', 'blob', 'document'].indexOf(t) < 0) return;
    if (x.state === 3 || x.state === 4) throw domErr('InvalidStateError', "Failed to set the 'responseType' property on 'XMLHttpRequest': The response type cannot be set if the object's state is LOADING or DONE.");
    x.responseType = t;
  },
  get responseText() {
    var x = netXHR(this);
    if (x.responseType !== '' && x.responseType !== 'text') {
      throw domErr('InvalidStateError', "Failed to read the 'responseText' property from 'XMLHttpRequest': The value is only accessible if the object's 'responseType' is '' or 'text' (was '" + x.responseType + "').");
    }
    if (x.state < 3 || x.error) return '';
    return netXHRText(x);
  },
  get responseXML() {
    var x = netXHR(this);
    if (x.responseType !== '' && x.responseType !== 'document') {
      throw domErr('InvalidStateError', "Failed to read the 'responseXML' property from 'XMLHttpRequest': The value is only accessible if the object's 'responseType' is '' or 'document'.");
    }
    return netXHRDocument(x);
  },
  get response() {
    var x = netXHR(this);
    switch (x.responseType) {
      case '':
      case 'text':
        return x.state < 3 || x.error ? '' : netXHRText(x);
      case 'json':
        if (x.state !== 4 || x.error) return null;
        if (x.value === undefined) {
          try { x.value = JSON.parse(netXHRText(x)); } catch (e) { x.value = null; }
        }
        return x.value;
      case 'arraybuffer':
        if (x.state !== 4 || x.error) return null;
        if (x.value === undefined) x.value = netCopyBuffer(x.body);
        return x.value;
      case 'blob':
        if (x.state !== 4 || x.error) return null;
        if (x.value === undefined) x.value = netMakeBlob(new Uint8Array(x.body), x.mime || netXHRHeader(x, 'content-type') || '');
        return x.value;
      case 'document':
        return netXHRDocument(x);
    }
    return null;
  },
  open: function (method, url, async) {
    var x = netXHR(this);
    if (arguments.length < 2) throw new TypeError("Failed to execute 'open' on 'XMLHttpRequest': 2 arguments required, but only " + arguments.length + ' present.');
    var m = netMethod(method, 'open');
    var abs = resolveURL(mainDoc, str(url));
    if (!abs) throw domErr('SyntaxError', "Failed to execute 'open' on 'XMLHttpRequest': Invalid URL");
    netXHRCancel(x);
    netXHRReset(x);
    x.method = m;
    x.url = abs;
    x.async = arguments.length < 3 || async === undefined ? true : !!async;
    x.headers = [];
    x.sent = false;
    if (x.state !== 1) {
      x.state = 1;
      netXHRFire(this, 'readystatechange');
    }
  },
  setRequestHeader: function (name, value) {
    var x = netXHR(this);
    if (x.state !== 1 || x.sent) throw domErr('InvalidStateError', "Failed to execute 'setRequestHeader' on 'XMLHttpRequest': The object's state must be OPENED.");
    var n = netHeaderName(name, 'setRequestHeader'), v = netHeaderValue(value, 'setRequestHeader');
    for (var i = 0; i < x.headers.length; i++) {
      if (x.headers[i][0] === n) {
        x.headers[i][1] += ', ' + v;
        return;
      }
    }
    x.headers.push([n, v]);
  },
  send: function (body) {
    var x = netXHR(this);
    if (x.state !== 1 || x.sent) throw domErr('InvalidStateError', "Failed to execute 'send' on 'XMLHttpRequest': The object's state must be OPENED.");
    var bytes = null;
    if (x.method !== 'GET' && x.method !== 'HEAD' && body !== undefined && body !== null) {
      var ex = netExtractBody(body);
      bytes = ex.bytes;
      var hasType = false;
      for (var i = 0; i < x.headers.length; i++) if (x.headers[i][0] === 'content-type') hasType = true;
      if (ex.type && !hasType) x.headers.push(['content-type', ex.type]);
    }
    x.sent = true;
    var req = {
      method: x.method, url: x.url, headers: x.headers.slice(), body: bytes,
      credentials: x.withCredentials ? 'include' : 'same-origin', mode: 'cors', redirect: 'follow'
    };
    netStats.fetches++;
    if (!x.async) {
      netXHRDone(this, N.fetchSync(req));
      if (x.error) throw domErr('NetworkError', "Failed to execute 'send' on 'XMLHttpRequest': Failed to load '" + x.url + "'.");
      return;
    }
    var self = this;
    var g = x.gen;
    netXHRFire(this, 'loadstart', true);
    if (x.gen !== g) return;
    var id = netNextId++;
    x.id = id;
    netPending[id] = { xhr: this, gen: g };
    if (x.timeout > 0) x.timer = loopAddTimer(function () { netXHRTimeout(self, g); }, x.timeout, null, false);
    var ok = false;
    try { ok = N.fetch(id, req); } catch (e) { ok = false; }
    if (!ok) {
      delete netPending[id];
      loopQueueTask(function () { netXHRDone(self, { err: 'refused' }, g); });
    }
  },
  abort: function () {
    var x = netXHR(this);
    var active = (x.state === 1 && x.sent) || x.state === 2 || x.state === 3;
    netXHRCancel(x);
    if (active) {
      netXHRReset(x);
      x.state = 4;
      x.sent = false;
      var g = x.gen;
      netXHRFire(this, 'readystatechange');
      if (x.gen === g) netXHRFire(this, 'abort', true);
      if (x.gen === g) netXHRFire(this, 'loadend', true);
    }
    if (x.state === 4) {
      x.state = 0;
      netXHRReset(x);
    }
  },
  getResponseHeader: function (name) {
    var x = netXHR(this);
    if (x.state < 2 || x.error) return null;
    return netXHRHeader(x, str(name).toLowerCase());
  },
  getAllResponseHeaders: function () {
    var x = netXHR(this);
    if (x.state < 2 || x.error) return '';
    var view = netHeaderView({ list: x.respHeaders });
    var out = '';
    for (var i = 0; i < view.length; i++) out += view[i][0] + ': ' + view[i][1] + '\r\n';
    return out;
  },
  overrideMimeType: function (mime) {
    var x = netXHR(this);
    if (x.state === 3 || x.state === 4) throw domErr('InvalidStateError', "Failed to execute 'overrideMimeType' on 'XMLHttpRequest': MimeType cannot be overridden when the state is LOADING or DONE.");
    x.mime = str(mime);
  }
});

// ---------------------------------------------------------------- FormData

function netFormValue(value, filename) {
  if (value instanceof Blob) {
    if (value instanceof File && filename === undefined) return value;
    var name = filename !== undefined ? str(filename) : (value instanceof File ? value.name : 'blob');
    return new File([value], name, { type: value.type });
  }
  return str(value);
}

function netFormList(o) {
  var list = o && o[FD];
  if (!list) throw new TypeError('Illegal invocation');
  return list;
}

var FormData = iface('FormData', null, function FormData(form, submitter) {
  if (!(this instanceof FormData)) throw new TypeError("Failed to construct 'FormData': Please use the 'new' operator.");
  var list = [];
  defineProperty(this, FD, { value: list });
  if (form !== undefined && form !== null) {
    var r = unwrap(form);
    if (!r || r.t !== 1 || r.ln !== 'form') throw new TypeError("Failed to construct 'FormData': parameter 1 is not of type 'HTMLFormElement'.");
    var entries = formDataEntries(r, unwrap(submitter));
    for (var i = 0; i < entries.length; i++) list.push([str(entries[i][0]), entries[i][1]]);
  }
});
mix(FormData.prototype, {
  append: function (name, value, filename) { netFormList(this).push([str(name), netFormValue(value, filename)]); },
  delete: function (name) {
    var list = netFormList(this), n = str(name);
    for (var i = list.length - 1; i >= 0; i--) if (list[i][0] === n) list.splice(i, 1);
  },
  get: function (name) {
    var list = netFormList(this), n = str(name);
    for (var i = 0; i < list.length; i++) if (list[i][0] === n) return list[i][1];
    return null;
  },
  getAll: function (name) {
    var n = str(name);
    return netFormList(this).filter(function (p) { return p[0] === n; }).map(function (p) { return p[1]; });
  },
  has: function (name) {
    var n = str(name);
    return netFormList(this).some(function (p) { return p[0] === n; });
  },
  set: function (name, value, filename) {
    var list = netFormList(this), n = str(name), v = netFormValue(value, filename), done = false;
    for (var i = 0; i < list.length; i++) {
      if (list[i][0] !== n) continue;
      if (!done) {
        list[i] = [n, v];
        done = true;
      } else {
        list.splice(i--, 1);
      }
    }
    if (!done) list.push([n, v]);
  },
  forEach: function (callback, thisArg) {
    var list = netFormList(this).slice();
    for (var i = 0; i < list.length; i++) callback.call(thisArg, list[i][1], list[i][0], this);
  },
  entries: function () { return netIterator(netFormList(this).map(function (p) { return [p[0], p[1]]; }), 'FormData Iterator'); },
  keys: function () { return netIterator(netFormList(this).map(function (p) { return p[0]; }), 'FormData Iterator'); },
  values: function () { return netIterator(netFormList(this).map(function (p) { return p[1]; }), 'FormData Iterator'); }
});
defineProperty(FormData.prototype, Symbol.iterator, { value: FormData.prototype.entries, writable: true, configurable: true });

function netMultipartName(s) { return str(s).replace(/\n/g, '%0A').replace(/\r/g, '%0D').replace(/"/g, '%22'); }

function netMultipart(fd) {
  var boundary = '----JukaHubFormBoundary' + netRandomToken();
  var chunks = [];
  var list = fd[FD];
  for (var i = 0; i < list.length; i++) {
    var name = list[i][0], value = list[i][1];
    var head = '--' + boundary + '\r\nContent-Disposition: form-data; name="' + netMultipartName(name) + '"';
    if (value instanceof Blob) {
      head += '; filename="' + netMultipartName(value.name || 'blob') + '"\r\nContent-Type: ' + (value.type || 'application/octet-stream');
      chunks.push(netUTF8(head + '\r\n\r\n'), value[BLOB].data, netUTF8('\r\n'));
    } else {
      chunks.push(netUTF8(head + '\r\n\r\n' + str(value).replace(/\r?\n|\r/g, '\r\n') + '\r\n'));
    }
  }
  chunks.push(netUTF8('--' + boundary + '--\r\n'));
  return { bytes: netConcat(chunks), type: 'multipart/form-data; boundary=' + boundary };
}

// ---------------------------------------------------------------- URLSearchParams

function netPercentDecode(s) {
  if (s.indexOf('%') < 0) return s;
  try {
    return decodeURIComponent(s);
  } catch (e) {
    // Malformed escapes stay as they are; well-formed runs between them still decode.
    return s.replace(/(%[0-9A-Fa-f]{2})+/g, function (run) {
      try { return decodeURIComponent(run); } catch (x) { return run; }
    });
  }
}

function netParseQuery(s) {
  var out = [];
  if (!s) return out;
  var parts = s.split('&');
  for (var i = 0; i < parts.length; i++) {
    var p = parts[i];
    if (p === '') continue;
    var eq = p.indexOf('=');
    var name = eq < 0 ? p : p.slice(0, eq);
    var value = eq < 0 ? '' : p.slice(eq + 1);
    out.push([netPercentDecode(name.replace(/\+/g, ' ')), netPercentDecode(value.replace(/\+/g, ' '))]);
  }
  return out;
}

function netWellFormed(s) {
  var out = '';
  for (var i = 0; i < s.length; i++) {
    var c = s.charCodeAt(i);
    if (c >= 0xD800 && c <= 0xDBFF && i + 1 < s.length && s.charCodeAt(i + 1) >= 0xDC00 && s.charCodeAt(i + 1) <= 0xDFFF) {
      out += s.charAt(i) + s.charAt(i + 1);
      i++;
    } else if (c >= 0xD800 && c <= 0xDFFF) {
      out += '�';
    } else {
      out += s.charAt(i);
    }
  }
  return out;
}

function netFormEncode(s) {
  var enc;
  try { enc = encodeURIComponent(s); } catch (e) { enc = encodeURIComponent(netWellFormed(s)); }
  return enc.replace(/%20/g, '+').replace(/[!'()~]/g, function (c) {
    return '%' + c.charCodeAt(0).toString(16).toUpperCase();
  });
}

function netSerializeQuery(list) {
  var parts = [];
  for (var i = 0; i < list.length; i++) parts.push(netFormEncode(list[i][0]) + '=' + netFormEncode(list[i][1]));
  return parts.join('&');
}

function netUSP(o) {
  var st = o && o[USP];
  if (!st) throw new TypeError('Illegal invocation');
  return st;
}

// A URL's searchParams writes back into the URL on every change.
function netUSPChanged(st) {
  if (!st.url) return;
  var u = st.url[URLI];
  var q = netSerializeQuery(st.list);
  var p = u.p;
  var href = envNoFragment(p.href).split('?')[0] + (q ? '?' + q : '') + p.hash;
  var np = envParseURL(href);
  if (np) u.p = np;
}

var URLSearchParams = iface('URLSearchParams', null, function URLSearchParams(init) {
  if (!(this instanceof URLSearchParams)) throw new TypeError("Failed to construct 'URLSearchParams': Please use the 'new' operator.");
  var st = { list: [], url: null };
  defineProperty(this, USP, { value: st });
  if (init === undefined || init === null) return;
  if (typeof init === 'object' || typeof init === 'function') {
    if (init instanceof URLSearchParams) {
      st.list = init[USP].list.map(function (p) { return [p[0], p[1]]; });
    } else if (typeof init[Symbol.iterator] === 'function') {
      var pairs = Array.from(init);
      for (var i = 0; i < pairs.length; i++) {
        var p = pairs[i];
        if (!p || typeof p !== 'object' || p.length !== 2) {
          throw new TypeError("Failed to construct 'URLSearchParams': The provided value cannot be converted to a sequence.");
        }
        st.list.push([str(p[0]), str(p[1])]);
      }
    } else {
      var keys = Object.keys(init);
      for (var k = 0; k < keys.length; k++) st.list.push([keys[k], str(init[keys[k]])]);
    }
    return;
  }
  var s = str(init);
  if (s.charAt(0) === '?') s = s.slice(1);
  st.list = netParseQuery(s);
});
mix(URLSearchParams.prototype, {
  get size() { return netUSP(this).list.length; },
  append: function (name, value) {
    var st = netUSP(this);
    st.list.push([str(name), str(value)]);
    netUSPChanged(st);
  },
  delete: function (name, value) {
    var st = netUSP(this), n = str(name), v = value === undefined ? undefined : str(value);
    st.list = st.list.filter(function (p) { return p[0] !== n || (v !== undefined && p[1] !== v); });
    netUSPChanged(st);
  },
  get: function (name) {
    var list = netUSP(this).list, n = str(name);
    for (var i = 0; i < list.length; i++) if (list[i][0] === n) return list[i][1];
    return null;
  },
  getAll: function (name) {
    var n = str(name);
    return netUSP(this).list.filter(function (p) { return p[0] === n; }).map(function (p) { return p[1]; });
  },
  has: function (name, value) {
    var n = str(name), v = value === undefined ? undefined : str(value);
    return netUSP(this).list.some(function (p) { return p[0] === n && (v === undefined || p[1] === v); });
  },
  set: function (name, value) {
    var st = netUSP(this), n = str(name), v = str(value), done = false, out = [];
    for (var i = 0; i < st.list.length; i++) {
      if (st.list[i][0] !== n) out.push(st.list[i]);
      else if (!done) {
        out.push([n, v]);
        done = true;
      }
    }
    if (!done) out.push([n, v]);
    st.list = out;
    netUSPChanged(st);
  },
  sort: function () {
    var st = netUSP(this);
    var indexed = st.list.map(function (p, i) { return [p, i]; });
    indexed.sort(function (a, b) {
      if (a[0][0] < b[0][0]) return -1;
      if (a[0][0] > b[0][0]) return 1;
      return a[1] - b[1];
    });
    st.list = indexed.map(function (x) { return x[0]; });
    netUSPChanged(st);
  },
  toString: function () { return netSerializeQuery(netUSP(this).list); },
  forEach: function (callback, thisArg) {
    var list = netUSP(this).list.slice();
    for (var i = 0; i < list.length; i++) callback.call(thisArg, list[i][1], list[i][0], this);
  },
  entries: function () { return netIterator(netUSP(this).list.map(function (p) { return [p[0], p[1]]; }), 'URLSearchParams Iterator'); },
  keys: function () { return netIterator(netUSP(this).list.map(function (p) { return p[0]; }), 'URLSearchParams Iterator'); },
  values: function () { return netIterator(netUSP(this).list.map(function (p) { return p[1]; }), 'URLSearchParams Iterator'); }
});
defineProperty(URLSearchParams.prototype, Symbol.iterator, { value: URLSearchParams.prototype.entries, writable: true, configurable: true });

// ---------------------------------------------------------------- URL

function netParseURL(url, base) {
  var b = '';
  if (base !== undefined) {
    var bp = envParseURL(base instanceof URL ? base.href : str(base));
    if (!bp) return null;
    b = bp.href;
  }
  return envParseURL(url instanceof URL ? url.href : str(url), b);
}

function netURL(o) {
  var st = o && o[URLI];
  if (!st) throw new TypeError('Illegal invocation');
  return st;
}

function netURLSpecial(p) { return /^(https?|wss?|ftp|file):$/.test(p.protocol); }

function netURLAuthority(p) {
  var auth = '';
  if (p.username || p.password) auth = p.username + (p.password ? ':' + p.password : '') + '@';
  return auth;
}

// netURLRebuild re-parses a URL with one component replaced; an invalid result leaves the URL as it was,
// which is how the URL setters fail.
function netURLRebuild(st, parts) {
  var p = st.p;
  var get = function (k) { return parts[k] !== undefined ? parts[k] : p[k]; };
  var protocol = get('protocol');
  var hasHost = netURLSpecial(p) || p.host !== '';
  var href = protocol + (hasHost ? '//' + netURLAuthority({ username: get('username'), password: get('password') }) + get('host') : '') +
    get('pathname') + get('search') + get('hash');
  var np = envParseURL(href);
  if (!np) return;
  st.p = np;
  if (st.params) st.params[USP].list = netParseQuery(np.search.slice(1));
}

var URL = iface('URL', null, function URL(url, base) {
  if (!(this instanceof URL)) throw new TypeError("Failed to construct 'URL': Please use the 'new' operator.");
  if (arguments.length < 1) throw new TypeError("Failed to construct 'URL': 1 argument required, but only 0 present.");
  var p = netParseURL(url, base);
  if (!p) throw new TypeError("Failed to construct 'URL': Invalid URL");
  defineProperty(this, URLI, { value: { p: p, params: null } });
});
mix(URL.prototype, {
  get href() { return netURL(this).p.href; },
  set href(v) {
    var st = netURL(this);
    var p = envParseURL(str(v));
    if (!p) throw new TypeError("Failed to set the 'href' property on 'URL': Invalid URL");
    st.p = p;
    if (st.params) st.params[USP].list = netParseQuery(p.search.slice(1));
  },
  get origin() { return netURL(this).p.origin; },
  get protocol() { return netURL(this).p.protocol; },
  set protocol(v) {
    var s = str(v).replace(/:.*$/, '').toLowerCase();
    if (!/^[a-z][a-z0-9+.-]*$/.test(s)) return;
    netURLRebuild(netURL(this), { protocol: s + ':' });
  },
  get username() { return netURL(this).p.username; },
  set username(v) { netURLRebuild(netURL(this), { username: encodeURIComponent(str(v)) }); },
  get password() { return netURL(this).p.password; },
  set password(v) { netURLRebuild(netURL(this), { password: encodeURIComponent(str(v)) }); },
  get host() { return netURL(this).p.host; },
  set host(v) { netURLRebuild(netURL(this), { host: str(v) }); },
  get hostname() { return netURL(this).p.hostname; },
  set hostname(v) {
    var st = netURL(this);
    netURLRebuild(st, { host: str(v) + (st.p.port ? ':' + st.p.port : '') });
  },
  get port() { return netURL(this).p.port; },
  set port(v) {
    var st = netURL(this);
    var s = str(v).replace(/[^0-9].*$/, '');
    netURLRebuild(st, { host: st.p.hostname + (s ? ':' + s : '') });
  },
  get pathname() { return netURL(this).p.pathname; },
  set pathname(v) {
    var s = str(v);
    var st = netURL(this);
    if (netURLSpecial(st.p) && s.charAt(0) !== '/') s = '/' + s;
    netURLRebuild(st, { pathname: s.replace(/[?#]/g, function (c) { return c === '?' ? '%3F' : '%23'; }) });
  },
  get search() { return netURL(this).p.search; },
  set search(v) {
    var s = str(v);
    if (s.charAt(0) === '?') s = s.slice(1);
    netURLRebuild(netURL(this), { search: s ? '?' + s.replace(/#/g, '%23') : '' });
  },
  get hash() { return netURL(this).p.hash; },
  set hash(v) {
    var s = str(v);
    if (s.charAt(0) === '#') s = s.slice(1);
    netURLRebuild(netURL(this), { hash: s ? '#' + s : '' });
  },
  get searchParams() {
    var st = netURL(this);
    if (!st.params) {
      st.params = new URLSearchParams(st.p.search);
      st.params[USP].url = this;
    }
    return st.params;
  },
  toString: function () { return netURL(this).p.href; },
  toJSON: function () { return netURL(this).p.href; }
});
URL.canParse = function (url, base) { return !!netParseURL(url, base); };
URL.parse = function (url, base) {
  try { return new URL(url, base); } catch (e) { return null; }
};
URL.createObjectURL = function (obj) {
  if (!(obj instanceof Blob)) throw new TypeError("Failed to execute 'createObjectURL' on 'URL': Overload resolution failed.");
  var b = new Uint8Array(N.randomBytes(16));
  var hex = '';
  for (var i = 0; i < 16; i++) {
    hex += (b[i] < 16 ? '0' : '') + b[i].toString(16);
    if (i === 3 || i === 5 || i === 7 || i === 9) hex += '-';
  }
  var origin = envLoc && envLoc.origin !== 'null' ? envLoc.origin : 'null';
  var u = 'blob:' + origin + '/' + hex;
  netBlobURLs[u] = obj;
  return u;
};
URL.revokeObjectURL = function (url) { delete netBlobURLs[str(url)]; };
defineGlobal('webkitURL', URL);

// ---------------------------------------------------------------- TextEncoder, TextDecoder

var TextEncoder = iface('TextEncoder', null, function TextEncoder() {
  if (!(this instanceof TextEncoder)) throw new TypeError("Failed to construct 'TextEncoder': Please use the 'new' operator.");
});
mix(TextEncoder.prototype, {
  get encoding() { return 'utf-8'; },
  encode: function (input) { return new Uint8Array(N.utf8Encode(input === undefined ? '' : str(input))); },
  encodeInto: function (source, destination) {
    var s = str(source);
    var dst = netBufferBytes(destination);
    if (!dst) throw new TypeError("Failed to execute 'encodeInto' on 'TextEncoder': parameter 2 is not of type 'Uint8Array'.");
    var all = netUTF8(s);
    if (all.length <= dst.length) {
      dst.set(all);
      return { read: s.length, written: all.length };
    }
    // The longest prefix of whole characters that fits.
    var read = 0, written = 0;
    while (read < s.length) {
      var c = s.charCodeAt(read), units = 1, size;
      if (c < 0x80) size = 1;
      else if (c < 0x800) size = 2;
      else if (c >= 0xD800 && c <= 0xDBFF && read + 1 < s.length && s.charCodeAt(read + 1) >= 0xDC00 && s.charCodeAt(read + 1) <= 0xDFFF) {
        size = 4;
        units = 2;
      } else size = 3;
      if (written + size > dst.length) break;
      written += size;
      read += units;
    }
    var bytes = netUTF8(s.slice(0, read));
    dst.set(bytes.subarray(0, Math.min(bytes.length, dst.length)));
    return { read: read, written: Math.min(bytes.length, dst.length) };
  }
});

var NET_ENCODINGS = {
  'utf-8': 'utf-8', 'utf8': 'utf-8', 'unicode-1-1-utf-8': 'utf-8', 'unicode11utf8': 'utf-8', 'unicode20utf8': 'utf-8', 'x-unicode20utf8': 'utf-8',
  'utf-16le': 'utf-16le', 'utf-16': 'utf-16le', 'ucs-2': 'utf-16le', 'unicode': 'utf-16le', 'csunicode': 'utf-16le', 'iso-10646-ucs-2': 'utf-16le', 'unicodefeff': 'utf-16le',
  'utf-16be': 'utf-16be', 'unicodefffe': 'utf-16be',
  'windows-1252': 'windows-1252', 'latin1': 'windows-1252', 'iso-8859-1': 'windows-1252', 'iso8859-1': 'windows-1252',
  'iso_8859-1': 'windows-1252', 'l1': 'windows-1252', 'cp1252': 'windows-1252', 'x-cp1252': 'windows-1252', 'ascii': 'windows-1252',
  'us-ascii': 'windows-1252', 'ansi_x3.4-1968': 'windows-1252', 'iso-ir-100': 'windows-1252', 'cp819': 'windows-1252',
  'ibm819': 'windows-1252', 'csisolatin1': 'windows-1252'
};

// The bytes of a trailing UTF-8 sequence that a streaming decode must hold for the next chunk.
function netUTF8Tail(bytes) {
  var n = bytes.length;
  for (var back = 1; back <= 3 && back <= n; back++) {
    var b = bytes[n - back];
    if ((b & 0xC0) === 0x80) continue;
    var need = b >= 0xF0 ? 4 : b >= 0xE0 ? 3 : b >= 0xC0 ? 2 : 1;
    return need > back ? back : 0;
  }
  return 0;
}

var TextDecoder = iface('TextDecoder', null, function TextDecoder(label, options) {
  if (!(this instanceof TextDecoder)) throw new TypeError("Failed to construct 'TextDecoder': Please use the 'new' operator.");
  var l = label === undefined ? 'utf-8' : stripWS(str(label)).toLowerCase();
  var enc = NET_ENCODINGS[l];
  if (!enc) throw new RangeError("Failed to construct 'TextDecoder': The encoding label provided ('" + str(label) + "') is invalid.");
  defineProperty(this, TD, {
    value: { enc: enc, fatal: !!(options && options.fatal), ignoreBOM: !!(options && options.ignoreBOM), held: null, started: false }
  });
});
mix(TextDecoder.prototype, {
  get encoding() { return this[TD].enc; },
  get fatal() { return this[TD].fatal; },
  get ignoreBOM() { return this[TD].ignoreBOM; },
  decode: function (input, options) {
    var st = this[TD];
    var bytes = input === undefined || input === null ? new Uint8Array(0) : netBufferBytes(input);
    if (!bytes) throw new TypeError("Failed to execute 'decode' on 'TextDecoder': The provided value is not of type '(ArrayBuffer or ArrayBufferView)'.");
    var stream = !!(options && options.stream);
    if (st.held) {
      bytes = netConcat([st.held, bytes]);
      st.held = null;
    }
    if (stream && st.enc === 'utf-8') {
      var tail = netUTF8Tail(bytes);
      if (tail) {
        st.held = bytes.slice(bytes.length - tail);
        bytes = bytes.subarray(0, bytes.length - tail);
      }
    } else if (stream && st.enc !== 'windows-1252' && bytes.length % 2 === 1) {
      st.held = bytes.slice(bytes.length - 1);
      bytes = bytes.subarray(0, bytes.length - 1);
    }
    var out = N.utf8Decode(bytes, st.enc, st.ignoreBOM || st.started, st.fatal);
    st.started = stream;
    if (!stream) st.held = null;
    if (out === null || out === undefined) throw new TypeError("Failed to execute 'decode' on 'TextDecoder': The encoded data was not valid.");
    return str(out);
  }
});

// ---------------------------------------------------------------- WebSocket, EventSource (never connect)

var WebSocket = iface('WebSocket', EventTarget, function WebSocket(url) {
  if (!(this instanceof WebSocket)) throw new TypeError("Failed to construct 'WebSocket': Please use the 'new' operator.");
  var abs = resolveURL(mainDoc, str(url).replace(/^http/i, 'ws'));
  if (!abs || !/^wss?:/i.test(abs)) throw domErr('SyntaxError', "Failed to construct 'WebSocket': The URL '" + str(url) + "' is invalid.");
  var st = { state: 0, url: abs, binaryType: 'blob' };
  defineProperty(this, WS, { value: st });
  var self = this;
  // Live sockets are out of reach for a page drawn once; fail the way a blocked connection does.
  loopQueueTask(function () {
    if (st.state === 3) return;
    st.state = 3;
    fireEvent(self, 'error', {}, Event);
    fireEvent(self, 'close', { code: 1006, reason: '', wasClean: false }, CloseEvent);
  });
});
var WS_STATES = { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 };
constants(WebSocket, WS_STATES);
constants(WebSocket.prototype, WS_STATES);
mix(WebSocket.prototype, {
  get readyState() { return this[WS].state; },
  get url() { return this[WS].url; },
  get protocol() { return ''; },
  get extensions() { return ''; },
  get bufferedAmount() { return 0; },
  get binaryType() { return this[WS].binaryType; },
  set binaryType(v) { if (v === 'blob' || v === 'arraybuffer') this[WS].binaryType = v; },
  send: function () {
    if (this[WS].state === 0) throw domErr('InvalidStateError', "Failed to execute 'send' on 'WebSocket': Still in CONNECTING state.");
  },
  close: function () { this[WS].state = 3; }
});
envOnProps(WebSocket.prototype, ['open', 'message', 'error', 'close']);

var EventSource = iface('EventSource', EventTarget, function EventSource(url, init) {
  if (!(this instanceof EventSource)) throw new TypeError("Failed to construct 'EventSource': Please use the 'new' operator.");
  var abs = resolveURL(mainDoc, str(url));
  if (!abs) throw domErr('SyntaxError', "Failed to construct 'EventSource': Cannot open an EventSource to '" + str(url) + "'. The URL is invalid.");
  var st = { state: 0, url: abs, cred: !!(init && init.withCredentials) };
  defineProperty(this, WS, { value: st });
  var self = this;
  loopQueueTask(function () {
    if (st.state === 2) return;
    st.state = 2;
    fireEvent(self, 'error', {}, Event);
  });
});
constants(EventSource, { CONNECTING: 0, OPEN: 1, CLOSED: 2 });
constants(EventSource.prototype, { CONNECTING: 0, OPEN: 1, CLOSED: 2 });
mix(EventSource.prototype, {
  get readyState() { return this[WS].state; },
  get url() { return this[WS].url; },
  get withCredentials() { return this[WS].cred; },
  close: function () { this[WS].state = 2; }
});
envOnProps(EventSource.prototype, ['open', 'message', 'error']);
