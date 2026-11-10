// webdom_dom.test.mjs — tests for the browser's JavaScript DOM core (player/webdom/10–59).
//
// Most checks run the same script against the prelude (in a vm context, see webdom_harness.mjs) and
// against jsdom, and compare the results: jsdom is a spec-faithful DOM, so agreeing with it on markup,
// selectors, serialization and traversal is a strong signal. The rest are behaviours jsdom does not have
// (shadow flattening for the renderer, lazy-image promotion, plausible geometry).

import test from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { loadPage, domPartsOnly, flush } from './webdom_harness.mjs';

const URL_ = 'https://example.test/dir/page.html';

async function ours(html, opts = {}) {
  return loadPage(html, { url: URL_, filter: opts.full ? null : domPartsOnly, useHost: false, ...opts });
}
function theirs(html) {
  return new JSDOM(html, { url: URL_, runScripts: 'outside-only' });
}
// Runs code in both and returns [ours, jsdom] results (code must evaluate to a string/number/boolean).
async function both(html, code) {
  const page = await ours(html);
  const a = page.run(code);
  const dom = theirs(html);
  const b = dom.window.eval(code);
  return [a, b];
}
async function same(html, code, msg) {
  const [a, b] = await both(html, code);
  assert.equal(a, b, msg || code);
}
// Same comparison against the whole prelude (DOM + window environment parts).
async function sameFull(html, code) {
  const page = await ours(html, { full: true });
  const a = page.run(code);
  const b = theirs(html).window.eval(code);
  assert.equal(a, b, code);
}

const FIXTURE = `<!doctype html><html lang="en"><head><title>  Fixture   Title </title>
<meta charset="utf-8"><link rel="stylesheet" href="/s.css"></head>
<body class="home page">
<header id="top"><nav><ul id="menu"><li class="item first"><a href="/a" id="la">A</a></li>
<li class="item"><a href="b.html" id="lb" title="Bee">B</a></li><li class="item last"><a id="lc">C</a></li></ul></nav></header>
<main id="main" data-role="content">
<h1 id="h">Heading <em>one</em></h1>
<p id="p1" class="lead intro">First <b>bold</b> para.</p>
<p id="p2" lang="fr-CA">Second</p>
<div id="d1" class="box"><span id="s1" class="x">one</span><span id="s2">two</span><!-- c --><span id="s3" class="x y">three</span></div>
<div id="d2" hidden><input id="i1" type="checkbox" checked name="agree"><input id="i2" type="text" value="v" disabled>
<input id="i3" placeholder="type"><textarea id="t1">text</textarea>
<select id="sel"><option id="o1" value="1">One</option><option id="o2" selected>Two</option><optgroup label="g"><option id="o3" disabled>Three</option></optgroup></select></div>
<table id="tbl"><tr id="r1"><td id="c1">1</td><td id="c2">2</td></tr><tr id="r2"><td>3</td><td></td></tr></table>
<form id="f" action="/search"><input name="q" id="q" value="cats"><button id="go">Go</button></form>
<ol id="ol"><li>1</li><li>2</li><li>3</li><li>4</li><li>5</li><li>6</li><li>7</li></ol>
<section id="empty"></section><section id="ws"> </section>
<svg id="svg" viewBox="0 0 10 10"><circle id="circ" r="4"/><linearGradient id="lg"/></svg>
</main>
<footer id="foot">Footer &amp; &lt;stuff&gt; &copy;</footer>
</body></html>`;

// ---------------------------------------------------------------- basics

test('document structure and title match jsdom', async () => {
  await same(FIXTURE, `[document.title, document.documentElement.lang, document.head.children.length,
    document.body.className, document.doctype.name, document.compatMode, document.characterSet,
    document.getElementById('main').children.length, document.body.firstElementChild.id].join('|')`);
});

test('textContent, innerText-free accessors and nodeName agree', async () => {
  await same(FIXTURE, `var p = document.getElementById('p1');
    [p.textContent, p.firstChild.nodeValue, p.childNodes.length, p.firstChild.nodeType, p.nodeName,
     p.lastChild.textContent, document.getElementById('foot').textContent,
     document.getElementById('svg').firstChild.nodeName, document.getElementById('lg').tagName].join('|')`);
});

test('tree mutation methods agree with jsdom', async () => {
  await same(FIXTURE, `var d = document.getElementById('d1'), out = [];
    var n = document.createElement('i'); n.textContent = 'new';
    d.insertBefore(n, document.getElementById('s2'));
    d.appendChild(document.getElementById('s1'));
    d.replaceChild(document.createTextNode('T'), document.getElementById('s3'));
    var f = document.createDocumentFragment(); f.append('a', document.createElement('br'), 'b');
    d.prepend(f);
    out.push(f.childNodes.length);
    document.getElementById('s2').before('<', document.createElement('hr'));
    document.getElementById('s2').after('>');
    document.getElementById('s2').replaceWith('S2');
    d.lastChild.remove();
    out.push(d.innerHTML);
    d.replaceChildren('only');
    out.push(d.outerHTML, d.childNodes.length);
    out.join('|')`);
});

test('hierarchy errors are DOMExceptions with the right names', async () => {
  await same(FIXTURE, `var out = [];
    function name(f) { try { f(); return 'ok'; } catch (e) { return e.name + ':' + (e instanceof DOMException); } }
    var b = document.body, d1 = document.getElementById('d1');
    out.push(name(function () { d1.appendChild(b); }));
    out.push(name(function () { d1.insertBefore(document.createElement('a'), b); }));
    out.push(name(function () { document.appendChild(document.createElement('div')); }));
    out.push(name(function () { document.createTextNode('x').appendChild(d1); }));
    out.push(name(function () { d1.removeChild(b); }));
    out.push(name(function () { document.createElement('1bad'); }));
    out.push(name(function () { d1.setAttribute('a b', 1); }));
    out.join('|')`);
});

test('cloneNode, importNode, isEqualNode, compareDocumentPosition, contains', async () => {
  await same(FIXTURE, `var d1 = document.getElementById('d1'), out = [];
    var c = d1.cloneNode(true), s = d1.cloneNode(false);
    out.push(c.outerHTML, s.outerHTML, c.isEqualNode(d1), s.isEqualNode(d1), c.parentNode === null);
    var a = document.getElementById('s1'), b = document.getElementById('s3');
    out.push(a.compareDocumentPosition(b), b.compareDocumentPosition(a), d1.compareDocumentPosition(a),
      a.compareDocumentPosition(d1), a.compareDocumentPosition(document.createElement('x')) & 1);
    out.push(d1.contains(a), a.contains(d1), d1.contains(d1), document.contains(a));
    var doc2 = document.implementation.createHTMLDocument('t');
    var imp = doc2.importNode(d1, true);
    out.push(imp.ownerDocument === doc2, imp.innerHTML === d1.innerHTML, doc2.title);
    doc2.body.appendChild(a);
    out.push(a.ownerDocument === doc2, document.getElementById('s1'));
    out.join('|')`);
});

test('normalize and splitText', async () => {
  await same('<p id=p>a</p>', `var p = document.getElementById('p');
    p.appendChild(document.createTextNode('b')); p.appendChild(document.createTextNode(''));
    p.appendChild(document.createElement('i')).appendChild(document.createTextNode('c'));
    p.appendChild(document.createTextNode('d'));
    var before = p.childNodes.length;
    p.normalize();
    var t = p.firstChild.splitText(1);
    [before, p.childNodes.length, p.firstChild.data, t.data, t.wholeText, p.innerHTML].join('|')`);
});

// ---------------------------------------------------------------- live collections

