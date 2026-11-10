// 30_selectors.js — CSS selectors for querySelector(All), matches, closest and :has.
//
// A selector is parsed once into compounds and combinators and compiled into small matcher closures;
// compiled selectors are cached by their text. Matching runs right to left over records. Unknown
// pseudo-classes throw a SyntaxError DOMException on purpose: jQuery/Sizzle try querySelectorAll first and
// fall back to their own engine (for :visible, :eq(), :contains() …) only when the browser throws.

var selCache = objCreate(null);
var selCacheSize = 0;

function selSyntax(text) {
  return domErr('SyntaxError', "Failed to execute 'querySelectorAll' on 'Document': '" + text + "' is not a valid selector.");
}

function isIdentStart(c) {
  return (c >= 97 && c <= 122) || (c >= 65 && c <= 90) || c === 95 || c >= 128 || c === 92;
}
function isIdentChar(c) {
  return isIdentStart(c) || (c >= 48 && c <= 57) || c === 45;
}
function isSelWS(c) { return c === 32 || c === 9 || c === 10 || c === 12 || c === 13; }

// Parser state: { s, i, text } — text is the original for error messages.
function selSkipWS(st) {
  var s = st.s;
  for (;;) {
    var c = s.charCodeAt(st.i);
    if (isSelWS(c)) { st.i++; continue; }
    if (c === 47 && s.charCodeAt(st.i + 1) === 42) {
      var end = s.indexOf('*/', st.i + 2);
      st.i = end < 0 ? s.length : end + 2;
      continue;
    }
    return;
  }
}
function selEscape(st) {
  // st.i is just past the backslash.
  var s = st.s;
  var hex = '';
  while (hex.length < 6 && /[0-9a-fA-F]/.test(s.charAt(st.i))) hex += s.charAt(st.i++);
  if (hex) {
    if (isSelWS(s.charCodeAt(st.i))) st.i++;
    var cp = parseInt(hex, 16);
    if (cp === 0 || cp > 0x10FFFF || (cp >= 0xD800 && cp <= 0xDFFF)) cp = 0xFFFD;
    return String.fromCodePoint(cp);
  }
  if (st.i >= s.length) return '\uFFFD';
  var ch = s.charAt(st.i++);
  if (ch === '\n') throw selSyntax(st.text);
  return ch;
}
function selIdent(st) {
  var s = st.s;
  var out = '';
  var c = s.charCodeAt(st.i);
  if (c === 45) {
    out += '-';
    st.i++;
    c = s.charCodeAt(st.i);
    if (c === 45) {
      out += '-';
      st.i++;
    } else if (!isIdentStart(c)) {
      throw selSyntax(st.text);
    }
  } else if (!isIdentStart(c)) {
    return null;
  }
  while (st.i < s.length) {
    c = s.charCodeAt(st.i);
    if (c === 92) {
      st.i++;
      out += selEscape(st);
    } else if (isIdentChar(c)) {
      out += s.charAt(st.i++);
    } else {
      break;
    }
  }
  return out;
}
function selString(st) {
  var s = st.s;
  var q = s.charAt(st.i++);
  var out = '';
  while (st.i < s.length) {
    var ch = s.charAt(st.i);
    if (ch === q) {
      st.i++;
      return out;
    }
    if (ch === '\\') {
      st.i++;
      if (s.charAt(st.i) === '\n') { st.i++; continue; }
      out += selEscape(st);
      continue;
    }
    if (ch === '\n') throw selSyntax(st.text);
    out += ch;
    st.i++;
  }
  return out;
}
// The raw text of a parenthesised argument (balanced), with st.i just past '('.
function selArgText(st) {
  var s = st.s;
  var depth = 1;
  var start = st.i;
  while (st.i < s.length) {
    var ch = s.charAt(st.i);
    if (ch === '"' || ch === "'") {
      selString(st);
      continue;
    }
    if (ch === '\\') {
      st.i += 2;
      continue;
    }
    if (ch === '(') depth++;
    else if (ch === ')') {
      depth--;
      if (depth === 0) {
        var arg = s.slice(start, st.i);
        st.i++;
        return arg;
      }
    }
    st.i++;
  }
  throw selSyntax(st.text);
}

