// 55_traversal.js — MutationObserver, TreeWalker, NodeIterator, Range, Selection, DOMParser, XMLSerializer.
//
// MutationObserver is real (Alpine, Stimulus, Turbo and many lazy loaders initialise what scripts add
// later through it): records are queued as the tree changes and delivered in one microtask. Range covers
// what libraries use to build fragments (selectNodeContents, createContextualFragment, extract/clone of
// simple ranges); it is not live across later mutations.

// ---------------------------------------------------------------- MutationObserver

var MO = Symbol('jukahub.observer');
var moActive = 0;
var moPending = [];
var moScheduled = false;
var moSeq = 0;

function moState(o) {
  var s = o !== null && o !== undefined ? o[MO] : undefined;
  if (!s) throw new TypeError('Illegal invocation');
  return s;
}

function MutationObserver(cb) {
  if (!new.target) throw new TypeError("Failed to construct 'MutationObserver': Please use the 'new' operator.");
  if (typeof cb !== 'function') throw new TypeError("Failed to construct 'MutationObserver': parameter 1 is not of type 'MutationCallback'.");
  defineProperty(this, MO, { value: { cb: cb, records: [], nodes: [], id: ++moSeq, obj: this }, configurable: true });
}
iface('MutationObserver', null, MutationObserver);
defineGlobal('WebKitMutationObserver', MutationObserver);
var MutationRecord = iface('MutationRecord', null);

mix(MutationObserver.prototype, {
  observe: function (target, options) {
    var s = moState(this);
    var r = nodeArg(target, 'observe');
    options = options || {};
    var o = {
      childList: !!options.childList,
      attributes: options.attributes === undefined ? undefined : !!options.attributes,
      characterData: options.characterData === undefined ? undefined : !!options.characterData,
      subtree: !!options.subtree,
      attributeOldValue: options.attributeOldValue === undefined ? undefined : !!options.attributeOldValue,
      characterDataOldValue: options.characterDataOldValue === undefined ? undefined : !!options.characterDataOldValue,
      attributeFilter: options.attributeFilter === undefined ? undefined : Array.from(options.attributeFilter, str)
    };
    if ((o.attributeOldValue !== undefined || o.attributeFilter !== undefined) && o.attributes === undefined) o.attributes = true;
    if (o.characterDataOldValue !== undefined && o.characterData === undefined) o.characterData = true;
    if (!o.childList && !o.attributes && !o.characterData) throw new TypeError("Failed to execute 'observe' on 'MutationObserver': The options object must set at least one of 'attributes', 'characterData', or 'childList' to true.");
    if (o.attributeOldValue && !o.attributes) throw new TypeError("Failed to execute 'observe' on 'MutationObserver': The options object may only set 'attributeOldValue' to true when 'attributes' is true or not present.");
    if (o.attributeFilter !== undefined && !o.attributes) throw new TypeError("Failed to execute 'observe' on 'MutationObserver': The options object may only set 'attributeFilter' when 'attributes' is true or not present.");
    if (o.characterDataOldValue && !o.characterData) throw new TypeError("Failed to execute 'observe' on 'MutationObserver': The options object may only set 'characterDataOldValue' to true when 'characterData' is true or not present.");
    var regs = r.mo || (r.mo = []);
    for (var i = 0; i < regs.length; i++) {
      if (regs[i].obs === s) {
        regs[i].opts = o;
        return;
      }
    }
    regs.push({ obs: s, opts: o });
    s.nodes.push(r);
    moActive++;
  },
  disconnect: function () {
    var s = moState(this);
    for (var i = 0; i < s.nodes.length; i++) {
      var regs = s.nodes[i].mo;
      if (!regs) continue;
      for (var j = regs.length - 1; j >= 0; j--) {
        if (regs[j].obs === s) {
          regs.splice(j, 1);
          moActive--;
        }
      }
    }
    s.nodes = [];
    s.records = [];
  },
  takeRecords: function () {
    var s = moState(this);
    var out = s.records;
    s.records = [];
    return out;
  }
});

