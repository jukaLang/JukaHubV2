// 35_serialize.js — HTML serialization: innerHTML/outerHTML, and the document the renderer draws.
//
// serializeForRenderer is what the script pass hands back to the reader (webpage.go parses it like any
// page): the DOM as scripts left it, with a few things a browser shows but markup alone does not —
// shadow roots flattened in place, lazy images promoted from data-src, form state written into
// attributes, CSSOM-only rules written into their <style>. Things the reader never draws (script bodies,
// template contents, comments, noscript) are left out to keep the handheld's parse small.
//
// Both walks are iterative: page scripts can build trees deeper than the interpreter's call stack.

var VOID_ELEMENTS = { area: 1, base: 1, basefont: 1, bgsound: 1, br: 1, col: 1, embed: 1, frame: 1, hr: 1, img: 1,
  input: 1, keygen: 1, link: 1, meta: 1, param: 1, source: 1, track: 1, wbr: 1 };
var RAW_TEXT_PARENTS = { style: 1, script: 1, xmp: 1, iframe: 1, noembed: 1, noframes: 1, plaintext: 1, noscript: 1 };

var TEXT_ESC = /[&<>\u00A0]/g;
// Attribute values escape '<' and '>' as well as '&', '"' and NBSP: that is what the HTML serialization
// algorithm now specifies and what browsers emit, and it keeps a value like title="<b>" from being read
// back as markup by the reader's tag scanner or by a page's own innerHTML round trip.
var ATTR_ESC = /[&"<>\u00A0]/g;
function escChar(c) {
  switch (c) {
    case '&': return '&amp;';
    case '<': return '&lt;';
    case '>': return '&gt;';
    case '"': return '&quot;';
    case '\u00A0': return '&nbsp;';
  }
  return c;
}
// The tests are indexOf scans rather than a regex test: in goja every regex call converts its whole subject to
// runes first, and most text and attribute values need no escaping at all.
function escapeText(s) {
  if (s.indexOf('&') < 0 && s.indexOf('<') < 0 && s.indexOf('>') < 0 && s.indexOf('\u00A0') < 0) return s;
  return s.replace(TEXT_ESC, escChar);
}
function escapeAttr(s) {
  if (s.indexOf('&') < 0 && s.indexOf('"') < 0 && s.indexOf('<') < 0 && s.indexOf('>') < 0 && s.indexOf('\u00A0') < 0) return s;
  return s.replace(ATTR_ESC, escChar);
}

function serName(r) { return r.ns === 0 ? r.ln : qualifiedName(r); }

// RENDER_UNSAFE_NAME: attribute names the reader's tag scanner would misread. HTML lets a name hold a quote
// or '<' (`<div a'b=1>`), and webpage.go's scanner takes any quote as the start of a value, so one such
// name in the output could swallow the rest of the page into a single tag. The renderer has no use for them.
var RENDER_UNSAFE_NAME = /[\x00-\x20"'<>\/=\x7f]/;
// Pages use few distinct attribute names, so the verdict is cached (bounded) instead of a regex per attribute.
var renderNameVerdict = objCreate(null);
var renderNameVerdicts = 0;
function renderNameSafe(name) {
  var v = renderNameVerdict[name];
  if (v !== undefined) return v;
  v = !RENDER_UNSAFE_NAME.test(name);
  if (renderNameVerdicts >= 2048) {
    renderNameVerdict = objCreate(null);
    renderNameVerdicts = 0;
  }
  renderNameVerdict[name] = v;
  renderNameVerdicts++;
  return v;
}

function serAttrs(out, a, render) {
  if (!a) return;
  for (var i = 0; i < a.length; i += 2) {
    if (render && !renderNameSafe(a[i])) continue;
    out.push(' ', a[i], '="', escapeAttr(a[i + 1]), '"');
  }
}

// rawTextForRenderer keeps raw text (a <style> body) from closing its element early: CSS can hold "</style>"
// inside a string or comment, which the reader's scanner would take for the end tag and then draw the rest
// as text. "<\/" means the same to CSS.
var RAW_END_TAG = /<\/([A-Za-z])/g;
function rawTextForRenderer(text) {
  return text.indexOf('</') < 0 ? text : text.replace(RAW_END_TAG, '<\\/$1');
}

// shadowStyleElement: a <style> or stylesheet <link> inside a shadow tree. Its rules are scoped to the
// component (:host, ::slotted, plain tag selectors meant for the shadow tree only); flattened into the page,
// the reader would apply them everywhere, so they are left out of its copy.
var STYLESHEET_REL = /(^|[\t\n\f\r ])stylesheet([\t\n\f\r ]|$)/i;
function shadowStyleElement(k) {
  if (k.ns !== 0 || (k.ln !== 'style' && k.ln !== 'link')) return false;
  if (k.ln === 'link' && !STYLESHEET_REL.test(getAttrRaw(k, 'rel') || '')) return false;
  return inShadowTree(k);
}

// kidsOf: the children a serialization walk visits. render → the flat tree (shadow roots, slots).
function kidsOf(r, render) {
  if (r.t === 1) {
    if (r.tc) return render ? [] : childrenArray(r.tc);
    if (render) {
      if (r.sr) return childrenArray(r.sr);
      if (r.ln === 'slot' && r.ns === 0 && inShadowTree(r)) {
        var assigned = slotAssigned(r);
        return assigned.length ? assigned : childrenArray(r);
      }
      if (r.ns === 0 && (r.ln === 'script' || r.ln === 'noscript')) return [];
    }
  }
  return childrenArray(r);
}

// serializeWalk writes the children of parent (or parent itself when self) into out.
function serializeWalk(out, parent, render, self) {
  var stack;
  if (self) {
    stack = [{ r: null, kids: [parent], i: 0 }];
  } else {
    stack = [{ r: parent, kids: kidsOf(parent, render), i: 0 }];
  }
  while (stack.length) {
    var fr = stack[stack.length - 1];
    if (fr.i >= fr.kids.length) {
      stack.pop();
      if (stack.length && fr.r) out.push('</', serName(fr.r), '>');
      continue;
    }
    var k = fr.kids[fr.i++];
    switch (k.t) {
      case 1: {
        if (render && shadowStyleElement(k)) break;
        var name = serName(k);
        out.push('<', name);
        var attrs = render ? renderAttrs(k) : k.a;
        serAttrs(out, attrs, render);
        out.push('>');
        if (k.ns === 0 && VOID_ELEMENTS[k.ln]) break;
        if (render) {
          var special = renderSpecialContent(k);
          if (special !== null) {
            out.push(k.ln === 'style' && k.ns === 0 ? rawTextForRenderer(special) : special, '</', name, '>');
            break;
          }
          if (k.ns === 0 && k.ln === 'head') {
            var adopted = adoptedSheetsText(k.d);
            if (adopted) out.push('<style>', adopted, '</style>');
          }
        }
        var kk = kidsOf(k, render);
        if (!kk.length) {
          out.push('</', name, '>');
          break;
        }
        stack.push({ r: k, kids: kk, i: 0 });
        break;
      }
      case 3:
      case 4: {
        var pr = fr.r || k.p;
        var raw = pr && pr.t === 1 && pr.ns === 0 && RAW_TEXT_PARENTS[pr.ln];
        out.push(raw ? (render ? rawTextForRenderer(k.x) : k.x) : escapeText(k.x));
        break;
      }
      case 8:
        if (!render) out.push('<!--', k.x, '-->');
        break;
      case 7:
        if (!render) out.push('<?', k.ln, ' ', k.x, '>');
        break;
      case 10:
        out.push('<!DOCTYPE ', k.ln, '>');
        break;
      case 11:
        // A fragment as "self" (outerHTML of a shadow root is not a thing; innerHTML walks children).
        var fk = kidsOf(k, render);
        if (fk.length) stack.push({ r: null, kids: fk, i: 0 });
        break;
    }
  }
}

function serializeChildren(r) {
  var out = [];
  serializeWalk(out, r.t === 11 || r.t === 9 || r.t === 1 ? r : r, false, false);
  return out.join('');
}
function serializeNode(r) {
  var out = [];
  serializeWalk(out, r, false, true);
  return out.join('');
}

// serialPass numbers renderer passes so per-<select> facts are computed once per pass, not per option.
var serialPass = 0;

function serializeForRenderer(d) {
  var out = [];
  serialPass++;
  serializeWalk(out, d || mainDoc, true, false);
  return out.join('');
}

// ---------------------------------------------------------------- renderer tweaks

var LAZY_SRC_ATTRS = ['data-src', 'data-lazy-src', 'data-original', 'data-lazy', 'data-url', 'data-echo',
  'data-hi-res-src', 'data-actualsrc', 'data-full-src', 'data-img-src', 'data-lazyload', 'data-defer-src',
  'data-ll-src', 'data-orig-src', 'data-src-retina', 'data-pagespeed-lazy-src', 'data-flickity-lazyload'];
var LAZY_SRCSET_ATTRS = ['data-srcset', 'data-lazy-srcset', 'data-original-srcset', 'data-ll-srcset'];
var PLACEHOLDER_NAME = /(^|[\/_.\-])(blank|spacer|placeholder|pixel|transparent|loading|loader|lazy|lazyload|grey|gray|empty|1x1|clear|dummy|trans)([\/_.\-]|$)/i;

var DATA_URL_PREFIX = /^data:/i;
var QUERY_OR_HASH = /[?#].*$/;
function isPlaceholderSrc(v) {
  if (v === null) return true;
  v = stripWS(v);
  if (v === '' || v === '#' || v === 'about:blank' || v === '//:0') return true;
  if (DATA_URL_PREFIX.test(v)) return true;
  return PLACEHOLDER_NAME.test(v.replace(QUERY_OR_HASH, ''));
}

function attrsWith(a, name, value) {
  var out = a ? a.slice() : [];
  for (var i = 0; i < out.length; i += 2) {
    if (out[i] === name) {
      if (value === null) out.splice(i, 2);
      else out[i + 1] = value;
      return out;
    }
  }
  if (value !== null) out.push(name, value);
  return out;
}
function firstAttr(r, names) {
  for (var i = 0; i < names.length; i++) {
    var v = getAttrRaw(r, names[i]);
    if (v !== null && stripWS(v) !== '') return v;
  }
  return null;
}

function renderAttrs(r) {
  var a = r.a;
  if (r.ns !== 0) return a;
  var v;
  switch (r.ln) {
    case 'img':
    case 'iframe':
    case 'video':
    case 'audio':
    case 'embed':
      v = firstAttr(r, LAZY_SRC_ATTRS);
      if (v !== null && isPlaceholderSrc(getAttrRaw(r, 'src'))) a = attrsWith(a, 'src', v);
      if (r.ln === 'img') {
        var ss = firstAttr(r, LAZY_SRCSET_ATTRS);
        if (ss !== null && isPlaceholderSrc(getAttrRaw(r, 'srcset'))) a = attrsWith(a, 'srcset', ss);
      }
      if (r.ln === 'video') {
        var poster = getAttrRaw(r, 'data-poster');
        if (poster && isPlaceholderSrc(getAttrRaw(r, 'poster'))) a = attrsWith(a, 'poster', poster);
      }
      return a;
    case 'source':
      v = firstAttr(r, LAZY_SRCSET_ATTRS);
      if (v !== null && isPlaceholderSrc(getAttrRaw(r, 'srcset'))) a = attrsWith(a, 'srcset', v);
      v = firstAttr(r, LAZY_SRC_ATTRS);
      if (v !== null && isPlaceholderSrc(getAttrRaw(r, 'src'))) a = attrsWith(a, 'src', v);
      return a;
    case 'input': {
      var type = inputType(r);
      if (r.fv !== undefined && type !== 'password' && type !== 'file') a = attrsWith(a, 'value', r.fv);
      if (r.fc !== undefined && (type === 'checkbox' || type === 'radio')) a = attrsWith(a, 'checked', r.fc ? '' : null);
      return a;
    }
    case 'option': {
      var sel = selectOf(r);
      if (sel) {
        if (sel.serPass !== serialPass) {
          sel.serPass = serialPass;
          sel.serSelected = selectHasDirtyOptions(sel) ? selectedOptions(sel) : null;
        }
        if (sel.serSelected) a = attrsWith(a, 'selected', sel.serSelected.indexOf(r) >= 0 ? '' : null);
      } else if (r.sel !== undefined) {
        a = attrsWith(a, 'selected', r.sel ? '' : null);
      }
      return a;
    }
  }
  return a;
}
function selectHasDirtyOptions(sel) {
  var opts = selectOptions(sel);
  for (var i = 0; i < opts.length; i++) if (opts[i].sel !== undefined) return true;
  return false;
}

// Content the renderer gets instead of the element's children, or null for the normal walk.
function renderSpecialContent(r) {
  if (r.ns !== 0) return null;
  if (r.ln === 'textarea' && r.fv !== undefined) return escapeText(r.fv);
  if (r.ln === 'style') {
    var css = sheetTextFor(r);
    if (css !== null) return css;
  }
  return null;
}

if (testHooks) {
  testHooks.serializeForRenderer = serializeForRenderer;
  testHooks.serializeChildren = serializeChildren;
  testHooks.serializeNode = serializeNode;
}
