// 20_nodes.js — node records, wrappers, the tree, and the Node / Document / CharacterData interfaces.
//
// A node is a plain record; page scripts get a wrapper object created on first touch and cached in r.o.
// Record fields: t nodeType, d node document (a document's d is itself), p parent, f/l first/last child,
// n/v next/previous sibling, c connected, o wrapper; elements add ln local name, ns namespace code
// (0 HTML, 1 SVG, 2 MathML, 3 other with nsu), px prefix, a flat attributes [name, value, …] or null;
// character data adds x. Parents keep kv, a child-list version for the live NodeList caches, and cc/ec,
// their child and element-child counts. Documents add url, ids (id → connected elements), ver (any
// mutation), trv (tree mutations only), av (attribute name → change count), bv (base-element changes),
// ready, cur (currentScript), tdoc (inert template-contents document), html (HTML document flag), main.
// The split versions let a live collection or a cache say exactly which changes it depends on: a
// getElementsByTagName list does not rebuild because a script wrote a style or a data-* attribute.

var ELEMENT_NODE = 1, ATTRIBUTE_NODE = 2, TEXT_NODE = 3, CDATA_SECTION_NODE = 4,
  PROCESSING_INSTRUCTION_NODE = 7, COMMENT_NODE = 8, DOCUMENT_NODE = 9, DOCUMENT_TYPE_NODE = 10,
  DOCUMENT_FRAGMENT_NODE = 11;

var NS_HTML = 'http://www.w3.org/1999/xhtml';
var NS_SVG = 'http://www.w3.org/2000/svg';
var NS_MATHML = 'http://www.w3.org/1998/Math/MathML';
var NS_XLINK = 'http://www.w3.org/1999/xlink';
var NS_XML = 'http://www.w3.org/XML/1998/namespace';
var NS_XMLNS = 'http://www.w3.org/2000/xmlns/';

function nsCode(uri) {
  if (uri === NS_HTML) return 0;
  if (uri === NS_SVG) return 1;
  if (uri === NS_MATHML) return 2;
  return 3;
}
function nsURI(r) {
  switch (r.ns) {
    case 0: return NS_HTML;
    case 1: return NS_SVG;
    case 2: return NS_MATHML;
    default: return r.nsu === undefined ? null : r.nsu;
  }
}

// Filled by 25_elements.js: local name → prototype for HTML and SVG elements.
var HTML_PROTOS = objCreate(null);
var SVG_PROTOS = objCreate(null);

// ---------------------------------------------------------------- records

// cssVersion counts changes that can alter the set of in-page style rules (a <style> or <link> inserted or
// removed, a style element's text, their media/rel/href/disabled attributes, CSSOM edits); the
// getComputedStyle cascade (45_css.js) rebuilds its rule index when it moves.
var cssVersion = 0;

function newDocRec(url, xml) {
  var d = {
    t: 9, d: null, p: null, f: null, l: null, n: null, v: null, c: true, o: null, kv: 0,
    url: url || 'about:blank', ids: objCreate(null), ver: 0, ready: 'complete', cur: null,
    tdoc: null, html: !xml, main: false, mode: 'no-quirks', active: null, baseVer: -1, baseURL: ''
  };
  d.d = d;
  return d;
}

// The inert document that owns <template> contents (so nothing in them runs or connects).
function templateDoc(d) {
  if (d.inert) return d;
  if (!d.tdoc) {
    d.tdoc = newDocRec(d.url, !d.html);
    d.tdoc.inert = true;
    d.tdoc.c = false;
  }
  return d.tdoc;
}

function newFragmentRec(d) {
  return { t: 11, d: d, p: null, f: null, l: null, n: null, v: null, c: false, o: null, kv: 0 };
}

function createElementRec(d, ln, ns) {
  var r = { t: 1, d: d, p: null, f: null, l: null, n: null, v: null, c: false, o: null, kv: 0, ln: ln, ns: ns | 0, a: null };
  if (r.ns === 0) {
    if (ln === 'template') {
      r.tc = newFragmentRec(templateDoc(d));
      r.tc.host = r;
    } else if (ln.indexOf('-') > 0 && validCustomElementName(ln)) {
      r.ce = 'undefined';
    }
  }
  return r;
}

function textRec(d, x) { return { t: 3, d: d, p: null, n: null, v: null, c: false, o: null, x: x }; }
function commentRec(d, x) { return { t: 8, d: d, p: null, n: null, v: null, c: false, o: null, x: x }; }
function cdataRec(d, x) { return { t: 4, d: d, p: null, n: null, v: null, c: false, o: null, x: x }; }
function piRec(d, target, x) { return { t: 7, d: d, p: null, n: null, v: null, c: false, o: null, ln: target, x: x }; }
function doctypeRec(d, name, pub, sys) {
  return { t: 10, d: d, p: null, n: null, v: null, c: false, o: null, ln: name || '', pub: pub || '', sys: sys || '' };
}

function docOf(r) { return r.t === 9 ? r : r.d; }

// ---------------------------------------------------------------- wrappers

function protoFor(r) {
  switch (r.t) {
    case 1:
      if (r.ns === 0) {
        var p = HTML_PROTOS[r.ln];
        if (p) return p;
        return r.ce !== undefined ? HTMLElement.prototype : HTMLUnknownElement.prototype;
      }
      if (r.ns === 1) return SVG_PROTOS[r.ln] || SVGElement.prototype;
      if (r.ns === 2) return MathMLElement.prototype;
      return Element.prototype;
    case 2: return Attr.prototype;
    case 3: return Text.prototype;
    case 4: return CDATASection.prototype;
    case 7: return ProcessingInstruction.prototype;
    case 8: return Comment.prototype;
    case 9: return r.html ? HTMLDocument.prototype : XMLDocument.prototype;
    case 10: return DocumentType.prototype;
    case 11: return r.shadow ? ShadowRoot.prototype : DocumentFragment.prototype;
  }
  return Node.prototype;
}

function bindWrapper(o, r) {
  defineProperty(o, $, { value: r, configurable: true });
  r.o = o;
  return o;
}

function wrap(r) {
  if (r === null || r === undefined) return null;
  var o = r.o;
  if (o) return o;
  return bindWrapper(objCreate(protoFor(r)), r);
}

// ---------------------------------------------------------------- tree walking

// Tree-order successor of r inside root (root itself excluded), light tree only.
function nextNode(r, root) {
  if (r.f) return r.f;
  while (r && r !== root) {
    if (r.n) return r.n;
    r = r.p;
  }
  return null;
}
// Successor that skips r's descendants.
function nextSkipping(r, root) {
  while (r && r !== root) {
    if (r.n) return r.n;
    r = r.p;
  }
  return null;
}
function nextElement(r, root) {
  do { r = nextNode(r, root); } while (r && r.t !== 1);
  return r;
}
// Tree-order predecessor inside root (root excluded).
function prevNode(r, root) {
  if (r === root) return null;
  var s = r.v;
  if (s) {
    while (s.l) s = s.l;
    return s;
  }
  return r.p === root ? null : r.p;
}

function childrenArray(r) {
  var out = [];
  for (var k = r.f; k; k = k.n) out.push(k);
  return out;
}
function elementChildren(r) {
  var out = [];
  for (var k = r.f; k; k = k.n) if (k.t === 1) out.push(k);
  return out;
}
function firstElementChild(r) {
  for (var k = r.f; k; k = k.n) if (k.t === 1) return k;
  return null;
}
function childIndex(r) {
  var i = 0;
  for (var k = r.v; k; k = k.v) i++;
  return i;
}
function treeRoot(r) {
  while (r.p) r = r.p;
  return r;
}
function inShadowTree(r) {
  var root = treeRoot(r);
  return !!root.shadow;
}
function isInclusiveAncestor(a, r) {
  for (; r; r = r.p) if (r === a) return true;
  return false;
}
function hostIncludingInclusiveAncestor(a, r) {
  while (r) {
    if (r === a) return true;
    r = r.p || (r.t === 11 && r.host) || null;
  }
  return false;
}

// ---------------------------------------------------------------- id index

function idAdd(d, id, r) {
  var l = d.ids[id];
  if (!l) {
    d.ids[id] = [r];
    if (d === mainDoc && windowNamed) namedAdd(id);
  } else if (l.indexOf(r) < 0) {
    l.push(r);
  }
}
function idRemove(d, id, r) {
  var l = d.ids[id];
  if (!l) return;
  var i = l.indexOf(r);
  if (i >= 0) l.splice(i, 1);
  if (!l.length) {
    delete d.ids[id];
    if (d === mainDoc && windowNamed && hasOwn.call(windowNamed, id)) delete windowNamed[id];
  }
}

// Window named access: old scripts use an element's id as a global (`submitButton.disabled = true`).
// Browsers expose those through an object between Window.prototype and EventTarget.prototype; this is
// that object, fed by the id index, so a page's own globals (and window's members) still win.
var windowNamed = null;
var NAMED_ID = /^[A-Za-z_$][\w$]*$/;
function setupWindowNamed() {
  if (windowNamed || typeof Window !== 'function' || getProto(global) !== Window.prototype) return;
  try {
    windowNamed = objCreate(getProto(Window.prototype));
    setProto(Window.prototype, windowNamed);
  } catch (e) {
    windowNamed = null;
  }
}
function namedAdd(id) {
  if (!NAMED_ID.test(id) || id in EventTarget.prototype || hasOwn.call(windowNamed, id)) return;
  defineProperty(windowNamed, id, {
    get: function () {
      var l = mainDoc && mainDoc.ids[id];
      return l && l.length ? wrap(l.length === 1 ? l[0] : firstInTreeOrder(l)) : undefined;
    },
    set: function (v) { defineProperty(global, id, { value: v, writable: true, enumerable: true, configurable: true }); },
    enumerable: false, configurable: true
  });
}
// First element of list in tree order (pages with duplicate ids are common).
function firstInTreeOrder(list) {
  var best = list[0];
  for (var i = 1; i < list.length; i++) if (precedes(list[i], best)) best = list[i];
  return best;
}
// precedes reports whether a comes before b in tree order (same tree).
function precedes(a, b) {
  if (a === b) return false;
  var pa = [], pb = [];
  for (var x = a; x; x = x.p) pa.push(x);
  for (var y = b; y; y = y.p) pb.push(y);
  var i = pa.length - 1, j = pb.length - 1;
  if (pa[i] !== pb[j]) return false;
  while (i >= 0 && j >= 0 && pa[i] === pb[j]) { i--; j--; }
  if (i < 0) return true;   // a is an ancestor of b
  if (j < 0) return false;  // b is an ancestor of a
  for (var s = pa[i].n; s; s = s.n) if (s === pb[j]) return true;
  return false;
}

// ---------------------------------------------------------------- attributes

function attrName(r, name) { return r.ns === 0 && r.d.html ? asciiLower(name) : name; }

function getAttrRaw(r, name) {
  var a = r.a;
  if (!a) return null;
  for (var i = 0; i < a.length; i += 2) if (a[i] === name) return a[i + 1];
  return null;
}
function getAttr(r, name) { return r.t === 1 ? getAttrRaw(r, attrName(r, str(name))) : null; }
function hasAttr(r, name) { return getAttr(r, name) !== null; }