function makeRecord(type, target, added, removed, prev, next, name, old) {
  var rec = objCreate(MutationRecord.prototype);
  rec.type = type;
  rec.target = wrap(target);
  rec.addedNodes = makeStaticList(added || []);
  rec.removedNodes = makeStaticList(removed || []);
  rec.previousSibling = prev ? wrap(prev) : null;
  rec.nextSibling = next ? wrap(next) : null;
  rec.attributeName = name === undefined ? null : name;
  rec.attributeNamespace = null;
  rec.oldValue = old === undefined ? null : old;
  return rec;
}

function moInterested(target, type, name) {
  var out = null;
  for (var node = target; node; node = node.p) {
    var regs = node.mo;
    if (!regs) continue;
    for (var i = 0; i < regs.length; i++) {
      var o = regs[i].opts;
      if (node !== target && !o.subtree) continue;
      if (type === 'attributes') {
        if (!o.attributes) continue;
        if (o.attributeFilter && o.attributeFilter.indexOf(name) < 0) continue;
      } else if (type === 'characterData') {
        if (!o.characterData) continue;
      } else if (!o.childList) {
        continue;
      }
      out = out || new Map();
      var wantOld = (type === 'attributes' && o.attributeOldValue) || (type === 'characterData' && o.characterDataOldValue);
      out.set(regs[i].obs, !!(out.get(regs[i].obs) || wantOld));
    }
  }
  return out;
}
function moEnqueue(s, record) {
  s.records.push(record);
  if (moPending.indexOf(s) < 0) moPending.push(s);
  if (!moScheduled) {
    moScheduled = true;
    Promise.resolve().then(moNotify);
  }
}
function moNotify() {
  moScheduled = false;
  var list = moPending.sort(function (a, b) { return a.id - b.id; });
  moPending = [];
  for (var i = 0; i < list.length; i++) {
    var s = list[i];
    var records = s.records;
    s.records = [];
    if (!records.length) continue;
    try {
      s.cb.call(s.obj, records, s.obj);
    } catch (e) {
      reportException(e);
    }
  }
}
// moRecover runs after an interrupted call: goja dropped the promise job that would have delivered the
// pending records, and moScheduled would otherwise keep every later mutation from scheduling a new one.
function moRecover() {
  moScheduled = false;
  if (moPending.length) {
    moScheduled = true;
    Promise.resolve().then(moNotify);
  }
}
function moQueueChildList(target, added, removed, prev, next) {
  var obs = moInterested(target, 'childList');
  if (!obs) return;
  obs.forEach(function (wantOld, s) {
    moEnqueue(s, makeRecord('childList', target, added, removed, prev, next));
  });
}
function moQueueAttribute(target, name, old) {
  var obs = moInterested(target, 'attributes', name);
  if (!obs) return;
  obs.forEach(function (wantOld, s) {
    moEnqueue(s, makeRecord('attributes', target, null, null, null, null, name, wantOld ? old : null));
  });
}
function moQueueCharacterData(target, old) {
  var obs = moInterested(target, 'characterData');
  if (!obs) return;
  obs.forEach(function (wantOld, s) {
    moEnqueue(s, makeRecord('characterData', target, null, null, null, null, undefined, wantOld ? old : null));
  });
}
// Transient registrations (observing a node's subtree after it was removed) are not kept.
function moTransient(parent, node) {}

// ---------------------------------------------------------------- NodeFilter, TreeWalker, NodeIterator

function NodeFilter() { illegalConstructor(); }
iface('NodeFilter', null, NodeFilter);
var NODE_FILTER_CONSTANTS = {
  FILTER_ACCEPT: 1, FILTER_REJECT: 2, FILTER_SKIP: 3, SHOW_ALL: 0xFFFFFFFF, SHOW_ELEMENT: 0x1, SHOW_ATTRIBUTE: 0x2,
  SHOW_TEXT: 0x4, SHOW_CDATA_SECTION: 0x8, SHOW_ENTITY_REFERENCE: 0x10, SHOW_ENTITY: 0x20,
  SHOW_PROCESSING_INSTRUCTION: 0x40, SHOW_COMMENT: 0x80, SHOW_DOCUMENT: 0x100, SHOW_DOCUMENT_TYPE: 0x200,
  SHOW_DOCUMENT_FRAGMENT: 0x400, SHOW_NOTATION: 0x800
};
constants(NodeFilter, NODE_FILTER_CONSTANTS);
constants(NodeFilter.prototype, NODE_FILTER_CONSTANTS);

