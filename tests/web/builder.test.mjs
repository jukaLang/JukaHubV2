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
      window.localStorage.setItem('jukahub-ai-key', 'old-test-key');
      window.localStorage.setItem('jukahub-ai-provider', 'openai');
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

// The canvas draws tiles with Font Awesome and the player draws them with SDL
// primitives, so the two can only agree on which picture a name means. They did
// not: the canvas searched the label in its own order and ignored an explicit
// `icon`, which made the Tools card a grid here and sliders on the device, and
// drew Exit as a door where the player draws a power ring. These cases come from
// player/homelayout.go (drawHomeIconShapes, the home scene) and
// player/launcher.go (explicitTileIcon, tileIconKind, drawTileIcon).
test('tile icons follow the player home and launcher vocabularies', () => {
  const { window } = dom;
  const iconFor = (icon, text, trigger, layout) => window.eval(
    `tileIconClass(${JSON.stringify(icon)}, ${JSON.stringify(text)}, ${JSON.stringify(trigger)}, ${JSON.stringify(layout)})`
  );

  // Home scene: the name alone decides what is drawn.
  const home = {
    continue: 'fa-play',
    media: 'fa-play',
    files: 'fa-folder',
    packages: 'fa-box',
    apps: 'fa-table-cells-large',
    favorites: 'fa-star',
    browser: 'fa-globe',
    chat: 'fa-comment',
    tools: 'fa-sliders',
    settings: 'fa-gear',
    clock: 'fa-clock',
    emulator: 'fa-gamepad',
    exit: 'fa-power-off'
  };
  for (const [name, glyph] of Object.entries(home)) {
    assert.ok(iconFor(name, '', '', 'home').includes(glyph), `home tile ${name} should draw ${glyph}`);
  }
  // A home tile without its own icon keeps the card's built-in one.
  assert.ok(iconFor('', 'Tools', 'change_scene:Misc', 'home').includes('fa-sliders'));
  assert.ok(iconFor('not-a-glyph', 'Media', 'change_scene:Tube', 'home').includes('fa-circle'), 'unknown home name falls back to a ring, like drawHomeIconShapes');

  // Ordinary scene: an explicit glyph name wins over what the label suggests.
  assert.ok(iconFor('disk', 'Disk Space', 'change_scene:DiskSpace', '').includes('fa-hard-drive'));
  assert.ok(iconFor('cache', 'Clear Cache', 'cache_clear', '').includes('fa-broom'));
  assert.ok(iconFor('logs', 'Log Exporter', 'change_scene:LogExporter', '').includes('fa-file-lines'));

  // Otherwise the label and trigger are searched in tileIconKind's order:
  // apps/tools comes before anything else tool-like, and continue is last.
  assert.ok(iconFor('', 'Tools', 'change_scene:Misc', '').includes('fa-table-cells-large'));
  assert.ok(iconFor('', 'Continue', 'change_scene:Tube', '').includes('fa-play'));
  assert.ok(iconFor('', 'Weather', '', '').includes('fa-cloud'));
  assert.ok(iconFor('', 'Nothing in particular', '', '').includes('fa-table-cells-large'));

  // The tile the canvas builds carries the glyph, not just the helper: a home
  // card and an ordinary tools tile must end up with different pictures.
  const tileIcon = (data, layout) => {
    window.eval(`window.__tile = createElementFromData(${JSON.stringify(data)}, ${JSON.stringify(layout)})`);
    const cls = window.eval("window.__tile.querySelector('.tile-icon i').className");
    window.eval("window.__tile.remove(); delete window.__tile;");
    return cls;
  };
  const tile = { type: 'button', style: 'tile', x: 0, y: 0, width: '120', height: '120' };
  assert.ok(tileIcon({ ...tile, icon: 'tools', text: 'Tools', trigger: 'change_scene:Misc' }, 'home').includes('fa-sliders'), 'a home Tools card draws sliders');
  assert.ok(tileIcon({ ...tile, icon: 'tools', text: 'Tools', trigger: 'change_scene:Misc' }, '').includes('fa-table-cells-large'), 'a Tools tile outside the home scene draws the app grid');
  assert.ok(tileIcon({ ...tile, icon: 'disk', text: 'Disk Space', trigger: 'change_scene:DiskSpace' }, '').includes('fa-hard-drive'), 'a tile with an explicit glyph name keeps it');
});

