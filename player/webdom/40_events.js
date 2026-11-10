// 40_events.js — EventTarget, Event and its subclasses, dispatch, on<event> handlers.
//
// Listeners live on the object they were added to (under the EV symbol), so any object can be a target:
// node wrappers, the global object (window), and the environment's XHR/AbortSignal/MessagePort objects.
// Dispatch builds the path from records, so ancestors that nobody listens on never get a wrapper.

var ES = Symbol('jukahub.event');

function eventState(e) {
  var s = e !== null && e !== undefined ? e[ES] : undefined;
  if (!s) throw new TypeError('Illegal invocation');
  return s;
}

// Extra fields per Event subclass, filled from the init dictionary (field: default).
var EVENT_FIELDS = new Map();

function initEventState(e, type, init, Ctor) {
  init = init === undefined || init === null ? {} : init;
  if (typeof init !== 'object' && typeof init !== 'function') throw new TypeError("The provided value is not of type 'EventInit'.");
  var s = {
    type: str(type), bubbles: !!init.bubbles, cancelable: !!init.cancelable, composed: !!init.composed,
    trusted: false, ts: performanceNow(), target: null, rt: null, cur: null, phase: 0, stop: false, stopImm: false,
    canceled: false, passiveNow: false, dispatching: false, initialized: true, path: null, x: {}
  };
  defineProperty(e, ES, { value: s, configurable: true });
  for (var C = Ctor; C && C !== Event; C = getProto(C)) {
    var fields = EVENT_FIELDS.get(C);
    if (!fields) continue;
    for (var k in fields) {
      if (hasOwn.call(s.x, k)) continue;
      var v = init[k];
      s.x[k] = v === undefined ? fields[k] : v;
    }
  }
  return s;
}

