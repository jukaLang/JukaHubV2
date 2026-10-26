// Smoke and security tests for the static web builder. Runs the real
// index.html and scripts inside jsdom with network access blocked.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { JSDOM, VirtualConsole, requestInterceptor } from 'jsdom';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');

// The page is served from http://localhost/ (so localStorage works) but its
// files are read from the repo; CDN fonts and icons are skipped so the suite is
// offline-safe.
const ORIGIN = 'http://localhost/';
const MIME = { '.js': 'application/javascript', '.css': 'text/css', '.json': 'application/json', '.html': 'text/html', '.png': 'image/png', '.jpg': 'image/jpeg' };

// jsdom 27+ replaced the ResourceLoader class with request interceptors: the
// callback returns a synthetic Response, or undefined to let the request go to
// the network. Nothing may reach the network here, so every unknown URL gets an
// empty reply.
const localOnly = requestInterceptor(async request => {
  const url = request.url;
  if (url.startsWith(ORIGIN)) {
    const rel = decodeURIComponent(url.slice(ORIGIN.length).split(/[?#]/)[0]) || 'index.html';
    const file = path.join(root, rel);
    if (file.startsWith(root) && fs.existsSync(file) && fs.statSync(file).isFile()) {
      const type = MIME[path.extname(file).toLowerCase()] || 'application/octet-stream';
      return new Response(fs.readFileSync(file), { headers: { 'Content-Type': type } });
    }
    return new Response('', { status: 404 });
  }
  // CDN fonts and icon CSS: an empty stylesheet keeps the suite offline-safe.
  return new Response('', { headers: { 'Content-Type': 'text/css' } });
});

let dom;
const pageErrors = [];

before(async () => {
  const virtualConsole = new VirtualConsole();
  virtualConsole.on('jsdomError', err => pageErrors.push(err));
  dom = await JSDOM.fromFile(path.join(root, 'index.html'), {
    url: ORIGIN,
    runScripts: 'dangerously',
    resources: { interceptors: [localOnly] },
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

test('external links that open a new tab set rel=noopener noreferrer', () => {
  const bad = [...dom.window.document.querySelectorAll('a[target="_blank"]')]
    .filter(a => !/noopener/.test(a.rel) || !/noreferrer/.test(a.rel))
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

test('clicking a Dynamic List element previews its items instead of throwing', () => {
  const { window } = dom;
  const el = window.document.createElement('div');
  el.className = 'element';
  el.setAttribute('data-type', 'dynamiclist');
  el.setAttribute('data-command', '/bin/ls');
  el.setAttribute('data-variable', 'picked');
  window.document.body.appendChild(el);
  window.setupDynamicListExecution(el);

  const errorsBefore = pageErrors.length;
  el.click();

  // The handler used to read an `el` that was not in scope: a ReferenceError on
  // every click, reported by jsdom as an uncaught page error.
  assert.equal(pageErrors.length, errorsBefore, pageErrors.slice(errorsBefore).map(e => String(e.message)).join('; '));
  const select = el.querySelector('select.dynamic-list-select');
  assert.ok(select, 'a preview <select> replaces the element content');
  assert.equal(select.options.length, 2);
  el.remove();
});

test('the remove button of a Dynamic List does not trigger the preview', () => {
  const { window } = dom;
  const el = window.document.createElement('div');
  el.setAttribute('data-command', '/bin/ls');
  el.setAttribute('data-variable', 'picked');
  const remove = window.document.createElement('span');
  remove.className = 'remove-button';
  el.appendChild(remove);
  window.document.body.appendChild(el);
  window.setupDynamicListExecution(el);

  remove.click();
  assert.equal(el.querySelector('select'), null);
  el.remove();
});

test('welcome cards use valid markup: no headings or paragraphs inside buttons', () => {
  const bad = [...dom.window.document.querySelectorAll('button h1, button h2, button h3, button h4, button h5, button h6, button p, button div')]
    .map(el => el.outerHTML.slice(0, 60));
  assert.deepEqual(bad, []);
  assert.equal(dom.window.document.querySelectorAll('.welcome-card .welcome-card-title').length, 4);
  const demoCard = dom.window.document.querySelector('.welcome-card[data-start="demo"]');
  assert.ok(demoCard, 'the 60-second demo card exists in the welcome dialog');
});

test('the AI key field asks browsers not to autofill or autocorrect it', () => {
  const input = dom.window.document.getElementById('aiApiKey');
  assert.equal(input.getAttribute('autocomplete'), 'off');
  assert.equal(input.getAttribute('spellcheck'), 'false');
});

test('searchresults and input elements reveal their inspector blocks and keep them wired', () => {
  const { window } = dom;
  const doc = window.document;

  const results = doc.createElement('div');
  results.className = 'element';
  results.setAttribute('data-type', 'searchresults');
  results.setAttribute('data-variable', 'search_results');
  results.setAttribute('data-columns', '3');
  results.setAttribute('data-rows', '6');
  doc.body.appendChild(results);
  window.showElementProperties(results);

  const block = doc.querySelector('.searchresults-properties');
  assert.equal(block.style.display, 'block');
  assert.equal(doc.getElementById('resultsVariable').value, 'search_results');
  assert.equal(doc.getElementById('gridColumns').value, '3');

  // Blank fields mean "use the player default", so they must remove the
  // attribute instead of exporting nulls/empty strings.
  doc.getElementById('gridColumns').value = '';
  doc.getElementById('gridColumns').onchange();
  assert.equal(results.hasAttribute('data-columns'), false);
  assert.equal(window.elementToConfig(results).columns, undefined);
  assert.equal(window.elementToConfig(results).variable, 'search_results');

  // Another element type hides the block again.
  const button = doc.createElement('div');
  button.className = 'element';
  button.setAttribute('data-type', 'button');
  doc.body.appendChild(button);
  window.showElementProperties(button);
  assert.equal(block.style.display, 'none');

  const input = doc.createElement('div');
  input.className = 'element';
  input.setAttribute('data-type', 'input');
  input.setAttribute('data-variable', 'terminal_cmd');
  doc.body.appendChild(input);
  window.showElementProperties(input);
  const inputBlock = doc.querySelector('.input-properties');
  assert.equal(inputBlock.style.display, 'block');
  assert.equal(doc.getElementById('inputVariable').value, 'terminal_cmd');
  doc.getElementById('inputVariable').value = '';
  doc.getElementById('inputVariable').onchange();
  assert.equal(input.hasAttribute('data-variable'), false);

  [results, button, input].forEach(el => el.remove());
});

test('youtube trigger options are visible, prefilled and round-trip to triggerTarget', () => {
  const { window } = dom;
  const doc = window.document;

  const search = doc.createElement('button');
  search.className = 'element';
  search.setAttribute('data-type', 'button');
  search.setAttribute('data-trigger', 'youtube_search');
  search.setAttribute('data-trigger-target', 'yt-dlp ytsearch20:$search_query');
  search.setAttribute('data-trigger-value', 'search_results');
  doc.body.appendChild(search);
  window.showElementProperties(search);
  const cmd = doc.getElementById('ytSearchCommand');
  const resultVar = doc.getElementById('ytSearchResultVar');
  assert.equal(cmd.style.display, 'block');
  assert.equal(cmd.value, 'yt-dlp ytsearch20:$search_query');
  assert.equal(resultVar.style.display, 'block');
  assert.equal(resultVar.value, 'search_results');

  const play = doc.createElement('button');
  play.className = 'element';
  play.setAttribute('data-type', 'button');
  play.setAttribute('data-trigger', 'play_video_from_var');
  doc.body.appendChild(play);
  window.showElementProperties(play);
  const playFromVar = doc.getElementById('playFromVarSelector');
  assert.equal(playFromVar.style.display, 'block');

  // Choosing a target variable writes the attribute the player reads
  // (core.go reads TriggerTarget for play_video_from_var).
  if (playFromVar.options.length > 0) {
    playFromVar.value = playFromVar.options[playFromVar.options.length - 1].value;
    playFromVar.onchange();
    assert.equal(play.getAttribute('data-trigger-target'), playFromVar.value);
  }

  // Export maps the trigger payloads onto the fields core.go reads.
  const exported = window.elementToConfig(search);
  assert.equal(exported.triggerTarget, 'yt-dlp ytsearch20:$search_query');
  assert.equal(exported.triggerValue, 'search_results');

  [search, play].forEach(el => el.remove());
});

test('typed setting fallback reads the real camelCase key, not a lowercased one', () => {
  const { window } = dom;

  // effectiveConfigValue resolves a setting in the player's order: Custom
  // override first, then live variables, then the imported config's typed
  // fields. The typed-field fallback used to look up a *lowercased* name
  // ("gridcolumns"), so a config that only declared the player's camelCase
  // "gridColumns" silently fell through and the panel showed a stale value.
  //
  // "gridColumns" is used as the probe because it is not one of the live
  // variables the page seeds at load (screenWidth/screenHeight are), so the
  // lookup exercises the imported-config branch rather than the live one.
  // importedConfig is module-scoped, so it is seeded through restoreSnapshot
  // (the real project-load path) rather than by assigning window.importedConfig.
  const snapshot = {
    scenes: {},
    variables: {},
    importedConfig: { top: {}, variables: { gridColumns: 7 }, scenes: {} },
  };
  window.restoreSnapshot(snapshot);

  // The imported config's camelCase key must be found as-is.
  assert.equal(window.effectiveConfigValue('gridColumns'), 7);

  // A key absent everywhere resolves to undefined rather than throwing.
  assert.equal(window.effectiveConfigValue('definitelyNotAKey'), undefined);
});

test('importing an oversized file is refused before it is read', () => {
  const { window } = dom;
  const toasts = () => [...window.document.querySelectorAll('#toastContainer .toast')].map(n => n.textContent);
  let read = false;
  const RealReader = window.FileReader;
  window.FileReader = class { readAsText() { read = true; } };
  try {
    window.eval("importConfigFile({ name: 'movie.json', size: 6 * 1024 * 1024 })");
  } finally {
    window.FileReader = RealReader;
  }
  assert.equal(read, false, 'the oversized file must not be read');
  assert.ok(toasts().some(t => /too large/i.test(t)), 'user is told why: ' + toasts().join(' | '));
});

test('the 60-second demo loads a controller-navigable shell in preview', async () => {
  const { window } = dom;
  const doc = window.document;
  // A visitor's first click: welcome card "60-second demo".
  const card = doc.querySelector('.welcome-card[data-start="demo"]');
  assert.ok(card, 'demo card exists');
  card.click();
  // The card enters preview on the next animation frame; give jsdom one.
  await new Promise(r => setTimeout(r, 60));

  // The demo config actually loaded onto the canvas.
  const labels = [...doc.querySelectorAll('.element')].map(el => el.textContent);
  assert.ok(labels.length >= 8, 'the demo home screen rendered multiple tiles, got ' + labels.length);
  assert.ok(labels.some(t => /Media/.test(t)) && labels.some(t => /JukaHub/.test(t)), 'the demo is JukaHub-shaped, not a blank canvas');

  // ...and the builder jumped straight into preview mode with the D-pad ring
  // on the first button, i.e. the person sees a working shell immediately.
  assert.equal(doc.body.classList.contains('preview-mode'), true, 'preview mode is on after the demo card');
  const focused = doc.querySelector('.element.preview-focus');
  assert.ok(focused, 'a D-pad focus ring is visible');
  const hint = doc.getElementById('previewBar')?.querySelector('.preview-hint');
  assert.ok(hint, 'the preview bar tells the arrows how to act');

  // "A" (Enter) on a scene-linked tile navigates, like the real device.
  const beforeScene = doc.getElementById('previewSceneLabel')?.textContent || '';
  window.eval("previewFocusEl && previewFocusEl.click()");
  const afterScene = doc.getElementById('previewSceneLabel')?.textContent || '';
  assert.ok(/Home/.test(beforeScene), 'the demo starts on Home: ' + beforeScene);
  assert.ok(/Media/.test(afterScene), 'A on Media moves to Media: ' + beforeScene + ' -> ' + afterScene);

  // "B" (Backspace) goes back one scene.
  window.eval("previewGoBack()");
  const backScene = doc.getElementById('previewSceneLabel')?.textContent || '';
  assert.ok(/Home/.test(backScene), 'B returns to Home: ' + backScene);

  // Cleanup for later tests: exit preview the ordinary way.
  window.eval("exitPreviewMode()");
  assert.equal(doc.body.classList.contains('preview-mode'), false, 'preview exits');

  // Leave the editor object overlay-free for subsequent tests.
  window.eval("clearAll()");
});

// The builder stamps every export with the config schema version the player
// checks on load. The two constants live in different languages, so this is the
// only thing that stops them drifting apart.
test('the exported schemaVersion matches the player ConfigSchemaVersion', () => {
  const go = fs.readFileSync(path.join(root, 'player/config_schema.go'), 'utf8');
  const playerVersion = Number(/const ConfigSchemaVersion\s*=\s*(\d+)/.exec(go)?.[1]);
  assert.ok(playerVersion >= 1, 'could not read ConfigSchemaVersion from player/config_schema.go');

  const { window } = dom;
  window.eval("clearAll()");
  const exported = window.eval("buildEditorConfig()");
  assert.equal(exported.schemaVersion, playerVersion, 'builder export carries the player schema version');
});