// The shipped config's home screen is applied card by card, keyed on the tile's
// text (player/homelayout.go ApplyConfig + configItemFor): a tile whose text
// names no card is simply not drawn on the device while the canvas shows it.
// Its icon must also be one drawHomeIconShapes can paint, or the tile reads as
// the unknown-icon ring.
test('every home tile in the shipped configs is a card the player draws', () => {
  const go = fs.readFileSync(path.join(root, 'player/homelayout.go'), 'utf8');
  const cardTitles = [...go.matchAll(/card\(HomeFocus\w+,\s*"([^"]+)"/g)].map(m => m[1]);
  assert.ok(cardTitles.length >= 10, 'could not read the home card titles from player/homelayout.go');

  const { window } = dom;
  for (const file of ['player/jukaconfig.json', 'player/trimui/jukaconfig.device.json']) {
    const config = JSON.parse(fs.readFileSync(path.join(root, file), 'utf8'));
    const home = config.scenes.find(scene => String(scene.layout || '').toLowerCase() === 'home');
    assert.ok(home, `${file} has a home-layout scene`);
    for (const el of home.elements) {
      const text = String(el.text || '');
      // A home scene is drawn by the home layout, not by its element list: a
      // tile with no text names no card, so the device never draws it while the
      // canvas would (this is how a second Continue card got into the device
      // config).
      assert.ok(text.trim() !== '', `${file}: a ${el.type} with no text is drawn on the canvas but ignored by the player`);
      assert.ok(
        cardTitles.some(title => title.toLowerCase() === text.toLowerCase() || text.toLowerCase().includes('tools') && title === 'Tools'),
        `${file}: home tile ${JSON.stringify(text)} names no home card in player/homelayout.go`
      );
      const cls = window.eval(
        `tileIconClass(${JSON.stringify(el.icon || '')}, ${JSON.stringify(text)}, ${JSON.stringify(el.trigger || '')}, 'home')`
      );
      assert.ok(!cls.includes('fa-circle'), `${file}: home tile ${text} would draw the unknown-icon ring`);
    }
  }
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

test('the Element inspector shows controls only for a selection', () => {
  const { window } = dom;
  const doc = window.document;
  const original = window.eval('captureWorkspace("inspector-test")');
  const empty = doc.getElementById('noSelection');
  const geometry = doc.querySelector('.position-controls');
  try {
    window.clearSelectionState();
    window.switchTab('element-properties');
    assert.notEqual(window.getComputedStyle(empty).display, 'none');
    assert.equal(window.getComputedStyle(geometry).display, 'none');
    const el = window.addElement('button', 60, 60);
    el.click();
    assert.equal(window.getComputedStyle(empty).display, 'none');
    assert.equal(window.getComputedStyle(geometry).display, 'grid');
    window.enterPreviewMode();
    window.exitPreviewMode();
    window.switchTab('element-properties');
    assert.notEqual(window.getComputedStyle(empty).display, 'none', 'preview clears the selection');
    assert.equal(window.getComputedStyle(geometry).display, 'none');
  } finally {
    if (window.eval('previewMode')) window.exitPreviewMode();
    window.restoreSnapshot(original);
  }
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

test('component search hides empty categories and explains when nothing matches', () => {
  const { window } = dom;
  const doc = window.document;
  const search = doc.getElementById('componentSearch');
  const sections = [...doc.querySelectorAll('.left-sidebar .component-section')];
  const empty = doc.getElementById('componentSearchEmpty');
  const filter = value => {
    search.value = value;
    search.dispatchEvent(new window.Event('input', { bubbles: true }));
  };
  try {
    filter('search results');
    assert.deepEqual(sections.map(section => section.hidden), [true, true, false]);
    assert.equal(empty.hidden, true);
    filter('no such component');
    assert.ok(sections.every(section => section.hidden));
    assert.equal(empty.hidden, false);
    filter('');
    assert.ok(sections.every(section => !section.hidden));
    assert.ok([...doc.querySelectorAll('.left-sidebar .element[data-type]')].every(el => !el.hidden));
    assert.equal(empty.hidden, true);
  } finally {
    filter('');
  }
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


test('snap to grid is optional and respects zoom, bounds and undo', () => {
  const { window } = dom;
  const doc = window.document;
  const toggle = doc.getElementById('snapGridBtn');
  const el = window.eval("addElement('button', 13, 17)");
  Object.defineProperties(el, {
    offsetLeft: { configurable: true, get: () => parseFloat(el.style.left) },
    offsetTop: { configurable: true, get: () => parseFloat(el.style.top) },
    offsetWidth: { configurable: true, value: 120 },
    offsetHeight: { configurable: true, value: 40 },
  });
  const previousScale = window.eval('canvasScale');
  window.eval('canvasScale = 0.5');
  const drag = (dx, dy) => {
    el.dispatchEvent(new window.MouseEvent('mousedown', { bubbles: true, clientX: 10, clientY: 10 }));
    doc.dispatchEvent(new window.MouseEvent('mousemove', { bubbles: true, clientX: 10 + dx, clientY: 10 + dy }));
    doc.dispatchEvent(new window.MouseEvent('mouseup', { bubbles: true }));
  };
  try {
    assert.equal(toggle.getAttribute('aria-pressed'), 'false');
    drag(2, 3);
    assert.equal(el.getAttribute('data-x'), '17');
    assert.equal(el.getAttribute('data-y'), '23');
    toggle.click();
    assert.equal(toggle.getAttribute('aria-pressed'), 'true');
    drag(0, 0);
    assert.equal(el.getAttribute('data-x'), '17', 'a click does not snap an off-grid item');
    drag(5, 5);
    assert.equal(el.getAttribute('data-x'), '24');
    assert.equal(el.getAttribute('data-y'), '32');
    assert.equal(window.eval('undoStack[undoStack.length - 1].label'), 'move');
    window.eval('undo()');
    assert.ok(doc.querySelector('#canvas .element[data-x="17"][data-y="23"]'), 'undo restores the unsnapped location');
    el.style.left = '13px';
    el.style.top = '17px';
    window.beginElementDrag(el, 0, 0);
    window.moveElementDrag(10000, 10000);
    assert.equal(Number(el.getAttribute('data-x')), window.eval('canvasWidth') - 120);
    assert.equal(Number(el.getAttribute('data-y')), window.eval('canvasHeight') - 40);
    window.endElementDrag();
  } finally {
    if (toggle.getAttribute('aria-pressed') === 'true') toggle.click();
    window.eval(`canvasScale = ${previousScale}`);
  }
});

test('alignment guides are optional, zoom-aware and never change or save the layout', () => {
  const { window } = dom;
  const doc = window.document;
  const canvas = doc.getElementById('canvas');
  const toggle = doc.getElementById('alignmentGuidesBtn');
  const overlay = doc.getElementById('alignmentGuides');
  const vertical = overlay.querySelector('.alignment-guide--vertical');
  const horizontal = overlay.querySelector('.alignment-guide--horizontal');
  const hidden = [...canvas.children].map(el => [el, el.hidden]);
  hidden.forEach(([el]) => { el.hidden = true; });
  const el = window.addElement('button', 60, 20);
  const target = window.addElement('button', 100, 40);
  const layout = (item, width, height) => Object.defineProperties(item, {
    offsetLeft: { configurable: true, get: () => parseFloat(item.style.left) },
    offsetTop: { configurable: true, get: () => parseFloat(item.style.top) },
    offsetWidth: { configurable: true, value: width },
    offsetHeight: { configurable: true, value: height },
  });
  layout(el, 20, 20);
  layout(target, 40, 20);
  const previousScale = window.eval('canvasScale');
  window.eval('canvasScale = 0.5');
  try {
    assert.equal(toggle.getAttribute('aria-pressed'), 'false');
    window.beginElementDrag(el, 0, 0);
    window.moveElementDrag(10.5, 11);
    assert.equal(overlay.hidden, true, 'guides are off by default');
    window.endElementDrag();
    el.style.left = '60px'; el.style.top = '20px';
    toggle.click();
    assert.equal(toggle.getAttribute('aria-pressed'), 'true');
    window.beginElementDrag(el, 0, 0);
    window.moveElementDrag(10.5, 11);
    assert.equal(el.getAttribute('data-x'), '81', 'guides do not snap the element');
    assert.equal(el.getAttribute('data-y'), '42');
    assert.equal(overlay.hidden, false);
    assert.equal(vertical.hidden, false);
    assert.equal(horizontal.hidden, false);
    assert.equal(vertical.style.left, '50px');
    assert.equal(horizontal.style.top, '20px');
    window.saveCurrentScene();
    assert.equal(window.eval('scenes[currentScene].some(el => el.id === "alignmentGuides")'), false);
    window.moveElementDrag(70, 90);
    assert.equal(overlay.hidden, true, 'no match hides the guides');
    window.moveElementDrag(10.5, 11);
    toggle.click();
    assert.equal(overlay.hidden, true, 'turning guides off clears them mid-drag');
    toggle.click();
    window.moveElementDrag(10.5, 11);
    window.endElementDrag();
    assert.equal(overlay.hidden, true, 'guides disappear on release');
  } finally {
    window.endElementDrag();
    if (toggle.getAttribute('aria-pressed') === 'true') toggle.click();
    window.eval(`canvasScale = ${previousScale}`);
    el.remove(); target.remove();
    hidden.forEach(([item, value]) => { item.hidden = value; });
    window.saveCurrentScene();
  }
});

test('safe-area guide is optional, zoom-aware and excluded from the design', () => {
  const { window } = dom;
  const doc = window.document;
  const toggle = doc.getElementById('safeAreaBtn');
  const overlay = doc.getElementById('safeAreaOverlay');
  const original = window.eval('captureWorkspace("safe-area-test")');
  const previousScale = window.eval('canvasScale');
  const previousManual = window.eval('zoomIsManual');
  try {
    window.eval('canvasWidth = 1280; canvasHeight = 720; syncCanvasSizeControls(); applyCanvasScale(1)');
    const before = JSON.stringify(window.eval('captureWorkspace("safe-area-test")'));
    const configBefore = JSON.stringify(window.buildEditorConfig());
    const undoCount = window.eval('undoStack.length');
    assert.equal(toggle.getAttribute('aria-pressed'), 'false');
    assert.equal(overlay.hidden, true);
    toggle.click();
    assert.equal(toggle.getAttribute('aria-pressed'), 'true');
    assert.equal(overlay.hidden, false);
    assert.equal(overlay.style.left, '32px');
    assert.equal(overlay.style.top, '32px');
    assert.equal(overlay.style.width, '1216px');
    assert.equal(overlay.style.height, '656px');
    window.applyCanvasScale(0.5);
    assert.equal(overlay.style.left, '16px');
    assert.equal(overlay.style.top, '16px');
    assert.equal(overlay.style.width, '608px');
    assert.equal(overlay.style.height, '328px');
    assert.equal(JSON.stringify(window.eval('captureWorkspace("safe-area-test")')), before);
    assert.equal(JSON.stringify(window.buildEditorConfig()), configBefore);
    assert.equal(window.eval('undoStack.length'), undoCount);
    window.eval('canvasWidth = 1024; canvasHeight = 768; syncCanvasSizeControls()');
    assert.equal(overlay.hidden, true, 'guide is hidden on another device size');
    assert.equal(toggle.getAttribute('aria-pressed'), 'true', 'choice is remembered');
    window.eval('canvasWidth = 1280; canvasHeight = 720; syncCanvasSizeControls()');
    assert.equal(overlay.hidden, false, 'guide returns on the handheld canvas');
    window.enterPreviewMode();
    assert.equal(overlay.hidden, true, 'guide is hidden in preview');
    window.exitPreviewMode();
    assert.equal(overlay.hidden, false, 'guide returns after preview');
    toggle.click();
    assert.equal(overlay.hidden, true);
    assert.equal(toggle.getAttribute('aria-pressed'), 'false');
  } finally {
    if (window.eval('previewMode')) window.exitPreviewMode();
    if (toggle.getAttribute('aria-pressed') === 'true') toggle.click();
    window.restoreSnapshot(original);
    window.eval(`zoomIsManual = ${previousManual}`);
    window.applyCanvasScale(previousScale);
  }
});

test('AI keys are session-only, legacy keys are removed and providers do not share keys', async () => {
  const { window } = dom;
  const doc = window.document;
  const provider = doc.getElementById('aiProvider');
  const key = doc.getElementById('aiApiKey');
  assert.equal(window.localStorage.getItem('jukahub-ai-key'), null);
  assert.equal(window.localStorage.getItem('jukahub-ai-provider'), null);
  provider.value = 'openai';
  provider.dispatchEvent(new window.Event('change'));
  key.value = 'temporary-test-key';
  const originalFetch = window.fetch;
  let request;
  window.fetch = async (url, options) => {
    request = { url, options };
    return { ok: true, json: async () => ({ choices: [{ message: { content: 'test reply' } }] }) };
  };
  try {
    const prompt = doc.getElementById('aiPrompt');
    prompt.value = 'add a tile';
    prompt.dispatchEvent(new window.Event('input', { bubbles: true }));
    doc.getElementById('aiSuggestBtn').click();
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.equal(request.url, 'https://api.openai.com/v1/chat/completions');
    assert.equal(request.options.headers.Authorization, 'Bearer temporary-test-key');
    assert.equal(window.localStorage.getItem('jukahub-ai-key'), null);
    provider.value = 'anthropic';
    provider.dispatchEvent(new window.Event('change'));
    assert.equal(key.value, '', 'switching providers clears the old provider key');
    key.value = 'second-test-key';
    doc.getElementById('aiKeyClearBtn').click();
    assert.equal(key.value, '');
    assert.equal(window.localStorage.getItem('jukahub-ai-key'), null);
  } finally {
    window.fetch = originalFetch;
    provider.value = 'free';
    provider.dispatchEvent(new window.Event('change'));
  }
});
