// 50_shadow_ce.js — shadow roots, slots, custom elements, declarative shadow DOM.
//
// Web-component sites (Lit, Stencil, GitHub's Catalyst, Shoelace …) put their content in shadow roots;
// the serializer flattens them so the reader shows that content. Custom elements are upgraded when they are
// defined (and when created or connected afterwards); the trick that makes `class X extends HTMLElement`
// work is that HTMLElement's constructor returns the element being upgraded, which the derived constructor
// then uses as `this`.

var ShadowRoot = iface('ShadowRoot', DocumentFragment);
var SHADOW_HOSTS = { article: 1, aside: 1, blockquote: 1, body: 1, div: 1, footer: 1, h1: 1, h2: 1, h3: 1, h4: 1,
  h5: 1, h6: 1, header: 1, main: 1, nav: 1, p: 1, section: 1, span: 1 };

function canHostShadow(r) {
  return r.t === 1 && r.ns === 0 && (!!SHADOW_HOSTS[r.ln] || validCustomElementName(r.ln));
}

function attachShadowRec(r, init, declarative) {
  if (!canHostShadow(r)) throw domErr('NotSupportedError', "Failed to execute 'attachShadow' on 'Element': This element does not support attachShadow");
  var mode = init && init.mode;
  if (mode !== 'open' && mode !== 'closed') throw new TypeError("Failed to execute 'attachShadow' on 'Element': The provided value '" + mode + "' is not a valid enum value of type ShadowRootMode.");
  if (r.sr) {
    if (r.sr.declarative && r.sr.mode === mode && !declarative) {
      replaceAllRaw(r.sr, null);
      r.sr.declarative = false;
      return r.sr;
    }
    throw domErr('NotSupportedError', "Failed to execute 'attachShadow' on 'Element': Shadow root cannot be created on a host which already hosts a shadow tree.");
  }
  var sr = newFragmentRec(r.d);
  sr.shadow = true;
  sr.host = r;
  sr.mode = mode;
  sr.df = !!(init && init.delegatesFocus);
  sr.slotAssignment = init && init.slotAssignment === 'manual' ? 'manual' : 'named';
  sr.clonable = !!(init && init.clonable);
  sr.serializable = !!(init && init.serializable);
  sr.declarative = !!declarative;
  sr.c = r.c;
  r.sr = sr;
  return sr;
}

function shadowOf(o) {
  var r = R(o);
  if (!r.shadow) throw new TypeError('Illegal invocation');
  return r;
}

mix(ShadowRoot.prototype, {
  get mode() { return shadowOf(this).mode; },
  get delegatesFocus() { return shadowOf(this).df; },
  get slotAssignment() { return shadowOf(this).slotAssignment; },
  get clonable() { return shadowOf(this).clonable; },
  get serializable() { return shadowOf(this).serializable; },
  get host() { return wrap(shadowOf(this).host); },
  get innerHTML() { return serializeChildren(shadowOf(this)); },
  set innerHTML(v) { setInnerHTML(shadowOf(this), strOrEmpty(v)); },
  getHTML: function () { return serializeChildren(shadowOf(this)); },
  setHTMLUnsafe: function (html) {
    var sr = shadowOf(this);
    var frag = parseFragmentInto(sr.host, strOrEmpty(html), 'fragment');
    attachDeclarativeShadowRoots(frag);
    replaceAllRaw(sr, frag);
  },
  get activeElement() {
    var sr = shadowOf(this);
    var a = sr.host.d.active;
    return a && treeRoot(a) === sr ? wrap(a) : null;
  },
  get styleSheets() { return styleSheetsOf(shadowOf(this)); },
  get fullscreenElement() { return null; },
  get pointerLockElement() { return null; },
  get pictureInPictureElement() { return null; },
  getAnimations: function () { return []; },
  elementFromPoint: function () { return null; },
  elementsFromPoint: function () { return []; },
  getSelection: function () { return documentSelection(); }
});
defineProperty(ShadowRoot.prototype, 'adoptedStyleSheets', adoptedAccessor(function (o) { return shadowOf(o); }));
defineEventHandlers(ShadowRoot.prototype, ['slotchange']);

mix(Element.prototype, {
  attachShadow: function (init) {
    requiredArgs(arguments, 1, 'attachShadow');
    return wrap(attachShadowRec(elementOf(this), init, false));
  },
  get shadowRoot() {
    var r = elementOf(this);
    return r.sr && r.sr.mode === 'open' ? wrap(r.sr) : null;
  }
});

// ---------------------------------------------------------------- slots

