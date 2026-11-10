// 45_css.js — element.style (CSSStyleDeclaration), classList/relList (DOMTokenList), dataset, a small
// CSSOM (CSSStyleSheet, rules, document.styleSheets, adoptedStyleSheets), getComputedStyle and CSS.*.
//
// element.style writes through to the style attribute, so the renderer sees what scripts set (display,
// colours, widths …). Rules inserted with CSSOM only (styled-components and emotion in "speedy" mode never
// touch the <style> text) are kept per sheet and written back as text when the page is serialized.

var SD = Symbol('jukahub.style');
var TL = Symbol('jukahub.tokens');

var CSS_PROPERTIES = ('accent-color align-content align-items align-self all animation animation-composition animation-delay ' +
  'animation-direction animation-duration animation-fill-mode animation-iteration-count animation-name animation-play-state ' +
  'animation-timing-function appearance aspect-ratio backdrop-filter backface-visibility background background-attachment ' +
  'background-blend-mode background-clip background-color background-image background-origin background-position ' +
  'background-position-x background-position-y background-repeat background-size block-size border border-block ' +
  'border-block-color border-block-end border-block-start border-block-style border-block-width border-bottom ' +
  'border-bottom-color border-bottom-left-radius border-bottom-right-radius border-bottom-style border-bottom-width ' +
  'border-collapse border-color border-end-end-radius border-end-start-radius border-image border-image-outset ' +
  'border-image-repeat border-image-slice border-image-source border-image-width border-inline border-inline-color ' +
  'border-inline-end border-inline-start border-inline-style border-inline-width border-left border-left-color ' +
  'border-left-style border-left-width border-radius border-right border-right-color border-right-style border-right-width ' +
  'border-spacing border-start-end-radius border-start-start-radius border-style border-top border-top-color ' +
  'border-top-left-radius border-top-right-radius border-top-style border-top-width border-width bottom box-decoration-break ' +
  'box-shadow box-sizing break-after break-before break-inside caption-side caret-color clear clip clip-path color ' +
  'color-scheme column-count column-fill column-gap column-rule column-rule-color column-rule-style column-rule-width ' +
  'column-span column-width columns contain contain-intrinsic-size container container-name container-type content ' +
  'content-visibility counter-increment counter-reset counter-set cursor direction display empty-cells fill fill-opacity ' +
  'fill-rule filter flex flex-basis flex-direction flex-flow flex-grow flex-shrink flex-wrap float font font-display ' +
  'font-family font-feature-settings font-kerning font-optical-sizing font-size font-size-adjust font-stretch font-style ' +
  'font-synthesis font-variant font-variant-caps font-variant-east-asian font-variant-ligatures font-variant-numeric ' +
  'font-variation-settings font-weight gap grid grid-area grid-auto-columns grid-auto-flow grid-auto-rows grid-column ' +
  'grid-column-end grid-column-gap grid-column-start grid-gap grid-row grid-row-end grid-row-gap grid-row-start ' +
  'grid-template grid-template-areas grid-template-columns grid-template-rows height hyphens image-rendering inline-size ' +
  'inset inset-block inset-block-end inset-block-start inset-inline inset-inline-end inset-inline-start isolation ' +
  'justify-content justify-items justify-self left letter-spacing line-break line-clamp line-height list-style ' +
  'list-style-image list-style-position list-style-type margin margin-block margin-block-end margin-block-start ' +
  'margin-bottom margin-inline margin-inline-end margin-inline-start margin-left margin-right margin-top marker mask ' +
  'mask-image mask-position mask-repeat mask-size max-block-size max-height max-inline-size max-width min-block-size ' +
  'min-height min-inline-size min-width mix-blend-mode object-fit object-position offset opacity order orphans outline ' +
  'outline-color outline-offset outline-style outline-width overflow overflow-anchor overflow-wrap overflow-x overflow-y ' +
  'overscroll-behavior overscroll-behavior-x overscroll-behavior-y padding padding-block padding-block-end ' +
  'padding-block-start padding-bottom padding-inline padding-inline-end padding-inline-start padding-left padding-right ' +
  'padding-top page-break-after page-break-before page-break-inside paint-order perspective perspective-origin ' +
  'place-content place-items place-self pointer-events position quotes resize right rotate row-gap scale scroll-behavior ' +
  'scroll-margin scroll-margin-top scroll-padding scroll-padding-top scroll-snap-align scroll-snap-type scrollbar-color ' +
  'scrollbar-gutter scrollbar-width shape-outside stroke stroke-dasharray stroke-dashoffset stroke-linecap stroke-linejoin ' +
  'stroke-miterlimit stroke-opacity stroke-width tab-size table-layout text-align text-align-last text-anchor ' +
  'text-decoration text-decoration-color text-decoration-line text-decoration-style text-decoration-thickness ' +
  'text-emphasis text-indent text-justify text-orientation text-overflow text-rendering text-shadow text-size-adjust ' +
  'text-transform text-underline-offset text-underline-position text-wrap top touch-action transform transform-box ' +
  'transform-origin transform-style transition transition-behavior transition-delay transition-duration ' +
  'transition-property transition-timing-function translate unicode-bidi user-select vertical-align view-transition-name ' +
  'visibility white-space widows width will-change word-break word-spacing word-wrap writing-mode z-index zoom ' +
  '-webkit-appearance -webkit-backdrop-filter -webkit-box-orient -webkit-font-smoothing -webkit-line-clamp -webkit-mask ' +
  '-webkit-mask-image -webkit-overflow-scrolling -webkit-tap-highlight-color -webkit-text-fill-color ' +
  '-webkit-text-size-adjust -webkit-text-stroke -webkit-transform -webkit-transition -webkit-user-select ' +
  '-webkit-animation -webkit-box-shadow -webkit-filter -webkit-flex -webkit-box-flex -webkit-transform-origin ' +
  '-webkit-perspective -webkit-backface-visibility -webkit-user-drag -webkit-touch-callout -webkit-box-pack ' +
  '-webkit-box-align -webkit-flex-direction -webkit-justify-content -webkit-align-items').split(' ');
var CSS_KNOWN = objCreate(null);
CSS_PROPERTIES.forEach(function (p) { CSS_KNOWN[p] = 1; });

function cssCamel(name) {
  var s = name.charAt(0) === '-' && name.slice(0, 8) === '-webkit-' ? name.slice(1) : name;
  return s.replace(/-([a-z])/g, function (m, c) { return c.toUpperCase(); });
}
function cssKebab(prop) {
  if (prop === 'cssFloat') return 'float';
  var s = prop.replace(/[A-Z]/g, function (c) { return '-' + c.toLowerCase(); });
  if (/^(webkit|moz|ms)-/.test(s)) s = '-' + s;
  return s;
}

// ---------------------------------------------------------------- declarations

// Splits "a: b; c: d !important" into [{n, v, imp}], respecting quotes and parentheses.
var CSS_COMMENT = /\/\*[\s\S]*?\*\//g;
var CSS_IMPORTANT = /!\s*important\s*$/i;
function parseDeclarations(text) {
  var out = [];
  if (!text) return out;
  var depth = 0, quote = '', start = 0;
  var parts = [];
  for (var i = 0; i < text.length; i++) {
    var ch = text.charAt(i);
    if (quote) {
      if (ch === '\\') i++;
      else if (ch === quote) quote = '';
      continue;
    }
    if (ch === '"' || ch === "'") quote = ch;
    else if (ch === '(') depth++;
    else if (ch === ')') depth = Math.max(0, depth - 1);
    else if (ch === ';' && depth === 0) {
      parts.push(text.slice(start, i));
      start = i + 1;
    } else if (ch === '/' && text.charAt(i + 1) === '*') {
      var end = text.indexOf('*/', i + 2);
      i = end < 0 ? text.length : end + 1;
    }
  }
  parts.push(text.slice(start));
  for (var j = 0; j < parts.length; j++) {
    var p = parts[j];
    var colon = p.indexOf(':');
    if (colon < 0) continue;
    var name = p.slice(0, colon);
    if (name.indexOf('/*') >= 0) name = name.replace(CSS_COMMENT, '');
    name = stripWS(name);
    if (!name) continue;
    if (name.slice(0, 2) !== '--') name = asciiLower(name);
    var value = stripWS(p.slice(colon + 1));
    var imp = false;
    var m = value.indexOf('!') >= 0 ? CSS_IMPORTANT.exec(value) : null;
    if (m) {
      imp = true;
      value = stripWS(value.slice(0, m.index));
    }
    if (value === '') continue;
    setDecl(out, name, value, imp);
  }
  return out;
}
function setDecl(list, name, value, imp) {
  for (var i = 0; i < list.length; i++) {
    if (list[i].n === name) {
      list[i].v = value;
      list[i].imp = imp;
      return;
    }
  }
  list.push({ n: name, v: value, imp: imp });
}
function serializeDeclarations(list) {
  var out = [];
  for (var i = 0; i < list.length; i++) out.push(list[i].n + ': ' + list[i].v + (list[i].imp ? ' !important' : '') + ';');
  return out.join(' ');
}

