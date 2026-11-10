// 65_loop.js — the event loop: task queue, timers, animation frames, idle callbacks, message ports.
//
// Go drives the loop (webdom_runtime.go): it calls __jhHost.runTask(mayAdvance) once per task, delivers network
// completions as tasks of their own, and goja drains promise jobs after every call. Time is virtual:
// N.perfNow() is real time plus whatever the loop skipped, and the loop only skips ahead (N.advance) when Go
// says nothing is on the wire — so a page's timers fire in order and quickly, but never before a response that
// is about to arrive.
//
// Every source of work is capped, and the caps are per CHAIN as well as per page: a timer set from inside a
// timer callback belongs to the same chain as that callback, an animation frame requested from inside a frame
// callback to the same frame chain. A carousel interval, a poll for a script the ad filter blocked, or an
// endless requestAnimationFrame loop therefore wears out its own chain, not the budget the page's real work
// needs — the fade-in a load handler starts still runs. The budgets start again at `load` (loopResetBudgets),
// because that is where most pages begin the work the reader is waiting for.
//
// Only what a reader would see within a few seconds is fast-forwarded: a timer that asked for more than
// LOOP_LONG_DELAY is never skipped to (a "session expired" swap at 45 s, a redirect to the next article at
// 20 s, a newsletter overlay at 15 s are not the page), and each phase skips at most LOOP_HORIZON of virtual
// time.

var LOOP_HORIZON = 15000;         // ms of virtual time the loop may skip per phase (before load, after load)
var LOOP_LONG_DELAY = 10000;      // a timer that asked for more than this is never fast-forwarded to
var LOOP_MAX_TIMER_RUNS = 3000;   // timer, frame and idle callbacks per phase
var LOOP_MAX_REPEATS = 100;       // runs of one timer chain (an interval, or a timeout that re-arms itself)
var LOOP_MAX_FRAMES = 60;         // frames of one requestAnimationFrame chain
var LOOP_MAX_FRAMES_TOTAL = 600;  // frames per phase, whatever the chains
var LOOP_FRAME_MS = 16;

var loopQueue = [];              // macrotasks (functions), FIFO
var loopQueueHead = 0;
var loopHeap = [];               // timers: a binary min-heap on (due, seq)
var loopTimers = objCreate(null); // id → live timer
var loopNextId = 1;
var loopSeq = 0;
var loopNesting = 0;
var loopSkipped = 0;
var loopTimerRuns = 0;            // this phase
var loopTimerRunsTotal = 0;       // the whole page, for the stats
var loopFrames = 0;
var loopFrameTimer = null;
var loopFrameCallbacks = [];
var loopNextFrameId = 1;
var loopChain = null;             // the timer chain whose callback is running
var loopFrameChain = null;        // the frame chain whose callback is running

function loopNow() { return +N.perfNow(); }

function loopQueueTask(fn) { loopQueue.push(fn); }

// The DOM core's "queue a task" (details toggle, dialog close, canvas.toBlob …) is a real macrotask: on a
// promise job it would run before the current task's own follow-up work, and an interrupt would drop it.
hooks.queueTask = loopQueueTask;

function loopHeapLess(a, b) { return a.due < b.due || (a.due === b.due && a.seq < b.seq); }

function loopHeapPush(t) {
  var h = loopHeap;
  var i = h.length;
  h.push(t);
  while (i > 0) {
    var p = (i - 1) >> 1;
    if (!loopHeapLess(h[i], h[p])) break;
    var x = h[i]; h[i] = h[p]; h[p] = x;
    i = p;
  }
}

function loopHeapPop() {
  var h = loopHeap;
  var top = h[0];
  var last = h.pop();
  if (h.length) {
    h[0] = last;
    var i = 0;
    for (;;) {
      var l = 2 * i + 1, r = l + 1, m = i;
      if (l < h.length && loopHeapLess(h[l], h[m])) m = l;
      if (r < h.length && loopHeapLess(h[r], h[m])) m = r;
      if (m === i) break;
      var x = h[i]; h[i] = h[m]; h[m] = x;
      i = m;
    }
  }
  return top;
}