function Event(type, init) {
  if (!new.target) throw new TypeError("Failed to construct 'Event': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
  if (arguments.length === 0) throw new TypeError("Failed to construct 'Event': 1 argument required, but only 0 present.");
  initEventState(this, type, init, new.target);
}
iface('Event', null, Event);
constants(Event, { NONE: 0, CAPTURING_PHASE: 1, AT_TARGET: 2, BUBBLING_PHASE: 3 });
constants(Event.prototype, { NONE: 0, CAPTURING_PHASE: 1, AT_TARGET: 2, BUBBLING_PHASE: 3 });
mix(Event.prototype, {
  get type() { return eventState(this).type; },
  get target() { var s = eventState(this); return s.rt !== null ? s.rt : s.target; },
  get srcElement() { var s = eventState(this); return s.rt !== null ? s.rt : s.target; },
  get currentTarget() { return eventState(this).cur; },
  get eventPhase() { return eventState(this).phase; },
  get bubbles() { return eventState(this).bubbles; },
  get cancelable() { return eventState(this).cancelable; },
  get composed() { return eventState(this).composed; },
  get defaultPrevented() { return eventState(this).canceled; },
  get isTrusted() { return eventState(this).trusted; },
  get timeStamp() { return eventState(this).ts; },
  get returnValue() { return !eventState(this).canceled; },
  set returnValue(v) { var s = eventState(this); if (!v && s.cancelable && !s.passiveNow) s.canceled = true; },
  get cancelBubble() { return eventState(this).stop; },
  set cancelBubble(v) { if (v) eventState(this).stop = true; },
  composedPath: function () {
    var s = eventState(this);
    if (!s.path || !s.dispatching) return [];
    var out = [];
    for (var i = 0; i < s.path.length; i++) {
      var p = s.path[i];
      out.push(p.obj || wrap(p.r));
    }
    return out;
  },
  stopPropagation: function () { eventState(this).stop = true; },
  stopImmediatePropagation: function () { var s = eventState(this); s.stop = true; s.stopImm = true; },
  preventDefault: function () { var s = eventState(this); if (s.cancelable && !s.passiveNow) s.canceled = true; },
  initEvent: function (type, bubbles, cancelable) {
    var s = eventState(this);
    if (s.dispatching) return;
    s.initialized = true;
    s.type = str(type);
    s.bubbles = !!bubbles;
    s.cancelable = !!cancelable;
    s.stop = s.stopImm = s.canceled = false;
    s.target = null;
  }
});

// createEvent: internal construction (no new.target), used by fireEvent and document.createEvent.
function createEvent(Ctor, type, init) {
  var e = objCreate(Ctor.prototype);
  initEventState(e, type, init, Ctor);
  return e;
}

function makeEventClass(name, Parent, fields, extra) {
  var C = function (type, init) {
    if (!new.target) throw new TypeError("Failed to construct '" + name + "': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    if (arguments.length === 0) throw new TypeError("Failed to construct '" + name + "': 1 argument required, but only 0 present.");
    initEventState(this, type, init, new.target);
  };
  iface(name, Parent, C);
  if (fields) {
    EVENT_FIELDS.set(C, fields);
    Object.keys(fields).forEach(function (k) {
      defineProperty(C.prototype, k, {
        get: function () { return eventState(this).x[k]; },
        enumerable: true, configurable: true
      });
    });
  }
  if (extra) mix(C.prototype, extra);
  return C;
}

var modifierFields = { ctrlKey: false, shiftKey: false, altKey: false, metaKey: false };
function withFields(base, more) {
  var out = {};
  var k;
  for (k in base) out[k] = base[k];
  for (k in more) out[k] = more[k];
  return out;
}
function getModifierState(key) {
  var x = eventState(this).x;
  switch (str(key)) {
    case 'Control': return !!x.ctrlKey;
    case 'Shift': return !!x.shiftKey;
    case 'Alt': return !!x.altKey;
    case 'Meta': return !!x.metaKey;
  }
  return false;
}

var CustomEvent = makeEventClass('CustomEvent', Event, { detail: null }, {
  initCustomEvent: function (type, bubbles, cancelable, detail) {
    var s = eventState(this);
    if (s.dispatching) return;
    Event.prototype.initEvent.call(this, type, bubbles, cancelable);
    s.x.detail = detail === undefined ? null : detail;
  }
});
var UIEvent = makeEventClass('UIEvent', Event, { view: null, detail: 0, which: 0 }, {
  initUIEvent: function (type, bubbles, cancelable, view, detail) {
    var s = eventState(this);
    if (s.dispatching) return;
    Event.prototype.initEvent.call(this, type, bubbles, cancelable);
    s.x.view = view === undefined ? null : view;
    s.x.detail = detail | 0;
  }
});
var MouseEvent = makeEventClass('MouseEvent', UIEvent, withFields(modifierFields, {
  screenX: 0, screenY: 0, clientX: 0, clientY: 0, button: 0, buttons: 0, relatedTarget: null,
  movementX: 0, movementY: 0
}), {
  get pageX() { return eventState(this).x.clientX; },
  get pageY() { return eventState(this).x.clientY; },
  get x() { return eventState(this).x.clientX; },
  get y() { return eventState(this).x.clientY; },
  get offsetX() { return eventState(this).x.clientX; },
  get offsetY() { return eventState(this).x.clientY; },
  get layerX() { return eventState(this).x.clientX; },
  get layerY() { return eventState(this).x.clientY; },
  getModifierState: getModifierState,
  initMouseEvent: function (type, bubbles, cancelable, view, detail, screenX, screenY, clientX, clientY, ctrlKey, altKey, shiftKey, metaKey, button, relatedTarget) {
    var s = eventState(this);
    if (s.dispatching) return;
    Event.prototype.initEvent.call(this, type, bubbles, cancelable);
    var x = s.x;
    x.view = view === undefined ? null : view;
    x.detail = detail | 0;
    x.screenX = +screenX || 0; x.screenY = +screenY || 0; x.clientX = +clientX || 0; x.clientY = +clientY || 0;
    x.ctrlKey = !!ctrlKey; x.altKey = !!altKey; x.shiftKey = !!shiftKey; x.metaKey = !!metaKey;
    x.button = button | 0;
    x.relatedTarget = relatedTarget === undefined ? null : relatedTarget;
  }
});
var PointerEvent = makeEventClass('PointerEvent', MouseEvent, {
  pointerId: 0, width: 1, height: 1, pressure: 0, tangentialPressure: 0, tiltX: 0, tiltY: 0, twist: 0,
  altitudeAngle: Math.PI / 2, azimuthAngle: 0, pointerType: '', isPrimary: false
}, {
  getCoalescedEvents: function () { return []; },
  getPredictedEvents: function () { return []; }
});
var WheelEvent = makeEventClass('WheelEvent', MouseEvent, { deltaX: 0, deltaY: 0, deltaZ: 0, deltaMode: 0 });
constants(WheelEvent, { DOM_DELTA_PIXEL: 0, DOM_DELTA_LINE: 1, DOM_DELTA_PAGE: 2 });
var DragEvent = makeEventClass('DragEvent', MouseEvent, { dataTransfer: null });
var KeyboardEvent = makeEventClass('KeyboardEvent', UIEvent, withFields(modifierFields, {
  key: '', code: '', location: 0, repeat: false, isComposing: false, charCode: 0, keyCode: 0
}), {
  getModifierState: getModifierState,
  initKeyboardEvent: function (type, bubbles, cancelable, view, key, location, ctrlKey, altKey, shiftKey, metaKey) {
    var s = eventState(this);
    if (s.dispatching) return;
    Event.prototype.initEvent.call(this, type, bubbles, cancelable);
    var x = s.x;
    x.view = view === undefined ? null : view;
    x.key = key === undefined ? '' : str(key);
    x.location = location >>> 0;
    x.ctrlKey = !!ctrlKey; x.altKey = !!altKey; x.shiftKey = !!shiftKey; x.metaKey = !!metaKey;
  }
});
constants(KeyboardEvent, { DOM_KEY_LOCATION_STANDARD: 0, DOM_KEY_LOCATION_LEFT: 1, DOM_KEY_LOCATION_RIGHT: 2, DOM_KEY_LOCATION_NUMPAD: 3 });
var FocusEvent = makeEventClass('FocusEvent', UIEvent, { relatedTarget: null });
var InputEvent = makeEventClass('InputEvent', UIEvent, { data: null, inputType: '', isComposing: false, dataTransfer: null }, {
  getTargetRanges: function () { return []; }
});
var CompositionEvent = makeEventClass('CompositionEvent', UIEvent, { data: '' });
var TouchEvent = makeEventClass('TouchEvent', UIEvent, withFields(modifierFields, { touches: [], targetTouches: [], changedTouches: [] }));
var ErrorEvent = makeEventClass('ErrorEvent', Event, { message: '', filename: '', lineno: 0, colno: 0, error: undefined });
var MessageEvent = makeEventClass('MessageEvent', Event, { data: null, origin: '', lastEventId: '', source: null, ports: [] }, {
  initMessageEvent: function (type, bubbles, cancelable, data, origin, lastEventId, source, ports) {
    var s = eventState(this);
    if (s.dispatching) return;
    Event.prototype.initEvent.call(this, type, bubbles, cancelable);
    var x = s.x;
    x.data = data === undefined ? null : data;
    x.origin = origin === undefined ? '' : str(origin);
    x.lastEventId = lastEventId === undefined ? '' : str(lastEventId);
    x.source = source === undefined ? null : source;
    x.ports = ports || [];
  }
});
var ProgressEvent = makeEventClass('ProgressEvent', Event, { lengthComputable: false, loaded: 0, total: 0 });
var PopStateEvent = makeEventClass('PopStateEvent', Event, { state: null, hasUAVisualTransition: false });
var HashChangeEvent = makeEventClass('HashChangeEvent', Event, { oldURL: '', newURL: '' });
var PageTransitionEvent = makeEventClass('PageTransitionEvent', Event, { persisted: false });
var StorageEvent = makeEventClass('StorageEvent', Event, { key: null, oldValue: null, newValue: null, url: '', storageArea: null });
var AnimationEvent = makeEventClass('AnimationEvent', Event, { animationName: '', elapsedTime: 0, pseudoElement: '' });
var TransitionEvent = makeEventClass('TransitionEvent', Event, { propertyName: '', elapsedTime: 0, pseudoElement: '' });
var SubmitEvent = makeEventClass('SubmitEvent', Event, { submitter: null });
var PromiseRejectionEvent = makeEventClass('PromiseRejectionEvent', Event, { promise: null, reason: undefined });
var CloseEvent = makeEventClass('CloseEvent', Event, { wasClean: false, code: 0, reason: '' });
var ClipboardEvent = makeEventClass('ClipboardEvent', Event, { clipboardData: null });
var FormDataEvent = makeEventClass('FormDataEvent', Event, { formData: null });
var ToggleEvent = makeEventClass('ToggleEvent', Event, { oldState: '', newState: '' });
var MediaQueryListEvent = makeEventClass('MediaQueryListEvent', Event, { media: '', matches: false });
var SecurityPolicyViolationEvent = makeEventClass('SecurityPolicyViolationEvent', Event, {
  documentURI: '', referrer: '', blockedURI: '', violatedDirective: '', effectiveDirective: '', originalPolicy: '',
  sourceFile: '', sample: '', disposition: 'enforce', statusCode: 0, lineNumber: 0, columnNumber: 0
});
var BeforeUnloadEvent = makeEventClass('BeforeUnloadEvent', Event, null);
defineProperty(BeforeUnloadEvent.prototype, 'returnValue', {
  get: function () { var x = eventState(this).x; return x.rv === undefined ? '' : x.rv; },
  set: function (v) { var s = eventState(this); s.x.rv = str(v); if (s.x.rv) s.canceled = true; },
  enumerable: true, configurable: true
});
var GamepadEvent = makeEventClass('GamepadEvent', Event, { gamepad: null });
var DeviceOrientationEvent = makeEventClass('DeviceOrientationEvent', Event, { alpha: null, beta: null, gamma: null, absolute: false });
var DeviceMotionEvent = makeEventClass('DeviceMotionEvent', Event, { acceleration: null, accelerationIncludingGravity: null, rotationRate: null, interval: 0 });

var CREATE_EVENT_NAMES = {
  event: Event, events: Event, htmlevents: Event, svgevents: Event, customevent: CustomEvent, uievent: UIEvent,
  uievents: UIEvent, mouseevent: MouseEvent, mouseevents: MouseEvent, keyboardevent: KeyboardEvent,
  keyevents: KeyboardEvent, focusevent: FocusEvent, messageevent: MessageEvent, storageevent: StorageEvent,
  textevent: CompositionEvent, compositionevent: CompositionEvent, touchevent: TouchEvent, hashchangeevent: HashChangeEvent,
  beforeunloadevent: BeforeUnloadEvent, dragevent: DragEvent, errorevent: ErrorEvent, popstateevent: PopStateEvent,
  progressevent: ProgressEvent, pagetransitionevent: PageTransitionEvent, wheelevent: WheelEvent,
  devicemotionevent: DeviceMotionEvent, deviceorientationevent: DeviceOrientationEvent
};
defineProperty(Document.prototype, 'createEvent', {
  value: function (name) {
    requiredArgs(arguments, 1, 'createEvent');
    var C = CREATE_EVENT_NAMES[asciiLower(str(name))];
    if (!C) throw domErr('NotSupportedError', "Failed to execute 'createEvent' on 'Document': The provided event type ('" + name + "') is invalid.");
    var e = createEvent(C, '', undefined);
    e[ES].initialized = false;
    return e;
  },
  writable: true, enumerable: true, configurable: true
});

// ---------------------------------------------------------------- listeners

function targetObj(t) { return t === null || t === undefined ? global : t; }

function listenerMap(t, create) {
  var m = t[EV];
  if (!m && create) {
    m = objCreate(null);
    defineProperty(t, EV, { value: m, configurable: true });
  }
  return m;
}
function listenerOptions(options) {
  var o = { capture: false, once: false, passive: undefined, signal: null };
  if (typeof options === 'boolean') o.capture = options;
  else if (options !== null && options !== undefined && typeof options === 'object') {
    o.capture = !!options.capture;
    o.once = !!options.once;
    if (options.passive !== undefined) o.passive = !!options.passive;
    if (options.signal !== undefined) o.signal = options.signal;
  }
  return o;
}
function addListener(t, type, cb, options) {
  if (cb === null || cb === undefined) return;
  if (typeof cb !== 'function' && typeof cb !== 'object') throw new TypeError("Failed to execute 'addEventListener' on 'EventTarget': The callback provided as parameter 2 is not an object.");
  var o = listenerOptions(options);
  if (o.signal && o.signal.aborted) return;
  type = str(type);
  var m = listenerMap(t, true);
  var list = m[type] || (m[type] = []);
  for (var i = 0; i < list.length; i++) if (list[i].cb === cb && list[i].capture === o.capture && !list[i].removed) return;
  var l = { cb: cb, capture: o.capture, once: o.once, passive: o.passive, removed: false };
  list.push(l);
  if (o.signal && typeof o.signal.addEventListener === 'function') {
    o.signal.addEventListener('abort', function () { removeListenerRec(t, type, l); });
  }
}
function removeListenerRec(t, type, l) {
  var m = listenerMap(t, false);
  var list = m && m[type];
  if (!list) return;
  var i = list.indexOf(l);
  if (i >= 0) list.splice(i, 1);
  l.removed = true;
}
function removeListener(t, type, cb, options) {
  var capture = typeof options === 'boolean' ? options : !!(options && options.capture);
  var m = listenerMap(t, false);
  type = str(type);
  var list = m && m[type];
  if (!list) return;
  for (var i = 0; i < list.length; i++) {
    if (list[i].cb === cb && list[i].capture === capture) {
      list[i].removed = true;
      list.splice(i, 1);
      return;
    }
  }
}

mix(EventTarget.prototype, {
  addEventListener: function (type, cb, options) {
    addListener(targetObj(this), type, cb, options);
  },
  removeEventListener: function (type, cb, options) { removeListener(targetObj(this), type, cb, options); },
  dispatchEvent: function (ev) {
    var s = ev !== null && ev !== undefined && typeof ev === 'object' ? ev[ES] : undefined;
    if (!s) throw new TypeError("Failed to execute 'dispatchEvent' on 'EventTarget': parameter 1 is not of type 'Event'.");
    if (s.dispatching || !s.initialized) throw domErr('InvalidStateError', "Failed to execute 'dispatchEvent' on 'EventTarget': The event is already being dispatched.");
    s.trusted = false;
    return dispatchEvent(targetObj(this), ev);
  }
});

// ---------------------------------------------------------------- dispatch

var windowEventsSkipLoad = { load: 1 };

function eventPath(target, s) {
  var path = [];
  var r = unwrap(target);
  if (!r) {
    path.push({ obj: target, r: null, rt: target, win: target === global });
    return path;
  }
  var rt = target;
  var node = r;
  while (node) {
    path.push({ obj: null, r: node, rt: rt });
    if (node.p) {
      node = node.p;
      continue;
    }
    if (node.shadow) {
      if (!s.composed && !composedByDefault(s)) break;
      node = node.host;
      if (node) rt = wrap(node);
      continue;
    }
    if (node.t === 9 && node === mainDoc && !windowEventsSkipLoad[s.type]) {
      path.push({ obj: global, r: null, rt: rt, win: true });
    }
    break;
  }
  return path;
}
// Trusted UI events are composed; script-created ones only when they ask.
function composedByDefault(s) { return s.trusted && s.composed; }

var currentEventStack = [];

function dispatchEvent(target, ev) {
  var s = ev[ES];
  s.dispatching = true;
  s.target = target;
  s.rt = null;
  var path = eventPath(target, s);
  s.path = path;
  var prevEvent = global.event;
  currentEventStack.push(ev);
  try {
    var i;
    for (i = path.length - 1; i > 0 && !s.stop; i--) invokeAt(path[i], ev, s, 1, true);
    if (!s.stop) invokeAt(path[0], ev, s, 2, true);
    if (!s.stop) invokeAt(path[0], ev, s, 2, false);
    if (s.bubbles) for (i = 1; i < path.length && !s.stop; i++) invokeAt(path[i], ev, s, 3, false);
  } finally {
    currentEventStack.pop();
    try { global.event = prevEvent; } catch (e) { /* read-only in some hosts */ }
    s.phase = 0;
    s.cur = null;
    s.rt = null;
    s.dispatching = false;
    s.stop = false;
    s.stopImm = false;
    s.path = null;
  }
  return !s.canceled;
}

// eventsRecover runs after an interrupted call (hostRecover in 90_host.js): goja skipped the finally above
// for every event that was being dispatched, so they would still read as "being dispatched" (dispatchEvent
// of the same object throws InvalidStateError) and window.event would point at the last one for good.
function eventsRecover() {
  for (var i = 0; i < currentEventStack.length; i++) {
    var s = currentEventStack[i] ? currentEventStack[i][ES] : null;
    if (!s) continue;
    s.phase = 0;
    s.cur = null;
    s.rt = null;
    s.dispatching = false;
    s.stop = false;
    s.stopImm = false;
    s.path = null;
  }
  currentEventStack.length = 0;
  try { global.event = undefined; } catch (e) { /* read-only in some hosts */ }
}

function invokeAt(entry, ev, s, phase, captureListeners) {
  var obj = entry.obj;
  var r = entry.r;
  if (!obj && r) {
    obj = r.o;
    if (!obj && r.t === 1 && r.a && !captureListeners && getAttrRaw(r, 'on' + s.type) !== null) obj = wrap(r);
    if (!obj) return;
    entry.obj = obj;
  }
  s.cur = obj;
  s.rt = entry.rt;
  s.phase = phase;
  try { global.event = ev; } catch (e) { /* ignore */ }
  // Event handler (on<type> property or content attribute) — a non-capture listener at its own position.
  if (!captureListeners) invokeHandler(obj, r, entry.win, ev, s);
  if (s.stopImm) return;
  var m = obj[EV];
  if (!m) return;
  var list = m[s.type];
  if (!list || !list.length) return;
  list = list.slice();
  for (var i = 0; i < list.length; i++) {
    var l = list[i];
    if (l.removed || l.capture !== captureListeners) continue;
    if (l.once) removeListenerRec(obj, s.type, l);
    s.passiveNow = l.passive === true;
    try {
      if (typeof l.cb === 'function') l.cb.call(obj, ev);
      else if (l.cb && typeof l.cb.handleEvent === 'function') l.cb.handleEvent(ev);
    } catch (e) {
      reportException(e);
    }
    s.passiveNow = false;
    if (s.stopImm) break;
  }
}

// ---------------------------------------------------------------- event handlers (onclick …)

var GLOBAL_EVENT_HANDLERS = ('abort animationcancel animationend animationiteration animationstart auxclick beforeinput ' +
  'beforematch beforetoggle blur cancel canplay canplaythrough change click close contextlost contextmenu contextrestored ' +
  'copy cuechange cut dblclick drag dragend dragenter dragleave dragover dragstart drop durationchange emptied ended ' +
  'error focus focusin focusout formdata gotpointercapture input invalid keydown keypress keyup load loadeddata ' +
  'loadedmetadata loadstart lostpointercapture mousedown mouseenter mouseleave mousemove mouseout mouseover mouseup ' +
  'mousewheel paste pause play playing pointercancel pointerdown pointerenter pointerleave pointermove pointerout ' +
  'pointerover pointerrawupdate pointerup progress ratechange reset resize scroll scrollend securitypolicyviolation ' +
  'seeked seeking select selectionchange selectstart slotchange stalled submit suspend timeupdate toggle touchcancel ' +
  'touchend touchmove touchstart transitioncancel transitionend transitionrun transitionstart volumechange waiting ' +
  'webkitanimationend webkitanimationiteration webkitanimationstart webkittransitionend wheel').split(' ');
var WINDOW_EVENT_HANDLERS = ('afterprint beforeprint beforeunload hashchange languagechange message messageerror offline ' +
  'online pagehide pagereveal pageshow pageswap popstate rejectionhandled storage unhandledrejection unload').split(' ');
// Body/frameset attributes for these fire on the window.
var BODY_FORWARDED = { blur: 1, error: 1, focus: 1, load: 1, resize: 1, scroll: 1 };
WINDOW_EVENT_HANDLERS.forEach(function (n) { BODY_FORWARDED[n] = 1; });

function handlerSlot(obj, create) {
  var h = obj[EH];
  if (!h && create) {
    h = objCreate(null);
    defineProperty(obj, EH, { value: h, configurable: true });
  }
  return h;
}

// Compiles an inline handler attribute once per source text.
function inlineHandler(r, type, code) {
  var cache = r.ih || (r.ih = objCreate(null));
  var hit = cache[type];
  if (hit && hit.src === code) return hit.fn;
  var fn = null;
  try {
    fn = type === 'error' && r.ln === 'body'
      ? new Function('event', 'source', 'lineno', 'colno', 'error', code)
      : new Function('event', code);
  } catch (e) {
    reportException(e);
  }
  cache[type] = { src: code, fn: fn };
  return fn;
}

function invokeHandler(obj, r, isWindow, ev, s) {
  var type = s.type;
  var h = obj[EH];
  var fn = h && type in h ? h[type] : undefined;
  var thisObj = obj;
  if (fn === undefined) {
    if (r && r.t === 1 && r.a) {
      var code = getAttrRaw(r, 'on' + type);
      if (code !== null && !(r.ns === 0 && (r.ln === 'body' || r.ln === 'frameset') && BODY_FORWARDED[type])) fn = inlineHandler(r, type, code);
    } else if (isWindow && mainDoc) {
      var body = bodyOf(mainDoc);
      if (body && body.a && BODY_FORWARDED[type]) {
        var bcode = getAttrRaw(body, 'on' + type);
        if (bcode !== null) fn = inlineHandler(body, type, bcode);
      }
    }
  }
  if (typeof fn !== 'function') return;
  var rv;
  s.passiveNow = false;
  try {
    if (type === 'error' && isWindow && ev instanceof ErrorEvent) {
      var x = ev[ES].x;
      rv = fn.call(thisObj, x.message, x.filename, x.lineno, x.colno, x.error);
      if (rv === true && s.cancelable) s.canceled = true;
      return;
    }
    rv = fn.call(thisObj, ev);
  } catch (e) {
    reportException(e);
    return;
  }
  if (type === 'beforeunload') {
    if (rv !== undefined && rv !== null && s.cancelable) s.canceled = true;
  } else if (rv === false && s.cancelable) {
    s.canceled = true;
  }
}

function defineEventHandlers(target, names, forwardTo) {
  names.forEach(function (n) {
    var type = n;
    defineProperty(target, 'on' + n, {
      get: function () {
        var obj = forwardTo ? forwardTo() : targetObj(this);
        var h = obj[EH];
        if (h && type in h) return h[type];
        var r = unwrap(obj);
        if (r && r.t === 1 && r.a) {
          var code = getAttrRaw(r, 'on' + type);
          if (code !== null) return inlineHandler(r, type, code);
        }
        return null;
      },
      set: function (v) {
        var obj = forwardTo ? forwardTo() : targetObj(this);
        var h = handlerSlot(obj, true);
        h[type] = typeof v === 'function' || (v !== null && typeof v === 'object') ? v : null;
      },
      enumerable: true, configurable: true
    });
  });
}
defineEventHandlers(HTMLElement.prototype, GLOBAL_EVENT_HANDLERS);
defineEventHandlers(SVGElement.prototype, GLOBAL_EVENT_HANDLERS);
defineEventHandlers(MathMLElement.prototype, GLOBAL_EVENT_HANDLERS);
defineEventHandlers(Document.prototype, GLOBAL_EVENT_HANDLERS.concat(['readystatechange', 'visibilitychange',
  'pointerlockchange', 'pointerlockerror', 'fullscreenchange', 'fullscreenerror', 'freeze', 'resume', 'prerenderingchange']));
defineEventHandlers(global, GLOBAL_EVENT_HANDLERS.concat(WINDOW_EVENT_HANDLERS, ['devicemotion', 'deviceorientation',
  'deviceorientationabsolute', 'orientationchange', 'appinstalled', 'beforeinstallprompt', 'search', 'gamepadconnected',
  'gamepaddisconnected']));
(function () {
  var forward = function () { return global; };
  var names = Object.keys(BODY_FORWARDED);
  defineEventHandlers(HTMLBodyElement.prototype, names, forward);
  defineEventHandlers(HTMLFrameSetElement.prototype, names, forward);
})();
try { defineProperty(global, 'event', { value: undefined, writable: true, configurable: true, enumerable: false }); } catch (e) { /* ignore */ }

// fireEvent: a trusted event from the engine (load, DOMContentLoaded, readystatechange, toggle …).
function fireEvent(target, type, init, Ctor) {
  var ev = createEvent(Ctor || Event, type, init);
  ev[ES].trusted = true;
  dispatchEvent(target, ev);
  return ev;
}

if (testHooks) {
  testHooks.fireEvent = fireEvent;
  testHooks.dispatchEvent = dispatchEvent;
  testHooks.defineEventHandlers = defineEventHandlers;
}