function slotName(slot) { var v = getAttrRaw(slot, 'name'); return v === null ? '' : v; }
function slottableName(n) {
  if (n.t !== 1) return '';
  var v = getAttrRaw(n, 'slot');
  return v === null ? '' : v;
}
function firstSlotNamed(sr, name) {
  for (var e = nextElement(sr, sr); e; e = nextElement(e, sr)) if (e.ln === 'slot' && e.ns === 0 && slotName(e) === name) return e;
  return null;
}
function slotAssigned(slot) {
  var sr = treeRoot(slot);
  if (!sr.shadow || !sr.host) return [];
  var host = sr.host;
  var out = [];
  if (sr.slotAssignment === 'manual') {
    var manual = slot.manual || [];
    for (var i = 0; i < manual.length; i++) if (manual[i].p === host) out.push(manual[i]);
    return out;
  }
  var name = slotName(slot);
  if (firstSlotNamed(sr, name) !== slot) return out;
  for (var k = host.f; k; k = k.n) {
    if ((k.t === 1 || k.t === 3) && slottableName(k) === name) out.push(k);
  }
  return out;
}
function assignedSlotOf(n) {
  var host = n.p;
  if (!host || host.t !== 1 || !host.sr) return null;
  var sr = host.sr;
  if (sr.slotAssignment === 'manual') {
    for (var e = nextElement(sr, sr); e; e = nextElement(e, sr)) {
      if (e.ln === 'slot' && e.manual && e.manual.indexOf(n) >= 0) return sr.mode === 'open' ? e : null;
    }
    return null;
  }
  var slot = firstSlotNamed(sr, slottableName(n));
  return slot && sr.mode === 'open' ? slot : null;
}
function flattenAssigned(slot, out) {
  var assigned = slotAssigned(slot);
  if (!assigned.length) {
    for (var k = slot.f; k; k = k.n) {
      if (k.t === 1 && k.ln === 'slot' && k.ns === 0) flattenAssigned(k, out);
      else if (k.t === 1 || k.t === 3) out.push(k);
    }
    return;
  }
  for (var i = 0; i < assigned.length; i++) {
    var a = assigned[i];
    if (a.t === 1 && a.ln === 'slot' && a.ns === 0 && inShadowTree(a)) flattenAssigned(a, out);
    else out.push(a);
  }
}
mix(HTMLSlotElement.prototype, {
  assignedNodes: function (opts) {
    var r = elementOf(this);
    if (opts && opts.flatten) {
      var out = [];
      flattenAssigned(r, out);
      return out.map(wrap);
    }
    return slotAssigned(r).map(wrap);
  },
  assignedElements: function (opts) {
    return this.assignedNodes(opts).filter(function (n) { return n.nodeType === 1; });
  },
  assign: function () {
    var r = elementOf(this);
    var list = [];
    for (var i = 0; i < arguments.length; i++) {
      var n = unwrap(arguments[i]);
      if (n && (n.t === 1 || n.t === 3) && list.indexOf(n) < 0) list.push(n);
    }
    r.manual = list;
  }
});

// ---------------------------------------------------------------- custom elements

var RESERVED_CE_NAMES = { 'annotation-xml': 1, 'color-profile': 1, 'font-face': 1, 'font-face-src': 1,
  'font-face-uri': 1, 'font-face-format': 1, 'font-face-name': 1, 'missing-glyph': 1 };
var CE_NAME = /^[a-z][^\t\n\f\r \/>A-Z]*$/;
function validCustomElementName(name) {
  return name.indexOf('-') > 0 && CE_NAME.test(name) && !RESERVED_CE_NAMES[name];
}

var ceRegistry = objCreate(null);
var ceByCtor = new Map();
var ceWaiting = objCreate(null);
var ceCreateDoc = null;
var ceUpgradeStack = [];

function ceDefinitionFor(name) {
  var def = ceRegistry[name];
  return def && !def.ext ? def : null;
}

function ceCall(r, def, name, args) {
  var cb = def.cb[name];
  if (typeof cb !== 'function') return;
  try {
    cb.apply(wrap(r), args || []);
  } catch (e) {
    reportException(e);
  }
}

function ceUpgrade(r, def) {
  if (r.ce === 'custom' || r.ce === 'failed' || r.ceRunning) return;
  flagOn(r, 'ceRunning');
  r.ceDef = def;
  r.ceConstructed = false;
  ceUpgradeStack.push(r);
  try {
    var o = new def.ctor();
    if (o !== r.o) throw new TypeError('Failed to upgrade a custom element: the constructor did not return the element.');
    r.ce = 'custom';
  } catch (e) {
    r.ce = 'failed';
    var i = ceUpgradeStack.lastIndexOf(r);
    if (i >= 0) ceUpgradeStack.splice(i, 1);
    flagOff(r, 'ceRunning');
    reportException(e);
    return;
  }
  flagOff(r, 'ceRunning');
  if (def.cb.attributeChangedCallback && r.a) {
    var a = r.a.slice();
    for (var k = 0; k < a.length; k += 2) {
      if (def.observed[a[k]]) ceCall(r, def, 'attributeChangedCallback', [a[k], null, a[k + 1], null]);
    }
  }
  if (r.c) ceCall(r, def, 'connectedCallback');
}

