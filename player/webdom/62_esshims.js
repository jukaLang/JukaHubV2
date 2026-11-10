// 62_esshims.js — the few ECMAScript built-ins browsers have and goja does not.
//
// goja is close to current ECMAScript (iterator helpers, Set methods, Promise.withResolvers, toSorted…), but
// it has no WeakRef, no FinalizationRegistry and no Array.fromAsync. Framework builds use the first two
// directly (caches of DOM nodes, signal graphs) and a missing global is a ReferenceError that stops the whole
// bundle, so they are filled in here — each only when the engine lacks it, so a node test run keeps V8's.
//
// The stand-ins are honest about what a page pass can promise: a WeakRef holds its target strongly (the whole
// runtime is dropped when the page's pass ends, so nothing outlives it anyway) and a FinalizationRegistry
// never calls back (nothing is collected while the pass runs).

var esWeakTarget = Symbol('jukahub.weakref');

function esIsObject(v) { return v !== null && (typeof v === 'object' || typeof v === 'function'); }

if (typeof global.WeakRef !== 'function') {
  var EsWeakRef = function WeakRef(target) {
    if (!(this instanceof EsWeakRef)) throw new TypeError("Constructor WeakRef requires 'new'");
    if (!esIsObject(target)) throw new TypeError('WeakRef: target must be an object');
    defineProperty(this, esWeakTarget, { value: target });
  };
  defineProperty(EsWeakRef.prototype, 'deref', {
    value: function deref() { return this[esWeakTarget]; }, writable: true, configurable: true
  });
  defineProperty(EsWeakRef.prototype, Symbol.toStringTag, { value: 'WeakRef', configurable: true });
  defineGlobal('WeakRef', EsWeakRef);
}

if (typeof global.FinalizationRegistry !== 'function') {
  var EsFinalizationRegistry = function FinalizationRegistry(callback) {
    if (!(this instanceof EsFinalizationRegistry)) throw new TypeError("Constructor FinalizationRegistry requires 'new'");
    if (typeof callback !== 'function') throw new TypeError('FinalizationRegistry: cleanup must be callable');
  };
  var esFRMethods = {
    register: function register(target) {
      if (!esIsObject(target)) throw new TypeError('FinalizationRegistry.register: target must be an object');
    },
    unregister: function unregister(token) {
      if (!esIsObject(token)) throw new TypeError('FinalizationRegistry.unregister: token must be an object');
      return false;
    },
    cleanupSome: function cleanupSome() {}
  };
  for (var esFRName in esFRMethods) {
    defineProperty(EsFinalizationRegistry.prototype, esFRName, { value: esFRMethods[esFRName], writable: true, configurable: true });
  }
  defineProperty(EsFinalizationRegistry.prototype, Symbol.toStringTag, { value: 'FinalizationRegistry', configurable: true });
  defineGlobal('FinalizationRegistry', EsFinalizationRegistry);
}

if (typeof Array.fromAsync !== 'function') {
  // Array.fromAsync(items[, mapFn[, thisArg]]): async iterables are walked through their iterator, sync
  // iterables and array-likes have each value awaited in turn, exactly once and in order.
  defineProperty(Array, 'fromAsync', {
    writable: true, configurable: true,
    value: function fromAsync(items, mapFn, thisArg) {
      var Ctor = this;
      return new Promise(function (resolve, reject) {
        if (mapFn !== undefined && typeof mapFn !== 'function') throw new TypeError('Array.fromAsync: mapper is not a function');
        if (items === null || items === undefined) throw new TypeError('Array.fromAsync requires an iterable or array-like');
        var out = typeof Ctor === 'function' && Ctor !== Array ? new Ctor() : [];
        var index = 0;
        var put = function (v) {
          return Promise.resolve(mapFn ? mapFn.call(thisArg, v, index) : v).then(function (mv) {
            out[index++] = mv;
          });
        };
        var finish = function () { out.length = index; resolve(out); };
        var asyncIt = items[Symbol.asyncIterator];
        if (typeof asyncIt === 'function') {
          var it = asyncIt.call(items);
          var stepAsync = function () {
            Promise.resolve(it.next()).then(function (res) {
              if (res.done) return finish();
              return put(res.value).then(stepAsync);
            }).then(null, reject);
          };
          stepAsync();
          return;
        }
        var syncIt = items[Symbol.iterator];
        if (typeof syncIt === 'function') {
          var sit = syncIt.call(items);
          var stepSync = function () {
            var res;
            try { res = sit.next(); } catch (e) { reject(e); return; }
            if (res.done) return finish();
            Promise.resolve(res.value).then(put).then(stepSync, reject);
          };
          stepSync();
          return;
        }
        var len = Math.min(Math.max(Number(items.length) || 0, 0), 4294967295);
        var i = 0;
        var stepLike = function () {
          if (i >= len) return finish();
          var v = items[i++];
          Promise.resolve(v).then(put).then(stepLike, reject);
        };
        stepLike();
      });
    }
  });
}
