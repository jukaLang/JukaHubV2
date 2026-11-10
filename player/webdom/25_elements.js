// 25_elements.js — Element, HTMLElement and the HTML/SVG element interfaces: reflected attributes, form
// controls (value/checked/selected state), tables, media stubs and plausible geometry.
//
// Geometry: the reader has no layout engine, but scripts ask for sizes all the time — mostly to decide
// whether something is visible (lazy loaders, "is in viewport" checks). Rendered elements therefore report
// a plausible non-zero box at the top of the viewport; elements that are disconnected or hidden (hidden
// attribute, inline display:none on them or an ancestor) report zeros, as in a browser.

// ---------------------------------------------------------------- reflection helpers

function reflectStr(proto, specs) {
  specs.split(' ').forEach(function (spec) {
    var parts = spec.split(':');
    var prop = parts[0], attr = parts[1] || asciiLower(prop);
    defineProperty(proto, prop, {
      get: function () { var v = getAttrRaw(R(this), attr); return v === null ? '' : v; },
      set: function (v) { setAttrRaw(R(this), attr, str(v)); },
      enumerable: true, configurable: true
    });
  });
}
function reflectNullableStr(proto, specs) {
  specs.split(' ').forEach(function (spec) {
    var parts = spec.split(':');
    var prop = parts[0], attr = parts[1] || asciiLower(prop);
    defineProperty(proto, prop, {
      get: function () { return getAttrRaw(R(this), attr); },
      set: function (v) {
        var r = R(this);
        if (v === null || v === undefined) removeAttrRaw(r, attr);
        else setAttrRaw(r, attr, str(v));
      },
      enumerable: true, configurable: true
    });
  });
}
function reflectBool(proto, specs) {
  specs.split(' ').forEach(function (spec) {
    var parts = spec.split(':');
    var prop = parts[0], attr = parts[1] || asciiLower(prop);
    defineProperty(proto, prop, {
      get: function () { return getAttrRaw(R(this), attr) !== null; },
      set: function (v) {
        var r = R(this);
        if (v) setAttrRaw(r, attr, '');
        else removeAttrRaw(r, attr);
      },
      enumerable: true, configurable: true
    });
  });
}
// HTML "rules for parsing integers": leading whitespace, sign, digits; anything else → NaN.
function parseHTMLInt(s) {
  var m = /^[\t\n\f\r ]*([-+]?[0-9]+)/.exec(s);
  return m ? parseInt(m[1], 10) : NaN;
}
function parseHTMLFloat(s) {
  var m = /^[\t\n\f\r ]*(-?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][-+]?[0-9]+)?)/.exec(s);
  return m ? parseFloat(m[1]) : NaN;
}
// specs: "prop=default" or "prop:attr=default"; unsigned when nonNegative.
function reflectLong(proto, specs, nonNegative) {
  specs.split(' ').forEach(function (spec) {
    var eq = spec.split('=');
    var def = +eq[1];
    var parts = eq[0].split(':');
    var prop = parts[0], attr = parts[1] || asciiLower(prop);
    defineProperty(proto, prop, {
      get: function () {
        var v = getAttrRaw(R(this), attr);
        if (v === null) return def;
        var n = parseHTMLInt(v);
        if (isNaN(n) || (nonNegative && n < 0) || n > 2147483647 || n < -2147483648) return def;
        return n;
      },
      set: function (v) {
        var n = Number(v);
        if (nonNegative && n < 0) throw domErr('IndexSizeError', 'The value provided (' + n + ') is negative.');
        setAttrRaw(R(this), attr, String(n | 0));
      },
      enumerable: true, configurable: true
    });
  });
}
function reflectURL(proto, specs) {
  specs.split(' ').forEach(function (spec) {
    var parts = spec.split(':');
    var prop = parts[0], attr = parts[1] || asciiLower(prop);
    defineProperty(proto, prop, {
      get: function () {
        var r = R(this);
        var v = getAttrRaw(r, attr);
        if (v === null) return '';
        var abs = resolveURL(r.d, v);
        return abs || v;
      },
      set: function (v) { setAttrRaw(R(this), attr, str(v)); },
      enumerable: true, configurable: true
    });
  });
}
function reflectEnum(proto, prop, attr, keywords, missing, invalid) {
  defineProperty(proto, prop, {
    get: function () {
      var v = getAttrRaw(R(this), attr);
      if (v === null) return missing;
      var lower = asciiLower(v);
      for (var i = 0; i < keywords.length; i++) if (keywords[i] === lower) return lower;
      return invalid;
    },
    set: function (v) { setAttrRaw(R(this), attr, str(v)); },
    enumerable: true, configurable: true
  });
}

// ---------------------------------------------------------------- geometry

var DOMRectReadOnly = iface('DOMRectReadOnly', null, function DOMRectReadOnly(x, y, width, height) {
  if (!(this instanceof DOMRectReadOnly)) throw new TypeError("Failed to construct 'DOMRectReadOnly': Please use the 'new' operator.");
  this._x = +x || 0;
  this._y = +y || 0;
  this._w = +width || 0;
  this._h = +height || 0;
});
mix(DOMRectReadOnly.prototype, {
  get x() { return this._x; },
  get y() { return this._y; },
  get width() { return this._w; },
  get height() { return this._h; },
  get top() { return Math.min(this._y, this._y + this._h); },
  get left() { return Math.min(this._x, this._x + this._w); },
  get right() { return Math.max(this._x, this._x + this._w); },
  get bottom() { return Math.max(this._y, this._y + this._h); },
  toJSON: function () {
    return { x: this.x, y: this.y, width: this.width, height: this.height, top: this.top, right: this.right, bottom: this.bottom, left: this.left };
  }
});
DOMRectReadOnly.fromRect = function (o) { o = o || {}; return new DOMRectReadOnly(o.x, o.y, o.width, o.height); };
var DOMRect = iface('DOMRect', DOMRectReadOnly, function DOMRect(x, y, width, height) {
  if (!(this instanceof DOMRect)) throw new TypeError("Failed to construct 'DOMRect': Please use the 'new' operator.");
  this._x = +x || 0;
  this._y = +y || 0;
  this._w = +width || 0;
  this._h = +height || 0;
});
mix(DOMRect.prototype, {
  get x() { return this._x; }, set x(v) { this._x = +v; },
  get y() { return this._y; }, set y(v) { this._y = +v; },
  get width() { return this._w; }, set width(v) { this._w = +v; },
  get height() { return this._h; }, set height(v) { this._h = +v; }
});
DOMRect.fromRect = function (o) { o = o || {}; return new DOMRect(o.x, o.y, o.width, o.height); };
var DOMRectList = iface('DOMRectList', null);
mix(DOMRectList.prototype, {
  item: function (i) { return this[i >>> 0] || null; }
});
defineProperty(DOMRectList.prototype, Symbol.iterator, { value: Array.prototype[Symbol.iterator], writable: true, configurable: true });
function rectList(rects) {
  var o = objCreate(DOMRectList.prototype);
  for (var i = 0; i < rects.length; i++) defineProperty(o, i, { value: rects[i], enumerable: true });
  defineProperty(o, 'length', { value: rects.length });
  return o;
}
var DOMPointReadOnly = iface('DOMPointReadOnly', null, function DOMPointReadOnly(x, y, z, w) {
  if (!(this instanceof DOMPointReadOnly)) throw new TypeError("Failed to construct 'DOMPointReadOnly': Please use the 'new' operator.");
  this._x = +x || 0; this._y = +y || 0; this._z = +z || 0; this._w = w === undefined ? 1 : +w;
});
mix(DOMPointReadOnly.prototype, {
  get x() { return this._x; }, get y() { return this._y; }, get z() { return this._z; }, get w() { return this._w; },
  toJSON: function () { return { x: this._x, y: this._y, z: this._z, w: this._w }; },
  matrixTransform: function () { return new DOMPoint(this._x, this._y, this._z, this._w); }
});
var DOMPoint = iface('DOMPoint', DOMPointReadOnly, function DOMPoint(x, y, z, w) {
  if (!(this instanceof DOMPoint)) throw new TypeError("Failed to construct 'DOMPoint': Please use the 'new' operator.");
  this._x = +x || 0; this._y = +y || 0; this._z = +z || 0; this._w = w === undefined ? 1 : +w;
});
mix(DOMPoint.prototype, {
  get x() { return this._x; }, set x(v) { this._x = +v; },
  get y() { return this._y; }, set y(v) { this._y = +v; },
  get z() { return this._z; }, set z(v) { this._z = +v; },
  get w() { return this._w; }, set w(v) { this._w = +v; }
});
DOMPoint.fromPoint = function (o) { o = o || {}; return new DOMPoint(o.x, o.y, o.z, o.w); };

var INLINE_TAGS = { a: 1, abbr: 1, b: 1, bdi: 1, bdo: 1, br: 1, cite: 1, code: 1, data: 1, dfn: 1, em: 1, i: 1, kbd: 1,
  label: 1, mark: 1, q: 1, s: 1, samp: 1, small: 1, span: 1, strong: 1, sub: 1, sup: 1, time: 1, u: 1, var: 1,
  font: 1, big: 1, tt: 1, strike: 1, button: 1, input: 1, select: 1, textarea: 1, img: 1, svg: 1, wbr: 1 };
var NEVER_RENDERED = { head: 1, script: 1, style: 1, template: 1, title: 1, meta: 1, link: 1, base: 1, noscript: 1 };

// elementRendered: connected, and neither it nor an ancestor is hidden by attribute or inline display:none.
var INLINE_DISPLAY_NONE = /display\s*:\s*none/i;
function elementRendered(r) {
  if (!r.c) return false;
  for (var a = r; a; a = a.p || (a.shadow ? a.host : null)) {
    if (a.t !== 1) continue;
    if (a.ns === 0 && NEVER_RENDERED[a.ln]) return false;
    if (a.a) {
      if (a.ns === 0 && getAttrRaw(a, 'hidden') !== null) return false;
      var st = getAttrRaw(a, 'style');
      if (st && st.indexOf('none') >= 0 && INLINE_DISPLAY_NONE.test(st)) return false;
    }
    // The page's own stylesheets can hide it too (45_css.js): jQuery's :visible and lazy loaders ask.
    if (cascadeHides(a)) return false;
  }
  return true;
}
// imgHasSource: the image names something to load (src or srcset), so it counts as loaded.
function imgHasSource(r) {
  var src = getAttrRaw(r, 'src'), set = getAttrRaw(r, 'srcset');
  return (src !== null && stripWS(src) !== '') || (set !== null && stripWS(set) !== '');
}
function numericAttr(r, name) {
  var v = getAttrRaw(r, name);
  if (v === null) return 0;
  var n = parseHTMLInt(v);
  return isNaN(n) || n < 0 ? 0 : n;
}
// boxOf: {w, h} for a rendered element, null otherwise.
function boxOf(r) {
  if (!elementRendered(r)) return null;
  var vp = hooks.viewport() || { w: 1280, h: 720 };
  var w = numericAttr(r, 'width'), h = numericAttr(r, 'height');
  if (r.ns === 0 && (r.ln === 'html' || r.ln === 'body')) return { w: vp.w, h: Math.max(vp.h, h) };
  if (!w) w = r.ns === 0 && INLINE_TAGS[r.ln] ? Math.min(vp.w, r.ln === 'img' ? 300 : 120) : vp.w;
  if (!h) h = r.ln === 'img' || r.ln === 'video' || r.ln === 'iframe' || r.ln === 'canvas' ? 150 : 20;
  return { w: Math.min(w, Math.max(vp.w, w)), h: h };
}

// ---------------------------------------------------------------- Element

var Element = iface('Element', Node);
function elementOf(o) {
  var r = R(o);
  if (r.t !== 1) throw new TypeError('Illegal invocation');
  return r;
}

function insertAdjacent(r, where, node) {
  switch (asciiLower(str(where))) {
    case 'beforebegin':
      if (!r.p) return null;
      insertNode(r.p, node, r);
      return node;
    case 'afterbegin':
      insertNode(r, node, r.f);
      return node;
    case 'beforeend':
      insertNode(r, node, null);
      return node;
    case 'afterend':
      if (!r.p) return null;
      insertNode(r.p, node, r.n);
      return node;
  }
  throw domErr('SyntaxError', "The value provided ('" + where + "') is not one of 'beforeBegin', 'afterBegin', 'beforeEnd', or 'afterEnd'.");
}