// HTMLElement's constructor body (see HTMLElementCtor in 25_elements.js).
function ceConstruct(newTarget) {
  if (!newTarget || newTarget === HTMLElement) throw new TypeError('Illegal constructor');
  var def = null;
  for (var C = newTarget; C && C !== HTMLElement && C !== Function.prototype; C = getProto(C)) {
    def = ceByCtor.get(C);
    if (def) break;
  }
  if (!def) throw new TypeError("Failed to construct 'HTMLElement': Illegal constructor");
  var top = ceUpgradeStack.length ? ceUpgradeStack[ceUpgradeStack.length - 1] : null;
  if (top && top.ceDef === def && top.ceRunning && !top.ceConstructed) {
    ceUpgradeStack.pop();
    top.ceConstructed = true;
    var o = top.o || bindWrapper(objCreate(newTarget.prototype), top);
    setProto(o, newTarget.prototype);
    return o;
  }
  var d = ceCreateDoc || mainDoc || standaloneDoc();
  var nr = createElementRec(d, def.name, 0);
  nr.ce = 'custom';
  nr.ceDef = def;
  return bindWrapper(objCreate(newTarget.prototype), nr);
}

// document.createElement for a defined name: synchronous construction.
function ceCreateSync(d, def) {
  var prev = ceCreateDoc;
  ceCreateDoc = d;
  var o = null, err = null;
  try {
    o = new def.ctor();
  } catch (e) {
    err = e;
  } finally {
    ceCreateDoc = prev;
  }
  var r = o ? unwrap(o) : null;
  if (!err && (!r || r.t !== 1 || r.ln !== def.name || r.p || r.f || (r.a && r.a.length) || r.d !== d)) {
    err = domErr('NotSupportedError', 'The custom element constructor for ' + def.name + ' did not produce a valid element.');
  }
  if (err) {
    reportException(err);
    var failed = createElementRec(d, def.name, 0);
    failed.ce = 'failed';
    return bindWrapper(objCreate(HTMLUnknownElement.prototype), failed);
  }
  return o;
}

function ceConnected(r) {
  if (r.ce === 'custom') {
    if (r.ceDef) ceCall(r, r.ceDef, 'connectedCallback');
    return;
  }
  if (r.ce === 'undefined' && r.ns === 0 && r.d === mainDoc) {
    var def = ceDefinitionFor(r.ln);
    if (def) ceUpgrade(r, def);
  }
}
function ceDisconnected(r) {
  if (r.ceDef) ceCall(r, r.ceDef, 'disconnectedCallback');
}
function ceAdopted(r, oldDoc, newDoc) {
  if (r.ceDef) ceCall(r, r.ceDef, 'adoptedCallback', [wrap(oldDoc), wrap(newDoc)]);
}
function ceAttributeChanged(r, name, old, value) {
  var def = r.ceDef;
  if (def && def.cb.attributeChangedCallback && def.observed[name]) {
    ceCall(r, def, 'attributeChangedCallback', [name, old, value, null]);
  }
}

function ceUpgradeTree(root, name) {
  var stack = [root];
  var found = [];
  while (stack.length) {
    var top = stack.pop();
    for (var e = top.t === 1 ? top : nextElement(top, top); e; e = nextElement(e, top)) {
      if (e.ce === 'undefined' && e.ns === 0 && (name === null || e.ln === name)) found.push(e);
      if (e.sr) stack.push(e.sr);
    }
  }
  for (var i = 0; i < found.length; i++) {
    var def = ceDefinitionFor(found[i].ln);
    if (def) ceUpgrade(found[i], def);
  }
}