var CSSStyleDeclaration = iface('CSSStyleDeclaration', null);
function declState(o) {
  var s = o !== null && o !== undefined ? o[SD] : undefined;
  if (!s) throw new TypeError('Illegal invocation');
  return s;
}
// The current declaration list (re-parsed from the style attribute when it changed underneath).
function declList(s) {
  if (s.el && (s.stale || s.list === null)) {
    s.list = parseDeclarations(getAttrRaw(s.el, 'style') || '');
    s.stale = false;
  }
  if (s.rule) return s.rule.decls;
  if (s.list === null) s.list = [];
  return s.list;
}
function declCommit(s) {
  if (s.ro) throw domErr('NoModificationAllowedError', 'Failed to set property: these styles are computed, and therefore read-only.');
  if (s.el) {
    var text = serializeDeclarations(s.list);
    flagOn(s.el, 'sdSelf');
    try {
      if (text === '' && getAttrRaw(s.el, 'style') === null) { /* nothing to write */ } else setAttrRaw(s.el, 'style', text);
    } finally {
      flagOff(s.el, 'sdSelf');
    }
  } else if (s.rule) {
    ruleChanged(s.rule);
  }
}
function declGet(o, name) {
  var s = declState(o);
  if (s.computed) return computedValue(s.el, name);
  var list = declList(s);
  for (var i = 0; i < list.length; i++) if (list[i].n === name) return list[i].v;
  return '';
}
function declSet(o, name, value, priority) {
  var s = declState(o);
  if (s.ro) throw domErr('NoModificationAllowedError', 'Failed to set property: these styles are computed, and therefore read-only.');
  var list = declList(s);
  if (value === null || value === undefined || str(value) === '') {
    for (var i = 0; i < list.length; i++) {
      if (list[i].n === name) {
        list.splice(i, 1);
        declCommit(s);
        return;
      }
    }
    return;
  }
  var v = stripWS(str(value));
  var imp = priority !== undefined && priority !== null && asciiLower(str(priority)) === 'important';
  var m = /!\s*important\s*$/i.exec(v);
  if (m) {
    imp = true;
    v = stripWS(v.slice(0, m.index));
  }
  setDecl(list, name, v, imp);
  declCommit(s);
}
function makeDecl(state) {
  if (!cssAccessorsInstalled) installCSSAccessors();
  var o = objCreate(CSSStyleDeclaration.prototype);
  defineProperty(o, SD, { value: state, configurable: true });
  return o;
}
function styleOf(r) {
  if (!r.sd) r.sd = { el: r, list: null, stale: true, ro: false, computed: false, rule: null };
  if (!r.sdo) r.sdo = makeDecl(r.sd);
  return r.sdo;
}
function propName(name) {
  name = str(name);
  return name.slice(0, 2) === '--' ? name : asciiLower(name);
}

mix(CSSStyleDeclaration.prototype, {
  get cssText() {
    var s = declState(this);
    if (s.computed) return '';
    return serializeDeclarations(declList(s));
  },
  set cssText(v) {
    var s = declState(this);
    if (s.ro) throw domErr('NoModificationAllowedError', 'Read-only.');
    var parsed = parseDeclarations(strOrEmpty(v));
    if (s.rule) s.rule.decls = parsed;
    else s.list = parsed;
    s.stale = false;
    declCommit(s);
  },
  get length() {
    var s = declState(this);
    return s.computed ? CSS_PROPERTIES.length : declList(s).length;
  },
  item: function (i) {
    var s = declState(this);
    i = i >>> 0;
    if (s.computed) return CSS_PROPERTIES[i] || '';
    var list = declList(s);
    return i < list.length ? list[i].n : '';
  },
  getPropertyValue: function (name) { return declGet(this, propName(name)); },
  getPropertyPriority: function (name) {
    var s = declState(this);
    if (s.computed) return '';
    name = propName(name);
    var list = declList(s);
    for (var i = 0; i < list.length; i++) if (list[i].n === name) return list[i].imp ? 'important' : '';
    return '';
  },
  setProperty: function (name, value, priority) { declSet(this, propName(name), value, priority); },
  removeProperty: function (name) {
    name = propName(name);
    var old = declGet(this, name);
    declSet(this, name, null);
    return old;
  },
  get parentRule() { var s = declState(this); return s.rule ? s.rule.obj : null; },
  get cssFloat() { return declGet(this, 'float'); },
  set cssFloat(v) { declSet(this, 'float', v); }
});
defineProperty(CSSStyleDeclaration.prototype, Symbol.iterator, {
  value: function () {
    var s = declState(this);
    return (s.computed ? CSS_PROPERTIES.slice() : declList(s).map(function (d) { return d.n; }))[Symbol.iterator]();
  },
  writable: true, configurable: true
});
// The ~1000 property accessors (camelCase, dashed and the webkit aliases) are installed when the first style
// object is made, not at load: defining them costs a few milliseconds of every page on the handheld, and a
// page that never touches element.style or getComputedStyle does not need them.
var cssAccessorsInstalled = false;
function installCSSAccessors() {
  if (cssAccessorsInstalled) return;
  cssAccessorsInstalled = true;
  var seen = objCreate(null);
  function accessor(prop, css) {
    if (seen[prop] || prop in CSSStyleDeclaration.prototype) return;
    seen[prop] = 1;
    defineProperty(CSSStyleDeclaration.prototype, prop, {
      get: function () { return declGet(this, css); },
      set: function (v) { declSet(this, css, v); },
      enumerable: true, configurable: true
    });
  }
  CSS_PROPERTIES.forEach(function (css) {
    // -webkit-transform and friends are aliases of the standard property, as in browsers, so the
    // renderer sees one declaration whichever name the script used.
    var target = css.slice(0, 8) === '-webkit-' && CSS_KNOWN[css.slice(8)] ? css.slice(8) : css;
    accessor(cssCamel(css), target);
    accessor(css, target);
    if (css.slice(0, 8) === '-webkit-') {
      var camel = cssCamel(css);
      accessor('W' + camel.slice(1), target);
    }
  });
  // Unprefixed names scripts set through the prefixed alias.
  ['transform', 'transition', 'animation', 'user-select', 'appearance', 'box-shadow', 'filter', 'mask', 'flex'].forEach(function (p) {
    var camel = cssCamel(p);
    accessor('webkit' + camel.charAt(0).toUpperCase() + camel.slice(1), p);
    accessor('Webkit' + camel.charAt(0).toUpperCase() + camel.slice(1), p);
    accessor('Moz' + camel.charAt(0).toUpperCase() + camel.slice(1), p);
    accessor('ms' + camel.charAt(0).toUpperCase() + camel.slice(1), p);
  });
}

// Inline display:none on the element itself (getComputedStyle, visibility checks).
function inlineDisplay(r) {
  var st = getAttrRaw(r, 'style');
  if (!st) return '';
  var list = parseDeclarations(st);
  for (var i = 0; i < list.length; i++) if (list[i].n === 'display') return asciiLower(list[i].v);
  return '';
}

var BLOCK_DISPLAY = { address: 1, article: 1, aside: 1, blockquote: 1, body: 1, center: 1, dd: 1, details: 1, dialog: 1,
  dir: 1, div: 1, dl: 1, dt: 1, fieldset: 1, figcaption: 1, figure: 1, footer: 1, form: 1, frameset: 1, h1: 1, h2: 1,
  h3: 1, h4: 1, h5: 1, h6: 1, header: 1, hgroup: 1, hr: 1, html: 1, legend: 1, listing: 1, main: 1, menu: 1, nav: 1,
  ol: 1, p: 1, plaintext: 1, pre: 1, search: 1, section: 1, summary: 1, ul: 1, xmp: 1, optgroup: 1 };
var SPECIAL_DISPLAY = { li: 'list-item', table: 'table', caption: 'table-caption', colgroup: 'table-column-group',
  col: 'table-column', thead: 'table-header-group', tbody: 'table-row-group', tfoot: 'table-footer-group',
  tr: 'table-row', td: 'table-cell', th: 'table-cell', input: 'inline-block', button: 'inline-block',
  select: 'inline-block', textarea: 'inline-block', img: 'inline', video: 'inline', iframe: 'inline', ruby: 'ruby',
  rt: 'ruby-text', marquee: 'inline-block', meter: 'inline-block', progress: 'inline-block', slot: 'contents' };