function setAttrRaw(r, name, value) {
  var a = r.a, old = null, found = false;
  if (!a) a = r.a = [];
  for (var i = 0; i < a.length; i += 2) {
    if (a[i] === name) {
      old = a[i + 1];
      a[i + 1] = value;
      found = true;
      break;
    }
  }
  if (!found) a.push(name, value);
  attrChanged(r, name, old, value);
}
function setAttr(r, name, value) { setAttrRaw(r, attrName(r, str(name)), str(value)); }

function removeAttrRaw(r, name) {
  var a = r.a;
  if (!a) return false;
  for (var i = 0; i < a.length; i += 2) {
    if (a[i] === name) {
      var old = a[i + 1];
      a.splice(i, 2);
      if (r.am && r.am[name]) detachAttrRec(r, name, old);
      attrChanged(r, name, old, null);
      return true;
    }
  }
  return false;
}
function removeAttr(r, name) { return removeAttrRaw(r, attrName(r, str(name))); }

// The attribute change steps every set/remove runs.
function attrChanged(r, name, old, value) {
  var d = r.d;
  d.ver++;
  var av = d.av || (d.av = objCreate(null));
  av[name] = (av[name] | 0) + 1;
  if (r.ns === 0) {
    if (r.ln === 'base' && name === 'href') d.bv = (d.bv | 0) + 1;
    else if ((r.ln === 'style' || r.ln === 'link') && (name === 'media' || name === 'rel' || name === 'href' ||
        name === 'disabled' || name === 'type')) cssVersion++;
  }
  if (moActive) moQueueAttribute(r, name, old);
  if (name === 'id' && r.c && !inShadowTree(r)) {
    if (old) idRemove(r.d, old, r);
    if (value) idAdd(r.d, value, r);
  }
  if (name === 'style' && r.sd && !r.sdSelf) r.sd.stale = true;
  if (name === 'open' && r.ln === 'details' && r.ns === 0 && (old === null) !== (value === null)) queueDetailsToggle(r, value !== null);
  if (r.ce === 'custom') ceAttributeChanged(r, name, old, value);
  if (r.d === mainDoc) {
    try { hooks.attrChanged(r, name, old, value); } catch (e) { reportException(e); }
  }
}

// <details> fires a (coalesced) toggle event after its open state changes.
function queueDetailsToggle(r, open) {
  if (r.toggleQueued) {
    r.toggleNew = open;
    return;
  }
  r.toggleQueued = true;
  r.toggleOld = !open;
  r.toggleNew = open;
  hooks.queueTask(function () {
    r.toggleQueued = false;
    if (r.toggleOld === r.toggleNew) return;
    fireEvent(wrap(r), 'toggle', { oldState: r.toggleOld ? 'open' : 'closed', newState: r.toggleNew ? 'open' : 'closed' }, ToggleEvent);
  });
}