var TW = Symbol('jukahub.walker');
function twState(o) {
  var s = o !== null && o !== undefined ? o[TW] : undefined;
  if (!s) throw new TypeError('Illegal invocation');
  return s;
}
function filterNode(s, r) {
  if (s.active) throw domErr('InvalidStateError', 'The filter is already running.');
  var n = r.t - 1;
  if (!((s.what >>> n) & 1)) return 3;
  var f = s.filter;
  if (f === null || f === undefined) return 1;
  flagOn(s, 'active');
  var res;
  try {
    if (typeof f === 'function') res = f(wrap(r));
    else res = f.acceptNode(wrap(r));
  } finally {
    flagOff(s, 'active');
  }
  res = Number(res);
  return res === 1 || res === 2 || res === 3 ? res : 3;
}

var TreeWalker = iface('TreeWalker', null);
function makeWalker(Proto, root, what, filter) {
  var o = objCreate(Proto);
  defineProperty(o, TW, {
    value: { root: root, cur: root, what: what === undefined ? 0xFFFFFFFF : what >>> 0, filter: filter === undefined ? null : filter, active: false, ref: root, before: true },
    configurable: true
  });
  return o;
}
function twTraverseChildren(s, first) {
  var node = first ? s.cur.f : s.cur.l;
  while (node) {
    var result = filterNode(s, node);
    if (result === 1) {
      s.cur = node;
      return wrap(node);
    }
    if (result === 3) {
      var child = first ? node.f : node.l;
      if (child) {
        node = child;
        continue;
      }
    }
    while (node) {
      var sibling = first ? node.n : node.v;
      if (sibling) {
        node = sibling;
        break;
      }
      var parent = node.p;
      if (!parent || parent === s.root || parent === s.cur) return null;
      node = parent;
    }
  }
  return null;
}
function twTraverseSiblings(s, next) {
  var node = s.cur;
  if (node === s.root) return null;
  for (;;) {
    var sibling = next ? node.n : node.v;
    while (sibling) {
      node = sibling;
      var result = filterNode(s, node);
      if (result === 1) {
        s.cur = node;
        return wrap(node);
      }
      sibling = next ? node.f : node.l;
      if (result === 2 || !sibling) sibling = next ? node.n : node.v;
    }
    node = node.p;
    if (!node || node === s.root) return null;
    if (filterNode(s, node) === 1) return null;
  }
}
mix(TreeWalker.prototype, {
  get root() { return wrap(twState(this).root); },
  get whatToShow() { return twState(this).what; },
  get filter() { return twState(this).filter; },
  get currentNode() { return wrap(twState(this).cur); },
  set currentNode(v) { twState(this).cur = nodeArg(v, 'currentNode'); },
  parentNode: function () {
    var s = twState(this);
    var node = s.cur;
    while (node && node !== s.root) {
      node = node.p;
      if (node && filterNode(s, node) === 1) {
        s.cur = node;
        return wrap(node);
      }
    }
    return null;
  },
  firstChild: function () { return twTraverseChildren(twState(this), true); },
  lastChild: function () { return twTraverseChildren(twState(this), false); },
  nextSibling: function () { return twTraverseSiblings(twState(this), true); },
  previousSibling: function () { return twTraverseSiblings(twState(this), false); },
  previousNode: function () {
    var s = twState(this);
    var node = s.cur;
    while (node !== s.root) {
      var sibling = node.v;
      while (sibling) {
        node = sibling;
        var result = filterNode(s, node);
        while (result !== 2 && node.l) {
          node = node.l;
          result = filterNode(s, node);
        }
        if (result === 1) {
          s.cur = node;
          return wrap(node);
        }
        sibling = node.v;
      }
      if (node === s.root || !node.p) return null;
      node = node.p;
      if (filterNode(s, node) === 1) {
        s.cur = node;
        return wrap(node);
      }
    }
    return null;
  },
  nextNode: function () {
    var s = twState(this);
    var node = s.cur;
    var result = 1;
    for (;;) {
      while (result !== 2 && node.f) {
        node = node.f;
        result = filterNode(s, node);
        if (result === 1) {
          s.cur = node;
          return wrap(node);
        }
      }
      var temporary = node;
      var sibling = null;
      while (temporary) {
        if (temporary === s.root) return null;
        sibling = temporary.n;
        if (sibling) {
          node = sibling;
          break;
        }
        temporary = temporary.p;
      }
      if (!temporary) return null;
      result = filterNode(s, node);
      if (result === 1) {
        s.cur = node;
        return wrap(node);
      }
    }
  }
});