function defaultDisplay(r) {
  if (r.ns !== 0) return r.ln === 'svg' ? 'inline' : 'inline';
  if (NEVER_RENDERED[r.ln] || r.ln === 'area' || r.ln === 'datalist' || r.ln === 'param' || r.ln === 'noembed' ||
      r.ln === 'noframes' || r.ln === 'rp') return 'none';
  if (getAttrRaw(r, 'hidden') !== null) return 'none';
  if (r.ln === 'dialog' && getAttrRaw(r, 'open') === null) return 'none';
  if (BLOCK_DISPLAY[r.ln]) return 'block';
  return SPECIAL_DISPLAY[r.ln] || 'inline';
}
var COMPUTED_DEFAULTS = {
  visibility: 'visible', position: 'static', opacity: '1', 'font-size': '16px', 'font-weight': '400',
  'font-style': 'normal', 'line-height': 'normal', color: 'rgb(0, 0, 0)', 'background-color': 'rgba(0, 0, 0, 0)',
  'background-image': 'none', 'box-sizing': 'content-box', overflow: 'visible', 'overflow-x': 'visible',
  'overflow-y': 'visible', float: 'none', 'z-index': 'auto', 'transition-duration': '0s', 'transition-delay': '0s',
  'transition-property': 'all', 'animation-duration': '0s', 'animation-delay': '0s', 'animation-name': 'none',
  transform: 'none', 'pointer-events': 'auto', cursor: 'auto', direction: 'ltr', 'text-align': 'start',
  'white-space': 'normal', 'font-family': 'sans-serif', zoom: '1', 'clip-path': 'none', filter: 'none',
  'max-height': 'none', 'max-width': 'none', 'min-height': '0px', 'min-width': '0px', 'margin-top': '0px',
  'margin-right': '0px', 'margin-bottom': '0px', 'margin-left': '0px', 'padding-top': '0px', 'padding-right': '0px',
  'padding-bottom': '0px', 'padding-left': '0px', 'border-top-width': '0px', 'border-right-width': '0px',
  'border-bottom-width': '0px', 'border-left-width': '0px', 'border-top-style': 'none', 'border-right-style': 'none',
  'border-bottom-style': 'none', 'border-left-style': 'none', 'list-style-type': 'disc', 'text-transform': 'none',
  'text-decoration-line': 'none', 'vertical-align': 'baseline', 'content': 'normal', 'flex-direction': 'row',
  'flex-wrap': 'nowrap', 'justify-content': 'normal', 'align-items': 'normal', 'flex-grow': '0', 'flex-shrink': '1',
  'flex-basis': 'auto', 'object-fit': 'fill', 'user-select': 'auto', resize: 'none', 'will-change': 'auto',
  'scroll-behavior': 'auto', 'touch-action': 'auto', 'mix-blend-mode': 'normal', isolation: 'auto',
  'backface-visibility': 'visible', 'text-overflow': 'clip', 'word-break': 'normal', 'overflow-wrap': 'normal',
  'letter-spacing': 'normal', 'word-spacing': '0px', top: 'auto', right: 'auto', bottom: 'auto', left: 'auto',
  'color-scheme': 'normal', 'container-type': 'normal', 'aspect-ratio': 'auto', 'content-visibility': 'visible'
};
function computedValue(r, name) {
  if (!r) return '';
  var own = ownComputed(r, name);
  if (own !== null) {
    if (name === 'display' && CSS_WIDE[own]) return own === 'inherit' ? inheritedComputed(r, name) : (r.c ? defaultDisplay(r) : '');
    if (own !== 'inherit') return own;
    return inheritedComputed(r, name);
  }
  if (CASCADE_INHERITED[name] && r.c) return inheritedComputed(r, name);
  if (name === 'display') return r.c ? defaultDisplay(r) : '';
  if (name === 'width' || name === 'height') {
    var b = boxOf(r);
    return b ? (name === 'width' ? b.w : b.h) + 'px' : 'auto';
  }
  if (name.slice(0, 2) === '--') return '';
  var d = COMPUTED_DEFAULTS[name];
  return d === undefined ? '' : d;
}

// ownComputed: the value an element's own declarations give a property — its inline style, or the in-page
// cascade when no inline value wins over it — or null.
var CSS_WIDE = { initial: 1, revert: 1, 'revert-layer': 1, unset: 1, inherit: 1 };
function ownComputed(r, name) {
  var inline = null, inlineImp = false;
  var st = getAttrRaw(r, 'style');
  if (st) {
    var list = parseDeclarations(st);
    for (var i = 0; i < list.length; i++) {
      if (list[i].n === name) {
        inline = list[i].v;
        inlineImp = list[i].imp;
      }
    }
  }
  if (CASCADE_PROPS[name] && r.c) {
    var cv = cascadedValues(r);
    if (cv && cv.values[name] !== undefined && (inline === null || (cv.imp[name] && !inlineImp))) return cv.values[name];
  }
  return inline;
}
// inheritedComputed: the parent's computed value (iteratively, so deep trees cannot overflow the stack).
function inheritedComputed(r, name) {
  for (var a = parentForStyle(r); a; a = parentForStyle(a)) {
    var v = ownComputed(a, name);
    if (v !== null && v !== 'inherit' && !(v === 'unset' && CASCADE_INHERITED[name])) {
      return CSS_WIDE[v] ? (COMPUTED_DEFAULTS[name] || '') : v;
    }
  }
  return COMPUTED_DEFAULTS[name] || '';
}
function parentForStyle(r) {
  var p = r.p;
  if (p && p.t === 1) return p;
  if (p && p.t === 11 && p.host) return p.host;
  return null;
}

// ---------------------------------------------------------------- the in-page cascade
//
// getComputedStyle used to see only the element's inline style and the UA defaults, so a script asking "is
// this hidden?" about content the page's own stylesheet hides heard "block" — and jQuery's .show(),
// .fadeIn() and .slideDown() did nothing, since they only write display when the computed value is none.
// This covers the sheets a script can see without a network: <style> elements, CSSOM-inserted rules and
// adopted sheets (external <link> sheets are not fetched for scripts). Rules aimed at pseudo-elements are
// skipped; specificity, source order and !important decide; only the properties scripts test are kept.
// The rule index is rebuilt when cssVersion moves; each element's result is kept until the document or the
// rules change.

var CASCADE_PROPS = { display: 1, visibility: 1, opacity: 1, position: 1, float: 1, 'overflow-x': 1, 'overflow-y': 1,
  overflow: 1, 'z-index': 1, 'pointer-events': 1, 'content-visibility': 1, 'transition-duration': 1,
  'transition-delay': 1, 'transition-property': 1, 'animation-name': 1, 'animation-duration': 1, 'box-sizing': 1,
  'white-space': 1, 'text-align': 1, 'flex-direction': 1, 'justify-content': 1, 'align-items': 1, cursor: 1 };
