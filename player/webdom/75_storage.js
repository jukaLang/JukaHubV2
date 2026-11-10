// 75_storage.js — localStorage and sessionStorage.
//
// A Storage is a key → string map per origin, loaded from Go (N.storageGet) on first use and written back
// (N.storageSet) once per batch of changes — a microtask after the first change, so a page that calls setItem
// a thousand times in a loop serialises once. Go keeps the maps in memory only: state survives a reload or a
// revisit while the hub runs, and nothing a page stores reaches the card. Property access
// (localStorage.theme = 'dark') works through a Proxy, as in browsers.

var STO = Symbol('jukahub.storage');
var STO_QUOTA = 500000; // characters per origin and kind (Go caps the serialised bytes as well)

var Storage = iface('Storage', null);

function stoLoad(st) {
  if (st.map) return;
  st.map = objCreate(null);
  st.keys = [];
  st.size = 0;
  var raw = '';
  try { raw = N.storageGet(st.kind) || ''; } catch (e) { raw = ''; }
  if (!raw) return;
  try {
    var obj = JSON.parse(str(raw));
    if (!obj || typeof obj !== 'object') return;
    var keys = Object.keys(obj);
    for (var i = 0; i < keys.length; i++) {
      var v = str(obj[keys[i]]);
      st.map[keys[i]] = v;
      st.keys.push(keys[i]);
      st.size += keys[i].length + v.length;
    }
  } catch (e) { /* a damaged store reads as empty */ }
}

function stoState(o) {
  var st = o && o[STO];
  if (!st) throw new TypeError('Illegal invocation');
  stoLoad(st);
  return st;
}

function stoSave(st) {
  if (st.pending) return;
  st.pending = true;
  Promise.resolve().then(function () {
    st.pending = false;
    var obj = {};
    for (var i = 0; i < st.keys.length; i++) obj[st.keys[i]] = st.map[st.keys[i]];
    try { N.storageSet(st.kind, JSON.stringify(obj)); } catch (e) { /* best effort */ }
  });
}

mix(Storage.prototype, {
  get length() { return stoState(this).keys.length; },
  key: function (index) {
    var st = stoState(this);
    var i = Math.trunc(+index) || 0;
    return i >= 0 && i < st.keys.length ? st.keys[i] : null;
  },
  getItem: function (key) {
    var st = stoState(this);
    var k = str(key);
    return k in st.map ? st.map[k] : null;
  },
  setItem: function (key, value) {
    if (arguments.length < 2) throw new TypeError("Failed to execute 'setItem' on 'Storage': 2 arguments required, but only " + arguments.length + ' present.');
    var st = stoState(this);
    var k = str(key), v = str(value);
    var old = k in st.map ? st.map[k] : null;
    var size = st.size - (old === null ? 0 : k.length + old.length) + k.length + v.length;
    if (size > STO_QUOTA) {
      throw domErr('QuotaExceededError', "Failed to execute 'setItem' on 'Storage': Setting the value of '" + k + "' exceeded the quota.");
    }
    if (old === v) return;
    if (old === null) st.keys.push(k);
    st.map[k] = v;
    st.size = size;
    stoSave(st);
  },
  removeItem: function (key) {
    var st = stoState(this);
    var k = str(key);
    if (!(k in st.map)) return;
    st.size -= k.length + st.map[k].length;
    delete st.map[k];
    st.keys.splice(st.keys.indexOf(k), 1);
    stoSave(st);
  },
  clear: function () {
    var st = stoState(this);
    if (!st.keys.length) return;
    st.map = objCreate(null);
    st.keys = [];
    st.size = 0;
    stoSave(st);
  }
});

function stoMake(kind) {
  var target = objCreate(Storage.prototype);
  defineProperty(target, STO, { value: { kind: kind, map: null, keys: null, size: 0, pending: false } });
  var proto = Storage.prototype;
  return new Proxy(target, {
    get: function (t, prop) {
      if (typeof prop === 'symbol' || prop in t) return Reflect.get(t, prop, t);
      var st = stoState(t);
      return prop in st.map ? st.map[prop] : undefined;
    },
    set: function (t, prop, value) {
      if (typeof prop === 'symbol') return Reflect.set(t, prop, value, t);
      proto.setItem.call(t, prop, value);
      return true;
    },
    has: function (t, prop) {
      if (typeof prop === 'symbol' || prop in t) return prop in t;
      return prop in stoState(t).map;
    },
    deleteProperty: function (t, prop) {
      if (typeof prop === 'symbol') return Reflect.deleteProperty(t, prop);
      proto.removeItem.call(t, prop);
      return true;
    },
    ownKeys: function (t) {
      return stoState(t).keys.slice().concat(Reflect.ownKeys(t));
    },
    getOwnPropertyDescriptor: function (t, prop) {
      if (typeof prop === 'string') {
        var st = stoState(t);
        if (prop in st.map) return { value: st.map[prop], writable: true, enumerable: true, configurable: true };
      }
      return Reflect.getOwnPropertyDescriptor(t, prop);
    },
    defineProperty: function (t, prop, desc) {
      if (typeof prop === 'string' && desc && 'value' in desc) {
        proto.setItem.call(t, prop, desc.value);
        return true;
      }
      return Reflect.defineProperty(t, prop, desc);
    }
  });
}

var stoLocal = null;
var stoSession = null;

// stoRecover runs after an interrupted call: the queued save was a promise job goja dropped, and the pending
// flag it would have cleared keeps every later change from being saved.
function stoRecover() {
  var list = [stoLocal, stoSession];
  for (var i = 0; i < list.length; i++) {
    var st = list[i] ? list[i][STO] : null;
    if (st && st.pending) {
      st.pending = false;
      stoSave(st);
    }
  }
}
envReadonly('localStorage', function () { return stoLocal || (stoLocal = stoMake('local')); });
envReadonly('sessionStorage', function () { return stoSession || (stoSession = stoMake('session')); });