var NodeIterator = iface('NodeIterator', null);
function precedingInclusive(r, root) {
  if (r === root) return null;
  var s = r.v;
  if (s) {
    while (s.l) s = s.l;
    return s;
  }
  return r.p;
}
function niTraverse(s, next) {
  var node = s.ref;
  var before = s.before;
  for (;;) {
    if (next) {
      if (!before) {
        node = nextNode(node, s.root);
        if (!node) return null;
      } else {
        before = false;
      }
    } else if (before) {
      node = precedingInclusive(node, s.root);
      if (!node) return null;
    } else {
      before = true;
    }
    if (filterNode(s, node) === 1) break;
  }
  s.ref = node;
  s.before = before;
  return wrap(node);
}
mix(NodeIterator.prototype, {
  get root() { return wrap(twState(this).root); },
  get referenceNode() { return wrap(twState(this).ref); },
  get pointerBeforeReferenceNode() { return twState(this).before; },
  get whatToShow() { return twState(this).what; },
  get filter() { return twState(this).filter; },
  nextNode: function () { return niTraverse(twState(this), true); },
  previousNode: function () { return niTraverse(twState(this), false); },
  detach: function () {}
});

mix(Document.prototype, {
  createTreeWalker: function (root, what, filter) {
    requiredArgs(arguments, 1, 'createTreeWalker');
    return makeWalker(TreeWalker.prototype, nodeArg(root, 'createTreeWalker'), what, filter);
  },
  createNodeIterator: function (root, what, filter) {
    requiredArgs(arguments, 1, 'createNodeIterator');
    return makeWalker(NodeIterator.prototype, nodeArg(root, 'createNodeIterator'), what, filter);
  }
});

// ---------------------------------------------------------------- Range