mix(Element.prototype, {
  get namespaceURI() { return nsURI(elementOf(this)); },
  get prefix() { var r = elementOf(this); return r.px || null; },
  get localName() { return elementOf(this).ln; },
  get tagName() { return htmlUpperName(elementOf(this)); },
  get id() { var v = getAttrRaw(elementOf(this), 'id'); return v === null ? '' : v; },
  set id(v) { setAttrRaw(elementOf(this), 'id', str(v)); },
  get className() { var v = getAttrRaw(elementOf(this), 'class'); return v === null ? '' : v; },
  set className(v) { setAttrRaw(elementOf(this), 'class', str(v)); },
  get classList() { return tokenListOf(elementOf(this), 'class'); },
  set classList(v) { setAttrRaw(elementOf(this), 'class', str(v)); },
  get part() { return tokenListOf(elementOf(this), 'part'); },
  get slot() { var v = getAttrRaw(elementOf(this), 'slot'); return v === null ? '' : v; },
  set slot(v) { setAttrRaw(elementOf(this), 'slot', str(v)); },
  get attributes() { return attributesOf(elementOf(this)); },
  hasAttributes: function () { var r = elementOf(this); return !!(r.a && r.a.length); },
  getAttributeNames: function () {
    var r = elementOf(this);
    var out = [];
    if (r.a) for (var i = 0; i < r.a.length; i += 2) out.push(r.a[i]);
    return out;
  },
  getAttribute: function (name) {
    return getAttr(elementOf(this), name);
  },
  getAttributeNS: function (ns, local) {
    var r = elementOf(this);
    var name = attrByNS(r, ns === null || ns === undefined ? null : str(ns), str(local));
    return name === null ? null : getAttrRaw(r, name);
  },
  setAttribute: function (name, value) {
    var r = elementOf(this);
    name = str(name);
    checkName(name, 'setAttribute');
    setAttrRaw(r, attrName(r, name), str(value));
  },
  setAttributeNS: function (ns, qname, value) {
    requiredArgs(arguments, 3, 'setAttributeNS');
    var r = elementOf(this);
    qname = str(qname);
    checkName(qname, 'setAttributeNS');
    ns = ns === null || ns === undefined || ns === '' ? null : str(ns);
    var colon = qname.indexOf(':');
    var local = colon > 0 ? qname.slice(colon + 1) : qname;
    var existing = attrByNS(r, ns, local);
    var name = existing !== null ? existing : qname;
    setAttrRaw(r, name, str(value));
    if (ns !== null) {
      var ar = attrRecFor(r, name);
      ar.nsu = ns;
    }
  },
  removeAttribute: function (name) { removeAttr(elementOf(this), name); },
  removeAttributeNS: function (ns, local) {
    var r = elementOf(this);
    var name = attrByNS(r, ns === null || ns === undefined ? null : str(ns), str(local));
    if (name !== null) removeAttrRaw(r, name);
  },
  toggleAttribute: function (name, force) {
    var r = elementOf(this);
    name = str(name);
    checkName(name, 'toggleAttribute');
    name = attrName(r, name);
    var has = getAttrRaw(r, name) !== null;
    if (!has) {
      if (force === undefined || force) {
        setAttrRaw(r, name, '');
        return true;
      }
      return false;
    }
    if (force === undefined || !force) {
      removeAttrRaw(r, name);
      return false;
    }
    return true;
  },
  hasAttribute: function (name) { return getAttr(elementOf(this), name) !== null; },
  hasAttributeNS: function (ns, local) {
    return attrByNS(elementOf(this), ns === null || ns === undefined ? null : str(ns), str(local)) !== null;
  },
  getAttributeNode: function (name) {
    var r = elementOf(this);
    name = attrName(r, str(name));
    return getAttrRaw(r, name) === null ? null : wrap(attrRecFor(r, name));
  },
  getAttributeNodeNS: function (ns, local) {
    var r = elementOf(this);
    var name = attrByNS(r, ns === null || ns === undefined ? null : str(ns), str(local));
    return name === null ? null : wrap(attrRecFor(r, name));
  },
  setAttributeNode: function (attr) { return setAttributeNode(elementOf(this), nodeArg(attr, 'setAttributeNode')); },
  setAttributeNodeNS: function (attr) { return setAttributeNode(elementOf(this), nodeArg(attr, 'setAttributeNodeNS')); },
  removeAttributeNode: function (attr) {
    var r = elementOf(this);
    var ar = nodeArg(attr, 'removeAttributeNode');
    if (ar.t !== 2 || ar.el !== r) throw domErr('NotFoundError', 'The attribute is not owned by this element.');
    removeAttrRaw(r, ar.name);
    return attr;
  },
  matches: function (sel) {
    return matchesSelector(elementOf(this), str(sel));
  },
  webkitMatchesSelector: function (sel) { return matchesSelector(elementOf(this), str(sel)); },
  msMatchesSelector: function (sel) { return matchesSelector(elementOf(this), str(sel)); },
  mozMatchesSelector: function (sel) { return matchesSelector(elementOf(this), str(sel)); },
  closest: function (sel) {
    var r = elementOf(this);
    var m = compileSelector(str(sel));
    for (var e = r; e && e.t === 1; e = e.p) if (m(e, r)) return wrap(e);
    return null;
  },
  insertAdjacentElement: function (where, el) {
    var r = elementOf(this);
    var n = nodeArg(el, 'insertAdjacentElement');
    if (n.t !== 1) throw new TypeError("Failed to execute 'insertAdjacentElement': parameter 2 is not of type 'Element'.");
    var res = insertAdjacent(r, where, n);
    return res ? el : null;
  },
  insertAdjacentText: function (where, data) {
    var r = elementOf(this);
    insertAdjacent(r, where, textRec(r.d, str(data)));
  },
  insertAdjacentHTML: function (where, html) {
    requiredArgs(arguments, 2, 'insertAdjacentHTML');
    var r = elementOf(this);
    var pos = asciiLower(str(where));
    var ctx;
    if (pos === 'beforebegin' || pos === 'afterend') {
      if (!r.p || r.p.t === 9) throw domErr('NoModificationAllowedError', 'The element has no parent.');
      ctx = r.p.t === 1 ? r.p : null;
    } else if (pos === 'afterbegin' || pos === 'beforeend') {
      ctx = r;
    } else {
      throw domErr('SyntaxError', "The value provided ('" + where + "') is not one of 'beforeBegin', 'afterBegin', 'beforeEnd', or 'afterEnd'.");
    }
    if (!ctx || (ctx.ns === 0 && ctx.ln === 'html')) ctx = createElementRec(r.d, 'body', 0);
    var frag = parseFragmentInto(ctx, strOrEmpty(html), 'fragment');
    insertAdjacent(r, pos, frag);
  },
  get innerHTML() { return serializeChildren(elementOf(this)); },
  set innerHTML(v) { setInnerHTML(elementOf(this), strOrEmpty(v)); },
  get outerHTML() { return serializeNode(elementOf(this)); },
  set outerHTML(v) {
    var r = elementOf(this);
    var parent = r.p;
    if (!parent) return;
    if (parent.t === 9) throw domErr('NoModificationAllowedError', 'This element has no parent node.');
    var ctx = parent.t === 11 ? createElementRec(r.d, 'body', 0) : parent;
    var frag = parseFragmentInto(ctx, strOrEmpty(v), 'fragment');
    replaceChildRaw(parent, frag, r);
  },
  getHTML: function (opts) {
    var r = elementOf(this);
    return serializeChildren(r, opts && opts.serializableShadowRoots ? true : false);
  },
  setHTMLUnsafe: function (html) {
    var r = elementOf(this);
    var frag = parseFragmentInto(r.ln === 'template' && r.tc ? r : r, strOrEmpty(html), 'fragment');
    attachDeclarativeShadowRoots(frag);
    replaceAllRaw(r.ln === 'template' && r.tc ? r.tc : r, frag);
  },
  getBoundingClientRect: function () {
    var b = boxOf(elementOf(this));
    return b ? new DOMRect(0, 0, b.w, b.h) : new DOMRect(0, 0, 0, 0);
  },
  getClientRects: function () {
    var b = boxOf(elementOf(this));
    return rectList(b ? [new DOMRect(0, 0, b.w, b.h)] : []);
  },
  checkVisibility: function () { return elementRendered(elementOf(this)); },
  scrollIntoView: function () {},
  scrollIntoViewIfNeeded: function () {},
  scroll: function (x, y) { scrollElementTo(elementOf(this), x, y, false); },
  scrollTo: function (x, y) { scrollElementTo(elementOf(this), x, y, false); },
  scrollBy: function (x, y) { scrollElementTo(elementOf(this), x, y, true); },
  get scrollTop() { return elementOf(this).sy || 0; },
  set scrollTop(v) { elementOf(this).sy = Math.max(0, +v || 0); },
  get scrollLeft() { return elementOf(this).sx || 0; },
  set scrollLeft(v) { elementOf(this).sx = Math.max(0, +v || 0); },
  get scrollWidth() { var b = boxOf(elementOf(this)); return b ? b.w : 0; },
  get scrollHeight() { var b = boxOf(elementOf(this)); return b ? b.h : 0; },
  get clientTop() { return 0; },
  get clientLeft() { return 0; },
  get clientWidth() {
    var r = elementOf(this);
    if (r.d && documentElementOf(r.d) === r) return (hooks.viewport() || { w: 1280 }).w;
    var b = boxOf(r);
    return b ? b.w : 0;
  },
  get clientHeight() {
    var r = elementOf(this);
    if (r.d && documentElementOf(r.d) === r) return (hooks.viewport() || { h: 720 }).h;
    var b = boxOf(r);
    return b ? b.h : 0;
  },
  get currentCSSZoom() { return 1; },
  requestFullscreen: function () { return Promise.reject(domErr('NotSupportedError', 'Fullscreen is not supported.')); },
  webkitRequestFullscreen: function () {},
  requestPointerLock: function () { return Promise.reject(domErr('NotSupportedError', 'Pointer lock is not supported.')); },
  setPointerCapture: function () {},
  releasePointerCapture: function () {},
  hasPointerCapture: function () { return false; },
  animate: function (keyframes, options) { return makeAnimation(elementOf(this), options); },
  getAnimations: function () { return []; },
  get assignedSlot() { return wrap(assignedSlotOf(elementOf(this))); },
  get role() { return getAttrRaw(elementOf(this), 'role'); },
  set role(v) { var r = elementOf(this); if (v === null) removeAttrRaw(r, 'role'); else setAttrRaw(r, 'role', str(v)); }
});
mix(Element.prototype, parentNodeMixin);
mix(Element.prototype, childNodeMixin);
mix(Element.prototype, nonDocumentTypeChildNodeMixin);
(function () {
  // ARIA reflection (ariaLabel ↔ aria-label …): nullable strings.
  var aria = 'ariaAtomic ariaAutoComplete ariaBusy ariaChecked ariaColCount ariaColIndex ariaColSpan ariaCurrent ' +
    'ariaDescription ariaDisabled ariaExpanded ariaHasPopup ariaHidden ariaInvalid ariaKeyShortcuts ariaLabel ' +
    'ariaLevel ariaLive ariaModal ariaMultiLine ariaMultiSelectable ariaOrientation ariaPlaceholder ariaPosInSet ' +
    'ariaPressed ariaReadOnly ariaRequired ariaRoleDescription ariaRowCount ariaRowIndex ariaRowSpan ariaSelected ' +
    'ariaSetSize ariaSort ariaValueMax ariaValueMin ariaValueNow ariaValueText';
  reflectNullableStr(Element.prototype, aria.split(' ').map(function (p) {
    return p + ':aria-' + asciiLower(p.slice(4));
  }).join(' '));
})();

function scrollElementTo(r, x, y, relative) {
  var left = x, top = y;
  if (x && typeof x === 'object') {
    left = x.left;
    top = x.top;
  }
  if (left !== undefined && !isNaN(+left)) r.sx = Math.max(0, (relative ? (r.sx || 0) : 0) + (+left));
  if (top !== undefined && !isNaN(+top)) r.sy = Math.max(0, (relative ? (r.sy || 0) : 0) + (+top));
}

function setInnerHTML(r, html) {
  var target = r.t === 1 && r.tc ? r.tc : r;
  var ctx = r.t === 1 ? r : null;
  if (r.t === 11 && r.shadow) ctx = r.host;
  // Fast path for plain text: no markup characters means one text node.
  if (html.indexOf('<') < 0 && html.indexOf('&') < 0 && html.indexOf('\r') < 0 && html.indexOf('\u0000') < 0 &&
      !(ctx && ctx.ns === 0 && RAWTEXT_CONTEXT[ctx.ln])) {
    replaceAllRaw(target, html === '' ? null : textRec(target.d, html));
    return;
  }
  var frag = parseFragmentInto(ctx, html, 'fragment');
  replaceAllRaw(target, frag);
}
var RAWTEXT_CONTEXT = { script: 1, style: 1, xmp: 1, iframe: 1, noembed: 1, noframes: 1, noscript: 1, textarea: 1, title: 1, plaintext: 1 };

// Animation stub: finished immediately, so code awaiting `animation.finished` proceeds.
function makeAnimation(r, options) {
  var finished = Promise.resolve();
  var anim = {
    playState: 'finished', currentTime: 0, startTime: 0, playbackRate: 1, pending: false, id: '',
    effect: { target: wrap(r), getTiming: function () { return {}; }, getComputedTiming: function () { return {}; } },
    finished: null, ready: finished, onfinish: null, oncancel: null,
    play: function () {}, pause: function () {}, cancel: function () {}, finish: function () {}, reverse: function () {},
    commitStyles: function () {}, persist: function () {}, updatePlaybackRate: function () {},
    addEventListener: function (type, cb) { if (type === 'finish' && typeof cb === 'function') hooks.queueTask(function () { cb({ type: 'finish' }); }); },
    removeEventListener: function () {}
  };
  anim.finished = finished.then(function () { return anim; });
  hooks.queueTask(function () {
    if (typeof anim.onfinish === 'function') {
      try { anim.onfinish({ type: 'finish', target: anim }); } catch (e) { reportException(e); }
    }
  });
  return anim;
}

// ---------------------------------------------------------------- HTMLElement

function HTMLElementCtor() {
  // Custom element constructors call this through super(); see 50_shadow_ce.js.
  return ceConstruct(new.target);
}
var HTMLElement = iface('HTMLElement', Element, HTMLElementCtor);

// innerText: rendered text — hidden subtrees skipped, block boundaries as line breaks (two after <p>),
// whitespace collapsed outside <pre>/<textarea>. Not rendered → textContent, as in browsers.
function innerTextOf(r) {
  if (!elementRendered(r)) return textOf(r);
  var out = [];
  var pending = 0;
  function blockBreak(e, enter) {
    if (e.t !== 1 || e.ns !== 0 || INLINE_TAGS[e.ln]) return;
    var n = e.ln === 'p' ? 2 : 1;
    if (n > pending) pending = n;
  }
  function inPre(k) {
    for (var a = k.p; a && a !== r.p; a = a.p) if (a.t === 1 && a.ns === 0 && (a.ln === 'pre' || a.ln === 'textarea' || a.ln === 'listing')) return true;
    return false;
  }
  var k = r.f;
  while (k) {
    var skip = false;
    if (k.t === 1) {
      if (k.ns === 0 && (NEVER_RENDERED[k.ln] || getAttrRaw(k, 'hidden') !== null)) skip = true;
      else if (k.a) {
        var st = getAttrRaw(k, 'style');
        if (st && st.indexOf('none') >= 0 && INLINE_DISPLAY_NONE.test(st)) skip = true;
      }
      if (!skip) {
        if (k.ns === 0 && k.ln === 'br') {
          out.push('\n');
          pending = 0;
        } else {
          blockBreak(k, true);
        }
      }
    } else if (k.t === 3 || k.t === 4) {
      var text = inPre(k) ? k.x : k.x.replace(/[\t\n\f\r ]+/g, ' ');
      if (text !== '') {
        var last = out.length ? out[out.length - 1] : '';
        if (text === ' ') {
          if (last && !/\s$/.test(last) && !pending) out.push(' ');
        } else {
          if (pending && out.length) {
            out.push(pending === 2 ? '\n\n' : '\n');
            if (text.charAt(0) === ' ') text = text.slice(1);
          } else if (/\s$/.test(last) && text.charAt(0) === ' ') {
            text = text.slice(1);
          }
          pending = 0;
          out.push(text);
        }
      }
    }
    if (!skip && k.f) {
      k = k.f;
      continue;
    }
    if (k.t === 1 && !skip) blockBreak(k, false);
    while (k && k !== r && !k.n) {
      k = k.p;
      if (k && k !== r) blockBreak(k, false);
    }
    if (!k || k === r) break;
    k = k.n;
  }
  return out.join('').replace(/ +\n/g, '\n').replace(/\n +/g, '\n').replace(/^\s+|\s+$/g, '');
}
function setInnerText(r, v) {
  var s = strOrEmpty(v);
  var frag = newFragmentRec(r.d);
  var lines = s.split(/\r\n|\r|\n/);
  for (var i = 0; i < lines.length; i++) {
    if (i > 0) attachBuilt(frag, createElementRec(r.d, 'br', 0));
    if (lines[i] !== '') attachBuilt(frag, textRec(r.d, lines[i]));
  }
  replaceAllRaw(r, frag.f ? frag : null);
}