test('childNodes / children / getElementsBy* are live', async () => {
  await same(FIXTURE, `var ul = document.getElementById('menu'), out = [];
    var kids = ul.childNodes, els = ul.children, items = document.getElementsByClassName('item'), lis = ul.getElementsByTagName('li');
    out.push(kids.length, els.length, items.length, lis.length);
    var li = document.createElement('li'); li.className = 'item';
    ul.appendChild(li);
    out.push(kids.length, els.length, items.length, lis.length, ul.childNodes === kids);
    while (kids.length) ul.removeChild(kids[0]);
    out.push(kids.length, els.length, items.length, lis.length, els[0] === undefined);
    out.push(Array.from(document.body.children).map(function (e) { return e.id || e.tagName; }).join(','));
    out.push(document.forms.length, document.forms.f.id, document.links.length, document.images.length, document.scripts.length);
    out.join('|')`);
});

test('NodeList iteration and HTMLCollection named access', async () => {
  await same(FIXTURE, `var out = [];
    var spans = document.querySelectorAll('#d1 span');
    var ids = []; spans.forEach(function (s, i) { ids.push(i + s.id); });
    out.push(ids.join(','), [].slice.call(spans).length, Array.from(spans).map(function (s) { return s.id; }).join(','));
    var col = document.getElementById('d1').children;
    out.push(col.namedItem('s2').id, col.s3 && col.s3.id, 's1' in col, 0 in col, 9 in col);
    out.push(Object.keys(spans).join(','));
    out.join('|')`);
});

// ---------------------------------------------------------------- attributes and reflection

test('attributes, NamedNodeMap and Attr nodes', async () => {
  await same(FIXTURE, `var a = document.getElementById('lb'), out = [];
    out.push(a.getAttribute('TITLE'), a.hasAttribute('Href'), a.getAttributeNames().join(','));
    a.setAttribute('Data-X', '1'); a.toggleAttribute('hidden'); a.toggleAttribute('hidden', true);
    out.push(a.outerHTML);
    out.push(Array.from(a.attributes).map(function (x) { return x.name + '=' + x.value; }).join(';'));
    out.push(a.attributes.length, a.attributes[0].name, a.attributes.title.value, a.attributes.getNamedItem('id').ownerElement === a);
    var at = a.getAttributeNode('title'); at.value = 'Bea';
    out.push(a.title, a.attributes.title === at);
    a.removeAttribute('title');
    out.push(at.ownerElement, at.value, a.hasAttribute('title'));
    var n = document.createAttribute('ALT'); n.value = 'z'; a.setAttributeNode(n);
    out.push(a.getAttribute('alt'));
    a.setAttributeNS('http://www.w3.org/1999/xlink', 'xlink:href', '#q');
    out.push(a.getAttributeNS('http://www.w3.org/1999/xlink', 'href'));
    out.join('|')`);
});

test('reflected IDL attributes: links, images, ids, classes', async () => {
  await same(FIXTURE, `var out = [];
    var la = document.getElementById('la'), lb = document.getElementById('lb'), lc = document.getElementById('lc');
    out.push(la.href, lb.href, lc.href, lb.pathname, lb.host, lb.origin, lb.protocol, lb.search, lb.hash);
    lb.search = '?q=1'; lb.hash = 'h';
    out.push(lb.href, lb.getAttribute('href'));
    var img = document.createElement('img'); img.src = '../i.png'; img.alt = 'A';
    out.push(img.src, img.getAttribute('src'), img.outerHTML);
    var d = document.getElementById('d1');
    d.id = 'dd'; d.className = 'q r';
    out.push(d.id, d.classList.length, d.classList[1], d.tabIndex, la.tabIndex, d.title, d.lang, d.hidden, document.getElementById('d2').hidden);
    out.join('|')`);
});

test('classList operations', async () => {
  await same('<div id=x class="  a b  a "></div>', `var c = document.getElementById('x').classList, out = [];
    out.push(c.length, c.value, c.contains('a'), c.item(1));
    c.add('c', 'a'); c.remove('b'); out.push(c.value);
    out.push(c.toggle('a'), c.toggle('z'), c.toggle('z', true), c.toggle('q', false), c.value);
    out.push(c.replace('c', 'd'), c.replace('nope', 'e'), c.value, String(c), c.length, c[0], c[1]);
    function name(f) { try { f(); return 'ok'; } catch (e) { return e.name; } }
    out.push(name(function () { c.add(''); }), name(function () { c.add('a b'); }));
    var seen = []; c.forEach(function (t) { seen.push(t); }); out.push(seen.join(','), Array.from(c).join(','));
    out.join('|')`);
});

test('dataset maps camelCase to data-*', async () => {
  await same('<div id=x data-foo-bar="1" data-x="2"></div>', `var e = document.getElementById('x'), d = e.dataset, out = [];
    out.push(d.fooBar, d.x, d.missing, 'fooBar' in d, Object.keys(d).join(','));
    d.newThing = 'n'; delete d.x;
    out.push(e.outerHTML, JSON.stringify(Object.assign({}, d)));
    out.join('|')`);
});

test('form controls: value, checked, selected, elements', async () => {
  await same(FIXTURE, `var out = [];
    var cb = document.getElementById('i1'), t = document.getElementById('i2'), ta = document.getElementById('t1');
    var sel = document.getElementById('sel');
    out.push(cb.checked, cb.value, cb.defaultChecked, t.value, t.disabled, t.type, document.getElementById('i3').type);
    cb.checked = false; t.value = 'w'; ta.value = 'new';
    out.push(cb.checked, cb.hasAttribute('checked'), t.value, t.getAttribute('value'), ta.value, ta.defaultValue, ta.textContent);
    out.push(sel.value, sel.selectedIndex, sel.options.length, sel.length, sel.selectedOptions.length, sel.type);
    sel.value = '1';
    out.push(sel.selectedIndex, document.getElementById('o1').selected, document.getElementById('o2').selected, sel.value);
    sel.selectedIndex = 2;
    out.push(sel.value, document.getElementById('o3').index, document.getElementById('o2').text, document.getElementById('o3').label);
    var f = document.getElementById('f');
    out.push(f.elements.length, f.elements.q.value, f.elements[1].id, f.action, f.method, f.length);
    out.push(document.getElementById('q').form === f, document.getElementById('go').type);
    out.join('|')`);
});

test('tables API', async () => {
  await same(FIXTURE, `var t = document.getElementById('tbl'), out = [];
    out.push(t.rows.length, t.tBodies.length, t.rows[1].cells.length, document.getElementById('c2').cellIndex, document.getElementById('r2').rowIndex);
    var r = t.insertRow(1); r.insertCell().textContent = 'x'; r.insertCell(0).textContent = 'y';
    out.push(t.rows.length, r.rowIndex, r.innerHTML, t.createTHead().tagName, t.createCaption().tagName);
    t.deleteRow(0);
    out.push(t.rows.length, t.innerHTML.length > 0);
    out.join('|')`);
});

// ---------------------------------------------------------------- selectors

const SELECTORS = [
  '*', 'li', 'LI', '#menu', '.item', '.item.first', 'li.item:not(.first)', 'ul > li', 'nav li a', 'header a[href]',
  'a[title="Bee"]', 'a[href^="/"]', 'a[href$=".html"]', 'a[href*="b"]', '[class~="last"]', '[lang|="fr"]',
  'p[ID="p1" i]', '[data-role]', 'li:first-child', 'li:last-child', 'li:only-child', 'span:first-of-type',
  'span:last-of-type', 'span:nth-child(2)', '#ol li:nth-child(odd)', '#ol li:nth-child(even)',
  '#ol li:nth-child(3n+1)', '#ol li:nth-child(-n+3)', '#ol li:nth-last-child(2)', '#ol li:nth-of-type(2n)',
  '#ol li:nth-last-of-type(1)', 'span:nth-child(2 of .x)', 'h1 + p', 'h1 ~ p', 'p ~ div', 'div > span ~ span',
  ':root', 'html', 'body > *', 'main :is(h1, p)', ':where(#d1) span', 'div:has(> span.x)', 'main:has(table td)',
  'li:has(+ li.last)', 'section:empty', 'p:not(#p1):not([lang])', 'input:checked', 'option:checked',
  'input:disabled', 'input:enabled', 'option:disabled', 'input[type="text" i]', ':lang(fr)', 'circle', 'svg circle',
  'linearGradient', 'a:link', 'a:any-link', 'td:empty', 'tr:nth-child(2) td', 'input:placeholder-shown',
  'textarea:read-write', 'input:read-only', 'form :required', 'p:first-child', 'main > p:nth-of-type(2)',
  'li:not(:has(a[title]))', ':is(.first, .last) a', 'html:has(body)', 'section:not(:empty)', '#d1 > :nth-child(3)',
  'body :where(header, footer)', 'ul:has(> li:nth-child(3))', 'p, li.first, #foot',
];