var RG = Symbol('jukahub.range');
var AbstractRange = iface('AbstractRange', null);
function rangeState(o) {
  var s = o !== null && o !== undefined ? o[RG] : undefined;
  if (!s) throw new TypeError('Illegal invocation');
  return s;
}
function nodeLength(r) {
  if (r.t === 10) return 0;
  if (r.t === 3 || r.t === 4 || r.t === 7 || r.t === 8) return r.x.length;
  var n = 0;
  for (var k = r.f; k; k = k.n) n++;
  return n;
}
function childAt(r, i) {
  var k = r.f;
  while (k && i > 0) {
    k = k.n;
    i--;
  }
  return k || null;
}
function Range() {
  if (!new.target) throw new TypeError("Failed to construct 'Range': Please use the 'new' operator.");
  var d = mainDoc || standaloneDoc();
  defineProperty(this, RG, { value: { sc: d, so: 0, ec: d, eo: 0 }, configurable: true });
}
iface('Range', AbstractRange, Range);
constants(Range, { START_TO_START: 0, START_TO_END: 1, END_TO_END: 2, END_TO_START: 3 });
function makeRangeFor(d) {
  var o = objCreate(Range.prototype);
  defineProperty(o, RG, { value: { sc: d, so: 0, ec: d, eo: 0 }, configurable: true });
  return o;
}
// comparePoints: -1 / 0 / 1 for (a, ao) relative to (b, bo).
function comparePoints(a, ao, b, bo) {
  if (a === b) return ao === bo ? 0 : (ao < bo ? -1 : 1);
  if (isInclusiveAncestor(a, b)) {
    var child = b;
    while (child.p !== a) child = child.p;
    return childIndex(child) < ao ? 1 : -1;
  }
  if (isInclusiveAncestor(b, a)) return -comparePoints(b, bo, a, ao);
  return precedes(a, b) ? -1 : 1;
}
function setBoundary(s, start, node, offset) {
  if (node.t === 10) throw domErr('InvalidNodeTypeError', 'The node provided is a doctype.');
  if (offset > nodeLength(node)) throw domErr('IndexSizeError', 'The offset ' + offset + ' is larger than the node\'s length (' + nodeLength(node) + ').');
  if (start) {
    s.sc = node;
    s.so = offset;
    if (treeRoot(s.ec) !== treeRoot(node) || comparePoints(node, offset, s.ec, s.eo) > 0) {
      s.ec = node;
      s.eo = offset;
    }
  } else {
    s.ec = node;
    s.eo = offset;
    if (treeRoot(s.sc) !== treeRoot(node) || comparePoints(node, offset, s.sc, s.so) < 0) {
      s.sc = node;
      s.so = offset;
    }
  }
}
function commonAncestor(a, b) {
  for (var x = a; x; x = x.p) if (isInclusiveAncestor(x, b)) return x;
  return treeRoot(a);
}
function rangeText(s) {
  if (s.sc === s.ec && (s.sc.t === 3 || s.sc.t === 4)) return s.sc.x.slice(s.so, s.eo);
  var out = '';
  if (s.sc.t === 3 || s.sc.t === 4) out += s.sc.x.slice(s.so);
  var root = commonAncestor(s.sc, s.ec);
  for (var r = nextNode(root, root); r; r = nextNode(r, root)) {
    if ((r.t === 3 || r.t === 4) && r !== s.sc && r !== s.ec) {
      if (comparePoints(r, 0, s.sc, s.so) >= 0 && comparePoints(r, r.x.length, s.ec, s.eo) <= 0) out += r.x;
    }
  }
  if (s.ec !== s.sc && (s.ec.t === 3 || s.ec.t === 4)) out += s.ec.x.slice(0, s.eo);
  return out;
}
// Extract/clone/delete for the common shapes: both boundaries in one node, or whole children of a
// common parent. Partially selected trees are approximated by their fully selected children.
function rangeContents(s, mode) {
  var d = docOf(s.sc);
  var frag = newFragmentRec(d);
  if (s.sc === s.ec && (s.sc.t === 3 || s.sc.t === 4 || s.sc.t === 7 || s.sc.t === 8)) {
    var piece = s.sc.x.slice(s.so, s.eo);
    if (mode !== 'delete') {
      var c = cloneOne(s.sc, d);
      c.x = piece;
      attachBuilt(frag, c);
    }
    if (mode !== 'clone') {
      replaceData(s.sc, s.so, s.eo - s.so, '');
      s.eo = s.so;
    }
    return frag;
  }
  var parent = commonAncestor(s.sc, s.ec);
  var startChild = s.sc === parent ? childAt(parent, s.so) : (function () { var x = s.sc; while (x.p !== parent) x = x.p; return x; })();
  var endChild = s.ec === parent ? childAt(parent, s.eo) : (function () { var x = s.ec; while (x.p !== parent) x = x.p; return x.n; })();
  var nodes = [];
  for (var k = startChild; k && k !== endChild; k = k.n) nodes.push(k);
  for (var i = 0; i < nodes.length; i++) {
    if (mode === 'clone') attachBuilt(frag, cloneRec(nodes[i], d, true));
    else if (mode === 'extract') insertRaw(nodes[i], frag, null, false);
    else removeRaw(nodes[i], false);
  }
  if (mode !== 'clone') {
    s.sc = s.ec = parent;
    s.so = s.eo = Math.min(s.so, nodeLength(parent));
    if (s.sc !== parent) s.so = 0;
  }
  return frag;
}
mix(AbstractRange.prototype, {
  get startContainer() { return wrap(rangeState(this).sc); },
  get startOffset() { return rangeState(this).so; },
  get endContainer() { return wrap(rangeState(this).ec); },
  get endOffset() { return rangeState(this).eo; },
  get collapsed() { var s = rangeState(this); return s.sc === s.ec && s.so === s.eo; }
});
mix(Range.prototype, {
  get commonAncestorContainer() { var s = rangeState(this); return wrap(commonAncestor(s.sc, s.ec)); },
  setStart: function (node, offset) { setBoundary(rangeState(this), true, nodeArg(node, 'setStart'), offset >>> 0); },
  setEnd: function (node, offset) { setBoundary(rangeState(this), false, nodeArg(node, 'setEnd'), offset >>> 0); },
  setStartBefore: function (node) {
    var r = nodeArg(node, 'setStartBefore');
    if (!r.p) throw domErr('InvalidNodeTypeError', 'The node has no parent.');
    setBoundary(rangeState(this), true, r.p, childIndex(r));
  },
  setStartAfter: function (node) {
    var r = nodeArg(node, 'setStartAfter');
    if (!r.p) throw domErr('InvalidNodeTypeError', 'The node has no parent.');
    setBoundary(rangeState(this), true, r.p, childIndex(r) + 1);
  },
  setEndBefore: function (node) {
    var r = nodeArg(node, 'setEndBefore');
    if (!r.p) throw domErr('InvalidNodeTypeError', 'The node has no parent.');
    setBoundary(rangeState(this), false, r.p, childIndex(r));
  },
  setEndAfter: function (node) {
    var r = nodeArg(node, 'setEndAfter');
    if (!r.p) throw domErr('InvalidNodeTypeError', 'The node has no parent.');
    setBoundary(rangeState(this), false, r.p, childIndex(r) + 1);
  },
  selectNode: function (node) {
    var r = nodeArg(node, 'selectNode');
    if (!r.p) throw domErr('InvalidNodeTypeError', 'The node has no parent.');
    var s = rangeState(this);
    var i = childIndex(r);
    s.sc = s.ec = r.p;
    s.so = i;
    s.eo = i + 1;
  },
  selectNodeContents: function (node) {
    var r = nodeArg(node, 'selectNodeContents');
    if (r.t === 10) throw domErr('InvalidNodeTypeError', 'The node is a doctype.');
    var s = rangeState(this);
    s.sc = s.ec = r;
    s.so = 0;
    s.eo = nodeLength(r);
  },
  collapse: function (toStart) {
    var s = rangeState(this);
    if (toStart) {
      s.ec = s.sc;
      s.eo = s.so;
    } else {
      s.sc = s.ec;
      s.so = s.eo;
    }
  },
  cloneRange: function () {
    var s = rangeState(this);
    var o = objCreate(Range.prototype);
    defineProperty(o, RG, { value: { sc: s.sc, so: s.so, ec: s.ec, eo: s.eo }, configurable: true });
    return o;
  },
  detach: function () {},
  toString: function () { return rangeText(rangeState(this)); },
  compareBoundaryPoints: function (how, other) {
    var a = rangeState(this), b = rangeState(other);
    switch (how) {
      case 0: return comparePoints(a.sc, a.so, b.sc, b.so);
      case 1: return comparePoints(a.ec, a.eo, b.sc, b.so);
      case 2: return comparePoints(a.ec, a.eo, b.ec, b.eo);
      case 3: return comparePoints(a.sc, a.so, b.ec, b.eo);
    }
    throw domErr('NotSupportedError', 'The comparison method provided must be one of the Range constants.');
  },
  comparePoint: function (node, offset) {
    var s = rangeState(this);
    var r = nodeArg(node, 'comparePoint');
    if (treeRoot(r) !== treeRoot(s.sc)) throw domErr('WrongDocumentError', 'The node provided and the Range are not in the same tree.');
    if (comparePoints(r, offset >>> 0, s.sc, s.so) < 0) return -1;
    if (comparePoints(r, offset >>> 0, s.ec, s.eo) > 0) return 1;
    return 0;
  },
  isPointInRange: function (node, offset) {
    var s = rangeState(this);
    var r = unwrap(node);
    if (!r || treeRoot(r) !== treeRoot(s.sc)) return false;
    return comparePoints(r, offset >>> 0, s.sc, s.so) >= 0 && comparePoints(r, offset >>> 0, s.ec, s.eo) <= 0;
  },
  intersectsNode: function (node) {
    var s = rangeState(this);
    var r = unwrap(node);
    if (!r || treeRoot(r) !== treeRoot(s.sc)) return false;
    var parent = r.p;
    if (!parent) return true;
    var i = childIndex(r);
    return comparePoints(parent, i, s.ec, s.eo) < 0 && comparePoints(parent, i + 1, s.sc, s.so) > 0;
  },
  deleteContents: function () { rangeContents(rangeState(this), 'delete'); },
  extractContents: function () { return wrap(rangeContents(rangeState(this), 'extract')); },
  cloneContents: function () { return wrap(rangeContents(rangeState(this), 'clone')); },
  insertNode: function (node) {
    var s = rangeState(this);
    var n = nodeArg(node, 'insertNode');
    var parent, ref;
    if (s.sc.t === 3) {
      parent = s.sc.p;
      if (!parent) throw domErr('HierarchyRequestError', 'The range start is detached.');
      var tail = textRec(s.sc.d, s.sc.x.slice(s.so));
      replaceData(s.sc, s.so, s.sc.x.length - s.so, '');
      insertRaw(tail, parent, s.sc.n, false);
      ref = tail;
    } else {
      parent = s.sc;
      ref = childAt(parent, s.so);
    }
    insertNode(parent, n, ref);
    if (s.sc === s.ec && s.so === s.eo && s.sc === parent) s.eo++;
  },
  surroundContents: function (newParent) {
    var p = nodeArg(newParent, 'surroundContents');
    var frag = this.extractContents();
    replaceAllRaw(p, null);
    this.insertNode(newParent);
    insertNode(p, unwrap(frag), null);
    this.selectNode(newParent);
  },
  createContextualFragment: function (html) {
    var s = rangeState(this);
    var ctx = s.sc.t === 1 ? s.sc : s.sc.p && s.sc.p.t === 1 ? s.sc.p : null;
    if (!ctx || (ctx.ns === 0 && ctx.ln === 'html')) ctx = createElementRec(docOf(s.sc), 'body', 0);
    return wrap(parseFragmentInto(ctx, strOrEmpty(html), 'contextual'));
  },
  getBoundingClientRect: function () { return new DOMRect(0, 0, 0, 0); },
  getClientRects: function () { return rectList([]); }
});
function StaticRange(init) {
  if (!new.target) throw new TypeError("Failed to construct 'StaticRange': Please use the 'new' operator.");
  init = init || {};
  var sc = nodeArg(init.startContainer, 'StaticRange');
  var ec = nodeArg(init.endContainer, 'StaticRange');
  defineProperty(this, RG, { value: { sc: sc, so: init.startOffset >>> 0, ec: ec, eo: init.endOffset >>> 0 }, configurable: true });
}
iface('StaticRange', AbstractRange, StaticRange);
mix(Document.prototype, {
  createRange: function () { return makeRangeFor(documentOf(this)); }
});
function rangesBeforeRemove(node) {}