var FOCUSABLE = { a: 1, area: 1, button: 1, input: 1, select: 1, textarea: 1, iframe: 1, summary: 1 };
function focusable(r) {
  if (r.ns !== 0 && r.ns !== 1) return false;
  if (getAttrRaw(r, 'tabindex') !== null) return true;
  if (r.ns === 0 && FOCUSABLE[r.ln]) return r.ln !== 'a' || getAttrRaw(r, 'href') !== null;
  var ce = getAttrRaw(r, 'contenteditable');
  return ce !== null && asciiLower(ce) !== 'false';
}
function focusElement(r) {
  if (!r.c || !focusable(r)) return;
  var d = r.d;
  if (d.active === r) return;
  if (d.active && d.active.c) blurElement(d.active);
  d.active = r;
  fireEvent(wrap(r), 'focus', { bubbles: false, composed: true }, FocusEvent);
  fireEvent(wrap(r), 'focusin', { bubbles: true, composed: true }, FocusEvent);
}
function blurElement(r) {
  var d = r.d;
  if (d.active !== r) return;
  d.active = null;
  fireEvent(wrap(r), 'blur', { bubbles: false, composed: true }, FocusEvent);
  fireEvent(wrap(r), 'focusout', { bubbles: true, composed: true }, FocusEvent);
}

// click(): synthetic click with the activation behaviour scripts rely on.
function clickElement(r) {
  if (r.clicking) return;
  if (r.ns === 0 && isDisabledControl(r)) return;
  flagOn(r, 'clicking');
  try {
    var input = r.ns === 0 && r.ln === 'input' ? inputType(r) : '';
    var oldChecked = null, radioPrev = null;
    if (input === 'checkbox') {
      oldChecked = checkedOf(r);
      r.fc = !oldChecked;
    } else if (input === 'radio') {
      oldChecked = checkedOf(r);
      radioPrev = checkedRadioInGroup(r);
      setChecked(r, true);
    }
    var ev = createEvent(MouseEvent, 'click', { bubbles: true, cancelable: true, composed: true, view: global, detail: 1 });
    eventState(ev).trusted = false;
    var ok = dispatchEvent(wrap(r), ev);
    if (input === 'checkbox' || input === 'radio') {
      if (!ok) {
        if (input === 'checkbox') r.fc = oldChecked;
        else if (radioPrev && radioPrev !== r) setChecked(radioPrev, true);
        else if (!oldChecked) r.fc = false;
      } else if (oldChecked !== checkedOf(r)) {
        fireEvent(wrap(r), 'input', { bubbles: true, composed: true });
        fireEvent(wrap(r), 'change', { bubbles: true });
      }
      return;
    }
    if (!ok) return;
    activationBehavior(r);
  } finally {
    flagOff(r, 'clicking');
  }
}
function activationBehavior(target) {
  for (var r = target; r; r = r.p) {
    if (r.t !== 1 || r.ns !== 0) continue;
    if ((r.ln === 'a' || r.ln === 'area') && getAttrRaw(r, 'href') !== null) {
      var href = resolveURL(r.d, getAttrRaw(r, 'href'));
      var tgt = asciiLower(getAttrRaw(r, 'target') || '');
      if (href && getAttrRaw(r, 'download') === null && (tgt === '' || tgt === '_self' || tgt === '_top' || tgt === '_parent') && r.d === mainDoc) {
        try { hooks.navigate(href, false); } catch (e) { reportException(e); }
      }
      return;
    }
    if ((r.ln === 'button' && buttonType(r) === 'submit') || (r.ln === 'input' && (inputType(r) === 'submit' || inputType(r) === 'image'))) {
      var form = formOwner(r);
      if (form) submitForm(form, r, true);
      return;
    }
    if (r.ln === 'button' && buttonType(r) === 'reset') {
      var f2 = formOwner(r);
      if (f2) resetForm(f2);
      return;
    }
    if (r.ln === 'summary' && r.p && r.p.t === 1 && r.p.ln === 'details') {
      if (getAttrRaw(r.p, 'open') !== null) removeAttrRaw(r.p, 'open');
      else setAttrRaw(r.p, 'open', '');
      return;
    }
    if (r.ln === 'label') {
      var control = labelControl(r);
      if (control && control !== target && !isInclusiveAncestor(control, target)) clickElement(control);
      return;
    }
  }
}

mix(HTMLElement.prototype, {
  get innerText() { return innerTextOf(elementOf(this)); },
  set innerText(v) { setInnerText(elementOf(this), v); },
  get outerText() { return innerTextOf(elementOf(this)); },
  set outerText(v) {
    var r = elementOf(this);
    if (!r.p) throw domErr('NoModificationAllowedError', 'The element has no parent.');
    replaceChildRaw(r.p, textRec(r.d, strOrEmpty(v)), r);
  },
  get hidden() {
    var v = getAttrRaw(elementOf(this), 'hidden');
    if (v === null) return false;
    return asciiLower(v) === 'until-found' ? 'until-found' : true;
  },
  set hidden(v) {
    var r = elementOf(this);
    if (v === 'until-found') setAttrRaw(r, 'hidden', 'until-found');
    else if (v) setAttrRaw(r, 'hidden', '');
    else removeAttrRaw(r, 'hidden');
  },
  get tabIndex() {
    var r = elementOf(this);
    var v = getAttrRaw(r, 'tabindex');
    var n = v === null ? NaN : parseHTMLInt(v);
    if (!isNaN(n)) return n;
    return focusable(r) ? 0 : -1;
  },
  set tabIndex(v) { setAttrRaw(elementOf(this), 'tabindex', String(Number(v) | 0)); },
  get dir() {
    var v = getAttrRaw(elementOf(this), 'dir');
    v = v === null ? '' : asciiLower(v);
    return v === 'ltr' || v === 'rtl' || v === 'auto' ? v : '';
  },
  set dir(v) { setAttrRaw(elementOf(this), 'dir', str(v)); },
  get draggable() {
    var r = elementOf(this);
    var v = getAttrRaw(r, 'draggable');
    if (v !== null) return asciiLower(v) === 'true';
    return r.ln === 'img' || (r.ln === 'a' && getAttrRaw(r, 'href') !== null);
  },
  set draggable(v) { setAttrRaw(elementOf(this), 'draggable', v ? 'true' : 'false'); },
  get spellcheck() { var v = getAttrRaw(elementOf(this), 'spellcheck'); return v === null || asciiLower(v) !== 'false'; },
  set spellcheck(v) { setAttrRaw(elementOf(this), 'spellcheck', v ? 'true' : 'false'); },
  get translate() { var v = getAttrRaw(elementOf(this), 'translate'); return v === null || asciiLower(v) !== 'no'; },
  set translate(v) { setAttrRaw(elementOf(this), 'translate', v ? 'yes' : 'no'); },
  get contentEditable() {
    var v = getAttrRaw(elementOf(this), 'contenteditable');
    if (v === null) return 'inherit';
    v = asciiLower(v);
    if (v === '' || v === 'true') return 'true';
    if (v === 'false') return 'false';
    if (v === 'plaintext-only') return v;
    return 'inherit';
  },
  set contentEditable(v) {
    var r = elementOf(this);
    var s = asciiLower(str(v));
    if (s === 'inherit') removeAttrRaw(r, 'contenteditable');
    else if (s === 'true' || s === 'false' || s === 'plaintext-only') setAttrRaw(r, 'contenteditable', s);
    else throw domErr('SyntaxError', "The value provided ('" + v + "') is not one of 'true', 'false', 'plaintext-only', or 'inherit'.");
  },
  get isContentEditable() {
    for (var r = elementOf(this); r && r.t === 1; r = r.p) {
      var v = getAttrRaw(r, 'contenteditable');
      if (v !== null) return asciiLower(v) !== 'false';
    }
    return false;
  },
  get offsetParent() {
    var r = elementOf(this);
    if (!elementRendered(r) || r.ln === 'body' || r.ln === 'html') return null;
    return wrap(bodyOf(r.d));
  },
  get offsetTop() { return 0; },
  get offsetLeft() { return 0; },
  get offsetWidth() { var b = boxOf(elementOf(this)); return b ? b.w : 0; },
  get offsetHeight() { var b = boxOf(elementOf(this)); return b ? b.h : 0; },
  click: function () { clickElement(elementOf(this)); },
  focus: function () { focusElement(elementOf(this)); },
  blur: function () { blurElement(elementOf(this)); },
  attachInternals: function () { return makeElementInternals(elementOf(this)); },
  showPopover: function () { setAttrRaw(elementOf(this), 'data-jh-popover-open', ''); },
  hidePopover: function () { removeAttrRaw(elementOf(this), 'data-jh-popover-open'); },
  togglePopover: function (force) {
    var r = elementOf(this);
    var open = getAttrRaw(r, 'data-jh-popover-open') !== null;
    var want = force === undefined ? !open : !!force;
    if (want) setAttrRaw(r, 'data-jh-popover-open', ''); else removeAttrRaw(r, 'data-jh-popover-open');
    return want;
  },
  get autofocus() { return getAttrRaw(elementOf(this), 'autofocus') !== null; },
  set autofocus(v) { var r = elementOf(this); if (v) setAttrRaw(r, 'autofocus', ''); else removeAttrRaw(r, 'autofocus'); },
  get nonce() { var r = elementOf(this); return r.nonce !== undefined ? r.nonce : (getAttrRaw(r, 'nonce') || ''); },
  set nonce(v) { elementOf(this).nonce = str(v); }
});
reflectStr(HTMLElement.prototype, 'title lang accessKey:accesskey autocapitalize enterKeyHint:enterkeyhint inputMode:inputmode popover');
reflectBool(HTMLElement.prototype, 'inert');
defineProperty(HTMLElement.prototype, 'accessKeyLabel', { get: function () { return ''; }, enumerable: true, configurable: true });

function makeElementInternals(r) {
  var states = new Set();
  return {
    shadowRoot: r.sr ? wrap(r.sr) : null,
    form: null, labels: makeStaticList([]), willValidate: false, validationMessage: '',
    validity: makeValidity(), states: states,
    setFormValue: function () {}, setValidity: function () {}, checkValidity: function () { return true; },
    reportValidity: function () { return true; }
  };
}

// ---------------------------------------------------------------- form controls

var ValidityState = iface('ValidityState', null);
function makeValidity() {
  var v = objCreate(ValidityState.prototype);
  ['valueMissing', 'typeMismatch', 'patternMismatch', 'tooLong', 'tooShort', 'rangeUnderflow', 'rangeOverflow',
    'stepMismatch', 'badInput', 'customError'].forEach(function (k) { defineProperty(v, k, { value: false, enumerable: true }); });
  defineProperty(v, 'valid', { value: true, enumerable: true });
  return v;
}
var FileList = iface('FileList', null);
mix(FileList.prototype, { item: function () { return null; }, get length() { return 0; } });
defineProperty(FileList.prototype, Symbol.iterator, { value: function () { return [][Symbol.iterator](); }, writable: true, configurable: true });

var INPUT_TYPES = ['hidden', 'text', 'search', 'tel', 'url', 'email', 'password', 'date', 'month', 'week', 'time',
  'datetime-local', 'number', 'range', 'color', 'checkbox', 'radio', 'file', 'submit', 'image', 'reset', 'button'];
function inputType(r) {
  var v = getAttrRaw(r, 'type');
  if (v === null) return 'text';
  v = asciiLower(v);
  return INPUT_TYPES.indexOf(v) >= 0 ? v : 'text';
}
function buttonType(r) {
  var v = getAttrRaw(r, 'type');
  v = v === null ? '' : asciiLower(v);
  return v === 'reset' || v === 'button' ? v : 'submit';
}
function isDisabledControl(r) {
  if (r.t !== 1 || r.ns !== 0) return false;
  if (!(r.ln === 'button' || r.ln === 'input' || r.ln === 'select' || r.ln === 'textarea' || r.ln === 'optgroup' ||
      r.ln === 'option' || r.ln === 'fieldset')) return false;
  if (getAttrRaw(r, 'disabled') !== null) return true;
  if (r.ln === 'option' && r.p && r.p.t === 1 && r.p.ln === 'optgroup' && getAttrRaw(r.p, 'disabled') !== null) return true;
  for (var a = r.p; a; a = a.p) {
    if (a.t === 1 && a.ns === 0 && a.ln === 'fieldset' && getAttrRaw(a, 'disabled') !== null) {
      // Controls inside the fieldset's first legend stay enabled.
      var legend = null;
      for (var k = a.f; k; k = k.n) if (k.t === 1 && k.ln === 'legend') { legend = k; break; }
      if (!legend || !isInclusiveAncestor(legend, r)) return true;
    }
  }
  return false;
}

function formOwner(r) {
  var fid = getAttrRaw(r, 'form');
  if (fid !== null && r.ln !== 'form') {
    if (r.c && !inShadowTree(r)) {
      var l = r.d.ids[fid];
      if (l && l.length) {
        var f = l.length === 1 ? l[0] : firstInTreeOrder(l);
        return f.ln === 'form' && f.ns === 0 ? f : null;
      }
    }
    return null;
  }
  for (var a = r.p; a; a = a.p) if (a.t === 1 && a.ns === 0 && a.ln === 'form') return a;
  return null;
}
var LISTED = { button: 1, fieldset: 1, input: 1, object: 1, output: 1, select: 1, textarea: 1 };
var LABELABLE = { button: 1, input: 1, meter: 1, output: 1, progress: 1, select: 1, textarea: 1 };
function formElements(form) {
  var out = [];
  var root = treeRoot(form);
  for (var e = nextElement(root, root); e; e = nextElement(e, root)) {
    if (e.ns !== 0 || !LISTED[e.ln]) continue;
    if (e.ln === 'input' && inputType(e) === 'image') continue;
    if (formOwner(e) === form) out.push(e);
  }
  return out;
}

function checkedOf(r) {
  if (r.fc !== undefined) return r.fc;
  return getAttrRaw(r, 'checked') !== null;
}
function radioGroupMembers(r) {
  var name = getAttrRaw(r, 'name');
  if (!name) return [r];
  var owner = formOwner(r);
  var root = treeRoot(r);
  var out = [];
  for (var e = root.t === 1 ? root : nextElement(root, root); e; e = nextElement(e, root)) {
    if (e.ns === 0 && e.ln === 'input' && inputType(e) === 'radio' && getAttrRaw(e, 'name') === name && formOwner(e) === owner) out.push(e);
  }
  return out;
}
function checkedRadioInGroup(r) {
  var m = radioGroupMembers(r);
  for (var i = 0; i < m.length; i++) if (checkedOf(m[i])) return m[i];
  return null;
}
function setChecked(r, on) {
  r.fc = !!on;
  if (on && r.ln === 'input' && inputType(r) === 'radio') {
    var m = radioGroupMembers(r);
    for (var i = 0; i < m.length; i++) if (m[i] !== r) m[i].fc = false;
  }
  r.d.ver++;
}