test('querySelectorAll results match jsdom for many selectors', async () => {
  const code = `(function () {
    var sels = ${JSON.stringify(SELECTORS)};
    var all = Array.prototype.slice.call(document.getElementsByTagName('*'));
    return sels.map(function (s) {
      try {
        return s + '=>' + Array.prototype.map.call(document.querySelectorAll(s), function (e) { return e.id || (e.localName + all.indexOf(e)); }).join(',');
      } catch (e) { return s + '=>ERR ' + e.name; }
    }).join('\\n');
  })()`;
  const [a, b] = await both(FIXTURE, code);
  const la = a.split('\n'), lb = b.split('\n');
  for (let i = 0; i < la.length; i++) assert.equal(la[i], lb[i], SELECTORS[i]);
});

test('matches, closest, scoped querySelector and invalid selectors', async () => {
  await same(FIXTURE, `var out = [], s = document.getElementById('s3');
    out.push(s.matches('div > .x.y'), s.matches('span:nth-child(3)'), s.closest('main').id, s.closest('.box').id, s.closest('table'));
    var d = document.getElementById('d1');
    out.push(d.querySelectorAll(':scope > span').length, d.querySelector('main span').id, d.querySelectorAll('div span').length);
    out.push(document.querySelector('div::before'), document.querySelectorAll('p::first-line').length);
    out.join('|')`);
  // Chrome's behaviour (jsdom's nwsapi accepts :contains): jQuery's fallback relies on the SyntaxError.
  const page = await ours(FIXTURE);
  const r = page.run(`function name(sel) { try { document.querySelector(sel); return 'ok'; } catch (e) { return e.name; } }
    [name('div:visible'), name('li:eq(1)'), name(':contains(x)'), name('a[href'), name('##x'), name(''), name('div::before'),
     name('::-webkit-scrollbar'), name(':is(div, :bogus)'), name('::bogus-thing'), document.querySelectorAll(':is(p, :bogus)').length].join('|')`);
  assert.equal(r, 'SyntaxError|SyntaxError|SyntaxError|ok|SyntaxError|SyntaxError|ok|ok|ok|SyntaxError|2');
});

// ---------------------------------------------------------------- serialization

test('innerHTML / outerHTML serialization matches jsdom', async () => {
  const html = `<!doctype html><body><div id=x title='a "q" &amp; &lt;b&gt; &nbsp;'>t&amp;u&lt;v&gt;w&nbsp;z<br><img src=a.png alt=""><pre>\n\nline</pre>` +
    `<textarea>\nkeep</textarea><style>a > b { c: "<&>" }</style><script>if (a < b && c > d) {}</script><template><p>in template</p></template>` +
    `<svg viewBox="0 0 1 1"><foreignObject><p>x</p></foreignObject><path d="M0"/></svg><math><mi>x</mi></math>` +
    `<table><tr><td>1</td></tr></table><select><option>a<option>b</select><!--comment--></div></body>`;
  await same(html, `var x = document.getElementById('x'); x.innerHTML + '\\n' + x.outerHTML.length + '\\n' + document.body.innerHTML`);
});

test('innerHTML parsing in context (table rows, options, raw text)', async () => {
  await same('<table><tbody id=tb></tbody></table><select id=s></select><style id=st></style><textarea id=ta></textarea>', `var out = [];
    var tb = document.getElementById('tb'); tb.innerHTML = '<tr><td>1</td></tr><tr><td>2</td>';
    out.push(tb.rows.length, tb.innerHTML);
    var s = document.getElementById('s'); s.innerHTML = '<option>a</option><option selected>b</option>';
    out.push(s.value, s.options.length);
    var st = document.getElementById('st'); st.innerHTML = 'a < b { }';
    out.push(st.textContent, st.childNodes.length);
    var ta = document.getElementById('ta'); ta.innerHTML = '<b>x</b>';
    out.push(ta.value, ta.childNodes.length);
    var d = document.createElement('div'); d.innerHTML = '<p>a<p>b<div>c</div>';
    out.push(d.children.length, d.innerHTML);
    var t = document.createElement('template'); t.innerHTML = '<span>q</span>';
    out.push(t.childNodes.length, t.content.childNodes.length, t.innerHTML);
    d.insertAdjacentHTML('beforeend', '<i>e</i>'); d.insertAdjacentHTML('afterbegin', 'start');
    out.push(d.innerHTML);
    out.join('|')`);
});

test('outerHTML setter and insertAdjacent* positions', async () => {
  await same('<div id=w><p id=p>x</p></div>', `var p = document.getElementById('p'), w = document.getElementById('w');
    p.insertAdjacentText('beforebegin', 'A'); p.insertAdjacentElement('afterend', document.createElement('hr'));
    p.outerHTML = '<span>1</span><span>2</span>';
    [w.innerHTML, p.parentNode].join('|')`);
});

// ---------------------------------------------------------------- events

test('event dispatch order, propagation control and options', async () => {
  await same('<div id=a><p id=b><span id=c>x</span></p></div>', `var out = [];
    var a = document.getElementById('a'), b = document.getElementById('b'), c = document.getElementById('c');
    function log(tag) { return function (e) { out.push(tag + ':' + e.eventPhase + ':' + (e.currentTarget.id || e.currentTarget.nodeName)); }; }
    a.addEventListener('x', log('a-cap'), true); a.addEventListener('x', log('a-bub'));
    b.addEventListener('x', log('b-cap'), { capture: true }); b.addEventListener('x', log('b-bub'));
    c.addEventListener('x', log('c-bub')); c.addEventListener('x', log('c-cap'), true);
    document.addEventListener('x', log('doc'));
    c.dispatchEvent(new Event('x', { bubbles: true }));
    out.push('--');
    c.dispatchEvent(new Event('x'));
    out.push('--');
    b.addEventListener('y', function (e) { e.stopPropagation(); out.push('b-stop'); });
    b.addEventListener('y', function () { out.push('b-second'); });
    a.addEventListener('y', function () { out.push('a-never'); });
    c.dispatchEvent(new Event('y', { bubbles: true }));
    c.addEventListener('z', function (e) { e.stopImmediatePropagation(); out.push('z1'); });
    c.addEventListener('z', function () { out.push('z2'); });
    c.dispatchEvent(new Event('z'));
    var n = 0; c.addEventListener('o', function () { n++; }, { once: true });
    c.dispatchEvent(new Event('o')); c.dispatchEvent(new Event('o')); out.push('once=' + n);
    c.addEventListener('p', function (e) { e.preventDefault(); out.push('passive-prevented=' + e.defaultPrevented); }, { passive: true });
    out.push('p-ret=' + c.dispatchEvent(new Event('p', { cancelable: true })));
    var obj = { handleEvent: function (e) { out.push('handleEvent:' + e.type); } };
    c.addEventListener('h', obj); c.dispatchEvent(new Event('h'));
    c.removeEventListener('h', obj); c.dispatchEvent(new Event('h'));
    var ce = new CustomEvent('cu', { detail: { k: 1 }, cancelable: true });
    c.addEventListener('cu', function (e) { out.push('detail=' + e.detail.k + ',target=' + e.target.id); e.preventDefault(); });
    out.push('cu-ret=' + c.dispatchEvent(ce), ce.defaultPrevented, ce.isTrusted, ce.eventPhase, ce.currentTarget);
    out.join(' ')`);
});