// ---------------------------------------------------------------- Selection

var Selection = iface('Selection', null);
var selectionObj = null;
function documentSelection() {
  if (!selectionObj) {
    selectionObj = objCreate(Selection.prototype);
    defineProperty(selectionObj, RG, { value: { ranges: [] }, configurable: true });
  }
  return selectionObj;
}
mix(Selection.prototype, {
  get anchorNode() { var r = this[RG].ranges[0]; return r ? r.startContainer : null; },
  get anchorOffset() { var r = this[RG].ranges[0]; return r ? r.startOffset : 0; },
  get focusNode() { var r = this[RG].ranges[0]; return r ? r.endContainer : null; },
  get focusOffset() { var r = this[RG].ranges[0]; return r ? r.endOffset : 0; },
  get isCollapsed() { var r = this[RG].ranges[0]; return !r || r.collapsed; },
  get rangeCount() { return this[RG].ranges.length; },
  get type() { var l = this[RG].ranges; return !l.length ? 'None' : l[0].collapsed ? 'Caret' : 'Range'; },
  get direction() { return 'none'; },
  getRangeAt: function (i) {
    var l = this[RG].ranges;
    i = i >>> 0;
    if (i >= l.length) throw domErr('IndexSizeError', "Failed to execute 'getRangeAt' on 'Selection': " + i + ' is not a valid index.');
    return l[i];
  },
  addRange: function (range) { rangeState(range); if (!this[RG].ranges.length) this[RG].ranges.push(range); },
  removeRange: function (range) { var l = this[RG].ranges; var i = l.indexOf(range); if (i >= 0) l.splice(i, 1); },
  removeAllRanges: function () { this[RG].ranges = []; },
  empty: function () { this[RG].ranges = []; },
  collapse: function (node, offset) {
    if (node === null) { this[RG].ranges = []; return; }
    var r = makeRangeFor(docOf(nodeArg(node, 'collapse')));
    r.setStart(node, offset || 0);
    this[RG].ranges = [r];
  },
  setPosition: function (node, offset) { this.collapse(node, offset); },
  collapseToStart: function () { var r = this[RG].ranges[0]; if (r) r.collapse(true); },
  collapseToEnd: function () { var r = this[RG].ranges[0]; if (r) r.collapse(false); },
  extend: function () {},
  setBaseAndExtent: function (a, ao, f, fo) {
    var r = makeRangeFor(docOf(nodeArg(a, 'setBaseAndExtent')));
    r.setStart(a, ao || 0);
    r.setEnd(f, fo || 0);
    this[RG].ranges = [r];
  },
  selectAllChildren: function (node) {
    var r = makeRangeFor(docOf(nodeArg(node, 'selectAllChildren')));
    r.selectNodeContents(node);
    this[RG].ranges = [r];
  },
  deleteFromDocument: function () { var r = this[RG].ranges[0]; if (r) r.deleteContents(); },
  containsNode: function (node) { var r = this[RG].ranges[0]; return !!r && r.intersectsNode(node); },
  modify: function () {},
  getComposedRanges: function () { return []; },
  toString: function () { var r = this[RG].ranges[0]; return r ? r.toString() : ''; }
});
mix(Document.prototype, { getSelection: function () { return documentSelection(); } });

