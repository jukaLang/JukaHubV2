// The builder ships everything it draws from the repository: Inter is its only
// typeface and its icons are inline SVG drawn by builder-icons.js. These checks
// are static on purpose: they read the same files the browser gets, so a link
// back to a CDN, a second font, an icon-font class, an icon name nobody drew or
// a character Inter cannot render (which the browser would take from some other
// font) fails here without starting a page.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import zlib from 'node:zlib';
import { fileURLToPath } from 'node:url';
import { JSDOM } from 'jsdom';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const read = rel => fs.readFileSync(path.join(root, rel), 'utf8');
const html = read('index.html');
const headers = read('_headers');
const css = read('style.css');
const interCss = read('fonts/inter.css');
// Every script index.html loads, in load order.
const SCRIPTS = [...html.matchAll(/<script src="([^"]+)"/g)].map(m => m[1]);
const THIRD_PARTY = /fonts\.googleapis\.com|fonts\.gstatic\.com|cdnjs\.cloudflare\.com|fontawesome\.com|cdn\.jsdelivr\.net|unpkg\.com/i;

// builder-icons.js evaluated on its own, the way the page loads it first.
function loadIcons() {
  const sandbox = { console: { warn() {} } };
  vm.createContext(sandbox);
  vm.runInContext(`${read('builder-icons.js')}\nthis.icons = BUILDER_ICONS;`, sandbox);
  return sandbox.icons;
}

// The code points the vendored Inter face can draw, read from the font's own
// cmap. WOFF2 is a small table directory in front of one brotli stream; cmap is
// never transformed, so it can be sliced out of the stream as stored.
function fontCodePoints(file) {
  const buf = fs.readFileSync(file);
  assert.equal(buf.toString('latin1', 0, 4), 'wOF2', `${file} is not a WOFF2 font`);
  const numTables = buf.readUInt16BE(12);
  const compressedSize = buf.readUInt32BE(20);
  let pos = 48;
  const base128 = () => {
    let value = 0;
    for (let i = 0; i < 5; i++) {
      const byte = buf[pos++];
      value = value * 128 + (byte & 0x7f);
      if (!(byte & 0x80)) return value;
    }
    throw new Error(`${file}: malformed table directory`);
  };
  const tables = [];
  for (let i = 0; i < numTables; i++) {
    const flags = buf[pos++];
    const known = flags & 0x3f;
    let tag = known === 0 ? 'cmap' : known === 10 ? 'glyf' : known === 11 ? 'loca' : `#${known}`;
    if (known === 0x3f) {
      tag = buf.toString('latin1', pos, pos + 4);
      pos += 4;
    }
    const version = flags >> 6;
    let length = base128();
    // glyf and loca are transformed at version 0, every other table above it.
    if (tag === 'glyf' || tag === 'loca' ? version === 0 : version !== 0) length = base128();
    tables.push({ tag, length });
  }
  const stream = zlib.brotliDecompressSync(buf.subarray(pos, pos + compressedSize));
  let offset = 0;
  let cmap = null;
  for (const table of tables) {
    if (table.tag === 'cmap') cmap = stream.subarray(offset, offset + table.length);
    offset += table.length;
  }
  assert.ok(cmap, `${file} has no cmap table`);

  let sub = -1;
  let subFormat = 0;
  for (let i = 0; i < cmap.readUInt16BE(2); i++) {
    const platform = cmap.readUInt16BE(4 + i * 8);
    const at = cmap.readUInt32BE(8 + i * 8);
    const format = cmap.readUInt16BE(at);
    if ((platform === 0 || platform === 3) && (format === 12 || (format === 4 && subFormat !== 12))) {
      sub = at;
      subFormat = format;
    }
  }
  const points = new Set();
  if (subFormat === 12) {
    for (let g = 0, groups = cmap.readUInt32BE(sub + 12); g < groups; g++) {
      const start = cmap.readUInt32BE(sub + 16 + g * 12);
      const end = cmap.readUInt32BE(sub + 20 + g * 12);
      const glyph = cmap.readUInt32BE(sub + 24 + g * 12);
      for (let c = start; c <= end; c++) if (glyph + c - start) points.add(c);
    }
  } else if (subFormat === 4) {
    const segments = cmap.readUInt16BE(sub + 6) / 2;
    const ends = sub + 14;
    const starts = ends + segments * 2 + 2;
    const deltas = starts + segments * 2;
    const ranges = deltas + segments * 2;
    for (let s = 0; s < segments; s++) {
      const start = cmap.readUInt16BE(starts + s * 2);
      const end = cmap.readUInt16BE(ends + s * 2);
      const delta = cmap.readInt16BE(deltas + s * 2);
      const range = cmap.readUInt16BE(ranges + s * 2);
      for (let c = start; c <= end && c !== 0xffff; c++) {
        let glyph = (c + delta) & 0xffff;
        if (range) {
          glyph = cmap.readUInt16BE(ranges + s * 2 + range + (c - start) * 2);
          if (glyph) glyph = (glyph + delta) & 0xffff;
        }
        if (glyph) points.add(c);
      }
    }
  }
  return points;
}