test('on<event> properties and inline handler attributes', async () => {
  // Not differential: jsdom only runs inline handlers with runScripts: 'dangerously'.
  const page = await ours('<button id=b onclick="globalThis.log.push(\'attr:\' + this.id + \':\' + event.type); return false">x</button>');
  const r = page.run(`globalThis.log = [];
    var b = document.getElementById('b');
    var e1 = new MouseEvent('click', { bubbles: true, cancelable: true });
    var r1 = b.dispatchEvent(e1);
    log.push(r1, e1.defaultPrevented, typeof b.onclick);
    b.onclick = function (e) { log.push('prop:' + e.type); };
    b.dispatchEvent(new MouseEvent('click'));
    b.onclick = null;
    b.dispatchEvent(new MouseEvent('click'));
    log.push(b.onclick, typeof document.onreadystatechange, 'onload' in globalThis, 'onclick' in HTMLElement.prototype);
    log.join('|')`);
  assert.equal(r, 'attr:b:click|false|true|function|prop:click||object|true|true');
});

test('click() toggles checkboxes and abort signals remove listeners', async () => {
  await sameFull('<input id=c type=checkbox><label id=l for=c>L</label>', `var out = [], c = document.getElementById('c');
    c.addEventListener('change', function () { out.push('change:' + c.checked); });
    c.addEventListener('input', function () { out.push('input'); });
    c.click(); out.push(c.checked);
    c.addEventListener('click', function (e) { e.preventDefault(); }, { once: true });
    c.click(); out.push(c.checked);
    document.getElementById('l').click(); out.push(c.checked);
    var ac = new AbortController(); var hits = 0;
    c.addEventListener('k', function () { hits++; }, { signal: ac.signal });
    c.dispatchEvent(new Event('k')); ac.abort(); c.dispatchEvent(new Event('k'));
    out.push('hits=' + hits);
    out.join('|')`);
});

test('document.createEvent / initEvent legacy path', async () => {
  await same('<p id=p></p>', `var out = [], p = document.getElementById('p');
    var e = document.createEvent('HTMLEvents');
    try { p.dispatchEvent(e); out.push('no-throw'); } catch (x) { out.push(x.name); }
    e.initEvent('ping', true, true);
    p.addEventListener('ping', function (ev) { out.push(ev.type + ev.bubbles + ev.cancelable); });
    out.push(p.dispatchEvent(e));
    var m = document.createEvent('MouseEvents'); m.initMouseEvent('click', true, true, globalThis, 1, 0, 0, 5, 6, false, false, false, false, 0, null);
    p.addEventListener('click', function (ev) { out.push(ev.clientX + ',' + ev.clientY + ',' + ev.button); });
    p.dispatchEvent(m);
    out.join('|')`);
});

// ---------------------------------------------------------------- CSS

test('element.style writes through to the style attribute', async () => {
  // Not differential: browsers (and jsdom) normalise values ("0" → "0px", shorthand expansion); the
  // reader keeps what the script wrote, which is all its renderer needs.
  const page = await ours('<div id=d style="color: red; margin: 0 auto"></div>');
  const r = page.run(`var d = document.getElementById('d'), s = d.style, out = [];
    out.push(s.color, s.margin, s.length, s.getPropertyValue('color'));
    s.backgroundColor = 'blue'; s.setProperty('width', '10px', 'important'); s.setProperty('--gap', '4px');
    out.push(d.getAttribute('style'));
    s.color = ''; s.removeProperty('margin'); s.cssFloat = 'left';
    out.push(d.getAttribute('style'), s.getPropertyPriority('width'), s.getPropertyValue('--gap'), s.float, s['background-color']);
    d.setAttribute('style', 'display:none');
    out.push(s.display, s.length);
    s.cssText = 'opacity: .5; top: 1px';
    out.push(d.style.cssText, s.item(1), 'webkitTransform' in s, s.WebkitTransition === '');
    s.webkitTransform = 'scale(2)';
    out.push(s.transform, s['-webkit-transform']);
    out.join('|')`);
  assert.equal(r, 'red|0 auto|2|red|color: red; margin: 0 auto; background-color: blue; width: 10px !important; --gap: 4px;|' +
    'background-color: blue; width: 10px !important; --gap: 4px; float: left;|important|4px|left|blue|none|1|opacity: .5; top: 1px;|top|true|true|scale(2)|scale(2)');
});

test('getComputedStyle reports inline values and display defaults', async () => {
  const page = await ours('<div id=d style="color: red"><span id=s>x</span><p id=p hidden>h</p></div><table id=t></table>');
  const r = page.run(`var cs = getComputedStyle(document.getElementById('d'));
    [cs.color, cs.display, getComputedStyle(document.getElementById('s')).display, getComputedStyle(document.getElementById('p')).display,
     getComputedStyle(document.getElementById('t')).display, cs.getPropertyValue('visibility'), cs.position].join('|')`);
  assert.equal(r, 'red|block|inline|none|table|visible|static');
});

test('CSSOM insertRule rules reach the renderer output', async () => {
  const page = await ours('<html><head><style id=st>.a{color:red}</style></head><body><p class=a>x</p></body></html>');
  const r = page.run(`var sh = document.getElementById('st').sheet;
    sh.insertRule('.b { color: blue }', sh.cssRules.length); sh.insertRule('@media (min-width: 1px) { .c { x: y } }', 0);
    var s2 = new CSSStyleSheet(); s2.replaceSync('.adopted { color: green }'); document.adoptedStyleSheets = [s2];
    [sh.cssRules.length, sh.cssRules[2].selectorText, sh.cssRules[0].type, document.styleSheets.length, sh.cssRules[1].style.color].join('|')`);
  assert.equal(r, '3|.b|4|1|red');
  const out = page.serialize();
  assert.match(out, /<style id="st">@media[^<]*\.a \{ color: red; \}\n\.b \{ color: blue; \}<\/style>/);
  assert.match(out, /<head><style>\.adopted \{ color: green; \}<\/style>/);
});

// ---------------------------------------------------------------- shadow DOM and custom elements

test('shadow roots flatten into the renderer output with slots', async () => {
  const page = await ours('<div id=host><span slot=title>Title</span><p>Body text</p><span slot=missing>gone</span></div><x-empty id=e><i>fallback-free</i></x-empty>');
  page.run(`var h = document.getElementById('host'); var sr = h.attachShadow({ mode: 'open' });
    sr.innerHTML = '<h2><slot name="title">Default title</slot></h2><section><slot></slot></section><footer><slot name="foot">Fallback foot</slot></footer>';
    var e = document.getElementById('e').attachShadow({ mode: 'closed' }); e.innerHTML = '<b>closed root</b>';`);
  const out = page.serialize();
  assert.match(out, /<div id="host"><h2><slot name="title"><span slot="title">Title<\/span><\/slot><\/h2><section><slot><p>Body text<\/p><\/slot><\/section><footer><slot name="foot">Fallback foot<\/slot><\/footer><\/div>/);
  assert.match(out, /<x-empty id="e"><b>closed root<\/b><\/x-empty>/);
  const r = page.run(`var h = document.getElementById('host');
    [h.shadowRoot.mode, document.getElementById('e').shadowRoot, h.shadowRoot.querySelector('slot[name=title]').assignedNodes().length,
     h.children[0].assignedSlot.name, h.shadowRoot.host === h, h.shadowRoot.getElementById === undefined].join('|')`);
  assert.equal(r, 'open||1|title|true|false');
});