function parseSelectorList(text, relative, forgiving) {
  var st = { s: text, i: 0, text: text };
  var list = [];
  for (;;) {
    selSkipWS(st);
    var start = st.i;
    var cx = null;
    try {
      cx = parseComplex(st, relative);
    } catch (e) {
      if (!forgiving) throw e;
      // Forgiving lists (:is/:where) drop the bad selector and continue after the next comma.
      st.i = start;
      var depth = 0;
      while (st.i < st.s.length) {
        var ch = st.s.charAt(st.i);
        if (ch === '(') depth++;
        else if (ch === ')') depth--;
        else if (ch === ',' && depth <= 0) break;
        st.i++;
      }
    }
    if (cx) list.push(cx);
    selSkipWS(st);
    if (st.i >= st.s.length) break;
    if (st.s.charAt(st.i) !== ',') {
      if (forgiving) {
        while (st.i < st.s.length && st.s.charAt(st.i) !== ',') st.i++;
        if (st.i >= st.s.length) break;
      } else {
        throw selSyntax(text);
      }
    }
    st.i++;
  }
  if (!list.length && !forgiving) throw selSyntax(text);
  return list;
}

function parseComplex(st, relative) {
  var compounds = [], combs = [];
  var lead = null;
  selSkipWS(st);
  var ch = st.s.charAt(st.i);
  if (relative) {
    if (ch === '>' || ch === '+' || ch === '~') {
      lead = ch;
      st.i++;
      selSkipWS(st);
    } else {
      lead = ' ';
    }
  }
  for (;;) {
    var c = parseCompound(st);
    if (!c) throw selSyntax(st.text);
    compounds.push(c);
    var before = st.i;
    selSkipWS(st);
    var sawWS = st.i > before;
    ch = st.s.charAt(st.i);
    if (ch === '>' || ch === '+' || ch === '~') {
      st.i++;
      selSkipWS(st);
      combs.push(ch);
      continue;
    }
    if (st.i >= st.s.length || ch === ',' || ch === ')') break;
    if (sawWS) {
      combs.push(' ');
      continue;
    }
    throw selSyntax(st.text);
  }
  return { compounds: compounds, combs: combs, lead: lead };
}

function parseCompound(st) {
  var s = st.s;
  var c = { tag: null, anyNS: false, noNS: false, ids: [], classes: [], attrs: [], pseudos: [], pseudoEl: false };
  var any = false;
  var ch = s.charAt(st.i);
  // Type selector with optional namespace prefix.
  if (ch === '*' || ch === '|' || isIdentStart(s.charCodeAt(st.i)) || (ch === '-' && st.i + 1 < s.length)) {
    var name = null;
    if (ch === '*') {
      st.i++;
      name = '*';
    } else if (ch !== '|') {
      name = selIdent(st);
    }
    if (s.charAt(st.i) === '|' && s.charAt(st.i + 1) !== '=') {
      st.i++;
      if (name === '*') c.anyNS = true;
      else if (name === null) c.noNS = true;
      else throw domErr('SyntaxError', "'" + st.text + "' uses an undeclared namespace prefix.");
      if (s.charAt(st.i) === '*') {
        st.i++;
        name = '*';
      } else {
        name = selIdent(st);
        if (name === null) throw selSyntax(st.text);
      }
    }
    if (name !== null) {
      c.tag = name;
      any = true;
    }
  }
  for (;;) {
    ch = s.charAt(st.i);
    if (ch === '#') {
      st.i++;
      var id = selIdent(st);
      if (id === null) throw selSyntax(st.text);
      c.ids.push(id);
    } else if (ch === '.') {
      st.i++;
      var cls = selIdent(st);
      if (cls === null) throw selSyntax(st.text);
      c.classes.push(cls);
    } else if (ch === '[') {
      st.i++;
      c.attrs.push(parseAttrSel(st));
    } else if (ch === ':') {
      st.i++;
      var el = false;
      if (s.charAt(st.i) === ':') {
        st.i++;
        el = true;
      }
      var pname = selIdent(st);
      if (pname === null) throw selSyntax(st.text);
      pname = asciiLower(pname);
      var arg = null;
      if (s.charAt(st.i) === '(') {
        st.i++;
        arg = selArgText(st);
      }
      if (el || pname === 'before' || pname === 'after' || pname === 'first-line' || pname === 'first-letter') {
        if (!el || PSEUDO_ELEMENTS[pname] || /^-(webkit|moz|ms)-/.test(pname)) c.pseudoEl = true;
        else throw selSyntax(st.text);
      } else {
        c.pseudos.push({ name: pname, arg: arg });
      }
    } else {
      break;
    }
    any = true;
  }
  return any ? c : null;
}
var PSEUDO_ELEMENTS = { before: 1, after: 1, 'first-line': 1, 'first-letter': 1, marker: 1, placeholder: 1, selection: 1,
  backdrop: 1, part: 1, slotted: 1, cue: 1, 'cue-region': 1, 'file-selector-button': 1, 'grammar-error': 1,
  'spelling-error': 1, 'target-text': 1, 'highlight': 1, 'view-transition': 1, 'details-content': 1, 'picker': 1,
  'scroll-marker': 1, 'scroll-button': 1, column: 1, 'checkmark': 1, 'picker-icon': 1 };