var CASCADE_INHERITED = { visibility: 1, 'white-space': 1, 'text-align': 1, 'pointer-events': 1, cursor: 1 };
var CASCADE_MAX_RULES = 6000;
var CASCADE_MAX_SHEET = 262144;
var CASCADE_PSEUDO_ELEMENT = /::|:(before|after|first-line|first-letter|marker|placeholder|selection|backdrop)\b/i;
var CASCADE_MEDIA = /^@media\s*([^{]*)/i;
var cascadeCache = { doc: null, ver: -1, adopted: null, adoptedLen: -1, index: null };

function cascadeIndex(d) {
  var adopted = d.adopted || null;
  var adoptedLen = adopted ? adopted.length : 0;
  if (cascadeCache.doc === d && cascadeCache.ver === cssVersion && cascadeCache.adopted === adopted &&
      cascadeCache.adoptedLen === adoptedLen) return cascadeCache.index;
  var index = { byId: objCreate(null), byClass: objCreate(null), byTag: objCreate(null), any: [], n: 0, display: false, order: 0 };
  var els = (d.sheetEls || []).slice();
  els.sort(function (a, b) { return a === b ? 0 : precedes(a, b) ? -1 : 1; });
  for (var i = 0; i < els.length && index.n < CASCADE_MAX_RULES; i++) {
    var e = els[i];
    if (!e.c || e.d !== d || e.ln !== 'style' || inShadowTree(e)) continue;
    var media = getAttrRaw(e, 'media');
    if (media !== null && !cascadeMedia(media)) continue;
    if (childText(e).length > CASCADE_MAX_SHEET && !(e.sheet && e.sheet.dirty)) continue;
    var so = sheetOfStyle(e);
    var sst = so && so[SH];
    if (sst && !sst.disabled) cascadeAddRules(index, sheetRules(sst), 0);
  }
  if (adopted) {
    for (var j = 0; j < adopted.length && index.n < CASCADE_MAX_RULES; j++) {
      var ast = adopted[j] && adopted[j][SH];
      if (ast && !ast.get && !ast.disabled) cascadeAddRules(index, sheetRules(ast), 0);
    }
  }
  cascadeCache = { doc: d, ver: cssVersion, adopted: adopted, adoptedLen: adoptedLen, index: index };
  return index;
}

function cascadeMedia(text) {
  if (typeof envEvalMedia !== 'function') return true;
  try { return !!envEvalMedia(text); } catch (e) { return true; }
}

function cascadeAddRules(index, rules, depth) {
  for (var i = 0; i < rules.length && index.n < CASCADE_MAX_RULES; i++) {
    var rec = rules[i];
    if (rec.kind === 1) {
      cascadeAddStyleRule(index, rec);
    } else if (depth < 4 && (rec.kind === 4 || rec.kind === 12 || (rec.kind === 0 && rec.text.indexOf('{') > 0 &&
        asciiLower(rec.text.slice(0, 6)) === '@layer'))) {
      if (rec.kind === 4) {
        var m = CASCADE_MEDIA.exec(rec.text);
        if (m && !cascadeMedia(stripWS(m[1]))) continue;
      }
      cascadeAddRules(index, innerRules(rec), depth + 1);
    }
  }
}

function cascadeAddStyleRule(index, rec) {
  var decls = null;
  var list = rec.decls || [];
  for (var i = 0; i < list.length; i++) {
    var n = list[i].n;
    if (!CASCADE_PROPS[n]) continue;
    decls = decls || [];
    if (n === 'overflow') {
      var parts = splitTokens(list[i].v);
      decls.push({ n: 'overflow-x', v: parts[0] || '', imp: list[i].imp });
      decls.push({ n: 'overflow-y', v: parts[1] || parts[0] || '', imp: list[i].imp });
    }
    decls.push(list[i]);
    if (n === 'display') index.display = true;
  }
  if (!decls) return;
  var sels = splitSelectorList(rec.sel || '');
  for (var j = 0; j < sels.length; j++) {
    var sel = sels[j];
    if (sel === '' || CASCADE_PSEUDO_ELEMENT.test(sel)) continue;
    var m;
    try { m = compileSelector(sel); } catch (e) { continue; }
    var entry = { m: m, spec: selectorSpecificity(sel), order: index.order++, decls: decls };
    var key = rightmostKey(sel);
    var bucket;
    if (key.charAt(0) === '#') bucket = index.byId[key.slice(1)] || (index.byId[key.slice(1)] = []);
    else if (key.charAt(0) === '.') bucket = index.byClass[key.slice(1)] || (index.byClass[key.slice(1)] = []);
    else if (key !== '') bucket = index.byTag[key] || (index.byTag[key] = []);
    else bucket = index.any;
    bucket.push(entry);
    index.n++;
  }
}

// splitSelectorList splits "a, b:is(c, d), e" at top-level commas.
function splitSelectorList(text) {
  var out = [], depth = 0, quote = '', start = 0;
  for (var i = 0; i < text.length; i++) {
    var c = text.charAt(i);
    if (quote) {
      if (c === '\\') i++;
      else if (c === quote) quote = '';
    } else if (c === '"' || c === "'") quote = c;
    else if (c === '\\') i++;
    else if (c === '(' || c === '[') depth++;
    else if (c === ')' || c === ']') depth = depth > 0 ? depth - 1 : 0;
    else if (c === ',' && depth === 0) {
      out.push(stripWS(text.slice(start, i)));
      start = i + 1;
    }
  }
  out.push(stripWS(text.slice(start)));
  return out;
}

function selNameChar(c) {
  return (c >= 48 && c <= 57) || (c >= 65 && c <= 90) || (c >= 97 && c <= 122) || c === 45 || c === 95 || c > 127;
}

// rightmostKey: the bucket a complex selector is filed under — the #id, else the first .class, else the type
// of its last compound ("" for anything else, which every element checks).
function rightmostKey(sel) {
  var depth = 0, quote = '', start = 0;
  for (var i = 0; i < sel.length; i++) {
    var c = sel.charAt(i);
    if (quote) {
      if (c === '\\') i++;
      else if (c === quote) quote = '';
    } else if (c === '"' || c === "'") quote = c;
    else if (c === '\\') i++;
    else if (c === '(' || c === '[') depth++;
    else if (c === ')' || c === ']') depth = depth > 0 ? depth - 1 : 0;
    else if (depth === 0 && (c === ' ' || c === '>' || c === '+' || c === '~' || c === '\t' || c === '\n')) start = i + 1;
  }
  var comp = sel.slice(start);
  var id = '', cls = '', type = '';
  depth = 0;
  for (var j = 0; j < comp.length; j++) {
    var ch = comp.charAt(j);
    if (ch === '(' || ch === '[') { depth++; continue; }
    if (ch === ')' || ch === ']') { depth = depth > 0 ? depth - 1 : 0; continue; }
    if (depth > 0 || ch === '\\') return id ? '#' + id : cls ? '.' + cls : type;
    if (ch === '#' || ch === '.') {
      var k = j + 1;
      while (k < comp.length && selNameChar(comp.charCodeAt(k))) k++;
      var name = comp.slice(j + 1, k);
      if (ch === '#' && !id) id = name;
      if (ch === '.' && !cls) cls = name;
      j = k - 1;
    } else if (j === 0 && selNameChar(comp.charCodeAt(0))) {
      var t = 0;
      while (t < comp.length && selNameChar(comp.charCodeAt(t))) t++;
      type = asciiLower(comp.slice(0, t));
      j = t - 1;
    } else if (ch === ':') {
      break;
    }
  }
  if (id) return '#' + id;
  if (cls) return '.' + cls;
  return type;
}

// selectorSpecificity: (ids, classes/attributes/pseudo-classes, types) packed into one number. :where() adds
// nothing; :is()/:not()/:has()/:matches() count their most specific argument.
function selectorSpecificity(sel) {
  var a = 0, b = 0, c = 0;
  var i = 0, n = sel.length;
  while (i < n) {
    var ch = sel.charAt(i);
    if (ch === '#') {
      a++;
      i++;
      while (i < n && selNameChar(sel.charCodeAt(i))) i++;
    } else if (ch === '.') {
      b++;
      i++;
      while (i < n && selNameChar(sel.charCodeAt(i))) i++;
    } else if (ch === '[') {
      b++;
      var close = sel.indexOf(']', i);
      i = close < 0 ? n : close + 1;
    } else if (ch === ':') {
      var dbl = sel.charAt(i + 1) === ':';
      var s0 = i + (dbl ? 2 : 1);
      var e0 = s0;
      while (e0 < n && selNameChar(sel.charCodeAt(e0))) e0++;
      var pname = asciiLower(sel.slice(s0, e0));
      i = e0;
      var arg = null;
      if (sel.charAt(i) === '(') {
        var depth = 1, k = i + 1;
        while (k < n && depth > 0) {
          if (sel.charAt(k) === '(') depth++;
          else if (sel.charAt(k) === ')') depth--;
          k++;
        }
        arg = sel.slice(i + 1, k - 1);
        i = k;
      }
      if (dbl) c++;
      else if (pname === 'where') { /* zero */ }
      else if (arg !== null && (pname === 'is' || pname === 'not' || pname === 'has' || pname === 'matches' || pname === '-webkit-any')) {
        var best = 0, list = splitSelectorList(arg);
        for (var x = 0; x < list.length; x++) {
          var sp = selectorSpecificity(list[x]);
          if (sp > best) best = sp;
        }
        a += Math.floor(best / 1000000);
        b += Math.floor(best / 1000) % 1000;
        c += best % 1000;
      } else b++;
    } else if (selNameChar(sel.charCodeAt(i))) {
      c++;
      while (i < n && selNameChar(sel.charCodeAt(i))) i++;
    } else {
      i++;
    }
  }
  return a * 1000000 + Math.min(b, 999) * 1000 + Math.min(c, 999);
}

// cascadedValues: the in-page rules' winning values for r ({values, imp}), or null when there are none.
function cascadedValues(r) {
  var d = r.d;
  if (!d || !d.sheetEls || (!d.sheetEls.length && !(d.adopted && d.adopted.length))) return null;
  var index = cascadeIndex(d);
  if (!index.n) return null;
  var cv = r.cv;
  if (cv && cv.css === cssVersion && cv.ver === d.ver && cv.idx === index) return cv;
  var cands = [];
  var id = getAttrRaw(r, 'id');
  if (id && index.byId[id]) cands.push(index.byId[id]);
  var cls = getAttrRaw(r, 'class');
  if (cls) {
    var toks = splitTokens(cls);
    for (var i = 0; i < toks.length; i++) {
      var b = index.byClass[toks[i]];
      if (b && cands.indexOf(b) < 0) cands.push(b);
    }
  }
  var tb = index.byTag[r.ns === 0 ? r.ln : asciiLower(r.ln)];
  if (tb) cands.push(tb);
  if (index.any.length) cands.push(index.any);
  var hits = [];
  for (var j = 0; j < cands.length; j++) {
    var bucket = cands[j];
    for (var k = 0; k < bucket.length; k++) {
      var entry = bucket[k];
      var ok = false;
      try { ok = entry.m(r, null); } catch (e) { ok = false; }
      if (ok) hits.push(entry);
    }
  }
  hits.sort(function (x, y) { return x.spec - y.spec || x.order - y.order; });
  var values = objCreate(null), imp = objCreate(null);
  for (var h = 0; h < hits.length; h++) {
    var ds = hits[h].decls;
    for (var q = 0; q < ds.length; q++) if (!ds[q].imp && !imp[ds[q].n]) values[ds[q].n] = ds[q].v;
  }
  for (var h2 = 0; h2 < hits.length; h2++) {
    var ds2 = hits[h2].decls;
    for (var q2 = 0; q2 < ds2.length; q2++) {
      if (ds2[q2].imp) {
        values[ds2[q2].n] = ds2[q2].v;
        imp[ds2[q2].n] = true;
      }
    }
  }
  cv = r.cv = { css: cssVersion, ver: d.ver, idx: index, values: values, imp: imp };
  return cv;
}

// cascadeHides reports whether the in-page cascade gives r display:none that its inline style does not
// override (elementRendered: offsetWidth, getBoundingClientRect, IntersectionObserver).
var INLINE_DISPLAY = /(^|;)\s*display\s*:/i;
function cascadeHides(r) {
  var d = r.d;
  if (!d || !d.sheetEls || !d.sheetEls.length) return false;
  var index = cascadeIndex(d);
  if (!index.display) return false;
  var cv = cascadedValues(r);
  if (!cv || asciiLower(cv.values.display || '') !== 'none') return false;
  if (cv.imp.display) return true;
  var st = getAttrRaw(r, 'style');
  return !(st && INLINE_DISPLAY.test(st));
}

function getComputedStyleImpl(el, pseudo) {
  var r = unwrap(el);
  if (!r || r.t !== 1) throw new TypeError("Failed to execute 'getComputedStyle' on 'Window': parameter 1 is not of type 'Element'.");
  return makeDecl({ el: r, list: [], stale: false, ro: true, computed: true, rule: null, pseudo: pseudo || null });
}
defineGlobal('getComputedStyle', function getComputedStyle(el, pseudo) { return getComputedStyleImpl(el, pseudo); });

defineProperty(HTMLElement.prototype, 'style', {
  get: function () { return styleOf(elementOf(this)); },
  set: function (v) { styleOf(elementOf(this)).cssText = v; },
  enumerable: true, configurable: true
});
defineProperty(SVGElement.prototype, 'style', {
  get: function () { return styleOf(elementOf(this)); },
  set: function (v) { styleOf(elementOf(this)).cssText = v; },
  enumerable: true, configurable: true
});
defineProperty(MathMLElement.prototype, 'style', {
  get: function () { return styleOf(elementOf(this)); },
  set: function (v) { styleOf(elementOf(this)).cssText = v; },
  enumerable: true, configurable: true
});
defineProperty(Element.prototype, 'computedStyleMap', {
  value: function () {
    var decl = getComputedStyleImpl(this);
    return { get: function (p) { var v = decl.getPropertyValue(p); return v ? { value: v, toString: function () { return v; } } : null; }, has: function (p) { return decl.getPropertyValue(p) !== ''; } };
  },
  writable: true, enumerable: true, configurable: true
});

// ---------------------------------------------------------------- DOMTokenList

var DOMTokenList = iface('DOMTokenList', null);
function tokenState(o) {
  var s = o !== null && o !== undefined ? o[TL] : undefined;
  if (!s) throw new TypeError('Illegal invocation');
  return s;
}
function tokensOf(s) {
  var v = getAttrRaw(s.el, s.attr);
  if (v === s.cachedValue && s.cached) return s.cached;
  var out = [];
  var raw = splitTokens(v || '');
  for (var i = 0; i < raw.length; i++) if (out.indexOf(raw[i]) < 0) out.push(raw[i]);
  s.cached = out;
  s.cachedValue = v;
  return out;
}
function tokensWrite(s, tokens) {
  if (!tokens.length && getAttrRaw(s.el, s.attr) === null) return;
  setAttrRaw(s.el, s.attr, tokens.join(' '));
}
function syncTokenIndices(o, s) {
  var tokens = tokensOf(s);
  var n = s.indexed | 0;
  for (var i = 0; i < Math.max(n, tokens.length); i++) {
    if (i < tokens.length) defineProperty(o, i, { value: tokens[i], enumerable: true, configurable: true, writable: false });
    else delete o[i];
  }
  s.indexed = tokens.length;
  s.indexedValue = s.cachedValue;
}
function checkToken(t, what) {
  t = str(t);
  if (t === '') throw domErr('SyntaxError', "Failed to execute '" + what + "' on 'DOMTokenList': The token provided must not be empty.");
  if (/[\t\n\f\r ]/.test(t)) throw domErr('InvalidCharacterError', "Failed to execute '" + what + "' on 'DOMTokenList': The token provided ('" + t + "') contains HTML space characters, which are not valid in tokens.");
  return t;
}
function tokenListOf(r, attr) {
  var cache = r.tls || (r.tls = objCreate(null));
  var o = cache[attr];
  if (!o) {
    o = objCreate(DOMTokenList.prototype);
    defineProperty(o, TL, { value: { el: r, attr: attr, cached: null, cachedValue: undefined, indexed: 0, indexedValue: undefined }, configurable: true });
    cache[attr] = o;
  }
  var s = o[TL];
  if (s.indexedValue !== getAttrRaw(r, attr) || s.indexed === undefined) syncTokenIndices(o, s);
  return o;
}
mix(DOMTokenList.prototype, {
  get length() { return tokensOf(tokenState(this)).length; },
  get value() { var v = getAttrRaw(tokenState(this).el, tokenState(this).attr); return v === null ? '' : v; },
  set value(v) { var s = tokenState(this); setAttrRaw(s.el, s.attr, str(v)); syncTokenIndices(this, s); },
  toString: function () { var s = tokenState(this); var v = getAttrRaw(s.el, s.attr); return v === null ? '' : v; },
  item: function (i) { var t = tokensOf(tokenState(this)); i = i >>> 0; return i < t.length ? t[i] : null; },
  contains: function (token) { return tokensOf(tokenState(this)).indexOf(str(token)) >= 0; },
  add: function () {
    var s = tokenState(this);
    var add = [];
    for (var i = 0; i < arguments.length; i++) add.push(checkToken(arguments[i], 'add'));
    var tokens = tokensOf(s).slice();
    for (var j = 0; j < add.length; j++) if (tokens.indexOf(add[j]) < 0) tokens.push(add[j]);
    tokensWrite(s, tokens);
    syncTokenIndices(this, s);
  },
  remove: function () {
    var s = tokenState(this);
    var rem = [];
    for (var i = 0; i < arguments.length; i++) rem.push(checkToken(arguments[i], 'remove'));
    var tokens = tokensOf(s).filter(function (t) { return rem.indexOf(t) < 0; });
    tokensWrite(s, tokens);
    syncTokenIndices(this, s);
  },
  toggle: function (token, force) {
    var s = tokenState(this);
    token = checkToken(token, 'toggle');
    var tokens = tokensOf(s).slice();
    var i = tokens.indexOf(token);
    var result;
    if (i >= 0) {
      if (force === undefined || !force) {
        tokens.splice(i, 1);
        tokensWrite(s, tokens);
        result = false;
      } else {
        result = true;
      }
    } else if (force === undefined || force) {
      tokens.push(token);
      tokensWrite(s, tokens);
      result = true;
    } else {
      result = false;
    }
    syncTokenIndices(this, s);
    return result;
  },
  replace: function (token, replacement) {
    var s = tokenState(this);
    token = checkToken(token, 'replace');
    replacement = checkToken(replacement, 'replace');
    var tokens = tokensOf(s).slice();
    var i = tokens.indexOf(token);
    if (i < 0) return false;
    var j = tokens.indexOf(replacement);
    if (j >= 0 && j !== i) {
      tokens.splice(i, 1);
    } else {
      tokens[i] = replacement;
    }
    tokensWrite(s, tokens);
    syncTokenIndices(this, s);
    return true;
  },
  supports: function () { return true; },
  forEach: function (cb, thisArg) {
    var t = tokensOf(tokenState(this)).slice();
    for (var i = 0; i < t.length; i++) cb.call(thisArg, t[i], i, this);
  },
  entries: function () { return tokensOf(tokenState(this)).map(function (t, i) { return [i, t]; })[Symbol.iterator](); },
  keys: function () { return tokensOf(tokenState(this)).map(function (t, i) { return i; })[Symbol.iterator](); },
  values: function () { return tokensOf(tokenState(this)).slice()[Symbol.iterator](); }
});
defineProperty(DOMTokenList.prototype, Symbol.iterator, { value: DOMTokenList.prototype.values, writable: true, configurable: true });

// ---------------------------------------------------------------- dataset

var DOMStringMap = iface('DOMStringMap', null);
function dataAttrName(prop) {
  if (/-[a-z]/.test(prop)) return null;
  return 'data-' + prop.replace(/[A-Z]/g, function (c) { return '-' + c.toLowerCase(); });
}
function dataPropName(attr) {
  return attr.slice(5).replace(/-([a-z])/g, function (m, c) { return c.toUpperCase(); });
}
var datasetHandler = {
  get: function (t, k, recv) {
    if (typeof k !== 'string' || k in t) return Reflect.get(t, k, recv);
    var name = dataAttrName(k);
    if (!name) return undefined;
    var v = getAttrRaw(t[TL], name);
    return v === null ? undefined : v;
  },
  set: function (t, k, v) {
    if (typeof k !== 'string') return Reflect.set(t, k, v);
    var name = dataAttrName(k);
    if (!name) throw domErr('SyntaxError', "Failed to set the '" + k + "' property on 'DOMStringMap': '" + k + "' is not a valid property name.");
    setAttrRaw(t[TL], name, str(v));
    return true;
  },
  has: function (t, k) {
    if (typeof k !== 'string') return k in t;
    var name = dataAttrName(k);
    return (name !== null && getAttrRaw(t[TL], name) !== null) || k in t;
  },
  deleteProperty: function (t, k) {
    if (typeof k !== 'string') return Reflect.deleteProperty(t, k);
    var name = dataAttrName(k);
    if (name) removeAttrRaw(t[TL], name);
    return true;
  },
  ownKeys: function (t) {
    var el = t[TL];
    var out = [];
    var a = el.a || [];
    for (var i = 0; i < a.length; i += 2) if (a[i].slice(0, 5) === 'data-') out.push(dataPropName(a[i]));
    return out;
  },
  getOwnPropertyDescriptor: function (t, k) {
    if (typeof k !== 'string') return Reflect.getOwnPropertyDescriptor(t, k);
    var name = dataAttrName(k);
    var v = name ? getAttrRaw(t[TL], name) : null;
    return v === null ? undefined : { value: v, writable: true, enumerable: true, configurable: true };
  },
  defineProperty: function (t, k, d) {
    if (typeof k === 'string' && d && 'value' in d) {
      var name = dataAttrName(k);
      if (name) {
        setAttrRaw(t[TL], name, str(d.value));
        return true;
      }
    }
    return false;
  }
};
function datasetOf(r) {
  if (!r.dso) {
    var t = objCreate(DOMStringMap.prototype);
    defineProperty(t, TL, { value: r, configurable: true });
    r.dso = new Proxy(t, datasetHandler);
  }
  return r.dso;
}
[HTMLElement.prototype, SVGElement.prototype, MathMLElement.prototype].forEach(function (p) {
  defineProperty(p, 'dataset', { get: function () { return datasetOf(elementOf(this)); }, enumerable: true, configurable: true });
});

// ---------------------------------------------------------------- CSSOM

var SH = Symbol('jukahub.sheet');
var RU = Symbol('jukahub.rule');

var MediaList = iface('MediaList', null);
function makeMediaList(get, set) {
  var o = objCreate(MediaList.prototype);
  defineProperty(o, SH, { value: { get: get, set: set }, configurable: true });
  return o;
}
function mediaItems(o) { return o[SH].get().split(',').map(stripWS).filter(function (x) { return x !== ''; }); }
mix(MediaList.prototype, {
  get mediaText() { return o_mediaText(this); },
  set mediaText(v) { this[SH].set(str(v)); },
  get length() { return mediaItems(this).length; },
  item: function (i) { return mediaItems(this)[i >>> 0] || null; },
  appendMedium: function (m) { var l = mediaItems(this); l.push(str(m)); this[SH].set(l.join(', ')); },
  deleteMedium: function (m) { this[SH].set(mediaItems(this).filter(function (x) { return x !== str(m); }).join(', ')); },
  toString: function () { return o_mediaText(this); }
});
function o_mediaText(o) { return mediaItems(o).join(', '); }

// Splits a stylesheet into top-level rules (text), respecting strings, comments and nesting.
function splitRules(text) {
  var out = [];
  var depth = 0, quote = '', start = 0;
  for (var i = 0; i < text.length; i++) {
    var ch = text.charAt(i);
    if (quote) {
      if (ch === '\\') i++;
      else if (ch === quote) quote = '';
      continue;
    }
    if (ch === '/' && text.charAt(i + 1) === '*') {
      var end = text.indexOf('*/', i + 2);
      if (depth === 0) {
        var before = text.slice(start, i);
        if (stripWS(before) === '') start = end < 0 ? text.length : end + 2;
      }
      i = end < 0 ? text.length : end + 1;
      continue;
    }
    if (ch === '"' || ch === "'") quote = ch;
    else if (ch === '{') depth++;
    else if (ch === '}') {
      depth--;
      if (depth <= 0) {
        depth = 0;
        var rule = stripWS(text.slice(start, i + 1));
        if (rule) out.push(rule);
        start = i + 1;
      }
    } else if (ch === ';' && depth === 0) {
      var stmt = stripWS(text.slice(start, i + 1));
      if (stmt && stmt.charAt(0) === '@') out.push(stmt);
      start = i + 1;
    }
  }
  var rest = stripWS(text.slice(start));
  if (rest && rest.charAt(0) === '@' && !/\{/.test(rest)) out.push(rest);
  return out;
}

var CSSRule = iface('CSSRule', null);
constants(CSSRule, { STYLE_RULE: 1, CHARSET_RULE: 2, IMPORT_RULE: 3, MEDIA_RULE: 4, FONT_FACE_RULE: 5, PAGE_RULE: 6,
  KEYFRAMES_RULE: 7, KEYFRAME_RULE: 8, NAMESPACE_RULE: 10, COUNTER_STYLE_RULE: 11, SUPPORTS_RULE: 12, FONT_FEATURE_VALUES_RULE: 14 });
var CSSStyleRule = iface('CSSStyleRule', CSSRule);
var CSSGroupingRule = iface('CSSGroupingRule', CSSRule);
var CSSConditionRule = iface('CSSConditionRule', CSSGroupingRule);
var CSSMediaRule = iface('CSSMediaRule', CSSConditionRule);
var CSSSupportsRule = iface('CSSSupportsRule', CSSConditionRule);
var CSSImportRule = iface('CSSImportRule', CSSRule);
var CSSFontFaceRule = iface('CSSFontFaceRule', CSSRule);
var CSSKeyframesRule = iface('CSSKeyframesRule', CSSRule);
var CSSLayerBlockRule = iface('CSSLayerBlockRule', CSSGroupingRule);
var CSSRuleList = iface('CSSRuleList', null);

function makeRule(text, sheet) {
  var rec = { text: text, sheet: sheet, obj: null, decls: null, sel: null, kind: 0 };
  var t = text;
  if (t.charAt(0) === '@') {
    var kw = asciiLower((/^@([\w-]+)/.exec(t) || ['', ''])[1]);
    rec.kind = kw === 'media' ? 4 : kw === 'import' ? 3 : kw === 'font-face' ? 5 : kw === 'keyframes' || kw === '-webkit-keyframes' ? 7 :
      kw === 'supports' ? 12 : kw === 'page' ? 6 : kw === 'namespace' ? 10 : kw === 'charset' ? 2 : kw === 'layer' ? 0 : 0;
  } else {
    rec.kind = 1;
    var brace = t.indexOf('{');
    rec.sel = stripWS(brace < 0 ? t : t.slice(0, brace));
    var body = brace < 0 ? '' : t.slice(brace + 1, t.lastIndexOf('}') > brace ? t.lastIndexOf('}') : t.length);
    rec.decls = parseDeclarations(body);
  }
  var proto = rec.kind === 1 ? CSSStyleRule.prototype : rec.kind === 4 ? CSSMediaRule.prototype : rec.kind === 3 ? CSSImportRule.prototype :
    rec.kind === 5 ? CSSFontFaceRule.prototype : rec.kind === 7 ? CSSKeyframesRule.prototype : rec.kind === 12 ? CSSSupportsRule.prototype : CSSRule.prototype;
  var o = objCreate(proto);
  defineProperty(o, RU, { value: rec, configurable: true });
  rec.obj = o;
  return rec;
}
function ruleText(rec) {
  if (rec.kind === 1) return rec.sel + ' { ' + serializeDeclarations(rec.decls) + ' }';
  return rec.text;
}
function ruleChanged(rec) {
  if (rec.sheet) rec.sheet.dirty = true;
  cssVersion++;
}
function ruleOf(o) {
  var r = o !== null && o !== undefined ? o[RU] : undefined;
  if (!r) throw new TypeError('Illegal invocation');
  return r;
}
mix(CSSRule.prototype, {
  get cssText() { return ruleText(ruleOf(this)); },
  set cssText(v) {},
  get type() { return ruleOf(this).kind; },
  get parentRule() { return null; },
  get parentStyleSheet() { var r = ruleOf(this); return r.sheet ? r.sheet.obj : null; }
});
mix(CSSStyleRule.prototype, {
  get selectorText() { return ruleOf(this).sel; },
  set selectorText(v) { var r = ruleOf(this); r.sel = str(v); ruleChanged(r); },
  get style() {
    var r = ruleOf(this);
    if (!r.decl) r.decl = makeDecl({ el: null, list: null, stale: false, ro: false, computed: false, rule: r });
    return r.decl;
  },
  get styleMap() { return { get: function () { return null; }, set: function () {}, has: function () { return false; } }; }
});
mix(CSSMediaRule.prototype, {
  get media() {
    var r = ruleOf(this);
    var m = /^@media\s*([^{]*)/i.exec(r.text);
    return makeMediaList(function () { return m ? stripWS(m[1]) : ''; }, function () {});
  },
  get conditionText() { var m = /^@media\s*([^{]*)/i.exec(ruleOf(this).text); return m ? stripWS(m[1]) : ''; },
  get cssRules() { return ruleListOf(innerRules(ruleOf(this))); }
});
mix(CSSSupportsRule.prototype, {
  get conditionText() { var m = /^@supports\s*([^{]*)/i.exec(ruleOf(this).text); return m ? stripWS(m[1]) : ''; },
  get cssRules() { return ruleListOf(innerRules(ruleOf(this))); }
});
mix(CSSKeyframesRule.prototype, {
  get name() { var m = /^@(?:-webkit-)?keyframes\s+([^\s{]+)/i.exec(ruleOf(this).text); return m ? m[1] : ''; },
  get cssRules() { return ruleListOf([]); },
  appendRule: function () {}, deleteRule: function () {}, findRule: function () { return null; }
});
mix(CSSImportRule.prototype, {
  get href() { var m = /url\(\s*['"]?([^'")]+)|['"]([^'"]+)['"]/.exec(ruleOf(this).text); return m ? (m[1] || m[2]) : ''; },
  get styleSheet() { return null; },
  get media() { return makeMediaList(function () { return ''; }, function () {}); }
});
function innerRules(rec) {
  var t = rec.text;
  var a = t.indexOf('{'), b = t.lastIndexOf('}');
  if (a < 0 || b <= a) return [];
  return splitRules(t.slice(a + 1, b)).map(function (x) { return makeRule(x, null); });
}
function ruleListOf(recs) {
  var o = objCreate(CSSRuleList.prototype);
  for (var i = 0; i < recs.length; i++) defineProperty(o, i, { value: recs[i].obj, enumerable: true, configurable: true });
  defineProperty(o, 'length', { value: recs.length, configurable: true });
  return o;
}
mix(CSSRuleList.prototype, { item: function (i) { return this[i >>> 0] || null; } });
defineProperty(CSSRuleList.prototype, Symbol.iterator, { value: Array.prototype[Symbol.iterator], writable: true, configurable: true });

var StyleSheet = iface('StyleSheet', null);
function SheetCtor(options) {
  if (!new.target) throw new TypeError("Failed to construct 'CSSStyleSheet': Please use the 'new' operator.");
  var s = newSheet(null, '', null);
  s.constructed = true;
  if (options && options.media !== undefined) s.media = typeof options.media === 'string' ? options.media : str(options.media.mediaText || '');
  if (options && options.disabled) s.disabled = true;
  bindSheet(this, s);
}
var CSSStyleSheet = iface('CSSStyleSheet', StyleSheet, SheetCtor);
function newSheet(owner, text, href) {
  return { owner: owner, rules: null, text: text, href: href, dirty: false, obj: null, media: '', disabled: false, title: null, constructed: false };
}
function bindSheet(o, s) {
  defineProperty(o, SH, { value: s, configurable: true });
  s.obj = o;
  return o;
}
function sheetState(o) {
  var s = o !== null && o !== undefined ? o[SH] : undefined;
  if (!s || s.get) throw new TypeError('Illegal invocation');
  return s;
}
function sheetRules(s) {
  if (!s.rules) s.rules = splitRules(s.text || '').map(function (t) { return makeRule(t, s); });
  return s.rules;
}
function sheetCSSText(s) { return sheetRules(s).map(ruleText).join('\n'); }
mix(StyleSheet.prototype, {
  get type() { return 'text/css'; },
  get href() { return sheetState(this).href; },
  get ownerNode() { var s = sheetState(this); return s.owner ? wrap(s.owner) : null; },
  get parentStyleSheet() { return null; },
  get title() { var s = sheetState(this); return s.owner ? getAttrRaw(s.owner, 'title') : null; },
  get media() {
    var s = sheetState(this);
    return makeMediaList(function () { return s.owner ? (getAttrRaw(s.owner, 'media') || '') : s.media; },
      function (v) { if (s.owner) setAttrRaw(s.owner, 'media', v); else s.media = v; });
  },
  get disabled() { return !!sheetState(this).disabled; },
  set disabled(v) { sheetState(this).disabled = !!v; cssVersion++; }
});
mix(CSSStyleSheet.prototype, {
  get cssRules() { return ruleListOf(sheetRules(sheetState(this))); },
  get rules() { return ruleListOf(sheetRules(sheetState(this))); },
  get ownerRule() { return null; },
  insertRule: function (rule, index) {
    var s = sheetState(this);
    var rules = sheetRules(s);
    index = index === undefined ? 0 : index >>> 0;
    if (index > rules.length) throw domErr('IndexSizeError', "Failed to execute 'insertRule' on 'CSSStyleSheet': The index provided (" + index + ') is larger than the maximum index (' + rules.length + ').');
    var text = stripWS(str(rule));
    var parts = splitRules(text);
    if (parts.length !== 1) throw domErr('SyntaxError', "Failed to execute 'insertRule' on 'CSSStyleSheet': Failed to parse the rule '" + text + "'.");
    rules.splice(index, 0, makeRule(parts[0], s));
    s.dirty = true;
    cssVersion++;
    return index;
  },
  deleteRule: function (index) {
    var s = sheetState(this);
    var rules = sheetRules(s);
    index = index >>> 0;
    if (index >= rules.length) throw domErr('IndexSizeError', "Failed to execute 'deleteRule' on 'CSSStyleSheet': The index provided (" + index + ') is larger than the maximum index (' + (rules.length - 1) + ').');
    rules.splice(index, 1);
    s.dirty = true;
    cssVersion++;
  },
  addRule: function (selector, style, index) {
    var rules = sheetRules(sheetState(this));
    this.insertRule(str(selector) + ' { ' + (style === undefined ? '' : str(style)) + ' }', index === undefined ? rules.length : index);
    return -1;
  },
  removeRule: function (index) { this.deleteRule(index === undefined ? 0 : index); },
  replaceSync: function (text) {
    var s = sheetState(this);
    if (!s.constructed) throw domErr('NotAllowedError', "Failed to execute 'replaceSync' on 'CSSStyleSheet': Can't call replaceSync on non-constructed CSSStyleSheets.");
    s.text = strOrEmpty(text).replace(/@import[^;]*;/gi, '');
    s.rules = null;
    s.dirty = true;
    cssVersion++;
  },
  replace: function (text) {
    var self = this;
    try {
      self.replaceSync(text);
      return Promise.resolve(self);
    } catch (e) {
      return Promise.reject(e);
    }
  }
});

// <style>: the sheet is rebuilt from the element's text unless CSSOM changed it since.
function sheetOfStyle(r) {
  if (!r.c) return null;
  var text = childText(r);
  var s = r.sheet;
  if (!s || (!s.dirty && s.text !== text)) {
    s = r.sheet = newSheet(r, text, null);
    bindSheet(objCreate(CSSStyleSheet.prototype), s);
  } else if (s.dirty && s.srcText !== undefined && s.srcText !== text) {
    s = r.sheet = newSheet(r, text, null);
    bindSheet(objCreate(CSSStyleSheet.prototype), s);
  }
  if (s.srcText === undefined) s.srcText = text;
  return s.obj;
}
function sheetOfLink(r) {
  if (!r.c) return null;
  var rel = asciiLower(getAttrRaw(r, 'rel') || '');
  if (splitTokens(rel).indexOf('stylesheet') < 0) return null;
  if (!r.sheet) {
    var href = getAttrRaw(r, 'href');
    r.sheet = newSheet(r, '', href === null ? null : (resolveURL(r.d, href) || href));
    bindSheet(objCreate(CSSStyleSheet.prototype), r.sheet);
  }
  return r.sheet.obj;
}
// What the serializer writes for a <style> whose rules CSSOM changed; null when the text is current.
function sheetTextFor(r) {
  var s = r.sheet;
  if (!s || !s.dirty || s.owner !== r) return null;
  var text = childText(r);
  if (s.srcText !== undefined && s.srcText !== text) return null;
  return sheetCSSText(s);
}
function adoptedSheetsText(d) {
  if (!d || !d.adopted || !d.adopted.length) return '';
  var out = [];
  for (var i = 0; i < d.adopted.length; i++) {
    var s = d.adopted[i] && d.adopted[i][SH];
    if (s && !s.get && !s.disabled) out.push(sheetCSSText(s));
  }
  return out.join('\n');
}

var StyleSheetList = iface('StyleSheetList', null);
mix(StyleSheetList.prototype, { item: function (i) { return this[i >>> 0] || null; } });
defineProperty(StyleSheetList.prototype, Symbol.iterator, { value: Array.prototype[Symbol.iterator], writable: true, configurable: true });
function styleSheetsOf(root) {
  var list = [];
  for (var e = nextElement(root, root); e; e = nextElement(e, root)) {
    if (e.ns !== 0) continue;
    var sh = e.ln === 'style' ? sheetOfStyle(e) : e.ln === 'link' ? sheetOfLink(e) : null;
    if (sh) list.push(sh);
  }
  var o = objCreate(StyleSheetList.prototype);
  for (var i = 0; i < list.length; i++) defineProperty(o, i, { value: list[i], enumerable: true, configurable: true });
  defineProperty(o, 'length', { value: list.length, configurable: true });
  return o;
}
function adoptedAccessor(getRec) {
  return {
    get: function () {
      var r = getRec(this);
      if (!r.adopted) r.adopted = [];
      return r.adopted;
    },
    set: function (v) {
      var r = getRec(this);
      var arr = [];
      if (v !== null && v !== undefined) {
        for (var i = 0; i < v.length; i++) {
          if (!v[i] || !v[i][SH]) throw new TypeError("Failed to set the 'adoptedStyleSheets' property: Failed to convert value to 'CSSStyleSheet'.");
          arr.push(v[i]);
        }
      }
      r.adopted = arr;
      cssVersion++;
    },
    enumerable: true, configurable: true
  };
}
defineProperty(Document.prototype, 'styleSheets', { get: function () { return styleSheetsOf(documentOf(this)); }, enumerable: true, configurable: true });
defineProperty(Document.prototype, 'adoptedStyleSheets', adoptedAccessor(function (o) { return documentOf(o); }));

// ---------------------------------------------------------------- fonts (CSS Font Loading API stand-in)

// Pages wait on document.fonts.ready before showing text; the reader draws with its own face, so every
// font is "loaded" at once.
function FontFace(family, source, descriptors) {
  if (!new.target) throw new TypeError("Failed to construct 'FontFace': Please use the 'new' operator.");
  this.family = str(family);
  this.style = (descriptors && descriptors.style) || 'normal';
  this.weight = (descriptors && descriptors.weight) || 'normal';
  this.stretch = (descriptors && descriptors.stretch) || 'normal';
  this.unicodeRange = (descriptors && descriptors.unicodeRange) || 'U+0-10FFFF';
  this.display = (descriptors && descriptors.display) || 'auto';
  this.status = 'loaded';
  this.loaded = Promise.resolve(this);
}
iface('FontFace', null, FontFace);
FontFace.prototype.load = function () { return Promise.resolve(this); };
var FontFaceSet = iface('FontFaceSet', EventTarget);
var fontSetObj = null;
function fontSet() {
  if (!fontSetObj) {
    var faces = [];
    fontSetObj = objCreate(FontFaceSet.prototype);
    defineProperty(fontSetObj, 'ready', { value: Promise.resolve(fontSetObj), enumerable: true });
    mix(fontSetObj, {
      get status() { return 'loaded'; },
      get size() { return faces.length; },
      add: function (f) { if (faces.indexOf(f) < 0) faces.push(f); return this; },
      delete: function (f) { var i = faces.indexOf(f); if (i >= 0) faces.splice(i, 1); return i >= 0; },
      has: function (f) { return faces.indexOf(f) >= 0; },
      clear: function () { faces.length = 0; },
      check: function () { return true; },
      load: function () { return Promise.resolve([]); },
      forEach: function (cb, thisArg) { faces.slice().forEach(function (f) { cb.call(thisArg, f, f, fontSetObj); }); },
      values: function () { return faces.slice()[Symbol.iterator](); }
    });
    defineProperty(fontSetObj, Symbol.iterator, { value: function () { return faces.slice()[Symbol.iterator](); }, configurable: true });
    defineEventHandlers(fontSetObj, ['loading', 'loadingdone', 'loadingerror']);
  }
  return fontSetObj;
}
defineProperty(Document.prototype, 'fonts', { get: function () { documentOf(this); return fontSet(); }, enumerable: true, configurable: true });

// ---------------------------------------------------------------- CSS namespace

function cssEscape(value) {
  var s = str(value);
  var out = '';
  for (var i = 0; i < s.length; i++) {
    var c = s.charCodeAt(i);
    if (c === 0) { out += '\uFFFD'; continue; }
    if ((c >= 1 && c <= 31) || c === 127 || (i === 0 && c >= 48 && c <= 57) ||
        (i === 1 && c >= 48 && c <= 57 && s.charCodeAt(0) === 45)) {
      out += '\\' + c.toString(16) + ' ';
      continue;
    }
    if (i === 0 && c === 45 && s.length === 1) { out += '\\-'; continue; }
    if (c >= 128 || c === 45 || c === 95 || (c >= 48 && c <= 57) || (c >= 65 && c <= 90) || (c >= 97 && c <= 122)) {
      out += s.charAt(i);
      continue;
    }
    out += '\\' + s.charAt(i);
  }
  return out;
}
function cssSupports(a, b) {
  if (b !== undefined) {
    var prop = stripWS(str(a));
    return prop.slice(0, 2) === '--' || !!CSS_KNOWN[asciiLower(prop)];
  }
  var cond = stripWS(str(a));
  var sel = /^selector\(([\s\S]*)\)$/i.exec(cond);
  if (sel) {
    try { compileSelector(sel[1]); return true; } catch (e) { return false; }
  }
  var m = /\(\s*([-\w]+)\s*:/.exec(cond);
  if (m) return m[1].slice(0, 2) === '--' || !!CSS_KNOWN[asciiLower(m[1])];
  return false;
}
var CSSNamespace = {
  supports: cssSupports,
  escape: cssEscape,
  registerProperty: function () {},
  highlights: new Map(),
  px: function (n) { return { value: +n, unit: 'px', toString: function () { return n + 'px'; } }; }
};
defineGlobal('CSS', CSSNamespace);

if (testHooks) {
  testHooks.parseDeclarations = parseDeclarations;
  testHooks.splitRules = splitRules;
}