test('custom elements: upgrade, callbacks, createElement, whenDefined', async () => {
  const page = await ours('<my-card id=c1 name="a"><b>light</b></my-card><div id=w></div>');
  const r = page.run(`globalThis.log = [];
    var p = customElements.whenDefined('my-card').then(function (C) { log.push('defined:' + (C === MyCard)); });
    class MyCard extends HTMLElement {
      static get observedAttributes() { return ['name']; }
      constructor() { super(); this.created = true; log.push('ctor:' + this.localName); }
      connectedCallback() { log.push('connected:' + this.id); }
      disconnectedCallback() { log.push('disconnected:' + this.id); }
      attributeChangedCallback(n, o, v) { log.push('attr:' + n + ':' + o + '>' + v); }
      get label() { return 'card-' + this.getAttribute('name'); }
    }
    var before = document.getElementById('c1') instanceof MyCard;
    customElements.define('my-card', MyCard);
    var c1 = document.getElementById('c1');
    log.push('upgraded:' + (c1 instanceof MyCard) + ':' + before + ':' + c1.created + ':' + c1.label + ':' + c1.matches(':defined'));
    c1.setAttribute('name', 'b'); c1.setAttribute('other', 'x');
    var c2 = document.createElement('my-card'); c2.id = 'c2';
    log.push('created:' + (c2 instanceof MyCard) + ':' + c2.created);
    document.getElementById('w').appendChild(c2);
    c2.remove();
    var c3 = new MyCard(); log.push('new:' + c3.localName + ':' + (c3.ownerDocument === document));
    log.push(customElements.get('my-card') === MyCard, customElements.getName(MyCard));
    try { customElements.define('my-card', class extends HTMLElement {}); } catch (e) { log.push(e.name); }
    try { customElements.define('nodash', class extends HTMLElement {}); } catch (e) { log.push(e.name); }
    try { new HTMLElement(); } catch (e) { log.push(e.name); }
    log.join('|')`);
  await flush();
  const all = page.run('log.join("|")');
  assert.equal(r, 'ctor:my-card|attr:name:null>a|connected:c1|upgraded:true:false:true:card-a:true|attr:name:a>b|ctor:my-card|created:true:true|connected:c2|disconnected:c2|ctor:my-card|new:my-card:true|true|my-card|NotSupportedError|SyntaxError|TypeError');
  assert.match(all, /\|defined:true$/);
});

test('declarative shadow DOM attaches at boot and flattens', async () => {
  const page = await ours('<article id=a><template shadowrootmode="open"><h3>Shadow head</h3><slot></slot></template><p>light</p></article>');
  const r = page.run(`var a = document.getElementById('a'); [a.shadowRoot && a.shadowRoot.innerHTML, a.children.length, a.firstElementChild.tagName].join('|')`);
  assert.equal(r, '<h3>Shadow head</h3><slot></slot>|1|P');
  assert.match(page.serialize(), /<article id="a"><h3>Shadow head<\/h3><slot><p>light<\/p><\/slot><\/article>/);
});

// ---------------------------------------------------------------- MutationObserver

test('MutationObserver records and microtask delivery', async () => {
  const page = await ours('<div id=d><p id=p>t</p></div>');
  page.run(`globalThis.recs = [];
    var mo = new MutationObserver(function (list, obs) {
      list.forEach(function (r) { recs.push(r.type + ':' + (r.attributeName || '') + ':' + r.addedNodes.length + ':' + r.removedNodes.length + ':' + r.oldValue + ':' + (r.target.id || r.target.nodeName)); });
      recs.push('this=' + (obs === mo));
    });
    var d = document.getElementById('d');
    mo.observe(d, { childList: true, subtree: true, attributes: true, attributeOldValue: true, characterData: true, characterDataOldValue: true, attributeFilter: ['class', 'title'] });
    d.appendChild(document.createElement('i'));
    d.className = 'x'; d.title = 't'; d.setAttribute('data-ignored', 1);
    document.getElementById('p').firstChild.data = 'u';
    document.getElementById('p').remove();
    recs.push('sync=' + recs.length);`);
  await flush();
  const r = page.run(`var d = document.getElementById('d');
    var mo2 = new MutationObserver(function () {}); mo2.observe(d, { attributes: true }); d.id = 'e';
    var taken = mo2.takeRecords().length; mo2.disconnect(); d.id = 'd';
    recs.join('|') + '|taken=' + taken`);
  assert.equal(r, 'sync=0|childList::1:0:null:d|attributes:class:0:0:null:d|attributes:title:0:0:null:d|characterData::0:0:t:#text|childList::0:1:null:d|this=true|taken=1');
});

// ---------------------------------------------------------------- traversal and ranges

test('TreeWalker and NodeIterator match jsdom', async () => {
  await same(FIXTURE, `var out = [];
    function tag(n) { return n ? (n.id || n.nodeName) : 'null'; }
    var root = document.getElementById('main');
    var w = document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_COMMENT, {
      acceptNode: function (n) { return n.nodeName === 'SPAN' && n.id === 's2' ? NodeFilter.FILTER_REJECT : n.nodeName === 'B' ? NodeFilter.FILTER_SKIP : NodeFilter.FILTER_ACCEPT; } });
    var n, seq = []; while ((n = w.nextNode())) seq.push(tag(n)); out.push(seq.join(','));
    seq = []; while ((n = w.previousNode())) seq.push(tag(n)); out.push(seq.join(','));
    w.currentNode = document.getElementById('d1');
    out.push(tag(w.firstChild()), tag(w.nextSibling()), tag(w.nextSibling()), tag(w.parentNode()), tag(w.lastChild()), tag(w.previousSibling()));
    var it = document.createNodeIterator(root, NodeFilter.SHOW_TEXT, function (t) { return t.data.trim() ? 1 : 3; });
    seq = []; while ((n = it.nextNode())) seq.push(n.data.trim()); out.push(seq.join(','));
    seq = []; while ((n = it.previousNode())) seq.push(n.data.trim()); out.push(seq.join(','));
    out.join('|')`);
});

test('Range basics match jsdom', async () => {
  await same('<div id=d><p id=a>Hello <b>big</b> world</p><p id=b>Second</p></div>', `var out = [], r = document.createRange();
    var d = document.getElementById('d'), a = document.getElementById('a');
    r.selectNodeContents(a); out.push(r.toString(), r.startOffset, r.endOffset, r.collapsed, r.commonAncestorContainer.id);
    r.setStart(a.firstChild, 2); r.setEnd(a.lastChild, 3);
    out.push(r.toString(), r.commonAncestorContainer.id);
    var r2 = document.createRange(); r2.selectNode(document.getElementById('b'));
    var frag = r2.cloneContents(); out.push(frag.childNodes.length, frag.firstChild.outerHTML);
    var f2 = r2.extractContents(); out.push(f2.firstChild.id, d.children.length);
    var r3 = document.createRange(); r3.selectNodeContents(d);
    var cf = r3.createContextualFragment('<i>ctx</i><b>y</b>'); out.push(cf.childNodes.length, cf.firstChild.tagName);
    r3.collapse(true); out.push(r3.collapsed, r3.startContainer.id);
    var t = a.firstChild; var r4 = document.createRange(); r4.setStart(t, 1); r4.setEnd(t, 4); r4.deleteContents(); out.push(t.data);
    out.join('|')`);
});

// ---------------------------------------------------------------- renderer output