function inputValue(r) {
  var type = inputType(r);
  if (type === 'checkbox' || type === 'radio') {
    var v = getAttrRaw(r, 'value');
    return v === null ? 'on' : v;
  }
  if (type === 'file') return '';
  if (r.fv !== undefined) return r.fv;
  var a = getAttrRaw(r, 'value');
  return a === null ? '' : sanitizeValue(type, a);
}
function sanitizeValue(type, v) {
  switch (type) {
    case 'text': case 'search': case 'tel': case 'password':
      return v.replace(/[\r\n]/g, '');
    case 'url': case 'email':
      return stripWS(v.replace(/[\r\n]/g, ''));
    case 'number':
      return v !== '' && isFinite(+v) && /^-?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$/.test(v) ? v : '';
    case 'color':
      return /^#[0-9a-fA-F]{6}$/.test(v) ? asciiLower(v) : '#000000';
    case 'range':
      return v !== '' && isFinite(+v) ? v : '50';
  }
  return v;
}
function setInputValue(r, v) {
  var type = inputType(r);
  if (type === 'checkbox' || type === 'radio' || type === 'hidden' || type === 'submit' || type === 'reset' ||
      type === 'button' || type === 'image') {
    setAttrRaw(r, 'value', v);
    return;
  }
  if (type === 'file') {
    if (v === '') r.fv = undefined;
    else throw domErr('InvalidStateError', 'This input element accepts a filename, which may only be programmatically set to the empty string.');
    return;
  }
  r.fv = sanitizeValue(type, v === null || v === undefined ? '' : str(v));
  r.d.ver++;
}

function textareaValue(r) { return r.fv !== undefined ? r.fv : childText(r).replace(/\r\n?/g, '\n'); }

// <select>: option list, selectedness.
function selectOptions(sel) {
  var out = [];
  for (var k = sel.f; k; k = k.n) {
    if (k.t !== 1 || k.ns !== 0) continue;
    if (k.ln === 'option') out.push(k);
    else if (k.ln === 'optgroup') {
      for (var o = k.f; o; o = o.n) if (o.t === 1 && o.ln === 'option' && o.ns === 0) out.push(o);
    }
  }
  return out;
}
function optionSelectedRaw(o) { return o.sel !== undefined ? o.sel : getAttrRaw(o, 'selected') !== null; }
function selectOf(o) {
  var p = o.p;
  if (p && p.t === 1 && p.ln === 'optgroup') p = p.p;
  return p && p.t === 1 && p.ln === 'select' && p.ns === 0 ? p : null;
}
function selectMultiple(sel) { return getAttrRaw(sel, 'multiple') !== null; }
function selectDisplaySize(sel) {
  var n = parseHTMLInt(getAttrRaw(sel, 'size') || '');
  return isNaN(n) || n <= 0 ? (selectMultiple(sel) ? 4 : 1) : n;
}
// The selected options, applying the single-select rules (last selected wins; none → first enabled).
function selectedOptions(sel) {
  var opts = selectOptions(sel);
  var out = [];
  for (var i = 0; i < opts.length; i++) if (optionSelectedRaw(opts[i])) out.push(opts[i]);
  if (selectMultiple(sel)) return out;
  if (out.length > 1) out = [out[out.length - 1]];
  if (!out.length && selectDisplaySize(sel) === 1) {
    for (var j = 0; j < opts.length; j++) {
      if (!isDisabledControl(opts[j])) {
        out.push(opts[j]);
        break;
      }
    }
  }
  return out;
}
function optionSelected(o) {
  var sel = selectOf(o);
  if (!sel) return optionSelectedRaw(o);
  return selectedOptions(sel).indexOf(o) >= 0;
}
function setOptionSelected(o, on) {
  var sel = selectOf(o);
  if (on && sel && !selectMultiple(sel)) {
    var opts = selectOptions(sel);
    for (var i = 0; i < opts.length; i++) opts[i].sel = opts[i] === o;
  } else {
    o.sel = !!on;
  }
  o.d.ver++;
}
function optionValue(o) {
  var v = getAttrRaw(o, 'value');
  return v !== null ? v : collapseWS(textOf(o));
}
function optionText(o) {
  var out = '';
  for (var k = o.f; k; k = nextNode(k, o)) {
    if (k.t === 1 && k.ns === 0 && (k.ln === 'script' || k.ln === 'style')) continue;
    if (k.t === 3 && !(k.p && k.p.t === 1 && (k.p.ln === 'script' || k.p.ln === 'style'))) out += k.x;
  }
  return collapseWS(out);
}

function labelControl(label) {
  var forId = getAttrRaw(label, 'for');
  if (forId !== null) {
    if (!label.c) return null;
    var l = label.d.ids[forId];
    var e = l && l.length ? (l.length === 1 ? l[0] : firstInTreeOrder(l)) : null;
    return e && e.ns === 0 && LABELABLE[e.ln] ? e : null;
  }
  for (var d = nextElement(label, label); d; d = nextElement(d, label)) {
    if (d.ns === 0 && LABELABLE[d.ln] && !(d.ln === 'input' && inputType(d) === 'hidden')) return d;
  }
  return null;
}
function labelsOf(r) {
  var root = treeRoot(r);
  var out = [];
  for (var e = nextElement(root, root); e; e = nextElement(e, root)) {
    if (e.ns === 0 && e.ln === 'label' && labelControl(e) === r) out.push(e);
  }
  return makeStaticList(out);
}

// formDataEntries: the form data set for submission (also what `new FormData(form)` reads).
function formDataEntries(form, submitter) {
  var out = [];
  var els = formElements(form);
  for (var i = 0; i < els.length; i++) {
    var e = els[i];
    if (isDisabledControl(e)) continue;
    var name = getAttrRaw(e, 'name');
    if (e.ln === 'button') {
      if (e !== submitter || !name) continue;
      out.push([name, getAttrRaw(e, 'value') || '']);
      continue;
    }
    if (e.ln === 'input') {
      var type = inputType(e);
      if (type === 'submit' || type === 'image' || type === 'button' || type === 'reset') {
        if (e !== submitter) continue;
        if (type === 'image') {
          var px = name ? name + '.' : '';
          out.push([px + 'x', '0'], [px + 'y', '0']);
          continue;
        }
        if (!name) continue;
        out.push([name, inputValue(e)]);
        continue;
      }
      if (!name) continue;
      if ((type === 'checkbox' || type === 'radio') && !checkedOf(e)) continue;
      if (type === 'file') { out.push([name, '']); continue; }
      out.push([name, inputValue(e)]);
      continue;
    }
    if (!name) continue;
    if (e.ln === 'select') {
      var so = selectedOptions(e);
      for (var j = 0; j < so.length; j++) if (!isDisabledControl(so[j])) out.push([name, optionValue(so[j])]);
    } else if (e.ln === 'textarea') {
      out.push([name, textareaValue(e).replace(/\r?\n/g, '\r\n')]);
    }
  }
  return out;
}
function urlencodeEntries(entries) {
  return entries.map(function (kv) {
    return encodeURIComponent(kv[0]).replace(/%20/g, '+') + '=' + encodeURIComponent(kv[1]).replace(/%20/g, '+');
  }).join('&');
}
function submitForm(form, submitter, fireSubmit) {
  if (form.submitting) return;
  if (fireSubmit) {
    var ev = createEvent(SubmitEvent, 'submit', { bubbles: true, cancelable: true, submitter: submitter ? wrap(submitter) : null });
    eventState(ev).trusted = true;
    if (!dispatchEvent(wrap(form), ev)) return;
  }
  var method = asciiLower((submitter && getAttrRaw(submitter, 'formmethod')) || getAttrRaw(form, 'method') || 'get');
  if (method === 'dialog') return;
  var actionAttr = (submitter && getAttrRaw(submitter, 'formaction')) || getAttrRaw(form, 'action');
  var action = actionAttr ? resolveURL(form.d, actionAttr) : form.d.url;
  if (!action || method !== 'get' || form.d !== mainDoc) return;
  var query = urlencodeEntries(formDataEntries(form, submitter));
  var hash = action.indexOf('#');
  if (hash >= 0) action = action.slice(0, hash);
  var q = action.indexOf('?');
  if (q >= 0) action = action.slice(0, q);
  flagOn(form, 'submitting');
  try { hooks.navigate(action + '?' + query, false); } catch (e) { reportException(e); }
  flagOff(form, 'submitting');
}
function resetForm(form) {
  var ev = createEvent(Event, 'reset', { bubbles: true, cancelable: true });
  eventState(ev).trusted = true;
  if (!dispatchEvent(wrap(form), ev)) return;
  var els = formElements(form);
  for (var i = 0; i < els.length; i++) {
    var e = els[i];
    e.fv = undefined;
    e.fc = undefined;
    if (e.ln === 'select') {
      var opts = selectOptions(e);
      for (var j = 0; j < opts.length; j++) opts[j].sel = undefined;
    }
  }
}

// Shared validation surface for listed elements (always valid: the reader does not validate).
var validationMixin = {
  get willValidate() { return false; },
  get validity() { return makeValidity(); },
  get validationMessage() { return ''; },
  checkValidity: function () { return true; },
  reportValidity: function () { return true; },
  setCustomValidity: function () {},
  get form() { return wrap(formOwner(elementOf(this))); },
  get labels() { return labelsOf(elementOf(this)); }
};

// ---------------------------------------------------------------- interface table

function htmlIface(name, tags, parent) {
  var C = iface(name, parent || HTMLElement);
  if (tags) tags.split(' ').forEach(function (t) { HTML_PROTOS[t] = C.prototype; });
  return C;
}

// Plain HTMLElement tags.
'abbr acronym address article aside b basefont bdi bdo big center cite code dd dfn dt em figcaption figure footer header hgroup i kbd main mark nav nobr noembed noframes noscript plaintext rb rp rt rtc ruby s samp search section small strike strong sub summary sup tt u var wbr'
  .split(' ').forEach(function (t) { HTML_PROTOS[t] = HTMLElement.prototype; });