// XML Name-ish validation for setAttribute / createElement (browsers throw InvalidCharacterError).
var NAME_START = /^[A-Za-z_:\u00C0-\uFFFF]/;
var NAME_BAD = /[\s\u0000"'>\/=<]/;
function validName(s) { return s.length > 0 && NAME_START.test(s) && !NAME_BAD.test(s); }
function checkName(s, what) {
  if (!validName(s)) throw domErr('InvalidCharacterError', "Failed to execute '" + what + "': '" + s + "' is not a valid name.");
}

// ---------------------------------------------------------------- mutation

function linkBefore(parent, node, ref) {
  node.p = parent;
  if (ref) {
    node.n = ref;
    node.v = ref.v;
    if (ref.v) ref.v.n = node; else parent.f = node;
    ref.v = node;
  } else {
    node.n = null;
    node.v = parent.l;
    if (parent.l) parent.l.n = node; else parent.f = node;
    parent.l = node;
  }
  parent.kv = (parent.kv | 0) + 1;
  parent.cc = (parent.cc | 0) + 1;
  if (node.t === 1) parent.ec = (parent.ec | 0) + 1;
}
function unlink(node) {
  var p = node.p;
  if (!p) return;
  if (node.v) node.v.n = node.n; else p.f = node.n;
  if (node.n) node.n.v = node.v; else p.l = node.v;
  node.p = node.n = node.v = null;
  p.kv = (p.kv | 0) + 1;
  p.cc = (p.cc | 0) - 1;
  if (node.t === 1) p.ec = (p.ec | 0) - 1;
}

function ensurePreInsertValidity(node, parent, child) {
  if (parent.t !== 9 && parent.t !== 11 && parent.t !== 1) {
    throw domErr('HierarchyRequestError', 'This node type does not support this method.');
  }
  if (hostIncludingInclusiveAncestor(node, parent)) {
    throw domErr('HierarchyRequestError', 'The new child element contains the parent.');
  }
  if (child && child.p !== parent) {
    throw domErr('NotFoundError', 'The node before which the new node is to be inserted is not a child of this node.');
  }
  var t = node.t;
  if (t !== 11 && t !== 10 && t !== 1 && t !== 3 && t !== 4 && t !== 7 && t !== 8) {
    throw domErr('HierarchyRequestError', 'Nodes of this type cannot be inserted.');
  }
  if ((t === 3 && parent.t === 9) || (t === 10 && parent.t !== 9)) {
    throw domErr('HierarchyRequestError', 'Nodes of this type cannot be inserted here.');
  }
  if (parent.t === 9) checkDocumentInsert(node, parent, child, false);
}

function docHasElementChild(d, except) {
  for (var k = d.f; k; k = k.n) if (k.t === 1 && k !== except) return true;
  return false;
}
function doctypeFollows(child) {
  for (var k = child; k; k = k.n) if (k.t === 10) return true;
  return false;
}
function checkDocumentInsert(node, d, child, replacing) {
  var bad = false, k;
  if (node.t === 11) {
    var els = 0, text = false;
    for (k = node.f; k; k = k.n) {
      if (k.t === 1) els++;
      else if (k.t === 3) text = true;
    }
    if (els > 1 || text) bad = true;
    else if (els === 1) {
      if (docHasElementChild(d, replacing ? child : null) || (child && child.t === 10) || (child && doctypeFollows(child.n)) ||
          (!replacing && child && doctypeFollows(child))) bad = true;
    }
  } else if (node.t === 1) {
    if (docHasElementChild(d, replacing ? child : null) || (child && !replacing && child.t === 10) ||
        (child && doctypeFollows(replacing ? child.n : child))) bad = true;
  } else if (node.t === 10) {
    for (k = d.f; k; k = k.n) if (k.t === 10 && k !== (replacing ? child : null)) bad = true;
    if (child) {
      for (k = child.v; k; k = k.v) if (k.t === 1) bad = true;
    } else if (docHasElementChild(d, null)) bad = true;
  }
  if (bad) throw domErr('HierarchyRequestError', 'Only one element and one doctype are allowed on a Document.');
}

// insertNode is the DOM "pre-insert": validation, adoption, the insert itself.
function insertNode(parent, node, child) {
  ensurePreInsertValidity(node, parent, child);
  var ref = child === node ? node.n : child;
  adoptRec(node, docOf(parent));
  insertRaw(node, parent, ref || null, false);
  return node;
}

function insertRaw(node, parent, ref, suppress) {
  var nodes, i;
  if (node.t === 11) {
    nodes = childrenArray(node);
    if (!nodes.length) return;
    for (i = 0; i < nodes.length; i++) removeRaw(nodes[i], true);
    if (moActive && !suppress) moQueueChildList(node, null, nodes, null, null);
  } else {
    nodes = [node];
    if (node.p) removeRaw(node, false);
  }
  var prev = ref ? ref.v : parent.l;
  for (i = 0; i < nodes.length; i++) linkBefore(parent, nodes[i], ref);
  var pd = docOf(parent);
  pd.ver++;
  pd.trv = (pd.trv | 0) + 1;
  if (parent.t === 1 && parent.ln === 'style') cssVersion++;
  if (moActive && !suppress) moQueueChildList(parent, nodes, null, prev, ref);
  childrenChanged(parent);
  if (parent.c) {
    var els = [];
    var shadowRoot = parent.shadow || (parent.t !== 9 && inShadowTree(parent));
    for (i = 0; i < nodes.length; i++) markConnected(nodes[i], els, shadowRoot);
    afterConnect(els);
  }
}

function removeNode(node) {
  if (node.p) removeRaw(node, false);
}

function removeRaw(node, suppress) {
  var parent = node.p;
  if (!parent) return;
  var prev = node.v, next = node.n;
  rangesBeforeRemove(node);
  unlink(node);
  var pd = docOf(parent);
  pd.ver++;
  pd.trv = (pd.trv | 0) + 1;
  if (parent.t === 1 && parent.ln === 'style') cssVersion++;
  if (moActive && !suppress) moQueueChildList(parent, null, [node], prev, next);
  if (moActive) moTransient(parent, node);
  childrenChanged(parent);
  if (node.c) {
    var els = [];
    markDisconnected(node, els);
    afterDisconnect(els);
  }
}

function childrenChanged(parent) {
  parent.tv = (parent.tv | 0) + 1;
  if (parent.t === 1 && parent.d === mainDoc && parent.c) {
    try { if (hooks.childrenChanged) hooks.childrenChanged(parent); } catch (e) { reportException(e); }
  }
}

// markConnected flags node's shadow-including subtree as connected, indexes ids (outside shadow trees)
// and collects the elements for the post-connection steps.
function markConnected(node, els, shadow) {
  var stack = [node, shadow];
  while (stack.length) {
    var sh = stack.pop();
    var top = stack.pop();
    for (var r = top; r; r = nextNode(r, top)) {
      r.c = true;
      if (r.t === 1) {
        els.push(r);
        if (r.ns === 0) connectNoteSpecial(r, true);
        if (!sh && r.a) {
          var id = getAttrRaw(r, 'id');
          if (id) idAdd(r.d, id, r);
        }
        if (r.sr) stack.push(r.sr, true);
      }
    }
  }
}
function markDisconnected(node, els) {
  var shadowRoot = inShadowTree(node);
  var stack = [node, shadowRoot];
  while (stack.length) {
    var sh = stack.pop();
    var top = stack.pop();
    for (var r = top; r; r = nextNode(r, top)) {
      r.c = false;
      if (r.t === 1) {
        els.push(r);
        if (r.ns === 0) connectNoteSpecial(r, false);
        if (!sh && r.a) {
          var id = getAttrRaw(r, 'id');
          if (id) idRemove(r.d, id, r);
        }
        if (r.sr) stack.push(r.sr, true);
        if (r.d.active === r) r.d.active = null;
      }
    }
  }
}
// connectNoteSpecial: elements whose arrival or departure moves a document-level version. Connected <style>
// and <link> elements are also listed on the document (sheetEls), so the getComputedStyle cascade finds its
// sheets without walking the tree.
function connectNoteSpecial(r, on) {
  if (r.ln === 'base') {
    r.d.bv = (r.d.bv | 0) + 1;
  } else if (r.ln === 'style' || r.ln === 'link') {
    cssVersion++;
    var list = r.d.sheetEls || (r.d.sheetEls = []);
    var at = list.indexOf(r);
    if (on && at < 0) list.push(r);
    else if (!on && at >= 0) list.splice(at, 1);
  }
}
function afterConnect(els) {
  var main = mainDoc;
  for (var i = 0; i < els.length; i++) {
    var r = els[i];
    // A customized built-in has no `ce` marker until it is upgraded; it is
    // identified by its `is` attribute, so every connected element asks.
    if (r.t === 1) ceConnected(r);
    if (r.d === main && r.c) {
      try { hooks.connected(r); } catch (e) { reportException(e); }
    }
  }
}
function afterDisconnect(els) {
  var main = mainDoc;
  for (var i = 0; i < els.length; i++) {
    var r = els[i];
    if (r.ce === 'custom') ceDisconnected(r);
    if (r.d === main) {
      try { hooks.disconnected(r); } catch (e) { reportException(e); }
    }
  }
}

// adoptRec moves node (and its shadow-including descendants) into document d.
function adoptRec(node, d) {
  if (node.t === 9) throw domErr('NotSupportedError', 'Documents cannot be adopted.');
  if (node.shadow) throw domErr('HierarchyRequestError', 'Shadow roots cannot be adopted.');
  if (node.p && node.d !== d) removeRaw(node, false);
  if (node.d === d) return;
  var old = node.d;
  var stack = [node];
  var adopted = [];
  while (stack.length) {
    var top = stack.pop();
    for (var r = top; r; r = nextNode(r, top)) {
      r.d = d;
      if (r.t === 1) {
        if (r.tc) adoptTemplateContents(r.tc, templateDoc(d));
        if (r.sr) stack.push(r.sr);
        if (r.ce === 'custom') adopted.push(r);
        if (r.am) for (var k in r.am) r.am[k].d = d;
      }
    }
  }
  for (var i = 0; i < adopted.length; i++) ceAdopted(adopted[i], old, d);
}
function adoptTemplateContents(frag, td) {
  if (frag.d === td) return;
  for (var r = frag; r; r = nextNode(r, frag)) {
    r.d = td;
    if (r.t === 1 && r.tc) adoptTemplateContents(r.tc, templateDoc(td));
  }
}

// replaceAllRaw: the DOM "replace all" (innerHTML, textContent, replaceChildren).
function replaceAllRaw(parent, node) {
  var removed = childrenArray(parent);
  var added = node ? (node.t === 11 ? childrenArray(node) : [node]) : [];
  if (node) adoptRec(node, docOf(parent));
  for (var i = 0; i < removed.length; i++) removeRaw(removed[i], true);
  if (node) insertRaw(node, parent, null, true);
  if (moActive && (removed.length || added.length)) moQueueChildList(parent, added, removed, null, null);
}

function replaceChildRaw(parent, node, child) {
  if (parent.t !== 9 && parent.t !== 11 && parent.t !== 1) throw domErr('HierarchyRequestError', 'This node type does not support this method.');
  if (hostIncludingInclusiveAncestor(node, parent)) throw domErr('HierarchyRequestError', 'The new child element contains the parent.');
  if (child.p !== parent) throw domErr('NotFoundError', 'The node to be replaced is not a child of this node.');
  var t = node.t;
  if (t !== 11 && t !== 10 && t !== 1 && t !== 3 && t !== 4 && t !== 7 && t !== 8) throw domErr('HierarchyRequestError', 'Nodes of this type cannot be inserted.');
  if ((t === 3 && parent.t === 9) || (t === 10 && parent.t !== 9)) throw domErr('HierarchyRequestError', 'Nodes of this type cannot be inserted here.');
  if (parent.t === 9) checkDocumentInsert(node, parent, child, true);
  var ref = child.n;
  if (ref === node) ref = node.n;
  var prev = child.v;
  var added = node.t === 11 ? childrenArray(node) : [node];
  adoptRec(node, docOf(parent));
  if (child.p) removeRaw(child, true);
  insertRaw(node, parent, ref, true);
  if (moActive) moQueueChildList(parent, added, [child], prev, ref);
  return child;
}

// ---------------------------------------------------------------- text

function textOf(r) {
  switch (r.t) {
    case 3: case 4: case 7: case 8: return r.x;
    case 1: case 11: break;
    default: return null;
  }
  var k = r.f;
  if (!k) return '';
  if (!k.n && k.t === 3) return k.x;
  var out = '';
  for (var x = k; x; x = nextNode(x, r)) if (x.t === 3 || x.t === 4) out += x.x;
  return out;
}
function childText(r) {
  var out = '';
  for (var k = r.f; k; k = k.n) if (k.t === 3 || k.t === 4) out += k.x;
  return out;
}
function setTextContent(r, value) {
  var s = strOrEmpty(value);
  if (r.t === 1 || r.t === 11) {
    replaceAllRaw(r, s === '' ? null : textRec(r.d, s));
  } else if (r.t === 3 || r.t === 4 || r.t === 7 || r.t === 8) {
    replaceData(r, 0, r.x.length, s);
  }
}

// CharacterData "replace data" with mutation records and live-range-free offsets.
function replaceData(r, offset, count, data) {
  var len = r.x.length;
  if (offset > len) throw domErr('IndexSizeError', 'The offset is larger than the data length.');
  if (offset + count > len) count = len - offset;
  var old = r.x;
  r.x = old.slice(0, offset) + data + old.slice(offset + count);
  r.d.ver++;
  if (r.p && r.p.t === 1 && r.p.ln === 'style') cssVersion++;
  if (moActive) moQueueCharacterData(r, old);
  if (r.p) childrenChanged(r.p);
}

// ---------------------------------------------------------------- URLs

function parseURLParts(input, base) {
  var res;
  try { res = N.urlParse(input, base); } catch (e) { return null; }
  if (!res) return null;
  if (typeof res === 'string') {
    try { res = JSON.parse(res); } catch (e) { return null; }
  }
  return res && res.href ? res : null;
}

// The base URL is cached until a <base> element arrives, leaves or changes its href, or the document moves
// to another URL: without a <base> the lookup would otherwise walk the whole document after any mutation,
// and every href/src getter asks for it.
function documentBaseURL(d) {
  if (d.baseVer === (d.bv | 0) && d.baseFor === d.url) return d.baseURL;
  var base = d.url;
  for (var r = d.f; r; r = nextNode(r, d)) {
    if (r.t === 1 && r.ln === 'base' && r.ns === 0) {
      var href = getAttrRaw(r, 'href');
      if (href !== null) {
        var p = parseURLParts(href, d.url);
        if (p) base = p.href;
        break;
      }
    }
  }
  d.baseVer = d.bv | 0;
  d.baseFor = d.url;
  d.baseURL = base;
  return base;
}

function resolveURL(d, ref) {
  if (ref === null || ref === undefined) return '';
  var p = parseURLParts(str(ref), documentBaseURL(d.t === 9 ? d : d.d));
  return p ? p.href : '';
}

// ---------------------------------------------------------------- live collections

var LIVE = Symbol('jukahub.list');

function listOf(o) {
  var s = o[LIVE];
  if (!s) throw new TypeError('Illegal invocation');
  if (s.arr) return s.arr;
  var root = s.root;
  var ver = s.kind === 0 || s.kind === 1 ? root.kv : s.deps ? depVersion(docOf(root), s.deps) : docOf(root).ver;
  if (s.cache && s.ver === ver) return s.cache;
  var out;
  if (s.kind === 0) out = childrenArray(root);
  else if (s.kind === 1) out = elementChildren(root);
  else if (s.dyn) out = s.dyn(root);
  else {
    out = [];
    var test = s.test;
    for (var r = nextElement(root, root); r; r = nextElement(r, root)) if (test(r, s)) out.push(r);
  }
  s.cache = out;
  s.ver = ver;
  return out;
}

// depVersion: a number that moves whenever the tree, or one of the named attributes, changes anywhere in
// the document (every counter only grows, so the sum does too).
function depVersion(d, deps) {
  var v = d.trv | 0;
  var av = d.av;
  if (av) for (var i = 0; i < deps.length; i++) v += av[deps[i]] | 0;
  return v;
}

// liveCount: length of childNodes (kind 0) / children (kind 1) from the parent's counters, without
// materialising the list; -1 for other kinds.
function liveCount(s) {
  if (s.kind === 0) return s.root.cc | 0;
  if (s.kind === 1) return s.root.ec | 0;
  return -1;
}
// liveEdge: the first or last entry of childNodes/children straight from the links, so the common loops
// `while (el.firstChild …)`-style over el.childNodes[0], and backwards removal over [length - 1], stay
// linear instead of rebuilding the array after every change. undefined means "ask listOf".
function liveEdge(s, i) {
  var root = s.root, k;
  if (s.kind === 0) {
    if (i === 0) return root.f;
    if (i === (root.cc | 0) - 1) return root.l;
    return undefined;
  }
  if (s.kind === 1) {
    var n = root.ec | 0;
    if (i === 0 && n > 0) {
      for (k = root.f; k; k = k.n) if (k.t === 1) return k;
    } else if (i === n - 1 && n > 0) {
      for (k = root.l; k; k = k.v) if (k.t === 1) return k;
    }
    return undefined;
  }
  return undefined;
}

function listIndex(k) {
  if (typeof k !== 'string') return -1;
  var c = k.charCodeAt(0);
  if (c < 48 || c > 57) return -1;
  var n = +k;
  return (n >>> 0) === n && String(n) === k ? n : -1;
}

var liveHandler = {
  get: function (t, k, recv) {
    var i = listIndex(k);
    if (i >= 0) {
      var st = t[LIVE];
      if (st && !st.arr) {
        var c = liveCount(st);
        if (c >= 0) {
          if (i >= c) return undefined;
          var edge = liveEdge(st, i);
          if (edge !== undefined) return wrap(edge);
        }
      }
      var l = listOf(t);
      return i < l.length ? wrap(l[i]) : undefined;
    }
    if (k === 'length') {
      var ls = t[LIVE];
      var cnt = ls && !ls.arr ? liveCount(ls) : -1;
      return cnt >= 0 ? cnt : listOf(t).length;
    }
    if (typeof k === 'string' && t[LIVE].named && !(k in t)) {
      var n = liveNamed(t, recv, k);
      return n === null ? undefined : n;
    }
    return Reflect.get(t, k, recv);
  },
  has: function (t, k) {
    var i = listIndex(k);
    if (i >= 0) {
      var hs = t[LIVE];
      var hc = hs && !hs.arr ? liveCount(hs) : -1;
      return i < (hc >= 0 ? hc : listOf(t).length);
    }
    if (typeof k === 'string' && t[LIVE].named && !(k in t)) return liveNamed(t, t, k) !== null;
    return k in t;
  },
  set: function (t, k, v, recv) {
    if (listIndex(k) >= 0 || k === 'length') return true;
    return Reflect.set(t, k, v, recv);
  },
  defineProperty: function (t, k, d) {
    if (listIndex(k) >= 0) return false;
    return Reflect.defineProperty(t, k, d);
  },
  deleteProperty: function (t, k) {
    if (listIndex(k) >= 0) return false;
    return Reflect.deleteProperty(t, k);
  },
  ownKeys: function (t) {
    var keys = [];
    var n = listOf(t).length;
    for (var i = 0; i < n; i++) keys.push(String(i));
    var own = Reflect.ownKeys(t);
    for (var j = 0; j < own.length; j++) if (own[j] !== LIVE) keys.push(own[j]);
    return keys;
  },
  getOwnPropertyDescriptor: function (t, k) {
    var i = listIndex(k);
    if (i >= 0) {
      var l = listOf(t);
      if (i < l.length) return { value: wrap(l[i]), writable: false, enumerable: true, configurable: true };
      return undefined;
    }
    if (k === LIVE) return undefined;
    return Reflect.getOwnPropertyDescriptor(t, k);
  }
};

// Named access goes through the collection's own namedItem (HTMLFormControlsCollection returns a
// RadioNodeList for a radio group).
function liveNamed(t, recv, k) {
  var ni = t.namedItem;
  return typeof ni === 'function' ? ni.call(recv, k) : namedItemOf(t, k);
}

function makeLive(proto, state) {
  var t = objCreate(proto);
  defineProperty(t, LIVE, { value: state, configurable: true });
  return new Proxy(t, liveHandler);
}
function makeStaticList(recs) {
  var o = objCreate(NodeList.prototype);
  defineProperty(o, LIVE, { value: { arr: recs }, configurable: true });
  for (var i = 0; i < recs.length; i++) defineProperty(o, i, { value: wrap(recs[i]), enumerable: true, configurable: true });
  return o;
}
function cachedLive(r, key, proto, state) {
  var cache = r.lc || (r.lc = objCreate(null));
  return cache[key] || (cache[key] = makeLive(proto, state));
}
function childNodesOf(r) { return cachedLive(r, 'cn', NodeList.prototype, { kind: 0, root: r }); }
function childrenOf(r) { return cachedLive(r, 'ch', HTMLCollection.prototype, { kind: 1, root: r, named: true }); }
function filteredCollection(r, key, test, extra) {
  var state = { kind: 2, root: r, test: test, named: true };
  if (extra) for (var k in extra) state[k] = extra[k];
  return cachedLive(r, key, HTMLCollection.prototype, state);
}

function namedItemOf(o, name) {
  if (name === '') return null;
  var l = listOf(o);
  var i;
  for (i = 0; i < l.length; i++) if (getAttrRaw(l[i], 'id') === name) return wrap(l[i]);
  for (i = 0; i < l.length; i++) {
    var r = l[i];
    if (r.ns === 0 && getAttrRaw(r, 'name') === name && NAMED_ACCESS[r.ln]) return wrap(r);
  }
  return null;
}
var NAMED_ACCESS = { a: 1, applet: 1, area: 1, embed: 1, form: 1, frame: 1, frameset: 1, iframe: 1, img: 1, object: 1,
  input: 1, select: 1, textarea: 1, button: 1, fieldset: 1, output: 1, meta: 1, map: 1 };

function tagNameTest(r, s) {
  if (s.name === '*') return true;
  return r.ns === 0 && r.d.html ? r.ln === s.lower : qualifiedName(r) === s.name;
}
function classTest(r, s) {
  if (!s.classes.length || !r.a) return false;
  for (var i = 0; i < s.classes.length; i++) if (!hasClass(r, s.classes[i])) return false;
  return true;
}
function getElementsByTagNameOf(r, name) {
  name = str(name);
  return filteredCollection(r, 'tn:' + name, tagNameTest, { name: name, lower: asciiLower(name), deps: [] });
}
function getElementsByTagNameNSOf(r, ns, local) {
  ns = ns === null || ns === undefined || ns === '' ? null : str(ns);
  local = str(local);
  return filteredCollection(r, 'tns:' + ns + ':' + local, function (e, s) {
    return (s.ns === '*' || nsURI(e) === s.ns) && (s.local === '*' || e.ln === s.local);
  }, { ns: ns, local: local, deps: [] });
}
function getElementsByClassNameOf(r, names) {
  names = str(names);
  return filteredCollection(r, 'cn:' + names, classTest, { classes: splitTokens(names), deps: ['class'] });
}

// ---------------------------------------------------------------- interfaces: EventTarget → Node

// EventTarget is defined in 40_events.js; Node needs it as its parent, so the interface object is created
// here and completed there.
var EventTarget = iface('EventTarget', null, function EventTarget() {
  if (!(this instanceof EventTarget)) throw new TypeError("Failed to construct 'EventTarget': Please use the 'new' operator.");
});

var Node = iface('Node', EventTarget);
constants(Node, {
  ELEMENT_NODE: 1, ATTRIBUTE_NODE: 2, TEXT_NODE: 3, CDATA_SECTION_NODE: 4, ENTITY_REFERENCE_NODE: 5,
  ENTITY_NODE: 6, PROCESSING_INSTRUCTION_NODE: 7, COMMENT_NODE: 8, DOCUMENT_NODE: 9, DOCUMENT_TYPE_NODE: 10,
  DOCUMENT_FRAGMENT_NODE: 11, NOTATION_NODE: 12, DOCUMENT_POSITION_DISCONNECTED: 1,
  DOCUMENT_POSITION_PRECEDING: 2, DOCUMENT_POSITION_FOLLOWING: 4, DOCUMENT_POSITION_CONTAINS: 8,
  DOCUMENT_POSITION_CONTAINED_BY: 16, DOCUMENT_POSITION_IMPLEMENTATION_SPECIFIC: 32
});
constants(Node.prototype, {
  ELEMENT_NODE: 1, ATTRIBUTE_NODE: 2, TEXT_NODE: 3, CDATA_SECTION_NODE: 4, ENTITY_REFERENCE_NODE: 5,
  ENTITY_NODE: 6, PROCESSING_INSTRUCTION_NODE: 7, COMMENT_NODE: 8, DOCUMENT_NODE: 9, DOCUMENT_TYPE_NODE: 10,
  DOCUMENT_FRAGMENT_NODE: 11, NOTATION_NODE: 12, DOCUMENT_POSITION_DISCONNECTED: 1,
  DOCUMENT_POSITION_PRECEDING: 2, DOCUMENT_POSITION_FOLLOWING: 4, DOCUMENT_POSITION_CONTAINS: 8,
  DOCUMENT_POSITION_CONTAINED_BY: 16, DOCUMENT_POSITION_IMPLEMENTATION_SPECIFIC: 32
});

function qualifiedName(r) { return r.px ? r.px + ':' + r.ln : r.ln; }
function htmlUpperName(r) {
  var q = qualifiedName(r);
  return r.ns === 0 && r.d.html ? asciiUpper(q) : q;
}

function nodeName(r) {
  switch (r.t) {
    case 1: return htmlUpperName(r);
    case 2: return r.name;
    case 3: return '#text';
    case 4: return '#cdata-section';
    case 7: return r.ln;
    case 8: return '#comment';
    case 9: return '#document';
    case 10: return r.ln;
    case 11: return '#document-fragment';
  }
  return '';
}

function anyArgNode(v, d) {
  // ChildNode/ParentNode methods accept nodes and strings (strings become Text nodes).
  var r = unwrap(v);
  if (r) return r;
  return textRec(d, str(v));
}
// convertNodesIntoNode: the ParentNode/ChildNode helper.
function nodesToNode(args, d) {
  if (args.length === 1) return anyArgNode(args[0], d);
  var frag = newFragmentRec(d);
  for (var i = 0; i < args.length; i++) {
    var r = anyArgNode(args[i], d);
    insertNode(frag, r, null);
  }
  return frag;
}

mix(Node.prototype, {
  get nodeType() { return R(this).t; },
  get nodeName() { return nodeName(R(this)); },
  get baseURI() { var r = R(this); return documentBaseURL(docOf(r)); },
  get isConnected() { return R(this).c; },
  get ownerDocument() { var r = R(this); return r.t === 9 ? null : wrap(r.d); },
  getRootNode: function (opts) {
    var r = treeRoot(R(this));
    if (opts && opts.composed) {
      while (r.shadow && r.host) r = treeRoot(r.host);
    }
    return wrap(r);
  },
  get parentNode() { var r = R(this); return r.p ? wrap(r.p) : null; },
  get parentElement() { var r = R(this); return r.p && r.p.t === 1 ? wrap(r.p) : null; },
  hasChildNodes: function () { return !!R(this).f; },
  get childNodes() { return childNodesOf(R(this)); },
  get firstChild() { var r = R(this); return r.f ? wrap(r.f) : null; },
  get lastChild() { var r = R(this); return r.l ? wrap(r.l) : null; },
  get previousSibling() { var r = R(this); return r.v ? wrap(r.v) : null; },
  get nextSibling() { var r = R(this); return r.n ? wrap(r.n) : null; },
  get nodeValue() {
    var r = R(this);
    if (r.t === 3 || r.t === 4 || r.t === 7 || r.t === 8) return r.x;
    if (r.t === 2) return attrValue(r);
    return null;
  },
  set nodeValue(v) {
    var r = R(this);
    if (r.t === 3 || r.t === 4 || r.t === 7 || r.t === 8) replaceData(r, 0, r.x.length, strOrEmpty(v));
    else if (r.t === 2) setAttrValue(r, strOrEmpty(v));
  },
  get textContent() {
    var r = R(this);
    if (r.t === 2) return attrValue(r);
    return textOf(r);
  },
  set textContent(v) {
    var r = R(this);
    if (r.t === 2) setAttrValue(r, strOrEmpty(v));
    else setTextContent(r, v);
  },
  normalize: function () {
    var root = R(this);
    var r = root.f ? root.f : null;
    while (r) {
      var next = nextNode(r, root);
      if (r.t === 3) {
        if (r.x === '') {
          next = nextSkipping(r, root);
          removeRaw(r, false);
        } else {
          var data = '';
          var s = r.n;
          while (s && s.t === 3) {
            data += s.x;
            var after = s.n;
            removeRaw(s, false);
            s = after;
          }
          if (data) replaceData(r, r.x.length, 0, data);
          next = nextNode(r, root);
        }
      }
      r = next;
    }
  },
  cloneNode: function (deep) {
    var r = R(this);
    if (r.shadow) throw domErr('NotSupportedError', 'ShadowRoot nodes are not clonable.');
    return wrap(cloneRec(r, r.t === 9 ? null : r.d, !!deep));
  },
  isEqualNode: function (other) {
    var o = unwrap(other);
    return !!o && nodesEqual(R(this), o);
  },
  isSameNode: function (other) { return unwrap(other) === R(this); },
  compareDocumentPosition: function (other) {
    requiredArgs(arguments, 1, 'compareDocumentPosition');
    return comparePosition(R(this), nodeArg(other, 'compareDocumentPosition'));
  },
  contains: function (other) {
    var o = unwrap(other);
    return !!o && isInclusiveAncestor(R(this), o);
  },
  lookupPrefix: function (ns) { return null; },
  lookupNamespaceURI: function (prefix) {
    var r = R(this);
    var e = r.t === 1 ? r : (r.t === 9 ? firstElementChild(r) : r.p);
    if ((prefix === null || prefix === undefined || prefix === '') && e && e.t === 1) return nsURI(e);
    if (prefix === 'xml') return NS_XML;
    if (prefix === 'xmlns') return NS_XMLNS;
    return null;
  },
  isDefaultNamespace: function (ns) {
    var r = R(this);
    var e = r.t === 1 ? r : (r.t === 9 ? firstElementChild(r) : r.p);
    return !!e && e.t === 1 && nsURI(e) === (ns === '' ? null : ns);
  },
  insertBefore: function (node, child) {
    var r = R(this);
    var n = nodeArg(node, 'insertBefore');
    var c = child === null || child === undefined ? null : nodeArg(child, 'insertBefore');
    insertNode(r, n, c);
    return node;
  },
  appendChild: function (node) {
    insertNode(R(this), nodeArg(node, 'appendChild'), null);
    return node;
  },
  replaceChild: function (node, child) {
    var r = R(this);
    replaceChildRaw(r, nodeArg(node, 'replaceChild'), nodeArg(child, 'replaceChild'));
    return child;
  },
  removeChild: function (child) {
    var r = R(this);
    var c = nodeArg(child, 'removeChild');
    if (c.p !== r) throw domErr('NotFoundError', "Failed to execute 'removeChild' on 'Node': The node to be removed is not a child of this node.");
    removeRaw(c, false);
    return child;
  }
});

function nodesEqual(a, b) {
  var sa = [a], sb = [b];
  while (sa.length) {
    var x = sa.pop(), y = sb.pop();
    if (x.t !== y.t) return false;
    switch (x.t) {
      case 1:
        if (x.ln !== y.ln || x.ns !== y.ns || (x.px || '') !== (y.px || '')) return false;
        var ax = x.a || [], ay = y.a || [];
        if (ax.length !== ay.length) return false;
        for (var i = 0; i < ax.length; i += 2) if (getAttrRaw(y, ax[i]) !== ax[i + 1]) return false;
        break;
      case 3: case 4: case 8:
        if (x.x !== y.x) return false;
        break;
      case 7:
        if (x.ln !== y.ln || x.x !== y.x) return false;
        break;
      case 10:
        if (x.ln !== y.ln || x.pub !== y.pub || x.sys !== y.sys) return false;
        break;
      case 2:
        if (x.name !== y.name || attrValue(x) !== attrValue(y)) return false;
        break;
    }
    var nx = 0, ny = 0, k, m;
    for (k = x.f; k; k = k.n) nx++;
    for (k = y.f; k; k = k.n) ny++;
    if (nx !== ny) return false;
    for (k = x.f, m = y.f; k; k = k.n, m = m.n) {
      sa.push(k);
      sb.push(m);
    }
  }
  return true;
}

function comparePosition(a, b) {
  if (a === b) return 0;
  var a1 = a, b1 = b, attrA = null, attrB = null;
  if (a1.t === 2) { attrA = a1; a1 = a1.el || a1; }
  if (b1.t === 2) {
    attrB = b1;
    b1 = b1.el || b1;
    if (attrA && a1 === b1 && a1.t === 1) {
      return 34 | (attrNamesOrder(a1, attrA.name, attrB.name) ? 4 : 2);
    }
  }
  var ra = treeRoot(a1), rb = treeRoot(b1);
  if (ra !== rb || (a1.t === 2 && !attrA) || (b1.t === 2 && !attrB)) return 1 | 32 | (precedesArbitrary(ra, rb) ? 4 : 2);
  if (!attrA && isInclusiveAncestor(b1, a1) && a1 !== b1) return 8 | 2;
  if (!attrB && isInclusiveAncestor(a1, b1) && a1 !== b1) return 16 | 4;
  if (attrA && a1 === b1) return 4;
  if (attrB && a1 === b1) return 2;
  return precedes(b1, a1) ? 2 : 4;
}
function attrNamesOrder(el, n1, n2) {
  var a = el.a || [];
  var i1 = -1, i2 = -1;
  for (var i = 0; i < a.length; i += 2) {
    if (a[i] === n1) i1 = i;
    if (a[i] === n2) i2 = i;
  }
  return i1 < i2;
}
var arbitraryOrder = 0;
function precedesArbitrary(a, b) {
  if (a.ord === undefined) a.ord = ++arbitraryOrder;
  if (b.ord === undefined) b.ord = ++arbitraryOrder;
  return a.ord < b.ord;
}

// ---------------------------------------------------------------- cloning

function cloneOne(r, d) {
  var c;
  switch (r.t) {
    case 1:
      c = createElementRec(d, r.ln, r.ns);
      if (r.nsu !== undefined) c.nsu = r.nsu;
      if (r.px) c.px = r.px;
      if (r.a) c.a = r.a.slice();
      if (r.ce === 'custom' || r.ce === 'failed') c.ce = 'undefined';
      cloningSteps(r, c);
      break;
    case 3: c = textRec(d, r.x); break;
    case 4: c = cdataRec(d, r.x); break;
    case 7: c = piRec(d, r.ln, r.x); break;
    case 8: c = commentRec(d, r.x); break;
    case 10: c = doctypeRec(d, r.ln, r.pub, r.sys); break;
    case 11: c = newFragmentRec(d); break;
    case 9:
      c = newDocRec(r.url, !r.html);
      c.mode = r.mode;
      break;
    case 2:
      c = { t: 2, d: d, p: null, n: null, v: null, c: false, o: null, name: r.name, el: null, val: attrValue(r), nsu: r.nsu, px: r.px, lname: r.lname };
      break;
  }
  return c;
}
// Element-specific cloning steps (form state, script "already started", template contents).
function cloningSteps(src, c) {
  if (src.fv !== undefined) c.fv = src.fv;
  if (src.fc !== undefined) c.fc = src.fc;
  if (src.sel !== undefined) c.sel = src.sel;
  // A clone of a script that already started never runs again (spec "already started" flag).
  if (src.st0) c.st0 = true;
}

function cloneRec(r, d, deep) {
  var target = d || null;
  var root = cloneOne(r, target || r.d);
  if (r.t === 9) {
    target = root;
  }
  if (!target) target = root.d;
  if (deep) cloneChildrenInto(r, root, r.t === 9 ? root : target);
  if (r.t === 1 && r.tc && deep) cloneChildrenInto(r.tc, root.tc, root.tc.d);
  return root;
}
function cloneChildrenInto(src, dst, d) {
  // Iterative deep copy: stack of [source parent, clone parent].
  var stack = [src, dst];
  while (stack.length) {
    var cp = stack.pop();
    var sp = stack.pop();
    for (var k = sp.f; k; k = k.n) {
      var kd = cp.t === 9 ? cp : cp.d;
      var c = cloneOne(k, kd);
      linkBefore(cp, c, null);
      if (k.f) stack.push(k, c);
      if (k.t === 1 && k.tc && k.tc.f) stack.push(k.tc, c.tc);
    }
  }
}

// ---------------------------------------------------------------- building from the parser's JSON tree

// buildChildren appends nodes of the Go parser's JSON tree (already JSON.parse'd) under parent.
// mode 'parser' or 'write' marks scripts parser-inserted (they may run); 'contextual'
// (Range.createContextualFragment) leaves them as ordinary scripts that run once connected; anything else
// marks them already started (innerHTML scripts never run).
function buildChildren(parent, nodes, mode) {
  if (!nodes || !nodes.length) return;
  var runnable = mode === 'parser' || mode === 'write';
  var contextual = mode === 'contextual';
  var stack = [parent, nodes, 0];
  while (stack.length) {
    var top = stack.length;
    var p = stack[top - 3], arr = stack[top - 2], i = stack[top - 1];
    if (i >= arr.length) {
      stack.length = top - 3;
      continue;
    }
    stack[top - 1] = i + 1;
    var item = arr[i];
    var d = p.t === 9 ? p : p.d;
    var r;
    if (typeof item === 'string') {
      if (p.l && p.l.t === 3) {
        p.l.x += item;
        continue;
      }
      r = textRec(d, item);
    } else if (!item || !item.length) {
      continue;
    } else {
      var kind = item[0];
      if (typeof kind === 'string') {
        var ns = item.length > 3 ? (item[3] | 0) : 0;
        r = createElementRec(d, kind, ns);
        var attrs = item[1];
        if (attrs && attrs.length) r.a = attrs;
        if (ns === 0 && kind === 'script') {
          if (runnable) r.pi = true;
          else if (!contextual) r.st0 = true;
        }
        var kids = item[2];
        attachBuilt(p, r);
        if (kids && kids.length) stack.push(r.tc ? r.tc : r, kids, 0);
        continue;
      } else if (kind === 8) {
        r = commentRec(d, str(item[1] === undefined ? '' : item[1]));
      } else if (kind === 10) {
        r = doctypeRec(d, item[1], item[2], item[3]);
      } else if (kind === 7) {
        r = piRec(d, str(item[1]), str(item[2] === undefined ? '' : item[2]));
      } else {
        continue;
      }
    }
    attachBuilt(p, r);
  }
}
function attachBuilt(p, r) {
  linkBefore(p, r, null);
  if (p.c) {
    r.c = true;
    if (r.t === 1) {
      if (r.ns === 0 && (r.ln === 'style' || r.ln === 'link' || r.ln === 'base')) connectNoteSpecial(r, true);
      if (r.a && !p.shadow) {
        var id = getAttrRaw(r, 'id');
        if (id) idAdd(r.d, id, r);
      }
    }
  }
}

function parseFragmentInto(ctx, html, mode) {
  var context = 'body', ns = 0;
  if (ctx && ctx.t === 1) {
    if (ctx.ns <= 2) {
      context = ctx.ln;
      ns = ctx.ns;
    }
  }
  var d = ctx ? docOf(ctx) : mainDoc;
  if (ctx && ctx.t === 1 && ctx.ln === 'template' && ctx.tc) d = ctx.tc.d;
  var frag = newFragmentRec(d);
  var json = N.parseFragment(str(html), context, ns);
  var nodes = typeof json === 'string' ? JSON.parse(json) : json;
  buildChildren(frag, nodes, mode || 'fragment');
  return frag;
}

// parseFragmentRecs: the contract version — detached records, in order.
function parseFragmentRecs(ctx, html, mode) {
  var frag = parseFragmentInto(ctx, html, mode);
  var out = childrenArray(frag);
  for (var i = 0; i < out.length; i++) unlink(out[i]);
  return out;
}

// bootDocument builds the page's document from the Go parser's tree.
function bootDocument(treeJSON, url) {
  var d = newDocRec(url);
  d.main = true;
  d.ready = 'loading';
  mainDoc = d;
  setupWindowNamed();
  var nodes = typeof treeJSON === 'string' ? JSON.parse(treeJSON) : treeJSON;
  buildChildren(d, nodes || [], 'parser');
  ensureSkeleton(d);
  d.mode = quirksModeFor(d);
  attachDeclarativeShadowRoots(d);
  return d;
}

function ensureSkeleton(d) {
  var html = firstElementChild(d);
  if (!html) {
    html = createElementRec(d, 'html', 0);
    attachBuilt(d, html);
  }
  if (html.ln !== 'html' || html.ns !== 0) return;
  var head = null, body = null;
  for (var k = html.f; k; k = k.n) {
    if (k.t !== 1) continue;
    if (k.ln === 'head' && !head) head = k;
    else if ((k.ln === 'body' || k.ln === 'frameset') && !body) body = k;
  }
  if (!head) {
    head = createElementRec(d, 'head', 0);
    linkBefore(html, head, html.f);
    head.c = html.c;
  }
  if (!body) {
    body = createElementRec(d, 'body', 0);
    linkBefore(html, body, null);
    body.c = html.c;
  }
}

function quirksModeFor(d) {
  for (var k = d.f; k; k = k.n) {
    if (k.t === 10) return asciiLower(k.ln) === 'html' ? 'no-quirks' : 'quirks';
    if (k.t === 1) break;
  }
  return 'quirks';
}

// ---------------------------------------------------------------- NodeList, HTMLCollection

var NodeList = iface('NodeList', null);
mix(NodeList.prototype, {
  get length() { return listOf(this).length; },
  item: function (i) { return liveItem(this, i >>> 0); },
  forEach: function (cb, thisArg) {
    var l = listOf(this).slice();
    for (var i = 0; i < l.length; i++) cb.call(thisArg, wrap(l[i]), i, this);
  },
  entries: function () { return listOf(this).map(function (r, i) { return [i, wrap(r)]; })[Symbol.iterator](); },
  keys: function () { return listOf(this).map(function (r, i) { return i; })[Symbol.iterator](); },
  values: function () { return listOf(this).map(wrap)[Symbol.iterator](); }
});
defineProperty(NodeList.prototype, Symbol.iterator, { value: NodeList.prototype.values, writable: true, configurable: true });

// liveItem: item(i) with the childNodes/children fast paths of the proxy's index access.
function liveItem(o, i) {
  var st = o[LIVE];
  if (st && !st.arr) {
    var c = liveCount(st);
    if (c >= 0) {
      if (i >= c) return null;
      var edge = liveEdge(st, i);
      if (edge !== undefined) return wrap(edge);
    }
  }
  var l = listOf(o);
  return i < l.length ? wrap(l[i]) : null;
}

var HTMLCollection = iface('HTMLCollection', null);
mix(HTMLCollection.prototype, {
  get length() { return listOf(this).length; },
  item: function (i) { return liveItem(this, i >>> 0); },
  namedItem: function (name) { return namedItemOf(this, str(name)); }
});
defineProperty(HTMLCollection.prototype, Symbol.iterator, {
  value: function () { return listOf(this).map(wrap)[Symbol.iterator](); }, writable: true, configurable: true
});

// ---------------------------------------------------------------- ParentNode / ChildNode mixins

var parentNodeMixin = {
  get children() { return childrenOf(R(this)); },
  get firstElementChild() { return wrap(firstElementChild(R(this))); },
  get lastElementChild() {
    for (var k = R(this).l; k; k = k.v) if (k.t === 1) return wrap(k);
    return null;
  },
  get childElementCount() {
    var n = 0;
    for (var k = R(this).f; k; k = k.n) if (k.t === 1) n++;
    return n;
  },
  prepend: function () {
    var r = R(this);
    insertNode(r, nodesToNode(arguments, docOf(r)), r.f);
  },
  append: function () {
    var r = R(this);
    insertNode(r, nodesToNode(arguments, docOf(r)), null);
  },
  replaceChildren: function () {
    var r = R(this);
    var node = nodesToNode(arguments, docOf(r));
    ensurePreInsertValidity(node, r, null);
    replaceAllRaw(r, node);
  },
  querySelector: function (sel) {
    return wrap(querySelectorRec(R(this), str(sel)));
  },
  querySelectorAll: function (sel) {
    return makeStaticList(querySelectorAllRecs(R(this), str(sel)));
  },
  getElementsByTagName: function (name) { return getElementsByTagNameOf(R(this), name); },
  getElementsByTagNameNS: function (ns, local) { return getElementsByTagNameNSOf(R(this), ns, local); },
  getElementsByClassName: function (names) { return getElementsByClassNameOf(R(this), names); }
};

function viableSibling(r, set, dir) {
  for (var k = dir ? r.n : r.v; k; k = dir ? k.n : k.v) if (set.indexOf(k) < 0) return k;
  return null;
}
function argRecs(args) {
  var out = [];
  for (var i = 0; i < args.length; i++) {
    var r = unwrap(args[i]);
    if (r) out.push(r);
  }
  return out;
}

var childNodeMixin = {
  before: function () {
    var r = R(this);
    var parent = r.p;
    if (!parent) return;
    var prev = viableSibling(r, argRecs(arguments), false);
    var node = nodesToNode(arguments, docOf(r));
    insertNode(parent, node, prev ? prev.n : parent.f);
  },
  after: function () {
    var r = R(this);
    var parent = r.p;
    if (!parent) return;
    var next = viableSibling(r, argRecs(arguments), true);
    insertNode(parent, nodesToNode(arguments, docOf(r)), next);
  },
  replaceWith: function () {
    var r = R(this);
    var parent = r.p;
    if (!parent) return;
    var next = viableSibling(r, argRecs(arguments), true);
    var node = nodesToNode(arguments, docOf(r));
    if (r.p === parent) replaceChildRaw(parent, node, r);
    else insertNode(parent, node, next);
  },
  remove: function () {
    var r = R(this);
    if (r.p) removeRaw(r, false);
  }
};

var nonDocumentTypeChildNodeMixin = {
  get previousElementSibling() {
    for (var k = R(this).v; k; k = k.v) if (k.t === 1) return wrap(k);
    return null;
  },
  get nextElementSibling() {
    for (var k = R(this).n; k; k = k.n) if (k.t === 1) return wrap(k);
    return null;
  }
};

// ---------------------------------------------------------------- CharacterData, Text, Comment, …

var CharacterData = iface('CharacterData', Node);
mix(CharacterData.prototype, {
  get data() { return R(this).x; },
  set data(v) { var r = R(this); replaceData(r, 0, r.x.length, strOrEmpty(v)); },
  get length() { return R(this).x.length; },
  substringData: function (offset, count) {
    var r = R(this);
    offset = offset >>> 0;
    count = count >>> 0;
    if (offset > r.x.length) throw domErr('IndexSizeError', 'The offset is larger than the data length.');
    return r.x.substr(offset, count);
  },
  appendData: function (data) { var r = R(this); replaceData(r, r.x.length, 0, str(data)); },
  insertData: function (offset, data) { replaceData(R(this), offset >>> 0, 0, str(data)); },
  deleteData: function (offset, count) { replaceData(R(this), offset >>> 0, count >>> 0, ''); },
  replaceData: function (offset, count, data) { replaceData(R(this), offset >>> 0, count >>> 0, str(data)); }
});
mix(CharacterData.prototype, childNodeMixin);
mix(CharacterData.prototype, nonDocumentTypeChildNodeMixin);

var Text = iface('Text', CharacterData, function Text(data) {
  if (!(this instanceof Text)) throw new TypeError("Failed to construct 'Text': Please use the 'new' operator.");
  return wrap(textRec(mainDoc || standaloneDoc(), data === undefined ? '' : str(data)));
});
mix(Text.prototype, {
  splitText: function (offset) {
    var r = R(this);
    offset = offset >>> 0;
    if (offset > r.x.length) throw domErr('IndexSizeError', 'The offset is larger than the data length.');
    var tail = textRec(r.d, r.x.slice(offset));
    if (r.p) insertRaw(tail, r.p, r.n, false);
    replaceData(r, offset, r.x.length - offset, '');
    return wrap(tail);
  },
  get wholeText() {
    var r = R(this);
    var s = r;
    while (s.v && (s.v.t === 3 || s.v.t === 4)) s = s.v;
    var out = '';
    for (; s && (s.t === 3 || s.t === 4); s = s.n) out += s.x;
    return out;
  },
  get assignedSlot() { return wrap(assignedSlotOf(R(this))); }
});

var CDATASection = iface('CDATASection', Text);
var Comment = iface('Comment', CharacterData, function Comment(data) {
  if (!(this instanceof Comment)) throw new TypeError("Failed to construct 'Comment': Please use the 'new' operator.");
  return wrap(commentRec(mainDoc || standaloneDoc(), data === undefined ? '' : str(data)));
});
var ProcessingInstruction = iface('ProcessingInstruction', CharacterData);
mix(ProcessingInstruction.prototype, {
  get target() { return R(this).ln; }
});

var DocumentType = iface('DocumentType', Node);
mix(DocumentType.prototype, {
  get name() { return R(this).ln; },
  get publicId() { return R(this).pub; },
  get systemId() { return R(this).sys; }
});
mix(DocumentType.prototype, childNodeMixin);

var standaloneDocRec = null;
function standaloneDoc() {
  if (!standaloneDocRec) standaloneDocRec = newDocRec('about:blank');
  return standaloneDocRec;
}

// ---------------------------------------------------------------- DocumentFragment

var DocumentFragment = iface('DocumentFragment', Node, function DocumentFragment() {
  if (!(this instanceof DocumentFragment)) throw new TypeError("Failed to construct 'DocumentFragment': Please use the 'new' operator.");
  return wrap(newFragmentRec(mainDoc || standaloneDoc()));
});
mix(DocumentFragment.prototype, parentNodeMixin);
mix(DocumentFragment.prototype, {
  getElementById: function (id) {
    var r = R(this);
    id = str(id);
    if (id === '') return null;
    for (var e = nextElement(r, r); e; e = nextElement(e, r)) if (getAttrRaw(e, 'id') === id) return wrap(e);
    return null;
  }
});

// ---------------------------------------------------------------- Attr, NamedNodeMap

// Attr records are created on demand and cached per element (r.am[name]) so identity holds.
function attrRecFor(el, name) {
  var am = el.am || (el.am = objCreate(null));
  var ar = am[name];
  if (!ar) {
    var colon = name.indexOf(':');
    ar = am[name] = {
      t: 2, d: el.d, p: null, n: null, v: null, c: false, o: null, name: name, el: el, val: null,
      px: colon > 0 ? name.slice(0, colon) : null, lname: colon > 0 ? name.slice(colon + 1) : name
    };
  }
  return ar;
}
function detachAttrRec(el, name, value) {
  var ar = el.am && el.am[name];
  if (ar) {
    ar.val = value;
    ar.el = null;
    delete el.am[name];
  }
}
function attrValue(ar) {
  if (ar.el) {
    var v = getAttrRaw(ar.el, ar.name);
    if (v !== null) return v;
  }
  return ar.val === null ? '' : ar.val;
}
function setAttrValue(ar, v) {
  if (ar.el) setAttrRaw(ar.el, ar.name, v);
  else ar.val = v;
}

var Attr = iface('Attr', Node);
mix(Attr.prototype, {
  get name() { return R(this).name; },
  get localName() { return R(this).lname; },
  get prefix() { return R(this).px; },
  get namespaceURI() {
    var r = R(this);
    if (r.nsu !== undefined) return r.nsu;
    if (r.px === 'xlink') return NS_XLINK;
    if (r.px === 'xml') return NS_XML;
    if (r.px === 'xmlns' || r.name === 'xmlns') return NS_XMLNS;
    return null;
  },
  get value() { return attrValue(R(this)); },
  set value(v) { setAttrValue(R(this), str(v)); },
  get ownerElement() { var r = R(this); return r.el ? wrap(r.el) : null; },
  get specified() { return true; }
});

var NamedNodeMap = iface('NamedNodeMap', null);
function attrsListOf(o) {
  var s = o[LIVE];
  if (!s) throw new TypeError('Illegal invocation');
  return s.root;
}
var attrMapHandler = {
  get: function (t, k, recv) {
    var el = t[LIVE].root;
    var i = listIndex(k);
    if (i >= 0) {
      var a = el.a;
      return a && i * 2 < a.length ? wrap(attrRecFor(el, a[i * 2])) : undefined;
    }
    if (k === 'length') return el.a ? el.a.length / 2 : 0;
    if (typeof k === 'string' && !(k in t)) {
      var name = attrName(el, k);
      if (getAttrRaw(el, name) !== null) return wrap(attrRecFor(el, name));
      return undefined;
    }
    return Reflect.get(t, k, recv);
  },
  has: function (t, k) {
    var el = t[LIVE].root;
    var i = listIndex(k);
    if (i >= 0) return !!el.a && i * 2 < el.a.length;
    if (typeof k === 'string' && !(k in t)) return getAttrRaw(el, attrName(el, k)) !== null;
    return k in t;
  },
  set: function (t, k, v, recv) {
    if (listIndex(k) >= 0 || k === 'length') return true;
    return Reflect.set(t, k, v, recv);
  },
  ownKeys: function (t) {
    var el = t[LIVE].root;
    var keys = [];
    var n = el.a ? el.a.length / 2 : 0;
    for (var i = 0; i < n; i++) keys.push(String(i));
    var own = Reflect.ownKeys(t);
    for (var j = 0; j < own.length; j++) if (own[j] !== LIVE) keys.push(own[j]);
    return keys;
  },
  getOwnPropertyDescriptor: function (t, k) {
    var el = t[LIVE].root;
    var i = listIndex(k);
    if (i >= 0) {
      if (el.a && i * 2 < el.a.length) return { value: wrap(attrRecFor(el, el.a[i * 2])), writable: false, enumerable: true, configurable: true };
      return undefined;
    }
    if (k === LIVE) return undefined;
    return Reflect.getOwnPropertyDescriptor(t, k);
  }
};
function attributesOf(r) {
  if (!r.nm) {
    var t = objCreate(NamedNodeMap.prototype);
    defineProperty(t, LIVE, { value: { root: r }, configurable: true });
    r.nm = new Proxy(t, attrMapHandler);
  }
  return r.nm;
}
function attrByNS(el, ns, local) {
  var a = el.a;
  if (!a) return null;
  ns = ns === undefined || ns === '' ? null : ns;
  for (var i = 0; i < a.length; i += 2) {
    var name = a[i];
    var colon = name.indexOf(':');
    var px = colon > 0 ? name.slice(0, colon) : null;
    var ln = colon > 0 ? name.slice(colon + 1) : name;
    var am = el.am && el.am[name];
    var uri = am && am.nsu !== undefined ? am.nsu : (px === 'xlink' ? NS_XLINK : px === 'xml' ? NS_XML : (px === 'xmlns' || name === 'xmlns') ? NS_XMLNS : null);
    if (ns === null) {
      if (px === null && name === local && (!am || am.nsu === undefined || am.nsu === null)) return name;
    } else if (uri === ns && ln === local) {
      return name;
    }
  }
  return null;
}
mix(NamedNodeMap.prototype, {
  get length() { var el = attrsListOf(this); return el.a ? el.a.length / 2 : 0; },
  item: function (i) {
    var el = attrsListOf(this);
    i = i >>> 0;
    return el.a && i * 2 < el.a.length ? wrap(attrRecFor(el, el.a[i * 2])) : null;
  },
  getNamedItem: function (name) {
    var el = attrsListOf(this);
    name = attrName(el, str(name));
    return getAttrRaw(el, name) !== null ? wrap(attrRecFor(el, name)) : null;
  },
  getNamedItemNS: function (ns, local) {
    var el = attrsListOf(this);
    var name = attrByNS(el, ns, str(local));
    return name === null ? null : wrap(attrRecFor(el, name));
  },
  setNamedItem: function (attr) { return setAttributeNode(attrsListOf(this), nodeArg(attr, 'setNamedItem')); },
  setNamedItemNS: function (attr) { return setAttributeNode(attrsListOf(this), nodeArg(attr, 'setNamedItemNS')); },
  removeNamedItem: function (name) {
    var el = attrsListOf(this);
    name = attrName(el, str(name));
    if (getAttrRaw(el, name) === null) throw domErr('NotFoundError', "Failed to execute 'removeNamedItem': No item with name '" + name + "' was found.");
    var ar = attrRecFor(el, name);
    var o = wrap(ar);
    removeAttrRaw(el, name);
    return o;
  },
  removeNamedItemNS: function (ns, local) {
    var el = attrsListOf(this);
    var name = attrByNS(el, ns, str(local));
    if (name === null) throw domErr('NotFoundError', "Failed to execute 'removeNamedItemNS': No item was found.");
    var o = wrap(attrRecFor(el, name));
    removeAttrRaw(el, name);
    return o;
  }
});
defineProperty(NamedNodeMap.prototype, Symbol.iterator, {
  value: function () {
    var el = attrsListOf(this);
    var out = [];
    var a = el.a || [];
    for (var i = 0; i < a.length; i += 2) out.push(wrap(attrRecFor(el, a[i])));
    return out[Symbol.iterator]();
  },
  writable: true, configurable: true
});

function setAttributeNode(el, ar) {
  if (ar.t !== 2) throw new TypeError("parameter is not of type 'Attr'.");
  if (ar.el && ar.el !== el) throw domErr('InUseAttributeError', 'The attribute is in use by another element.');
  var name = ar.name;
  var oldVal = getAttrRaw(el, name);
  var old = null;
  if (oldVal !== null) {
    var prev = el.am && el.am[name];
    if (prev === ar) return wrap(ar);
    if (prev) {
      prev.val = oldVal;
      prev.el = null;
      old = wrap(prev);
    } else {
      old = wrap({ t: 2, d: el.d, p: null, n: null, v: null, c: false, o: null, name: name, el: null, val: oldVal, px: ar.px, lname: ar.lname });
    }
  }
  var value = ar.val === null ? '' : ar.val;
  var am = el.am || (el.am = objCreate(null));
  am[name] = ar;
  ar.el = el;
  ar.d = el.d;
  setAttrRaw(el, name, value);
  return old;
}

// ---------------------------------------------------------------- Document

var Document = iface('Document', Node, function Document() {
  if (!(this instanceof Document)) throw new TypeError("Failed to construct 'Document': Please use the 'new' operator.");
  var d = newDocRec('about:blank', true);
  return wrap(d);
});
var HTMLDocument = iface('HTMLDocument', Document);
var XMLDocument = iface('XMLDocument', Document);

function documentElementOf(d) { return firstElementChild(d); }
function headOf(d) {
  var html = documentElementOf(d);
  if (!html || html.ln !== 'html') return null;
  for (var k = html.f; k; k = k.n) if (k.t === 1 && k.ln === 'head' && k.ns === 0) return k;
  return null;
}
function bodyOf(d) {
  var html = documentElementOf(d);
  if (!html || html.ln !== 'html') return null;
  for (var k = html.f; k; k = k.n) if (k.t === 1 && k.ns === 0 && (k.ln === 'body' || k.ln === 'frameset')) return k;
  return null;
}
function titleElementOf(d) {
  var root = documentElementOf(d);
  if (!root) return null;
  if (root.ns === 1 && root.ln === 'svg') {
    for (var k = root.f; k; k = k.n) if (k.t === 1 && k.ns === 1 && k.ln === 'title') return k;
    return null;
  }
  for (var e = nextElement(d, d); e; e = nextElement(e, d)) if (e.ln === 'title' && e.ns === 0) return e;
  return null;
}

function createElementForDoc(d, localName, options) {
  var name = str(localName);
  checkName(name, 'createElement');
  if (d.html) name = asciiLower(name);
  var ns = d.html || d.ctype === 'application/xhtml+xml' ? 0 : 3;
  var def = ns === 0 ? ceDefinitionFor(name) : null;
  if (def) return ceCreateSync(d, def);
  // createElement(tag, { is }) creates the built-in tag with its `is`
  // attribute, then upgrades it through the definition that names that tag
  // (APP-13).
  var is = ns === 0 && options && options.is !== undefined && options.is !== null ? str(options.is) : null;
  var r = createElementRec(d, name, ns);
  if (is !== null) setAttrRaw(r, 'is', is);
  if (ns === 3) r.nsu = null;
  if (name === 'script' && ns === 0) r.fa = true;
  if (ns === 0) {
    var builtin = ceBuiltinDefinition(r);
    if (builtin) ceUpgrade(r, builtin);
  }
  return wrap(r);
}

function createElementNSForDoc(d, ns, qname) {
  ns = ns === null || ns === undefined || ns === '' ? null : str(ns);
  qname = str(qname);
  checkName(qname, 'createElementNS');
  var colon = qname.indexOf(':');
  var px = colon > 0 ? qname.slice(0, colon) : null;
  var ln = colon > 0 ? qname.slice(colon + 1) : qname;
  if (px !== null && ns === null) throw domErr('NamespaceError', 'A prefix requires a namespace.');
  if (px === 'xml' && ns !== NS_XML) throw domErr('NamespaceError', 'The xml prefix needs the XML namespace.');
  var code = ns === null ? 3 : nsCode(ns);
  if (code === 0 && !px) {
    var def = ceDefinitionFor(ln);
    if (def) return ceCreateSync(d, def);
  }
  var r = createElementRec(d, ln, code);
  if (code === 3) r.nsu = ns;
  if (px) r.px = px;
  if (code === 0 && ln === 'script') r.fa = true;
  return wrap(r);
}

function documentOf(o) {
  var r = R(o);
  if (r.t !== 9) throw new TypeError('Illegal invocation');
  return r;
}

// The dependency lists of the document's own collections (see depVersion).
var DEPS_TREE = { deps: [] };
var DEPS_HREF = { deps: ['href'] };
var DEPS_NAME = { deps: ['name'] };

var docCollections = {
  images: function (e) { return e.ln === 'img' && e.ns === 0; },
  embeds: function (e) { return e.ln === 'embed' && e.ns === 0; },
  plugins: function (e) { return e.ln === 'embed' && e.ns === 0; },
  links: function (e) { return (e.ln === 'a' || e.ln === 'area') && e.ns === 0 && getAttrRaw(e, 'href') !== null; },
  forms: function (e) { return e.ln === 'form' && e.ns === 0; },
  scripts: function (e) { return e.ln === 'script' && e.ns === 0; },
  anchors: function (e) { return e.ln === 'a' && e.ns === 0 && getAttrRaw(e, 'name') !== null; }
};

function pad2(n) { return n < 10 ? '0' + n : '' + n; }

mix(Document.prototype, {
  get implementation() {
    var d = documentOf(this);
    if (!d.impl) {
      d.impl = objCreate(DOMImplementation.prototype);
      defineProperty(d.impl, $, { value: d, configurable: true });
    }
    return d.impl;
  },
  get URL() { return documentOf(this).url; },
  get documentURI() { return documentOf(this).url; },
  get compatMode() { return documentOf(this).mode === 'quirks' ? 'BackCompat' : 'CSS1Compat'; },
  get characterSet() { return 'UTF-8'; },
  get charset() { return 'UTF-8'; },
  get inputEncoding() { return 'UTF-8'; },
  get contentType() { var d = documentOf(this); return d.ctype || (d.html ? 'text/html' : 'application/xml'); },
  get doctype() {
    for (var k = documentOf(this).f; k; k = k.n) if (k.t === 10) return wrap(k);
    return null;
  },
  get documentElement() { return wrap(documentElementOf(documentOf(this))); },
  get scrollingElement() { return wrap(documentElementOf(documentOf(this))); },
  getElementById: function (id) {
    var d = documentOf(this);
    id = str(id);
    var l = d.ids[id];
    if (l && l.length) return wrap(l.length === 1 ? l[0] : firstInTreeOrder(l));
    if (!d.c) {
      for (var e = nextElement(d, d); e; e = nextElement(e, d)) if (getAttrRaw(e, 'id') === id) return wrap(e);
    }
    return null;
  },
  getElementsByName: function (name) {
    var d = documentOf(this);
    name = str(name);
    return cachedLive(d, 'gn:' + name, NodeList.prototype, {
      kind: 2, root: d, name: name, deps: ['name'],
      test: function (e, s) { return e.ns === 0 && getAttrRaw(e, 'name') === s.name; }
    });
  },
  createElement: function (localName, options) {
    return createElementForDoc(documentOf(this), localName, options);
  },
  createElementNS: function (ns, qname) {
    requiredArgs(arguments, 2, 'createElementNS');
    return createElementNSForDoc(documentOf(this), ns, qname);
  },
  createDocumentFragment: function () { return wrap(newFragmentRec(documentOf(this))); },
  createTextNode: function (data) {
    return wrap(textRec(documentOf(this), str(data)));
  },
  createCDATASection: function (data) {
    var d = documentOf(this);
    if (d.html) throw domErr('NotSupportedError', 'This operation is not supported for HTML documents.');
    return wrap(cdataRec(d, str(data)));
  },
  createComment: function (data) {
    return wrap(commentRec(documentOf(this), str(data)));
  },
  createProcessingInstruction: function (target, data) {
    target = str(target);
    checkName(target, 'createProcessingInstruction');
    data = str(data);
    if (data.indexOf('?>') >= 0) throw domErr('InvalidCharacterError', 'The data contains "?>".');
    return wrap(piRec(documentOf(this), target, data));
  },
  createAttribute: function (name) {
    var d = documentOf(this);
    name = str(name);
    checkName(name, 'createAttribute');
    if (d.html) name = asciiLower(name);
    return wrap({ t: 2, d: d, p: null, n: null, v: null, c: false, o: null, name: name, el: null, val: '', px: null, lname: name });
  },
  createAttributeNS: function (ns, qname) {
    var d = documentOf(this);
    qname = str(qname);
    checkName(qname, 'createAttributeNS');
    var colon = qname.indexOf(':');
    return wrap({
      t: 2, d: d, p: null, n: null, v: null, c: false, o: null, name: qname, el: null, val: '',
      px: colon > 0 ? qname.slice(0, colon) : null, lname: colon > 0 ? qname.slice(colon + 1) : qname,
      nsu: ns === undefined || ns === '' ? null : ns
    });
  },
  importNode: function (node, deep) {
    var r = nodeArg(node, 'importNode');
    if (r.t === 9 || r.shadow) throw domErr('NotSupportedError', 'This node cannot be imported.');
    return wrap(cloneRec(r, documentOf(this), !!deep));
  },
  adoptNode: function (node) {
    var r = nodeArg(node, 'adoptNode');
    if (r.t === 9) throw domErr('NotSupportedError', 'Documents cannot be adopted.');
    if (r.shadow) throw domErr('HierarchyRequestError', 'Shadow roots cannot be adopted.');
    adoptRec(r, documentOf(this));
    return node;
  },
  get location() {
    var d = documentOf(this);
    return d === mainDoc ? (global.location === undefined ? null : global.location) : null;
  },
  set location(v) {
    var d = documentOf(this);
    if (d === mainDoc && global.location) global.location.href = str(v);
  },
  get domain() {
    var p = parseURLParts(documentOf(this).url);
    return p ? p.hostname : '';
  },
  set domain(v) { /* document.domain setter is deprecated; keep the current value */ },
  get referrer() { var d = documentOf(this); return d.referrer || ''; },
  get cookie() {
    var d = documentOf(this);
    if (d !== mainDoc) return '';
    try { return str(N.cookieGet() || ''); } catch (e) { return ''; }
  },
  set cookie(v) {
    var d = documentOf(this);
    if (d !== mainDoc) return;
    try { N.cookieSet(str(v)); } catch (e) { /* refused */ }
  },
  get lastModified() {
    var t = new Date();
    return pad2(t.getMonth() + 1) + '/' + pad2(t.getDate()) + '/' + t.getFullYear() + ' ' +
      pad2(t.getHours()) + ':' + pad2(t.getMinutes()) + ':' + pad2(t.getSeconds());
  },
  get readyState() { return documentOf(this).ready; },
  get title() {
    var d = documentOf(this);
    var t = titleElementOf(d);
    return t ? collapseWS(childText(t)) : '';
  },
  set title(v) {
    var d = documentOf(this);
    var t = titleElementOf(d);
    if (!t) {
      var head = headOf(d);
      if (!head) return;
      t = createElementRec(d, 'title', 0);
      insertNode(head, t, null);
    }
    setTextContent(t, str(v));
  },
  get dir() {
    var html = documentElementOf(documentOf(this));
    var v = html ? getAttrRaw(html, 'dir') : null;
    v = v ? asciiLower(v) : '';
    return v === 'ltr' || v === 'rtl' || v === 'auto' ? v : '';
  },
  set dir(v) {
    var html = documentElementOf(documentOf(this));
    if (html) setAttrRaw(html, 'dir', str(v));
  },
  get body() { return wrap(bodyOf(documentOf(this))); },
  set body(v) {
    var d = documentOf(this);
    var r = unwrap(v);
    if (!r || r.t !== 1 || (r.ln !== 'body' && r.ln !== 'frameset')) throw domErr('HierarchyRequestError', 'The new body element is of type ' + (r ? r.ln : 'null') + '.');
    var old = bodyOf(d);
    if (old === r) return;
    if (old) {
      replaceChildRaw(old.p, r, old);
      return;
    }
    var html = documentElementOf(d);
    if (!html) throw domErr('HierarchyRequestError', 'No document element.');
    insertNode(html, r, null);
  },
  get head() { return wrap(headOf(documentOf(this))); },
  get images() { return filteredCollection(documentOf(this), 'images', docCollections.images, DEPS_TREE); },
  get embeds() { return filteredCollection(documentOf(this), 'embeds', docCollections.embeds, DEPS_TREE); },
  get plugins() { return filteredCollection(documentOf(this), 'embeds', docCollections.embeds, DEPS_TREE); },
  get links() { return filteredCollection(documentOf(this), 'links', docCollections.links, DEPS_HREF); },
  get forms() { return filteredCollection(documentOf(this), 'forms', docCollections.forms, DEPS_TREE); },
  get scripts() { return filteredCollection(documentOf(this), 'scripts', docCollections.scripts, DEPS_TREE); },
  get anchors() { return filteredCollection(documentOf(this), 'anchors', docCollections.anchors, DEPS_NAME); },
  get applets() { return filteredCollection(documentOf(this), 'applets', function () { return false; }); },
  get all() { return undefined; },
  get currentScript() { var d = documentOf(this); return d.cur ? wrap(d.cur) : null; },
  get defaultView() { return documentOf(this) === mainDoc ? global : null; },
  get activeElement() {
    var d = documentOf(this);
    if (d.active && d.active.c && d.active.d === d) return wrap(d.active);
    return wrap(bodyOf(d) || documentElementOf(d));
  },
  get hidden() { return false; },
  get visibilityState() { return 'visible'; },
  get webkitHidden() { return false; },
  get webkitVisibilityState() { return 'visible'; },
  get fullscreenEnabled() { return false; },
  get fullscreen() { return false; },
  get fullscreenElement() { return null; },
  get webkitFullscreenElement() { return null; },
  get pointerLockElement() { return null; },
  get pictureInPictureEnabled() { return false; },
  get pictureInPictureElement() { return null; },
  get designMode() { return 'off'; },
  set designMode(v) { /* editing is not supported */ },
  get timeline() { return { currentTime: performanceNow() }; },
  hasFocus: function () { return documentOf(this) === mainDoc; },
  open: function () { return this; },
  close: function () {},
  write: function () {
    var d = documentOf(this);
    var text = '';
    for (var i = 0; i < arguments.length; i++) text += str(arguments[i]);
    hooks.write(d, text);
  },
  writeln: function () {
    var d = documentOf(this);
    var text = '';
    for (var i = 0; i < arguments.length; i++) text += str(arguments[i]);
    hooks.write(d, text + '\n');
  },
  execCommand: function () { return false; },
  queryCommandEnabled: function () { return false; },
  queryCommandSupported: function () { return false; },
  queryCommandState: function () { return false; },
  queryCommandValue: function () { return ''; },
  elementFromPoint: function () { var d = documentOf(this); return wrap(bodyOf(d) || documentElementOf(d)); },
  elementsFromPoint: function () {
    var d = documentOf(this);
    var out = [];
    var b = bodyOf(d), h = documentElementOf(d);
    if (b) out.push(wrap(b));
    if (h) out.push(wrap(h));
    return out;
  },
  caretRangeFromPoint: function () { return null; },
  caretPositionFromPoint: function () { return null; },
  exitFullscreen: function () { return Promise.resolve(); },
  webkitExitFullscreen: function () {},
  exitPointerLock: function () {},
  exitPictureInPicture: function () { return Promise.resolve(); },
  getAnimations: function () { return []; },
  startViewTransition: function (cb) {
    var done;
    try { done = Promise.resolve(typeof cb === 'function' ? cb() : undefined); } catch (e) { done = Promise.reject(e); }
    return { finished: done, ready: done, updateCallbackDone: done, skipTransition: function () {} };
  },
  hasStorageAccess: function () { return Promise.resolve(false); },
  requestStorageAccess: function () { return Promise.reject(domErr('NotAllowedError', 'Storage access is not available.')); }
});
mix(Document.prototype, parentNodeMixin);

// The page's `document` (F1 owns this global; the window parts must not redefine it).
defineProperty(global, 'document', {
  get: function () { return mainDoc ? wrap(mainDoc) : null; },
  enumerable: true, configurable: true
});

function performanceNow() {
  try { return +N.perfNow(); } catch (e) { return 0; }
}

// DOMImplementation
var DOMImplementation = iface('DOMImplementation', null);
mix(DOMImplementation.prototype, {
  createHTMLDocument: function (title) {
    var d = newDocRec('about:blank');
    var dt = doctypeRec(d, 'html', '', '');
    attachBuilt(d, dt);
    var html = createElementRec(d, 'html', 0);
    attachBuilt(d, html);
    var head = createElementRec(d, 'head', 0);
    attachBuilt(html, head);
    if (title !== undefined) {
      var t = createElementRec(d, 'title', 0);
      attachBuilt(head, t);
      attachBuilt(t, textRec(d, str(title)));
    }
    attachBuilt(html, createElementRec(d, 'body', 0));
    return wrap(d);
  },
  createDocument: function (ns, qname, doctype) {
    var d = newDocRec('about:blank', true);
    if (ns === NS_HTML) d.ctype = 'application/xhtml+xml';
    else if (ns === NS_SVG) d.ctype = 'image/svg+xml';
    else d.ctype = 'application/xml';
    var dt = unwrap(doctype);
    if (dt) insertNode(d, dt, null);
    if (qname !== null && qname !== undefined && str(qname) !== '') {
      var el = unwrap(createElementNSForDoc(d, ns, qname));
      insertNode(d, el, null);
    }
    return wrap(d);
  },
  createDocumentType: function (name, pub, sys) {
    name = str(name);
    checkName(name, 'createDocumentType');
    return wrap(doctypeRec(R(this), name, str(pub), str(sys)));
  },
  hasFeature: function () { return true; }
});

if (testHooks) {
  testHooks.wrap = wrap;
  testHooks.unwrap = unwrap;
  testHooks.bootDocument = bootDocument;
  testHooks.newDocRec = newDocRec;
  testHooks.parseFragmentRecs = parseFragmentRecs;
  testHooks.insertNode = insertNode;
  testHooks.removeNode = removeNode;
  testHooks.textOf = textOf;
  testHooks.getAttr = getAttr;
  testHooks.setAttr = setAttr;
  testHooks.resolveURL = resolveURL;
}