test('renderer serialization: lazy images, form state, dropped bodies', async () => {
  const page = await ours(`<html><head><script>var big = "</div>";</script><style>p{}</style></head><body>
    <img id=i1 class=lazyload src="data:image/gif;base64,R0lGOD" data-src="/real.jpg" data-srcset="/r1.jpg 1x, /r2.jpg 2x">
    <img id=i2 src="/keep.jpg" data-src="/other.jpg"><img id=i3 data-lazy-src="/lazy3.jpg">
    <img id=i4 src="/img/blank.gif" data-original="/orig4.jpg"><picture><source data-srcset="/s.webp"><img src=x.png></picture>
    <iframe data-src="https://www.youtube.com/embed/abc" src="about:blank"></iframe>
    <input id=t value=a><input id=pw type=password><input id=cb type=checkbox checked><textarea id=ta>old</textarea>
    <select id=s><option value=1>one<option value=2>two</select><template><p>tpl</p></template><!-- gone --></body></html>`);
  page.run(`document.getElementById('t').value = 'typed'; document.getElementById('pw').value = 'secret';
    document.getElementById('cb').checked = false; document.getElementById('ta').value = 'new <text>';
    document.getElementById('s').value = '2';`);
  const out = page.serialize();
  assert.match(out, /<img id="i1" class="lazyload" src="\/real.jpg" data-src="\/real.jpg" data-srcset="\/r1.jpg 1x, \/r2.jpg 2x" srcset="\/r1.jpg 1x, \/r2.jpg 2x">/);
  assert.match(out, /<img id="i2" src="\/keep.jpg" data-src="\/other.jpg">/);
  assert.match(out, /<img id="i3" data-lazy-src="\/lazy3.jpg" src="\/lazy3.jpg">/);
  assert.match(out, /<img id="i4" src="\/orig4.jpg" data-original="\/orig4.jpg">/);
  assert.match(out, /<source data-srcset="\/s.webp" srcset="\/s.webp">/);
  assert.match(out, /<iframe data-src="https:\/\/www.youtube.com\/embed\/abc" src="https:\/\/www.youtube.com\/embed\/abc"><\/iframe>/);
  assert.match(out, /<input id="t" value="typed">/);
  assert.match(out, /<input id="pw" type="password">/);
  assert.match(out, /<input id="cb" type="checkbox">/);
  assert.match(out, /<textarea id="ta">new &lt;text&gt;<\/textarea>/);
  assert.match(out, /<option value="1">one<\/option><option value="2" selected="">two<\/option>/);
  assert.match(out, /<script><\/script>/);
  assert.match(out, /<template><\/template>/);
  assert.doesNotMatch(out, /gone|tpl|big/);
});

test('innerText approximates rendered text', async () => {
  const page = await ours('<div id=d><h1>Title</h1><p>One  two<br>three</p><div hidden>secret</div><span style="display:none">no</span><ul><li>a</li><li>b</li></ul>tail <b>bold</b></div>');
  const r = page.run(`document.getElementById('d').innerText`);
  assert.equal(r, 'Title\n\nOne two\nthree\n\na\nb\ntail bold');
});

test('geometry is plausible for rendered elements and zero for hidden ones', async () => {
  const page = await ours('<div id=a>x</div><img id=i width=40 height=30><p id=h hidden>h</p><div style="display:none"><span id=s>in hidden</span></div>');
  const r = page.run(`var a = document.getElementById('a').getBoundingClientRect(), i = document.getElementById('i').getBoundingClientRect();
    var d = document.createElement('div');
    [a.width > 0, a.height > 0, a.top, i.width, i.height, document.getElementById('h').offsetHeight, document.getElementById('s').getBoundingClientRect().width,
     d.offsetWidth, document.documentElement.clientWidth > 0, document.getElementById('a').offsetParent === document.body].join('|')`);
  assert.equal(r, 'true|true|0|40|30|0|0|0|true|true');
});

// ---------------------------------------------------------------- framework-shaped workloads

test('a virtual-DOM style renderer (Preact-like) builds and patches a list', async () => {
  const page = await ours('<div id=app></div>');
  const r = page.run(`
    function h(tag, props, kids) { return { tag: tag, props: props || {}, kids: kids || [] }; }
    function create(v) {
      if (typeof v === 'string') return document.createTextNode(v);
      var el = document.createElement(v.tag);
      for (var k in v.props) { if (k.slice(0, 2) === 'on') el.addEventListener(k.slice(2), v.props[k]); else if (k === 'className') el.className = v.props[k]; else el.setAttribute(k, v.props[k]); }
      v.kids.forEach(function (c) { el.appendChild(create(c)); });
      return el;
    }
    function patch(parent, vs) {
      var node = parent.firstChild, i = 0;
      for (; i < vs.length; i++) {
        var v = vs[i];
        if (!node) { parent.appendChild(create(v)); continue; }
        if (typeof v === 'string') { if (node.nodeType === 3) { if (node.data !== v) node.data = v; } else { parent.replaceChild(create(v), node); } }
        else if (node.nodeName.toLowerCase() !== v.tag) { var n2 = create(v); parent.insertBefore(n2, node); parent.removeChild(node); node = n2; }
        else patch(node, v.kids);
        node = node.nextSibling;
      }
      while (node) { var next = node.nextSibling; parent.removeChild(node); node = next; }
    }
    var app = document.getElementById('app'), clicks = 0;
    var items = []; for (var i = 0; i < 300; i++) items.push('item ' + i);
    patch(app, [h('ul', { className: 'list' }, items.map(function (t) { return h('li', { onclick: function () { clicks++; } }, [t]); }))]);
    var first = app.querySelector('li');
    first.click();
    items.reverse(); items.length = 200;
    patch(app, [h('ul', {}, items.map(function (t) { return h('li', {}, [t]); }))]);
    [app.querySelectorAll('li').length, app.querySelector('li').textContent, app.firstChild.className, clicks, app.firstChild.lastChild.textContent].join('|')`);
  assert.equal(r, '200|item 299|list|1|item 100');
});

test('a template-cloning renderer (Lit/Svelte-like) with comment markers', async () => {
  const page = await ours('<div id=app></div>');
  const r = page.run(`
    var tpl = document.createElement('template');
    tpl.innerHTML = '<article class="card"><h2><!--?m0--></h2><p class="b"><!--?m1--></p><button>Like</button></article>';
    var app = document.getElementById('app');
    ['Alpha', 'Beta', 'Gamma'].forEach(function (title, i) {
      var frag = document.importNode(tpl.content, true);
      var walker = document.createTreeWalker(frag, 128);
      var markers = [], n;
      while ((n = walker.nextNode())) markers.push(n);
      markers[0].replaceWith(document.createTextNode(title));
      markers[1].parentNode.insertBefore(document.createTextNode('body ' + i), markers[1]);
      markers[1].remove();
      app.appendChild(frag);
    });
    [app.children.length, app.querySelectorAll('h2')[1].textContent, app.querySelector('.card:last-child p').textContent, tpl.content.querySelector('h2').childNodes.length].join('|')`);
  assert.equal(r, '3|Beta|body 2|1');
});

test('jQuery-style parsing through createHTMLDocument and DOMParser', async () => {
  await same('<div id=x></div>', `var doc = document.implementation.createHTMLDocument('');
    var base = doc.createElement('base'); base.href = document.location ? document.location.href : 'https://example.test/dir/page.html'; doc.head.appendChild(base);
    doc.body.innerHTML = '<div class="a"><a href="rel.html">link</a></div><p>2</p>';
    var nodes = Array.prototype.slice.call(doc.body.childNodes);
    var x = document.getElementById('x');
    nodes.forEach(function (n) { x.appendChild(document.adoptNode(n)); });
    var parsed = new DOMParser().parseFromString('<p>Hi <b>there</b></p>', 'text/html');
    [x.children.length, x.querySelector('a').getAttribute('href'), parsed.body.innerHTML, parsed.title, parsed.documentElement.nodeName].join('|')`);
});