function parseAttrSel(st) {
  var s = st.s;
  selSkipWS(st);
  var name;
  if (s.charAt(st.i) === '*' && s.charAt(st.i + 1) === '|') {
    st.i += 2;
    name = selIdent(st);
  } else if (s.charAt(st.i) === '|') {
    st.i++;
    name = selIdent(st);
  } else {
    name = selIdent(st);
    if (name !== null && s.charAt(st.i) === '|' && s.charAt(st.i + 1) !== '=') {
      var px = name;
      st.i++;
      var local = selIdent(st);
      if (local === null) throw selSyntax(st.text);
      name = px + ':' + local;
    }
  }
  if (name === null) throw selSyntax(st.text);
  selSkipWS(st);
  var op = null, value = null, flag = null;
  var ch = s.charAt(st.i);
  // CSS closes open blocks at the end of input, so "a[href" is "a[href]" (as in browsers).
  if (ch === ']' || st.i >= s.length) {
    st.i++;
    return { name: name, lower: asciiLower(name), op: null, value: null, flag: null };
  }
  if (ch === '=') {
    op = '=';
    st.i++;
  } else if ((ch === '~' || ch === '|' || ch === '^' || ch === '$' || ch === '*') && s.charAt(st.i + 1) === '=') {
    op = ch + '=';
    st.i += 2;
  } else {
    throw selSyntax(st.text);
  }
  selSkipWS(st);
  ch = s.charAt(st.i);
  if (ch === '"' || ch === "'") value = selString(st);
  else {
    value = selIdent(st);
    if (value === null) {
      // Unquoted values may also be numbers in practice ([data-index=1]); accept a token up to ] or space.
      var m = /^[^\s\]]+/.exec(s.slice(st.i));
      if (!m || /["'\\]/.test(m[0])) throw selSyntax(st.text);
      if (/^[0-9.+-]/.test(m[0]) && !/^-?[0-9]/.test(m[0])) throw selSyntax(st.text);
      value = m[0];
      st.i += m[0].length;
    }
  }
  selSkipWS(st);
  ch = s.charAt(st.i);
  if (ch === 'i' || ch === 'I' || ch === 's' || ch === 'S') {
    flag = asciiLower(ch);
    st.i++;
    selSkipWS(st);
  }
  if (s.charAt(st.i) !== ']' && st.i < s.length) throw selSyntax(st.text);
  st.i++;
  return { name: name, lower: asciiLower(name), op: op, value: value, flag: flag };
}

// an+b parsing for :nth-*; returns {a, b, of} or throws.
function parseNth(arg, text) {
  var ofList = null;
  var m = /^([\s\S]*?)\s+of\s+([\s\S]+)$/i.exec(arg);
  var expr = arg;
  if (m) {
    expr = m[1];
    ofList = parseSelectorList(m[2], false, false);
  }
  expr = asciiLower(stripWS(expr)).replace(/\s+/g, '');
  if (expr === 'odd') return { a: 2, b: 1, of: ofList };
  if (expr === 'even') return { a: 2, b: 0, of: ofList };
  var nm = /^([+-]?\d*)?n([+-]\d+)?$/.exec(expr);
  if (nm) {
    var as = nm[1];
    var a = as === undefined || as === '' || as === '+' ? 1 : as === '-' ? -1 : parseInt(as, 10);
    var b = nm[2] ? parseInt(nm[2], 10) : 0;
    return { a: a, b: b, of: ofList };
  }
  if (/^[+-]?\d+$/.test(expr)) return { a: 0, b: parseInt(expr, 10), of: ofList };
  throw selSyntax(text);
}
function nthMatches(a, b, pos) {
  if (a === 0) return pos === b;
  var diff = pos - b;
  return diff % a === 0 && diff / a >= 0;
}

// ---------------------------------------------------------------- matching helpers

function parentElementRec(r) { var p = r.p; return p && p.t === 1 ? p : null; }
function prevElementRec(r) {
  for (var k = r.v; k; k = k.v) if (k.t === 1) return k;
  return null;
}
function nextElementRec(r) {
  for (var k = r.n; k; k = k.n) if (k.t === 1) return k;
  return null;
}
function hasClass(r, name) {
  var cls = getAttrRaw(r, 'class');
  if (!cls) return false;
  var i = cls.indexOf(name);
  while (i >= 0) {
    var end = i + name.length;
    if ((i === 0 || isSelWS(cls.charCodeAt(i - 1))) && (end === cls.length || isSelWS(cls.charCodeAt(end)))) return true;
    i = cls.indexOf(name, i + 1);
  }
  return false;
}
function attrForSel(r, sel) {
  if (r.ns === 0) return getAttrRaw(r, sel.lower);
  var v = getAttrRaw(r, sel.name);
  return v === null && sel.name !== sel.lower ? getAttrRaw(r, sel.lower) : v;
}
var SEL_HAS_WS = /\s/;
function attrMatches(r, sel) {
  var v = attrForSel(r, sel);
  if (v === null) return false;
  if (sel.op === null) return true;
  var want = sel.value;
  if (sel.flag === 'i') {
    v = asciiLower(v);
    want = asciiLower(want);
  }
  switch (sel.op) {
    case '=': return v === want;
    case '~=': return want !== '' && !SEL_HAS_WS.test(want) && splitTokens(v).indexOf(want) >= 0;
    case '|=': return v === want || v.slice(0, want.length + 1) === want + '-';
    case '^=': return want !== '' && v.slice(0, want.length) === want;
    case '$=': return want !== '' && v.length >= want.length && v.slice(v.length - want.length) === want;
    case '*=': return want !== '' && v.indexOf(want) >= 0;
  }
  return false;
}

// Element positions for :nth-*, :first/last/only-* are computed for all children of a parent at once and
// kept on the child records until the parent's child list changes (kv): one pass per parent instead of a
// walk over the earlier siblings for every candidate, which made `tr:nth-child(even)` on a long table
// quadratic.
function positionCache(p) {
  if (p.pcv === p.kv) return;
  var n = 0, counts = objCreate(null), k;
  for (k = p.f; k; k = k.n) {
    if (k.t !== 1) continue;
    n++;
    var key = k.ns === 0 ? k.ln : k.ns + ':' + k.ln;
    var c = (counts[key] | 0) + 1;
    counts[key] = c;
    k.pf = n;
    k.ptf = c;
    k.ptk = key;
  }
  for (k = p.f; k; k = k.n) {
    if (k.t !== 1) continue;
    k.pb = n - k.pf + 1;
    k.ptb = counts[k.ptk] - k.ptf + 1;
  }
  p.pcv = p.kv;
}

function elementPosition(r, fromEnd, ofType, filter, scope) {
  if (!filter && r.p) {
    positionCache(r.p);
    return fromEnd ? (ofType ? r.ptb : r.pb) : (ofType ? r.ptf : r.pf);
  }
  var pos = 1;
  for (var k = fromEnd ? r.n : r.v; k; k = fromEnd ? k.n : k.v) {
    if (k.t !== 1) continue;
    if (ofType && (k.ln !== r.ln || k.ns !== r.ns)) continue;
    if (filter && !filter(k, scope)) continue;
    pos++;
  }
  return pos;
}

function isReadWrite(r) {
  if (r.ns === 0) {
    if (r.ln === 'input') {
      var t = inputType(r);
      if (t === 'hidden' || t === 'range' || t === 'color' || t === 'checkbox' || t === 'radio' || t === 'file' ||
          t === 'submit' || t === 'image' || t === 'reset' || t === 'button') return false;
      return getAttrRaw(r, 'readonly') === null && !isDisabledControl(r);
    }
    if (r.ln === 'textarea') return getAttrRaw(r, 'readonly') === null && !isDisabledControl(r);
  }
  for (var a = r; a && a.t === 1; a = a.p) {
    var ce = getAttrRaw(a, 'contenteditable');
    if (ce !== null) return asciiLower(ce) !== 'false';
  }
  return false;
}
function langOf(r) {
  for (var a = r; a; a = a.p) {
    if (a.t !== 1) continue;
    var v = getAttrRaw(a, 'xml:lang');
    if (v === null) v = getAttrRaw(a, 'lang');
    if (v !== null) return v;
  }
  return '';
}
function dirOf(r) {
  for (var a = r; a; a = a.p) {
    if (a.t !== 1) continue;
    var v = getAttrRaw(a, 'dir');
    if (v !== null) {
      v = asciiLower(v);
      if (v === 'rtl' || v === 'ltr') return v;
    }
  }
  return 'ltr';
}
function targetId() {
  if (!mainDoc) return null;
  var url = mainDoc.url;
  var h = url.indexOf('#');
  if (h < 0 || h === url.length - 1) return null;
  try { return decodeURIComponent(url.slice(h + 1)); } catch (e) { return url.slice(h + 1); }
}
function isDisabledFormElement(r) {
  return r.ns === 0 && (r.ln === 'button' || r.ln === 'input' || r.ln === 'select' || r.ln === 'textarea' ||
    r.ln === 'optgroup' || r.ln === 'option' || r.ln === 'fieldset') && isDisabledControl(r);
}
var CAN_BE_DISABLED = { button: 1, input: 1, select: 1, textarea: 1, optgroup: 1, option: 1, fieldset: 1 };

function alwaysFalse() { return false; }

function compilePseudo(p, text) {
  var name = p.name, arg = p.arg;
  var inner, nth;
  switch (name) {
    case 'root': return function (r) { return !!r.p && r.p.t === 9; };
    case 'scope': return function (r, scope) { return scope && scope.t === 1 ? r === scope : (!!r.p && r.p.t === 9); };
    case 'first-child': return function (r) { return prevElementRec(r) === null; };
    case 'last-child': return function (r) { return nextElementRec(r) === null; };
    case 'only-child': return function (r) { return prevElementRec(r) === null && nextElementRec(r) === null; };
    case 'first-of-type': return function (r) { return elementPosition(r, false, true, null) === 1; };
    case 'last-of-type': return function (r) { return elementPosition(r, true, true, null) === 1; };
    case 'only-of-type': return function (r) { return elementPosition(r, false, true, null) === 1 && elementPosition(r, true, true, null) === 1; };
    case 'nth-child':
    case 'nth-last-child':
    case 'nth-of-type':
    case 'nth-last-of-type':
      if (arg === null) throw selSyntax(text);
      nth = parseNth(arg, text);
      var fromEnd = name === 'nth-last-child' || name === 'nth-last-of-type';
      var ofType = name === 'nth-of-type' || name === 'nth-last-of-type';
      var filter = nth.of ? compileList(nth.of, text) : null;
      return function (r, scope) {
        if (filter && !filter(r, scope)) return false;
        return nthMatches(nth.a, nth.b, elementPosition(r, fromEnd, ofType, filter, scope));
      };
    case 'not':
      if (arg === null) throw selSyntax(text);
      inner = compileList(parseSelectorList(arg, false, false), text);
      return function (r, scope) { return !inner(r, scope); };
    case 'is': case 'where': case 'matches': case '-webkit-any': case '-moz-any': case 'any':
      if (arg === null) throw selSyntax(text);
      return compileForgiving(parseSelectorList(arg, false, true), text);
    case 'has':
      if (arg === null) throw selSyntax(text);
      return compileHas(parseSelectorList(arg, true, false));
    case 'empty':
      return function (r) {
        for (var k = r.f; k; k = k.n) {
          if (k.t === 1) return false;
          if ((k.t === 3 || k.t === 4) && k.x !== '') return false;
        }
        return true;
      };
    case 'checked':
      return function (r) {
        if (r.ns !== 0) return false;
        if (r.ln === 'input') { var t = inputType(r); return (t === 'checkbox' || t === 'radio') && checkedOf(r); }
        if (r.ln === 'option') return optionSelected(r);
        return false;
      };
    case 'disabled': return function (r) { return isDisabledFormElement(r); };
    case 'enabled': return function (r) { return r.ns === 0 && !!CAN_BE_DISABLED[r.ln] && !isDisabledControl(r); };
    case 'required':
      return function (r) { return r.ns === 0 && (r.ln === 'input' || r.ln === 'select' || r.ln === 'textarea') && getAttrRaw(r, 'required') !== null; };
    case 'optional':
      return function (r) { return r.ns === 0 && (r.ln === 'input' || r.ln === 'select' || r.ln === 'textarea') && getAttrRaw(r, 'required') === null; };
    case 'read-write': return isReadWrite;
    case 'read-only': return function (r) { return !isReadWrite(r); };
    case 'link': case 'any-link': case '-webkit-any-link':
      return function (r) { return r.ns === 0 && (r.ln === 'a' || r.ln === 'area') && getAttrRaw(r, 'href') !== null; };
    case 'defined':
      return function (r) { return r.ce === undefined || r.ce === 'custom'; };
    case 'placeholder-shown':
      return function (r) {
        if (r.ns !== 0 || getAttrRaw(r, 'placeholder') === null) return false;
        if (r.ln === 'input') return inputValue(r) === '';
        if (r.ln === 'textarea') return textareaValue(r) === '';
        return false;
      };
    case 'indeterminate':
      return function (r) {
        if (r.ns !== 0) return false;
        if (r.ln === 'input' && inputType(r) === 'checkbox') return !!r.ind;
        if (r.ln === 'input' && inputType(r) === 'radio') return checkedRadioInGroup(r) === null;
        if (r.ln === 'progress') return getAttrRaw(r, 'value') === null;
        return false;
      };
    case 'focus': return function (r) { return r.d.active === r; };
    case 'focus-within':
      return function (r) { var a = r.d.active; return !!a && isInclusiveAncestor(r, a); };
    case 'target': return function (r) { var id = targetId(); return id !== null && r.d === mainDoc && getAttrRaw(r, 'id') === id; };
    case 'lang':
      if (arg === null) throw selSyntax(text);
      var langs = arg.split(',').map(function (x) { return asciiLower(stripWS(x).replace(/^["']|["']$/g, '')); });
      return function (r) {
        var l = asciiLower(langOf(r));
        for (var i = 0; i < langs.length; i++) {
          var want = langs[i].replace(/^\*-?/, '');
          if (want === '' || l === want || l.slice(0, want.length + 1) === want + '-') return true;
        }
        return false;
      };
    case 'dir':
      if (arg === null) throw selSyntax(text);
      var dirWant = asciiLower(stripWS(arg));
      return function (r) { return dirOf(r) === dirWant; };
    case 'open':
      return function (r) { return r.ns === 0 && (r.ln === 'details' || r.ln === 'dialog') && getAttrRaw(r, 'open') !== null; };
    case 'closed':
      return function (r) { return r.ns === 0 && (r.ln === 'details' || r.ln === 'dialog') && getAttrRaw(r, 'open') === null; };
    case 'valid': case 'in-range':
      return function (r) { return r.ns === 0 && (LISTED[r.ln] || r.ln === 'form'); };
    case 'popover-open':
      return function (r) { return getAttrRaw(r, 'data-jh-popover-open') !== null; };
    case 'paused':
      return function (r) { return r.ns === 0 && (r.ln === 'video' || r.ln === 'audio'); };
    case 'invalid': case 'out-of-range': case 'user-invalid': case 'user-valid': case 'hover': case 'active':
    case 'focus-visible': case 'visited': case 'target-within': case 'autofill': case '-webkit-autofill': case 'modal':
    case 'fullscreen': case '-webkit-full-screen': case 'picture-in-picture': case 'playing': case 'muted':
    case 'volume-locked': case 'seeking': case 'buffering': case 'stalled': case 'local-link': case 'current':
    case 'past': case 'future': case 'blank': case 'default': case 'host': case 'host-context': case 'state':
    case 'nth-col': case 'nth-last-col': case 'has-slotted': case 'xr-overlay': case 'active-view-transition':
      return alwaysFalse;
  }
  throw selSyntax(text);
}

function compileCompound(c, text) {
  if (c.pseudoEl) return alwaysFalse;
  var checks = [];
  if (c.tag !== null && c.tag !== '*') {
    var tag = c.tag, tagLower = asciiLower(c.tag);
    checks.push(function (r) { return r.ns === 0 ? r.ln === tagLower : (r.ln === tag || asciiLower(r.ln) === tagLower); });
  }
  if (c.noNS) checks.push(function (r) { return r.ns === 3 && !r.nsu; });
  c.ids.forEach(function (id) { checks.push(function (r) { return getAttrRaw(r, 'id') === id; }); });
  c.classes.forEach(function (cls) { checks.push(function (r) { return hasClass(r, cls); }); });
  c.attrs.forEach(function (a) { checks.push(function (r) { return attrMatches(r, a); }); });
  c.pseudos.forEach(function (p) { checks.push(compilePseudo(p, text)); });
  var n = checks.length;
  if (n === 0) return function () { return true; };
  if (n === 1) return checks[0];
  if (n === 2) {
    var c0 = checks[0], c1 = checks[1];
    return function (r, scope) { return c0(r, scope) && c1(r, scope); };
  }
  return function (r, scope) {
    for (var i = 0; i < n; i++) if (!checks[i](r, scope)) return false;
    return true;
  };
}

function compileComplex(cx, text, anchorTest) {
  var ms = cx.compounds.map(function (c) { return compileCompound(c, text); });
  var combs = cx.combs.slice();
  if (anchorTest) {
    ms.unshift(anchorTest);
    combs.unshift(cx.lead || ' ');
  }
  var n = ms.length;
  if (n === 1) return ms[0];
  function from(i, r, scope) {
    if (!ms[i](r, scope)) return false;
    if (i === 0) return true;
    var a;
    switch (combs[i - 1]) {
      case ' ':
        for (a = parentElementRec(r); a; a = parentElementRec(a)) if (from(i - 1, a, scope)) return true;
        return false;
      case '>':
        a = parentElementRec(r);
        return !!a && from(i - 1, a, scope);
      case '+':
        a = prevElementRec(r);
        return !!a && from(i - 1, a, scope);
      case '~':
        for (a = prevElementRec(r); a; a = prevElementRec(a)) if (from(i - 1, a, scope)) return true;
        return false;
    }
    return false;
  }
  return function (r, scope) { return from(n - 1, r, scope); };
}

function compileList(list, text) {
  var ms = list.map(function (cx) { return compileComplex(cx, text); });
  if (ms.length === 1) return ms[0];
  return function (r, scope) {
    for (var i = 0; i < ms.length; i++) if (ms[i](r, scope)) return true;
    return false;
  };
}

// Forgiving selector lists (:is, :where) drop the selectors that do not compile.
function compileForgiving(list, text) {
  var ms = [];
  for (var i = 0; i < list.length; i++) {
    try { ms.push(compileComplex(list[i], text)); } catch (e) { /* dropped */ }
  }
  if (!ms.length) return alwaysFalse;
  if (ms.length === 1) return ms[0];
  return function (r, scope) {
    for (var j = 0; j < ms.length; j++) if (ms[j](r, scope)) return true;
    return false;
  };
}

// :has(<relative selectors>): the anchor is the element being tested.
function compileHas(list) {
  var current = null;
  var anchorTest = function (r) { return r === current; };
  var parts = list.map(function (cx) {
    return { lead: cx.lead || ' ', m: compileComplex(cx, '', anchorTest) };
  });
  return function (r, scope) {
    var saved = current;
    current = r;
    try {
      for (var i = 0; i < parts.length; i++) {
        var part = parts[i];
        var x;
        if (part.lead === ' ' || part.lead === '>') {
          for (x = nextElement(r, r); x; x = nextElement(x, r)) if (part.m(x, scope)) return true;
        } else {
          for (var s = r.n; s; s = s.n) {
            if (s.t !== 1) continue;
            if (part.m(s, scope)) return true;
            for (x = nextElement(s, s); x; x = nextElement(x, s)) if (part.m(x, scope)) return true;
            if (part.lead === '+') break;
          }
        }
      }
      return false;
    } finally {
      current = saved;
    }
  };
}

function compileSelector(text) {
  var hit = selCache[text];
  if (hit) {
    if (hit.err) throw hit.err;
    return hit.m;
  }
  var entry;
  try {
    entry = { m: compileList(parseSelectorList(text, false, false), text) };
  } catch (e) {
    if (!(e instanceof DOMException)) e = selSyntax(text);
    entry = { err: e };
  }
  if (selCacheSize >= 512) {
    selCache = objCreate(null);
    selCacheSize = 0;
  }
  selCache[text] = entry;
  selCacheSize++;
  if (entry.err) throw entry.err;
  return entry.m;
}

function matchesSelector(r, text) { return compileSelector(text)(r, r); }

var SIMPLE_ID = /^#([A-Za-z_][\w-]*)$/;
var SIMPLE_CLASS = /^\.([A-Za-z_][\w-]*)$/;
var SIMPLE_TAG = /^[A-Za-z][A-Za-z0-9-]*$/;

function scopeOf(root) { return root.t === 1 ? root : null; }

// qsPlan classifies a selector string once (bounded cache): an #id, a .class, a bare tag, or anything else
// for the compiled matcher — so a page calling querySelector in a loop pays for no regex per call.
var qsPlans = objCreate(null);
var qsPlanCount = 0;
function qsPlan(text) {
  var p = qsPlans[text];
  if (p) return p;
  var t = stripWS(text), m;
  if ((m = SIMPLE_ID.exec(t))) p = { k: 1, v: m[1] };
  else if ((m = SIMPLE_CLASS.exec(t))) p = { k: 2, v: m[1] };
  else if (SIMPLE_TAG.test(t)) p = { k: 3, v: t, lower: asciiLower(t) };
  else p = { k: 0 };
  if (qsPlanCount >= 512) {
    qsPlans = objCreate(null);
    qsPlanCount = 0;
  }
  qsPlans[text] = p;
  qsPlanCount++;
  return p;
}

function querySelectorRec(root, text) {
  var plan = qsPlan(text);
  if (plan.k === 1) {
    var d = docOf(root);
    if (root.c && !(root.t === 11) && !inShadowTree(root.t === 9 ? firstElementChild(root) || root : root)) {
      var l = d.ids[plan.v];
      if (!l || !l.length) return null;
      var sorted = l.length === 1 ? l : l.slice().sort(function (a, b) { return precedes(a, b) ? -1 : 1; });
      for (var i = 0; i < sorted.length; i++) if (sorted[i] !== root && isInclusiveAncestor(root, sorted[i])) return sorted[i];
      return null;
    }
    for (var e0 = nextElement(root, root); e0; e0 = nextElement(e0, root)) if (getAttrRaw(e0, 'id') === plan.v) return e0;
    return null;
  }
  if (plan.k === 2) {
    for (var e1 = nextElement(root, root); e1; e1 = nextElement(e1, root)) if (hasClass(e1, plan.v)) return e1;
    return null;
  }
  if (plan.k === 3) {
    var lower = plan.lower, t = plan.v;
    for (var e2 = nextElement(root, root); e2; e2 = nextElement(e2, root)) {
      if (e2.ns === 0 ? e2.ln === lower : (e2.ln === t || asciiLower(e2.ln) === lower)) return e2;
    }
    return null;
  }
  var match = compileSelector(text);
  var scope = scopeOf(root);
  for (var e = nextElement(root, root); e; e = nextElement(e, root)) if (match(e, scope)) return e;
  return null;
}

function querySelectorAllRecs(root, text) {
  var out = [];
  var plan = qsPlan(text);
  if (plan.k === 2) {
    for (var e1 = nextElement(root, root); e1; e1 = nextElement(e1, root)) if (hasClass(e1, plan.v)) out.push(e1);
    return out;
  }
  if (plan.k === 3) {
    var lower = plan.lower, t = plan.v;
    for (var e2 = nextElement(root, root); e2; e2 = nextElement(e2, root)) {
      if (e2.ns === 0 ? e2.ln === lower : (e2.ln === t || asciiLower(e2.ln) === lower)) out.push(e2);
    }
    return out;
  }
  var match = compileSelector(text);
  var scope = scopeOf(root);
  for (var e = nextElement(root, root); e; e = nextElement(e, root)) if (match(e, scope)) out.push(e);
  return out;
}

if (testHooks) {
  testHooks.compileSelector = compileSelector;
  testHooks.querySelectorAllRecs = querySelectorAllRecs;
}
