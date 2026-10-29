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

// Every form control needs an accessible name. Three trigger selects shipped
// without one (axe: select-name, critical) because they sit in a panel that is
// hidden until an element is selected, so a scan of the loaded page missed them.
test('every form control has an accessible name', () => {
  const { document: page } = dom.window;
  const unnamed = [];
  for (const control of page.querySelectorAll('select, textarea, input:not([type="hidden"])')) {
    const named =
      control.getAttribute('aria-label')?.trim() ||
      control.getAttribute('aria-labelledby') ||
      control.getAttribute('title')?.trim() ||
      control.closest('label') ||
      (control.id && page.querySelector(`label[for="${control.id}"]`));
    if (!named) unnamed.push(control.tagName.toLowerCase() + (control.id ? '#' + control.id : ''));
  }
  assert.deepEqual(unnamed, [], 'unnamed controls: ' + unnamed.join(', '));
});

// Preview resolves $variables on the canvas in place. The autosave that the
// repaint itself triggers used to capture that canvas half a second in, so the
// design permanently lost "$greeting" and exported "Hello" instead.
test('preview never writes resolved $variables back into the design', async () => {
  const { window } = dom;
  window.eval("variables.greeting = 'Hello'");
  const el = window.eval("addElement('label', 40, 40)");
  el.querySelector('.text-content').textContent = '$greeting';
  window.eval('enterPreviewMode()');
  await new Promise(r => setTimeout(r, 700)); // past the 500 ms autosave debounce
  assert.match(window.document.getElementById('canvas').textContent, /Hello/, 'preview shows the resolved value');
  window.eval('exitPreviewMode()');
  const texts = window.eval('buildEditorConfig().scenes.find(s => s.name === currentScene).elements.map(e => e.text)');
  assert.ok(texts.includes('$greeting'), 'the design keeps the reference: ' + JSON.stringify(texts));
  assert.ok(!texts.includes('Hello'), 'the resolved value never reaches the export');
});

test('one Undo after Clear All brings the whole project back', () => {
  const { window } = dom;
  const realConfirm = window.confirm;
  window.confirm = () => true;
  try {
    const before = window.eval('Object.keys(scenes).length');
    assert.ok(before > 1, 'the test needs a multi-scene project, got ' + before);
    window.eval('clearAll()');
    assert.equal(window.eval('Object.keys(scenes).length'), 1);
    window.eval('undo()');
    assert.equal(window.eval('Object.keys(scenes).length'), before, 'Undo restores every scene, not just the new menu');
  } finally {
    window.confirm = realConfirm;
  }
});

test('scene bars share one clock timer instead of leaking one per menu', () => {
  const { window } = dom;
  const realSetInterval = window.setInterval;
  let started = 0;
  window.setInterval = (...args) => { started += 1; return realSetInterval.apply(window, args); };
  try {
    window.eval('addScene(); addScene();');
  } finally {
    window.setInterval = realSetInterval;
  }
  assert.equal(started, 0, 'adding scenes must not start timers');
  const clocks = [...window.document.querySelectorAll('#canvas .menu-clock')];
  assert.ok(clocks.length >= 1, 'the new scene has a scene bar');
  assert.match(clocks[0].textContent, /^\d{2}:\d{2}$/, 'the clock shows the time now');
});

test('switching scenes releases the element the Inspector was editing', () => {
  const { window } = dom;
  const doc = window.document;
  const el = window.eval("addElement('button', 60, 60)");
  el.click();
  assert.equal(window.eval('currentElement'), el);
  const other = window.eval('Object.keys(scenes).find(name => name !== currentScene)');
  doc.getElementById('sceneSelector').value = other;
  window.eval('changeScene()');
  assert.equal(window.eval('currentElement'), null, 'no edits may land on a detached element');
  assert.equal(doc.body.classList.contains('element-selected'), false);
});

test('Export menus keep aria-expanded in step and close on Escape', () => {
  const { window } = dom;
  const doc = window.document;
  const btn = doc.getElementById('headerExportBtn');
  const menu = doc.getElementById('headerExportDropdown');
  btn.click();
  assert.equal(btn.getAttribute('aria-expanded'), 'true');
  assert.ok(menu.classList.contains('open'));
  doc.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  assert.equal(btn.getAttribute('aria-expanded'), 'false');
  assert.ok(!menu.classList.contains('open'));
});

test('Delete from the context menu releases the Inspector', () => {
  const { window } = dom;
  const doc = window.document;
  const el = window.eval("addElement('button', 80, 80)");
  el.click();
  el.dispatchEvent(new window.MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }));
  doc.querySelector('#contextMenu [data-action="delete"]').click();
  assert.equal(el.isConnected, false);
  assert.equal(window.eval('currentElement'), null);
  assert.equal(doc.body.classList.contains('element-selected'), false, 'the Inspector no longer shows the deleted element');
});

// touchstart has to cancel its default so the page does not scroll under a
// drag, and that also cancels the click a tap would produce.
test('a tap selects an element on a touch screen', () => {
  const { window } = dom;
  const el = window.eval("addElement('label', 100, 100)");
  window.eval('clearSelectionState()');
  const touch = (type, target) => {
    const event = new window.Event(type, { bubbles: true, cancelable: true });
    Object.defineProperty(event, 'touches', { value: type === 'touchend' ? [] : [{ clientX: 5, clientY: 5 }] });
    target.dispatchEvent(event);
  };
  touch('touchstart', el);
  touch('touchend', el);
  assert.equal(window.eval('currentElement'), el);
});

test('a plain click records no undo step and a drag records one', () => {
  const { window } = dom;
  const doc = window.document;
  const el = window.eval("addElement('button', 120, 120)");
  const top = () => window.eval('undoStack[undoStack.length - 1]');
  const before = top();
  el.dispatchEvent(new window.MouseEvent('mousedown', { bubbles: true, clientX: 10, clientY: 10 }));
  doc.dispatchEvent(new window.MouseEvent('mouseup', { bubbles: true }));
  assert.equal(top(), before, 'selecting is not an edit');
  el.dispatchEvent(new window.MouseEvent('mousedown', { bubbles: true, clientX: 10, clientY: 10 }));
  doc.dispatchEvent(new window.MouseEvent('mousemove', { bubbles: true, clientX: 60, clientY: 40 }));
  doc.dispatchEvent(new window.MouseEvent('mouseup', { bubbles: true }));
  assert.notEqual(top(), before);
  assert.equal(top().label, 'move');
  assert.notEqual(el.getAttribute('data-x'), '120', 'the element moved');
});

test('the mobile tab strip follows a drawer that closes on its own', () => {
  const { window } = dom;
  const doc = window.document;
  const own = Object.getOwnPropertyDescriptor(window, 'innerWidth');
  Object.defineProperty(window, 'innerWidth', { configurable: true, value: 400 });
  try {
    const tab = target => doc.querySelector(`.mobile-tab[data-target="${target}"]`);
    tab('left').click();
    assert.ok(doc.getElementById('leftSidebar').classList.contains('open'));
    assert.equal(tab('left').getAttribute('aria-selected'), 'true');
    doc.getElementById('canvas').click(); // a tap outside the drawer closes it
    assert.ok(!doc.getElementById('leftSidebar').classList.contains('open'));
    assert.equal(tab('canvas').getAttribute('aria-selected'), 'true', 'the strip is back on Canvas');
    assert.ok(tab('canvas').classList.contains('active'));
  } finally {
    if (own) Object.defineProperty(window, 'innerWidth', own);
    else delete window.innerWidth;
  }
});