test('large and deep documents stay fast and do not overflow', async () => {
  let rows = '';
  for (let i = 0; i < 3000; i++) rows += `<tr class="r${i % 10}"><td id="c${i}">${i}</td><td>v${i}</td></tr>`;
  const page = await ours(`<table id=t>${rows}</table><div id=deep></div>`);
  const t0 = Date.now();
  const r = page.run(`var t = document.getElementById('t'), n = 0;
    for (var i = 0; i < 300; i++) if (document.getElementById('c' + (i * 7))) n++;
    var r5 = document.querySelectorAll('tr.r5 td:first-child').length;
    var el = document.getElementById('deep'), cur = el;
    for (var j = 0; j < 5000; j++) { var d = document.createElement('div'); cur.appendChild(d); cur = d; }
    cur.textContent = 'bottom';
    var ul = document.createElement('ul'); document.body.appendChild(ul);
    for (var k = 0; k < 2000; k++) { ul.appendChild(document.createElement('li')); if (ul.childNodes.length !== k + 1) throw new Error('live'); }
    [n, r5, el.innerHTML.length > 5000 * 11, el.textContent, ul.children.length].join('|')`);
  const ms = Date.now() - t0;
  assert.equal(r, '300|300|true|bottom|2000');
  assert.ok(ms < 5000, 'took ' + ms + ' ms');
  const html = page.serialize();
  assert.ok(html.length > 100000);
});

test('radio groups, RadioNodeList and form named access', async () => {
  await same('<form id=f><input type=radio name=c value=r id=a checked><input type=radio name=c value=g id=b><input name=solo id=s value=1></form>', `var f = document.getElementById('f'), out = [];
    var rl = f.elements.c;
    out.push(rl.length, rl.value, f.elements.solo.id, f.elements.namedItem('c').length, Object.prototype.toString.call(rl));
    document.getElementById('b').checked = true;
    out.push(document.getElementById('a').checked, rl.value);
    rl.value = 'r';
    out.push(document.getElementById('a').checked, document.getElementById('b').checked);
    out.join('|')`);
});

test('custom elements created by innerHTML and cloning upgrade when connected', async () => {
  const page = await ours('<div id=w></div>');
  const r = page.run(`globalThis.seen = [];
    customElements.define('x-tag', class extends HTMLElement { connectedCallback() { seen.push('c:' + this.textContent); } });
    var w = document.getElementById('w');
    var holder = document.createElement('div');
    holder.innerHTML = '<x-tag>one</x-tag>';
    seen.push('detached:' + (holder.firstChild instanceof customElements.get('x-tag')));
    w.appendChild(holder);
    seen.push('attached:' + (holder.firstChild instanceof customElements.get('x-tag')));
    var copy = holder.firstChild.cloneNode(true); copy.textContent = 'two';
    w.appendChild(copy);
    seen.push(w.querySelectorAll(':defined').length, w.querySelectorAll('x-tag:not(:defined)').length);
    seen.join('|')`);
  assert.equal(r, 'detached:false|c:one|attached:true|c:two|3|0');
});

test('window load handlers: body onload attribute and onerror signature', async () => {
  const page = await ours('<body onload="globalThis.loaded = (globalThis.loaded || 0) + 1"><p>x</p></body>');
  const r = page.run(`var T = globalThis.__jhTest;
    T.fireEvent(globalThis, 'load', {}, Event);
    globalThis.onerror = function (msg, src, line, col, err) { globalThis.errInfo = [msg, src, line, col, err && err.message].join(','); return true; };
    var ev = T.fireEvent(globalThis, 'error', { message: 'boom', filename: 'a.js', lineno: 3, colno: 4, error: new Error('E'), cancelable: true }, ErrorEvent);
    [globalThis.loaded, globalThis.errInfo, ev.defaultPrevented].join('|')`);
  assert.equal(r, '1|boom,a.js,3,4,E|true');
});

test('document.cookie goes through the natives and stays off detached documents', async () => {
  const page = await ours('<p>x</p>');
  const r = page.run(`document.cookie = 'a=1; path=/'; document.cookie = 'b=2';
    var d2 = document.implementation.createHTMLDocument(''); d2.cookie = 'c=3';
    [document.cookie, d2.cookie].join('|')`);
  assert.equal(r, 'a=1; b=2|');
});

// ---------------------------------------------------------------- differential fuzzing

function lcg(seed) {
  return (n) => {
    seed = (seed * 1103515245 + 12345) & 0x7fffffff;
    return seed % n;
  };
}

test('random mutation sequences serialize exactly like jsdom', async () => {
  const html = '<!doctype html><body><div id=root><p>a</p><span>b</span><ul><li>1</li><li>2</li></ul></div></body>';
  const script = (seed) => `(function(){
    var seed = ${seed};
    function rnd(n) { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed % n; }
    var root = document.getElementById('root');
    var tags = ['div','p','span','b','i','ul','li','section','em','table','tr','td','select','option','template'];
    function all() { var out = [root]; var w = root.getElementsByTagName('*'); for (var i = 0; i < w.length; i++) out.push(w[i]); return out; }
    function pick(list) { return list[rnd(list.length)]; }
    var log = [];
    for (var step = 0; step < 120; step++) {
      var els = all(), op = rnd(14), a = pick(els), b = pick(els);
      try {
        switch (op) {
          case 0: a.appendChild(document.createElement(pick(tags))); break;
          case 1: a.insertBefore(document.createTextNode('t' + step), a.firstChild); break;
          case 2: if (b !== root && !b.contains(a)) a.appendChild(b); break;
          case 3: if (a !== root && a.parentNode) a.parentNode.removeChild(a); break;
          case 4: if (a.firstChild) a.replaceChild(document.createElement(pick(tags)), a.firstChild); break;
          case 5: a.innerHTML = '<b>x' + step + '</b>y<i>z</i>'; break;
          case 6: a.setAttribute('data-s', String(step)); a.className = 'c' + (step % 3); break;
          case 7: a.removeAttribute('data-s'); break;
          case 8: if (a !== root) a.before('pre' + step, document.createElement('hr')); break;
          case 9: if (a !== root) a.after(document.createElement('br')); break;
          case 10: a.textContent = 'txt' + step; break;
          case 11: if (a !== root && !a.contains(b) && b !== root) a.replaceWith(b); break;
          case 12: a.insertAdjacentHTML(pick(['afterbegin','beforeend']), '<em>adj</em>'); break;
          case 13: var f = document.createDocumentFragment(); f.append('f1', document.createElement('u')); a.prepend(f); break;
        }
        log.push(step + ':' + op + ':ok');
      } catch (e) { log.push(step + ':' + op + ':' + e.name); }
    }
    root.normalize();
    return root.outerHTML + '\\n' + log.join(',') + '\\n' + root.querySelectorAll('*').length + ':' + document.querySelectorAll('.c1').length;
  })()`;
  for (let seed = 1; seed <= 24; seed++) {
    const page = await ours(html);
    const a = page.run(script(seed));
    const b = theirs(html).window.eval(script(seed));
    assert.equal(a, b, 'seed ' + seed);
  }
});

test('random selectors match jsdom', async () => {
  const rnd = lcg(11);
  const pick = (a) => a[rnd(a.length)];
  const types = ['*', 'div', 'span', 'p', 'li', 'a', 'ul', 'input', 'option', 'td', 'tr', 'section', 'main', 'b', 'em'];
  const simple = ['#d1', '#s2', '#main', '#menu', '#p1', '#ol', '#tbl', '.item', '.x', '.y', '.box', '.lead', '.first', '.last',
    '[id]', '[class]', '[href]', '[title]', '[lang]', '[type]', '[id^="s"]', '[id$="1"]', '[id*="d"]', '[class~="x"]',
    '[lang|="fr"]', '[value]', '[disabled]', '[checked]', '[data-role="content"]', '[title="bee" i]', ':first-child',
    ':last-child', ':only-child', ':empty', ':nth-child(2)', ':nth-child(odd)', ':nth-child(3n)', ':nth-last-child(1)',
    ':nth-of-type(2)', ':first-of-type', ':last-of-type', ':not(.x)', ':not(span)', ':is(p, li)', ':where(.item)',
    ':has(span)', ':has(> a)', ':checked', ':disabled', ':enabled', ':root', ':link'];
  const compound = () => {
    let s = rnd(3) ? pick(types) : '';
    for (let i = rnd(3); i > 0; i--) s += pick(simple);
    return s || '*';
  };
  const complex = () => {
    let s = compound();
    for (let i = rnd(3); i > 0; i--) s += pick([' ', ' > ', ' + ', ' ~ ']) + compound();
    return s;
  };
  const sels = [];
  for (let i = 0; i < 300; i++) sels.push(rnd(5) ? complex() : complex() + ', ' + complex());
  const code = `(function () {
    var sels = ${JSON.stringify(sels)};
    var all = Array.prototype.slice.call(document.getElementsByTagName('*'));
    return sels.map(function (s) {
      try { return Array.prototype.map.call(document.querySelectorAll(s), function (e) { return all.indexOf(e); }).join(','); }
      catch (e) { return 'ERR'; }
    }).join('\\n');
  })()`;
  const [a, b] = await both(FIXTURE, code);
  const la = a.split('\n'), lb = b.split('\n');
  for (let i = 0; i < sels.length; i++) assert.equal(la[i], lb[i], sels[i]);
});