var HTMLUnknownElement = htmlIface('HTMLUnknownElement', null);
var HTMLHtmlElement = htmlIface('HTMLHtmlElement', 'html');
reflectStr(HTMLHtmlElement.prototype, 'version');
var HTMLHeadElement = htmlIface('HTMLHeadElement', 'head');
var HTMLTitleElement = htmlIface('HTMLTitleElement', 'title');
mix(HTMLTitleElement.prototype, {
  get text() { return childText(elementOf(this)); },
  set text(v) { setTextContent(elementOf(this), str(v)); }
});
var HTMLBaseElement = htmlIface('HTMLBaseElement', 'base');
reflectStr(HTMLBaseElement.prototype, 'target');
mix(HTMLBaseElement.prototype, {
  get href() {
    var r = elementOf(this);
    var v = getAttrRaw(r, 'href');
    if (v === null) return r.d.url;
    var p = parseURLParts(v, r.d.url);
    return p ? p.href : v;
  },
  set href(v) { setAttrRaw(elementOf(this), 'href', str(v)); }
});
var HTMLLinkElement = htmlIface('HTMLLinkElement', 'link');
reflectURL(HTMLLinkElement.prototype, 'href');
reflectStr(HTMLLinkElement.prototype, 'rel as media integrity hreflang type imageSrcset:imagesrcset imageSizes:imagesizes referrerPolicy:referrerpolicy blocking fetchPriority:fetchpriority charset rev target');
reflectNullableStr(HTMLLinkElement.prototype, 'crossOrigin:crossorigin');
reflectBool(HTMLLinkElement.prototype, 'disabled');
mix(HTMLLinkElement.prototype, {
  get relList() { return tokenListOf(elementOf(this), 'rel'); },
  get sizes() { return tokenListOf(elementOf(this), 'sizes'); },
  get sheet() { return sheetOfLink(elementOf(this)); }
});
var HTMLMetaElement = htmlIface('HTMLMetaElement', 'meta');
reflectStr(HTMLMetaElement.prototype, 'name httpEquiv:http-equiv content media scheme');
var HTMLStyleElement = htmlIface('HTMLStyleElement', 'style');
reflectStr(HTMLStyleElement.prototype, 'media type blocking');
reflectBool(HTMLStyleElement.prototype, 'disabled');
mix(HTMLStyleElement.prototype, { get sheet() { return sheetOfStyle(elementOf(this)); } });
var HTMLBodyElement = htmlIface('HTMLBodyElement', 'body');
reflectStr(HTMLBodyElement.prototype, 'text link vLink:vlink aLink:alink bgColor:bgcolor background');
var HTMLDivElement = htmlIface('HTMLDivElement', 'div');
reflectStr(HTMLDivElement.prototype, 'align');
var HTMLParagraphElement = htmlIface('HTMLParagraphElement', 'p');
reflectStr(HTMLParagraphElement.prototype, 'align');
var HTMLHeadingElement = htmlIface('HTMLHeadingElement', 'h1 h2 h3 h4 h5 h6');
reflectStr(HTMLHeadingElement.prototype, 'align');
var HTMLSpanElement = htmlIface('HTMLSpanElement', 'span');
var HTMLBRElement = htmlIface('HTMLBRElement', 'br');
reflectStr(HTMLBRElement.prototype, 'clear');
var HTMLHRElement = htmlIface('HTMLHRElement', 'hr');
reflectStr(HTMLHRElement.prototype, 'align color size width');
reflectBool(HTMLHRElement.prototype, 'noShade:noshade');
var HTMLPreElement = htmlIface('HTMLPreElement', 'pre listing xmp');
reflectLong(HTMLPreElement.prototype, 'width=0');
var HTMLQuoteElement = htmlIface('HTMLQuoteElement', 'q blockquote');
reflectURL(HTMLQuoteElement.prototype, 'cite');
var HTMLModElement = htmlIface('HTMLModElement', 'ins del');
reflectURL(HTMLModElement.prototype, 'cite');
reflectStr(HTMLModElement.prototype, 'dateTime:datetime');
var HTMLOListElement = htmlIface('HTMLOListElement', 'ol');
reflectBool(HTMLOListElement.prototype, 'reversed compact');
reflectLong(HTMLOListElement.prototype, 'start=1');
reflectStr(HTMLOListElement.prototype, 'type');
var HTMLUListElement = htmlIface('HTMLUListElement', 'ul');
reflectBool(HTMLUListElement.prototype, 'compact');
reflectStr(HTMLUListElement.prototype, 'type');
var HTMLMenuElement = htmlIface('HTMLMenuElement', 'menu');
reflectBool(HTMLMenuElement.prototype, 'compact');
var HTMLDirectoryElement = htmlIface('HTMLDirectoryElement', 'dir');
reflectBool(HTMLDirectoryElement.prototype, 'compact');
var HTMLLIElement = htmlIface('HTMLLIElement', 'li');
reflectLong(HTMLLIElement.prototype, 'value=0');
reflectStr(HTMLLIElement.prototype, 'type');
var HTMLDListElement = htmlIface('HTMLDListElement', 'dl');
reflectBool(HTMLDListElement.prototype, 'compact');
var HTMLAnchorElement = htmlIface('HTMLAnchorElement', 'a');
var HTMLAreaElement = htmlIface('HTMLAreaElement', 'area');
var HTMLDataElement = htmlIface('HTMLDataElement', 'data');
reflectStr(HTMLDataElement.prototype, 'value');
var HTMLTimeElement = htmlIface('HTMLTimeElement', 'time');
reflectStr(HTMLTimeElement.prototype, 'dateTime:datetime');
var HTMLPictureElement = htmlIface('HTMLPictureElement', 'picture');
var HTMLSourceElement = htmlIface('HTMLSourceElement', 'source');
reflectURL(HTMLSourceElement.prototype, 'src');
reflectStr(HTMLSourceElement.prototype, 'type srcset sizes media');
reflectLong(HTMLSourceElement.prototype, 'width=0 height=0', true);
var HTMLImageElement = htmlIface('HTMLImageElement', 'img image');
var HTMLIFrameElement = htmlIface('HTMLIFrameElement', 'iframe');
var HTMLEmbedElement = htmlIface('HTMLEmbedElement', 'embed');
reflectURL(HTMLEmbedElement.prototype, 'src');
reflectStr(HTMLEmbedElement.prototype, 'type width height align name');
mix(HTMLEmbedElement.prototype, { getSVGDocument: function () { return null; } });
var HTMLObjectElement = htmlIface('HTMLObjectElement', 'object');
reflectURL(HTMLObjectElement.prototype, 'data');
reflectStr(HTMLObjectElement.prototype, 'type name useMap:usemap width height align archive code border codeBase:codebase codeType:codetype standby');
mix(HTMLObjectElement.prototype, validationMixin);
mix(HTMLObjectElement.prototype, {
  get contentDocument() { return null; },
  get contentWindow() { return null; },
  getSVGDocument: function () { return null; }
});
var HTMLParamElement = htmlIface('HTMLParamElement', 'param');
reflectStr(HTMLParamElement.prototype, 'name value type valueType:valuetype');
var HTMLMapElement = htmlIface('HTMLMapElement', 'map');
reflectStr(HTMLMapElement.prototype, 'name');
mix(HTMLMapElement.prototype, {
  get areas() { return filteredCollection(elementOf(this), 'areas', function (e) { return e.ln === 'area' && e.ns === 0; }, DEPS_TREE); }
});
var HTMLFontElement = htmlIface('HTMLFontElement', 'font');
reflectStr(HTMLFontElement.prototype, 'color face size');
var HTMLMarqueeElement = htmlIface('HTMLMarqueeElement', 'marquee');
mix(HTMLMarqueeElement.prototype, { start: function () {}, stop: function () {} });
var HTMLFrameSetElement = htmlIface('HTMLFrameSetElement', 'frameset');
reflectStr(HTMLFrameSetElement.prototype, 'cols rows');
var HTMLFrameElement = htmlIface('HTMLFrameElement', 'frame');
reflectStr(HTMLFrameElement.prototype, 'name scrolling frameBorder:frameborder marginHeight:marginheight marginWidth:marginwidth');
reflectURL(HTMLFrameElement.prototype, 'src longDesc:longdesc');
reflectBool(HTMLFrameElement.prototype, 'noResize:noresize');
var HTMLCanvasElement = htmlIface('HTMLCanvasElement', 'canvas');
reflectLong(HTMLCanvasElement.prototype, 'width=300 height=150', true);
mix(HTMLCanvasElement.prototype, {
  getContext: function () { return null; },
  toDataURL: function () { return 'data:,'; },
  toBlob: function (cb) { if (typeof cb === 'function') hooks.queueTask(function () { cb(null); }); },
  transferControlToOffscreen: function () { throw domErr('InvalidStateError', 'Canvas is not supported.'); },
  captureStream: function () { throw domErr('NotSupportedError', 'Canvas is not supported.'); }
});
var HTMLTemplateElement = htmlIface('HTMLTemplateElement', 'template');
reflectStr(HTMLTemplateElement.prototype, 'shadowRootMode:shadowrootmode');
reflectBool(HTMLTemplateElement.prototype, 'shadowRootDelegatesFocus:shadowrootdelegatesfocus shadowRootClonable:shadowrootclonable shadowRootSerializable:shadowrootserializable');
mix(HTMLTemplateElement.prototype, {
  get content() {
    var r = elementOf(this);
    if (!r.tc) {
      r.tc = newFragmentRec(templateDoc(r.d));
      r.tc.host = r;
    }
    return wrap(r.tc);
  }
});
var HTMLSlotElement = htmlIface('HTMLSlotElement', 'slot');
reflectStr(HTMLSlotElement.prototype, 'name');
var HTMLDetailsElement = htmlIface('HTMLDetailsElement', 'details');
reflectBool(HTMLDetailsElement.prototype, 'open');
reflectStr(HTMLDetailsElement.prototype, 'name');
var HTMLDialogElement = htmlIface('HTMLDialogElement', 'dialog');
reflectBool(HTMLDialogElement.prototype, 'open');
mix(HTMLDialogElement.prototype, {
  get returnValue() { var r = elementOf(this); return r.rv === undefined ? '' : r.rv; },
  set returnValue(v) { elementOf(this).rv = str(v); },
  show: function () { setAttrRaw(elementOf(this), 'open', ''); },
  showModal: function () { setAttrRaw(elementOf(this), 'open', ''); },
  close: function (rv) {
    var r = elementOf(this);
    if (getAttrRaw(r, 'open') === null) return;
    removeAttrRaw(r, 'open');
    if (rv !== undefined) r.rv = str(rv);
    hooks.queueTask(function () { fireEvent(wrap(r), 'close', { bubbles: false }); });
  },
  requestClose: function (rv) { this.close(rv); }
});
var HTMLDataListElement = htmlIface('HTMLDataListElement', 'datalist');
mix(HTMLDataListElement.prototype, {
  get options() { return filteredCollection(elementOf(this), 'options', function (e) { return e.ln === 'option' && e.ns === 0; }, DEPS_TREE); }
});
var HTMLTableElement = htmlIface('HTMLTableElement', 'table');
var HTMLTableCaptionElement = htmlIface('HTMLTableCaptionElement', 'caption');
reflectStr(HTMLTableCaptionElement.prototype, 'align');
var HTMLTableSectionElement = htmlIface('HTMLTableSectionElement', 'thead tbody tfoot');
var HTMLTableRowElement = htmlIface('HTMLTableRowElement', 'tr');
var HTMLTableCellElement = htmlIface('HTMLTableCellElement', 'td th');
var HTMLTableColElement = htmlIface('HTMLTableColElement', 'col colgroup');
reflectLong(HTMLTableColElement.prototype, 'span=1', true);
reflectStr(HTMLTableColElement.prototype, 'align ch:char chOff:charoff vAlign:valign width');
var HTMLFormElement = htmlIface('HTMLFormElement', 'form');
var HTMLInputElement = htmlIface('HTMLInputElement', 'input');
var HTMLButtonElement = htmlIface('HTMLButtonElement', 'button');
var HTMLSelectElement = htmlIface('HTMLSelectElement', 'select');
var HTMLOptionElement = htmlIface('HTMLOptionElement', 'option');
var HTMLOptGroupElement = htmlIface('HTMLOptGroupElement', 'optgroup');
reflectBool(HTMLOptGroupElement.prototype, 'disabled');
reflectStr(HTMLOptGroupElement.prototype, 'label');
var HTMLTextAreaElement = htmlIface('HTMLTextAreaElement', 'textarea');
var HTMLLabelElement = htmlIface('HTMLLabelElement', 'label');
var HTMLFieldSetElement = htmlIface('HTMLFieldSetElement', 'fieldset');
var HTMLLegendElement = htmlIface('HTMLLegendElement', 'legend');
reflectStr(HTMLLegendElement.prototype, 'align');
mix(HTMLLegendElement.prototype, {
  get form() { var r = elementOf(this); return r.p && r.p.t === 1 && r.p.ln === 'fieldset' ? wrap(formOwner(r.p)) : null; }
});
var HTMLOutputElement = htmlIface('HTMLOutputElement', 'output');
var HTMLProgressElement = htmlIface('HTMLProgressElement', 'progress');
var HTMLMeterElement = htmlIface('HTMLMeterElement', 'meter');
var HTMLScriptElement = htmlIface('HTMLScriptElement', 'script');
var HTMLMediaElement = htmlIface('HTMLMediaElement', null);
var HTMLVideoElement = htmlIface('HTMLVideoElement', 'video', HTMLMediaElement);
var HTMLAudioElement = htmlIface('HTMLAudioElement', 'audio', HTMLMediaElement);
var HTMLTrackElement = htmlIface('HTMLTrackElement', 'track');
reflectStr(HTMLTrackElement.prototype, 'kind srclang label');
reflectURL(HTMLTrackElement.prototype, 'src');
reflectBool(HTMLTrackElement.prototype, 'default');
mix(HTMLTrackElement.prototype, { get readyState() { return 0; }, get track() { return null; } });

// ---- hyperlinks (a, area)
function hyperlinkParts(r) {
  var v = getAttrRaw(r, 'href');
  if (v === null) return null;
  return parseURLParts(v, documentBaseURL(r.d));
}
function setHyperlinkPart(r, part, value) {
  var p = hyperlinkParts(r);
  if (!p) return;
  var proto = p.protocol, user = p.username, pass = p.password, host = p.host, path = p.pathname, search = p.search, hash = p.hash;
  value = str(value);
  switch (part) {
    case 'protocol': proto = value.replace(/:.*$/, '') + ':'; break;
    case 'username': user = encodeURIComponent(value); break;
    case 'password': pass = encodeURIComponent(value); break;
    case 'host': host = value; break;
    case 'hostname': host = value + (p.port ? ':' + p.port : ''); break;
    case 'port': host = p.hostname + (value ? ':' + value : ''); break;
    case 'pathname': path = value.charAt(0) === '/' ? value : '/' + value; break;
    case 'search': search = value === '' ? '' : (value.charAt(0) === '?' ? value : '?' + value); break;
    case 'hash': hash = value === '' ? '' : (value.charAt(0) === '#' ? value : '#' + value); break;
  }
  var auth = user ? user + (pass ? ':' + pass : '') + '@' : '';
  var href = proto + (host || /^(https?|ftp|wss?|file):$/.test(proto) ? '//' + auth + host : '') + path + search + hash;
  var check = parseURLParts(href);
  setAttrRaw(r, 'href', check ? check.href : href);
}
var hyperlinkMixin = {
  get href() {
    var r = elementOf(this);
    var v = getAttrRaw(r, 'href');
    if (v === null) return '';
    var p = parseURLParts(v, documentBaseURL(r.d));
    return p ? p.href : v;
  },
  set href(v) { setAttrRaw(elementOf(this), 'href', str(v)); },
  toString: function () { return this.href; },
  get origin() { var p = hyperlinkParts(elementOf(this)); return p ? (p.origin || '') : ''; },
  get protocol() { var p = hyperlinkParts(elementOf(this)); return p ? p.protocol : ':'; },
  set protocol(v) { setHyperlinkPart(elementOf(this), 'protocol', v); },
  get username() { var p = hyperlinkParts(elementOf(this)); return p ? p.username : ''; },
  set username(v) { setHyperlinkPart(elementOf(this), 'username', v); },
  get password() { var p = hyperlinkParts(elementOf(this)); return p ? p.password : ''; },
  set password(v) { setHyperlinkPart(elementOf(this), 'password', v); },
  get host() { var p = hyperlinkParts(elementOf(this)); return p ? p.host : ''; },
  set host(v) { setHyperlinkPart(elementOf(this), 'host', v); },
  get hostname() { var p = hyperlinkParts(elementOf(this)); return p ? p.hostname : ''; },
  set hostname(v) { setHyperlinkPart(elementOf(this), 'hostname', v); },
  get port() { var p = hyperlinkParts(elementOf(this)); return p ? p.port : ''; },
  set port(v) { setHyperlinkPart(elementOf(this), 'port', v); },
  get pathname() { var p = hyperlinkParts(elementOf(this)); return p ? p.pathname : ''; },
  set pathname(v) { setHyperlinkPart(elementOf(this), 'pathname', v); },
  get search() { var p = hyperlinkParts(elementOf(this)); return p && p.search !== '?' ? p.search : ''; },
  set search(v) { setHyperlinkPart(elementOf(this), 'search', v); },
  get hash() { var p = hyperlinkParts(elementOf(this)); return p && p.hash !== '#' ? p.hash : ''; },
  set hash(v) { setHyperlinkPart(elementOf(this), 'hash', v); },
  get relList() { return tokenListOf(elementOf(this), 'rel'); }
};
mix(HTMLAnchorElement.prototype, hyperlinkMixin);
mix(HTMLAreaElement.prototype, hyperlinkMixin);
reflectStr(HTMLAnchorElement.prototype, 'target download ping rel hreflang type referrerPolicy:referrerpolicy name coords shape charset rev');
reflectStr(HTMLAreaElement.prototype, 'alt coords shape target download ping rel referrerPolicy:referrerpolicy');
reflectBool(HTMLAreaElement.prototype, 'noHref:nohref');
mix(HTMLAnchorElement.prototype, {
  get text() { return textOf(elementOf(this)); },
  set text(v) { setTextContent(elementOf(this), str(v)); }
});