// ---------------------------------------------------------------- DOMParser, XMLSerializer

function parseDocumentString(html, url) {
  var d = newDocRec(url || (mainDoc ? mainDoc.url : 'about:blank'));
  var json = N.parseDocument(str(html));
  buildChildren(d, typeof json === 'string' ? JSON.parse(json) : json, 'inert');
  ensureSkeleton(d);
  d.mode = quirksModeFor(d);
  return d;
}

function DOMParser() {
  if (!new.target) throw new TypeError("Failed to construct 'DOMParser': Please use the 'new' operator.");
}
iface('DOMParser', null, DOMParser);
mix(DOMParser.prototype, {
  parseFromString: function (text, type) {
    requiredArgs(arguments, 2, 'parseFromString');
    type = asciiLower(str(type));
    text = str(text);
    if (type === 'text/html') return wrap(parseDocumentString(text));
    if (type !== 'text/xml' && type !== 'application/xml' && type !== 'application/xhtml+xml' && type !== 'image/svg+xml') {
      throw new TypeError("Failed to execute 'parseFromString' on 'DOMParser': The provided value '" + type + "' is not a valid enum value of type DOMParserSupportedType.");
    }
    // No XML parser: parse as an HTML fragment and make its first element the document element.
    var d = newDocRec(mainDoc ? mainDoc.url : 'about:blank', true);
    d.ctype = type;
    var body = text.replace(/^\s*<\?xml[^>]*\?>/, '').replace(/^\s*<!DOCTYPE[^>]*>/i, '');
    var frag = parseFragmentInto(createElementRec(d, 'body', 0), body, 'inert');
    var root = firstElementChild(frag);
    if (root) {
      unlink(root);
      attachBuilt(d, root);
      for (var x = root; x; x = nextNode(x, root)) x.c = true;
    }
    return wrap(d);
  }
});

function XMLSerializer() {
  if (!new.target) throw new TypeError("Failed to construct 'XMLSerializer': Please use the 'new' operator.");
}
iface('XMLSerializer', null, XMLSerializer);
mix(XMLSerializer.prototype, {
  serializeToString: function (node) {
    var r = nodeArg(node, 'serializeToString');
    if (r.t === 9 || r.t === 11) return serializeChildren(r);
    var out = serializeNode(r);
    if (r.t === 1 && r.ns === 1 && !getAttrRaw(r, 'xmlns')) out = out.replace(/^<svg/, '<svg xmlns="' + NS_SVG + '"');
    return out;
  }
});

if (testHooks) {
  testHooks.parseDocumentString = parseDocumentString;
}