// ---------------------------------------------------------------- fixes after review

test('renderer output leaves out shadow-root styles and keeps raw text from closing its element', async () => {
  const page = await ours(`<body><my-card><template shadowrootmode="open"><style>:host([hidden]){display:none} h2{color:white}</style>
    <link rel="stylesheet" href="/card.css"><h2><slot name="t"></slot></h2><slot></slot></template>
    <span slot="t">Card</span><p>Body</p></my-card><article><p>Following article</p></article>
    <style id="st"></style></body>`);
  page.run(`document.getElementById('st').textContent = '.x::after{content:"</style><b>leak</b>"}'`);
  const out = page.serialize();
  assert.doesNotMatch(out, /:host|card\.css/);
  assert.match(out, /<h2><slot name="t"><span slot="t">Card<\/span><\/slot><\/h2>/);
  assert.match(out, /<p>Following article<\/p>/);
  assert.match(out, /content:"<\\\/style><b>leak<\\\/b>"/);
  assert.doesNotMatch(out, /<\/style><b>/);
  assert.match(page.run(`document.getElementById('st').innerHTML`), /content:"<\/style><b>leak<\/b>"/);
});

test('getComputedStyle follows the page stylesheets: specificity, !important, inline, CSSOM, pseudo-elements', async () => {
  const page = await ours(`<head><style id="s1">
    #content { display: none }
    .card { display: flex; visibility: hidden }
    div.card.wide { display: grid }
    p { display: inline }
    [data-hide] { display: none !important }
    .tip::before { display: none }
    .tip { opacity: 0.5 }
    </style></head><body><div id="content"><p id="p">x</p></div><div class="card" id="c1"><span id="inner">i</span></div>
    <div class="card wide" id="c2"></div><div id="c3" class="card" style="display:block"></div>
    <div id="c4" data-hide style="display:block"></div><span class="tip" id="tip"></span></body>`);
  const cs = (id, p) => page.run(`getComputedStyle(document.getElementById('${id}')).getPropertyValue('${p}')`);
  assert.equal(cs('content', 'display'), 'none');
  assert.equal(cs('p', 'display'), 'inline');
  assert.equal(cs('c1', 'display'), 'flex');
  assert.equal(cs('c2', 'display'), 'grid');
  assert.equal(cs('c3', 'display'), 'block', 'inline style wins over a normal rule');
  assert.equal(cs('c4', 'display'), 'none', '!important wins over inline');
  assert.equal(cs('inner', 'visibility'), 'hidden', 'visibility is inherited');
  assert.equal(cs('tip', 'display'), 'inline', 'a ::before rule does not apply to the element');
  assert.equal(cs('tip', 'opacity'), '0.5');
  assert.equal(page.run(`document.getElementById('content').offsetWidth`), 0, 'stylesheet-hidden content is not rendered');
  page.run(`document.getElementById('s1').sheet.insertRule('.card { display: block }', 0)`);
  assert.equal(cs('c1', 'display'), 'flex', 'an earlier rule of equal specificity loses');
  page.run(`document.getElementById('s1').sheet.insertRule('#c1 { display: table }', document.getElementById('s1').sheet.cssRules.length)`);
  assert.equal(cs('c1', 'display'), 'table');
  page.run(`document.getElementById('s1').textContent = '#content { display: block }'`);
  assert.equal(cs('content', 'display'), 'block', 'a rewritten <style> is read again');
  assert.equal(cs('c1', 'display'), 'block', 'the old rules are gone');
});

test('images with a source report a natural size', async () => {
  const page = await ours(`<body><img id="a" src="/p.jpg"><img id="b" src="/p.jpg" width="40" height="30"><img id="c"></body>`);
  assert.equal(page.run(`[a.naturalWidth, a.naturalHeight, b.naturalWidth, b.naturalHeight, c.naturalWidth].join()`.replace(/\b([abc])\./g, "document.getElementById('$1').")), '300,150,40,30,0');
});

test('live collections stay right but do not rebuild for unrelated attribute writes', async () => {
  let rows = '';
  for (let i = 0; i < 2000; i++) rows += `<div class="row${i % 10 === 0 ? ' lazyload' : ''}" name="n${i % 3}"><img src="/i${i}.png"><a href="#${i}">${i}</a></div>`;
  const page = await ours(`<body><div id="root">${rows}</div></body>`);
  const t0 = Date.now();
  const res = page.run(`(function () {
    var c = document.getElementsByClassName('lazyload'), imgs = document.getElementsByTagName('img');
    for (var j = 0; j < c.length; j++) { c[j].setAttribute('data-done', '1'); c[j].style.color = 'red'; }
    for (var k = 0; k < imgs.length; k += 50) imgs[k].src = '/x.png';
    var before = c.length;
    c[0].classList.remove('lazyload');
    var names = document.getElementsByName('n1').length;
    document.getElementById('root').firstElementChild.setAttribute('name', 'n1');
    var links = document.links.length;
    document.querySelector('a').removeAttribute('href');
    return [before, c.length, imgs.length, names, document.getElementsByName('n1').length, links, document.links.length].join();
  })()`);
  assert.equal(res, '200,199,2000,667,668,2000,1999');
  assert.ok(Date.now() - t0 < 2000, 'took ' + (Date.now() - t0) + ' ms');
});

test('childNodes and children answer length and the ends without rebuilding', async () => {
  let kids = '';
  for (let i = 0; i < 20000; i++) kids += '<i>' + i + '</i> ';
  const page = await ours(`<body><div id="box">${kids}</div><ul id="u">a<li>1</li>b<li>2</li>c</ul></body>`);
  assert.equal(page.run(`(function () { var u = document.getElementById('u');
    return [u.childNodes.length, u.children.length, u.children[0].textContent, u.children[1].textContent,
      u.childNodes[0].data, u.childNodes[4].data, u.children.item(1).textContent, 2 in u.children].join(); })()`),
  '5,2,1,2,a,c,2,false');
  const t0 = Date.now();
  page.run(`(function () { var b = document.getElementById('box'), c = b.childNodes; while (c.length) b.removeChild(c[0]); })()`);
  assert.equal(page.run(`document.getElementById('box').childNodes.length`), 0);
  assert.ok(Date.now() - t0 < 2000, 'took ' + (Date.now() - t0) + ' ms');
});

test(':nth-child on a long table is not quadratic', async () => {
  let rows = '';
  for (let i = 0; i < 3000; i++) rows += `<tr><td>${i}</td><td class="name">n${i}</td></tr>`;
  const page = await ours(`<body><table><tbody id="tb">${rows}</tbody></table></body>`);
  const t0 = Date.now();
  const n = page.run(`(function () { var n = 0; for (var i = 0; i < 30; i++) n = document.querySelectorAll('#tb > tr:nth-child(2n+1) td.name, tr:last-child td:first-of-type').length; return n; })()`);
  assert.equal(n, 1501);
  assert.ok(Date.now() - t0 < 3000, 'took ' + (Date.now() - t0) + ' ms');
});