var CustomElementRegistry = iface('CustomElementRegistry', null);
mix(CustomElementRegistry.prototype, {
  define: function (name, ctor, options) {
    requiredArgs(arguments, 2, 'define');
    name = str(name);
    if (typeof ctor !== 'function') throw new TypeError("Failed to execute 'define' on 'CustomElementRegistry': The provided value is not of type 'CustomElementConstructor'.");
    if (!validCustomElementName(name)) throw domErr('SyntaxError', "Failed to execute 'define' on 'CustomElementRegistry': \"" + name + '" is not a valid custom element name');
    if (ceRegistry[name]) throw domErr('NotSupportedError', "Failed to execute 'define' on 'CustomElementRegistry': the name \"" + name + '" has already been used with this registry');
    if (ceByCtor.has(ctor)) throw domErr('NotSupportedError', "Failed to execute 'define' on 'CustomElementRegistry': this constructor has already been used with this registry");
    var proto = ctor.prototype;
    if (proto === null || (typeof proto !== 'object' && typeof proto !== 'function')) throw new TypeError('The prototype of the custom element constructor is not an object.');
    var cb = {
      connectedCallback: proto.connectedCallback,
      disconnectedCallback: proto.disconnectedCallback,
      adoptedCallback: proto.adoptedCallback,
      attributeChangedCallback: proto.attributeChangedCallback
    };
    var observed = objCreate(null);
    if (typeof cb.attributeChangedCallback === 'function') {
      var list = ctor.observedAttributes;
      if (list !== undefined && list !== null) {
        var arr = Array.from(list);
        for (var i = 0; i < arr.length; i++) observed[str(arr[i])] = 1;
      }
    }
    var def = { name: name, ctor: ctor, proto: proto, cb: cb, observed: observed, ext: options && options.extends ? str(options.extends) : null };
    ceRegistry[name] = def;
    ceByCtor.set(ctor, def);
    if (!def.ext && mainDoc) ceUpgradeTree(mainDoc, name);
    var w = ceWaiting[name];
    if (w) {
      delete ceWaiting[name];
      w.resolve(ctor);
    }
  },
  get: function (name) {
    var def = ceRegistry[str(name)];
    return def ? def.ctor : undefined;
  },
  getName: function (ctor) {
    var def = ceByCtor.get(ctor);
    return def ? def.name : null;
  },
  whenDefined: function (name) {
    name = str(name);
    if (!validCustomElementName(name)) return Promise.reject(domErr('SyntaxError', '"' + name + '" is not a valid custom element name'));
    var def = ceRegistry[name];
    if (def) return Promise.resolve(def.ctor);
    var w = ceWaiting[name];
    if (!w) {
      w = ceWaiting[name] = {};
      w.promise = new Promise(function (resolve) { w.resolve = resolve; });
    }
    return w.promise;
  },
  upgrade: function (root) {
    var r = nodeArg(root, 'upgrade');
    ceUpgradeTree(r, null);
  },
  initialize: function () {}
});
var customElementsObj = objCreate(CustomElementRegistry.prototype);
defineProperty(global, 'customElements', { get: function () { return customElementsObj; }, enumerable: true, configurable: true });

// ---------------------------------------------------------------- declarative shadow DOM

function moveSubtreeToDoc(node, d) {
  for (var r = node; r; r = nextNode(r, node)) {
    r.d = d;
    if (r.t === 1 && r.tc) adoptTemplateContents(r.tc, templateDoc(d));
  }
}

// attachDeclarativeShadowRoots turns <template shadowrootmode> into its parent's shadow root, throughout
// root (including the shadow roots it creates). No hooks run: this is parse time.
function attachDeclarativeShadowRoots(root) {
  var stack = [root];
  while (stack.length) {
    var top = stack.pop();
    var r = nextElement(top, top);
    while (r) {
      var next = nextElement(r, top);
      if (r.ns === 0 && r.ln === 'template' && r.tc && r.p && r.p.t === 1) {
        var mode = getAttrRaw(r, 'shadowrootmode');
        mode = mode === null ? '' : asciiLower(mode);
        var host = r.p;
        if ((mode === 'open' || mode === 'closed') && !host.sr && canHostShadow(host)) {
          next = nextSkipping(r, top);
          var sr = attachShadowRec(host, {
            mode: mode,
            delegatesFocus: getAttrRaw(r, 'shadowrootdelegatesfocus') !== null,
            clonable: getAttrRaw(r, 'shadowrootclonable') !== null,
            serializable: getAttrRaw(r, 'shadowrootserializable') !== null
          }, true);
          var kids = childrenArray(r.tc);
          for (var i = 0; i < kids.length; i++) {
            unlink(kids[i]);
            moveSubtreeToDoc(kids[i], host.d);
            linkBefore(sr, kids[i], null);
          }
          unlink(r);
          if (host.c) {
            var ignored = [];
            for (var j = 0; j < kids.length; j++) markConnected(kids[j], ignored, true);
          }
          stack.push(sr);
        }
      }
      r = next;
    }
  }
}

if (testHooks) {
  testHooks.attachDeclarativeShadowRoots = attachDeclarativeShadowRoots;
  testHooks.slotAssigned = slotAssigned;
}