function loopPeek() {
  while (loopHeap.length && loopHeap[0].dead) loopHeapPop();
  return loopHeap.length ? loopHeap[0] : null;
}

function loopAddTimer(handler, timeout, args, repeat) {
  timeout = +timeout;
  if (!(timeout >= 0)) timeout = 0;
  if (timeout > 2147483647) timeout = 2147483647;
  // The HTML standard's clamp: deeply nested timers wait at least 4 ms.
  if (loopNesting > 5 && timeout < 4) timeout = 4;
  var t = {
    id: loopNextId++, due: loopNow() + timeout, seq: ++loopSeq, fn: handler, args: args,
    every: repeat ? timeout : -1, delay: timeout, nest: loopNesting, dead: false, kind: 0,
    chain: loopChain || { runs: 0 }
  };
  loopTimers[t.id] = t;
  loopHeapPush(t);
  return t.id;
}

function loopClear(id) {
  var t = loopTimers[id];
  if (t) {
    t.dead = true;
    delete loopTimers[id];
  }
}

function loopRunString(code) {
  var res = N.runScript(str(code), 'timer');
  if (scrIsError(res)) scrReportResult(res, 'timer', null);
}

function loopRetire(t) {
  t.dead = true;
  delete loopTimers[t.id];
}

function loopRunTimer(t) {
  var chain = t.chain;
  if (chain.runs >= LOOP_MAX_REPEATS) {
    // The chain has had its share: this timer (and anything it would have re-armed) is dropped.
    loopRetire(t);
    return;
  }
  chain.runs++;
  loopTimerRuns++;
  loopTimerRunsTotal++;
  if (t.every < 0) loopRetire(t);
  var prev = loopNesting, prevChain = loopChain;
  loopNesting = t.nest + 1;
  loopChain = chain;
  try {
    if (typeof t.fn === 'function') t.fn.apply(global, t.args || []);
    else loopRunString(t.fn);
  } catch (e) {
    reportException(e);
  } finally {
    loopNesting = prev;
    loopChain = prevChain;
  }
  if (t.every >= 0 && !t.dead) {
    if (chain.runs < LOOP_MAX_REPEATS) {
      t.due = loopNow() + t.every;
      t.seq = ++loopSeq;
      loopHeapPush(t);
    } else {
      loopRetire(t);
    }
  }
}

function loopRequestFrame(fn) {
  var id = loopNextFrameId++;
  loopFrameCallbacks.push({ id: id, fn: fn, chain: loopFrameChain || { frames: 0 } });
  loopScheduleFrame();
  return id;
}

function loopScheduleFrame() {
  if (loopFrameTimer || loopFrames >= LOOP_MAX_FRAMES_TOTAL || !loopFrameCallbacks.length) return;
  loopFrameTimer = { id: 0, due: loopNow() + LOOP_FRAME_MS, seq: ++loopSeq, dead: false, kind: 1, delay: LOOP_FRAME_MS };
  loopHeapPush(loopFrameTimer);
}

function loopCancelFrame(id) {
  for (var i = 0; i < loopFrameCallbacks.length; i++) {
    if (loopFrameCallbacks[i].id === id) {
      loopFrameCallbacks.splice(i, 1);
      return;
    }
  }
}

function loopRunFrame() {
  loopFrameTimer = null;
  loopFrames++;
  loopTimerRuns++;
  loopTimerRunsTotal++;
  var list = loopFrameCallbacks;
  loopFrameCallbacks = [];
  var ts = loopNow();
  // A chain counts one frame however many of its callbacks run in it.
  var counted = [];
  for (var i = 0; i < list.length; i++) {
    var c = list[i].chain;
    if (counted.indexOf(c) < 0) {
      counted.push(c);
      c.frames++;
    }
  }
  var prevChain = loopFrameChain;
  for (var j = 0; j < list.length; j++) {
    var entry = list[j];
    if (entry.chain.frames > LOOP_MAX_FRAMES) continue;
    loopFrameChain = entry.chain;
    try { entry.fn.call(global, ts); } catch (e) { reportException(e); }
  }
  loopFrameChain = prevChain;
  loopScheduleFrame();
}