// ---- images
reflectURL(HTMLImageElement.prototype, 'src lowsrc longDesc:longdesc');
reflectStr(HTMLImageElement.prototype, 'alt srcset sizes useMap:usemap referrerPolicy:referrerpolicy decoding loading fetchPriority:fetchpriority name align border');
reflectNullableStr(HTMLImageElement.prototype, 'crossOrigin:crossorigin');
reflectBool(HTMLImageElement.prototype, 'isMap:ismap');
reflectLong(HTMLImageElement.prototype, 'hspace=0 vspace=0', true);
mix(HTMLImageElement.prototype, {
  get width() { var r = elementOf(this); var b = numericAttr(r, 'width'); return b || (r.c && elementRendered(r) ? 300 : 0); },
  set width(v) { setAttrRaw(elementOf(this), 'width', String(Number(v) >>> 0)); },
  get height() { var r = elementOf(this); var b = numericAttr(r, 'height'); return b || (r.c && elementRendered(r) ? 150 : 0); },
  set height(v) { setAttrRaw(elementOf(this), 'height', String(Number(v) >>> 0)); },
  // The reader never decodes images, but `complete` is true and `load` fires, so a loaded-looking image has to
  // have a size too: gallery and lazy-load code hides images whose naturalWidth is 0 ("broken") or divides by
  // it for an aspect ratio. The width/height attributes when given, else the 300×150 default replaced size.
  get naturalWidth() { var r = elementOf(this); return numericAttr(r, 'width') || (imgHasSource(r) ? 300 : 0); },
  get naturalHeight() { var r = elementOf(this); return numericAttr(r, 'height') || (imgHasSource(r) ? 150 : 0); },
  get complete() { return true; },
  get currentSrc() {
    var r = elementOf(this);
    var v = getAttrRaw(r, 'src');
    return v === null ? '' : resolveURL(r.d, v) || v;
  },
  get x() { return 0; },
  get y() { return 0; },
  decode: function () { return Promise.resolve(); }
});

// ---- iframes: no nested browsing context; contentWindow is a small stand-in so postMessage and
// document.write on it do not throw.
reflectURL(HTMLIFrameElement.prototype, 'src longDesc:longdesc');
reflectStr(HTMLIFrameElement.prototype, 'srcdoc name allow width height referrerPolicy:referrerpolicy loading align scrolling frameBorder:frameborder marginHeight:marginheight marginWidth:marginwidth');
reflectBool(HTMLIFrameElement.prototype, 'allowFullscreen:allowfullscreen');
function iframeWindow(r) {
  if (!r.iw) {
    var d = newDocRec('about:blank');
    var html = createElementRec(d, 'html', 0);
    attachBuilt(d, html);
    attachBuilt(html, createElementRec(d, 'head', 0));
    attachBuilt(html, createElementRec(d, 'body', 0));
    var w = new EventTarget();
    w.document = wrap(d);
    w.window = w;
    w.self = w;
    w.parent = global;
    w.top = global;
    w.frameElement = wrap(r);
    w.location = { href: 'about:blank', origin: 'null', toString: function () { return 'about:blank'; }, replace: function () {}, assign: function () {}, reload: function () {} };
    w.postMessage = function () {};
    w.close = function () {};
    w.focus = function () {};
    w.closed = false;
    w.length = 0;
    r.iw = { win: w, doc: d };
  }
  return r.iw;
}
mix(HTMLIFrameElement.prototype, {
  get sandbox() { return tokenListOf(elementOf(this), 'sandbox'); },
  get contentWindow() { var r = elementOf(this); return r.c ? iframeWindow(r).win : null; },
  get contentDocument() { var r = elementOf(this); return r.c ? wrap(iframeWindow(r).doc) : null; },
  getSVGDocument: function () { return null; }
});

// ---- media
reflectURL(HTMLMediaElement.prototype, 'src');
reflectNullableStr(HTMLMediaElement.prototype, 'crossOrigin:crossorigin');
reflectStr(HTMLMediaElement.prototype, 'preload');
reflectBool(HTMLMediaElement.prototype, 'autoplay loop controls defaultMuted:muted');
constants(HTMLMediaElement, { NETWORK_EMPTY: 0, NETWORK_IDLE: 1, NETWORK_LOADING: 2, NETWORK_NO_SOURCE: 3, HAVE_NOTHING: 0, HAVE_METADATA: 1, HAVE_CURRENT_DATA: 2, HAVE_FUTURE_DATA: 3, HAVE_ENOUGH_DATA: 4 });
function timeRanges() { return { length: 0, start: function () { throw domErr('IndexSizeError', 'Empty range.'); }, end: function () { throw domErr('IndexSizeError', 'Empty range.'); } }; }
function emptyTrackList() {
  var l = new EventTarget();
  l.length = 0;
  l.getTrackById = function () { return null; };
  l[Symbol.iterator] = function () { return [][Symbol.iterator](); };
  return l;
}
mix(HTMLMediaElement.prototype, {
  get currentSrc() { var r = elementOf(this); var v = getAttrRaw(r, 'src'); return v === null ? '' : resolveURL(r.d, v) || v; },
  get networkState() { return 0; },
  get readyState() { return 0; },
  get buffered() { return timeRanges(); },
  get played() { return timeRanges(); },
  get seekable() { return timeRanges(); },
  get seeking() { return false; },
  get currentTime() { return elementOf(this).mt || 0; },
  set currentTime(v) { elementOf(this).mt = +v || 0; },
  get duration() { return NaN; },
  get paused() { return true; },
  get ended() { return false; },
  get error() { return null; },
  get defaultPlaybackRate() { return 1; }, set defaultPlaybackRate(v) {},
  get playbackRate() { return 1; }, set playbackRate(v) {},
  get preservesPitch() { return true; }, set preservesPitch(v) {},
  get volume() { var r = elementOf(this); return r.vol === undefined ? 1 : r.vol; },
  set volume(v) { elementOf(this).vol = Math.max(0, Math.min(1, +v || 0)); },
  get muted() { var r = elementOf(this); return r.mu !== undefined ? r.mu : getAttrRaw(r, 'muted') !== null; },
  set muted(v) { elementOf(this).mu = !!v; },
  get srcObject() { return null; }, set srcObject(v) {},
  get audioTracks() { return emptyTrackList(); },
  get videoTracks() { return emptyTrackList(); },
  get textTracks() { return emptyTrackList(); },
  load: function () {},
  play: function () { return Promise.reject(domErr('NotAllowedError', 'Media playback is not available in this reader.')); },
  pause: function () {},
  fastSeek: function () {},
  canPlayType: function () { return ''; },
  addTextTrack: function () { return { mode: 'disabled', cues: [], addCue: function () {}, removeCue: function () {} }; },
  getStartDate: function () { return new Date(NaN); },
  setSinkId: function () { return Promise.resolve(); },
  captureStream: function () { throw domErr('NotSupportedError', 'Not supported.'); }
});
reflectURL(HTMLVideoElement.prototype, 'poster');
reflectBool(HTMLVideoElement.prototype, 'playsInline:playsinline disablePictureInPicture:disablepictureinpicture');
reflectLong(HTMLVideoElement.prototype, 'width=0 height=0', true);
mix(HTMLVideoElement.prototype, {
  get videoWidth() { return 0; },
  get videoHeight() { return 0; },
  getVideoPlaybackQuality: function () { return { creationTime: 0, droppedVideoFrames: 0, totalVideoFrames: 0 }; },
  requestPictureInPicture: function () { return Promise.reject(domErr('NotSupportedError', 'Not supported.')); }
});

// ---- tables
function tableSections(t) {
  var out = [];
  for (var k = t.f; k; k = k.n) if (k.t === 1 && k.ns === 0 && (k.ln === 'thead' || k.ln === 'tbody' || k.ln === 'tfoot')) out.push(k);
  return out;
}
function tableRows(t) {
  var head = [], body = [], foot = [];
  for (var k = t.f; k; k = k.n) {
    if (k.t !== 1 || k.ns !== 0) continue;
    if (k.ln === 'tr') body.push(k);
    else if (k.ln === 'thead' || k.ln === 'tbody' || k.ln === 'tfoot') {
      var target = k.ln === 'thead' ? head : k.ln === 'tfoot' ? foot : body;
      for (var r = k.f; r; r = r.n) if (r.t === 1 && r.ln === 'tr' && r.ns === 0) target.push(r);
    }
  }
  // Rows directly in the table and in tbody keep document order relative to each other.
  return head.concat(body.sort(function (a, b) { return precedes(a, b) ? -1 : 1; }), foot);
}
function childOfType(t, ln) {
  for (var k = t.f; k; k = k.n) if (k.t === 1 && k.ln === ln && k.ns === 0) return k;
  return null;
}
function insertRowAt(container, rows, index, beforeFallback) {
  index = index === undefined ? -1 : Number(index) | 0;
  if (index < -1 || index > rows.length) throw domErr('IndexSizeError', 'The index provided (' + index + ') is outside the range [-1, ' + rows.length + '].');
  var tr = createElementRec(container.d, 'tr', 0);
  if (index === -1 || index === rows.length) {
    var parent = rows.length ? rows[rows.length - 1].p : container;
    insertNode(beforeFallback && !rows.length ? beforeFallback : parent, tr, null);
  } else {
    insertNode(rows[index].p, tr, rows[index]);
  }
  return wrap(tr);
}
reflectStr(HTMLTableElement.prototype, 'align border frame rules summary width bgColor:bgcolor cellPadding:cellpadding cellSpacing:cellspacing');
mix(HTMLTableElement.prototype, {
  get caption() { return wrap(childOfType(elementOf(this), 'caption')); },
  set caption(v) {
    var t = elementOf(this);
    var old = childOfType(t, 'caption');
    if (old) removeRaw(old, false);
    var c = unwrap(v);
    if (c) insertNode(t, c, t.f);
  },
  get tHead() { return wrap(childOfType(elementOf(this), 'thead')); },
  set tHead(v) {
    var t = elementOf(this);
    var old = childOfType(t, 'thead');
    if (old) removeRaw(old, false);
    var h = unwrap(v);
    if (h) {
      var ref = t.f;
      while (ref && ref.t === 1 && (ref.ln === 'caption' || ref.ln === 'colgroup')) ref = ref.n;
      insertNode(t, h, ref);
    }
  },
  get tFoot() { return wrap(childOfType(elementOf(this), 'tfoot')); },
  set tFoot(v) {
    var t = elementOf(this);
    var old = childOfType(t, 'tfoot');
    if (old) removeRaw(old, false);
    var f = unwrap(v);
    if (f) insertNode(t, f, null);
  },
  get tBodies() { return filteredCollection(elementOf(this), 'tbodies', function (e, s) { return e.ln === 'tbody' && e.p === s.root; }, DEPS_TREE); },
  get rows() {
    var t = elementOf(this);
    return cachedLive(t, 'rows', HTMLCollection.prototype, { kind: 3, root: t, named: true, arr: null, test: null, dyn: tableRows });
  },
  createCaption: function () {
    var t = elementOf(this);
    var c = childOfType(t, 'caption');
    if (!c) {
      c = createElementRec(t.d, 'caption', 0);
      insertNode(t, c, t.f);
    }
    return wrap(c);
  },
  deleteCaption: function () { var c = childOfType(elementOf(this), 'caption'); if (c) removeRaw(c, false); },
  createTHead: function () {
    var t = elementOf(this);
    var h = childOfType(t, 'thead');
    if (!h) {
      h = createElementRec(t.d, 'thead', 0);
      var ref = t.f;
      while (ref && ref.t === 1 && (ref.ln === 'caption' || ref.ln === 'colgroup')) ref = ref.n;
      insertNode(t, h, ref);
    }
    return wrap(h);
  },
  deleteTHead: function () { var h = childOfType(elementOf(this), 'thead'); if (h) removeRaw(h, false); },
  createTFoot: function () {
    var t = elementOf(this);
    var f = childOfType(t, 'tfoot');
    if (!f) {
      f = createElementRec(t.d, 'tfoot', 0);
      insertNode(t, f, null);
    }
    return wrap(f);
  },
  deleteTFoot: function () { var f = childOfType(elementOf(this), 'tfoot'); if (f) removeRaw(f, false); },
  createTBody: function () {
    var t = elementOf(this);
    var b = createElementRec(t.d, 'tbody', 0);
    var last = null;
    for (var k = t.f; k; k = k.n) if (k.t === 1 && k.ln === 'tbody') last = k;
    insertNode(t, b, last ? last.n : null);
    return wrap(b);
  },
  insertRow: function (index) {
    var t = elementOf(this);
    var rows = tableRows(t);
    index = index === undefined ? -1 : Number(index) | 0;
    if (index < -1 || index > rows.length) throw domErr('IndexSizeError', 'The index provided (' + index + ') is outside the range [-1, ' + rows.length + '].');
    var tr = createElementRec(t.d, 'tr', 0);
    if (!rows.length) {
      var bodies = tableSections(t).filter(function (s) { return s.ln === 'tbody'; });
      if (bodies.length) insertNode(bodies[bodies.length - 1], tr, null);
      else {
        var tb = createElementRec(t.d, 'tbody', 0);
        insertNode(t, tb, null);
        insertNode(tb, tr, null);
      }
    } else if (index === -1 || index === rows.length) {
      insertNode(rows[rows.length - 1].p, tr, null);
    } else {
      insertNode(rows[index].p, tr, rows[index]);
    }
    return wrap(tr);
  },
  deleteRow: function (index) {
    var t = elementOf(this);
    var rows = tableRows(t);
    index = Number(index) | 0;
    if (index === -1) index = rows.length - 1;
    if (index < 0 || index >= rows.length) {
      if (index === -1 || (index === rows.length - 1 && !rows.length)) return;
      throw domErr('IndexSizeError', 'The index provided is outside the range.');
    }
    removeRaw(rows[index], false);
  }
});
reflectStr(HTMLTableSectionElement.prototype, 'align ch:char chOff:charoff vAlign:valign');
mix(HTMLTableSectionElement.prototype, {
  get rows() { return filteredCollection(elementOf(this), 'rows', function (e, s) { return e.ln === 'tr' && e.p === s.root; }, DEPS_TREE); },
  insertRow: function (index) {
    var s = elementOf(this);
    var rows = elementChildren(s).filter(function (e) { return e.ln === 'tr'; });
    return insertRowAt(s, rows, index, null);
  },
  deleteRow: function (index) {
    var s = elementOf(this);
    var rows = elementChildren(s).filter(function (e) { return e.ln === 'tr'; });
    index = Number(index) | 0;
    if (index === -1) index = rows.length - 1;
    if (index < 0 || index >= rows.length) {
      if (!rows.length) return;
      throw domErr('IndexSizeError', 'The index provided is outside the range.');
    }
    removeRaw(rows[index], false);
  }
});
reflectStr(HTMLTableRowElement.prototype, 'align ch:char chOff:charoff vAlign:valign bgColor:bgcolor');
mix(HTMLTableRowElement.prototype, {
  get cells() { return filteredCollection(elementOf(this), 'cells', function (e, s) { return (e.ln === 'td' || e.ln === 'th') && e.p === s.root; }, DEPS_TREE); },
  get rowIndex() {
    var r = elementOf(this);
    var t = r.p;
    if (t && t.t === 1 && (t.ln === 'thead' || t.ln === 'tbody' || t.ln === 'tfoot')) t = t.p;
    if (!t || t.t !== 1 || t.ln !== 'table') return -1;
    return tableRows(t).indexOf(r);
  },
  get sectionRowIndex() {
    var r = elementOf(this);
    if (!r.p) return -1;
    var i = 0;
    for (var k = r.p.f; k; k = k.n) {
      if (k === r) return i;
      if (k.t === 1 && k.ln === 'tr') i++;
    }
    return -1;
  },
  insertCell: function (index) {
    var r = elementOf(this);
    var cells = elementChildren(r).filter(function (e) { return e.ln === 'td' || e.ln === 'th'; });
    index = index === undefined ? -1 : Number(index) | 0;
    if (index < -1 || index > cells.length) throw domErr('IndexSizeError', 'The index provided is outside the range.');
    var td = createElementRec(r.d, 'td', 0);
    insertNode(r, td, index === -1 || index === cells.length ? null : cells[index]);
    return wrap(td);
  },
  deleteCell: function (index) {
    var r = elementOf(this);
    var cells = elementChildren(r).filter(function (e) { return e.ln === 'td' || e.ln === 'th'; });
    index = Number(index) | 0;
    if (index === -1) index = cells.length - 1;
    if (index < 0 || index >= cells.length) {
      if (!cells.length) return;
      throw domErr('IndexSizeError', 'The index provided is outside the range.');
    }
    removeRaw(cells[index], false);
  }
});
reflectLong(HTMLTableCellElement.prototype, 'colSpan:colspan=1 rowSpan:rowspan=1', true);
reflectStr(HTMLTableCellElement.prototype, 'headers abbr align axis height width ch:char chOff:charoff vAlign:valign bgColor:bgcolor');
reflectBool(HTMLTableCellElement.prototype, 'noWrap:nowrap');
mix(HTMLTableCellElement.prototype, {
  get cellIndex() {
    var r = elementOf(this);
    if (!r.p || r.p.ln !== 'tr') return -1;
    var i = 0;
    for (var k = r.p.f; k; k = k.n) {
      if (k === r) return i;
      if (k.t === 1 && (k.ln === 'td' || k.ln === 'th')) i++;
    }
    return -1;
  },
  get scope() {
    var v = getAttrRaw(elementOf(this), 'scope');
    v = v === null ? '' : asciiLower(v);
    return v === 'row' || v === 'col' || v === 'rowgroup' || v === 'colgroup' ? v : '';
  },
  set scope(v) { setAttrRaw(elementOf(this), 'scope', str(v)); }
});