const hex = ch => `U+${ch.codePointAt(0).toString(16).toUpperCase().padStart(4, '0')}`;

test('the builder loads Inter and its own files, nothing from a CDN', () => {
  const sheets = [...html.matchAll(/<link rel="stylesheet" href="([^"]+)"/g)].map(m => m[1]);
  assert.deepEqual(sheets, ['fonts/inter.css', 'style.css']);
  for (const rel of [...sheets, ...SCRIPTS]) {
    assert.ok(fs.existsSync(path.join(root, rel)), `${rel} is linked but missing`);
  }
  assert.ok(SCRIPTS.indexOf('builder-icons.js') === 0, 'builder-icons.js must load before the scripts that draw icons');
  for (const [name, text] of [['index.html', html], ['style.css', css], ['fonts/inter.css', interCss], ...SCRIPTS.map(rel => [rel, read(rel)])]) {
    assert.doesNotMatch(text, THIRD_PARTY, `${name} still reaches a third-party host`);
  }

  // fonts/ holds Inter and nothing else, and every face inter.css names is there.
  const faces = fs.readdirSync(path.join(root, 'fonts'));
  assert.deepEqual(faces.filter(f => !/^(inter[\w-]*\.(css|woff2)|LICENSE-inter\.txt)$/.test(f)), [], 'fonts/ may only hold Inter');
  const families = [...interCss.matchAll(/font-family:\s*([^;]+);/g)].map(m => m[1].trim().replace(/['"]/g, ''));
  assert.ok(families.length > 0 && families.every(f => f === 'Inter'), `fonts/inter.css declares ${families.join(', ')}`);
  const urls = [...interCss.matchAll(/url\('\.\/([^']+)'\)/g)].map(m => m[1]);
  assert.ok(urls.length > 0, 'fonts/inter.css references no faces');
  for (const file of urls) {
    assert.ok(fs.existsSync(path.join(root, 'fonts', file)), `fonts/inter.css points at missing ${file}`);
  }
});

test('Inter is the only font family the builder names', () => {
  const generic = new Set(['inherit', 'initial', 'unset', 'system-ui', 'sans-serif', 'ui-sans-serif']);
  const declarations = [
    ...[...css.matchAll(/font-family:\s*([^;}]+)/g)].map(m => ['style.css', m[1]]),
    ...[...css.matchAll(/(--font-[a-z]+):\s*([^;]+);/g)].map(m => [`style.css ${m[1]}`, m[2]]),
    ...[...html.matchAll(/font-family:\s*([^;"]+)/g)].map(m => ['index.html', m[1]]),
    ...SCRIPTS.flatMap(rel => [...read(rel).matchAll(/fontFamily\s*=\s*'([^']+)'|font-family:\s*([^;'"`]+)/g)].map(m => [rel, m[1] || m[2]])),
  ];
  assert.ok(declarations.length >= 5, 'found too few font declarations; did the parser break?');
  for (const [where, value] of declarations) {
    const names = value.split(',').map(part => part.trim().replace(/^['"]|['"]$/g, ''));
    assert.ok(names[0] === 'Inter' || /^var\(--font-(sans|mono)\)$/.test(names[0]) || generic.has(names[0]),
      `${where}: "${value.trim()}" must start with Inter`);
    for (const name of names.slice(1)) {
      assert.ok(generic.has(name), `${where}: "${value.trim()}" names ${name}; only Inter and generic fallbacks are allowed`);
    }
  }
  assert.match(css, /--font-sans:\s*'Inter',/);
  assert.match(css, /--font-mono:\s*var\(--font-sans\);/);
});

test('no icon font is left: no <i> glyphs, no Font Awesome classes', () => {
  assert.doesNotMatch(html, /<i[\s>]/, 'index.html still has <i> icon elements');
  assert.doesNotMatch(html, /fontawesome|font-awesome/i);
  assert.doesNotMatch(headers, /fontawesome|font-awesome/i);
  const faClass = /class="[^"]*\bfa[srb]?\b|class="[^"]*\bfa-|\bfa-(?:solid|regular|brands|light|thin|duotone)\b|['"`]fa[srb] fa-/;
  for (const [name, text] of [['index.html', html], ['style.css', css], ...SCRIPTS.map(rel => [rel, read(rel)])]) {
    assert.doesNotMatch(text, faClass, `${name} still uses a Font Awesome class`);
  }
  assert.doesNotMatch(css, /\.fa[srb]?\b|\.fa-[a-z]/, 'style.css still styles Font Awesome classes');
});

test('every icon the builder asks for is one builder-icons.js draws', () => {
  const icons = loadIcons();
  const known = new Set(Object.keys(icons));
  assert.ok(known.size >= 90, `only ${known.size} icons`);
  for (const [name, markup] of Object.entries(icons)) {
    assert.match(markup, /^(<(path|circle|rect) [^>]+\/>)+$/, `${name} is not plain path/circle/rect markup`);
    assert.doesNotMatch(markup, /style=|on[a-z]+=|href/i, `${name} carries an attribute the CSP or the sanitiser would object to`);
  }

  const placeholders = [...html.matchAll(/<svg class="ico[^"]*" data-icon="([^"]+)" aria-hidden="true"><\/svg>/g)].map(m => m[1]);
  assert.equal(placeholders.length, [...html.matchAll(/data-icon=/g)].length, 'every data-icon in index.html uses the placeholder form');
  assert.ok(placeholders.length >= 100, `only ${placeholders.length} icon placeholders in index.html`);
  for (const name of placeholders) assert.ok(known.has(name), `index.html asks for unknown icon "${name}"`);

  // Literal names handed to iconSvg/createIcon: the first argument, or the
  // branches of a ternary there. Names that arrive through tables are checked
  // at runtime by builder.test.mjs.
  let calls = 0;
  for (const rel of SCRIPTS.filter(rel => rel !== 'builder-icons.js')) {
    const text = read(rel);
    for (const match of text.matchAll(/\b(?:iconSvg|createIcon)\(/g)) {
      let depth = 1;
      let i = match.index + match[0].length;
      const start = i;
      let quote = '';
      for (; i < text.length && depth > 0; i++) {
        const ch = text[i];
        if (quote) { if (ch === quote && text[i - 1] !== '\\') quote = ''; continue; }
        if (ch === '\'' || ch === '"' || ch === '`') quote = ch;
        else if (ch === '(') depth++;
        else if (ch === ')') depth--;
        else if (ch === ',' && depth === 1) break;
      }
      const first = text.slice(start, i);
      const branch = first.includes('?') ? first.slice(first.lastIndexOf('?') + 1) : first;
      for (const lit of branch.matchAll(/'([^']*)'/g)) {
        calls++;
        assert.ok(known.has(lit[1]), `${rel} asks for unknown icon "${lit[1]}"`);
      }
    }
  }
  assert.ok(calls >= 15, `found only ${calls} literal icon names in the scripts; did the scan break?`);
});

test('every character the builder shows is one the vendored Inter draws', () => {
  const inter = fontCodePoints(path.join(root, 'fonts', 'inter-latin-wght-normal.woff2'));
  assert.ok(inter.has(0x41) && inter.has(0x2014) && !inter.has(0x2192), 'the cmap reader disagrees with the font');

  // The page as a browser decodes it (entities included), and the raw sources
  // whose strings end up on screen.
  const { document } = new JSDOM(html).window;
  const shown = [document.body.textContent];
  for (const el of document.querySelectorAll('[title], [placeholder], [aria-label], [alt], [value]')) {
    for (const attr of ['title', 'placeholder', 'aria-label', 'alt', 'value']) {
      if (el.hasAttribute(attr)) shown.push(el.getAttribute(attr));
    }
  }
  const sources = { 'index.html (rendered)': shown.join('\n'), 'index.html': html, 'style.css': css };
  for (const rel of SCRIPTS) sources[rel] = read(rel);
  for (const [name, text] of Object.entries(sources)) {
    const missing = [...new Set([...text].filter(ch => ch.codePointAt(0) > 0x7e && !inter.has(ch.codePointAt(0))))];
    assert.deepEqual(missing.map(hex), [], `${name} has characters Inter cannot draw, so a fallback font would: ${missing.join(' ')}`);
  }
  // A CSS escape in content: is a character too (the old disclosure triangle).
  for (const m of css.matchAll(/content:\s*'\\([0-9a-fA-F]{2,6})/g)) {
    assert.ok(inter.has(parseInt(m[1], 16)), `style.css content: '\\${m[1]}' is not in Inter`);
  }
});

test('the shipped policy allows only same-origin styles and fonts', () => {
  const csp = /Content-Security-Policy:([^\r\n]+)/.exec(headers)?.[1] || '';
  assert.match(csp, /default-src 'self'/);
  assert.match(csp, /script-src 'self'/);
  assert.match(csp, /style-src 'self' 'unsafe-inline';/);
  assert.match(csp, /font-src 'self';/);
  assert.doesNotMatch(csp, THIRD_PARTY);
});