// loopResetBudgets starts the per-phase budgets again (called as `load` fires). Chains keep their counts: a
// poll or an animation that wore out its chain before load stays stopped.
function loopResetBudgets() {
  loopSkipped = 0;
  loopTimerRuns = 0;
  loopFrames = 0;
  loopScheduleFrame();
}

// runTask runs ONE macrotask and reports what is left: -2 a task ran; -1 nothing is runnable (no task, no timer
// the loop may still skip to); n ≥ 0 nothing is runnable now and the next timer is due in n ms (Go waits for the
// network that long at most).
function loopRunTask(mayAdvance) {
  if (loopQueueHead < loopQueue.length) {
    var fn = loopQueue[loopQueueHead];
    loopQueue[loopQueueHead++] = undefined;
    if (loopQueueHead >= 1024 && loopQueueHead * 2 >= loopQueue.length) {
      loopQueue = loopQueue.slice(loopQueueHead);
      loopQueueHead = 0;
    }
    try { fn(); } catch (e) { reportException(e); }
    return -2;
  }
  var t = loopPeek();
  if (!t || loopTimerRuns >= LOOP_MAX_TIMER_RUNS) return -1;
  var now = loopNow();
  if (t.due > now) {
    if (!mayAdvance) return Math.max(0, Math.ceil(t.due - now));
    var skip = t.due - now;
    if (t.delay > LOOP_LONG_DELAY || loopSkipped + skip > LOOP_HORIZON) return -1;
    loopSkipped += skip;
    N.advance(skip);
  }
  loopHeapPop();
  if (t.kind === 1) loopRunFrame();
  else loopRunTimer(t);
  return -2;
}

function loopHasWork() {
  return loopQueueHead < loopQueue.length || loopPeek() !== null;
}

// ---------------------------------------------------------------- timer API

function loopTimerArgs(args) {
  return args.length > 2 ? arraySlice.call(args, 2) : null;
}

defineGlobal('setTimeout', function setTimeout(handler, timeout) {
  return loopAddTimer(handler, timeout, loopTimerArgs(arguments), false);
});
defineGlobal('setInterval', function setInterval(handler, timeout) {
  return loopAddTimer(handler, timeout, loopTimerArgs(arguments), true);
});
defineGlobal('clearTimeout', function clearTimeout(id) { loopClear(id); });
defineGlobal('clearInterval', function clearInterval(id) { loopClear(id); });
defineGlobal('requestAnimationFrame', function requestAnimationFrame(callback) {
  if (typeof callback !== 'function') {
    throw new TypeError("Failed to execute 'requestAnimationFrame' on 'Window': The callback provided as parameter 1 is not a function.");
  }
  return loopRequestFrame(callback);
});
defineGlobal('cancelAnimationFrame', function cancelAnimationFrame(id) { loopCancelFrame(id); });
defineGlobal('requestIdleCallback', function requestIdleCallback(callback, options) {
  if (typeof callback !== 'function') {
    throw new TypeError("Failed to execute 'requestIdleCallback' on 'Window': The callback provided as parameter 1 is not a function.");
  }
  var start = loopNow();
  return loopAddTimer(function () {
    callback.call(global, {
      didTimeout: false,
      timeRemaining: function () { return Math.max(0, 50 - (loopNow() - start)); }
    });
  }, 1, null, false);
});
defineGlobal('cancelIdleCallback', function cancelIdleCallback(id) { loopClear(id); });
defineGlobal('queueMicrotask', function queueMicrotask(callback) {
  if (typeof callback !== 'function') {
    throw new TypeError("Failed to execute 'queueMicrotask' on 'Window': The callback provided as parameter 1 is not a function.");
  }
  Promise.resolve().then(function () {
    try { callback(); } catch (e) { reportException(e); }
  });
});

// setImmediate is not a browser API, but bundles probe for it before MessageChannel; leaving it undefined
// keeps them on the path browsers take.

// ---------------------------------------------------------------- on<event> properties for env objects

var ENV_HANDLERS = Symbol('jukahub.envHandlers');