// ---- forms
reflectStr(HTMLFormElement.prototype, 'acceptCharset:accept-charset autocomplete name target rel');
reflectBool(HTMLFormElement.prototype, 'noValidate:novalidate');
reflectEnum(HTMLFormElement.prototype, 'method', 'method', ['get', 'post', 'dialog'], 'get', 'get');
reflectEnum(HTMLFormElement.prototype, 'enctype', 'enctype', ['application/x-www-form-urlencoded', 'multipart/form-data', 'text/plain'], 'application/x-www-form-urlencoded', 'application/x-www-form-urlencoded');
reflectEnum(HTMLFormElement.prototype, 'encoding', 'enctype', ['application/x-www-form-urlencoded', 'multipart/form-data', 'text/plain'], 'application/x-www-form-urlencoded', 'application/x-www-form-urlencoded');
var HTMLFormControlsCollection = iface('HTMLFormControlsCollection', HTMLCollection);
var HTMLOptionsCollection = iface('HTMLOptionsCollection', HTMLCollection);
var RadioNodeList = iface('RadioNodeList', NodeList);
mix(HTMLFormControlsCollection.prototype, {
  namedItem: function (name) {
    name = str(name);
    var l = listOf(this);
    var hits = [];
    for (var i = 0; i < l.length; i++) if (getAttrRaw(l[i], 'id') === name || getAttrRaw(l[i], 'name') === name) hits.push(l[i]);
    if (!hits.length) return null;
    if (hits.length === 1) return wrap(hits[0]);
    var list = makeStaticList(hits);
    setProto(list, RadioNodeList.prototype);
    return list;
  }
});
defineProperty(RadioNodeList.prototype, 'value', {
  get: function () {
    var l = listOf(this);
    for (var i = 0; i < l.length; i++) if (l[i].ln === 'input' && inputType(l[i]) === 'radio' && checkedOf(l[i])) return inputValue(l[i]);
    return '';
  },
  set: function (v) {
    var l = listOf(this);
    v = str(v);
    for (var i = 0; i < l.length; i++) {
      if (l[i].ln === 'input' && inputType(l[i]) === 'radio' && inputValue(l[i]) === v) {
        setChecked(l[i], true);
        return;
      }
    }
  },
  enumerable: true, configurable: true
});
mix(HTMLFormElement.prototype, {
  get action() {
    var r = elementOf(this);
    var v = getAttrRaw(r, 'action');
    if (v === null || v === '') return r.d.url;
    return resolveURL(r.d, v) || v;
  },
  set action(v) { setAttrRaw(elementOf(this), 'action', str(v)); },
  get elements() {
    var f = elementOf(this);
    return cachedLive(f, 'elements', HTMLFormControlsCollection.prototype, { kind: 3, root: f, named: true, dyn: formElements });
  },
  get length() { return formElements(elementOf(this)).length; },
  get relList() { return tokenListOf(elementOf(this), 'rel'); },
  item: function (i) { var l = formElements(elementOf(this)); i = i >>> 0; return i < l.length ? wrap(l[i]) : null; },
  namedItem: function (name) { return this.elements.namedItem(name); },
  submit: function () { submitForm(elementOf(this), null, false); },
  requestSubmit: function (submitter) {
    var f = elementOf(this);
    var s = submitter === undefined || submitter === null ? null : unwrap(submitter);
    if (s && formOwner(s) !== f) throw domErr('NotFoundError', 'The specified element is not owned by this form element.');
    submitForm(f, s, true);
  },
  reset: function () { resetForm(elementOf(this)); },
  checkValidity: function () { return true; },
  reportValidity: function () { return true; }
});

reflectStr(HTMLInputElement.prototype, 'accept alt autocomplete dirName:dirname formEnctype:formenctype formMethod:formmethod formTarget:formtarget max min name pattern placeholder step');
reflectURL(HTMLInputElement.prototype, 'src formAction:formaction');
reflectBool(HTMLInputElement.prototype, 'defaultChecked:checked disabled formNoValidate:formnovalidate multiple readOnly:readonly required');
reflectLong(HTMLInputElement.prototype, 'maxLength:maxlength=-1 minLength:minlength=-1');
reflectLong(HTMLInputElement.prototype, 'size=20 width=0 height=0', true);
mix(HTMLInputElement.prototype, validationMixin);
mix(HTMLInputElement.prototype, {
  get type() { return inputType(elementOf(this)); },
  set type(v) { setAttrRaw(elementOf(this), 'type', str(v)); },
  get defaultValue() { var v = getAttrRaw(elementOf(this), 'value'); return v === null ? '' : v; },
  set defaultValue(v) { setAttrRaw(elementOf(this), 'value', str(v)); },
  get value() { return inputValue(elementOf(this)); },
  set value(v) { setInputValue(elementOf(this), v === null ? '' : str(v)); },
  get checked() { return checkedOf(elementOf(this)); },
  set checked(v) { setChecked(elementOf(this), !!v); },
  get indeterminate() { return !!elementOf(this).ind; },
  set indeterminate(v) { elementOf(this).ind = !!v; },
  get files() { return objCreate(FileList.prototype); },
  set files(v) {},
  get list() {
    var r = elementOf(this);
    var id = getAttrRaw(r, 'list');
    if (!id || !r.c) return null;
    var l = r.d.ids[id];
    var e = l && l.length ? l[0] : null;
    return e && e.ln === 'datalist' ? wrap(e) : null;
  },
  get valueAsNumber() {
    var r = elementOf(this);
    var t = inputType(r);
    var v = inputValue(r);
    if (t === 'number' || t === 'range') return v === '' ? NaN : +v;
    if (t === 'date' || t === 'datetime-local' || t === 'month' || t === 'week' || t === 'time') {
      var d = Date.parse(t === 'time' ? '1970-01-01T' + v + 'Z' : v + (t === 'date' ? 'T00:00:00Z' : ''));
      return isNaN(d) ? NaN : d;
    }
    return NaN;
  },
  set valueAsNumber(n) { setInputValue(elementOf(this), isNaN(n) ? '' : String(n)); },
  get valueAsDate() {
    var r = elementOf(this);
    var t = inputType(r);
    if (t !== 'date' && t !== 'time' && t !== 'month' && t !== 'week') return null;
    var v = inputValue(r);
    if (!v) return null;
    var d = new Date(t === 'time' ? '1970-01-01T' + v + 'Z' : v + 'T00:00:00Z');
    return isNaN(d.getTime()) ? null : d;
  },
  set valueAsDate(d) {
    if (d === null) { setInputValue(elementOf(this), ''); return; }
    setInputValue(elementOf(this), d.toISOString().slice(0, 10));
  },
  get selectionStart() { var r = elementOf(this); return r.ss === undefined ? inputValue(r).length : r.ss; },
  set selectionStart(v) { elementOf(this).ss = v >>> 0; },
  get selectionEnd() { var r = elementOf(this); return r.se === undefined ? inputValue(r).length : r.se; },
  set selectionEnd(v) { elementOf(this).se = v >>> 0; },
  get selectionDirection() { return 'none'; },
  set selectionDirection(v) {},
  select: function () { var r = elementOf(this); r.ss = 0; r.se = inputValue(r).length; },
  setSelectionRange: function (s, e) { var r = elementOf(this); r.ss = s >>> 0; r.se = e >>> 0; },
  setRangeText: function (text) { setInputValue(elementOf(this), str(text)); },
  stepUp: function (n) {
    var r = elementOf(this);
    var v = +inputValue(r) || 0;
    setInputValue(r, String(v + (+getAttrRaw(r, 'step') || 1) * (n === undefined ? 1 : +n)));
  },
  stepDown: function (n) {
    var r = elementOf(this);
    var v = +inputValue(r) || 0;
    setInputValue(r, String(v - (+getAttrRaw(r, 'step') || 1) * (n === undefined ? 1 : +n)));
  },
  showPicker: function () {}
});

reflectStr(HTMLButtonElement.prototype, 'formEnctype:formenctype formMethod:formmethod formTarget:formtarget name value');
reflectURL(HTMLButtonElement.prototype, 'formAction:formaction');
reflectBool(HTMLButtonElement.prototype, 'disabled formNoValidate:formnovalidate');
mix(HTMLButtonElement.prototype, validationMixin);
mix(HTMLButtonElement.prototype, {
  get type() { return buttonType(elementOf(this)); },
  set type(v) { setAttrRaw(elementOf(this), 'type', str(v)); }
});

reflectStr(HTMLTextAreaElement.prototype, 'autocomplete dirName:dirname name placeholder wrap');
reflectBool(HTMLTextAreaElement.prototype, 'disabled readOnly:readonly required');
reflectLong(HTMLTextAreaElement.prototype, 'maxLength:maxlength=-1 minLength:minlength=-1');
reflectLong(HTMLTextAreaElement.prototype, 'cols=20 rows=2', true);
mix(HTMLTextAreaElement.prototype, validationMixin);
mix(HTMLTextAreaElement.prototype, {
  get type() { return 'textarea'; },
  get defaultValue() { return childText(elementOf(this)); },
  set defaultValue(v) { setTextContent(elementOf(this), str(v)); },
  get value() { return textareaValue(elementOf(this)); },
  set value(v) { var r = elementOf(this); r.fv = v === null || v === undefined ? '' : str(v).replace(/\r\n?/g, '\n'); r.d.ver++; },
  get textLength() { return textareaValue(elementOf(this)).length; },
  get selectionStart() { var r = elementOf(this); return r.ss === undefined ? textareaValue(r).length : r.ss; },
  set selectionStart(v) { elementOf(this).ss = v >>> 0; },
  get selectionEnd() { var r = elementOf(this); return r.se === undefined ? textareaValue(r).length : r.se; },
  set selectionEnd(v) { elementOf(this).se = v >>> 0; },
  get selectionDirection() { return 'none'; },
  set selectionDirection(v) {},
  select: function () {},
  setSelectionRange: function (s, e) { var r = elementOf(this); r.ss = s >>> 0; r.se = e >>> 0; },
  setRangeText: function (text) { var r = elementOf(this); r.fv = str(text); }
});

reflectStr(HTMLSelectElement.prototype, 'autocomplete name');
reflectBool(HTMLSelectElement.prototype, 'disabled multiple required');
reflectLong(HTMLSelectElement.prototype, 'size=0', true);
mix(HTMLSelectElement.prototype, validationMixin);
function addOption(sel, element, before) {
  var e = nodeArg(element, 'add');
  var ref = null;
  if (before !== undefined && before !== null) {
    if (typeof before === 'number') {
      var opts = selectOptions(sel);
      ref = opts[before] || null;
    } else {
      ref = unwrap(before);
    }
  }
  if (ref && ref.p) insertNode(ref.p, e, ref);
  else insertNode(sel, e, null);
}
mix(HTMLSelectElement.prototype, {
  get type() { return selectMultiple(elementOf(this)) ? 'select-multiple' : 'select-one'; },
  get options() {
    var s = elementOf(this);
    return cachedLive(s, 'options', HTMLOptionsCollection.prototype, { kind: 3, root: s, named: true, dyn: selectOptions });
  },
  get length() { return selectOptions(elementOf(this)).length; },
  set length(n) {
    var s = elementOf(this);
    var opts = selectOptions(s);
    n = n >>> 0;
    while (opts.length > n) removeRaw(opts.pop(), false);
    while (opts.length < n) {
      var o = createElementRec(s.d, 'option', 0);
      insertNode(s, o, null);
      opts.push(o);
    }
  },
  get selectedOptions() {
    var s = elementOf(this);
    return cachedLive(s, 'selectedOptions', HTMLCollection.prototype, { kind: 3, root: s, named: true, dyn: selectedOptions });
  },
  get selectedIndex() {
    var s = elementOf(this);
    var so = selectedOptions(s);
    return so.length ? selectOptions(s).indexOf(so[0]) : -1;
  },
  set selectedIndex(i) {
    var s = elementOf(this);
    var opts = selectOptions(s);
    i = Number(i) | 0;
    for (var k = 0; k < opts.length; k++) opts[k].sel = k === i;
    s.d.ver++;
  },
  get value() {
    var so = selectedOptions(elementOf(this));
    return so.length ? optionValue(so[0]) : '';
  },
  set value(v) {
    var s = elementOf(this);
    var opts = selectOptions(s);
    v = str(v);
    var found = false;
    for (var k = 0; k < opts.length; k++) {
      var hit = !found && optionValue(opts[k]) === v;
      opts[k].sel = hit;
      if (hit) found = true;
    }
    s.d.ver++;
  },
  item: function (i) { var o = selectOptions(elementOf(this)); i = i >>> 0; return i < o.length ? wrap(o[i]) : null; },
  namedItem: function (name) {
    name = str(name);
    var o = selectOptions(elementOf(this));
    for (var i = 0; i < o.length; i++) if (getAttrRaw(o[i], 'id') === name || getAttrRaw(o[i], 'name') === name) return wrap(o[i]);
    return null;
  },
  add: function (element, before) { addOption(elementOf(this), element, before); },
  remove: function (index) {
    var s = elementOf(this);
    if (arguments.length === 0) {
      if (s.p) removeRaw(s, false);
      return;
    }
    var o = selectOptions(s)[Number(index) | 0];
    if (o) removeRaw(o, false);
  },
  showPicker: function () {}
});
mix(HTMLOptionsCollection.prototype, {
  get selectedIndex() { var s = listOwner(this); return s ? wrap(s).selectedIndex : -1; },
  set selectedIndex(i) { var s = listOwner(this); if (s) wrap(s).selectedIndex = i; },
  add: function (element, before) { var s = listOwner(this); if (s) addOption(s, element, before); },
  remove: function (index) { var s = listOwner(this); var o = s && selectOptions(s)[Number(index) | 0]; if (o) removeRaw(o, false); }
});
defineProperty(HTMLOptionsCollection.prototype, 'length', {
  get: function () { return listOf(this).length; },
  set: function (n) { var s = listOwner(this); if (s) wrap(s).length = n; },
  enumerable: true, configurable: true
});
function listOwner(o) { var s = o[LIVE]; return s ? s.root : null; }

