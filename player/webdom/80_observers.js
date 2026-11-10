// 80_observers.js — IntersectionObserver, ResizeObserver, PerformanceObserver, ReportingObserver.
//
// The reader draws the whole page at once, so everything is "in view": an IntersectionObserver reports every
// target it is given as intersecting, once, in a task of its own. That is what makes lazy-loading code — the
// most common reason a page's images and "below the fold" sections never appear without a browser — load
// everything. A ResizeObserver reports each target's size once. Neither re-fires: nothing here ever scrolls
// or resizes.

var IO = Symbol('jukahub.intersection');
var RO = Symbol('jukahub.resize');

function obsElement(target, what) {
  var r = unwrap(target);
  if (!r || r.t !== 1) throw new TypeError("Failed to execute 'observe' on '" + what + "': parameter 1 is not of type 'Element'.");
  return r;
}

function obsRect(target) {
  try {
    if (target && typeof target.getBoundingClientRect === 'function') return target.getBoundingClientRect();
  } catch (e) { /* fall through to a viewport-sized box */ }
  return new DOMRectReadOnly(0, 0, envWidth, envHeight);
}

function obsSchedule(observer, st, deliver) {
  if (st.scheduled) return;
  st.scheduled = true;
  loopQueueTask(function () {
    st.scheduled = false;
    if (!st.pending.length) return;
    var list = st.pending;
    st.pending = [];
    var entries = [];
    for (var i = 0; i < list.length; i++) entries.push(deliver(list[i]));
    try { st.cb.call(observer, entries, observer); } catch (e) { reportException(e); }
  });
}

var IntersectionObserverEntry = iface('IntersectionObserverEntry', null);

function obsIntersectionEntry(target) {
  var rect = obsRect(target);
  var e = objCreate(IntersectionObserverEntry.prototype);
  var values = {
    time: loopNow(), target: target, rootBounds: new DOMRectReadOnly(0, 0, envWidth, envHeight),
    boundingClientRect: rect, intersectionRect: rect, isIntersecting: true, isVisible: true, intersectionRatio: 1
  };
  for (var k in values) defineProperty(e, k, { value: values[k], enumerable: true });
  return e;
}

var IntersectionObserver = iface('IntersectionObserver', null, function IntersectionObserver(callback, options) {
  if (!(this instanceof IntersectionObserver)) throw new TypeError("Failed to construct 'IntersectionObserver': Please use the 'new' operator.");
  if (typeof callback !== 'function') {
    throw new TypeError("Failed to construct 'IntersectionObserver': The callback provided as parameter 1 is not a function.");
  }
  var o = options || {};
  var t = o.threshold === undefined ? [0] : [].concat(o.threshold).map(Number);
  for (var i = 0; i < t.length; i++) {
    if (!(t[i] >= 0 && t[i] <= 1)) throw new RangeError("Failed to construct 'IntersectionObserver': Threshold values must be numbers between 0 and 1");
  }
  t.sort(function (a, b) { return a - b; });
  defineProperty(this, IO, {
    value: {
      cb: callback, targets: [], pending: [], scheduled: false, root: o.root || null,
      rootMargin: o.rootMargin !== undefined ? str(o.rootMargin) : '0px 0px 0px 0px', thresholds: Object.freeze(t)
    }
  });
});
mix(IntersectionObserver.prototype, {
  get root() { return this[IO].root; },
  get rootMargin() { return this[IO].rootMargin; },
  get thresholds() { return this[IO].thresholds; },
  get scrollMargin() { return '0px 0px 0px 0px'; },
  get delay() { return 0; },
  get trackVisibility() { return false; },
  observe: function (target) {
    var st = this[IO];
    obsElement(target, 'IntersectionObserver');
    if (st.targets.indexOf(target) >= 0) return;
    st.targets.push(target);
    st.pending.push(target);
    obsSchedule(this, st, obsIntersectionEntry);
  },
  unobserve: function (target) {
    var st = this[IO];
    st.targets = st.targets.filter(function (t) { return t !== target; });
    st.pending = st.pending.filter(function (t) { return t !== target; });
  },
  disconnect: function () {
    var st = this[IO];
    st.targets = [];
    st.pending = [];
  },
  takeRecords: function () {
    var st = this[IO];
    var list = st.pending;
    st.pending = [];
    return list.map(obsIntersectionEntry);
  }
});

var ResizeObserverSize = iface('ResizeObserverSize', null);
var ResizeObserverEntry = iface('ResizeObserverEntry', null);

function obsSize(w, h) {
  var s = objCreate(ResizeObserverSize.prototype);
  defineProperty(s, 'inlineSize', { value: w, enumerable: true });
  defineProperty(s, 'blockSize', { value: h, enumerable: true });
  return s;
}

function obsResizeEntry(target) {
  var rect = obsRect(target);
  var w = rect.width || 0, h = rect.height || 0;
  var e = objCreate(ResizeObserverEntry.prototype);
  var box = Object.freeze([obsSize(w, h)]);
  var values = {
    target: target, contentRect: new DOMRectReadOnly(0, 0, w, h),
    borderBoxSize: box, contentBoxSize: box, devicePixelContentBoxSize: box
  };
  for (var k in values) defineProperty(e, k, { value: values[k], enumerable: true });
  return e;
}

var ResizeObserver = iface('ResizeObserver', null, function ResizeObserver(callback) {
  if (!(this instanceof ResizeObserver)) throw new TypeError("Failed to construct 'ResizeObserver': Please use the 'new' operator.");
  if (typeof callback !== 'function') {
    throw new TypeError("Failed to construct 'ResizeObserver': The callback provided as parameter 1 is not a function.");
  }
  defineProperty(this, RO, { value: { cb: callback, targets: [], pending: [], scheduled: false } });
});
mix(ResizeObserver.prototype, {
  observe: function (target) {
    var st = this[RO];
    obsElement(target, 'ResizeObserver');
    if (st.targets.indexOf(target) >= 0) return;
    st.targets.push(target);
    st.pending.push(target);
    obsSchedule(this, st, obsResizeEntry);
  },
  unobserve: function (target) {
    var st = this[RO];
    st.targets = st.targets.filter(function (t) { return t !== target; });
    st.pending = st.pending.filter(function (t) { return t !== target; });
  },
  disconnect: function () {
    var st = this[RO];
    st.targets = [];
    st.pending = [];
  }
});

function obsNoopObserver(name) {
  var C = iface(name, null, function (callback) {
    if (!(this instanceof C)) throw new TypeError("Failed to construct '" + name + "': Please use the 'new' operator.");
    if (typeof callback !== 'function') throw new TypeError("Failed to construct '" + name + "': The callback provided as parameter 1 is not a function.");
  });
  mix(C.prototype, {
    observe: function () {},
    disconnect: function () {},
    takeRecords: function () { return []; }
  });
  return C;
}

var PerformanceObserver = obsNoopObserver('PerformanceObserver');
defineProperty(PerformanceObserver, 'supportedEntryTypes', {
  get: function () { return Object.freeze([]); }, enumerable: true, configurable: true
});
obsNoopObserver('ReportingObserver');