// envOnProp defines an `on<type>` attribute on proto: setting a function registers it (once) as a listener
// that calls whatever handler is current, the way event handler IDL attributes behave. onSet runs after a
// set (MessagePort starts itself when onmessage is assigned).
function envOnProp(proto, type, onSet) {
  defineProperty(proto, 'on' + type, {
    configurable: true,
    enumerable: true,
    get: function () {
      var h = this[ENV_HANDLERS];
      return h && h[type] !== undefined ? h[type] : null;
    },
    set: function (v) {
      var h = this[ENV_HANDLERS];
      if (!h) {
        h = objCreate(null);
        defineProperty(this, ENV_HANDLERS, { value: h, configurable: true });
      }
      var fresh = !(type in h);
      h[type] = typeof v === 'function' || (v && typeof v === 'object') ? v : null;
      if (fresh) {
        var self = this;
        EventTarget.prototype.addEventListener.call(this, type, function (e) {
          var fn = self[ENV_HANDLERS][type];
          if (typeof fn !== 'function') return;
          var ret = fn.call(self, e);
          if (ret === false) e.preventDefault();
        });
      }
      if (onSet) onSet.call(this);
    }
  });
}

function envOnProps(proto, types) {
  for (var i = 0; i < types.length; i++) envOnProp(proto, types[i]);
}

// envTarget makes a fresh object of an EventTarget subclass (the subclass constructors page scripts see may
// be "Illegal constructor").
function envTarget(proto) {
  return objCreate(proto);
}

// ---------------------------------------------------------------- MessageChannel / MessagePort / BroadcastChannel

var MP = Symbol('jukahub.port');

var MessagePort = iface('MessagePort', EventTarget);
mix(MessagePort.prototype, {
  postMessage: function (message) {
    var st = this[MP];
    if (!st) throw new TypeError('Illegal invocation');
    if (st.closed || !st.other) return;
    var data = envStructuredClone(message);
    var other = st.other;
    other.queue.push(data);
    if (other.started) loopFlushPort(other);
  },
  start: function () {
    var st = this[MP];
    if (!st || st.started) return;
    st.started = true;
    loopFlushPort(st);
  },
  close: function () {
    var st = this[MP];
    if (!st) return;
    st.closed = true;
    if (st.other) st.other.closed = true;
  }
});
envOnProp(MessagePort.prototype, 'message', function () { this.start(); });
envOnProp(MessagePort.prototype, 'messageerror');

function loopFlushPort(st) {
  while (st.queue.length) loopQueueTask(loopPortDelivery(st, st.queue.shift()));
}

function loopPortDelivery(st, data) {
  return function () {
    if (st.closed) return;
    fireEvent(st.port, 'message', { data: data, origin: '', lastEventId: '', ports: [] }, MessageEvent);
  };
}

function loopNewPort() {
  var port = envTarget(MessagePort.prototype);
  var st = { port: port, other: null, queue: [], started: false, closed: false };
  defineProperty(port, MP, { value: st });
  return port;
}

var MessageChannel = iface('MessageChannel', null, function MessageChannel() {
  if (!(this instanceof MessageChannel)) throw new TypeError("Failed to construct 'MessageChannel': Please use the 'new' operator.");
  var a = loopNewPort(), b = loopNewPort();
  a[MP].other = b[MP];
  b[MP].other = a[MP];
  defineProperty(this, 'port1', { value: a, enumerable: true });
  defineProperty(this, 'port2', { value: b, enumerable: true });
});

// One document, so a BroadcastChannel never has anyone to talk to; it exists so pages that use it for
// cross-tab sync start up.
var BroadcastChannel = iface('BroadcastChannel', EventTarget, function BroadcastChannel(name) {
  if (!(this instanceof BroadcastChannel)) throw new TypeError("Failed to construct 'BroadcastChannel': Please use the 'new' operator.");
  defineProperty(this, 'name', { value: str(name), enumerable: true });
});
mix(BroadcastChannel.prototype, {
  postMessage: function () {},
  close: function () {}
});
envOnProps(BroadcastChannel.prototype, ['message', 'messageerror']);
