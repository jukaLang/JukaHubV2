// Smoke and security tests for the static web builder. Runs the real
// index.html and scripts inside jsdom with network access blocked.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { JSDOM, ResourceLoader, VirtualConsole } from 'jsdom';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');

// The page is served from http://localhost/ (so localStorage works) but its
// files are read from the repo; CDN fonts and icons are skipped so the suite is
// offline-safe.
const ORIGIN = 'http://localhost/';
class LocalOnlyLoader extends ResourceLoader {
  fetch(url) {
    if (url.startsWith(ORIGIN)) {
      const rel = decodeURIComponent(url.slice(ORIGIN.length).split(/[?#]/)[0]);
      const file = path.join(root, rel);
      if (file.startsWith(root) && fs.existsSync(file)) return Promise.resolve(fs.readFileSync(file));
    }
    return Promise.resolve(Buffer.from(''));
  }
}

let dom;
const pageErrors = [];

before(async () => {
  const virtualConsole = new VirtualConsole();
  virtualConsole.on('jsdomError', err => pageErrors.push(err));
  dom = await JSDOM.fromFile(path.join(root, 'index.html'), {
    url: ORIGIN,
    runScripts: 'dangerously',
    resources: new LocalOnlyLoader(),
    pretendToBeVisual: true,
    virtualConsole,
    beforeParse(window) {
      window.matchMedia ||= () => ({ matches: false, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} });
      window.ResizeObserver ||= class { observe() {} unobserve() {} disconnect() {} };
      window.IntersectionObserver ||= class { observe() {} unobserve() {} disconnect() {} };
      window.HTMLElement.prototype.scrollIntoView ||= function () {};
      window.HTMLCanvasElement.prototype.getContext = () => null;
      // jsdom has no fetch: serve repo files, refuse everything else.
      window.fetch ||= async url => {
        const full = new URL(String(url), ORIGIN).href;
        const file = full.startsWith(ORIGIN) ? path.join(root, decodeURIComponent(full.slice(ORIGIN.length).split(/[?#]/)[0])) : '';
        if (!file || !file.startsWith(root) || !fs.existsSync(file)) return { ok: false, status: 404, statusText: 'Not Found', json: async () => ({}), text: async () => '' };
        const text = fs.readFileSync(file, 'utf8');
        return { ok: true, status: 200, statusText: 'OK', json: async () => JSON.parse(text), text: async () => text };
      };
      window.URL.createObjectURL ||= () => 'blob:test';
      window.URL.revokeObjectURL ||= () => {};
    },
  });
  // Let load handlers and DOMContentLoaded work finish.
  if (dom.window.document.readyState !== 'complete') {
    await Promise.race([
      new Promise(resolve => dom.window.addEventListener('load', resolve)),
      new Promise(resolve => setTimeout(resolve, 5000)),
    ]);
  }
  await new Promise(resolve => setTimeout(resolve, 100));
});

// The builder keeps timers running (clock, autosave); close the window so the
// test process can exit.
after(() => { dom.window.close(); });

test('page loads without uncaught script errors', () => {
  const real = pageErrors.filter(e => !/Could not load|Could not parse CSS|Not implemented/.test(String(e && e.message)));
  assert.deepEqual(real.map(e => String(e.message)), []);
});

test('viewport does not block pinch zoom', () => {
  const meta = dom.window.document.querySelector('meta[name="viewport"]').content;
  assert.doesNotMatch(meta, /user-scalable\s*=\s*no/);
  assert.doesNotMatch(meta, /maximum-scale\s*=\s*1(\.0)?\b/);
});

test('no inline event-handler attributes remain in the markup', () => {
  const offenders = [...dom.window.document.querySelectorAll('*')]
    .filter(el => [...el.attributes].some(a => /^on[a-z]+$/i.test(a.name)))
    .map(el => el.outerHTML.slice(0, 80));
  assert.deepEqual(offenders, []);
});

test('external links that open a new tab set rel=noopener', () => {
  const bad = [...dom.window.document.querySelectorAll('a[target="_blank"]')]
    .filter(a => !/noopener/.test(a.rel))
    .map(a => a.href);
  assert.deepEqual(bad, []);
});

test('variable rows render hostile names and values as inert text', () => {
  const { window } = dom;
  const nasty = `<img src=x onerror="window.__pwned=1">`;
  const item = window.createVariableItem(nasty, `</div><script>window.__pwned=2</script>`);
  window.document.body.appendChild(item);
  assert.equal(item.querySelector('img'), null);
  assert.equal(item.querySelector('script'), null);
  assert.equal(item.querySelector('.variable-name').textContent, nasty);
  assert.ok(![...item.querySelectorAll('*')].some(el => el.hasAttribute('onclick')));
  assert.equal(window.__pwned, undefined);
});

test('variable row buttons call edit/delete with the exact name, even with quotes', () => {
  const { window } = dom;
  const calls = [];
  window.editVariable = name => calls.push(['edit', name]);
  window.deleteVariable = name => calls.push(['delete', name]);
  const name = `x');alert(1);//`;
  const item = window.createVariableItem(name, 'v');
  const [edit, del] = item.querySelectorAll('button');
  edit.click();
  del.click();
  assert.deepEqual(calls, [['edit', name], ['delete', name]]);
});

test('share sanitizer removes secrets before a design becomes a link', () => {
  const clean = dom.window.sanitizeSharedConfig({
    variables: { apiKey: 'sk-live', theme: 'dark', nested: { password: 'p', ok: 1 } },
  });
  assert.equal(clean.variables.apiKey, undefined);
  assert.equal(clean.variables.nested.password, undefined);
  assert.equal(clean.variables.theme, 'dark');
  assert.equal(clean.variables.nested.ok, 1);
});

test('the editor produces a JSON-serializable config with scenes', () => {
  const config = dom.window.buildEditorConfig();
  const round = JSON.parse(JSON.stringify(config));
  assert.ok(round.scenes && typeof round.scenes === 'object');
});