reflectBool(HTMLOptionElement.prototype, 'disabled defaultSelected:selected');
mix(HTMLOptionElement.prototype, {
  get form() { var s = selectOf(elementOf(this)); return s ? wrap(formOwner(s)) : null; },
  get label() { var r = elementOf(this); var v = getAttrRaw(r, 'label'); return v !== null ? v : optionText(r); },
  set label(v) { setAttrRaw(elementOf(this), 'label', str(v)); },
  get value() { return optionValue(elementOf(this)); },
  set value(v) { setAttrRaw(elementOf(this), 'value', str(v)); },
  get text() { return optionText(elementOf(this)); },
  set text(v) { setTextContent(elementOf(this), str(v)); },
  get selected() { return optionSelected(elementOf(this)); },
  set selected(v) { setOptionSelected(elementOf(this), !!v); },
  get index() {
    var r = elementOf(this);
    var s = selectOf(r);
    return s ? selectOptions(s).indexOf(r) : 0;
  }
});

mix(HTMLLabelElement.prototype, {
  get htmlFor() { var v = getAttrRaw(elementOf(this), 'for'); return v === null ? '' : v; },
  set htmlFor(v) { setAttrRaw(elementOf(this), 'for', str(v)); },
  get control() { return wrap(labelControl(elementOf(this))); },
  get form() { var c = labelControl(elementOf(this)); return c ? wrap(formOwner(c)) : null; }
});

reflectStr(HTMLFieldSetElement.prototype, 'name');
reflectBool(HTMLFieldSetElement.prototype, 'disabled');
mix(HTMLFieldSetElement.prototype, validationMixin);
mix(HTMLFieldSetElement.prototype, {
  get type() { return 'fieldset'; },
  get elements() {
    var f = elementOf(this);
    return cachedLive(f, 'elements', HTMLFormControlsCollection.prototype, {
      kind: 3, root: f, named: true,
      dyn: function (root) {
        var out = [];
        for (var e = nextElement(root, root); e; e = nextElement(e, root)) if (e.ns === 0 && LISTED[e.ln]) out.push(e);
        return out;
      }
    });
  }
});

reflectStr(HTMLOutputElement.prototype, 'name');
mix(HTMLOutputElement.prototype, validationMixin);
mix(HTMLOutputElement.prototype, {
  get type() { return 'output'; },
  get htmlFor() { return tokenListOf(elementOf(this), 'for'); },
  get defaultValue() { var r = elementOf(this); return r.dv !== undefined ? r.dv : textOf(r); },
  set defaultValue(v) { elementOf(this).dv = str(v); },
  get value() { return textOf(elementOf(this)); },
  set value(v) { setTextContent(elementOf(this), str(v)); }
});

function reflectDouble(proto, specs) {
  specs.split(' ').forEach(function (spec) {
    var eq = spec.split('=');
    var def = +eq[1];
    var prop = eq[0];
    defineProperty(proto, prop, {
      get: function () {
        var v = getAttrRaw(R(this), prop);
        var n = v === null ? NaN : parseHTMLFloat(v);
        return isNaN(n) ? def : n;
      },
      set: function (v) { setAttrRaw(R(this), prop, String(+v)); },
      enumerable: true, configurable: true
    });
  });
}
reflectDouble(HTMLProgressElement.prototype, 'value=0 max=1');
mix(HTMLProgressElement.prototype, {
  get position() { var p = this; return p.max > 0 ? Math.min(1, p.value / p.max) : -1; },
  get labels() { return labelsOf(elementOf(this)); }
});
reflectDouble(HTMLMeterElement.prototype, 'value=0 min=0 max=1 low=0 high=1 optimum=0.5');
mix(HTMLMeterElement.prototype, { get labels() { return labelsOf(elementOf(this)); } });

// ---- scripts (execution lives in 85_scripts.js; these are the element's properties)
reflectURL(HTMLScriptElement.prototype, 'src');
reflectStr(HTMLScriptElement.prototype, 'type charset event integrity referrerPolicy:referrerpolicy fetchPriority:fetchpriority blocking htmlFor:for');
reflectNullableStr(HTMLScriptElement.prototype, 'crossOrigin:crossorigin');
reflectBool(HTMLScriptElement.prototype, 'defer noModule:nomodule');
mix(HTMLScriptElement.prototype, {
  get async() { var r = elementOf(this); return !!r.fa || getAttrRaw(r, 'async') !== null; },
  set async(v) {
    var r = elementOf(this);
    r.fa = false;
    if (v) setAttrRaw(r, 'async', ''); else removeAttrRaw(r, 'async');
  },
  get text() { return childText(elementOf(this)); },
  set text(v) { setTextContent(elementOf(this), str(v)); }
});
HTMLScriptElement.supports = function (type) { type = str(type); return type === 'classic' || type === 'module' || type === 'importmap'; };

// ---------------------------------------------------------------- SVG and MathML

var SVGElement = iface('SVGElement', Element);
var SVGGraphicsElement = iface('SVGGraphicsElement', SVGElement);
var SVGGeometryElement = iface('SVGGeometryElement', SVGGraphicsElement);
var SVGSVGElement = iface('SVGSVGElement', SVGGraphicsElement);
SVG_PROTOS.svg = SVGSVGElement.prototype;
function animatedString(r, attr) {
  return {
    get baseVal() { var v = getAttrRaw(r, attr); return v === null ? '' : v; },
    set baseVal(v) { setAttrRaw(r, attr, str(v)); },
    get animVal() { var v = getAttrRaw(r, attr); return v === null ? '' : v; }
  };
}
function animatedLength(r, attr) {
  var val = function () { var v = parseFloat(getAttrRaw(r, attr) || '0'); return isNaN(v) ? 0 : v; };
  return {
    get baseVal() { return { value: val(), valueInSpecifiedUnits: val(), unitType: 1, valueAsString: getAttrRaw(r, attr) || '0' }; },
    get animVal() { return { value: val(), valueInSpecifiedUnits: val(), unitType: 1, valueAsString: getAttrRaw(r, attr) || '0' }; }
  };
}
mix(SVGElement.prototype, {
  get className() { return animatedString(elementOf(this), 'class'); },
  get ownerSVGElement() {
    for (var a = elementOf(this).p; a; a = a.p) if (a.t === 1 && a.ns === 1 && a.ln === 'svg') return wrap(a);
    return null;
  },
  get viewportElement() { return this.ownerSVGElement; },
  get tabIndex() { var v = getAttrRaw(elementOf(this), 'tabindex'); var n = v === null ? NaN : parseHTMLInt(v); return isNaN(n) ? -1 : n; },
  set tabIndex(v) { setAttrRaw(elementOf(this), 'tabindex', String(Number(v) | 0)); },
  focus: function () { focusElement(elementOf(this)); },
  blur: function () { blurElement(elementOf(this)); },
  get nonce() { return ''; }
});
mix(SVGGraphicsElement.prototype, {
  getBBox: function () {
    var r = elementOf(this);
    return new DOMRect(0, 0, numericAttr(r, 'width'), numericAttr(r, 'height'));
  },
  getCTM: function () { return null; },
  getScreenCTM: function () { return null; },
  get transform() { return { baseVal: { numberOfItems: 0, getItem: function () { return null; }, consolidate: function () { return null; } } }; }
});
mix(SVGGeometryElement.prototype, {
  getTotalLength: function () { return 0; },
  getPointAtLength: function () { return new DOMPoint(0, 0); },
  isPointInFill: function () { return false; },
  isPointInStroke: function () { return false; }
});
mix(SVGSVGElement.prototype, {
  get width() { return animatedLength(elementOf(this), 'width'); },
  get height() { return animatedLength(elementOf(this), 'height'); },
  get x() { return animatedLength(elementOf(this), 'x'); },
  get y() { return animatedLength(elementOf(this), 'y'); },
  get viewBox() {
    var r = elementOf(this);
    var parts = (getAttrRaw(r, 'viewBox') || '0 0 0 0').split(/[\s,]+/).map(Number);
    var rect = { x: parts[0] || 0, y: parts[1] || 0, width: parts[2] || 0, height: parts[3] || 0 };
    return { baseVal: rect, animVal: rect };
  },
  createSVGPoint: function () { return new DOMPoint(0, 0); },
  createSVGRect: function () { return new DOMRect(0, 0, 0, 0); },
  createSVGMatrix: function () { return { a: 1, b: 0, c: 0, d: 1, e: 0, f: 0, multiply: function () { return this; }, inverse: function () { return this; }, translate: function () { return this; }, scale: function () { return this; } }; },
  createSVGTransform: function () { return { type: 0, matrix: this.createSVGMatrix(), setMatrix: function () {}, setTranslate: function () {}, setScale: function () {}, setRotate: function () {} }; },
  getCurrentTime: function () { return 0; },
  setCurrentTime: function () {},
  pauseAnimations: function () {},
  unpauseAnimations: function () {},
  animationsPaused: function () { return false; },
  suspendRedraw: function () { return 0; },
  unsuspendRedraw: function () {},
  unsuspendRedrawAll: function () {},
  forceRedraw: function () {},
  getElementById: function (id) {
    var r = elementOf(this);
    id = str(id);
    for (var e = nextElement(r, r); e; e = nextElement(e, r)) if (getAttrRaw(e, 'id') === id) return wrap(e);
    return null;
  }
});
(function () {
  var geometry = 'path circle ellipse line polygon polyline rect';
  geometry.split(' ').forEach(function (t) {
    var name = 'SVG' + t.charAt(0).toUpperCase() + t.slice(1) + 'Element';
    SVG_PROTOS[t] = iface(name, SVGGeometryElement).prototype;
  });
  var graphics = { g: 'SVGGElement', use: 'SVGUseElement', text: 'SVGTextElement', tspan: 'SVGTSpanElement', image: 'SVGImageElement',
    a: 'SVGAElement', foreignObject: 'SVGForeignObjectElement', defs: 'SVGDefsElement', switch: 'SVGSwitchElement', symbol: 'SVGSymbolElement' };
  Object.keys(graphics).forEach(function (t) { SVG_PROTOS[t] = iface(graphics[t], SVGGraphicsElement).prototype; });
  var plain = { title: 'SVGTitleElement', desc: 'SVGDescElement', linearGradient: 'SVGLinearGradientElement', radialGradient: 'SVGRadialGradientElement',
    stop: 'SVGStopElement', clipPath: 'SVGClipPathElement', mask: 'SVGMaskElement', pattern: 'SVGPatternElement', filter: 'SVGFilterElement',
    style: 'SVGStyleElement', script: 'SVGScriptElement', metadata: 'SVGMetadataElement', marker: 'SVGMarkerElement', view: 'SVGViewElement',
    animate: 'SVGAnimateElement', animateTransform: 'SVGAnimateTransformElement', animateMotion: 'SVGAnimateMotionElement', set: 'SVGSetElement' };
  Object.keys(plain).forEach(function (t) { SVG_PROTOS[t] = iface(plain[t], SVGElement).prototype; });
  ['SVGUseElement', 'SVGImageElement', 'SVGAElement', 'SVGScriptElement'].forEach(function (n) {
    defineProperty(global[n].prototype, 'href', {
      get: function () { var r = elementOf(this); return animatedString(r, getAttrRaw(r, 'href') !== null ? 'href' : 'xlink:href'); },
      enumerable: true, configurable: true
    });
  });
})();
var MathMLElement = iface('MathMLElement', Element);
mix(MathMLElement.prototype, {
  focus: function () { focusElement(elementOf(this)); },
  blur: function () { blurElement(elementOf(this)); }
});

// ---------------------------------------------------------------- element factories

function makeFactory(name, tag, setup) {
  var F = function () {
    if (!new.target) throw new TypeError("Failed to construct '" + name + "': Please use the 'new' operator.");
    var r = createElementRec(mainDoc || standaloneDoc(), tag, 0);
    setup(r, arguments);
    return wrap(r);
  };
  try { defineProperty(F, 'name', { value: name, configurable: true }); } catch (e) { /* ignore */ }
  F.prototype = global['HTML' + (tag === 'img' ? 'Image' : tag === 'audio' ? 'Audio' : 'Option') + 'Element'].prototype;
  defineGlobal(name, F);
  return F;
}
makeFactory('Image', 'img', function (r, args) {
  if (args.length > 0 && args[0] !== undefined) setAttrRaw(r, 'width', String(args[0] >>> 0));
  if (args.length > 1 && args[1] !== undefined) setAttrRaw(r, 'height', String(args[1] >>> 0));
});
makeFactory('Audio', 'audio', function (r, args) {
  setAttrRaw(r, 'preload', 'auto');
  if (args.length > 0 && args[0] !== undefined) setAttrRaw(r, 'src', str(args[0]));
});
makeFactory('Option', 'option', function (r, args) {
  if (args.length > 0 && args[0] !== undefined && str(args[0]) !== '') attachBuilt(r, textRec(r.d, str(args[0])));
  if (args.length > 1 && args[1] !== undefined) setAttrRaw(r, 'value', str(args[1]));
  if (args.length > 2 && args[2]) setAttrRaw(r, 'selected', '');
  if (args.length > 3 && args[3]) r.sel = true;
});

if (testHooks) {
  testHooks.formDataEntries = formDataEntries;
  testHooks.boxOf = boxOf;
}
