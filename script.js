// DOM Elements
const canvas = document.getElementById('canvas');
const darkModeToggle = document.getElementById('darkModeToggle');
const sceneSelector = document.getElementById('sceneSelector');
const toggleGuide = document.getElementById('toggleGuide');
const closeGuide = document.getElementById('closeGuide');
const guidePanel = document.getElementById('guidePanel');
const elementProperties = document.getElementById('elementProperties');
const canvasSizeSelect = document.getElementById('canvasSize');
const customWidthInput = document.getElementById('customWidth');
const customHeightInput = document.getElementById('customHeight');
const backgroundFileInput = document.getElementById('backgroundFile');
const titleSizeInput = document.getElementById('titleSize');
const bigSizeInput = document.getElementById('bigSize');
const mediumSizeInput = document.getElementById('mediumSize');
const smallSizeInput = document.getElementById('smallSize');
const addVariableButton = document.getElementById('addVariableButton');
const variablesList = document.getElementById('variablesList');
const loadFileInput = document.getElementById('loadFile');
const clearButton = document.getElementById('clearButton');
const propertiesTabs = document.querySelectorAll('.properties-tab');
const elementPropertiesPanel = document.getElementById('elementPropertiesPanel');
const appInfoPanel = document.getElementById('appInfoPanel');

// Global State
let backgroundPath = '';
let scenes = { 'Scene 1': [] };
let currentScene = 'Scene 1';
let variables = {};
let currentElement = null;
let canvasWidth = 1280;
let canvasHeight = 720;
let globalTooltip = null;
// Import/init also adds elements; those are not user edits and must not land on
// the undo stack. Set once the editor is fully constructed.
let editorReady = false;

// Settings keys the App Info panel owns. They never show up in the flat
// Variables list — every panel rebuild (import, snapshot restore, auto-save
// load) must filter them out or the panel gets cluttered with entries the
// user cannot meaningfully edit, plus "Custom" whose value is an object.
const SETTING_VARIABLE_KEYS = ['backgroundImage', 'fontSizes', 'buttonColor', 'labelColor', 'fonts',
  'inputColor', 'fullscreen', 'screenWidth', 'screenHeight', 'tools_path',
  'fileExplorerRoot', 'weatherEnabled', 'weatherUnit', 'tspUsername',
  'playbackResolution', 'reducedMotion', 'swapABButtons', 'lowPower', 'Custom'];

function isSettingVariableKey(key) {
  return SETTING_VARIABLE_KEYS.includes(key);
}

// Drop the settings keys the dedicated controls manage from a restored flat
// variables map, keeping their round-trip home in importedConfig untouched.
// "Custom" stays: it is the working copy the effective-value lookups read.
function scrubSettingVariables(map) {
  if (!map || typeof map !== 'object') return map;
  for (const key of Object.keys(map)) {
    if (isSettingVariableKey(key) && key !== 'Custom') delete map[key];
  }
  if (typeof map.Custom !== 'object' || map.Custom === null) map.Custom = {};
  return map;
}

// One shared variable-row builder so the list looks identical whichever path
// filled it. Object values render as compact JSON instead of "[object Object]".
function appendVariableItem(key, val) {
  variablesList.appendChild(createVariableItem(key, val));
}

function renderVariablePanelValue(val) {
  if (val === undefined || val === null) return '';
  if (typeof val === 'object') {
    const json = JSON.stringify(val);
    return json.length > 80 ? json.slice(0, 77) + '…' : json;
  }
  return String(val);
}

// Initialize the editor
document.addEventListener('DOMContentLoaded', () => {
  // Set initial canvas size
  updateCanvasSize();

  // Add initial scene
  const option = document.createElement('option');
  option.value = 'Scene 1';
  option.textContent = 'Scene 1';
  sceneSelector.appendChild(option);

  // Add initial menu
  addElement('menu', 0, canvasHeight - 50);

  // Set up event listeners
  setupEventListeners();
  setupAutoSaveTracking();

  // Initialize properties panel as expanded
  updateSceneChangeSelector();

  // Update variable change selector
  updateVariableChangeSelector();

  // Set active tab to Element Properties by default
  switchTab('app-properties');

  // Set up font size change listeners
  setupFontSizeListeners();

  // Create global tooltip
  createGlobalTooltip();

  // Load saved theme preference
  loadTheme();

  // Update readouts
  updateCanvasReadout();

  // Always start from the shipped player/jukaconfig.json so the website matches
  // the app; fall back to the auto-save only if the config cannot be loaded.
  loadDefaultConfig(() => { loadAutoSave(); });

  // Fit the device once the layout (and the measured footer height) settles.
  fitCanvasToViewport();
  requestAnimationFrame(fitCanvasToViewport);

  // New features
  setupExportDropdowns();
  setupContextMenu();
  setupKeyboardShortcuts();
  setupAiPanelToggle();
  setupFooterHeightTracking();

  // Preview toggle button
  const previewBtn = document.getElementById('previewToggle');
  if (previewBtn) previewBtn.addEventListener('click', onPreviewToggleButton);

  const reloadBtn = document.getElementById('reloadConfigBtn');
  if (reloadBtn) reloadBtn.addEventListener('click', reloadImportedConfig);
  refreshReloadButtonState();

  // Theme/layout sharing: a design can be handed to someone else as a single
  // link, then opened here as a fresh copy with the Remix button.
  setupDesignSharing();
  restoreSharedDesignFromURL();

  // History controls, design check, preview chrome, keyboard editing.
  setupEditorQuality();
  setupPreviewInteractions();
  setupConfigDrop();
  editorReady = true;
  refreshHistoryControls();
  scheduleValidationRefresh();

  setupMobileElementAdding();
  setupMobileCanvasClick();
  setupMobileElementSelection();
});

// Create global tooltip element
function createGlobalTooltip() {
  globalTooltip = document.createElement('div');
  globalTooltip.className = 'variable-tooltip';
  globalTooltip.style.display = 'none';
  document.body.appendChild(globalTooltip);
}

// ---

const undoStack = [];
const redoStack = [];
const MAX_UNDO = 60;

// The complete editor state an undo step must restore. Scenes alone were not
// enough: canvas size, background, font sizes and the imported metadata were
// left behind, so undoing an edit could bring back elements on the wrong
// canvas. Kept as one function so every history path captures the same shape.
function captureWorkspace(label) {
  // The live canvas is the source of truth while editing; scenes[] only catches
  // up when something saves them. Capture without this saved a stale snapshot of
  // the element positions a drag had just changed.
  saveCurrentScene();
  return {
    label: label || 'edit',
    scenes: JSON.parse(JSON.stringify(
      Object.fromEntries(Object.entries(scenes).map(([k, v]) => [k, v.map(el => el.outerHTML)]))
    )),
    currentScene,
    variables: JSON.parse(JSON.stringify(variables)),
    title: document.getElementById('title')?.value || '',
    author: document.getElementById('author')?.value || '',
    description: document.getElementById('description')?.value || '',
    canvasWidth,
    canvasHeight,
    backgroundPath,
    fontSizes: {
      title: document.getElementById('titleSize')?.value ?? '',
      big: document.getElementById('bigSize')?.value ?? '',
      medium: document.getElementById('mediumSize')?.value ?? '',
      small: document.getElementById('smallSize')?.value ?? ''
    },
    importedConfig: JSON.parse(JSON.stringify(importedConfig))
  };
}

function pushUndo(label) {
  saveCurrentScene();
  undoStack.push(captureWorkspace(label));
  if (undoStack.length > MAX_UNDO) undoStack.shift();
  redoStack.length = 0;
  refreshHistoryControls();
}

// Commit a snapshot taken before a continuous gesture (drag, resize, typing in
// an inspector field). Only records a step when the state actually changed, so
// a click that never moved an element does not fill the history with no-ops.
function commitUndoSnapshot(before) {
  if (!before) return;
  const now = captureWorkspace(before.label);
  const comparable = state => JSON.stringify(Object.assign({}, state, { label: '' }));
  if (comparable(now) === comparable(before)) return;
  undoStack.push(before);
  if (undoStack.length > MAX_UNDO) undoStack.shift();
  redoStack.length = 0;
  refreshHistoryControls();
  scheduleValidationRefresh();
}

function refreshHistoryControls() {
  const undoBtn = document.getElementById('undoBtn');
  const redoBtn = document.getElementById('redoBtn');
  if (undoBtn) {
    undoBtn.disabled = undoStack.length === 0;
    const label = undoStack.length ? undoStack[undoStack.length - 1].label : '';
    undoBtn.title = label ? `Undo ${label} (Ctrl+Z)` : 'Nothing to undo (Ctrl+Z)';
    undoBtn.setAttribute('aria-label', label ? `Undo ${label}` : 'Undo');
  }
  if (redoBtn) {
    redoBtn.disabled = redoStack.length === 0;
    const label = redoStack.length ? redoStack[redoStack.length - 1].label : '';
    redoBtn.title = label ? `Redo ${label} (Ctrl+Shift+Z)` : 'Nothing to redo (Ctrl+Shift+Z)';
    redoBtn.setAttribute('aria-label', label ? `Redo ${label}` : 'Redo');
  }
}

// One place that drops the selection: the element reference, the highlight, the
// inspector mode and the "nothing selected" hint. Deleting, undoing and loading
// each used to clear only part of it, which left the inspector editing an
// element that was no longer on the canvas.
function clearSelectionState() {
  currentElement = null;
  document.querySelectorAll('.element.selected').forEach(el => el.classList.remove('selected'));
  document.body.classList.remove('element-selected');
  const noSelection = document.getElementById('noSelection');
  if (noSelection) noSelection.style.display = '';
  switchTab('app-properties');
}

// Snapshots and autosaves are serialised from the live DOM, so a selected
// element is saved carrying its "selected" class. Restoring that as-is shows a
// highlighted element the editor does not consider selected, and Escape cannot
// clear it.
function parseSavedElement(html) {
  const tmp = document.createElement('div');
  tmp.innerHTML = html;
  const node = tmp.firstChild;
  if (node && node.classList) node.classList.remove('selected');
  return node;
}

function restoreSnapshot(snapshot) {
  // Restore scenes from HTML strings
  scenes = {};
  const sceneSelectorEl = document.getElementById('sceneSelector');
  sceneSelectorEl.innerHTML = '';

  for (const [name, htmlArr] of Object.entries(snapshot.scenes)) {
    scenes[name] = htmlArr.map(parseSavedElement);
    const option = document.createElement('option');
    option.value = name;
    option.textContent = name;
    sceneSelectorEl.appendChild(option);
  }

  currentScene = snapshot.currentScene || Object.keys(scenes)[0] || 'Scene 1';
  sceneSelectorEl.value = currentScene;
  variables = JSON.parse(JSON.stringify(snapshot.variables || {}));
  document.getElementById('title').value = snapshot.title || '';
  document.getElementById('author').value = snapshot.author || '';
  document.getElementById('description').value = snapshot.description || '';

  // Canvas geometry, background and font sizes are part of the design too.
  canvasWidth = Number(snapshot.canvasWidth) || 1280;
  canvasHeight = Number(snapshot.canvasHeight) || 720;
  canvas.style.width = `${canvasWidth}px`;
  canvas.style.height = `${canvasHeight}px`;
  backgroundPath = snapshot.backgroundPath || '';
  canvas.style.backgroundImage = backgroundPath ? `url(${backgroundPath})` : '';
  canvas.style.backgroundSize = backgroundPath ? 'cover' : '';
  syncCanvasSizeControls();
  const sizes = snapshot.fontSizes || {};
  ['title', 'big', 'medium', 'small'].forEach(role => {
    const input = document.getElementById(role + 'Size');
    if (input && sizes[role] !== undefined && sizes[role] !== '') input.value = sizes[role];
  });
  updateAllFontSizes();
  importedConfig = snapshot.importedConfig || { top: {}, variables: {}, scenes: {} };
  currentElement = null;
  document.querySelectorAll('.element').forEach(el => el.classList.remove('selected'));
  document.body.classList.remove('element-selected');
  const noSelection = document.getElementById('noSelection');
  if (noSelection) noSelection.style.display = '';

  // Rebuild variables UI
  variablesList.innerHTML = '';
  scrubSettingVariables(variables);
  for (const [key, val] of Object.entries(variables)) {
    if (isSettingVariableKey(key)) continue;
    appendVariableItem(key, val);
  }

  loadScene(currentScene);
  updateSceneBadge();
  updateSceneChangeSelector();
  updateVariableChangeSelector();
  updateAllMenuSceneButtons();
  updateAllStoredMenus();
  updateCanvasReadout();
  refreshHistoryControls();
  scheduleValidationRefresh();
}

function undo() {
  if (undoStack.length === 0) return;
  saveCurrentScene();
  redoStack.push(captureWorkspace(undoStack[undoStack.length - 1].label || 'edit'));
  const snapshot = undoStack.pop();
  restoreSnapshot(snapshot);
  showToast('Undone: ' + (snapshot.label || 'action'), 'info');
}

function redo() {
  if (redoStack.length === 0) return;
  saveCurrentScene();
  const snapshot = redoStack.pop();
  undoStack.push(captureWorkspace(snapshot.label || 'edit'));
  restoreSnapshot(snapshot);
  showToast('Redone: ' + (snapshot.label || 'action'), 'info');
}

// ---

function setupExportDropdowns() {
  const pairs = [
    { btn: 'headerExportBtn', dd: 'headerExportDropdown' },
    { btn: 'canvasExportBtn', dd: 'canvasExportDropdown' }
  ];
  pairs.forEach(({ btn, dd }) => {
    const btnEl = document.getElementById(btn);
    const ddEl = document.getElementById(dd);
    if (!btnEl || !ddEl) return;
    btnEl.addEventListener('click', (e) => {
      e.stopPropagation();
      // Close other dropdown
      pairs.forEach(({ dd: otherDd }) => {
        const el = document.getElementById(otherDd);
        if (el && el !== ddEl) el.classList.remove('open');
      });
      ddEl.classList.toggle('open');
    });
  });
  // Close dropdowns when clicking outside
  document.addEventListener('click', () => {
    document.querySelectorAll('.export-dropdown').forEach(dd => dd.classList.remove('open'));
  });
}

// ---

let contextMenuTarget = null;

function setupContextMenu() {
  const menu = document.getElementById('contextMenu');
  if (!menu) return;

  canvas.addEventListener('contextmenu', (e) => {
    const el = e.target.closest('.element');
    if (!el || el.classList.contains('menu')) return;
    e.preventDefault();
    contextMenuTarget = el;

    // Position the menu
    const x = Math.min(e.clientX, window.innerWidth - 200);
    const y = Math.min(e.clientY, window.innerHeight - 240);
    menu.style.left = x + 'px';
    menu.style.top = y + 'px';
    menu.style.display = 'flex';
    menu.style.flexDirection = 'column';
  });

  document.addEventListener('click', () => {
    menu.style.display = 'none';
    contextMenuTarget = null;
  });

  menu.querySelectorAll('button[data-action]').forEach(btn => {
    btn.addEventListener('click', () => {
      if (!contextMenuTarget) return;
      const action = btn.getAttribute('data-action');
      pushUndo(action);

      if (action === 'duplicate') {
        duplicateElement(contextMenuTarget);
      } else if (action === 'delete') {
        contextMenuTarget.remove();
        const sceneElements = scenes[currentScene];
        const idx = sceneElements.findIndex(item => item.isEqualNode(contextMenuTarget));
        if (idx > -1) sceneElements.splice(idx, 1);
        currentElement = null;
        showToast('Element deleted', 'info');
      } else if (action === 'bring-front') {
        contextMenuTarget.style.zIndex = '50';
        showToast('Brought to front', 'info');
      } else if (action === 'send-back') {
        contextMenuTarget.style.zIndex = '0';
        showToast('Sent to back', 'info');
      } else if (action === 'edit-text') {
        const type = contextMenuTarget.getAttribute('data-type');
        if (['button', 'label'].includes(type)) {
          const textSpan = contextMenuTarget.querySelector('.text-content');
          const newText = prompt('Edit text:', textSpan?.textContent || '');
          if (newText !== null && textSpan) textSpan.textContent = newText;
        }
      }
      menu.style.display = 'none';
      contextMenuTarget = null;
    });
  });
}

function duplicateElement(el) {
  const clone = el.cloneNode(true);
  clone.style.left = (parseInt(el.getAttribute('data-x')) + 20) + 'px';
  clone.style.top = (parseInt(el.getAttribute('data-y')) + 20) + 'px';
  clone.setAttribute('data-x', parseInt(el.getAttribute('data-x')) + 20);
  clone.setAttribute('data-y', parseInt(el.getAttribute('data-y')) + 20);
  canvas.appendChild(clone);
  setupElementEvents(clone);
  if (!scenes[currentScene]) scenes[currentScene] = [];
  scenes[currentScene].push(clone);
  showToast('Element duplicated', 'success');
}

// ---

let autoSaveTimer = null;

function persistAutoSave() {
  autoSaveTimer = null;
  saveCurrentScene();
  const data = {
    scenes: Object.fromEntries(
      Object.entries(scenes).map(([k, v]) => [k, v.map(el => el.outerHTML)])
    ),
    currentScene,
    variables,
    canvasWidth,
    canvasHeight,
    backgroundPath,
    title: document.getElementById('title')?.value || '',
    author: document.getElementById('author')?.value || '',
    description: document.getElementById('description')?.value || '',
    titleSize: titleSizeInput?.value || 48,
    bigSize: bigSizeInput?.value || 36,
    mediumSize: mediumSizeInput?.value || 24,
    smallSize: smallSizeInput?.value || 18,
    // Imported metadata (theme colours, font paths, player settings) has to
    // survive a reload too, otherwise $labelColor loses its source and the
    // preview cannot match the device after the first autosave.
    importedConfig: JSON.parse(JSON.stringify(importedConfig))
  };
  try {
    localStorage.setItem('jukahub-autosave', JSON.stringify(data));
  } catch (e) { /* quota exceeded */ }
}

function scheduleAutoSave() {
  if (autoSaveTimer) clearTimeout(autoSaveTimer);
  autoSaveTimer = setTimeout(persistAutoSave, 500);
  // The live builder listens for edits so it can push them to a paired device.
  // It debounces separately and is a no-op unless auto-push is enabled.
  if (window.JukaLiveBuilder && typeof window.JukaLiveBuilder.noteEdit === 'function') {
    window.JukaLiveBuilder.noteEdit();
  }
}

function flushAutoSave() {
  if (autoSaveTimer) {
    clearTimeout(autoSaveTimer);
    persistAutoSave();
  }
}

function setupAutoSaveTracking() {
  const scheduleForEditorChange = event => {
    if (event.target.closest('#aiPanel') || event.target.id === 'loadFile') return;
    scheduleAutoSave();
  };
  document.addEventListener('input', scheduleForEditorChange);
  document.addEventListener('change', scheduleForEditorChange);

  if (typeof MutationObserver === 'function') {
    const observer = new MutationObserver(scheduleAutoSave);
    observer.observe(canvas, { subtree: true, childList: true, characterData: true, attributes: true });
    observer.observe(variablesList, { subtree: true, childList: true, characterData: true });
  }

  window.addEventListener('pagehide', flushAutoSave);
}

function loadAutoSave() {
  try {
    const raw = localStorage.getItem('jukahub-autosave');
    if (!raw) return false;
    const data = JSON.parse(raw);
    if (!data.scenes) return false;

    // Restore scenes
    scenes = {};
    const sceneSelectorEl = document.getElementById('sceneSelector');
    sceneSelectorEl.innerHTML = '';

    for (const [name, htmlArr] of Object.entries(data.scenes)) {
      scenes[name] = htmlArr.map(parseSavedElement);
      const option = document.createElement('option');
      option.value = name;
      option.textContent = name;
      sceneSelectorEl.appendChild(option);
    }

    currentScene = data.currentScene || Object.keys(scenes)[0];
    sceneSelectorEl.value = currentScene;
    variables = data.variables || {};
    importedConfig = data.importedConfig || { top: {}, variables: {}, scenes: {} };
    canvasWidth = data.canvasWidth || 1280;
    canvasHeight = data.canvasHeight || 720;
    backgroundPath = data.backgroundPath || '';

    document.getElementById('title').value = data.title || '';
    document.getElementById('author').value = data.author || '';
    document.getElementById('description').value = data.description || '';
    if (data.titleSize) titleSizeInput.value = data.titleSize;
    if (data.bigSize) bigSizeInput.value = data.bigSize;
    if (data.mediumSize) mediumSizeInput.value = data.mediumSize;
    if (data.smallSize) smallSizeInput.value = data.smallSize;

    if (backgroundPath) {
      canvas.style.backgroundImage = `url(${backgroundPath})`;
      canvas.style.backgroundSize = 'cover';
    }

    syncCanvasSizeControls();
    updateCanvasSize();
    loadScene(currentScene);

    // Rebuild variables UI
    variablesList.innerHTML = '';
    scrubSettingVariables(variables);
    for (const [key, val] of Object.entries(variables)) {
      if (isSettingVariableKey(key)) continue;
      appendVariableItem(key, val);
    }

    updateSceneBadge();
    updateSceneChangeSelector();
    updateVariableChangeSelector();
    updateAllMenuSceneButtons();
    updateAllStoredMenus();
    return true;
  } catch (e) {
    console.error('Failed to load auto-save:', e);
    return false;
  }
}

// ---

// Preview mode is a read-only pass over the current workspace. It hides the
// editor chrome (sidebars, header actions, footer, inspector) and disables
// element selection, but the canvas keeps painting the same scenes/variables so
// the design can be reviewed without switching apps. There is always a visible
// way out: the same eye/pen button now turns into an "Exit preview" control
// while preview is active, plus Esc and Ctrl+P also toggle back.
let previewMode = false;

function setPreviewButtonLabel(active) {
  const btn = document.getElementById('previewToggle');
  if (!btn) return;
  btn.classList.toggle('active', active);
  btn.setAttribute('aria-pressed', String(active));
  btn.setAttribute('aria-label', active ? 'Exit preview and return to editor' : 'Preview');
  btn.setAttribute('title', active ? 'Exit preview (Esc)' : 'Preview Mode (Ctrl+P)');
  btn.innerHTML = active
    ? '<i class="fas fa-times" aria-hidden="true"></i><span>Exit preview</span>'
    : '<i class="fas fa-eye" aria-hidden="true"></i><span>Preview</span>';
}

function togglePreviewMode() {
  if (previewMode) {
    exitPreviewMode();
    return;
  }
  enterPreviewMode();
}

// Preview state: a resolved copy of the variables, plus the scene the user was
// editing so exiting returns them where they started. The runtime substitutes
// $variables at draw time; the preview does the same instead of showing the
// literal "$title" the editor uses for reference.
let previewVariables = {};
let previewOriginScene = null;
let previewScale = 1;

// Canvas zoom. The device is 1280x720 while the middle pane is usually around
// 800px wide, so the editor opens fitted to the window. Every pointer delta is
// divided by this scale before it becomes a canvas coordinate.
let canvasScale = 1;
let zoomIsManual = false;
const CANVAS_MIN_SCALE = 0.25;
const CANVAS_MAX_SCALE = 2;

// Mirrors Variables.Get in player/config.go: custom variables first, then the
// known colour names, whose RGB objects the runtime formats as "r,g,b".
function previewVariableValue(name) {
  let custom;
  if (previewMode && previewVariables[name] !== undefined) {
    custom = previewVariables[name];
  } else {
    // Edit-mode resolution follows the player too: Custom first
    // (case-insensitive, strings win), then the panel's own variables,
    // then the imported top-level fields.
    custom = customValueJs(flattenedConfigCustom(), name);
    if (custom === undefined && variables && variables[name] !== undefined) custom = variables[name];
    if (custom === undefined) custom = (importedConfig.variables || {})[name];
  }
  if (custom === undefined || custom === null) return '';
  if (typeof custom === 'object') {
    if ('r' in custom && 'g' in custom && 'b' in custom) return `${custom.r},${custom.g},${custom.b}`;
    return '';
  }
  return String(custom);
}

// The three theme slots resolve through the active theme preset in the player
// (player/themes.go: effectiveThemeColor), where zero RGB means "never chosen",
// not black. This is the same colour set the player ships: a config that picks
// "oled" or "sakura" must preview on the website with the exact palette the
// device renders, so keep this table in sync with themePresets in
// player/themes.go (background, buttonColor, labelColor, inputColor).
const PLAYER_THEME_PRESETS = {
  dark: { background: '#0f1115', buttonColor: '#1c2230', labelColor: '#e8eefc', inputColor: '#141a26' },
  light: { background: '#f0f2f5', buttonColor: '#ffffff', labelColor: '#1a1d26', inputColor: '#e8eaef' },
  oled: { background: '#000000', buttonColor: '#0a0a0a', labelColor: '#ffffff', inputColor: '#050505' },
  midnight: { background: '#090b14', buttonColor: '#151927', labelColor: '#f5f7ff', inputColor: '#0f1320' },
  hub: { background: '#090E19', buttonColor: '#1E293B', labelColor: '#F6F8FC', inputColor: '#0F1420' },
  ocean: { background: '#0B1526', buttonColor: '#142740', labelColor: '#E8F1FF', inputColor: '#0E1C31' },
  sunset: { background: '#1A0F14', buttonColor: '#2B1A24', labelColor: '#FFF0F3', inputColor: '#201219' },
  highcontrast: { background: '#000000', buttonColor: '#000000', labelColor: '#ffffff', inputColor: '#000000' },
  'cb-deuteranopia': { background: '#0d1117', buttonColor: '#1b2534', labelColor: '#f2f6ff', inputColor: '#131a24' },
  'cb-protanopia': { background: '#0e1116', buttonColor: '#1c2431', labelColor: '#f6f5ec', inputColor: '#141a22' },
  'cb-tritanopia': { background: '#101316', buttonColor: '#1f242a', labelColor: '#f4f6f5', inputColor: '#15191d' },
  red: { background: '#140708', buttonColor: '#241014', labelColor: '#ffecec', inputColor: '#1b0a0c' },
  green: { background: '#07120c', buttonColor: '#0e2418', labelColor: '#e9fff1', inputColor: '#0a1a11' },
  blue: { background: '#071426', buttonColor: '#0f2542', labelColor: '#eaf4ff', inputColor: '#0a1c33' },
  anime: { background: '#12091f', buttonColor: '#2e1d52', labelColor: '#f7ecff', inputColor: '#1a1030' },
  cute: { background: '#fff7fb', buttonColor: '#ffd6e8', labelColor: '#3d2230', inputColor: '#fff0f7' },
  girly: { background: '#fff0f7', buttonColor: '#ffc7df', labelColor: '#3a1226', inputColor: '#ffe6f2' },
  sakura: { background: '#fff6f4', buttonColor: '#ffdcd6', labelColor: '#3b2523', inputColor: '#ffefec' },
  lavender: { background: '#f4f1fb', buttonColor: '#e3daf7', labelColor: '#241a3a', inputColor: '#efe9fa' },
  sand: { background: '#f6efe4', buttonColor: '#e6d6bd', labelColor: '#2f2618', inputColor: '#f0e5d3' },
  nord: { background: '#2e3440', buttonColor: '#434c5e', labelColor: '#eceff4', inputColor: '#3b4252' },
  gruvbox: { background: '#282828', buttonColor: '#3c3836', labelColor: '#fbf1c7', inputColor: '#32302f' },
  dracula: { background: '#282a36', buttonColor: '#44475a', labelColor: '#f8f8f2', inputColor: '#21222c' },
  matrix: { background: '#000600', buttonColor: '#06240c', labelColor: '#b6ffb6', inputColor: '#021004' },
  fallout: { background: '#0d0f07', buttonColor: '#242a12', labelColor: '#ffd27a', inputColor: '#141709' },
  cyberpunk: { background: '#0a0416', buttonColor: '#26164a', labelColor: '#f5f0ff', inputColor: '#120a24' },
  mono: { background: '#101010', buttonColor: '#282828', labelColor: '#f2f2f2', inputColor: '#161616' },
  vaporwave: { background: '#140b26', buttonColor: '#371d66', labelColor: '#ffe9ff', inputColor: '#1e1038' }
};
// Kept as an alias: both the preview theme resolver and the config's effective
// theme colours read this table.
const PREVIEW_THEME_PRESETS = PLAYER_THEME_PRESETS;

// --- Effective config values (the player's resolution order) -----------------
// The player never reads settings straight out of variables.Custom: it first
// flattens nested Custom blocks, then looks a key up case-insensitively with
// string values winning (config.go customValue), and only then falls back to
// the typed fields. Mirroring that order here is what keeps a jukaconfig.json
//"reading" the same on the website as it does on the device.

// Mirror of player/core.go flattenCustomBlocks: nested "Custom" blocks merge
// into their parent one level at a time, and an empty Custom disappears.
function flattenCustomBlocksJs(custom) {
  if (!custom || typeof custom !== 'object' || Array.isArray(custom)) return custom || {};
  const copy = JSON.parse(JSON.stringify(custom));
  for (let depth = 0; depth < 4; depth++) {
    const nested = copy.Custom;
    if (!nested || typeof nested !== 'object' || Array.isArray(nested)) {
      if (typeof nested === 'string' || nested === undefined) delete copy.Custom;
      break;
    }
    for (const key of Object.keys(nested)) {
      if (!(key in copy)) copy[key] = nested[key];
    }
    delete copy.Custom;
  }
  return copy;
}

// Mirror of player/core.go customValue: exact key first, then case-insensitive
// with a string value winning over a same-named non-string.
function customValueJs(custom, name) {
  if (!custom || typeof custom !== 'object') return undefined;
  if (name in custom) return custom[name];
  let fallback;
  for (const key of Object.keys(custom)) {
    if (key.toLowerCase() === String(name).toLowerCase()) {
      if (typeof custom[key] === 'string') return custom[key];
      if (fallback === undefined) fallback = custom[key];
    }
  }
  return fallback;
}

// The flattened, case-normalised view of a config's variables.Custom.
function flattenedConfigCustom() {
  const imported = importedConfig.variables || {};
  const custom = customValueJs(imported, 'Custom');
  const merged = (typeof custom === 'object' && custom && !Array.isArray(custom) ? JSON.parse(JSON.stringify(custom)) : {});
  // The panel's own Custom copy (colour picker write-backs) wins over the raw
  // import, exactly like a user change should overwrite the stored value.
  const panelCustom = (variables && typeof variables.Custom === 'object' && variables.Custom) ? variables.Custom : {};
  for (const key of Object.keys(panelCustom)) merged[key] = panelCustom[key];
  return flattenCustomBlocksJs(merged);
}

// The theme slot colours a config actually resolves to on the player, in the
// same order effectiveThemeColor uses: an explicit string in Custom, then the
// typed RGB struct (zero channels mean "unset"), then the active preset.
function effectiveThemeColour(name) {
  const key = String(name || '').toLowerCase();
  if (!['buttoncolor', 'labelcolor', 'inputcolor'].includes(key)) return '';
  const custom = flattenedConfigCustom();
  const explicit = customValueJs(custom, key);
  if (typeof explicit === 'string' && colourIsCss(explicit)) return explicit;
  const importedVars = importedConfig.variables || {};
  const rgb = importedVars[camelCaseThemeKey(key)];
  if (rgb && typeof rgb === 'object' && (rgb.r || rgb.g || rgb.b)) {
    return `rgb(${rgb.r},${rgb.g},${rgb.b})`;
  }
  const presetName = String(customValueJs(custom, 'theme_preset') || 'dark').toLowerCase();
  const preset = PLAYER_THEME_PRESETS[presetName] || PLAYER_THEME_PRESETS.dark;
  return preset[key === 'buttoncolor' ? 'buttonColor' : key === 'labelcolor' ? 'labelColor' : 'inputColor'] || '';
}

function camelCaseThemeKey(key) {
  return key === 'buttoncolor' ? 'buttonColor' : key === 'labelcolor' ? 'labelColor' : key === 'inputcolor' ? 'inputColor' : key;
}

// Effective value for the settings the player keeps as typed fields with
// Custom overrides (screen size, weather, paths, ...): the authoritative
// lookup is Custom (case-insensitive), like the player's syncVariableOverrides.
function effectiveConfigValue(name) {
  const typedCasing = String(name || '');
  const importedVars = importedConfig.variables || {};
  const custom = flattenedConfigCustom();
  const customHit = customValueJs(custom, name);
  if (customHit !== undefined && (typeof customHit === 'string' ? customHit.trim() !== '' : true)) return customHit;
  if (variables && variables[name] !== undefined) return variables[name];
  if (importedVars[name] !== undefined) return importedVars[name];
  return importedVars[camelCaseThemeKey(typedCasing)];
}

function previewThemeColour(name) {
  const key = String(name || '').toLowerCase();
  const camel = key === 'buttoncolor' ? 'buttonColor'
    : key === 'labelcolor' ? 'labelColor'
    : key === 'inputcolor' ? 'inputColor' : key;
  // Preview's set_variable writes go first so a colour variable set at runtime
  // recolours live; then the flattened Custom view, where the panel's own
  // Custom copy (colour-picker write-backs) wins over the raw import.
  if (previewMode && previewVariables[name] !== undefined) {
    const set = String(previewVariables[name]);
    if (colourIsCss(set)) return set;
  }
  const importedVars = importedConfig.variables || {};
  const custom = flattenedConfigCustom();
  const explicit = customValueJs(custom, key) ?? customValueJs(custom, camel);
  if (typeof explicit === 'string' && colourIsCss(explicit)) return explicit;
  const rgb = importedVars[camel] || importedVars[name];
  if (rgb && typeof rgb === 'object' && (rgb.r || rgb.g || rgb.b)) {
    return `rgb(${rgb.r},${rgb.g},${rgb.b})`;
  }
  // Unset theme slots resolve like the player: through the active preset.
  const presetName = String(customValueJs(custom, 'theme_preset') || 'dark').toLowerCase();
  const preset = PREVIEW_THEME_PRESETS[presetName] || PREVIEW_THEME_PRESETS.dark;
  return preset[camel] || '';
}

function previewColourValue(name) {
  const key = String(name || '').toLowerCase();
  if (key === 'buttoncolor' || key === 'labelcolor' || key === 'inputcolor') {
    return previewThemeColour(key);
  }
  const value = previewVariableValue(name).trim();
  if (!value) return '';
  if (/^\d{1,3}\s*,\s*\d{1,3}\s*,\s*\d{1,3}$/.test(value)) return `rgb(${value})`;
  return value;
}

function resolvePreviewText(text) {
  return String(text || '').replace(/\$([a-zA-Z_][a-zA-Z0-9_]*)/g, (whole, name) => previewVariableValue(name));
}

function applyPreviewVariables() {
  canvas.querySelectorAll('.element').forEach(el => {
    const span = el.querySelector('.text-content');
    if (span) {
      if (el.dataset.previewSourceText === undefined) {
        el.dataset.previewSourceText = span.textContent;
      }
      span.textContent = resolvePreviewText(el.dataset.previewSourceText);
      el.setAttribute('aria-label', `${el.getAttribute('data-type') || 'element'}: ${span.textContent || 'no text'}`);
    }

    // Colours are variable references too. The player resolves them at draw
    // time, so the preview has to, or every tile renders as unstyled text.
    const colourAttrs = [['data-color', 'color'], ['data-bg-color', 'background']];
    colourAttrs.forEach(([attr, styleProp]) => {
      const raw = el.getAttribute(attr);
      if (!raw || !raw.trim().startsWith('$')) return;
      const resolved = previewColourValue(raw.trim().slice(1));
      if (resolved) el.style[styleProp] = resolved;
    });
  });
}

function previewSceneNames() {
  return Object.keys(scenes);
}

function previewSceneIndex() {
  const names = previewSceneNames();
  const index = names.indexOf(currentScene);
  return index < 0 ? 0 : index;
}

// Room the device can occupy inside the canvas pane, excluding its padding.
function canvasAvailableSize() {
  const host = document.getElementById('canvasWrapper');
  if (!host) return null;
  const hostStyle = window.getComputedStyle(host);
  const padX = parseFloat(hostStyle.paddingLeft) + parseFloat(hostStyle.paddingRight);
  const padY = parseFloat(hostStyle.paddingTop) + parseFloat(hostStyle.paddingBottom);
  return {
    width: Math.max(120, host.clientWidth - padX - 2),
    height: Math.max(90, host.clientHeight - padY - 2),
  };
}

function applyCanvasScale(scale) {
  canvasScale = Math.max(CANVAS_MIN_SCALE, Math.min(CANVAS_MAX_SCALE, scale));
  canvas.style.transform = canvasScale === 1 ? '' : `scale(${canvasScale})`;
  // The scaler keeps scrollbars aligned with what is actually on screen.
  const scaler = document.getElementById('canvasScaler');
  if (scaler) {
    scaler.style.width = `${canvasWidth * canvasScale}px`;
    scaler.style.height = `${canvasHeight * canvasScale}px`;
  }
  const percent = `${Math.round(canvasScale * 100)}%`;
  const readout = document.getElementById('zoomReadout');
  if (readout) readout.textContent = percent;
  const fitBtn = document.getElementById('zoomFitBtn');
  if (fitBtn) fitBtn.title = `Canvas at ${percent} — click to fit the device to the window`;
}

function fitCanvasToViewport() {
  const size = canvasAvailableSize();
  if (!size) return;
  zoomIsManual = false;
  applyCanvasScale(Math.min(1, size.width / canvasWidth, size.height / canvasHeight));
}

function zoomCanvas(factor) {
  zoomIsManual = true;
  applyCanvasScale(canvasScale * factor);
}

function fitPreviewCanvas() {
  if (!previewMode) return;
  const size = canvasAvailableSize();
  if (!size) return;
  previewScale = Math.min(1, size.width / canvasWidth, size.height / canvasHeight);
  applyCanvasScale(previewScale);
  const readout = document.getElementById('previewScale');
  if (readout) readout.textContent = `${Math.round(previewScale * 100)}%`;
}

function updatePreviewBar() {
  const names = previewSceneNames();
  const index = previewSceneIndex();
  const label = document.getElementById('previewSceneLabel');
  if (label) label.textContent = `${currentScene} · ${index + 1} / ${names.length}`;
  const device = document.getElementById('previewDevice');
  if (device) device.textContent = `${canvasWidth} × ${canvasHeight}`;
  const prev = document.getElementById('previewPrev');
  const next = document.getElementById('previewNext');
  if (prev) prev.disabled = index <= 0;
  if (next) next.disabled = index >= names.length - 1;
  const readout = document.getElementById('previewScale');
  if (readout) readout.textContent = `${Math.round(previewScale * 100)}%`;
}

function previewGoTo(sceneName) {
  if (!previewMode || !scenes[sceneName]) return;
  sceneSelector.value = sceneName;
  changeScene();
  applyPreviewVariables();
  updatePreviewBar();
  announce(`Preview: ${sceneName}.`);
}

function previewStep(delta) {
  const names = previewSceneNames();
  const next = previewSceneIndex() + delta;
  if (next < 0 || next >= names.length) return;
  previewGoTo(names[next]);
}

// --- D-pad + controller emulation (preview only) -------------------------
// The device shell: arrows move a focus ring, Enter tests “A”, Backspace plays
// “B” (goes back one scene). No focusable elements or user just wants pages:
// Shift+left/right left/right walk the scenes like the device menu did before.
let previewFocusEl = null;
const previewSceneHistory = [];

function previewFocusableElements() {
  return Array.from(canvas.querySelectorAll('.element')).filter(el => {
    const trigger = (el.getAttribute('data-trigger') || '').trim();
    const target = (el.getAttribute('data-scene-change') || '').trim();
    return trigger !== '' || target !== '';
  });
}

function previewCenter(el) {
  const rect = el.getBoundingClientRect();
  return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 };
}

// Nearest focusable in the pressed direction, renderer-style: prefer a member
// that has a clear screen-edge in the direction, tie breakers closest to center.
function previewFocusNeighbour(dir) {
  const candidates = previewFocusableElements().filter(el => el !== previewFocusEl);
  if (candidates.length === 0) return null;
  let best = null;
  let bestScore = Infinity;
  for (const el of candidates) {
    const a = previewCenter(previewFocusEl || el);
    const b = previewCenter(el);
    const dx = b.x - a.x;
    const dy = b.y - a.y;
    const axis = dir === 'ArrowLeft' ? -dx : dir === 'ArrowRight' ? dx : dir === 'ArrowUp' ? -dy : dy;
    const cross = dir === 'ArrowLeft' || dir === 'ArrowRight' ? Math.abs(dy) : Math.abs(dx);
    if (axis <= 0) continue;
    const score = cross * 1000 + axis;
    if (score < bestScore) { bestScore = score; best = el; }
  }
  return best;
}

function previewFocusSet(el) {
  if (previewFocusEl && previewFocusEl !== el) previewFocusEl.classList.remove('preview-focus');
  previewFocusEl = el || null;
  if (previewFocusEl) previewFocusEl.classList.add('preview-focus');
}

function previewFocusClear() {
  if (previewFocusEl) previewFocusEl.classList.remove('preview-focus');
  previewFocusEl = null;
}

function previewDpadMove(dir) {
  if (!previewFocusEl) {
    const first = previewFocusableElements()[0];
    if (!first) return false;
    previewFocusSet(first);
    return true;
  }
  const next = previewFocusNeighbour(dir);
  if (next) previewFocusSet(next);
  return true;
}

// Re-focus after a scene loads. The first focusable is highlighted so there is
// always something the room's visitor can press Enter on.
function previewFocusReset() {
  previewFocusClear();
  const first = previewFocusableElements()[0];
  if (first) previewFocusSet(first);
}

function previewGoBack() {
  const from = currentScene;
  while (previewSceneHistory.length > 0) {
    const target = previewSceneHistory.pop();
    if (target !== from && scenes[target]) {
      previewGoTo(target);
      return;
    }
  }
  // Nothing behind us: B behaves like an at-home press and stays put.
  showToast('No earlier scene to go back to', 'info');
}

// History bookkeeping + focus reset wrap the preview scene switch.
const basePreviewGoToForController = previewGoTo;
previewGoTo = function (sceneName) {
  if (currentScene !== sceneName) previewSceneHistory.push(currentScene);
  previewFocusClear();
  basePreviewGoToForController(sceneName);
  previewFocusReset();
};

function enterPreviewMode() {
  if (previewMode) return;
  // Snapshot the live DOM first: preview rewrites $variables in place and must
  // never write those resolved strings back into the design.
  saveCurrentScene();
  previewMode = true;
  previewOriginScene = currentScene;
  previewVariables = JSON.parse(JSON.stringify(variables));
  document.body.classList.add('preview-mode');
  setPreviewButtonLabel(true);

  // Deselect any element and drop the selection styling
  currentElement = null;
  document.querySelectorAll('.element').forEach(el => el.classList.remove('selected'));
  document.body.classList.remove('element-selected');

  applyPreviewVariables();
  previewSceneHistory.length = 0;
  const bar = document.getElementById('previewBar');
  if (bar) bar.removeAttribute('hidden');
  const toggle = document.getElementById('previewToggle');
  if (toggle) toggle.setAttribute('tabindex', '-1');
  fitPreviewCanvas();
  updatePreviewBar();
  previewFocusReset();
  requestAnimationFrame(() => { fitPreviewCanvas(); previewFocusReset(); });

  // Preserve a lightweight before-preview snapshot for diagnostics and cleanup.
  // Save immediately on entering preview so the latest workspace survives a
  // reload even if the normal debounce timer has not fired yet.
  if (autoSaveTimer) {
    clearTimeout(autoSaveTimer);
    autoSaveTimer = null;
  }
  try {
    localStorage.setItem('jukahub-autosave', JSON.stringify(snapshotWorkspace()));
  } catch (e) { /* private mode or storage quota can deny localStorage */ }

  showToast('Preview mode — click the scene menu, arrow keys, or Esc to exit', 'info');
  announce('Preview mode. Variables are resolved like the player. Press Escape to return to the editor.');
}

// The preview button remains available as the visible exit control while preview is active.
function onPreviewToggleButton() {
  if (previewMode) {
    exitPreviewMode();
  } else {
    enterPreviewMode();
  }
}

function leavePreviewMode() {
  if (!previewMode) return;
  previewMode = false;
  previewFocusClear();
  previewSceneHistory.length = 0;
  document.body.classList.remove('preview-mode');
  setPreviewButtonLabel(false);
  const bar = document.getElementById('previewBar');
  if (bar) bar.setAttribute('hidden', '');
  const toggle = document.getElementById('previewToggle');
  if (toggle) toggle.removeAttribute('tabindex');
  previewScale = 1;
  fitCanvasToViewport();

  // Return to the scene the user was editing, rebuilt from the clean snapshot
  // (which never contains resolved variable text).
  if (previewOriginScene && scenes[previewOriginScene]) {
    sceneSelector.value = previewOriginScene;
    changeScene();
  }
  previewOriginScene = null;
  previewVariables = {};
  // Restored design: repaint the $colour references with the edit-mode values.
  applyEditorVariablePaint();

  // Persist the workspace as it stands after preview, without reverting edits.
  scheduleAutoSave();
  showToast('Edit mode', 'info');
  announce('Edit mode.');
}

function exitPreviewMode() {
  leavePreviewMode();
}

// Preview interactions mirror the runtime: scene links navigate, set_variable
// updates the preview's own variable copy, everything else is a device action.
function setupPreviewInteractions() {
  if (!canvas) return;
  canvas.addEventListener('click', event => {
    if (!previewMode) return;
    const menuButton = event.target.closest('.menu-scene-button');
    if (menuButton) {
      updatePreviewBar();
      announce(`Preview: ${currentScene}.`);
      return;
    }
    const el = event.target.closest('.element');
    if (!el) return;
    const trigger = (el.getAttribute('data-trigger') || '').trim();
    const sceneTarget = (el.getAttribute('data-scene-change') ||
      (trigger.startsWith('change_scene:') ? trigger.slice('change_scene:'.length) : '')).trim();
    if (sceneTarget && scenes[sceneTarget]) {
      previewGoTo(sceneTarget);
      return;
    }
    if (trigger === 'change_scene') {
      showToast('This element has no target scene', 'warning');
      return;
    }
    if (trigger === 'set_variable') {
      const name = el.getAttribute('data-variable-change');
      if (name) {
        previewVariables[name] = el.getAttribute('data-variable-change-value') || '';
        applyPreviewVariables();
        const span = el.querySelector('.text-content');
        announce(`Set ${name} to ${previewVariables[name]}. ${span ? span.textContent : ''}`.trim());
      }
      return;
    }
    if (trigger) {
      showToast(`“${trigger.replace(/_/g, ' ')}” runs on the device`, 'info');
    }
  });

  const prev = document.getElementById('previewPrev');
  const next = document.getElementById('previewNext');
  if (prev) prev.addEventListener('click', () => previewStep(-1));
  if (next) next.addEventListener('click', () => previewStep(1));
  const exit = document.getElementById('previewExit');
  if (exit) exit.addEventListener('click', exitPreviewMode);
  window.addEventListener('resize', () => { if (previewMode) fitPreviewCanvas(); });
}

// Preview needs to re-resolve after any scene load, including menu navigation
// that calls changeScene() from inside the canvas.
const baseLoadSceneForPreview = loadScene;
loadScene = function (sceneName) {
  baseLoadSceneForPreview(sceneName);
  if (previewMode) {
    applyPreviewVariables();
    updatePreviewBar();
    fitPreviewCanvas();
    previewFocusReset();
  } else {
    // Edit mode repaints $colour references like the player draws them. Stored
    // clones carry the resolved inline styles, so restoring a scene keeps them.
    applyEditorVariablePaint();
  }
};


function snapshotWorkspace() {
  const sceneSelectorEl = document.getElementById('sceneSelector');
  if (!sceneSelectorEl) return null;
  return {
    scenes: Object.fromEntries(
      Object.entries(scenes).map(([k, v]) => [k, v.map(el => el.outerHTML)])
    ),
    currentScene,
    variables: JSON.parse(JSON.stringify(variables)),
    canvasWidth,
    canvasHeight,
    backgroundPath,
    title: document.getElementById('title')?.value || '',
    author: document.getElementById('author')?.value || '',
    description: document.getElementById('description')?.value || '',
    titleSize: titleSizeInput?.value || 48,
    bigSize: bigSizeInput?.value || 36,
    mediumSize: mediumSizeInput?.value || 24,
    smallSize: smallSizeInput?.value || 18,
    sceneSelectorValue: sceneSelectorEl.value
  };
}

// The chatbox and its launcher float over the workspace, so they have to clear
// the footer band that is pinned to the bottom of the 100dvh shell. How tall
// that band is depends on how the help links wrap, so measure it.
let footerResizeObserver = null;

function syncFooterHeight() {
  const footer = document.querySelector('.app-footer');
  if (!footer) return;
  const height = Math.round(footer.getBoundingClientRect().height);
  document.documentElement.style.setProperty('--footer-height', height + 'px');
}

function setupFooterHeightTracking() {
  syncFooterHeight();
  window.addEventListener('resize', syncFooterHeight);
  if (typeof ResizeObserver === 'function') {
    const footer = document.querySelector('.app-footer');
    if (footer && !footerResizeObserver) {
      footerResizeObserver = new ResizeObserver(syncFooterHeight);
      footerResizeObserver.observe(footer);
    }
  }
}

// AI assistant chatbox: floats over the workspace (bottom-right) and is opened
// from the app-bar button or its launcher. Closed by default so the editor keeps
// its classic three-column layout and the Inspector keeps its full height.
let aiPanelOpen = false;

function setAiPanelOpen(open) {
  const panel = document.getElementById('aiPanel');
  const btn = document.getElementById('aiToggleBtn');
  const launcher = document.getElementById('aiLauncher');
  aiPanelOpen = Boolean(open);
  document.body.classList.toggle('ai-open', aiPanelOpen);
  // The stylesheet makes [hidden] beat every author display rule, so the
  // panel's hidden attribute must track visibility itself: flipping only the
  // body class left the panel display:none forever (aria-hidden agreed).
  if (panel) {
    panel.hidden = !aiPanelOpen;
    panel.setAttribute('aria-hidden', String(!aiPanelOpen));
  }
  if (btn) {
    btn.classList.toggle('active', aiPanelOpen);
    btn.setAttribute('aria-expanded', String(aiPanelOpen));
  }
  if (launcher) launcher.setAttribute('aria-expanded', String(aiPanelOpen));
  if (!aiPanelOpen && panel) panel.classList.remove('open');
  try {
    localStorage.setItem('jukahub-ai-open', aiPanelOpen ? '1' : '0');
  } catch (e) { /* private mode can deny localStorage */ }
  if (aiPanelOpen) {
    const promptEl = document.getElementById('aiPrompt');
    if (promptEl) setTimeout(() => promptEl.focus(), 30);
  }
}

function setupAiPanelToggle() {
  const btn = document.getElementById('aiToggleBtn');
  const launcher = document.getElementById('aiLauncher');
  const closeBtn = document.getElementById('aiPanelToggle');
  const panel = document.getElementById('aiPanel');
  if (!panel || (!btn && !launcher)) return;

  let stored = false;
  try { stored = localStorage.getItem('jukahub-ai-open') === '1'; } catch (e) { /* ignore */ }
  setAiPanelOpen(stored);

  if (btn) btn.addEventListener('click', () => setAiPanelOpen(!aiPanelOpen));
  if (launcher) launcher.addEventListener('click', () => setAiPanelOpen(true));
  if (closeBtn) closeBtn.addEventListener('click', () => setAiPanelOpen(false));
}

// ---

function setupKeyboardShortcuts() {
  document.addEventListener('keydown', (e) => {
    // Skip if inside an input/textarea/select
    const tag = e.target.tagName;
    const isInput = tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || e.target.isContentEditable;

    // Ctrl/Cmd + Z = Undo (inside a text field the browser's own text undo wins)
    if ((e.ctrlKey || e.metaKey) && !e.shiftKey && e.key === 'z') {
      if (isInput) return;
      e.preventDefault();
      undo();
      return;
    }

    // Ctrl/Cmd + Shift + Z = Redo
    if ((e.ctrlKey || e.metaKey) && e.shiftKey && e.key === 'z') {
      if (isInput) return;
      e.preventDefault();
      redo();
      return;
    }

    // Ctrl/Cmd + Y = Redo
    if ((e.ctrlKey || e.metaKey) && e.key === 'y') {
      if (isInput) return;
      e.preventDefault();
      redo();
      return;
    }

    // Ctrl/Cmd + Shift + K = Design check
    if ((e.ctrlKey || e.metaKey) && e.shiftKey && e.key.toLowerCase() === 'k') {
      e.preventDefault();
      openValidationPanel(lastImportReport);
      return;
    }

    // Ctrl/Cmd + D = Duplicate selected element
    if ((e.ctrlKey || e.metaKey) && e.key === 'd') {
      e.preventDefault();
      if (currentElement && !currentElement.classList.contains('menu')) {
        pushUndo('duplicate');
        duplicateElement(currentElement);
      }
      return;
    }

    // Ctrl/Cmd + S = Export JSON (save)
    if ((e.ctrlKey || e.metaKey) && e.key === 's') {
      e.preventDefault();
      exportConfig();
      return;
    }

    // Ctrl/Cmd + P (or Ctrl+Shift+P, as the guide says) = Toggle preview
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'p') {
      e.preventDefault();
      togglePreviewMode();
      return;
    }

    // Preview: the D-pad. Arrows move the focus ring when the scene has
    // anything to focus; Shift+left/right still walks the scenes like the
    // device menu (and clicks the scene bar when focus would not move).
    if (previewMode && !isInput && e.key.startsWith('Arrow')) {
      e.preventDefault();
      if (e.shiftKey) {
        previewStep(e.key === 'ArrowRight' ? 1 : -1);
      } else if (!previewDpadMove(e.key) && (e.key === 'ArrowLeft' || e.key === 'ArrowRight')) {
        previewStep(e.key === 'ArrowRight' ? 1 : -1);
      }
      return;
    }

    // Preview: Enter/Space gen “A”, Backspace is “B” (scene back).
    if (previewMode && !isInput && (e.key === 'Enter' || e.key === ' ')) {
      e.preventDefault();
      if (previewFocusEl) previewFocusEl.click();
      return;
    }
    if (previewMode && !isInput && e.key === 'Backspace') {
      e.preventDefault();
      previewGoBack();
      return;
    }

    // Enter/Space activate a focused canvas element, matching a touch tap.
    if (!isInput && (e.key === 'Enter' || e.key === ' ') &&
        document.activeElement && document.activeElement.classList.contains('element')) {
      e.preventDefault();
      document.activeElement.click();
      return;
    }

    // Arrow keys nudge the selected element: 1 px, or 10 px with Shift.
    if (!isInput && !previewMode && currentElement && e.key.startsWith('Arrow')) {
      const step = e.shiftKey ? 10 : 1;
      const dx = e.key === 'ArrowLeft' ? -step : e.key === 'ArrowRight' ? step : 0;
      const dy = e.key === 'ArrowUp' ? -step : e.key === 'ArrowDown' ? step : 0;
      e.preventDefault();
      nudgeElement(currentElement, dx, dy);
      return;
    }

    // Escape closes the design check first when it is open.
    if (e.key === 'Escape') {
      const validate = document.getElementById('validatePanel');
      if (validate && !validate.hasAttribute('hidden')) {
        e.preventDefault();
        closeValidationPanel();
        return;
      }
    }

    // Delete / Backspace = Delete selected element (when not in input)
    if ((e.key === 'Delete' || e.key === 'Backspace') && !isInput) {
      if (currentElement && !currentElement.classList.contains('menu')) {
        e.preventDefault();
        pushUndo('delete');
        currentElement.remove();
        const sceneElements = scenes[currentScene];
        const idx = sceneElements.findIndex(item => item.isEqualNode(currentElement));
        if (idx > -1) sceneElements.splice(idx, 1);
        clearSelectionState();
        showToast('Element deleted', 'info');
      }
      return;
    }

    // Escape = deselect, close guide, or exit preview
    if (e.key === 'Escape') {
      if (previewMode) {
        exitPreviewMode();
        return;
      }
      if (guidePanel && !guidePanel.hasAttribute('hidden')) {
        closeGuideFn();
        return;
      }
      if (currentElement) {
        currentElement = null;
        document.querySelectorAll('.element').forEach(el => el.classList.remove('selected'));
        document.body.classList.remove('element-selected');
        const noSelection = document.getElementById('noSelection');
        if (noSelection) noSelection.style.display = '';
        switchTab('app-properties');
      }
      return;
    }

  });
}

// Set up all event listeners
function setupEventListeners() {
  // Dark mode toggle
  if (darkModeToggle) {
    darkModeToggle.addEventListener('click', () => {
      const isLight = document.body.classList.toggle('theme-light');
      document.body.classList.toggle('theme-dark', !isLight);
      setThemeToggleLabel(isLight);
      try { localStorage.setItem('jukahub-theme', isLight ? 'light' : 'dark'); } catch (e) { /* storage blocked */ }
    });
  }

  // Canvas zoom: fit by default, with explicit stepping. Refit on resize while
  // the user has not chosen a scale, so the device stays fully visible.
  const zoomOutBtn = document.getElementById('zoomOutBtn');
  const zoomInBtn = document.getElementById('zoomInBtn');
  const zoomFitBtn = document.getElementById('zoomFitBtn');
  if (zoomOutBtn) zoomOutBtn.addEventListener('click', () => zoomCanvas(1 / 1.2));
  if (zoomInBtn) zoomInBtn.addEventListener('click', () => zoomCanvas(1.2));
  if (zoomFitBtn) zoomFitBtn.addEventListener('click', () => fitCanvasToViewport());

  let canvasFitTimer = null;
  window.addEventListener('resize', () => {
    if (previewMode || zoomIsManual) return;
    clearTimeout(canvasFitTimer);
    canvasFitTimer = setTimeout(fitCanvasToViewport, 120);
  });

  // Guide panel toggle
  if (toggleGuide && guidePanel) {
    toggleGuide.addEventListener('click', () => {
      const isHidden = guidePanel.hasAttribute('hidden');
      if (isHidden) openGuide(); else closeGuide();
    });
  }

  if (closeGuide) {
    closeGuide.addEventListener('click', closeGuideFn);
  }

  // Escape key closes overlays
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      // Close context menu
      const ctxMenu = document.getElementById('contextMenu');
      if (ctxMenu) ctxMenu.style.display = 'none';

      if (guidePanel && !guidePanel.hasAttribute('hidden')) {
        closeGuideFn();
        return;
      }
      // Deselect element on Escape
      if (currentElement) {
        currentElement = null;
        document.querySelectorAll('.element').forEach(el => el.classList.remove('selected'));
        document.body.classList.remove('element-selected');
        const noSelection = document.getElementById('noSelection');
        if (noSelection) noSelection.style.display = '';
        switchTab('app-properties');
      }
    }
  });

  // Properties tabs
  propertiesTabs.forEach(tab => {
    tab.addEventListener('click', () => {
      const tabId = tab.getAttribute('data-tab');
      switchTab(tabId);
    });
  });

  // Sidebar panel toggles: off-canvas drawers on small screens, collapsible
  // rails on desktop.
  const leftToggle = document.getElementById('leftSidebarToggle');
  const rightToggle = document.getElementById('rightSidebarToggle');

  function sidebarToggle(side) {
    const sb = document.getElementById(side === 'left' ? 'leftSidebar' : 'rightSidebar');
    const btn = side === 'left' ? leftToggle : rightToggle;
    const label = side === 'left' ? 'library' : 'inspector';
    if (!sb) return () => {};
    return () => {
      if (window.innerWidth <= 992) {
        const open = sb.classList.toggle('open');
        if (btn) btn.setAttribute('aria-expanded', String(open));
        return;
      }
      const collapsed = sb.classList.toggle('collapsed');
      document.body.classList.toggle(side + '-collapsed', collapsed);
      if (btn) {
        btn.setAttribute('aria-expanded', String(!collapsed));
        btn.setAttribute('aria-label', (collapsed ? 'Expand ' : 'Collapse ') + label);
        btn.setAttribute('title', (collapsed ? 'Expand ' : 'Collapse ') + label);
      }
      try {
        localStorage.setItem('jukahub-' + side + '-collapsed', collapsed ? '1' : '0');
      } catch (e) { /* private mode can deny localStorage */ }
    };
  }

  if (leftToggle) leftToggle.addEventListener('click', sidebarToggle('left'));
  if (rightToggle) rightToggle.addEventListener('click', sidebarToggle('right'));

  // Restore collapsed rails from the last session (desktop only; the CSS that
  // honours .collapsed is scoped to min-width: 993px).
  ['left', 'right'].forEach((side) => {
    let stored = false;
    try { stored = localStorage.getItem('jukahub-' + side + '-collapsed') === '1'; } catch (e) { /* ignore */ }
    if (!stored) return;
    const sb = document.getElementById(side === 'left' ? 'leftSidebar' : 'rightSidebar');
    const btn = side === 'left' ? leftToggle : rightToggle;
    const label = side === 'left' ? 'library' : 'inspector';
    if (!sb) return;
    sb.classList.add('collapsed');
    document.body.classList.add(side + '-collapsed');
    if (btn) {
      btn.setAttribute('aria-expanded', 'false');
      btn.setAttribute('aria-label', 'Expand ' + label);
      btn.setAttribute('title', 'Expand ' + label);
    }
  });

  // Mobile panel tabs
  document.querySelectorAll('.mobile-tab').forEach(tab => {
    tab.addEventListener('click', () => {
      const target = tab.getAttribute('data-target');
      document.querySelectorAll('.mobile-tab').forEach(t => {
        t.classList.remove('active');
        t.setAttribute('aria-selected', 'false');
      });
      tab.classList.add('active');
      tab.setAttribute('aria-selected', 'true');
      document.getElementById('leftSidebar').classList.remove('open');
      document.getElementById('rightSidebar').classList.remove('open');
      if (target === 'left') document.getElementById('leftSidebar').classList.add('open');
      if (target === 'right') document.getElementById('rightSidebar').classList.add('open');
    });
  });

  // Component filter
  const componentSearch = document.getElementById('componentSearch');
  if (componentSearch) {
    componentSearch.addEventListener('input', (e) => {
      const term = e.target.value.trim().toLowerCase();
      document.querySelectorAll('.left-sidebar .element[data-type]').forEach(el => {
        const text = (el.textContent || '').toLowerCase();
        const type = (el.getAttribute('data-type') || '').toLowerCase();
        el.hidden = term.length > 0 && !text.includes(term) && !type.includes(term);
      });
    });
  }

  // Canvas drop zone
  canvas.addEventListener('dragover', e => e.preventDefault());

  canvas.addEventListener('drop', e => {
    e.preventDefault();
    const type = e.dataTransfer.getData('type');
    const rect = canvas.getBoundingClientRect();
    // The canvas is scaled to fit the pane, so screen pixels are not canvas pixels.
    const x = (e.clientX - rect.left) / canvasScale;
    const y = (e.clientY - rect.top) / canvasScale;
    addElement(type, x, y);
  });

  // Canvas click to deselect
  canvas.addEventListener('click', e => {
    if (e.target === canvas) {
      currentElement = null;
      document.querySelectorAll('.element').forEach(el => el.classList.remove('selected'));
      document.body.classList.remove('element-selected');
      const noSelection = document.getElementById('noSelection');
      if (noSelection) noSelection.style.display = '';
      switchTab('app-properties');
    }
  });

  // Initialize drag events for elements
  document.querySelectorAll('.left-sidebar .element').forEach(el => {
    el.addEventListener('dragstart', e => {
      e.dataTransfer.setData('type', e.target.getAttribute('data-type'));
    });
  });

  // Canvas size controls
  canvasSizeSelect.addEventListener('change', updateCanvasSize);
  customWidthInput.addEventListener('change', updateCanvasSize);
  customHeightInput.addEventListener('change', updateCanvasSize);

  // Background image file input
  if (backgroundFileInput) {
    backgroundFileInput.addEventListener('change', setBackground);
  }

  // Add variable button
  addVariableButton.addEventListener('click', addVariable);

  setupMobileDoubleTap();

  // Load file
  loadFileInput.addEventListener('change', function (e) {
    const file = e.target.files[0];
    if (!file) return;
    importConfigFile(file);
    // Allow selecting the same file again after a failed import.
    e.target.value = '';
  });

  // Clear button
  clearButton.addEventListener('click', clearAll);

  // Close mobile sidebars when clicking outside on narrow screens
  document.addEventListener('click', (e) => {
    if (window.innerWidth > 992) return;
    const left = document.getElementById('leftSidebar');
    const right = document.getElementById('rightSidebar');
    const leftToggle = document.getElementById('leftSidebarToggle');
    const rightToggle = document.getElementById('rightSidebarToggle');
    if (left && left.classList.contains('open') &&
        !left.contains(e.target) &&
        e.target !== leftToggle &&
        !leftToggle.contains(e.target) &&
        !e.target.closest('.mobile-tab')) {
      left.classList.remove('open');
    }
    if (right && right.classList.contains('open') &&
        !right.contains(e.target) &&
        e.target !== rightToggle &&
        !rightToggle.contains(e.target) &&
        !e.target.closest('.mobile-tab')) {
      right.classList.remove('open');
    }
  });
}

// The theme control is icon-only, so its accessible name has to carry the
// current state instead of a hidden label.
function setThemeToggleLabel(isLight) {
  if (!darkModeToggle) return;
  const label = isLight ? 'Switch to dark theme' : 'Switch to light theme';
  darkModeToggle.innerHTML = isLight
    ? '<i class="fas fa-sun" aria-hidden="true"></i>'
    : '<i class="fas fa-moon" aria-hidden="true"></i>';
  darkModeToggle.setAttribute('aria-label', label);
  darkModeToggle.title = label;
}

// Theme helpers
function loadTheme() {
  let saved = null;
  try { saved = localStorage.getItem('jukahub-theme'); } catch (e) { /* storage blocked: use the default theme */ }
  const prefersLight = saved === 'light';
  document.body.classList.toggle('theme-light', prefersLight);
  document.body.classList.toggle('theme-dark', !prefersLight);
  setThemeToggleLabel(prefersLight);
}

function openGuide() {
  if (!guidePanel) return;
  guidePanel.removeAttribute('hidden');
  guidePanel.setAttribute('aria-hidden', 'false');
  guidePanel.querySelector('.close-button')?.focus();
}

function closeGuideFn() {
  if (!guidePanel) return;
  guidePanel.setAttribute('hidden', '');
  guidePanel.setAttribute('aria-hidden', 'true');
  toggleGuide?.focus();
}

function updateCanvasReadout() {
  const readout = document.getElementById('canvasSizeReadout');
  if (readout) readout.textContent = `${canvasWidth} × ${canvasHeight}`;
  // The subtitle names the target hardware, so it must follow the device picker
  // instead of claiming the Smart Pro while a Brick-size canvas is selected.
  const subtitle = document.getElementById('projectTitle');
  if (subtitle) {
    const device = canvasWidth === 1280 && canvasHeight === 720
      ? 'TrimUI Smart Pro'
      : canvasWidth === 1024 && canvasHeight === 768
        ? 'TrimUI Brick'
        : 'Custom canvas';
    subtitle.textContent = `${device} · ${canvasWidth} × ${canvasHeight}`;
  }
}

function updateSceneBadge() {
  const badge = document.getElementById('sceneNameBadge');
  if (badge) badge.textContent = currentScene;
}

function showToast(message, type = 'info') {
  const container = document.getElementById('toastContainer');
  if (!container) return;
  const toast = document.createElement('div');
  toast.className = `toast ${type}`;
  toast.textContent = message;
  container.appendChild(toast);
  setTimeout(() => {
    toast.style.opacity = '0';
    toast.addEventListener('transitionend', () => toast.remove(), { once: true });
    setTimeout(() => toast.remove(), 500);
  }, 3000);
}

function setLoading(button, on) {
  if (!button) return;
  button.classList.toggle('spinning', on);
  if (on) {
    button.setAttribute('aria-busy', 'true');
  } else {
    button.removeAttribute('aria-busy');
  }
}

// Restore the last successfully imported JSON/XML config, discarding workspace
// edits and replacing the local auto-save with the imported source.
function reloadImportedConfig() {
  const btn = document.getElementById('reloadConfigBtn');
  if (!importedConfigSnapshot) {
    showToast('Load a JSON or XML config before reloading', 'warning');
    return;
  }

  setLoading(btn, true);
  try {
    // Exit preview without restoring its snapshot; a reload intentionally
    // replaces the workspace with the last loaded file.
    if (previewMode) {
      previewMode = false;
      document.body.classList.remove('preview-mode');
      setPreviewButtonLabel(false);
    }

    if (autoSaveTimer) {
      clearTimeout(autoSaveTimer);
      autoSaveTimer = null;
    }
    try { localStorage.removeItem('jukahub-autosave'); } catch (e) { /* ignore */ }

    const source = JSON.parse(JSON.stringify(importedConfigSnapshot));
    loadJukaApp(source);
    scheduleAutoSave();
    showToast('Configuration reloaded from last import', 'success');
  } catch (e) {
    showToast('Reload failed: ' + e.message, 'error');
  } finally {
    setLoading(btn, false);
  }
}


// Set up font size change listeners
function setupFontSizeListeners() {
  titleSizeInput.addEventListener('change', updateAllFontSizes);
  bigSizeInput.addEventListener('change', updateAllFontSizes);
  mediumSizeInput.addEventListener('change', updateAllFontSizes);
  smallSizeInput.addEventListener('change', updateAllFontSizes);
}

// Update all font sizes when font size inputs change
function updateAllFontSizes() {
  document.querySelectorAll('.element').forEach(el => {
    const fontType = el.getAttribute('data-font');
    if (fontType && fontType !== 'dynamiclist') {
      el.style.fontSize = getFontSize(fontType) + 'px';
    }
  });
  document.querySelectorAll('.menu-scene-button').forEach(el => {
    el.style.fontSize = getFontSize("small") + 'px';
  });

  document.querySelectorAll('.menu-clock').forEach(el => {
    el.style.fontSize = getFontSize("small") + 'px';
  });
}

// Switch between tabs
function switchTab(tabId) {
  // Update active tab
  propertiesTabs.forEach(tab => {
    const isMatch = tab.getAttribute('data-tab') === tabId;
    tab.classList.toggle('active', isMatch);
    tab.setAttribute('aria-selected', String(isMatch));
  });

  // Show/hide panels
  if (tabId === 'app-properties') {
    appInfoPanel.style.display = 'block';
    appInfoPanel.classList.add('active');
    elementPropertiesPanel.style.display = 'none';
    elementPropertiesPanel.classList.remove('active');
  } else {
    appInfoPanel.style.display = 'none';
    appInfoPanel.classList.remove('active');
    elementPropertiesPanel.style.display = 'block';
    elementPropertiesPanel.classList.add('active');
  }
}

// Reflect the live canvas geometry in the size picker and its custom fields so
// an import or an undo cannot leave the controls describing a different canvas.
function syncCanvasSizeControls() {
  if (!canvasSizeSelect) return;
  const preset = `${canvasWidth}x${canvasHeight}`;
  const hasPreset = Array.from(canvasSizeSelect.options).some(option => option.value === preset);
  canvasSizeSelect.value = hasPreset ? preset : 'custom';
  const customFields = document.getElementById('customSizeFields');
  if (customFields) customFields.style.display = hasPreset ? 'none' : 'grid';
  if (customWidthInput) customWidthInput.value = canvasWidth;
  if (customHeightInput) customHeightInput.value = canvasHeight;
  canvas.style.width = `${canvasWidth}px`;
  canvas.style.height = `${canvasHeight}px`;
  applyCanvasScale(canvasScale);
}

// Update canvas size based on selection
function updateCanvasSize() {
  if (canvasSizeSelect.value === 'custom') {
    canvasWidth = parseInt(customWidthInput.value) || 1280;
    canvasHeight = parseInt(customHeightInput.value) || 720;
    document.getElementById('customSizeFields').style.display = 'grid';
  } else {
    const [width, height] = canvasSizeSelect.value.split('x').map(Number);
    canvasWidth = width;
    canvasHeight = height;
    document.getElementById('customSizeFields').style.display = 'none';
  }

  // Apply new size
  canvas.style.width = `${canvasWidth}px`;
  canvas.style.height = `${canvasHeight}px`;
  updateCanvasReadout();
  fitCanvasToViewport();

  // Update menu position
  document.querySelectorAll('.element[data-type="menu"]').forEach(menu => {
    menu.style.top = `${canvasHeight - 50}px`;
  });

  // Update all elements to stay within new canvas bounds
  document.querySelectorAll('.element').forEach(el => {
    const x = parseInt(el.getAttribute('data-x'));
    const y = parseInt(el.getAttribute('data-y'));
    const width = parseInt(el.getAttribute('data-width'));
    const height = parseInt(el.getAttribute('data-height'));

    // Ensure element stays within canvas
    const newX = Math.min(x, canvasWidth - width);
    const newY = Math.min(y, canvasHeight - height);

    el.style.left = `${newX}px`;
    el.style.top = `${newY}px`;
    el.setAttribute('data-x', newX);
    el.setAttribute('data-y', newY);
  });
}

// Update the addScene function to call the new function
// Scene bookkeeping shared by add, rename, duplicate and delete.
function sceneExists(name) {
  return Object.prototype.hasOwnProperty.call(scenes, name);
}

// "Scene N" with N one past the count is not safe once a scene has been deleted
// or renamed: with Scene 1 deleted, the next name would be "Scene 3" again and
// silently replace the scene already called that.
function nextSceneName() {
  let n = Object.keys(scenes).length + 1;
  while (sceneExists(`Scene ${n}`)) n += 1;
  return `Scene ${n}`;
}

// Scene names are free text (imported configs use quotes and brackets), so look
// the option up by comparing values instead of building a CSS selector from it.
function findSceneOption(name) {
  return Array.from(sceneSelector.options).find(option => option.value === name) || null;
}

// Buttons reach a scene by name ("change_scene:Tube" or data-scene-change).
// Renaming the scene has to carry those links with it or navigation dead-ends.
function retargetSceneLinks(oldName, newName) {
  const retarget = el => {
    if (el.getAttribute('data-scene-change') === oldName) el.setAttribute('data-scene-change', newName);
    if ((el.getAttribute('data-trigger') || '').trim() === `change_scene:${oldName}`) {
      el.setAttribute('data-trigger', `change_scene:${newName}`);
    }
  };
  Object.values(scenes).forEach(list => list.forEach(retarget));
  canvas.querySelectorAll('.element').forEach(retarget);
}

function addScene() {
  pushUndo('add scene');
  saveCurrentScene();
  const newSceneName = nextSceneName();
  scenes[newSceneName] = [];

  const option = document.createElement('option');
  option.value = newSceneName;
  option.textContent = newSceneName;
  sceneSelector.appendChild(option);
  sceneSelector.value = newSceneName;

  currentScene = newSceneName;
  loadScene(currentScene);
  updateSceneBadge();

  // Add menu to new scene (the scene itself is already one history step)
  addElement('menu', 0, canvasHeight - 50, false);

  // Update scene change selector
  updateSceneChangeSelector();

  // Update all menu scene buttons in all scenes
  updateAllMenuSceneButtons();
  updateAllStoredMenus();
  scheduleAutoSave();
  showToast(`Scene "${newSceneName}" added`, 'success');
}

function updateAllMenuSceneButtons() {
  document.querySelectorAll('.element[data-type="menu"]').forEach(menu => {
    updateMenuSceneButtons(menu);
  });
}

function updateAllStoredMenus() {
  for (const sceneName in scenes) {
    scenes[sceneName].forEach(el => {
      if (el.getAttribute('data-type') === 'menu') {
        updateMenuSceneButtons(el);
      }
    });
  }
}

function duplicateScene() {
  saveCurrentScene();
  const newSceneName = prompt('Name for duplicated scene:', `${currentScene} Copy`);
  if (!newSceneName || sceneExists(newSceneName)) return;
  pushUndo('duplicate scene');

  scenes[newSceneName] = scenes[currentScene].map(el => el.cloneNode(true));

  const option = document.createElement('option');
  option.value = newSceneName;
  option.textContent = newSceneName;
  sceneSelector.appendChild(option);
  sceneSelector.value = newSceneName;
  currentScene = newSceneName;

  loadScene(newSceneName);
  updateSceneBadge();

  // Update scene change selector
  updateSceneChangeSelector();

  // Update all menu scene buttons
  updateAllMenuSceneButtons();
  updateAllStoredMenus();
  scheduleAutoSave();
  showToast(`Scene "${newSceneName}" duplicated`, 'success');
}

function changeScene() {
  currentScene = sceneSelector.value;
  loadScene(currentScene);
  updateAllMenuSceneButtons();
  updateSceneBadge();
}

function loadScene(sceneName) {
  canvas.innerHTML = '';
  if (scenes[sceneName]) {
    scenes[sceneName].forEach(el => {
      const clonedEl = el.cloneNode(true);
      setupElementEvents(clonedEl);
      canvas.appendChild(clonedEl);
    });
  }

  // Update menu buttons
  document.querySelectorAll('.menu').forEach(menu => {
    updateMenuSceneButtons(menu);
  });
}

// Small helper so both preview-exit and reload can keep font-size controls in
// sync without duplicating the same four assignments.
// Element Management
function addElement(type, x, y, recordHistory = true) {
  if (type === 'menu-element') {
    type = 'menu'; // Convert to the actual type used on canvas
  }
  if (recordHistory && editorReady && typeof pushUndo === 'function') pushUndo('add ' + type);
  const el = document.createElement('div');
  el.className = 'element';
  el.style.position = 'absolute';
  el.style.left = `${x}px`;
  el.style.top = `${y}px`;
  el.setAttribute('data-opacity', '100');
  el.style.opacity = 1;
  el.style.fontFamily = 'Inter, sans-serif';
  el.style.fontWeight = '900';


  // Set default dimensions
  const dimensions = {
    button: { width: '120px', height: '40px' },
    label: { width: '120px', height: '40px' },
    menu: { width: '100%', height: '50px', y: canvasHeight - 50 },
    image: { width: '100px', height: '100px' },
    input: { width: '150px', height: '40px' },
    video: { width: '200px', height: '150px' },
    dynamiclist: { width: '600px', height: '40px' }, // Add this line
    default: { width: 'auto', height: 'auto' }
  };

  const { width, height } = dimensions[type] || dimensions.default;
  el.style.width = width;
  el.style.height = height;


  // Make elements larger on mobile for better touch interaction
  if (window.innerWidth <= 768) {
    if (type === 'button' || type === 'label' || type === 'input') {
      el.style.minHeight = '44px'; // Minimum touch target size
      el.style.minWidth = '80px';
    }
  }


  if (type === 'dynamiclist') {
    el.innerHTML = `
      <span class="text-content">Dynamic List</span>
      <span class="remove-button">✕</span>
  `;
    el.setAttribute('data-command', '');
    el.setAttribute('data-variable', '');
    setupDynamicListExecution(el);
  } else if (type === 'textbrowser') {
    const sourceIcons = {
      'system': '🖥️',
      'zeroconf': '🔍',
      'json': '📋'
    };
    const sourceNames = {
      'system': 'System',
      'zeroconf': 'Zeroconf',
      'json': 'JSON'
    };
    el.innerHTML = `
      <span class="text-content">${sourceIcons['system'] || '🌐'} ${sourceNames['system'] || 'Text Browser'}</span>
      <span class="remove-button">✕</span>
    `;
    el.setAttribute('data-variable', '');
    el.setAttribute('data-source', 'system');
  } else if (type === 'menu') {
    el.style.top = `${dimensions.menu.y}px`;
    el.style.left = '0px';
    el.innerHTML = `
                    <div class="menu-scene-buttons"></div>
                    <div class="menu-clock">00:00</div>
                    <span class="remove-button">✕</span>
                `; // Removed the language button
    el.style.fontSize = '16px';
    el.setAttribute('data-type', 'menu');
    setupMenuEvents(el);
    updateMenuSceneButtons(el);
    updateMenuClock(el.querySelector('.menu-clock'));
  } else {
    const textSpan = document.createElement('span');
    textSpan.className = 'text-content';

    // Fix for Collapsed List text
    let displayText = type.charAt(0).toUpperCase() + type.slice(1);
    if (type === 'collapsedlist') {
      displayText = 'Collapsed List';
    }
    textSpan.textContent = displayText;

    el.appendChild(textSpan);

    const removeButton = document.createElement('span');
    removeButton.textContent = '✕';
    removeButton.className = 'remove-button';
    el.appendChild(removeButton);

    el.setAttribute('data-type', type);

    // Special handling for input elements
    if (type === 'input') {
      textSpan.style.display = 'none';
      const input = document.createElement('input');
      input.type = 'text';
      input.className = 'element-input';
      input.placeholder = 'Input text';
      input.addEventListener('mousedown', (e) => {
        e.stopPropagation(); // Prevent dragging when clicking on input
      });
      el.appendChild(input);
    }

    // Special handling for image elements
    if (type === 'image') {
      const img = document.createElement('img');
      img.className = 'element-image';
      img.src = '';
      img.draggable = false; // Prevent image dragging
      el.appendChild(img);
      textSpan.style.display = 'none';
    }

    // Update the addElement function
    if (type === 'collapsedlist') {
      const listIcon = document.createElement('i');
      listIcon.className = 'fas fa-bars';
      listIcon.style.marginRight = '8px';
      textSpan.prepend(listIcon);

      // Set up collapsed list properties
      el.setAttribute('data-list-variable', '');
    }

    // Labels should have no background
    if (type === 'label') {
      el.style.background = 'none';
    }
  }

  // Set element attributes
  // Stamp the type here so special-cased branches above (textbrowser,
  // dynamiclist, ...) export and inspect like every other element instead of
  // serialising with no type at all.
  el.setAttribute('data-type', type);
  el.setAttribute('data-x', x | 0);
  el.setAttribute('data-y', y | 0);
  el.setAttribute('data-width', width.replace('px', '') || '100');
  el.setAttribute('data-height', height.replace('px', '') || '100');

  if (type !== 'menu') {
    el.setAttribute('data-color', '#000000');
    el.setAttribute('data-font', 'medium');
    el.style.fontSize = getFontSize('medium') + 'px';
    el.style.padding = '4px';

    if (type === 'button') {
      el.setAttribute('data-bg-color', '#ffffff');
      el.style.backgroundColor = '#ffffff';
    }
  }

  // Add to canvas
  canvas.appendChild(el);
  setupElementEvents(el);

  if (!scenes[currentScene]) scenes[currentScene] = [];
  scenes[currentScene].push(el.cloneNode(true));

  scheduleAutoSave();
  scheduleValidationRefresh();
  return el;
}

function setupElementEvents(el) {
  let isDragging = false;
  // Pointer start plus the element's canvas offset at that moment. Dividing the
  // pointer delta by canvasScale keeps 1:1 tracking when the device is zoomed.
  let dragStartX = 0, dragStartY = 0;
  let dragStartLeft = 0, dragStartTop = 0;
  let dragUndoBase = null;

  // Canvas elements are part of the keyboard editing surface: Tab reaches them,
  // Enter selects, and arrow keys nudge (handled globally in the shortcuts).
  if (el.getAttribute('data-type') !== 'menu') {
    el.tabIndex = 0;
    el.setAttribute('role', 'group');
    const label = el.querySelector('.text-content')?.textContent || el.getAttribute('data-type') || 'element';
    el.setAttribute('aria-label', `${el.getAttribute('data-type') || 'element'}: ${label}`);
  }

  // Touch events for mobile
  el.addEventListener('touchstart', (event) => {
    if (previewMode) return;
    if (event.touches.length === 1) {
      event.preventDefault();
      const touch = event.touches[0];
      dragStartX = touch.clientX;
      dragStartY = touch.clientY;
      dragStartLeft = el.offsetLeft;
      dragStartTop = el.offsetTop;
      isDragging = true;
      dragUndoBase = captureWorkspace('move');
      el.style.cursor = 'grabbing';

    }
  }, { passive: false });


  document.addEventListener('touchmove', (event) => {
    if (!isDragging) return;
    event.preventDefault();

    const touch = event.touches[0];
    let newX = dragStartLeft + (touch.clientX - dragStartX) / canvasScale;
    let newY = dragStartTop + (touch.clientY - dragStartY) / canvasScale;

    newX = Math.round(Math.max(0, Math.min(newX, canvasWidth - el.offsetWidth)));
    newY = Math.round(Math.max(0, Math.min(newY, canvasHeight - el.offsetHeight)));

    el.style.transition = 'none';
    el.style.left = `${newX}px`;
    el.style.top = `${newY}px`;
    el.setAttribute('data-x', newX);
    el.setAttribute('data-y', newY);
  }, { passive: false });

  document.addEventListener('touchend', () => {
    if (isDragging) {
      isDragging = false;
      el.style.cursor = 'grab';
      commitUndoSnapshot(dragUndoBase);
      dragUndoBase = null;
      scheduleAutoSave();
    }
  }, { passive: false });


  // Mouse events for dragging and resizing
  el.addEventListener('mousedown', (event) => {
    if (previewMode) return;
    if (event.button === 2) { // Right click for resize
      handleResize(el, event);
    } else { // Left click for drag
      // Prevent dragging if clicking on input or image
      if (event.target.tagName === 'INPUT' || event.target.tagName === 'IMG') {
        return;
      }

      isDragging = true;
      dragStartX = event.clientX;
      dragStartY = event.clientY;
      dragStartLeft = el.offsetLeft;
      dragStartTop = el.offsetTop;
      dragUndoBase = captureWorkspace('move');
      el.style.cursor = 'grabbing';

      // Prevent text selection during drag
      event.preventDefault();
    }
  });

  // Mouse move for dragging
  document.addEventListener('mousemove', (event) => {
    if (!isDragging) return;

    let newX = dragStartLeft + (event.clientX - dragStartX) / canvasScale;
    let newY = dragStartTop + (event.clientY - dragStartY) / canvasScale;

    newX = Math.round(Math.max(0, Math.min(newX, canvasWidth - el.offsetWidth)));
    newY = Math.round(Math.max(0, Math.min(newY, canvasHeight - el.offsetHeight)));

    // Remove any transition effects
    el.style.transition = 'none';

    el.style.left = `${newX}px`;
    el.style.top = `${newY}px`;
    el.setAttribute('data-x', newX);
    el.setAttribute('data-y', newY);
  });

  // Mouse up to stop dragging
  document.addEventListener('mouseup', () => {
    if (isDragging) {
      isDragging = false;
      el.style.cursor = 'grab';
      commitUndoSnapshot(dragUndoBase);
      dragUndoBase = null;
      scheduleAutoSave();
    }
  });

  // Context menu prevention
  el.addEventListener('contextmenu', (event) => event.preventDefault());

  // Double click for editing
  el.addEventListener('dblclick', (event) => {
    if (previewMode) return;
    event.stopPropagation();
    const type = el.getAttribute('data-type');
    if (['button', 'label', 'video'].includes(type)) {
      const textSpan = el.querySelector('.text-content');
      const newText = prompt("Edit text:", textSpan.textContent);
      if (newText !== null && newText !== textSpan.textContent) {
        pushUndo('edit text');
        textSpan.textContent = newText;
        processTextForVariables(textSpan);
        scheduleValidationRefresh();
      }
    } else if (type === 'image') {
      const fileInput = document.createElement('input');
      fileInput.type = 'file';
      fileInput.accept = 'image/*';
      fileInput.style.display = 'none';

      fileInput.onchange = (e) => {
        const file = e.target.files[0];
        if (!file) return;

        const reader = new FileReader();
        reader.onload = (e) => {
          const img = el.querySelector('.element-image');
          if (img) img.src = e.target.result;
          const textSpan = el.querySelector('.text-content');
          if (textSpan) textSpan.style.display = 'none';
        };
        reader.readAsDataURL(file);
      };

      document.body.appendChild(fileInput);
      fileInput.click();
      document.body.removeChild(fileInput);
    } else if (type === 'input') {
      const input = el.querySelector('.element-input');
      if (input) input.focus();
    } else if (type === 'dynamiclist') {
      const command = prompt("Enter command path:", el.getAttribute('data-command') || '');
      if (command !== null) {
        el.setAttribute('data-command', command);
      }

      const variable = prompt("Enter variable name:", el.getAttribute('data-variable') || '');
      if (variable !== null) {
        el.setAttribute('data-variable', variable);
      }
    }
  });

  // Selection
  // Update the element click event listener to properly switch panels
  // Update the element click event listener to work better on mobile
  el.addEventListener('click', (e) => {
    if (previewMode) return;
    if (e.target.classList.contains('remove-button') ||
      e.target.classList.contains('menu-scene-button') ||
      e.target.classList.contains('menu-language') ||
      e.target.classList.contains('element-input')) {
      return;
    }

    document.querySelectorAll('.element').forEach(otherEl => {
      otherEl.classList.remove('selected');
    });
    el.classList.add('selected');
    currentElement = el;
    document.body.classList.add('element-selected');
    showElementProperties(el);

    // Force the properties panel to show element properties
    document.getElementById('appInfoPanel').style.display = 'none';
    document.getElementById('elementPropertiesPanel').style.display = 'block';

    // Update tab states
    document.querySelectorAll('.properties-tab').forEach(tab => {
      if (tab.getAttribute('data-tab') === 'element-properties') {
        tab.classList.add('active');
      } else {
        tab.classList.remove('active');
      }
    });
  });

  // Remove button
  const removeButton = el.querySelector('.remove-button');
  if (removeButton) {
    removeButton.addEventListener('click', (event) => {
      event.stopPropagation();
      pushUndo('delete');
      el.remove();
      const sceneElements = scenes[currentScene];
      const index = sceneElements.findIndex(item => item.isEqualNode(el));
      if (index > -1) sceneElements.splice(index, 1);
      if (currentElement === el) clearSelectionState();
      scheduleAutoSave();
    });
  }

  // Process text for variables
  const textSpan = el.querySelector('.text-content');
  if (textSpan) {
    processTextForVariables(textSpan);
  }
}

function handleResize(el, event) {
  el.style.cursor = 'nwse-resize';
  const startX = event.clientX;
  const startY = event.clientY;
  const startWidth = el.offsetWidth;
  const startHeight = el.offsetHeight;
  const undoBase = captureWorkspace('resize');

  // Dragging keeps an element on the device; resizing has to as well, or a
  // stretched button runs off the panel and the player clips it.
  const left = parseFloat(el.style.left) || 0;
  const top = parseFloat(el.style.top) || 0;
  const maxWidth = Math.max(50, canvasWidth - left);
  const maxHeight = Math.max(50, canvasHeight - top);

  const onMouseMove = (e) => {
    const newWidth = Math.min(maxWidth, Math.max(50, Math.round(startWidth + (e.clientX - startX) / canvasScale)));
    const newHeight = Math.min(maxHeight, Math.max(50, Math.round(startHeight + (e.clientY - startY) / canvasScale)));
    el.style.width = `${newWidth}px`;
    el.style.height = `${newHeight}px`;
    el.setAttribute('data-width', newWidth);
    el.setAttribute('data-height', newHeight);
  };

  const onMouseUp = () => {
    el.style.cursor = 'grab';
    document.removeEventListener('mousemove', onMouseMove);
    document.removeEventListener('mouseup', onMouseUp);
    commitUndoSnapshot(undoBase);
    scheduleAutoSave();
  };

  document.addEventListener('mousemove', onMouseMove);
  document.addEventListener('mouseup', onMouseUp);
}


function showElementProperties(el) {
  const noSelection = document.getElementById('noSelection');
  if (noSelection) noSelection.style.display = 'none';

  if (window.innerWidth <= 768) {
    document.getElementById('appInfoPanel').style.display = 'none';
    document.getElementById('elementPropertiesPanel').style.display = 'block';

    // Scroll to properties panel on mobile
    setTimeout(() => {
      document.querySelector('.right-sidebar').scrollIntoView({
        behavior: 'smooth',
        block: 'nearest'
      });
    }, 100);
  }

  const triggerControls = document.querySelector('.trigger-controls');
  if (triggerControls) {
    if (['button', 'input', 'textbrowser'].includes(el.getAttribute('data-type'))) {
      triggerControls.style.display = 'block';
    } else {
      triggerControls.style.display = 'none';
    }
  }

  // Dynamic List properties - only show for dynamiclist elements
  const dynamicListProperties = document.querySelector('.dynamic-list-properties');
  if (el.getAttribute('data-type') === 'dynamiclist') {
    dynamicListProperties.style.display = 'block';

    // Set command path
    const commandInput = document.getElementById('dynamicCommand');
    commandInput.value = el.getAttribute('data-command') || '';
    commandInput.onchange = () => {
      el.setAttribute('data-command', commandInput.value);
    };

    // Set up variable selector
    const variableSelector = document.getElementById('dynamicVariable');
    updateVariableSelector(variableSelector, el.getAttribute('data-variable') || '');
    variableSelector.onchange = () => {
      el.setAttribute('data-variable', variableSelector.value);
    };
  } else {
    dynamicListProperties.style.display = 'none';
  }

  // Text Browser properties - only show for textbrowser elements
  const textBrowserProperties = document.querySelector('.textbrowser-properties');
  if (el.getAttribute('data-type') === 'textbrowser') {
    textBrowserProperties.style.display = 'block';

    const tbVariable = document.getElementById('textBrowserVariable');
    updateVariableSelector(tbVariable, el.getAttribute('data-variable') || '');
    tbVariable.onchange = () => {
      el.setAttribute('data-variable', tbVariable.value);
    };

    const tbSource = document.getElementById('textBrowserSource');
    tbSource.value = el.getAttribute('data-source') || 'system';
    tbSource.onchange = () => {
      el.setAttribute('data-source', tbSource.value);
      const sourceIcons = {
        'system': '🖥️',
        'zeroconf': '🔍',
        'json': '📋'
      };
      const sourceNames = {
        'system': 'System',
        'zeroconf': 'Zeroconf',
        'json': 'JSON'
      };
      const textContent = el.querySelector('.text-content');
      if (textContent) {
        textContent.textContent = `${sourceIcons[tbSource.value] || '🌐'} ${sourceNames[tbSource.value] || 'Text Browser'}`;
      }
      const jsonPathGroup = document.getElementById('textBrowserJsonPathGroup');
      if (jsonPathGroup) {
        jsonPathGroup.style.display = tbSource.value === 'json' ? 'block' : 'none';
      }
      updateSourceBadges(tbSource.value);
    };

    const tbJsonPath = document.getElementById('textBrowserJsonPath');
    if (tbJsonPath) {
      tbJsonPath.value = el.getAttribute('data-json-path') || '';
      tbJsonPath.onchange = () => {
        el.setAttribute('data-json-path', tbJsonPath.value);
      };
    }

    const jsonPathGroup = document.getElementById('textBrowserJsonPathGroup');
    if (jsonPathGroup) {
      jsonPathGroup.style.display = tbSource.value === 'json' ? 'block' : 'none';
    }

    const tbAutoRefresh = document.getElementById('textBrowserAutoRefresh');
    if (tbAutoRefresh) {
      tbAutoRefresh.checked = el.getAttribute('data-auto-refresh') === 'true';
      tbAutoRefresh.onchange = () => {
        el.setAttribute('data-auto-refresh', tbAutoRefresh.checked ? 'true' : 'false');
      };
    }

    updateSourceBadges(tbSource.value);
  } else {
    textBrowserProperties.style.display = 'none';
  }

  // Search-results grid properties - only show for searchresults elements.
  const searchResultsProperties = document.querySelector('.searchresults-properties');
  if (searchResultsProperties) {
    if (el.getAttribute('data-type') === 'searchresults') {
      searchResultsProperties.style.display = 'block';

      const resultsVariable = document.getElementById('resultsVariable');
      if (resultsVariable) {
        resultsVariable.value = el.getAttribute('data-variable') || '';
        resultsVariable.onchange = () => {
          const value = resultsVariable.value.trim();
          if (value) el.setAttribute('data-variable', value);
          else el.removeAttribute('data-variable');
        };
      }

      // Grid geometry: blank means "use the player default", so an emptied
      // field removes the attribute instead of exporting columns: "".
      const gridColumnsInput = document.getElementById('gridColumns');
      if (gridColumnsInput) {
        gridColumnsInput.value = el.getAttribute('data-columns') || '';
        gridColumnsInput.onchange = () => {
          const value = String(gridColumnsInput.value).trim();
          if (value !== '') el.setAttribute('data-columns', Math.max(1, Math.min(8, parseInt(value, 10) || 1)));
          else el.removeAttribute('data-columns');
        };
      }

      const gridRowsInput = document.getElementById('gridRows');
      if (gridRowsInput) {
        gridRowsInput.value = el.getAttribute('data-rows') || '';
        gridRowsInput.onchange = () => {
          const value = String(gridRowsInput.value).trim();
          if (value !== '') el.setAttribute('data-rows', Math.max(1, Math.min(12, parseInt(value, 10) || 1)));
          else el.removeAttribute('data-rows');
        };
      }
    } else {
      searchResultsProperties.style.display = 'none';
    }
  }

  // Input properties - the config key the player stores typed text under.
  const inputProperties = document.querySelector('.input-properties');
  if (inputProperties) {
    if (el.getAttribute('data-type') === 'input') {
      inputProperties.style.display = 'block';
      const inputVariable = document.getElementById('inputVariable');
      if (inputVariable) {
        inputVariable.value = el.getAttribute('data-variable') || '';
        inputVariable.onchange = () => {
          const value = inputVariable.value.trim();
          if (value) el.setAttribute('data-variable', value);
          else el.removeAttribute('data-variable');
        };
      }
    } else {
      inputProperties.style.display = 'none';
    }
  }

  function updateSourceBadges(activeSource) {
    const badges = document.querySelectorAll('.source-badge');
    badges.forEach(badge => {
      badge.classList.remove('active');
      badge.style.opacity = '0.4';
      badge.style.transform = 'scale(0.92)';
      badge.style.boxShadow = 'none';
    });
    const activeBadge = document.querySelector('.source-badge.' + activeSource);
    if (activeBadge) {
      activeBadge.classList.add('active');
      activeBadge.style.opacity = '1';
      activeBadge.style.transform = 'scale(1)';
      const colors = {
        'system': 'rgba(67, 97, 238, 0.35)',
        'zeroconf': 'rgba(46, 204, 113, 0.35)',
        'json': 'rgba(155, 89, 182, 0.35)'
      };
      activeBadge.style.boxShadow = `0 3px 12px ${colors[activeSource] || 'rgba(0,0,0,0.1)'}`;
    }
  }


  // Hide all trigger options first
  document.querySelectorAll('#triggerOptions > *').forEach(el => {
    el.style.display = 'none';
  });



  elementProperties.classList.add('visible');

  // Add null checks for all DOM elements
  const bgColorPicker = document.getElementById('bgColorPicker');
  if (!bgColorPicker) return;

  const bgColorGroup = bgColorPicker.closest('.control-group');
  if (!bgColorGroup) return;

  if (el.getAttribute('data-type') === 'label') {
    bgColorGroup.style.display = 'none';
    el.style.backgroundColor = 'transparent';
    el.removeAttribute('data-bg-color');
  } else {
    bgColorGroup.style.display = 'block';
  }

  // Position/size
  const datax = document.getElementById('datax');
  const datay = document.getElementById('datay');
  const dataWidth = document.getElementById('dataWidth');
  const dataHeight = document.getElementById('dataHeight');

  if (datax && datay && dataWidth && dataHeight) {
    datax.value = el.getAttribute('data-x') || 0;
    datay.value = el.getAttribute('data-y') || 0;
    dataWidth.value = el.getAttribute('data-width') || 100;
    dataHeight.value = el.getAttribute('data-height') || 100;

    // Update position/size when inputs change
    const updatePositionSize = () => {
      el.style.left = `${datax.value}px`;
      el.setAttribute('data-x', datax.value);
      el.style.top = `${datay.value}px`;
      el.setAttribute('data-y', datay.value);
      el.style.width = `${dataWidth.value}px`;
      el.setAttribute('data-width', dataWidth.value);
      el.style.height = `${dataHeight.value}px`;
      el.setAttribute('data-height', dataHeight.value);
    };

    [datax, datay, dataWidth, dataHeight].forEach(input => {
      if (input) input.oninput = updatePositionSize;
    });
  }

  // Text styling
  if (!['image', 'input'].includes(el.getAttribute('data-type'))) {
    const colorPicker = document.getElementById('colorPicker');
    const fontSizePicker = document.getElementById('fontSizePicker');

    colorPicker.value = el.getAttribute('data-color') || '#000000';
    colorPicker.oninput = () => {
      el.style.color = colorPicker.value;
      el.setAttribute('data-color', colorPicker.value);
    };

    fontSizePicker.value = el.getAttribute('data-font') || 'medium';
    fontSizePicker.onchange = () => {
      el.setAttribute('data-font', fontSizePicker.value);
      el.style.fontSize = getFontSize(fontSizePicker.value) + 'px';
    };

    if (el.getAttribute('data-type') !== 'label') {
      const bgColorPicker = document.getElementById('bgColorPicker');
      bgColorPicker.value = el.getAttribute('data-bg-color') || '#ffffff';
      bgColorPicker.oninput = () => {
        el.style.backgroundColor = bgColorPicker.value;
        el.setAttribute('data-bg-color', bgColorPicker.value);
      };
    }
  }

  // Transparency
  if (['image', 'button', 'video', 'input', 'collapsedlist'].includes(el.getAttribute('data-type'))) {
    const opacitySlider = document.getElementById('opacitySlider');
    const opacityValue = document.getElementById('opacityValue');

    // Get opacity from data attribute or style
    let opacity = el.getAttribute('data-opacity');
    if (!opacity) {
      // Extract opacity from style if not in data attribute
      const styleOpacity = parseFloat(el.style.opacity || 1);
      opacity = Math.round(styleOpacity * 100);
      el.setAttribute('data-opacity', opacity);
    }

    opacitySlider.value = opacity;
    opacityValue.textContent = `${opacity}%`;
    el.style.opacity = opacity / 100;

    opacitySlider.oninput = () => {
      const value = opacitySlider.value;
      el.style.opacity = value / 100;
      el.setAttribute('data-opacity', value);
      opacityValue.textContent = `${value}%`;
    };
  }

  // Trigger controls
  const triggerSelector = document.getElementById('triggerSelector');
  triggerSelector.value = el.getAttribute('data-trigger') || '';

  // Show relevant options based on selected trigger
  if (triggerSelector.value === 'change_scene') {
    document.getElementById('sceneChangeSelector').style.display = 'block';
    document.getElementById('sceneChangeSelector').value = el.getAttribute('data-scene-change') || '';
  } else if (triggerSelector.value === 'external_app') {
    document.getElementById('externalAppPath').style.display = 'block';
    document.getElementById('externalAppPath').value = el.getAttribute('data-external-app-path') || '';
    document.getElementById('externalAppReturnVar').style.display = 'block';
    document.getElementById('externalAppReturnVar').value = el.getAttribute('data-external-app-return') || '';
  } else if (triggerSelector.value === 'set_variable') {
    document.getElementById('variableChangeSelector').style.display = 'block';
    document.getElementById('variableChangeSelector').value = el.getAttribute('data-variable-change') || '';
    document.getElementById('variableChangeValue').style.display = 'block';
    document.getElementById('variableChangeValue').value = el.getAttribute('data-variable-change-value') || '';
  } else if (triggerSelector.value === 'play_video' || triggerSelector.value === 'play_image') {
    document.getElementById('mediaVariableSelector').style.display = 'block';

    // Set up media variable selector
    const mediaVariableSelector = document.getElementById('mediaVariableSelector');
    updateVariableSelector(mediaVariableSelector, el.getAttribute('data-media-variable') || '');
    mediaVariableSelector.onchange = () => {
      el.setAttribute('data-media-variable', mediaVariableSelector.value);
    };
  } else if (triggerSelector.value === 'youtube_search') {
    // The player reads the command override and result variable from the
    // element's TriggerTarget/TriggerValue (core.go planMediaSearchTrigger),
    // so both fields map onto those attributes.
    const ytSearchCommand = document.getElementById('ytSearchCommand');
    const ytSearchResultVar = document.getElementById('ytSearchResultVar');
    ytSearchCommand.style.display = 'block';
    ytSearchCommand.value = el.getAttribute('data-trigger-target') || '';
    ytSearchResultVar.style.display = 'block';
    ytSearchResultVar.value = el.getAttribute('data-trigger-value') || '';
  } else if (triggerSelector.value === 'play_video_from_var') {
    const playFromVarSelector = document.getElementById('playFromVarSelector');
    playFromVarSelector.style.display = 'block';
    updateVariableSelector(playFromVarSelector, el.getAttribute('data-trigger-target') || '');
  }


  // Add this change handler to the existing ones in showElementProperties function
  const mediaVariableSelectorEl = document.getElementById('mediaVariableSelector');
  if (mediaVariableSelectorEl) {
    mediaVariableSelectorEl.onchange = () => {
      el.setAttribute('data-media-variable', mediaVariableSelectorEl.value);
    };
  }

  // Update trigger change handler
  triggerSelector.onchange = () => {
    const value = triggerSelector.value;
    el.setAttribute('data-trigger', value);

    // Hide all options first
    document.querySelectorAll('#triggerOptions > *').forEach(el => {
      el.style.display = 'none';
    });

    // Show relevant options
    if (value === 'change_scene') {
      document.getElementById('sceneChangeSelector').style.display = 'block';
    } else if (value === 'external_app') {
      document.getElementById('externalAppPath').style.display = 'block';
      document.getElementById('externalAppReturnVar').style.display = 'block';
    } else if (value === 'set_variable') {
      document.getElementById('variableChangeSelector').style.display = 'block';
      document.getElementById('variableChangeValue').style.display = 'block';
      // In triggerSelector.onchange, update the play_video/play_image section:
    } else if (value === 'play_video' || value === 'play_image') {
      document.getElementById('mediaVariableSelector').style.display = 'block';

      // Set up media variable selector
      const mediaVariableSelector = document.getElementById('mediaVariableSelector');
      updateVariableSelector(mediaVariableSelector, el.getAttribute('data-media-variable') || '');
      mediaVariableSelector.onchange = () => {
        el.setAttribute('data-media-variable', mediaVariableSelector.value);
      };
    } else if (value === 'youtube_search') {
      const ytSearchCommand = document.getElementById('ytSearchCommand');
      const ytSearchResultVar = document.getElementById('ytSearchResultVar');
      ytSearchCommand.style.display = 'block';
      ytSearchCommand.value = el.getAttribute('data-trigger-target') || '';
      ytSearchResultVar.style.display = 'block';
      ytSearchResultVar.value = el.getAttribute('data-trigger-value') || '';
    } else if (value === 'play_video_from_var') {
      const playFromVarSelector = document.getElementById('playFromVarSelector');
      playFromVarSelector.style.display = 'block';
      updateVariableSelector(playFromVarSelector, el.getAttribute('data-trigger-target') || '');
    }
    // youtube_play / youtube_trending / play_focused carry no options.

    scheduleAutoSave();
  };

  const videoVariableEl = document.getElementById('videoVariable');
  if (videoVariableEl) {
    videoVariableEl.onchange = () => {
      el.setAttribute('data-video-variable', videoVariableEl.value);
    };
  }

  const imageVariableEl = document.getElementById('imageVariable');
  if (imageVariableEl) {
    imageVariableEl.onchange = () => {
      el.setAttribute('data-image-variable', imageVariableEl.value);
    };
  }

  // Set up change handlers for trigger options
  const sceneChangeSelectorEl = document.getElementById('sceneChangeSelector');
  if (sceneChangeSelectorEl) {
    sceneChangeSelectorEl.value = el.getAttribute('data-scene-change') || '';
    sceneChangeSelectorEl.onchange = () => {
      el.setAttribute('data-scene-change', sceneChangeSelectorEl.value);
    };
  }

  const externalAppPathEl = document.getElementById('externalAppPath');
  if (externalAppPathEl) {
    externalAppPathEl.value = el.getAttribute('data-external-app-path') || '';
    externalAppPathEl.onchange = () => {
      el.setAttribute('data-external-app-path', externalAppPathEl.value);
    };
  }

  const externalAppReturnVarEl = document.getElementById('externalAppReturnVar');
  if (externalAppReturnVarEl) {
    externalAppReturnVarEl.onchange = () => {
      el.setAttribute('data-external-app-return', externalAppReturnVarEl.value);
    };
  }

  const variableChangeSelectorEl = document.getElementById('variableChangeSelector');
  if (variableChangeSelectorEl) {
    variableChangeSelectorEl.onchange = () => {
      el.setAttribute('data-variable-change', variableChangeSelectorEl.value);
    };
  }

  const variableChangeValueEl = document.getElementById('variableChangeValue');
  if (variableChangeValueEl) {
    variableChangeValueEl.onchange = () => {
      el.setAttribute('data-variable-change-value', variableChangeValueEl.value);
    };
  }

  const ytSearchCommandEl = document.getElementById('ytSearchCommand');
  if (ytSearchCommandEl) {
    ytSearchCommandEl.onchange = () => {
      const value = ytSearchCommandEl.value.trim();
      if (value) el.setAttribute('data-trigger-target', value);
      else el.removeAttribute('data-trigger-target');
    };
  }

  const ytSearchResultVarEl = document.getElementById('ytSearchResultVar');
  if (ytSearchResultVarEl) {
    ytSearchResultVarEl.onchange = () => {
      const value = ytSearchResultVarEl.value.trim();
      if (value) el.setAttribute('data-trigger-value', value);
      else el.removeAttribute('data-trigger-value');
    };
  }

  const playFromVarSelectorEl = document.getElementById('playFromVarSelector');
  if (playFromVarSelectorEl) {
    playFromVarSelectorEl.onchange = () => {
      const value = playFromVarSelectorEl.value.trim();
      if (value) el.setAttribute('data-trigger-target', value);
      else el.removeAttribute('data-trigger-target');
    };
  }

  const videoPathEl = document.getElementById('videoPath');
  if (videoPathEl) {
    videoPathEl.onchange = () => {
      el.setAttribute('data-video-path', videoPathEl.value);
    };
  }

  const imagePathEl = document.getElementById('imagePath');
  if (imagePathEl) {
    imagePathEl.onchange = () => {
      el.setAttribute('data-image-path', imagePathEl.value);
    };
  }
}

// Menu Functions
function setupMenuEvents(menuEl) {
  // Remove button
  const removeButton = menuEl.querySelector('.remove-button');
  if (removeButton) {
    removeButton.addEventListener('click', (event) => {
      event.stopPropagation();
      menuEl.remove();
      const sceneElements = scenes[currentScene];
      const index = sceneElements.findIndex(item => item.isEqualNode(menuEl));
      if (index > -1) sceneElements.splice(index, 1);
    });
  }
}

function updateMenuSceneButtons(menuEl) {
  const sceneButtonsContainer = menuEl.querySelector('.menu-scene-buttons');
  if (!sceneButtonsContainer) return;

  sceneButtonsContainer.innerHTML = '';

  Object.keys(scenes).forEach(sceneName => {
    const button = document.createElement('button');
    button.className = 'menu-scene-button';
    if (sceneName === currentScene) button.classList.add('active');
    button.textContent = sceneName;
    button.addEventListener('click', () => {
      sceneSelector.value = sceneName;
      changeScene();
      menuEl.querySelectorAll('.menu-scene-button').forEach(btn => btn.classList.remove('active'));
      button.classList.add('active');
    });
    sceneButtonsContainer.appendChild(button);
  });
}

function updateMenuClock(clockEl) {
  if (!clockEl) return;

  const updateTime = () => {
    const now = new Date();
    const hours = now.getHours().toString().padStart(2, '0');
    const minutes = now.getMinutes().toString().padStart(2, '0');
    clockEl.textContent = `${hours}:${minutes}`;
  };

  updateTime();
  setInterval(updateTime, 60000);
}

// Variable Management
function addVariable() {
  const asked = prompt('Enter variable name:');
  if (!asked) return;
  const variableName = asked.trim();
  if (!variableName) return showToast('Variable name cannot be empty.', 'error');
  // The player only substitutes $name references made of letters, digits and
  // underscores, so a name it can never read would be a silent dead end.
  if (!/^[a-zA-Z_][a-zA-Z0-9_]*$/.test(variableName)) {
    return showToast(`Use letters, digits or underscores — "${variableName}" is not referable in text.`, 'error');
  }
  if (variables[variableName] !== undefined) return showToast(`Variable "${variableName}" already exists.`, 'info');

  pushUndo('add variable');
  variables[variableName] = '';

  // Create variable item
  const variableItem = createVariableItem(variableName, variables[variableName]);

  variablesList.appendChild(variableItem);

  document.querySelectorAll('.dynamic-variable-selector').forEach(selector => {
    const currentValue = selector.value;
    updateVariableSelector(selector, currentValue);
  });

  updateVariableChangeSelector();
  scheduleAutoSave();
  scheduleValidationRefresh();
}

function editVariable(name) {
  const newValue = prompt(`Enter new value for ${name}:`, variables[name]);
  if (newValue !== null && newValue !== variables[name]) {
    pushUndo('edit variable');
    variables[name] = newValue;

    // Update UI
    document.querySelectorAll('.variable-item').forEach(item => {
      if (item.querySelector('.variable-name').textContent === name) {
        item.querySelector('.variable-value').textContent = renderVariablePanelValue(newValue);
      }
    });

    // Update all elements with variables
    document.querySelectorAll('.text-content').forEach(textEl => {
      processTextForVariables(textEl);
    });
    scheduleAutoSave();
    scheduleValidationRefresh();
  }
}

function deleteVariable(name) {
  if (confirm(`Delete variable ${name}?`)) {
    pushUndo('delete variable');
    delete variables[name];

    // Remove from UI
    document.querySelectorAll('.variable-item').forEach(item => {
      if (item.querySelector('.variable-name').textContent === name) {
        item.remove();
      }
    });

    document.querySelectorAll('.dynamic-variable-selector').forEach(selector => {
      const currentValue = selector.value === name ? '' : selector.value;
      updateVariableSelector(selector, currentValue);
    });

    // Update variable change selector
    updateVariableChangeSelector();

    // Update all elements with variables
    document.querySelectorAll('.text-content').forEach(textEl => {
      processTextForVariables(textEl);
    });
    scheduleAutoSave();
    scheduleValidationRefresh();
  }
}

function updateVariableChangeSelector() {
  const selector = document.getElementById('variableChangeSelector');
  selector.innerHTML = '';

  Object.keys(variables).forEach(variableName => {
    const option = document.createElement('option');
    option.value = variableName;
    option.textContent = variableName;
    selector.appendChild(option);
  });
}

function updateSceneChangeSelector() {
  const selector = document.getElementById('sceneChangeSelector');
  selector.innerHTML = '';

  Object.keys(scenes).forEach(sceneName => {
    const option = document.createElement('option');
    option.value = sceneName;
    option.textContent = sceneName;
    selector.appendChild(option);
  });
}

// Process text for variables and add tooltips
function processTextForVariables(textElement) {
  let text = textElement.textContent;
  const regex = /\$([a-zA-Z_][a-zA-Z0-9_]*)/g;
  let variablesUsed = {};
  let match;

  // Find all unique variables in the text
  while ((match = regex.exec(text)) !== null) {
    const varName = match[1];
    variablesUsed[varName] = variables[varName] || '""';
  }

  // If no variables, remove any existing tooltip and return
  if (Object.keys(variablesUsed).length === 0) {
    textElement.parentElement.classList.remove('has-variables');
    return;
  }

  // Add has-variables class for styling
  textElement.parentElement.classList.add('has-variables');

  // Format the tooltip text with evaluated values
  let evaluatedText = text;
  for (const [varName, varValue] of Object.entries(variablesUsed)) {
    evaluatedText = evaluatedText.replace(`$${varName}`, varValue);
  }

  const tooltipText = `Evaluated: ${evaluatedText}\n\nVariables:\n${Object.entries(variablesUsed)
    .map(([name, value]) => `${name}: ${value}`)
    .join('\n')}`;

  // Remove any existing event listeners
  const parentEl = textElement.parentElement;
  parentEl.removeEventListener('mouseenter', parentEl._tooltipMouseEnter);
  parentEl.removeEventListener('mouseleave', parentEl._tooltipMouseLeave);

  // Add new event listeners using the global tooltip
  parentEl._tooltipMouseEnter = function (e) {
    globalTooltip.textContent = tooltipText;
    globalTooltip.style.display = 'block';

    const rect = parentEl.getBoundingClientRect();
    const scrollTop = window.pageYOffset || document.documentElement.scrollTop;

    globalTooltip.style.left = `${rect.left + (rect.width / 2) - (globalTooltip.offsetWidth / 2)}px`;
    globalTooltip.style.top = `${rect.top + scrollTop - globalTooltip.offsetHeight - 5}px`;

    // Ensure tooltip stays within viewport
    const tooltipRect = globalTooltip.getBoundingClientRect();
    if (tooltipRect.left < 5) globalTooltip.style.left = '5px';
    if (tooltipRect.right > window.innerWidth - 5) {
      globalTooltip.style.left = `${window.innerWidth - tooltipRect.width - 5}px`;
    }
  };

  parentEl._tooltipMouseLeave = function (e) {
    globalTooltip.style.display = 'none';
  };

  parentEl.addEventListener('mouseenter', parentEl._tooltipMouseEnter);
  parentEl.addEventListener('mouseleave', parentEl._tooltipMouseLeave);
}

// File Operations
function setBackground() {
  const file = backgroundFileInput.files[0];
  if (!file) return;

  const reader = new FileReader();
  reader.onload = (e) => {
    canvas.style.backgroundImage = `url(${e.target.result})`;
    canvas.style.backgroundSize = 'cover';
    backgroundPath = e.target.result;
  };
  reader.readAsDataURL(file);
}

function getFontSize(fontSize) {
  const sizes = {
    title: parseInt(titleSizeInput.value) || 48,
    big: parseInt(bigSizeInput.value) || 36,
    medium: parseInt(mediumSizeInput.value) || 24,
    small: parseInt(smallSizeInput.value) || 18
  };

  return sizes[fontSize] || 24;
}

// === Config round-trip fidelity ===
// The builder models only part of the player's config. Everything it does not
// model is kept verbatim on import (per element, per scene, and at the top
// level) so exporting can never silently drop part of a design it just loaded.
//
// Round-trip is the design goal: load a config through the browser's XML or JSON
// import, edit nothing, and export the same format — the resulting file must load
// through the player's Go loaders identically to the original. The tests in the
// player/ harness (harness_gen.go) verify both formats, and the editor's own
// export paths are the equivalent in-browser check: an exported JSON must round
// trip through the same JSON loader, and an exported XML must round trip through
// the same XML loader.

const RAW_ELEMENT_ATTR = 'data-raw';
let importedConfig = { top: {}, variables: {}, scenes: {} };
let importedConfigSnapshot = null;

function refreshReloadButtonState() {
  const btn = document.getElementById('reloadConfigBtn');
  if (!btn) return;
  const canReload = Boolean(importedConfigSnapshot);
  btn.disabled = !canReload;
  btn.setAttribute('aria-disabled', String(!canReload));
}

const IMPORTED_TOP_KEYS = ['AppName', 'Version', 'Width', 'Height', 'Background', 'FontPath', 'channel_profile'];
// Variables the builder edits through its own controls (background picker and
// the font-size inputs). Every other imported variable is written back out
// untouched: a fixed allow-list here used to drop settings the panel never
// showed - inputColor, weatherUnit, screenWidth, fileExplorerRoot, and the
// colour half of a config whose colours live under variables.Custom.
const EDITOR_OWNED_VARIABLES = ['backgroundImage', 'fontSizes'];

// The control default each font role starts with, so an untouched control is
// not mistaken for a design decision.
const DEFAULT_FONT_SIZES = { title: 48, big: 36, medium: 24, small: 18 };

// Font sizes the design actually sets: a role is exported when the import had
// it (the user may have edited the value since) or when the input no longer
// holds the default. Writing all four unconditionally invented settings for a
// config that never specified them.
function exportFontSizes() {
  const imported = (importedConfig.variables && importedConfig.variables.fontSizes) || {};
  const out = {};
  for (const role of Object.keys(DEFAULT_FONT_SIZES)) {
    const input = document.getElementById(role + 'Size');
    if (!input) continue;
    const value = parseInt(input.value, 10);
    if (Number.isNaN(value)) continue;
    if (imported[role] !== undefined || value !== DEFAULT_FONT_SIZES[role]) out[role] = value;
  }
  return out;
}

function resetImportedConfig(data) {
  importedConfig = { top: {}, variables: {}, scenes: {} };
  if (!data || typeof data !== 'object') return;
  IMPORTED_TOP_KEYS.forEach(key => {
    if (data[key] !== undefined) importedConfig.top[key] = data[key];
  });
  if (data.variables && typeof data.variables === 'object') {
    importedConfig.variables = JSON.parse(JSON.stringify(data.variables));
  }
  (data.scenes || []).forEach(scene => {
    const extras = {};
    Object.keys(scene || {}).forEach(key => {
      if (key !== 'name' && key !== 'elements') extras[key] = scene[key];
    });
    importedConfig.scenes[scene.name] = extras;
  });
}

function preservedVariables() {
  const out = {};
  const imported = importedConfig.variables || {};
  for (const key of Object.keys(imported)) {
    if (EDITOR_OWNED_VARIABLES.includes(key)) continue;
    const val = imported[key];
    // Mirror the player's flattenCustomBlocks: nested <Custom>...</Custom>
    // blocks the web builder emits get merged at save time so the exported
    // JSON has the same shape the player's Variables loader produces.
    if (val && typeof val === 'object' && !Array.isArray(val) && !(val instanceof HTMLElement)) {
      if (Object.keys(val).length > 0) {
        out[key] = val;
        continue;
      }
    }
    out[key] = val;
  }
  return out;
}

function elementRawData(el) {
  const raw = el.getAttribute(RAW_ELEMENT_ATTR);
  if (!raw) return {};
  try { return JSON.parse(decodeURIComponent(raw)) || {}; } catch (e) { return {}; }
}

function setElementRawData(el, data) {
  try {
    el.setAttribute(RAW_ELEMENT_ATTR, encodeURIComponent(JSON.stringify(data || {})));
  } catch (e) { /* ignore oversized/unserialisable payloads */ }
}

// The label the canvas paints for an element that carries no text of its own.
function synthesizedTextFor(el) {
  const type = (el.getAttribute('data-type') || '').toLowerCase();
  if (type === 'textbrowser') {
    const source = el.getAttribute('data-source') || 'system';
    const icons = { system: '🖥️', zeroconf: '🔍', json: '📋' };
    const names = { system: 'System', zeroconf: 'Zeroconf', json: 'JSON' };
    return `${icons[source] || '🌐'} ${names[source] || 'Text Browser'}`;
  }
  return type ? type.charAt(0).toUpperCase() + type.slice(1) : '';
}

// One config element built from the live DOM, starting from whatever the import
// preserved so fields the builder does not model survive a round trip.
function elementToConfig(el) {
  const element = Object.assign({}, elementRawData(el));
  const type = (el.getAttribute('data-type') || '').toLowerCase();
  const attr = (name) => el.getAttribute(name);
  const assign = (key, value) => {
    if (value === null || value === undefined || value === '') delete element[key];
    else element[key] = value;
  };
  const assignInt = (key, raw) => {
    if (raw === null || raw === undefined || raw === '') { delete element[key]; return; }
    const n = parseInt(raw, 10);
    if (Number.isNaN(n)) delete element[key]; else element[key] = n;
  };

  assign('type', attr('data-type'));
  if (attr('data-x') !== null) assignInt('x', attr('data-x'));
  if (attr('data-y') !== null) assignInt('y', attr('data-y'));
  // An empty data-width/height means "let the player size it", which is how a
  // config that omits the size is represented.
  if (el.hasAttribute('data-width')) assignInt('width', attr('data-width'));
  if (el.hasAttribute('data-height')) assignInt('height', attr('data-height'));
  assign('color', attr('data-color'));
  assign('bgColor', attr('data-bg-color'));
  // The font-role control defaults to "medium", and the player sizes an element
  // with no role from its own default instead; the two agree only while the
  // config defines no "medium" size, so a role the import never declared is not
  // written back out.
  const rawFont = elementRawData(el).font;
  if (attr('data-font') !== null && (rawFont !== undefined || attr('data-font') !== 'medium')) {
    assign('font', attr('data-font'));
  }

  // Opacity: the canvas always carries a value, so an untouched default (100%)
  // is not a design decision and is left out - the player reads an absent
  // opacity as fully opaque, exactly like 1.
  const rawOpacity = elementRawData(el).opacity;
  const opacity = attr('data-opacity');
  if (opacity !== null && opacity !== '' && !(rawOpacity === undefined && opacity === '100')) {
    element.opacity = parseInt(opacity, 10) / 100;
  }

  assign('trigger', attr('data-trigger'));
  // A scene link can be stored either as trigger "change_scene" plus a
  // sceneChange target, or as the "change_scene:Target" shorthand the shipped
  // config uses. Keep the target whether or not the trigger dropdown happens to
  // be set, otherwise an imported sceneChange was silently dropped on export.
  assign('sceneChange', attr('data-scene-change'));
  if (element.trigger === 'external_app') {
    assign('externalAppPath', attr('data-external-app-path'));
    assign('externalAppReturn', attr('data-external-app-return'));
  } else if (element.trigger === 'set_variable') {
    assign('variableChange', attr('data-variable-change'));
    assign('variableChangeValue', attr('data-variable-change-value'));
  } else if (element.trigger === 'play_video' || element.trigger === 'play_image') {
    assign('mediaVariable', attr('data-media-variable'));
  }

  assign('triggerValue', attr('data-trigger-value'));
  // Target of youtube_search (command override), play_video_from_var (variable
  // name) and other trigger payloads; the player decodes it per trigger.
  assign('triggerTarget', attr('data-trigger-target'));
  assign('variable', attr('data-variable'));
  assign('command', attr('data-command'));
  assign('listVariable', attr('data-list-variable'));
  assign('placeholder', attr('data-placeholder'));
  assign('style', attr('data-style'));
  assign('icon', attr('data-icon'));
  // A text browser's source control defaults to "system", which is what the
  // player uses when the field is absent; only a declared or changed source is
  // part of the design.
  const rawSource = elementRawData(el).source;
  if (attr('data-source') !== null && (rawSource !== undefined || attr('data-source') !== 'system')) {
    assign('source', attr('data-source'));
  }
  assign('jsonPath', attr('data-json-path'));
  assign('video', attr('data-video'));
  assign('videoVariable', attr('data-video-variable'));
  assign('mediaVariable', attr('data-media-variable'));
  if (attr('data-auto-refresh') !== null) element.autoRefresh = attr('data-auto-refresh') === 'true';
  if (attr('data-columns') !== null) assignInt('columns', attr('data-columns'));
  if (attr('data-rows') !== null) assignInt('rows', attr('data-rows'));

  // Text: a label the canvas synthesized is not part of the design.
  if (type === 'input') {
    const input = el.querySelector('.element-input');
    if (input) assign('text', input.value);
  } else {
    const textSpan = el.querySelector('.text-content');
    if (textSpan) {
      const synthesized = el.getAttribute('data-text-auto') === '1' &&
        textSpan.textContent === synthesizedTextFor(el);
      if (synthesized) assign('text', elementRawData(el).text || '');
      else assign('text', textSpan.textContent);
    }
  }

  if (type === 'image') {
    const img = el.querySelector('.element-image');
    if (img && img.src) element.image = img.src;
  }

  return element;
}

// Every element attribute the builder owns, applied from the imported config.
// This is the mirror of elementToConfig: whatever the builder writes to the DOM
// on import must survive a round trip back to the same config shape, so the
// export path and the import path are kept in sync by hand and by the harness.
function applyElementConfigAttributes(el, data) {
  const set = (name, value) => {
    if (value === null || value === undefined) return;
    if (value === '') { el.removeAttribute(name); return; }
    el.setAttribute(name, String(value));
  };

  set('data-trigger', data.trigger);
  set('data-trigger-value', data.triggerValue);
  set('data-trigger-target', data.triggerTarget);
  set('data-scene-change', data.sceneChange);
  set('data-external-app-path', data.externalAppPath);
  set('data-external-app-return', data.externalAppReturn);
  set('data-variable-change', data.variableChange);
  set('data-variable-change-value', data.variableChangeValue);
  set('data-media-variable', data.mediaVariable);
  set('data-video-variable', data.videoVariable);
  set('data-video', data.video);
  set('data-media-variable', data.mediaVariable);
  set('data-variable', data.variable);
  set('data-list-variable', data.listVariable);
  set('data-command', data.command);
  set('data-placeholder', data.placeholder);
  set('data-style', data.style);
  set('data-icon', data.icon);
  set('data-source', data.source);
  set('data-json-path', data.jsonPath);
  set('data-image', data.image);
  if (data.autoRefresh !== undefined) set('data-auto-refresh', data.autoRefresh ? 'true' : 'false');
  if (data.columns !== undefined) set('data-columns', data.columns);
  if (data.rows !== undefined) set('data-rows', data.rows);

  // Colours/fonts were previously only kept for buttons and labels, so every
  // other element type lost them on export.
  set('data-color', data.color);
  set('data-bg-color', data.bgColor);
  set('data-font', data.font);

  if (data.color) el.style.color = data.color;
  if (data.bgColor) el.style.background = data.bgColor;
  if (data.font) el.style.fontSize = getFontSize(data.font) + 'px';
}

// The single source of truth for both exporters, so the JSON and XML exports can
// never drift apart again. The config the browser serialises is the same shape
// the player's loaders accept, including the metadata fields they both read.
function buildEditorConfig() {
  // Export must reflect the live canvas: scenes[] only holds snapshots taken by
  // undo/autosave/scene operations, so sync before serialising.
  saveCurrentScene();
  // Variables the panel owns are merged last so its current state wins; the
  // rest comes back from the import unchanged.
  const exportedVariables = Object.assign({}, preservedVariables(), variables);
  if (backgroundPath) exportedVariables.backgroundImage = backgroundPath;
  const fontSizes = exportFontSizes();
  if (Object.keys(fontSizes).length > 0) exportedVariables.fontSizes = fontSizes;

  return Object.assign({}, importedConfig.top, {
    // The player reads Width/Height for its logical canvas. The builder's size
    // controls therefore have to be written here, not only applied to the CSS
    // canvas, or a design authored at 1024x768 still loaded as 1280x720.
    Width: canvasWidth,
    Height: canvasHeight,
    title: document.getElementById('title').value,
    author: document.getElementById('author').value,
    description: document.getElementById('description').value,
    variables: exportedVariables,
    scenes: Object.keys(scenes).map(sceneName => Object.assign({}, importedConfig.scenes[sceneName] || {}, {
      name: sceneName,
      elements: scenes[sceneName].map(el => elementToConfig(el))
    }))
  });
}

// ============================================================================
// Import normalisation and design validation
// ============================================================================
// A jukaconfig.json in the wild is not always the exact shape the builder writes:
// the Go player accepts numeric strings, the XML path emits strings, older files
// omit Width/Height, and hand-edited files can repeat a scene name. Import used
// to trust the file completely, so a single bad element aborted the whole load
// with "Cannot read properties of undefined". This normalises first and reports
// what it changed.

// Every type the Go renderer draws (player/core.go), plus "menu", which is the
// builder's own scene bar. Keeping this list in sync matters: a config that uses
// a feature tile the builder does not expose is still valid on the device.
const PLAYER_ELEMENT_TYPES = [
  'button', 'label', 'image', 'input', 'menu', 'video', 'dynamiclist',
  'textlog', 'textbrowser', 'searchresults', 'piechart', 'favorites',
  'unitconverter', 'chat', 'shortslist', 'canvas', 'packagelist', 'recent',
  'themegallery', 'toggle'
];
const FONT_ROLES = ['title', 'big', 'medium', 'small'];

// Scenes the Go player builds UI for at runtime (registerScene in about.go,
// calculator.go, dashboard.go, …). A config ships them as menu targets with an
// empty elements list on purpose, so an empty scene is not a mistake for these.
// Keep this in sync with the registerScene init() calls in player/*.go.
const PLAYER_BUILT_SCENES = [
  'Alarm', 'Calculator', 'About', 'Dashboard', 'Dictionary', 'Games',
  'JukaLand', 'MusicPlayer', 'Notes', 'Patch', 'Pomodoro', 'PDFReader', 'Terminal'
];

// Element types whose renderer fills its own background and lays out its own
// text, so naive contrast and text-width checks against the scene colours do
// reflect nothing the player actually draws.
const PLAYER_SELF_PAINTING_TYPES = ['textbrowser', 'chat', 'shortslist', 'canvas', 'themegallery'];
const DEFAULT_SCENE_WIDTH = 1280;
const DEFAULT_SCENE_HEIGHT = 720;

function toIntOrNull(value) {
  if (value === null || value === undefined || value === '') return null;
  if (typeof value === 'number') return Number.isFinite(value) ? Math.round(value) : null;
  const text = String(value).trim().replace(/px$/i, '');
  if (text === '') return null;
  const n = Number(text);
  return Number.isFinite(n) ? Math.round(n) : null;
}

function toNumberOrNull(value) {
  if (value === null || value === undefined || value === '') return null;
  const n = Number(value);
  return Number.isFinite(n) ? n : null;
}

function uniqueSceneName(name, taken) {
  const base = String(name || '').trim() || 'Scene';
  if (!taken.has(base)) return base;
  let index = 2;
  while (taken.has(`${base} (${index})`)) index += 1;
  return `${base} (${index})`;
}

function normalizeElement(raw) {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
  const type = String(raw.type || '').trim().toLowerCase();
  if (!type) return null;
  const element = Object.assign({}, raw);
  element.type = type === 'menu-element' ? 'menu' : type;
  element.x = toIntOrNull(raw.x) ?? 0;
  element.y = toIntOrNull(raw.y) ?? 0;
  // null means "the player decides the size", which is different from 0.
  element.width = toIntOrNull(raw.width);
  element.height = toIntOrNull(raw.height);
  if (typeof raw.text === 'number' || typeof raw.text === 'boolean') element.text = String(raw.text);
  const opacity = toNumberOrNull(raw.opacity);
  if (opacity === null) delete element.opacity;
  else element.opacity = Math.min(1, Math.max(0, opacity));
  if (raw.columns !== undefined) element.columns = toIntOrNull(raw.columns) ?? undefined;
  if (raw.rows !== undefined) element.rows = toIntOrNull(raw.rows) ?? undefined;
  if (element.columns === undefined) delete element.columns;
  if (element.rows === undefined) delete element.rows;
  if (raw.autoRefresh !== undefined) element.autoRefresh = raw.autoRefresh === true || raw.autoRefresh === 'true';
  return element;
}

// Accepts a JSON object (or XML already converted to one) and returns a safe
// config plus a report of everything that had to be repaired.
function normalizeImportedConfig(raw) {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) {
    throw new Error('The file does not contain a JukaHub config object.');
  }
  // Some files wrap the config in <jukaconfig> when they were converted badly.
  const source = (raw.jukaconfig && typeof raw.jukaconfig === 'object' && !Array.isArray(raw.jukaconfig))
    ? raw.jukaconfig : raw;

  let scenesInput = source.scenes;
  if (!scenesInput) throw new Error('This config has no "scenes". Is it a JukaHub config?');
  if (!Array.isArray(scenesInput)) {
    // A map of {sceneName: {elements: [...]}} is what older exports produced.
    if (typeof scenesInput === 'object') {
      scenesInput = Object.entries(scenesInput).map(([name, scene]) =>
        Object.assign({}, scene && typeof scene === 'object' ? scene : {}, { name }));
    } else {
      throw new Error('This config\'s "scenes" is not a list or an object.');
    }
  }

  const report = {
    scenes: 0,
    elements: 0,
    skippedElements: 0,
    renamedScenes: [],
    fixedFields: 0,
    warnings: []
  };
  const takenNames = new Set();
  const scenes = [];

  scenesInput.forEach((rawScene, index) => {
    if (!rawScene || typeof rawScene !== 'object') {
      report.warnings.push(`Scene ${index + 1} is not an object and was skipped.`);
      return;
    }
    const requested = typeof rawScene.name === 'string' ? rawScene.name.trim() : '';
    const name = uniqueSceneName(requested || `Scene ${index + 1}`, takenNames);
    if (requested && name !== requested) report.renamedScenes.push(`${requested} → ${name}`);
    takenNames.add(name);

    const elementsInput = Array.isArray(rawScene.elements) ? rawScene.elements : [];
    const elements = [];
    elementsInput.forEach(rawElement => {
      const element = normalizeElement(rawElement);
      if (!element) {
        report.skippedElements += 1;
        return;
      }
      // Numeric strings are valid to the Go loader ("x": "30"), so converting
      // them is not a repair and should not inflate the import report. Only a
      // value that could not be a number is counted.
      ['x', 'y', 'width', 'height', 'columns', 'rows'].forEach(key => {
        const value = rawElement[key];
        if (value === undefined || value === null || value === '') return;
        if (toIntOrNull(value) === null) report.fixedFields += 1;
      });
      elements.push(element);
    });

    scenes.push(Object.assign({}, rawScene, { name, elements }));
    report.scenes += 1;
    report.elements += elements.length;
  });

  if (scenes.length === 0) throw new Error('This config has no usable scenes.');

  const variables = (source.variables && typeof source.variables === 'object' && !Array.isArray(source.variables))
    ? JSON.parse(JSON.stringify(source.variables))
    : {};

  const width = toIntOrNull(source.Width ?? source.width ?? source.canvasWidth);
  const height = toIntOrNull(source.Height ?? source.height ?? source.canvasHeight);

  return {
    config: Object.assign({}, source, {
      scenes,
      variables,
      Width: width && width > 0 ? width : DEFAULT_SCENE_WIDTH,
      Height: height && height > 0 ? height : DEFAULT_SCENE_HEIGHT
    }),
    report
  };
}

function importReportMessage(report) {
  const parts = [`${report.scenes} scene${report.scenes === 1 ? '' : 's'}`, `${report.elements} element${report.elements === 1 ? '' : 's'} imported`];
  if (report.skippedElements) parts.push(`${report.skippedElements} invalid element${report.skippedElements === 1 ? '' : 's'} skipped`);
  if (report.fixedFields) parts.push(`${report.fixedFields} field${report.fixedFields === 1 ? '' : 's'} repaired`);
  if (report.renamedScenes.length) parts.push(`renamed: ${report.renamedScenes.join(', ')}`);
  return parts.join(' · ');
}

let lastImportReport = null;

function parseConfigText(text, fileName) {
  const trimmed = String(text || '').trim();
  if (!trimmed) throw new Error('The file is empty.');
  const looksXml = /\.xml$/i.test(fileName || '') || trimmed.startsWith('<?xml') || trimmed.startsWith('<');
  if (looksXml) return xmlToJson(text);
  try {
    return JSON.parse(text);
  } catch (e) {
    throw new Error('JSON could not be parsed — ' + e.message);
  }
}

// One import path for the Load button and for dropping a file on the canvas, so
// a dropped file and a picked file behave identically.
// A real jukaconfig is tens of kilobytes. Reading a huge or wrong file (a video
// dropped on the canvas, say) as text would freeze the tab, so refuse it first.
const MAX_CONFIG_BYTES = 5 * 1024 * 1024;

function importConfigFile(file) {
  if (!file) return;
  if (file.size > MAX_CONFIG_BYTES) {
    const mb = (file.size / (1024 * 1024)).toFixed(1);
    showToast(`That file is ${mb} MB, too large for a JukaHub config (limit 5 MB)`, 'error');
    announce('Import failed: file too large');
    return;
  }
  const reader = new FileReader();
  reader.onerror = () => showToast('Could not read that file', 'error');
  reader.onload = event => {
    try {
      const parsed = parseConfigText(String(event.target.result || ''), file.name || '');
      const { config, report } = normalizeImportedConfig(parsed);
      if (autoSaveTimer) {
        clearTimeout(autoSaveTimer);
        autoSaveTimer = null;
      }
      importedConfigSnapshot = JSON.parse(JSON.stringify(config));
      loadJukaApp(config);
      scheduleAutoSave();
      refreshReloadButtonState();
      scheduleValidationRefresh();

      const repaired = report.skippedElements > 0 || report.renamedScenes.length > 0 || report.warnings.length > 0;
      showToast(importReportMessage(report), repaired ? 'info' : 'success');
      if (report.warnings.length) showToast(report.warnings[0], 'warning');
      announce(`Imported ${file.name || 'config'}: ${importReportMessage(report)}.`);
    } catch (error) {
      showToast('Error loading config: ' + error.message, 'error');
      announce('Import failed: ' + error.message);
    }
  };
  reader.readAsText(file);
}

function setupConfigDrop() {
  const dropZone = document.getElementById('canvasWrapper') || canvas;
  if (!dropZone) return;
  const acceptsFiles = event => Array.from(event.dataTransfer?.types || []).includes('Files');
  dropZone.addEventListener('dragover', event => {
    if (!acceptsFiles(event)) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = 'copy';
    dropZone.classList.add('config-drop-active');
  });
  dropZone.addEventListener('dragleave', event => {
    if (event.target === dropZone) dropZone.classList.remove('config-drop-active');
  });
  dropZone.addEventListener('drop', event => {
    if (!acceptsFiles(event)) return;
    event.preventDefault();
    dropZone.classList.remove('config-drop-active');
    const file = event.dataTransfer.files && event.dataTransfer.files[0];
    if (file) importConfigFile(file);
  });
}

// --- Colour helpers (the player accepts #rrggbb and "r,g,b") -----------------
function parseConfigColour(value) {
  if (typeof value !== 'string') return null;
  const v = value.trim().toLowerCase();
  if (!v) return null;
  let m = v.match(/^#([0-9a-f]{3,8})$/);
  if (m) {
    let hex = m[1];
    if (hex.length === 3) hex = hex.split('').map(c => c + c).join('');
    if (hex.length !== 6 && hex.length !== 8) return null;
    return { r: parseInt(hex.slice(0, 2), 16), g: parseInt(hex.slice(2, 4), 16), b: parseInt(hex.slice(4, 6), 16) };
  }
  m = v.match(/^rgba?\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})/);
  if (!m) m = v.match(/^(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})$/);
  if (m) {
    const rgb = [Number(m[1]), Number(m[2]), Number(m[3])];
    if (rgb.some(n => n > 255)) return null;
    return { r: rgb[0], g: rgb[1], b: rgb[2] };
  }
  // Named CSS colours are valid for the browser preview; the player only
  // understands hex/rgb, so flag them without trying to compute contrast.
  if (/^[a-z]+$/.test(v)) return { named: v };
  return null;
}

function colourIsCss(value) {
  if (typeof value !== 'string') return false;
  const v = value.trim().toLowerCase();
  if (!v) return false;
  // Colours are commonly "$buttonColor": the player substitutes variables when
  // it draws, so a reference is valid and cannot be checked statically.
  if (/^\$[a-z_][a-z0-9_]*$/i.test(v)) return true;
  if (parseConfigColour(v)) return true;
  return /^hsla?\(/.test(v) || /^var\(--/.test(v) || /^(transparent|currentcolor)$/.test(v);
}

function luminance(rgb) {
  const channel = c => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  };
  return 0.2126 * channel(rgb.r) + 0.7152 * channel(rgb.g) + 0.0722 * channel(rgb.b);
}

function contrastRatio(a, b) {
  if (!a || !b || a.named || b.named) return null;
  const la = luminance(a);
  const lb = luminance(b);
  const light = Math.max(la, lb);
  const dark = Math.min(la, lb);
  return (light + 0.05) / (dark + 0.05);
}

// Rough text width for clipping warnings; the builder canvas uses Inter 900, but
// an estimate keeps validation pure and fast across every element.
function estimateTextWidth(text, fontSize) {
  return String(text || '').length * fontSize * 0.56;
}

// The single validation pass used by the Check panel and the export gate. It
// checks what actually breaks on the handheld, not style preferences: values the
// Go loader rejects, scene links that go nowhere, text that will not fit, and
// controls too small to hit with a thumb.
function validateProject(config) {
  const issues = [];
  const add = (severity, code, message, fix, where, target) => {
    issues.push(Object.assign({ severity, code, message, fix: fix || '', where: where || '' }, target || {}));
  };

  if (!config || typeof config !== 'object') {
    add('error', 'config', 'There is no design to check.', 'Add a scene and try again.');
    return { issues, errors: 1, warnings: 0 };
  }
  const sceneList = Array.isArray(config.scenes) ? config.scenes : [];
  if (sceneList.length === 0) {
    add('error', 'no-scenes', 'This design has no scenes.', 'Add a scene from the canvas toolbar.');
    return { issues, errors: 1, warnings: 0 };
  }

  const canvasW = toIntOrNull(config.Width) || canvasWidth || DEFAULT_SCENE_WIDTH;
  const canvasH = toIntOrNull(config.Height) || canvasHeight || DEFAULT_SCENE_HEIGHT;
  const sceneNames = new Set();
  const duplicateNames = new Set();
  sceneList.forEach(scene => {
    const name = String(scene && scene.name || '').trim();
    if (!name) return;
    if (sceneNames.has(name)) duplicateNames.add(name);
    sceneNames.add(name);
  });
  const fontSizes = (config.variables && config.variables.fontSizes) || {};
  FONT_ROLES.forEach(role => {
    if (fontSizes[role] === undefined) return;
    const size = toIntOrNull(fontSizes[role]);
    if (size === null || size < 8 || size > 96) {
      add('warning', 'font-size', `The "${role}" font size (${fontSizes[role]}) is outside 8–96 px.`, 'Pick a size between 8 and 96 px in App Info.', 'Font sizes');
    }
  });

  const allVariables = Object.assign({}, (config.variables && config.variables.Custom) || {}, config.variables || {});

  sceneList.forEach((scene, sceneIndex) => {
    const sceneName = String(scene && scene.name || '').trim();
    const whereScene = sceneName ? `Scene “${sceneName}”` : `Scene ${sceneIndex + 1}`;
    if (!sceneName) {
      add('error', 'scene-name', 'A scene has no name.', 'Rename the scene so the menu and scene links can target it.', whereScene, { sceneIndex });
    } else if (duplicateNames.has(sceneName)) {
      add('error', 'scene-duplicate', `Two scenes are both named “${sceneName}”.`, 'Rename one scene; the player can only open one of them.', whereScene, { sceneIndex });
    }
    if (scene.background && !colourIsCss(scene.background)) {
      add('warning', 'scene-background', `Scene background “${scene.background}” is not a colour the player understands.`, 'Use #rrggbb or an image path for the background.', whereScene, { sceneIndex });
    }

    const elements = Array.isArray(scene.elements) ? scene.elements : [];
    if (elements.length === 0 && !PLAYER_BUILT_SCENES.includes(sceneName)) {
      add('warning', 'scene-empty', 'This scene has no elements.', 'Add a label or a button, or remove the scene.', whereScene, { sceneIndex });
    }
    let menuRect = null;
    elements.forEach(el => {
      if (String(el.type || '').toLowerCase() === 'menu') {
        menuRect = { x: 0, y: toIntOrNull(el.y) || 0, w: canvasW, h: toIntOrNull(el.height) || 50 };
      }
    });

    elements.forEach((el, elementIndex) => {
      const type = String(el.type || '').toLowerCase();
      const text = el.text === undefined || el.text === null ? '' : String(el.text);
      const label = text.trim() || type || 'element';
      const where = `${whereScene} · ${label.length > 32 ? label.slice(0, 32) + '…' : label}`;
      const target = { sceneIndex, elementIndex };

      if (!type) {
        add('error', 'element-type', 'An element has no type.', 'Choose a type in the Inspector.', whereScene, target);
        return;
      }
      if (!PLAYER_ELEMENT_TYPES.includes(type)) {
        add('error', 'element-type-unknown', `“${type}” is not an element type the player draws.`, `Use one of: ${PLAYER_ELEMENT_TYPES.join(', ')}.`, where, target);
      }

      const x = toIntOrNull(el.x);
      const y = toIntOrNull(el.y);
      if (x === null || y === null) {
        add('error', 'element-position', 'This element has a missing or non-numeric position.', 'Set X and Y to whole pixels.', where, target);
      } else {
        if (x < 0 || y < 0) add('error', 'element-negative', `Position ${x}, ${y} is off the top-left of the canvas.`, 'Move it inside the canvas.', where, target);
        if (x >= canvasW || y >= canvasH) add('error', 'element-offcanvas', `Position ${x}, ${y} is outside the ${canvasW}×${canvasH} canvas.`, 'Move it inside the canvas.', where, target);
      }

      const w = toIntOrNull(el.width);
      const h = toIntOrNull(el.height);
      if (el.width !== undefined && el.width !== null && el.width !== '' && (w === null || w <= 0)) {
        add('error', 'element-width', 'Width is not a positive number.', 'Set a width in pixels, or clear it to let the player size it.', where, target);
      }
      if (el.height !== undefined && el.height !== null && el.height !== '' && (h === null || h <= 0)) {
        add('error', 'element-height', 'Height is not a positive number.', 'Set a height in pixels, or clear it to let the player size it.', where, target);
      }
      if (x !== null && y !== null && w && h && (x + w > canvasW || y + h > canvasH)) {
        add('warning', 'element-overflow', `This element extends past the ${canvasW}×${canvasH} canvas and will be clipped.`, 'Move it left/up or reduce its size.', where, target);
      }

      if (['button', 'input', 'menu'].includes(type) && w && h && (w < 36 || h < 36)) {
        add('warning', 'tap-target', `This ${type} is only ${w}×${h} px — hard to hit on a handheld.`, 'Make touch controls at least 36×36 px.', where, target);
      }
      if (['button', 'label'].includes(type) && !text.trim() && !el.variable && type !== 'label') {
        add('warning', 'empty-text', 'This button has no text.', 'Double-click it on the canvas to add a label.', where, target);
      }

      if (el.font && !FONT_ROLES.includes(String(el.font).toLowerCase())) {
        add('warning', 'font-role', `Font role “${el.font}” is not one the player knows.`, `Use ${FONT_ROLES.join(', ')}.`, where, target);
      }
      if (el.color && !colourIsCss(el.color)) {
        add('warning', 'text-colour', `Text colour “${el.color}” is not valid.`, 'Use #rrggbb in the Inspector.', where, target);
      }
      if (el.bgColor && !colourIsCss(el.bgColor)) {
        add('warning', 'bg-colour', `Background colour “${el.bgColor}” is not valid.`, 'Use #rrggbb in the Inspector.', where, target);
      }

      const resolveColour = value => {
        if (typeof value !== 'string') return value;
        const ref = value.trim().replace(/^\$/, '');
        if (ref === value.trim()) return value;
        const resolved = allVariables[ref];
        return typeof resolved === 'string' ? resolved : '';
      };
      if (!PLAYER_SELF_PAINTING_TYPES.includes(type)) {
        const fg = parseConfigColour(resolveColour(el.color) || '#F8FAFC');
        const bg = parseConfigColour(resolveColour(el.bgColor) || resolveColour(scene.background) || '');
        const ratio = bg ? contrastRatio(fg, bg) : null;
        if (ratio !== null && ratio < 4.5 && (text.trim() || type === 'button')) {
          add('warning', 'contrast', `Text contrast is ${ratio.toFixed(1)}:1, below the 4.5:1 readable minimum.`, 'Lighten the text or darken the background.', where, target);
        }
      }

      const trigger = String(el.trigger || '').trim();
      let sceneTarget = String(el.sceneChange || '').trim();
      if (!sceneTarget && trigger.startsWith('change_scene:')) sceneTarget = trigger.slice('change_scene:'.length).trim();
      if ((trigger === 'change_scene' || trigger.startsWith('change_scene:')) && !sceneTarget) {
        add('error', 'scene-target-missing', 'This element changes scene but has no target scene.', 'Pick a target scene in the Inspector.', where, target);
      } else if (sceneTarget && !sceneNames.has(sceneTarget)) {
        add('error', 'scene-target-unknown', `Scene “${sceneTarget}” does not exist.`, 'Pick an existing scene, or create it first.', where, target);
      }

      // $variable references: the player substitutes an empty string for an
      // unknown name, which looks like a blank tile with no explanation.
      const refs = new Set();
      const regex = /\$([a-zA-Z_][a-zA-Z0-9_]*)/g;
      let match;
      while ((match = regex.exec(text)) !== null) refs.add(match[1]);
      refs.forEach(name => {
        if (allVariables[name] === undefined && !(config.variables && config.variables[name] !== undefined)) {
          add('warning', 'unknown-variable', `“$${name}” is not defined, so the player will draw nothing there.`, `Add a “${name}” variable in the Variables panel.`, where, target);
        }
      });

      if (text.trim() && w && !PLAYER_SELF_PAINTING_TYPES.includes(type)) {
        const size = toIntOrNull(fontSizes[String(el.font || 'medium').toLowerCase()]) || toIntOrNull(fontSizes.medium) || 24;
        const estimated = estimateTextWidth(text, size);
        if (estimated > w * 1.15) {
          add('warning', 'text-overflow', `“${text.trim().slice(0, 24)}…” may not fit in ${w} px at ${size} px.`, 'Widen the element, shorten the text, or use a smaller font role.', where, target);
        }
      }

      if (menuRect && type !== 'menu' && y !== null && h) {
        const bottom = y + h;
        if (bottom > menuRect.y && y < menuRect.y + menuRect.h) {
          add('warning', 'menu-overlap', 'This element is underneath the menu bar and will be covered.', 'Move it above the menu or shorten the menu.', where, target);
        }
      }
    });
  });

  const errors = issues.filter(issue => issue.severity === 'error').length;
  return { issues, errors, warnings: issues.length - errors };
}

// --- Design check panel ------------------------------------------------------
let validationResult = null;
let validationTimer = null;
let pendingExportKind = null;

function scheduleValidationRefresh() {
  const badge = document.getElementById('validateCount');
  if (!badge) return;
  if (validationTimer) clearTimeout(validationTimer);
  validationTimer = setTimeout(() => {
    validationTimer = null;
    const result = validateProject(buildEditorConfig());
    const total = result.errors + result.warnings;
    badge.hidden = total === 0;
    badge.textContent = String(total);
    badge.classList.toggle('has-errors', result.errors > 0);
    const btn = document.getElementById('validateBtn');
    if (btn) {
      btn.title = total === 0
        ? 'No problems found (Ctrl+Shift+K)'
        : `${result.errors} error${result.errors === 1 ? '' : 's'}, ${result.warnings} warning${result.warnings === 1 ? '' : 's'} (Ctrl+Shift+K)`;
    }
  }, 700);
}

function renderValidationPanel(result, report) {
  validationResult = result;
  const list = document.getElementById('validateList');
  const empty = document.getElementById('validateEmpty');
  const summary = document.getElementById('validateSummary');
  const exportAnyway = document.getElementById('validateExportAnyway');
  if (!list || !summary) return;

  list.innerHTML = '';
  const total = result.errors + result.warnings;
  // When the design is clean the honour-banner (validateEmpty) already says
  // "No problems found"; keep the header for what the check actually covers.
  summary.textContent = total === 0
    ? 'Checked values, scene links, contrast and thumb-fit.'
    : `${result.errors} error${result.errors === 1 ? '' : 's'} and ${result.warnings} warning${result.warnings === 1 ? '' : 's'}. Errors block export; warnings do not.`;
  if (report) {
    summary.textContent += ` Imported: ${importReportMessage(report)}.`;
  }
  empty.hidden = total !== 0;
  if (exportAnyway) exportAnyway.hidden = result.errors === 0;

  result.issues.forEach(issue => {
    const li = document.createElement('li');
    li.className = 'validate-item';
    const button = document.createElement('button');
    button.type = 'button';
    button.className = `validate-issue is-${issue.severity}`;
    button.setAttribute('aria-label', `${issue.severity}: ${issue.message}. ${issue.fix}`);
    button.innerHTML = `
      <i class="fas ${issue.severity === 'error' ? 'fa-circle-exclamation' : 'fa-triangle-exclamation'} validate-icon" aria-hidden="true"></i>
      <span class="validate-issue-body">
        <span class="validate-issue-where">${escapeHtml(issue.where || 'Design')}</span>
        <span>${escapeHtml(issue.message)}</span>
        ${issue.fix ? `<span class="validate-issue-fix">${escapeHtml(issue.fix)}</span>` : ''}
      </span>`;
    button.addEventListener('click', () => focusValidationIssue(issue));
    li.appendChild(button);
    list.appendChild(li);
  });
}

// Builds one row of the Variables list with DOM APIs only. Variable names and
// values come from imported configs, XML files and share links, so they must
// never be parsed as HTML or spliced into an inline handler.
function createVariableItem(key, val) {
  const item = document.createElement('div');
  item.className = 'variable-item';

  const info = document.createElement('div');
  const nameEl = document.createElement('span');
  nameEl.className = 'variable-name';
  nameEl.textContent = String(key);
  const valueEl = document.createElement('span');
  valueEl.className = 'variable-value';
  valueEl.textContent = renderVariablePanelValue(val);
  info.append(nameEl, ' ', valueEl);

  const actions = document.createElement('div');
  actions.className = 'variable-actions';
  const makeButton = (verb, iconClass, handler) => {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.setAttribute('aria-label', `${verb} ${key}`);
    btn.title = `${verb} ${key}`;
    const icon = document.createElement('i');
    icon.className = iconClass;
    icon.setAttribute('aria-hidden', 'true');
    btn.appendChild(icon);
    btn.addEventListener('click', () => handler(String(key)));
    return btn;
  };
  actions.append(
    makeButton('Edit', 'fas fa-edit', name => editVariable(name)),
    makeButton('Delete', 'fas fa-trash', name => deleteVariable(name))
  );

  item.append(info, actions);
  return item;
}

function escapeHtml(text) {
  return String(text).replace(/[&<>"']/g, ch => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]));
}

function focusValidationIssue(issue) {
  if (issue.sceneIndex === undefined) return;
  const names = Object.keys(scenes);
  const sceneName = names[issue.sceneIndex];
  if (!sceneName) return;
  const changed = currentScene !== sceneName;
  if (changed) {
    sceneSelector.value = sceneName;
    changeScene();
  }
  closeValidationPanel();
  if (issue.elementIndex === undefined) return;
  const elements = canvas.querySelectorAll('.element');
  const el = elements[issue.elementIndex];
  if (!el) return;
  el.scrollIntoView({ block: 'nearest', inline: 'nearest' });
  el.click();
  el.focus({ preventScroll: true });
  announce(`Selected ${el.getAttribute('data-type') || 'element'} in ${sceneName}.`);
}

function openValidationPanel(report) {
  const panel = document.getElementById('validatePanel');
  if (!panel) return;
  const result = validateProject(buildEditorConfig());
  renderValidationPanel(result, report || null);
  panel.removeAttribute('hidden');
  panel.setAttribute('aria-hidden', 'false');
  const firstIssue = panel.querySelector('.validate-issue');
  (firstIssue || panel.querySelector('[data-close-validate]'))?.focus();
  announce(result.errors || result.warnings
    ? `Design check: ${result.errors} errors, ${result.warnings} warnings.`
    : 'Design check found no problems.');
}

function closeValidationPanel() {
  const panel = document.getElementById('validatePanel');
  if (!panel || panel.hasAttribute('hidden')) return;
  panel.setAttribute('hidden', '');
  panel.setAttribute('aria-hidden', 'true');
  document.getElementById('validateBtn')?.focus();
}

function exportWithValidation(kind) {
  const result = validateProject(buildEditorConfig());
  if (result.errors > 0) {
    pendingExportKind = kind;
    openValidationPanel(lastImportReport);
    showToast(`Fix ${result.errors} error${result.errors === 1 ? '' : 's'} before exporting`, 'error');
    return false;
  }
  return true;
}

// Export functionality
function createJukaApp() {
  const config = buildEditorConfig();

  const dataStr = "data:text/json;charset=utf-8," + encodeURIComponent(JSON.stringify(config, null, 2));
  const downloadAnchorNode = document.createElement('a');
  downloadAnchorNode.href = dataStr;
  downloadAnchorNode.download = "jukaconfig.json";
  document.body.appendChild(downloadAnchorNode);
  downloadAnchorNode.click();
  downloadAnchorNode.remove();
  showToast('Exported jukaconfig.json', 'success');
}

// Legacy alias for inline Export buttons. The design check gates export on
// errors, because a config the player cannot load is worse than no download.
function exportConfig(force) {
  if (!force && !exportWithValidation('json')) return;
  createJukaApp();
}

// Copy as JSON: the same config Export writes, straight to the clipboard. This
// is for people pasting into Discord, a GitHub issue or a chat with the phone
// companion, where a downloaded file is one extra step.
function copyConfigJsonText() {
  return JSON.stringify(buildEditorConfig(), null, 2);
}

async function copyConfigJson() {
  if (!exportWithValidation('json')) return;
  let text;
  try {
    text = copyConfigJsonText();
  } catch (e) {
    showToast('Could not build the config: ' + e.message, 'error');
    return;
  }
  try {
    await navigator.clipboard.writeText(text);
  } catch (e) {
    // The Clipboard API needs a secure context. A hidden textarea still works on
    // file:// previews and older browsers, which is how the builder is often
    // opened straight from a folder.
    const area = document.createElement('textarea');
    area.value = text;
    area.setAttribute('readonly', '');
    area.style.position = 'fixed';
    area.style.opacity = '0';
    document.body.appendChild(area);
    area.select();
    let copied = false;
    try {
      copied = document.execCommand('copy');
    } catch (err) {
      copied = false;
    }
    area.remove();
    if (!copied) {
      showToast('Could not copy — use Export as JSON instead', 'warning');
      return;
    }
  }
  showToast('Config JSON copied to the clipboard', 'success');
}

// ---
// Design sharing (theme + layout in a URL)
// ---
// The shared payload is the same config buildEditorConfig() exports, gzipped
// and base64url-encoded into the fragment. Nothing is uploaded: the link is
// the transport. Opening one never overwrites the local workspace - the
// recipient explicitly presses Remix, which loads a copy through the normal
// import path.

const SHARE_HASH_PREFIX = '#remix=';
const SHARE_MAX_TOKEN = 200000;
const SHARE_MAX_SCENES = 64;
const SHARE_MAX_ELEMENTS = 4000;

let sharedDesign = null;      // { config, token, compressed, bytes, title, author }
let shareLinkValue = '';

function bytesToBase64Url(bytes) {
  let binary = '';
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode.apply(null, bytes.subarray(i, i + chunk));
  }
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function base64UrlToBytes(text) {
  const normalized = String(text || '').replace(/-/g, '+').replace(/_/g, '/');
  const padded = normalized + '='.repeat((4 - normalized.length % 4) % 4);
  const binary = atob(padded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

async function gzipBytes(bytes) {
  if (typeof CompressionStream !== 'function') return null;
  const stream = new Blob([bytes]).stream().pipeThrough(new CompressionStream('gzip'));
  return new Uint8Array(await new Response(stream).arrayBuffer());
}

async function gunzipBytes(bytes) {
  if (typeof DecompressionStream !== 'function') {
    throw new Error('This browser cannot open compressed share links');
  }
  const stream = new Blob([bytes]).stream().pipeThrough(new DecompressionStream('gzip'));
  return new Uint8Array(await new Response(stream).arrayBuffer());
}

// A share link is for the theme and layout, not the whole local profile.
// Remove account material and runtime history before anything is encoded: an
// imported config can carry a Discord token, a Groq key or a user's last
// searches, and none of that belongs in a URL pasted into a chat app.
const SHARE_SECRET_KEY = /(token|secret|password|api[-_]?key|credential|cookie|authorization)/i;
const SHARE_PRIVATE_VARIABLE = /^(recently_played|search_query|search_error|stream_url|custom_link_url|podcast_url|terminal_cmd|fe_path|fe_entries|canvas_code|custom_ticker|custom_tickers|packages_list|plugin_list|ssh_status|chat_attach_image|youtube_cookies_file|tspusername|fileexplorerroot|tools_path|discord_.*)$/i;

function sanitizeSharedConfig(config) {
  let copy;
  try { copy = JSON.parse(JSON.stringify(config || {})); } catch (e) { return {}; }
  delete copy.channel_profile;

  const scrub = obj => {
    if (!obj || typeof obj !== 'object') return;
    for (const key of Object.keys(obj)) {
      if (SHARE_SECRET_KEY.test(key) || SHARE_PRIVATE_VARIABLE.test(key)) {
        delete obj[key];
        continue;
      }
      const value = obj[key];
      if (value && typeof value === 'object' && !Array.isArray(value)) scrub(value);
    }
  };
  if (copy.variables && typeof copy.variables === 'object') scrub(copy.variables);
  return copy;
}

async function encodeSharedDesign(config) {
  const json = new TextEncoder().encode(JSON.stringify(config));
  const compressed = await gzipBytes(json);
  if (compressed) {
    return { token: 'v1g.' + bytesToBase64Url(compressed), compressed: true, bytes: compressed.length };
  }
  return { token: 'v1r.' + bytesToBase64Url(json), compressed: false, bytes: json.length };
}

function validateSharedConfig(config) {
  if (!config || typeof config !== 'object' || Array.isArray(config)) return false;
  if (!Array.isArray(config.scenes)) return false;
  if (config.scenes.length === 0 || config.scenes.length > SHARE_MAX_SCENES) return false;
  let elements = 0;
  for (const scene of config.scenes) {
    if (!scene || typeof scene !== 'object') return false;
    if (typeof scene.name !== 'string' || !scene.name.trim()) return false;
    if (!Array.isArray(scene.elements)) return false;
    elements += scene.elements.length;
    if (elements > SHARE_MAX_ELEMENTS) return false;
  }
  return true;
}

async function decodeSharedDesign(token) {
  if (!token || token.length > SHARE_MAX_TOKEN) throw new Error('This share link is too large');
  const dot = token.indexOf('.');
  if (dot < 1) throw new Error('This share link is not in a known format');
  const version = token.slice(0, dot);
  const payload = base64UrlToBytes(token.slice(dot + 1));
  let jsonBytes;
  if (version === 'v1g') jsonBytes = await gunzipBytes(payload);
  else if (version === 'v1r') jsonBytes = payload;
  else throw new Error('This share link was made by a newer builder');
  let config;
  try {
    config = JSON.parse(new TextDecoder().decode(jsonBytes));
  } catch (e) {
    throw new Error('This share link is damaged');
  }
  if (!validateSharedConfig(config)) throw new Error('This share link has no usable design');
  return config;
}

function showShareModal() {
  const panel = document.getElementById('sharePanel');
  if (!panel) return;
  panel.removeAttribute('hidden');
  const urlInput = document.getElementById('shareUrl');
  if (urlInput) {
    urlInput.value = shareLinkValue;
    setTimeout(() => { urlInput.focus(); urlInput.select(); }, 0);
  }
}

function closeShareModal() {
  const panel = document.getElementById('sharePanel');
  if (panel) panel.setAttribute('hidden', '');
}

function showRemixModal() {
  if (!sharedDesign) return;
  closeShareModal();
  const panel = document.getElementById('remixPanel');
  if (!panel) return;
  const meta = document.getElementById('remixMeta');
  if (meta) {
    const title = sharedDesign.title || 'Untitled design';
    const author = sharedDesign.author ? ' by ' + sharedDesign.author : '';
    meta.textContent = '“' + title + '”' + author;
  }
  panel.removeAttribute('hidden');
  const remixBtn = document.getElementById('remixDesignBtn');
  if (remixBtn) setTimeout(() => remixBtn.focus(), 0);
}

function closeRemixModal() {
  const panel = document.getElementById('remixPanel');
  if (panel) panel.setAttribute('hidden', '');
}

function stripShareHash() {
  try {
    history.replaceState(null, '', location.pathname + location.search);
  } catch (e) { /* file:// or restricted history */ }
}

function adoptSharedDesign() {
  if (!sharedDesign) return;
  const config = JSON.parse(JSON.stringify(sharedDesign.config));
  if (autoSaveTimer) {
    clearTimeout(autoSaveTimer);
    autoSaveTimer = null;
  }
  // A remix is an import: it replaces the workspace and becomes the source
  // the Reload config button restores.
  importedConfigSnapshot = JSON.parse(JSON.stringify(config));
  loadJukaApp(config);
  scheduleAutoSave();
  refreshReloadButtonState();
  closeRemixModal();
  closeShareModal();
  stripShareHash();
  const remixButton = document.getElementById('remixSharedBtn');
  if (remixButton) remixButton.hidden = true;
  showToast('Remixed — you are editing a copy', 'success');
}

async function shareCurrentDesign() {
  const btn = document.getElementById('shareDesignBtn');
  if (btn) setLoading(btn, true);
  try {
    const encoded = await encodeSharedDesign(sanitizeSharedConfig(buildEditorConfig()));
    shareLinkValue = location.href.split('#')[0] + SHARE_HASH_PREFIX + encoded.token;
    const urlInput = document.getElementById('shareUrl');
    if (urlInput) urlInput.value = shareLinkValue;
    const sizeNote = document.getElementById('shareSizeNote');
    if (sizeNote) {
      const kb = Math.round(shareLinkValue.length / 1024);
      sizeNote.textContent = 'Link size: about ' + kb + ' KB.';
      sizeNote.classList.toggle('warn', shareLinkValue.length > 12000);
      if (shareLinkValue.length > 12000) {
        sizeNote.textContent += ' Some chat apps truncate links this long — export JSON instead.';
      }
    }
    showShareModal();
  } catch (e) {
    showToast('Could not build the share link: ' + e.message, 'error');
  } finally {
    if (btn) setLoading(btn, false);
  }
}

async function copyShareLink() {
  const urlInput = document.getElementById('shareUrl');
  if (!urlInput || !urlInput.value) return;
  try {
    await navigator.clipboard.writeText(urlInput.value);
  } catch (e) {
    // Clipboard API needs a secure context; a selected readonly input still
    // works on file:// previews and older Safari.
    urlInput.focus();
    urlInput.select();
    try {
      if (!document.execCommand('copy')) throw new Error('copy rejected');
    } catch (err) {
      showToast('Press Ctrl/Cmd+C to copy the link', 'warning');
      return;
    }
  }
  showToast('Share link copied', 'success');
}

async function restoreSharedDesignFromURL() {
  if (!location.hash.startsWith(SHARE_HASH_PREFIX)) return;
  const token = location.hash.slice(SHARE_HASH_PREFIX.length);
  if (!token) return;
  try {
    const config = await decodeSharedDesign(token);
    sharedDesign = {
      config,
      token,
      title: config.title || '',
      author: config.author || ''
    };
    const remixButton = document.getElementById('remixSharedBtn');
    if (remixButton) remixButton.hidden = false;
    showRemixModal();
  } catch (e) {
    showToast('Could not open shared design: ' + e.message, 'error');
    stripShareHash();
  }
}

function setupDesignSharing() {
  const shareBtn = document.getElementById('shareDesignBtn');
  const remixBtn = document.getElementById('remixSharedBtn');
  const copyBtn = document.getElementById('copyShareLink');
  const adoptBtn = document.getElementById('remixDesignBtn');

  if (shareBtn) shareBtn.addEventListener('click', shareCurrentDesign);
  if (remixBtn) remixBtn.addEventListener('click', showRemixModal);
  if (copyBtn) copyBtn.addEventListener('click', copyShareLink);
  if (adoptBtn) adoptBtn.addEventListener('click', adoptSharedDesign);

  document.querySelectorAll('[data-close-share]').forEach(el => {
    el.addEventListener('click', closeShareModal);
  });
  document.querySelectorAll('[data-close-remix]').forEach(el => {
    el.addEventListener('click', () => {
      closeRemixModal();
      if (sharedDesign) showToast('Your design was left unchanged — press Remix to copy theirs', 'info');
    });
  });

  document.addEventListener('keydown', e => {
    if (e.key !== 'Escape') return;
    const sharePanel = document.getElementById('sharePanel');
    const remixPanel = document.getElementById('remixPanel');
    if (sharePanel && !sharePanel.hasAttribute('hidden')) {
      closeShareModal();
      e.preventDefault();
      return;
    }
    if (remixPanel && !remixPanel.hasAttribute('hidden')) {
      closeRemixModal();
      e.preventDefault();
    }
  });

  window.addEventListener('hashchange', restoreSharedDesignFromURL);
}

// ---

function exportConfigXml(force) {
  if (!force && !exportWithValidation('xml')) return;
  const config = buildEditorConfig();

  const xmlStr = configToXml(config);
  const dataStr = 'data:application/xml;charset=utf-8,' + encodeURIComponent(xmlStr);
  const downloadAnchorNode = document.createElement('a');
  downloadAnchorNode.href = dataStr;
  downloadAnchorNode.download = 'jukaconfig.xml';
  document.body.appendChild(downloadAnchorNode);
  downloadAnchorNode.click();
  downloadAnchorNode.remove();
  showToast('Exported jukaconfig.xml', 'success');
}

// ---

function escapeXml(str) {
  if (str == null) return '';
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&apos;');
}

function xmlEscapeKey(key) {
  return key.replace(/[^a-zA-Z0-9_-]/g, '_');
}

// Serialises attribute pairs, dropping the ones that are not set so an omitted
// size stays omitted (the player reads that as "size it yourself").
function xmlAttrs(pairs) {
  return pairs
    .filter(pair => pair[1] !== undefined && pair[1] !== null && pair[1] !== '')
    .map(pair => ' ' + pair[0] + '="' + escapeXml(pair[1]) + '"')
    .join('');
}

// Variables nest: the builder groups settings under <Custom> and colours can be
// {r,g,b} objects. Recursing keeps every entry instead of writing the useless
// "[object Object]" the flat writer produced for nested values.
function variablesToXml(vars, indent) {
  let xml = '';
  for (const [key, val] of Object.entries(vars || {})) {
    const tag = xmlEscapeKey(key);
    if (val !== null && typeof val === 'object') {
      // An empty block carries no information - and XML cannot express the
      // difference between an empty string and an empty map - so it is skipped.
      if (Object.keys(val).length === 0) continue;
      xml += indent + '<' + tag + '>\n' + variablesToXml(val, indent + '  ') + indent + '</' + tag + '>\n';
    } else {
      xml += indent + '<' + tag + '>' + escapeXml(val) + '</' + tag + '>\n';
    }
  }
  return xml;
}

const XML_ELEMENT_ATTRS = [
  'type', 'x', 'y', 'width', 'height', 'color', 'bgColor', 'font', 'opacity',
  'trigger', 'triggerValue', 'sceneChange', 'externalAppPath', 'externalAppReturn',
  'variableChange', 'variableChangeValue', 'mediaVariable', 'videoVariable', 'video',
  'command', 'variable', 'listVariable', 'columns', 'rows', 'placeholder', 'style',
  'icon', 'source', 'jsonPath', 'autoRefresh', 'image'
];

function configToXml(config) {
  let xml = '<?xml version="1.0" encoding="UTF-8"?>\n<jukaconfig>\n';

  // Top-level metadata
  xml += '  <title>' + escapeXml(config.title) + '</title>\n';
  xml += '  <author>' + escapeXml(config.author) + '</author>\n';
  xml += '  <description>' + escapeXml(config.description) + '</description>\n';

  // Player metadata the builder does not edit, but must not drop: without these
  // an XML export lost the app name, canvas size, background, font and the
  // channel profile that the player needs.
  if (config.AppName) xml += '  <appName>' + escapeXml(config.AppName) + '</appName>\n';
  if (config.Version) xml += '  <version>' + escapeXml(config.Version) + '</version>\n';
  if (config.Width) xml += '  <width>' + escapeXml(config.Width) + '</width>\n';
  if (config.Height) xml += '  <height>' + escapeXml(config.Height) + '</height>\n';
  if (config.Background) xml += '  <background>' + escapeXml(config.Background) + '</background>\n';
  if (config.FontPath) xml += '  <fontPath>' + escapeXml(config.FontPath) + '</fontPath>\n';
  if (config.channel_profile) {
    xml += '  <channelProfile channelId="' + escapeXml(config.channel_profile.channel_id || '') +
      '" token="' + escapeXml(config.channel_profile.token || '') + '" />\n';
  }

  // Variables
  if (config.variables) {
    xml += '  <variables>\n' + variablesToXml(config.variables, '    ') + '  </variables>\n';
  }

  // Scenes
  if (config.scenes) {
    xml += '  <scenes>\n';
    for (const scene of config.scenes) {
      xml += '    <scene' + xmlAttrs([
        ['name', scene.name],
        ['icon', scene.icon],
        ['description', scene.description],
        ['background', scene.background],
        ['layout', scene.layout]
      ]) + '>\n';

      for (const el of scene.elements) {
        const attrs = xmlAttrs(XML_ELEMENT_ATTRS.map(name => [name, el[name]]));
        if (el.text !== undefined && el.text !== null && el.text !== '') {
          xml += '      <element' + attrs + '>' + escapeXml(el.text) + '</element>\n';
        } else {
          xml += '      <element' + attrs + ' />\n';
        }
      }
      xml += '    </scene>\n';
    }
    xml += '  </scenes>\n';
  }

  xml += '</jukaconfig>';
  return xml;
}

// ---

// coerces a raw XML text node into the typed value the player's XML loader
// would produce, so the browser's XML path uses the same decoder as the Go
// player. Numbers accept the same forms (including leading zeros like "08" are
// kept as strings), booleans are lower-cased, empties stay as the original
// text, and anything else is returned verbatim.
function coerceXmlValue(text) {
  const raw = String(text);
  const trimmed = raw.trim();
  if (/^(true|false)$/i.test(trimmed)) return trimmed.toLowerCase() === 'true';
  if (/^[+-]?\d+$/.test(trimmed)) {
    const n = parseInt(trimmed, 10);
    if (String(n) === trimmed) return n;
  }
  if (/^[+-]?(\d+\.\d*|\.\d+)([eE][+-]?\d+)?$/.test(trimmed)) {
    const f = parseFloat(trimmed);
    if (!Number.isNaN(f)) return f;
  }
  return raw;
}

function xmlVariablesToObject(el) {
  const out = {};
  for (const child of el.children) {
    if (child.children.length > 0) out[child.tagName] = xmlVariablesToObject(child);
    else out[child.tagName] = coerceXmlValue(child.textContent);
  }
  return out;
}

function xmlToJson(xmlStr) {
  const parser = new DOMParser();
  const doc = parser.parseFromString(xmlStr, 'application/xml');
  const parseError = doc.querySelector('parsererror');
  if (parseError) {
    throw new Error('Invalid XML: ' + parseError.textContent.substring(0, 120));
  }

  const root = doc.querySelector('jukaconfig');
  if (!root) throw new Error('Root element <jukaconfig> not found');

  function getText(el) { return el ? el.textContent : ''; }

  const config = {
    title: getText(root.querySelector(':scope > title')),
    author: getText(root.querySelector(':scope > author')),
    description: getText(root.querySelector(':scope > description')),
    variables: {},
    scenes: []
  };

  // Optional player metadata (only present when exported by a builder/player
  // that supports it) is read back so a round trip keeps it.
  const metaText = (tag) => {
    const el = root.querySelector(':scope > ' + tag);
    return el ? getText(el) : '';
  };
  const metaInt = (tag) => {
    const value = parseInt(metaText(tag), 10);
    return Number.isNaN(value) ? '' : value;
  };
  const appName = metaText('appName');
  if (appName) config.AppName = appName;
  const version = metaText('version');
  if (version) config.Version = version;
  const width = metaInt('width');
  if (width !== '') config.Width = width;
  const height = metaInt('height');
  if (height !== '') config.Height = height;
  const background = metaText('background');
  if (background) config.Background = background;
  const fontPath = metaText('fontPath');
  if (fontPath) config.FontPath = fontPath;
  const channel = root.querySelector(':scope > channelProfile');
  if (channel) {
    config.channel_profile = {
      channel_id: channel.getAttribute('channelId') || '',
      token: channel.getAttribute('token') || ''
    };
  }

  // Parse <variables> (recursively: values may be nested blocks)
  const varsEl = root.querySelector(':scope > variables');
  if (varsEl) config.variables = xmlVariablesToObject(varsEl);

  // Parse <scenes>
  const scenesEl = root.querySelector(':scope > scenes');
  if (scenesEl) {
    for (const sceneEl of scenesEl.querySelectorAll(':scope > scene')) {
      const scene = { name: sceneEl.getAttribute('name') || '', elements: [] };
      ['icon', 'description', 'background', 'layout'].forEach(key => {
        if (sceneEl.getAttribute(key)) scene[key] = sceneEl.getAttribute(key);
      });

      for (const el of sceneEl.querySelectorAll(':scope > element')) {
        const element = {
          type: el.getAttribute('type') || '',
          x: parseInt(el.getAttribute('x'), 10) || 0,
          y: parseInt(el.getAttribute('y'), 10) || 0
        };
// Only keep sizes the element actually declares; an omitted/empty size
// means the player sizes the element itself. An explicit zero stays explicit —
// the player treats "0" as a real size, not "leave it out".
        if (el.hasAttribute('width') && el.getAttribute('width') !== '') element.width = parseInt(el.getAttribute('width'), 10);
        if (el.hasAttribute('height') && el.getAttribute('height') !== '') element.height = parseInt(el.getAttribute('height'), 10);

        XML_ELEMENT_ATTRS.forEach(name => {
          if (name === 'type' || name === 'x' || name === 'y' || name === 'width' || name === 'height') return;
          if (!el.hasAttribute(name)) return;
          const value = el.getAttribute(name);
          if (value === '') return;
          if (name === 'opacity') element.opacity = parseFloat(value);
          else if (name === 'columns' || name === 'rows') element[name] = parseInt(value, 10);
          else if (name === 'autoRefresh') element.autoRefresh = value === 'true';
          else element[name] = value;
        });

        const text = getText(el);
        if (text) element.text = text;
        scene.elements.push(element);
      }
      config.scenes.push(scene);
    }
  }

  return config;
}
function clearAll() {
  if (confirm('Are you sure you want to clear everything and start new?')) {
    pushUndo('clear project');
    scenes = { 'Scene 1': [] };
    currentScene = 'Scene 1';
    variables = {};
    canvas.innerHTML = '';
    sceneSelector.innerHTML = '';
    variablesList.innerHTML = '';

    const option = document.createElement('option');
    option.value = 'Scene 1';
    option.textContent = 'Scene 1';
    sceneSelector.appendChild(option);
    sceneSelector.value = 'Scene 1';

    document.getElementById('title').value = '';
    document.getElementById('author').value = '';
    document.getElementById('description').value = '';
    titleSizeInput.value = 48;
    bigSizeInput.value = 36;
    mediumSizeInput.value = 24;
    smallSizeInput.value = 18;

    canvas.style.backgroundImage = '';
    backgroundPath = '';

    updateCanvasSize();
    addElement('menu', 0, canvasHeight - 50);
    updateSceneBadge();

    document.querySelectorAll('.menu').forEach(menu => {
      updateMenuSceneButtons(menu);
    });

    updateSceneChangeSelector();
    updateVariableChangeSelector();
    // The snapshot pushed above is what makes Clear All recoverable, so the
    // history is deliberately kept: the button's own tooltip promises Undo.
    try { localStorage.removeItem('jukahub-autosave'); } catch (e) {}
    showToast('Project cleared — Undo restores it', 'success');
  }
}

function saveCurrentScene() {
  scenes[currentScene] = Array.from(canvas.children).map(el => el.cloneNode(true));
}

// Scene management functions
function renameScene() {
  const newName = prompt('Enter new name for scene:', currentScene);
  if (!newName || newName === currentScene || sceneExists(newName)) return;
  pushUndo('rename scene');
  const oldName = currentScene;

  // Update scenes object
  scenes[newName] = scenes[currentScene];
  delete scenes[currentScene];

  // Update scene selector
  const option = findSceneOption(oldName);
  if (option) {
    option.value = newName;
    option.textContent = newName;
  }

  currentScene = newName;
  retargetSceneLinks(oldName, newName);
  sceneSelector.value = newName;
  updateSceneBadge();

  // Update all menu scene buttons
  updateAllMenuSceneButtons();
  updateAllStoredMenus();

  // Update scene change selector
  updateSceneChangeSelector();
  scheduleAutoSave();
  showToast(`Scene renamed to "${newName}"`, 'success');
}

function deleteScene() {
  if (Object.keys(scenes).length <= 1) {
    alert('Cannot delete the only scene.');
    return;
  }

  if (confirm(`Are you sure you want to delete "${currentScene}"?`)) {
    pushUndo('delete scene');
    const removedScene = currentScene;
    // Find next scene to show
    const sceneNames = Object.keys(scenes);
    const currentIndex = sceneNames.indexOf(currentScene);
    const nextScene = currentIndex > 0 ? sceneNames[currentIndex - 1] : sceneNames[1];

    // Delete scene
    delete scenes[currentScene];

    // Remove from selector
    const option = findSceneOption(currentScene);
    if (option) option.remove();

    // Switch to next scene
    currentScene = nextScene;
    sceneSelector.value = nextScene;
    loadScene(nextScene);
    updateSceneBadge();

    // Update all menu scene buttons
    updateAllMenuSceneButtons();
    updateAllStoredMenus();

    // Update scene change selector
    updateSceneChangeSelector();
    scheduleAutoSave();
    showToast(`Scene "${removedScene}" deleted`, 'success');
  }
}

function loadDefaultConfig(onFailure) {
  fetch('player/jukaconfig.json')
    .then(response => {
      if (!response.ok) {
        throw new Error('jukaconfig.json not found');
      }
      return response.json();
    })
    .then(config => {
      loadJukaApp(config);
    })
    .catch(error => {
      console.log('No default config found:', error.message);
      if (onFailure) onFailure();
    });
}


function loadJukaApp(input) {
  // Normalise before touching any builder state: one malformed element must not
  // leave the workspace half-loaded. The report is shown by the Check panel.
  const normalized = normalizeImportedConfig(input);
  const data = normalized.config;
  lastImportReport = normalized.report;

  // Remember everything the builder does not model, so the export keeps it.
  resetImportedConfig(data);

  // The config's logical canvas is the design's canvas, not whatever size the
  // previous project used.
  canvasWidth = data.Width || DEFAULT_SCENE_WIDTH;
  canvasHeight = data.Height || DEFAULT_SCENE_HEIGHT;
  syncCanvasSizeControls();

  // Clear existing elements
  const variableChangeSelectorEl = document.getElementById('variableChangeSelector');
  if (variableChangeSelectorEl) variableChangeSelectorEl.innerHTML = '';
  canvas.innerHTML = '';

  // Load app info
  document.getElementById('title').value = data.title || '';
  document.getElementById('author').value = data.author || '';
  document.getElementById('description').value = data.description || '';

  // Load font sizes, falling back to defaults when absent in the imported file.
  const fontSizes = (data.variables && data.variables.fontSizes) || {};
  document.getElementById('titleSize').value = fontSizes.title ?? 48;
  document.getElementById('bigSize').value = fontSizes.big ?? 36;
  document.getElementById('mediumSize').value = fontSizes.medium ?? 24;
  document.getElementById('smallSize').value = fontSizes.small ?? 18;

  // Load background and clear a previous config's background if absent.
  backgroundPath = (data.variables && data.variables.backgroundImage) || '';
  canvas.style.backgroundImage = backgroundPath ? `url(${backgroundPath})` : '';
  canvas.style.backgroundSize = backgroundPath ? 'cover' : '';  // Clear existing scenes and variables
  scenes = {};
  variables = {};
  variablesList.innerHTML = '';

  // Load variables. Keys the panel edits through dedicated controls stay out
  // of the flat list, but their values still round-trip: the settings controls
  // carry the device-resolved values on import and write changes back, so the
  // export always produces what the player would read.
  if (data.variables) {
    for (const key in data.variables) {
      if (!isSettingVariableKey(key)) {
        variables[key] = data.variables[key];

        // Add variable to UI
        appendVariableItem(key, data.variables[key]);
      }
    }
    // A working copy of the config's Custom object (colour refs, theme_preset,
    // nested blocks?) feeds the effective-value lookups and export write-backs.
    variables.Custom = JSON.parse(JSON.stringify(
      (typeof data.variables.Custom === 'object' && data.variables.Custom) ? data.variables.Custom : {}));
  }
  if (!variables.Custom) variables.Custom = {};

  // Settings section of the App Info panel ← the values the device resolves
  // (Custom keys beat typed fields, exactly like cfg syncVariableOverrides).
  applyConfigSettingsToControls();

  // Load scenes
  const sceneSelector = document.getElementById('sceneSelector');
  sceneSelector.innerHTML = '';
  scenes = {};

  data.scenes.forEach(scene => {
    scenes[scene.name] = [];

    // Add scene to selector
    const option = document.createElement('option');
    option.value = scene.name;
    option.textContent = scene.name;
    sceneSelector.appendChild(option);

    // Load scene elements
    scene.elements.forEach(elementData => {
      const el = createElementFromData(elementData);
      if (el) {
        canvas.appendChild(el);
        scenes[scene.name].push(el.cloneNode(true));
        setupElementEvents(el);

        // Process text for variables if applicable
        const textSpan = el.querySelector('.text-content');
        if (textSpan) {
          processTextForVariables(textSpan);
        }
      }
    });
  });

  // Set current scene
  if (data.scenes.length > 0) {
    currentScene = data.scenes[0].name;
    sceneSelector.value = currentScene;
    loadScene(currentScene);
  }

  // Update UI
  updateSceneChangeSelector();
  updateVariableChangeSelector();

  // Update all menu scene buttons
  updateAllMenuSceneButtons();
  updateSceneBadge();
  updateCanvasReadout();

  // Paint $buttonColor-style colour refs the way the player draws them (after
  // the canvas holds the loaded scenes).
  applyEditorVariablePaint();
}

// --- Settings ↔ jukaconfig.json sync -----------------------------------------
// The App Info panel's settings section edits exactly the values the player
// resolves out of jukaconfig.json: variables.Custom first (case-insensitive,
// strings win — customValueJs mirrors customValue in player/core.go), then the
// typed fields. Import fills the controls with those effective values and edits
// write them back, so the export contains what the device would read.

const SETTING_CONTROLS = [
  { key: 'fullscreen', id: 'fullscreenToggle', kind: 'bool' },
  { key: 'screenWidth', id: 'screenWidthInput', kind: 'int' },
  { key: 'screenHeight', id: 'screenHeightInput', kind: 'int' },
  { key: 'tools_path', id: 'toolsPath', kind: 'string' },
  { key: 'fileExplorerRoot', id: 'fileExplorerRoot', kind: 'string' },
  { key: 'weatherUnit', id: 'weatherUnit', kind: 'select' }
];

function panelCustomMap() {
  if (!variables || typeof variables !== 'object') variables = {};
  if (typeof variables.Custom !== 'object' || variables.Custom === null || Array.isArray(variables.Custom)) {
    variables.Custom = {};
  }
  return variables.Custom;
}

function coerceBoolJs(value) {
  if (typeof value === 'boolean') return value;
  if (typeof value === 'number') return value !== 0;
  return String(value === undefined || value === null ? '' : value).trim().toLowerCase() === 'true';
}

// Push the imported config's effective settings into the settings controls and
// the theme/font inputs, so the panel shows what the device actually uses.
function applyConfigSettingsToControls() {
  SETTING_CONTROLS.forEach(control => {
    const input = document.getElementById(control.id);
    if (!input) return;
    const raw = effectiveConfigValue(control.key);
    if (control.kind === 'bool') {
      input.checked = coerceBoolJs(raw);
    } else if (control.kind === 'int') {
      const n = parseInt(raw, 10);
      if (Number.isFinite(n)) input.value = String(n);
    } else if (control.kind === 'select') {
      // The player stores an unset weatherUnit as "" (location-based auto).
      const text = (raw === undefined || raw === null ? '' : String(raw)).trim().toUpperCase();
      const option = (text === 'C' || text === 'F')
        ? input.querySelector(`option[value="${text}"]`)
        : input.querySelector('option[value=""]');
      if (option) input.value = option.value;
    } else {
      input.value = raw === undefined || raw === null ? '' : String(raw);
    }
  });
  applyConfigColoursToControls();
  applyConfigFontFilesToControls();
}

// The theme pickers show hex colours, so "r,g,b" strings are converted first —
// matching what the player's rgbHex writes for an appearance field.
function hexOrCssValue(value) {
  const parsed = parseConfigColour(value);
  if (parsed && parsed.named === undefined) {
    return '#' + [parsed.r, parsed.g, parsed.b].map(c => c.toString(16).padStart(2, '0')).join('');
  }
  if (typeof value === 'string' && value.startsWith('#') && value.length >= 7) return value.slice(0, 7);
  return '';
}

function applyConfigColoursToControls() {
  const buttonPick = document.getElementById('buttonColorPicker');
  const labelPick = document.getElementById('labelColorPicker');
  if (buttonPick) {
    const hex = hexOrCssValue(effectiveThemeColour('buttoncolor'));
    if (hex) buttonPick.value = hex;
  }
  if (labelPick) {
    const hex = hexOrCssValue(effectiveThemeColour('labelcolor'));
    if (hex) labelPick.value = hex;
  }
}

// variables.fonts holds the font-file paths (EqualFold keys, like the player's
// typed Fonts map).
function applyConfigFontFilesToControls() {
  const fontsValue = effectiveConfigValue('fonts');
  const map = (fontsValue && typeof fontsValue === 'object' && !Array.isArray(fontsValue)) ? fontsValue : {};
  [['title', 'titleFontFile'], ['big', 'bigFontFile'], ['medium', 'mediumFontFile'], ['small', 'smallFontFile']]
    .forEach(([role, id]) => {
      const input = document.getElementById(id);
      if (!input) return;
      const hit = customValueJs(map, role);
      input.value = typeof hit === 'string' ? hit : (hit === undefined || hit === null ? '' : String(hit));
    });
}

// Write-back: an edit becomes a Custom entry with the player's key casing
// ("Fullscreen", not "fullscreen") because Custom overrides the typed fields —
// the same call order syncVariableOverrides uses on the device.
function setupSettingsControlListeners() {
  SETTING_CONTROLS.forEach(control => {
    const input = document.getElementById(control.id);
    if (!input) return;
    const capitalKey = control.key.charAt(0).toUpperCase() + control.key.slice(1);
    const write = () => {
      let value;
      if (control.kind === 'bool') value = input.checked;
      else if (control.kind === 'int') {
        value = parseInt(input.value, 10);
        if (!value || !Number.isFinite(value)) return;
      } else value = input.value;
      if (control.kind === 'bool') {
        // Booleans always mean a choice: true or false, never "default".
        panelCustomMap()[capitalKey] = value;
      } else {
        assignSettingValue(control.key, value);
      }
      scheduleAutoSave();
      scheduleValidationRefresh();
    };
    if (control.kind === 'bool' || control.kind === 'select') {
      input.addEventListener('change', write);
    } else {
      input.addEventListener('input', write);
    }
  });

  ['buttonColorPicker', 'labelColorPicker'].forEach(id => {
    const input = document.getElementById(id);
    if (!input) return;
    input.addEventListener('input', () => {
      panelCustomMap()[id === 'buttonColorPicker' ? 'ButtonColor' : 'LabelColor'] = input.value;
      applyEditorVariablePaint();
      scheduleAutoSave();
    });
  });

  [['titleFontFile', 'title'], ['bigFontFile', 'big'], ['mediumFontFile', 'medium'], ['smallFontFile', 'small']]
    .forEach(([id, role]) => {
      const input = document.getElementById(id);
      if (!input) return;
      input.addEventListener('input', () => {
        if (typeof variables !== 'object' || !variables) variables = {};
        if (typeof variables.fonts !== 'object' || variables.fonts === null) variables.fonts = {};
        variables.fonts[role] = input.value;
        scheduleAutoSave();
      });
    });
}

// The player's precedence for these settings: the typed variable wins, Custom
// only overrides it (syncVariableOverrides on the device writes the typed
// struct). So an edit writes the typed key and clears any Custom override in
// either casing — otherwise a stale Custom key would silently mask the new
// value at export. An empty value clears both: "player default" (weatherUnit
// "" is auto).
function assignSettingValue(key, value) {
  if (typeof variables !== 'object' || !variables) variables = {};
  delete variables[key];
  const custom = panelCustomMap();
  delete custom[key];
  delete custom[key.charAt(0).toUpperCase() + key.slice(1)];
  if (value !== '' && value !== undefined && value !== null) variables[key] = value;
}

// Edit-mode colour resolution: paints $name references on the canvas and the
// saved scene snapshots with the values the player resolves, so what you design
// against is what the device draws. Attributes keep the raw "$name" strings for
// the round trip.
function applyEditorVariablePaint() {
  const paintElement = el => {
    if (!el || el.nodeType !== 1) return;
    [['data-color', 'color'], ['data-bg-color', 'background']].forEach(([attr, styleProp]) => {
      const raw = el.getAttribute(attr);
      if (!raw || !raw.trim().startsWith('$')) return;
      const resolved = previewColourValue(raw.trim().slice(1));
      if (resolved) el.style[styleProp === 'background' ? 'backgroundColor' : 'color'] = resolved;
    });
  };
  if (canvas) canvas.querySelectorAll('.element').forEach(paintElement);
  Object.values(scenes || {}).forEach(elements => (elements || []).forEach(paintElement));
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', setupSettingsControlListeners);
} else {
  setupSettingsControlListeners();
}

function calculateTextDimensions(text, fontSize, fontFamily = 'Inter, sans-serif', fontWeight = '900') {
  const canvas = document.createElement('canvas');
  const context = canvas.getContext('2d');
  context.font = `${fontWeight} ${fontSize}px ${fontFamily}`;
  const metrics = context.measureText(text);
  return {
    width: Math.ceil(metrics.width + 16), // Add padding
    height: Math.ceil(parseInt(fontSize) * 1.4) // Line height factor
  };
}

function createElementFromData(elementData) {
  const el = document.createElement('div');
  el.className = 'element';
  el.style.position = 'absolute';
  el.style.left = `${elementData.x}px`;
  el.style.top = `${elementData.y}px`;
  el.setAttribute('data-type', elementData.type);
  el.setAttribute('data-x', elementData.x);
  el.setAttribute('data-y', elementData.y);

  // Keep the config entry this element came from: anything the builder does not
  // model is written back verbatim on export.
  setElementRawData(el, elementData);

  // Fix opacity handling
  if (elementData.opacity !== undefined) {
    const opacityValue = Math.round(elementData.opacity * 100);
    el.style.opacity = elementData.opacity;
    el.setAttribute('data-opacity', opacityValue);
  } else {
    el.style.opacity = 1;
    el.setAttribute('data-opacity', '100');
  }

  // Handle menu element specifically
  if (elementData.type === 'menu') {
    el.style.width = `${canvasWidth}px`; // Full width
    el.style.height = `${elementData.height || 50}px`;
    el.setAttribute('data-width', canvasWidth);
    el.setAttribute('data-height', elementData.height || 50);

    // Create menu structure
    el.innerHTML = `
            <div class="menu-scene-buttons"></div>
            <div class="menu-clock">00:00</div>
            <span class="remove-button">✕</span>
        `;

    // Set up menu events and buttons
    setupMenuEvents(el);
    updateMenuSceneButtons(el);
    updateMenuClock(el.querySelector('.menu-clock'));

    return el;
  }

  // Handle button and label elements with null dimensions
  let width = elementData.width;
  let height = elementData.height;

  if ((elementData.type === 'button' || elementData.type === 'label') &&
    (width === null || height === null)) {
    const fontSize = getFontSize(elementData.font || 'medium');
    const dimensions = calculateTextDimensions(
      elementData.text || elementData.type,
      fontSize
    );

    if (width === null) width = dimensions.width;
    if (height === null) height = dimensions.height;
  }

  if (elementData.trigger === 'play_video' || elementData.trigger === 'play_image') {
    el.setAttribute('data-media-variable', elementData.mediaVariable || '');
  }

  // Set default dimensions if still null
  width = width || 100;
  height = height || 40;

  el.style.width = `${width}px`;
  el.style.height = `${height}px`;
  // The canvas needs a concrete box to draw, but a config that omitted the size
  // ("width": "" means "player decides") must export that way again.
  const explicitWidth = elementData.width !== null && elementData.width !== undefined && elementData.width !== '';
  const explicitHeight = elementData.height !== null && elementData.height !== undefined && elementData.height !== '';
  el.setAttribute('data-width', explicitWidth ? width : '');
  el.setAttribute('data-height', explicitHeight ? height : '');

  // Add text content
  const textSpan = document.createElement('span');
  textSpan.className = 'text-content';
  textSpan.textContent = elementData.text || elementData.type.charAt(0).toUpperCase() + elementData.type.slice(1);
  if (!elementData.text) el.setAttribute('data-text-auto', '1');
  el.appendChild(textSpan);

  // Add remove button
  const removeButton = document.createElement('span');
  removeButton.textContent = '✕';
  removeButton.className = 'remove-button';
  el.appendChild(removeButton);

  // Set element-specific properties
  if (elementData.type === 'dynamiclist') {
    el.setAttribute('data-command', elementData.command || '');
    el.setAttribute('data-variable', elementData.variable || '');
    setupDynamicListExecution(el);
  }
  if (elementData.type === 'textbrowser') {
    el.setAttribute('data-variable', elementData.variable || '');
    el.setAttribute('data-source', elementData.source || 'system');
    el.setAttribute('data-json-path', elementData.jsonPath || '');
    el.setAttribute('data-auto-refresh', elementData.autoRefresh ? 'true' : 'false');
    const sourceIcons = {
      'system': '🖥️',
      'zeroconf': '🔍',
      'json': '📋'
    };
    const sourceNames = {
      'system': 'System',
      'zeroconf': 'Zeroconf',
      'json': 'JSON'
    };
    const textContent = el.querySelector('.text-content');
    if (textContent) {
      el.setAttribute('data-text-auto', '1');
      const source = elementData.source || 'system';
      textContent.textContent = `${sourceIcons[source] || '🌐'} ${sourceNames[source] || 'Text Browser'}`;
    }
  }
  // The canvas needs concrete colours and a font size to draw, but a default it
  // applied itself is presentation, not design: only values the config declared
  // become attributes, so they are what the export writes back out.
  const declared = (name, value) => {
    if (value === undefined || value === null || value === '') el.removeAttribute(name);
    else el.setAttribute(name, String(value));
  };
  // Unset colours draw the way the player draws them (resolveColor falls back
  // to the card surface and light text for buttons, white for labels), not
  // black-on-white, so a config that leaves them blank looks the same here.
  if (elementData.type === 'button') {
    el.style.color = elementData.color || '#F8FAFC';
    el.style.backgroundColor = elementData.bgColor || '#182236';
    el.style.fontSize = getFontSize(elementData.font || 'medium') + 'px';
    declared('data-color', elementData.color);
    declared('data-bg-color', elementData.bgColor);
    declared('data-font', elementData.font);
  } else if (elementData.type === 'label') {
    el.style.color = elementData.color || '#FFFFFF';
    // The player shortens a label with no width to what fits before the screen
    // edge instead of letting it run off; mirror that with an ellipsis.
    if (String(elementData.width ?? '').trim() === '') {
      const room = Math.max(40, (parseFloat(canvas.style.width) || 1280) - (parseFloat(elementData.x) || 0) - 8);
      el.style.setProperty('--label-room', room + 'px');
      el.setAttribute('data-fit-canvas', '1');
    }
    el.style.fontSize = getFontSize(elementData.font || 'medium') + 'px';
    el.style.background = 'none';
    declared('data-color', elementData.color);
    declared('data-font', elementData.font);
  }

  // Everything else the player supports (triggers, variables, style, grid sizes,
  // ...) has to reach the export too, not just the two types above.
  applyElementConfigAttributes(el, elementData);

  return el;
}



function setupMobileElementAdding() {
  if (window.innerWidth <= 768) {
    // Remove any existing button first
    const existingButton = document.querySelector('.mobile-add-button');
    if (existingButton) existingButton.remove();

    const existingMenu = document.querySelector('.mobile-element-menu');
    if (existingMenu) existingMenu.remove();

    // Create mobile add button
    const mobileAddButton = document.createElement('button');
    mobileAddButton.className = 'mobile-add-button';
    mobileAddButton.innerHTML = '<i class="fas fa-plus"></i>';
    document.body.appendChild(mobileAddButton);

    let elementType = null;

    // Create mobile element selection menu
    const mobileMenu = document.createElement('div');
    mobileMenu.className = 'mobile-element-menu';
    mobileMenu.style.display = 'none';
    mobileMenu.style.position = 'fixed';
    mobileMenu.style.bottom = '170px'; // Position above the add button
    mobileMenu.style.right = '20px';
    mobileMenu.style.background = 'var(--surface)';
    mobileMenu.style.borderRadius = 'var(--border-radius-md)';
    mobileMenu.style.padding = '1rem';
    mobileMenu.style.boxShadow = 'var(--shadow-lg)';
    mobileMenu.style.zIndex = '1001'; // Above other elements
    mobileMenu.style.maxHeight = '60vh';
    mobileMenu.style.overflowY = 'auto';

    const elements = [
      { type: 'button', icon: 'fas fa-square', name: 'Button' },
      { type: 'label', icon: 'fas fa-font', name: 'Label' },
      { type: 'image', icon: 'fas fa-image', name: 'Image' },
      { type: 'input', icon: 'fas fa-edit', name: 'Input' },
      { type: 'menu', icon: 'fas fa-bars', name: 'Menu' },
      { type: 'dynamiclist', icon: 'fas fa-list', name: 'Dynamic List' },
      { type: 'textlog', icon: 'fas fa-align-left', name: 'Text Log' },
      { type: 'textbrowser', icon: 'fas fa-globe', name: 'Text Browser' }
    ];

    elements.forEach(element => {
      const button = document.createElement('button');
      button.className = 'mobile-menu-item';
      button.style.display = 'flex';
      button.style.alignItems = 'center';
      button.style.gap = '0.5rem';
      button.style.padding = '0.5rem';
      button.style.width = '100%';
      button.style.marginBottom = '0.5rem';
      button.innerHTML = `<i class="${escapeHtml(element.icon)}" aria-hidden="true"></i> ${escapeHtml(element.name)}`;

      button.addEventListener('click', () => {
        elementType = element.type;
        mobileMenu.style.display = 'none';
        // Add element to center of canvas
        const x = canvasWidth / 2 - 60;
        const y = canvasHeight / 2 - 20;
        addElement(elementType, x, y);
      });

      mobileMenu.appendChild(button);
    });

    document.body.appendChild(mobileMenu);

    // Toggle menu on add button click
    mobileAddButton.addEventListener('click', (e) => {
      e.stopPropagation(); // Prevent event from bubbling
      mobileMenu.style.display = mobileMenu.style.display === 'none' ? 'block' : 'none';
    });

    // Close menu when clicking outside
    document.addEventListener('click', (e) => {
      if (!mobileMenu.contains(e.target) && e.target !== mobileAddButton && !mobileAddButton.contains(e.target)) {
        mobileMenu.style.display = 'none';
      }
    });
  }
}

window.addEventListener('resize', () => {
  // Update mobile interface when switching to mobile size
  if (window.innerWidth <= 768) {
    setupMobileElementAdding();
  } else {
    // Remove mobile buttons
    const mobileButton = document.querySelector('.mobile-add-button');
    if (mobileButton) mobileButton.remove();
    const mobileMenu = document.querySelector('.mobile-element-menu');
    if (mobileMenu) mobileMenu.remove();
  }
});

function setupMobileDoubleTap() {
  if ('ontouchstart' in window) {
    let lastTap = 0;
    document.addEventListener('touchend', function (event) {
      const currentTime = new Date().getTime();
      const tapLength = currentTime - lastTap;

      if (tapLength < 300 && tapLength > 0) {
        // Double tap detected
        const target = event.target;
        const element = target.closest('.element');

        if (element && !element.classList.contains('menu')) {
          event.preventDefault();
          const type = element.getAttribute('data-type');

          if (['button', 'label', 'video'].includes(type)) {
            const textSpan = element.querySelector('.text-content');
            if (textSpan) {
              const newText = prompt("Edit text:", textSpan.textContent);
              if (newText !== null) {
                textSpan.textContent = newText;
                processTextForVariables(textSpan);
              }
            }
          }
        }
      }
      lastTap = currentTime;
    }, { passive: false });
  }
}

function executeDynamicListCommand(el, command, variable) {
  // This would be implemented in the Juka runtime
  console.log(`Executing command: ${command}, storing in: ${variable}`);
  // Simulate command execution
  const result = [{ name: "Item 1", value: "1" }, { name: "Item 2", value: "2" }];
  showDynamicListItems(el, result, variable);
}

function showDynamicListItems(el, items, variable) {
  // Clear existing content
  el.innerHTML = '';

  // Create dropdown/list UI
  const select = document.createElement('select');
  select.className = 'dynamic-list-select';
  select.style.width = '100%';
  select.style.height = '100%';

  // Add items to select
  items.forEach(item => {
    const option = document.createElement('option');
    option.value = item.value;
    option.textContent = item.name;
    select.appendChild(option);
  });

  // Handle selection
  select.addEventListener('change', () => {
    if (variable) {
      variables[variable] = select.value;

      // Update all elements with variables
      document.querySelectorAll('.text-content').forEach(textEl => {
        processTextForVariables(textEl);
      });
    }
  });

  el.appendChild(select);

  // Add remove button
  const removeButton = document.createElement('span');
  removeButton.textContent = '✕';
  removeButton.className = 'remove-button';
  removeButton.addEventListener('click', (e) => {
    e.stopPropagation();
    el.remove();
  });
  el.appendChild(removeButton);
}



function setupMobileCanvasClick() {
  canvas.addEventListener('touchstart', (e) => {
    if (e.target === canvas) {
      currentElement = null;
      document.querySelectorAll('.element').forEach(el => el.classList.remove('selected'));
      document.body.classList.remove('element-selected');
      switchTab('app-properties');

      // Scroll to top of properties panel on mobile
      if (window.innerWidth <= 768) {
        document.querySelector('.right-sidebar').scrollTo(0, 0);
      }
    }
  });
}

function setupDynamicListExecution(el) {
  el.addEventListener('click', (e) => {
    // Only a click on the element itself previews the list; the remove button
    // deletes the element and must not also swap its content.
    if (e.target !== el) return;

    const command = el.getAttribute('data-command');
    const variable = el.getAttribute('data-variable');

    if (command && variable) {
      // Execute command and store result
      executeDynamicListCommand(el, command, variable);
    }
  });
}

function setupMobileElementSelection() {
  if (window.innerWidth <= 768) {
    // Ensure properties panel shows element properties when an element is selected
    document.addEventListener('click', (e) => {
      if (e.target.closest('.element') && !e.target.closest('.menu')) {
        document.getElementById('appInfoPanel').style.display = 'none';
        document.getElementById('elementPropertiesPanel').style.display = 'block';
      }
    });
  }
}

function updateVariableSelector(selector, currentValue) {
  selector.innerHTML = '';

  // Add empty option
  const emptyOption = document.createElement('option');
  emptyOption.value = '';
  emptyOption.textContent = 'Select variable';
  selector.appendChild(emptyOption);

  // Add all variables
  Object.keys(variables).forEach(variableName => {
    const option = document.createElement('option');
    option.value = variableName;
    option.textContent = variableName;
    if (variableName === currentValue) {
      option.selected = true;
    }
    selector.appendChild(option);
  });
}



// ----- AI Assistant module (injected from ai-assistant.js) -----

// AI Assistant (builder-only helper)
// Suggests element placement and can generate a scene config JSON blob.
// Uses a free provider by default; paid providers require an API key
// that is stored only in browser localStorage (never committed).

const AI = (() => {
  const $ = (id) => document.getElementById(id);
  const providerEl = $('aiProvider');
  const keyEl = $('aiApiKey');
  const keySaveBtn = $('aiKeySaveBtn');
  const keyRowEl = $('aiKeyRow');
  const promptEl = $('aiPrompt');
  const suggestBtn = $('aiSuggestBtn');
  const generateBtn = $('aiGenerateBtn');
  const clearBtn = $('aiClearBtn');
  const outputEl = $('aiOutput');
  const copyBtn = $('aiCopyBtn');
  const applyBtn = $('aiApplyBtn');
  const statusEl = $('aiStatus');

  const LOCAL_KEY_NAME = 'jukahub-ai-key';
  const LOCAL_PROVIDER_NAME = 'jukahub-ai-provider';

  const FREE_PROVIDER = 'free';
  const CHROME_PROVIDER = 'chrome';
  const SUPPORTED_PROVIDERS = new Set([FREE_PROVIDER, CHROME_PROVIDER, 'openrouter', 'openai', 'anthropic']);

  // Chrome on-device Gemini Nano (Prompt API). This is a progressive enhancement:
  // it only appears as an option when the browser exposes LanguageModel and the
  // model is (or can become) available. No API key, no network for inference.
  let chromeAvailability = null; // 'available' | 'downloaded' | 'downloading' | 'unavailable' | null
  let chromeSession = null;

  function status(msg, kind = '') {
    if (!statusEl) return;
    statusEl.textContent = msg;
    statusEl.className = 'ai-status' + (kind ? ' ' + kind : '');
  }

  // Providers without a key keep the row hidden. The row itself carries the
  // inline display:none, so it has to be the element we toggle -- toggling the
  // input's parent left the key field unreachable behind a hidden row.
  function syncKeyRowVisibility() {
    if (!keyRowEl) return;
    const p = provider();
    keyRowEl.style.display = (p === FREE_PROVIDER || p === CHROME_PROVIDER) ? 'none' : 'flex';
  }

  const modelStatusEl = $('aiModelStatus');
  const modelStatusRowEl = $('aiModelStatusRow');

  function setModelStatus(kind, msg) {
    if (!modelStatusEl) return;
    modelStatusEl.className = 'ai-model-status' + (kind ? ' ' + kind : '');
    modelStatusEl.textContent = '';
    if (msg) {
      // The stylesheet styles a .ai-model-dot status indicator; build it here.
      const dot = document.createElement('span');
      dot.className = 'ai-model-dot';
      dot.setAttribute('aria-hidden', 'true');
      modelStatusEl.appendChild(dot);
      modelStatusEl.appendChild(document.createTextNode(msg));
    }
    if (modelStatusRowEl) {
      modelStatusRowEl.style.display = msg ? 'flex' : 'none';
    }
  }

  function chromeStatusMessage(avail) {
    if (avail === 'available') return 'On-device AI ready — runs Gemini Nano locally, no key needed.';
    if (avail === 'downloaded') return 'On-device AI model downloaded — ready to use.';
    if (avail === 'downloading') return 'On-device AI model is downloading (this can take a few minutes).';
    if (avail === 'unavailable') return 'On-device AI is not available in this browser or on this device.';
    return '';
  }

  function kil(msg) { throw new Error(msg); }

  function provider() {
    const val = (providerEl && providerEl.value) || FREE_PROVIDER;
    if (!SUPPORTED_PROVIDERS.has(val)) kil('Unsupported provider: ' + val);
    // Hide the Chrome option if the browser does not expose LanguageModel at all.
    if (val === CHROME_PROVIDER && !isChromePromptApiPresent()) {
      // Best-effort: fall back to free mode rather than failing.
      if (providerEl) providerEl.value = FREE_PROVIDER;
    }
    return val;
  }

  function apiKey() {
    const p = provider();
    if (p === FREE_PROVIDER || p === CHROME_PROVIDER) return null;
    return (keyEl && keyEl.value.trim()) || loadStoredKey();
  }


  function loadStoredKey() {
    try {
      const stored = localStorage.getItem(LOCAL_KEY_NAME);
      if (stored) {
        if (keyEl) keyEl.value = stored;
        const storedProvider = localStorage.getItem(LOCAL_PROVIDER_NAME);
        if (storedProvider && providerEl) providerEl.value = storedProvider;
      }
    } catch (e) { /* private mode can deny localStorage */ }
    return null;
  }

  function storeKey(key) {
    try {
      if (key) {
        localStorage.setItem(LOCAL_KEY_NAME, key);
        localStorage.setItem(LOCAL_PROVIDER_NAME, provider());
      } else {
        localStorage.removeItem(LOCAL_KEY_NAME);
        localStorage.removeItem(LOCAL_PROVIDER_NAME);
      }
    } catch (e) { /* ignore */ }
  }

  let lastReplyText = '';

  // Append one chat bubble. The transcript is a message list so the panel reads
  // as a conversation instead of a single result box.
  function bubble(role, text, kind) {
    if (!outputEl) return;
    const placeholder = outputEl.querySelector('.ai-msg-placeholder');
    if (placeholder) placeholder.remove();

    const msg = document.createElement('div');
    msg.className = 'ai-msg ai-msg-' + role;

    const b = document.createElement('div');
    b.className = 'ai-bubble' + (kind ? ' is-' + kind : '');
    b.textContent = text == null ? '' : String(text);
    msg.appendChild(b);
    outputEl.appendChild(msg);
    outputEl.scrollTop = outputEl.scrollHeight;

    if (role === 'ai') lastReplyText = b.textContent;
  }

  function showWelcome() {
    if (!outputEl) return;
    outputEl.textContent = '';
    const msg = document.createElement('div');
    msg.className = 'ai-msg ai-msg-ai ai-msg-placeholder';
    const b = document.createElement('div');
    b.className = 'ai-bubble';
    b.textContent = 'Hi! Tell me what to build \u2014 for example \u201cadd a Media tile, a Files tile, and a search bar at the top\u201d. I can suggest a layout or generate a scene config you can Apply.';
    msg.appendChild(b);
    outputEl.appendChild(msg);
    lastReplyText = '';
  }

  function showOutput(text) {
    if (text == null) return;
    const isError = /^\/\/\s*Error/i.test(String(text));
    bubble('ai', text, isError ? 'error' : '');
  }

  // One-line human summary of a generated config, so the reply is readable
  // before the raw JSON is unfolded.
  function describeConfig(parsed) {
    const { elements, name } = configElements(parsed);
    let what;
    if (elements) {
      what = 'Generated ' + elements.length + ' element' + (elements.length === 1 ? '' : 's');
      if (name) what += ' for \u201c' + name.slice(0, 40) + '\u201d';
    } else {
      what = 'Generated a config';
    }
    return what + '. Click Apply to import it into this scene.';
  }

  // A generated config can be thousands of characters, which would flood the
  // transcript, so show the summary and fold the JSON behind a disclosure.
  function showConfigOutput(parsed) {
    if (!outputEl) return;
    const summary = describeConfig(parsed);
    let json = '';
    try { json = JSON.stringify(parsed, null, 2); } catch (e) { json = String(parsed); }

    const placeholder = outputEl.querySelector('.ai-msg-placeholder');
    if (placeholder) placeholder.remove();

    const msg = document.createElement('div');
    msg.className = 'ai-msg ai-msg-ai';

    const b = document.createElement('div');
    b.className = 'ai-bubble';
    b.appendChild(document.createTextNode(summary));

    const det = document.createElement('details');
    det.className = 'ai-json';
    const sum = document.createElement('summary');
    sum.textContent = 'Show config JSON';
    const pre = document.createElement('pre');
    pre.className = 'ai-json-code';
    pre.textContent = json;
    det.appendChild(sum);
    det.appendChild(pre);
    b.appendChild(det);

    msg.appendChild(b);
    outputEl.appendChild(msg);
    outputEl.scrollTop = outputEl.scrollHeight;

    lastReplyText = summary + '\n\n' + json;
  }

  function extractJson(text) {
    if (typeof text !== 'string' || !text) return null;
    const candidates = [];
    const seen = new Set();
    const parsedCache = new Map();

    const addCandidate = (chunk) => {
      if (!chunk) return;
      const trimmed = chunk.trim();
      // Skip markdown-fenced or obviously non-JSON wrappers.
      if (/^```/.test(trimmed)) return;
      if (!trimmed || seen.has(trimmed)) return;
      seen.add(trimmed);
      candidates.push({ text: trimmed, length: trimmed.length });
    };

    // Prefer the largest balanced object near the end of the message, since
    // provider replies often include explanation text before the JSON.
    const objectMatches = [...text.matchAll(/\{(?:[^{}]|(?:\{[^{}]*\}))*\}/g)].map((m) => m[0]);
    objectMatches.sort((a, b) => b.length - a.length);
    objectMatches.forEach(addCandidate);

    const arrayMatches = [...text.matchAll(/\[(?:[^[\]]|(?:\[[^[\]]*\]))*\]/g)].map((m) => m[0]);
    arrayMatches.sort((a, b) => b.length - a.length);
    arrayMatches.forEach(addCandidate);

    // Also try the whole response trimmed, in case the model returned only JSON.
    addCandidate(text);

    // Score a parsed value by how well it matches the shape we expect.
    function shapeScore(parsed) {
      if (!parsed || typeof parsed !== 'object') return 0;
      if (Array.isArray(parsed)) {
        if (parsed.length && typeof parsed[0] === 'object' && parsed[0].type) return 60;
        if (parsed.length && typeof parsed[0] === 'object' && parsed[0].scenes) return 100;
        return 5;
      }
      if (Array.isArray(parsed.scenes)) return 100;
      if (Array.isArray(parsed.elements)) return 50;
      return 10;
    }

    // Prefer bigger candidates, then better-shaped ones.
    candidates.sort((a, b) => {
      const sa = shapeScore(parsedCache.get(a.text));
      const sb = shapeScore(parsedCache.get(b.text));
      if (sb !== sa) return sb - sa;
      return b.length - a.length;
    });

    for (const candidate of candidates) {
      try {
        const parsed = JSON.parse(candidate.text);
        parsedCache.set(candidate.text, parsed);
        if (parsed && typeof parsed === 'object') return parsed;
      } catch (e) { /* not JSON */ }
    }
    return null;
  }

  function sanitizePromptForProvider(promptText) {
    // Keep the prompt readable but avoid sending control characters that can
    // confuse JSON-bodied HTTP requests or the provider parser.
    if (typeof promptText !== 'string') return '';
    return promptText.replace(/[\x00-\x08\x0b\x0c\x0e-\x1f]/g, ' ').slice(0, 4000);
  }


  async function freeGenerate(promptText) { return localSuggest(promptText); }

  function isChromePromptApiPresent() {
    try {
      // The Prompt API global is LanguageModel (a WICG proposal, currently Chrome-only).
      return Boolean(window.LanguageModel && typeof window.LanguageModel.create === 'function');
    } catch (e) {
      return false;
    }
  }

  async function detectChromeAvailability() {
    if (!isChromePromptApiPresent()) {
      chromeAvailability = 'unavailable';
      return chromeAvailability;
    }
    try {
      chromeAvailability = await window.LanguageModel.availability();
      return chromeAvailability;
    } catch (e) {
      chromeAvailability = 'unavailable';
      return chromeAvailability;
    }
  }

  async function ensureChromeSession() {
    if (!isChromePromptApiPresent()) return null;
    if (chromeSession) {
      try {
        const info = chromeSession; // session object; we only reuse if still valid
        return chromeSession;
      } catch (e) {
        chromeSession = null;
      }
    }
    try {
      chromeSession = await window.LanguageModel.create({
        systemPrompt: AI_SYSTEM,
      });
      return chromeSession;
    } catch (e) {
      chromeSession = null;
      return null;
    }
  }

  async function chromeGenerate(promptText) {
    if (!isChromePromptApiPresent()) kil('On-device AI is not available in this browser.');
    const avail = await detectChromeAvailability();
    if (avail === 'unavailable') kil('On-device AI is not available on this device (unsupported hardware, or the model is disabled in chrome://flags).');
    if (avail === 'downloading') {
      // The model is still downloading. Try once more after a short wait; if still
      // downloading, give a helpful error pointing at chrome://on-device-internals.
      await new Promise((r) => setTimeout(r, 3500));
      const recheck = await detectChromeAvailability();
      if (recheck === 'downloading' || recheck === 'unavailable') {
        kil('On-device AI model is still downloading. Wait a few minutes and try again, or visit chrome://on-device-internals to check progress.');
      }
    }
    const session = await ensureChromeSession();
    if (!session) kil('Could not create an on-device AI session.');
    try {
      const answer = await session.prompt(promptText);
      if (!answer || !answer.trim()) kil('Empty response from on-device AI.');
      return answer;
    } finally {
      // Free the session/gpu ram promptly. We recreate next call.
      try { chromeSession = null; } catch (e) { /* ignore */ }
    }
  }

  async function openrouterGenerate(promptText, key) {
    if (!key) kil('OpenRouter key is required for this provider.');
    return callJsonChat({
      url: 'https://openrouter.ai/api/v1/chat/completions',
      headers: {
        Authorization: 'Bearer ' + key,
        'Content-Type': 'application/json',
        'HTTP-Referer': window.location.href || 'https://generate.jukalang.com',
        'X-Title': 'JukaHub Generator',
      },
      body: {
        model: 'openai/gpt-oss-120b',
        messages: [
          { role: 'system', content: AI_SYSTEM },
          { role: 'user', content: promptText },
        ],
        max_tokens: 2000,
        temperature: 0.2,
      },
    });
  }

  async function openaiGenerate(promptText, key) {
    if (!key) kil('OpenAI key is required for this provider.');
    return callJsonChat({
      url: 'https://api.openai.com/v1/chat/completions',
      headers: {
        Authorization: 'Bearer ' + key,
        'Content-Type': 'application/json',
      },
      body: {
        model: 'gpt-4o-mini',
        messages: [
          { role: 'system', content: AI_SYSTEM },
          { role: 'user', content: promptText },
        ],
        max_tokens: 2000,
        temperature: 0.2,
      },
    });
  }


  async function anthropicGenerate(promptText, key) {
    if (!key) kil('Anthropic key is required for this provider.');
    return callJsonChat({
      url: 'https://api.anthropic.com/v1/messages',
      headers: {
        'x-api-key': key,
        'anthropic-version': '2023-06-01',
        // Required by Anthropic for calls made straight from a browser; without
        // it the request is blocked by CORS before it reaches the API.
        'anthropic-dangerous-direct-browser-access': 'true',
        'Content-Type': 'application/json',
      },
      body: {
        model: 'claude-haiku-5-5',
        max_tokens: 2000,
        temperature: 0.2,
        system: AI_SYSTEM,
        messages: [
          { role: 'user', content: promptText },
        ],
      },
    });
  }

  async function callJsonChat(req) {
    const res = await fetch(req.url, { method: 'POST', headers: req.headers, body: JSON.stringify(req.body) });
    if (!res.ok) {
      let detail = res.statusText;
      try {
        const errBody = await res.json();
        if (errBody && errBody.error && errBody.error.message) detail = errBody.error.message;
      } catch (e) { /* fallback to status text */ }
      kil('Provider error: ' + detail);
    }
    const data = await res.json();
    const answer =
      req.url.includes('anthropic.com')
        ? (data.content && Array.isArray(data.content) && data.content[0] && data.content[0].text) || ''
        : (data.choices && data.choices[0] && data.choices[0].message && data.choices[0].message.content) || '';
    if (!answer.trim()) kil('Empty response from provider.');
    return answer;
  }


  function localSuggest(promptText) {
    const raw = promptText || '';
    const lower = raw.toLowerCase();

    // --- Title extraction (handles 'title: X', 'call it X', 'named X', 'rename to X',
    // 'app name is X', quoted titles, and 'JukaHub' style names) ---
    function extractTitleText(text) {
      if (!text) return '';
      const t = text.trim();
      // Explicit label: 'title: ...' or 'title is ...'
      const explicit = /^(?:title|app name|named|call it|rename(?:d?)\s*to|name is|caption)\b\s*[:=]?\s*/i.exec(t);
      if (explicit) {
        const after = t.slice(explicit[0].length).trim();
        // Stop the title at the first separator so "title: X, add tiles" -> "X".
        const namePart = after.split(/[,;.\u2022]|\s+(?:add|plus|with|then)\s+/i)[0].trim();
        const q = namePart.match(/^["']?([^"']+)["']?$/);
        if (q && q[1].trim()) return q[1].trim().slice(0, 80);
      return namePart.slice(0, 80);
      }
      // Quoted title anywhere: "My App" or 'My App'
      const quoted = t.match(/"([^"]{1,80})"|'([^']{1,80})'/);
      if (quoted) return (quoted[1] || quoted[2] || '').trim();
      // 'Name: rest of the request' -> treat the leading segment as the title.
      const colonMatch = t.match(/^([^:]{2,40}):\s+\S/);
      if (colonMatch) {
        const leading = colonMatch[1].trim();
        if (leading && !/^\d+$/.test(leading)) return leading.slice(0, 80);
      }
      return '';
    }

    const titleText = extractTitleText(raw);
    const hasTitle = titleText.length > 0 || /title|app name|rename|named|call it|caption/i.test(lower);

    // --- Intent detection (richer patterns) ---
    const hasMedia = /media|video|youtube|player|watch|films?|movie|stream|tube|trending|shorts?/i.test(lower);
    const hasShorts = /shorts?/i.test(lower);
    const hasIptv = /iptv|live tv|live tv|tv channel|m3u|channel list/i.test(lower);
    const hasPodcast = /podcast|podcasts|rss feed/i.test(lower);
    const hasFiles = /file|browse|explorer|folder|images?|gallery|photos?|samba|smb|file manager/i.test(lower);
    const hasSearch = /search|search bar|searchbox|search field|search box|search input|searchbar|paste link|url|link/i.test(lower);
    const hasSmartSearch = /smart|smart search|ai search|search with ai|auto|auto search/i.test(lower);
    const hasSettings = /setting|config|gear|preferences?|preference|options?|general|appearance|theme/i.test(lower);
    const hasChat = /chat|message|talk|assistant|ask|qa|copilot|discord|slack/i.test(lower);
    const hasTools = /tool|terminal|net speed|benchmark|hardware|disk|log|shell|command|console/i.test(lower);
    const hasFavorites = /favorite|recent|saves?|bookmark|continue|resume|history/i.test(lower);
    const hasPackages = /package|update|repair|patch|install|upgrade|apt/i.test(lower);
    const hasApps = /app|apps|launcher|open|run|start|launch/i.test(lower);
    const hasHome = /home|main|landing|welcome|hello|good (morning|afternoon|evening)/i.test(lower);
    const hasGrid = /grid|tiles?|rows?|columns?|panel|cards?/i.test(lower);
    const wantsCompact = /compact|small|density|dense|narrow/i.test(lower);
    const wantsRows = /row|single row|one row/i.test(lower);
    const wantsTwoRows = /two rows?|2 row|top and bottom|double row/i.test(lower);
    const wantsGrid = /grid|tiles?/i.test(lower) || hasGrid;
    const addTop = /top|above|header|search bar|near the top|first|up top/i.test(lower);
    const addBottom = /bottom|footer|below|underneath|at the bottom|footer/i.test(lower);

    // --- Ask-AI / chatbot intent: if user wants an assistant in the scene, add a Chat tile ---
    const hasAskAi = /ask (the )?ai|ai assistant|ask ai|chat with ai|ai chat|copilot|hey ai/i.test(lower);

    const elements = [];
    const suggestions = [];

    let titleY = 44;
    let contentTopY = 128;
    const canvasW = 1280;
    const canvasH = 720;
    const leftMargin = 36;
    const rightMargin = 36;

    // --- Title (only when explicitly requested, or as a last-resort fallback) ---
    if (hasTitle && titleText.length) {
      elements.push(makeElement('label', titleText, '#F8FAFC', 'none', 'title', 'center', canvasW - leftMargin - rightMargin, 64, leftMargin, titleY, ''));
      suggestions.push('Added a centered title "' + titleText + '" near the top.');
      contentTopY = 136;
    } else if (hasTitle) {
      elements.push(makeElement('label', 'My Retro GUI', '#F8FAFC', 'none', 'title', 'center', canvasW - leftMargin - rightMargin, 64, leftMargin, titleY, ''));
      suggestions.push('Added a centered title near the top.');
      contentTopY = 136;
    }

    // --- Search bar (smart vs plain) ---
    if (addTop && hasSearch) {
      const inputW = wantsCompact ? 560 : 720;
      const btnW = 120;
      const rowY = contentTopY;
      const placeholder = hasSmartSearch ? 'Search or paste a link' : 'Search or paste YouTube link';
      const trigger = hasSmartSearch ? 'youtube_smart' : 'youtube_search';
      elements.push(makeElement('input', '', '#F8FAFC', '#0F1420', 'medium', 'left', inputW, 46, leftMargin, rowY, placeholder));
      elements.push(makeElement('button', 'Search', '#F8FAFC', '#1E3A8A', 'medium', 'left', btnW, 46, leftMargin + inputW + 12, rowY, '', trigger));
      suggestions.push('Added a search input + Search button near the top' + (hasSmartSearch ? ' (smart search mode)' : '') + '.');
      contentTopY = rowY + 70;
    }

    // --- Tile catalog (expanded: IPTV, podcasts, shorts, home) ---
    const tileCatalog = [
      { text: 'Media', trigger: 'change_scene:Tube', fg: '#F8FAFC', bg: '#1E3A8A', keywords: ['media','video','youtube','player','watch','films','movie','stream','tube'] },
      { text: 'Shorts', trigger: 'change_scene:Shorts', fg: '#F8FAFC', bg: '#7C3AED', keywords: ['shorts'] },
      { text: 'Live TV', trigger: 'change_scene:IPTV', fg: '#F8FAFC', bg: '#0E7490', keywords: ['iptv','live tv','tv channel','m3u','channel list'] },
      { text: 'Podcasts', trigger: 'change_scene:Podcasts', fg: '#F8FAFC', bg: '#B45309', keywords: ['podcast','podcasts','rss feed'] },
      { text: 'Files', trigger: 'change_scene:FileExplorer', fg: '#F8FAFC', bg: '#0F766E', keywords: ['file','browse','explorer','folder','images','gallery','photos','samba','smb'] },
      { text: 'Packages', trigger: 'change_scene:Packages', fg: '#F8FAFC', bg: '#9A3412', keywords: ['package','update','repair','patch','install','upgrade'] },
      { text: 'Chat', trigger: 'change_scene:Chat', fg: '#F8FAFC', bg: '#5B21B6', keywords: ['chat','message','talk','discord','slack'] },
      { text: 'Favorites', trigger: 'change_scene:Favorites', fg: '#F8FAFC', bg: '#9D174D', keywords: ['favorite','recent','saves','bookmark','continue','resume','history'] },
      { text: 'Apps', trigger: 'change_scene:Apps', fg: '#F8FAFC', bg: '#334155', keywords: ['app','apps','launcher','open','run','start','launch'] },
      { text: 'Settings', trigger: 'change_scene:Settings', fg: '#F8FAFC', bg: '#475569', keywords: ['setting','config','gear','preferences','preference','options','general','appearance','theme'] },
      { text: 'Tools', trigger: 'change_scene:Misc', fg: '#F8FAFC', bg: '#0E7490', keywords: ['tool','terminal','net speed','benchmark','hardware','disk','log','shell','command'] },
      { text: 'Ask AI', trigger: 'change_scene:Chat', fg: '#F8FAFC', bg: '#16A34A', keywords: ['ask ai','ai assistant','ai chat','copilot','hey ai'] },
    ];

    // --- Dedupe tile catalog into a priority list based on what the user asked for.
    // Each requested keyword picks the matching tile; the first match wins so we don't
    // add the same tile twice.
    const chosenCatalog = [];
    const usedIndices = new Set();
    function tryAddTileByKeywords(keywords) {
      for (let i = 0; i < tileCatalog.length; i++) {
        if (usedIndices.has(i)) continue;
        if (keywords.some(kw => tileCatalog[i].keywords.includes(kw))) {
          usedIndices.add(i);
          chosenCatalog.push(i);
          return true;
        }
      }
      return false;
    }

    // Priority order: most-specific first. Each helper returns true if it added a tile.
    if (hasShorts && tryAddTileByKeywords(['shorts'])) suggestions.push('Added a Shorts tile.');
    if (hasIptv && tryAddTileByKeywords(['iptv','live tv','tv channel','m3u','channel list'])) suggestions.push('Added a Live TV tile.');
    if (hasPodcast && tryAddTileByKeywords(['podcast','rss feed'])) suggestions.push('Added a Podcasts tile.');
    if (hasMedia && tryAddTileByKeywords(['media','video','youtube','player','watch','films','movie','stream','tube'])) suggestions.push('Added a Media tile.');
    if (hasFiles && tryAddTileByKeywords(['file','browse','explorer','folder','images','gallery','photos','samba','smb'])) suggestions.push('Added a Files tile.');
    if (hasPackages && tryAddTileByKeywords(['package','update','repair','patch','install','upgrade'])) suggestions.push('Added a Packages tile.');
    if (hasChat && !hasAskAi && tryAddTileByKeywords(['chat','message','talk','discord','slack'])) suggestions.push('Added a Chat tile.');
    if (hasAskAi && tryAddTileByKeywords(['ask ai','ai assistant','ai chat','copilot','hey ai'])) suggestions.push('Added an Ask AI tile (opens Chat).');
    if (hasFavorites && tryAddTileByKeywords(['favorite','recent','saves','bookmark','continue','resume','history'])) suggestions.push('Added a Favorites tile.');
    if (hasApps && tryAddTileByKeywords(['app','apps','launcher','open','run','start','launch'])) suggestions.push('Added an Apps tile.');
    if (hasSettings && tryAddTileByKeywords(['setting','config','gear','preferences','preference','options','general','appearance','theme'])) suggestions.push('Added a Settings tile.');
    if (hasTools && tryAddTileByKeywords(['tool','terminal','net speed','benchmark','hardware','disk','log','shell','command'])) suggestions.push('Added a Tools tile.');

    // --- Determine layout style ---
    function selectedTileStyle() {
      if (wantsCompact) return { w: 180, h: 120, perRow: 6, gapX: 16, gapY: 18 };
      if (wantsRows) return { w: 220, h: 132, perRow: 5, gapX: 18, gapY: 20 };
      if (wantsTwoRows) return { w: 260, h: 158, perRow: 4, gapX: 22, gapY: 24 };
      if (wantsGrid || hasGrid) return { w: 300, h: 180, perRow: 3, gapX: 26, gapY: 26 };
      return { w: 260, h: 158, perRow: 4, gapX: 22, gapY: 24 };
    }

    const style = selectedTileStyle();
    const tileW = style.w;
    const tileH = style.h;
    const perRow = style.perRow;
    const tGapX = style.gapX;
    const tGapY = style.gapY;

    // --- Place tiles ---
    let placed = 0;
    function planTile(catalogIndex, rowOffset = 0) {
      const catalog = tileCatalog[catalogIndex];
      if (!catalog) return;
      const col = placed % perRow;
      const row = Math.floor(placed / perRow) + rowOffset;
      const x = leftMargin + col * (tileW + tGapX);
      const y = contentTopY + row * (tileH + tGapY);
      if (y + tileH > canvasH - 8) {
        suggestions.push('Not enough vertical space for every requested tile, so I dropped a few lower tiles.');
        return;
      }
      elements.push(makeElement('button', catalog.text, catalog.fg, catalog.bg, 'big', 'left', tileW, tileH, x, y, '', catalog.trigger));
      placed++;
    }

    chosenCatalog.forEach((idx) => planTile(idx, 0));

    // --- Fallback: if nothing was placed, give a minimal sensible home screen ---
    if (chosenCatalog.length === 0) {
      if (wantsTwoRows || wantsRows) {
        // Fill a balanced grid with the most common home actions
        [0, 4, 7, 5, 6, 3].forEach((idx) => {
          if (placed < perRow * 2) planTile(idx, 0);
        });
        suggestions.push('No specific tiles mentioned, so I filled a balanced grid with common home actions.');
      } else if (hasHome) {
        // Home/landing: Media + Files + Settings + Chat
        [0, 4, 6, 3].forEach((idx) => planTile(idx, 0));
        suggestions.push('Added a small home set: Media, Files, Settings, Chat.');
      } else {
        // Smallest meaningful screen: Media + Files + Settings
        [0, 4, 6].forEach((idx) => planTile(idx, 0));
        suggestions.push('Added a minimal set: Media, Files, Settings.');
      }
    }

    // --- Control hint at the bottom ---
    if (addBottom && elements.length) {
      const hintText = 'Use D-Pad to navigate \u00b7 A to open \u00b7 B to back';
      elements.push(makeElement('label', hintText, '#9AA6B6', 'none', 'small', 'center', canvasW - leftMargin - rightMargin, 30, leftMargin, canvasH - 44, ''));
      suggestions.push('Added a small control hint near the bottom.');
    }

    // --- If still empty (shouldn't happen now), force a title ---
    if (elements.length === 0) {
      suggestions.push('I couldn\u2019t place anything from that request, so I added a minimal centered title so the scene isn\u2019t empty.');
      elements.push(makeElement('label', titleText || 'My Retro GUI', '#F8FAFC', 'none', 'title', 'center', canvasW - leftMargin - rightMargin, 64, leftMargin, contentTopY, ''));
    }

    // --- Build output ---
    // Rendered as a short chat reply: the per-element coordinates used to be
    // dumped here too, which buried the answer in developer noise.
    const lines = [];
    if (suggestions.length) {
      lines.push('Here\u2019s what I\u2019d add:');
      suggestions.forEach((s) => lines.push('\u2022 ' + s));
    } else {
      lines.push('I didn\u2019t spot a specific layout in that request. Things you can say:');
      lines.push('\u2022 \u201cadd a Media tile\u201d to drop one tile.');
      lines.push('\u2022 \u201csearch bar at the top\u201d to add an input + button.');
      lines.push('\u2022 \u201ctitle: JukaHub\u201d to set the title text.');
      lines.push('\u2022 \u201ctwo rows of tiles\u201d for a denser grid.');
      lines.push('\u2022 \u201cask ai\u201d to add an Ask AI tile.');
      lines.push('\u2022 Pick another provider under Settings for more varied layouts.');
    }
    lines.push('');
    lines.push(elements.length + ' element' + (elements.length === 1 ? '' : 's') +
      ' ready \u2014 click Apply to add them to this scene.');

    showOutput(lines.join('\n'));
    status('Suggestion ready \u2014 click Apply to add these elements to the current scene.', 'success');
    lastSuggestion = elements;
    return elements;
  }


  function makeElement(type, text, color, bgColor, font, align, w, h, x, y, variableOrPlaceholder, trigger) {
    return {
      type: type,
      text: text,
      color: color,
      bgColor: bgColor,
      font: font,
      align: align,
      w: String(w),
      h: String(h),
      x: String(x),
      y: String(y),
      variable: variableOrPlaceholder || '',
      placeholder: variableOrPlaceholder || '',
      trigger: trigger || '',
    };
  }

  function suggest() { return run('suggest'); }

  function generate() {
    const promptText = promptEl.value.trim();
    if (!promptText) kil('Write what you want the scene to look like first.');
    return run('generate');
  }

  async function run(mode) {
    const providerName = provider();
    const key = apiKey();
    const promptText = promptEl.value.trim();
    if (!promptText) kil('Write what you want first (for example: "add a Media tile and a search bar at the top").');
    // Echo the request into the transcript and clear the composer, chat-style.
    bubble('user', promptText);
    promptEl.value = '';
    const safePromptText = sanitizePromptForProvider(promptText);
    const builtPrompt = mode === 'suggest' ? SUGGEST_PROMPT(safePromptText) : GENERATE_PROMPT(safePromptText);

    let answer;
    try {
      status('Thinking\u2026', '');
      if (providerName === FREE_PROVIDER) {
        // localSuggest() is a keyword matcher, so it must see the user's own words
        // (not the instruction prompt, whose example JSON would match everything).
        // It also returns element objects and already rendered a readable summary
        // into the output pane, so don't overwrite it with the raw array.
        answer = await freeGenerate(safePromptText);
        if (!answer) kil('Local suggestion returned nothing.');
        status('Suggestion ready \u2014 click Apply to add these elements to the current scene.', 'success');
        return;
      }

      answer = await providerRoute(providerName, builtPrompt, key);
      const parsed = extractJson(answer);
      if (parsed && (parsed.scene || parsed.elements || parsed.scenes || Array.isArray(parsed))) {
        showConfigOutput(parsed);
        status('Config JSON generated \u2014 click Apply to import it.', 'success');
        lastGenerated = parsed;
        return;
      }
      showOutput(answer);
      status('Got a text response \u2014 I tried to extract JSON, but it may need a manual copy.', 'warning');
    } catch (err) {
      status('AI error: ' + err.message, 'error');
      showOutput('// Error\n' + err.message);
      throw err;
    }
  }

  async function providerRoute(providerName, promptText, key) {
    if (providerName === CHROME_PROVIDER) return chromeGenerate(promptText);
    if (providerName === 'openrouter') return openrouterGenerate(promptText, key);
    if (providerName === 'openai') return openaiGenerate(promptText, key);
    if (providerName === 'anthropic') return anthropicGenerate(promptText, key);
    return freeGenerate(promptText);
  }


  function clear() {
    promptEl.value = '';
    status('');
    lastSuggestion = null;
    lastGenerated = null;
    showWelcome();
    if (promptEl) promptEl.focus();
  }

  function apply() {
    const suggestion = lastSuggestion;
    const generated = lastGenerated;

    if (suggestion && Array.isArray(suggestion) && suggestion.length) {
      const applied = addSuggestedElements(suggestion) || [];
      if (applied.length) {
        status('Applied ' + applied.length + ' of ' + suggestion.length +
          ' element(s) to the current scene.', 'success');
      } else {
        status('Those elements are already in this scene \u2014 nothing new to add.', 'warning');
      }
      return;
    }

    if (generated && typeof generated === 'object') {
      applyGeneratedConfig(generated);
      return;
    }

    kil('Nothing to apply \u2014 run Suggest Layout or Generate Config first.');
  }

  function addSuggestedElements(elements) {
    if (!elements || !elements.length) return;
    const canvasEl = document.getElementById('canvas');
    if (!canvasEl) kil('Canvas not found.');

    const applied = [];
    elements.forEach((spec) => {
      if (!spec || !spec.type) return;
      const type = String(spec.type).toLowerCase();
      if (!['button', 'label', 'input'].includes(type)) return;

      const x = clampPos(parseInt(spec.x, 10) || 0, 1280);
      const y = clampPos(parseInt(spec.y, 10) || 0, 720);
      const w = Math.max(60, parseInt(spec.w, 10) || parseInt(spec.width, 10) || 120);
      const h = Math.max(36, parseInt(spec.h, 10) || parseInt(spec.height, 10) || 48);

      const existing = canvasEl.querySelector(
        '.element[data-type="' + type + '"][data-text="' + escapeAttribute((spec.text || '').slice(0, 120)) + '"]'
      ) ||
        canvasEl.querySelector(
          '.element[data-type="' + type + '"][data-text=""]'
        );
      if (existing) return;

      const el = document.createElement('div');
      el.className = 'element';
      el.style.position = 'absolute';
      el.style.left = x + 'px';
      el.style.top = y + 'px';
      el.style.width = w + 'px';
      el.style.height = h + 'px';
      el.setAttribute('data-x', x);
      el.setAttribute('data-y', y);
      el.setAttribute('data-width', w);
      el.setAttribute('data-height', h);

      const textColor = (spec.color || '').trim() || '#F8FAFC';
      const bgColor = (spec.bgColor || '').trim() || 'transparent';

      if (type === 'label') {
        el.style.background = 'none';
        el.style.color = textColor;
      } else if (type === 'button') {
        el.style.background = bgColor;
        el.style.color = textColor;
      } else if (type === 'input') {
        el.style.background = bgColor || '#0F1420';
        el.style.color = textColor || '#F8FAFC';
        const input = document.createElement('input');
        input.type = 'text';
        input.className = 'element-input';
        input.value = (spec.placeholder || '').slice(0, 200);
        input.placeholder = (spec.placeholder || '').slice(0, 200);
        el.appendChild(input);
      } else {
        el.style.background = bgColor || '#111';
        el.style.color = textColor || '#F8FAFC';
      }

      // Persist what the assistant chose. The exporter reads these attributes
      // rather than the inline CSS, so without them an imported element lost
      // its colours on export, and the duplicate check below never matched.
      el.setAttribute('data-color', textColor);
      const declaredBg = (spec.bgColor || '').trim();
      if (declaredBg && declaredBg !== 'transparent') el.setAttribute('data-bg-color', declaredBg);
      el.setAttribute('data-text', (spec.text || '').slice(0, 120));

      const textSpan = document.createElement('span');
      textSpan.className = 'text-content';
      textSpan.textContent = (spec.text || '').slice(0, 120) || type;
      el.appendChild(textSpan);

      const removeButton = document.createElement('span');
      removeButton.className = 'remove-button';
      removeButton.textContent = '\u2715';
      el.appendChild(removeButton);

      el.setAttribute('data-type', type);
      if (spec.trigger) el.setAttribute('data-trigger', String(spec.trigger).slice(0, 120));
      if (spec.variable) el.setAttribute('data-variable', String(spec.variable).slice(0, 120));
      if (spec.font) el.setAttribute('data-font', String(spec.font).slice(0, 40));

      canvasEl.appendChild(el);
      if (typeof setupElementEvents === 'function') setupElementEvents(el);
      const sceneList = (typeof scenes !== 'undefined' && scenes[currentScene]) ? scenes[currentScene] : null;
      if (!sceneList) scenes[currentScene] = [];
      if (!scenes[currentScene].includes(el)) scenes[currentScene].push(el.cloneNode(true));
      applied.push(el);
    });

    if (applied.length) {
      if (typeof scheduleAutoSave === 'function') scheduleAutoSave();
    }

    return applied;
  }

  function escapeAttribute(value) {
    return String(value)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  function clampPos(pos, max) { return Math.max(0, Math.min(pos, max - 60)); }


  // The generate prompt asks for { "scenes": [ { "name", "elements": [...] } ] },
  // but a reply can also be { "scene": {...} }, { "elements": [...] } or a bare
  // array. Normalise all of those so Apply always has something to import -- the
  // scenes shape used to be rejected with "did not include any elements".
  function configElements(generated) {
    if (!generated) return { elements: null, name: '' };
    if (Array.isArray(generated)) return { elements: generated, name: '' };
    if (Array.isArray(generated.scenes)) {
      const scene = generated.scenes.find((s) => s && Array.isArray(s.elements));
      if (!scene) return { elements: null, name: '' };
      return { elements: scene.elements, name: scene.name ? String(scene.name) : '' };
    }
    if (generated.scene && Array.isArray(generated.scene.elements)) {
      const scene = generated.scene;
      return { elements: scene.elements, name: scene.name ? String(scene.name) : '' };
    }
    if (Array.isArray(generated.elements)) {
      return { elements: generated.elements, name: generated.name ? String(generated.name) : '' };
    }
    return { elements: null, name: '' };
  }

  function applyGeneratedConfig(generated) {
    const { elements, name } = configElements(generated);
    if (!elements || !elements.length) kil('That reply had no elements to import \u2014 ask the assistant to generate a scene config.');

    const canvasEl = document.getElementById('canvas');
    if (!canvasEl) kil('Canvas not found.');

    let added = 0;
    elements.forEach((spec) => {
      if (!spec.type) return;
      const existing = canvasEl.querySelector(
        '.element[data-type="' + String(spec.type).toLowerCase() + '"][data-text="' + escapeAttribute((spec.text || '').slice(0, 120)) + '"]'
      ) ||
        canvasEl.querySelector(
          '.element[data-type="' + String(spec.type).toLowerCase() + '"][data-text=""]'
        );
      if (existing) return;

      const x = clampPos(parseInt(spec.x, 10) || 0, 1280);
      const y = clampPos(parseInt(spec.y, 10) || 0, 720);
      const w = Math.max(60, parseInt(spec.width, 10) || parseInt(spec.w, 10) || 120);
      const h = Math.max(36, parseInt(spec.height, 10) || parseInt(spec.h, 10) || 48);

      const el = document.createElement('div');
      el.className = 'element';
      el.style.position = 'absolute';
      el.style.left = x + 'px';
      el.style.top = y + 'px';
      el.style.width = w + 'px';
      el.style.height = h + 'px';
      el.setAttribute('data-x', x);
      el.setAttribute('data-y', y);
      el.setAttribute('data-width', w);
      el.setAttribute('data-height', h);

      const type = String(spec.type).toLowerCase();
      if (type === 'label') {
        el.style.background = 'none';
        el.style.color = (spec.color || '').trim() || '#F8FAFC';
      } else if (type === 'button') {
        el.style.background = (spec.bgColor || '').trim() || '#1E3A8A';
        el.style.color = (spec.color || '').trim() || '#F8FAFC';
      } else if (type === 'input') {
        el.style.background = (spec.bgColor || '').trim() || '#0F1420';
        el.style.color = (spec.color || '').trim() || '#F8FAFC';
        const input = document.createElement('input');
        input.type = 'text';
        input.className = 'element-input';
        input.value = (spec.text || spec.placeholder || '').slice(0, 200);
        input.placeholder = (spec.placeholder || spec.text || '').slice(0, 200);
        el.appendChild(input);
      } else {
        el.style.background = (spec.bgColor || '').trim() || '#111';
        el.style.color = (spec.color || '').trim() || '#F8FAFC';
      }

      // See addSuggestedElements(): keep the colours in the attributes the
      // exporter reads, and give the element a text hook for de-duplication.
      el.setAttribute('data-color', (spec.color || '').trim() || '#F8FAFC');
      const declaredBg = (spec.bgColor || '').trim();
      if (declaredBg && declaredBg !== 'transparent') el.setAttribute('data-bg-color', declaredBg);
      el.setAttribute('data-text', (spec.text || '').slice(0, 120));

      const textSpan = document.createElement('span');
      textSpan.className = 'text-content';
      textSpan.textContent = (spec.text || '').slice(0, 120) || type;
      el.appendChild(textSpan);

      const removeButton = document.createElement('span');
      removeButton.className = 'remove-button';
      removeButton.textContent = '\u2715';
      el.appendChild(removeButton);

      el.setAttribute('data-type', type);
      if (spec.trigger) el.setAttribute('data-trigger', String(spec.trigger).slice(0, 120));
      if (spec.variable) el.setAttribute('data-variable', String(spec.variable).slice(0, 120));
      if (spec.font) el.setAttribute('data-font', String(spec.font).slice(0, 40));
      if (spec.placeholder) el.setAttribute('data-placeholder', String(spec.placeholder).slice(0, 200));

      canvasEl.appendChild(el);
      if (typeof setupElementEvents === 'function') setupElementEvents(el);
      const sceneList = (typeof scenes !== 'undefined' && scenes[currentScene]) ? scenes[currentScene] : null;
      if (!sceneList) scenes[currentScene] = [];
      if (!scenes[currentScene].includes(el)) scenes[currentScene].push(el.cloneNode(true));
      added++;
    });

    if (typeof scheduleAutoSave === 'function') scheduleAutoSave();
    if (!added) {
      status('Those elements are already in this scene \u2014 nothing new to add.', 'warning');
      return;
    }
    status('Imported ' + added + ' element(s) into this scene' +
      (name ? ' (from \u201c' + name + '\u201d)' : '') + '.', 'success');
  }


  function copyOutput() {
    const text = lastReplyText || (outputEl ? outputEl.textContent : '');
    if (!text || !String(text).trim()) { status('Nothing to copy yet.', 'warning'); return; }
    try {
      navigator.clipboard.writeText(text).then(
        () => status('Copied to clipboard.', 'success'),
        () => fallbackCopy(text)
      );
    } catch (e) { fallbackCopy(text); }
  }

  function fallbackCopy(text) {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    try {
      document.execCommand('copy');
      status('Copied to clipboard.', 'success');
    } catch (e) { status('Copy failed \u2014 select the result text manually.', 'warning'); }
    document.body.removeChild(ta);
  }

  let lastSuggestion = null;
  let lastGenerated = null;

  function init() {
    loadStoredKey();
    showWelcome();

    if (providerEl && keyEl) {
      // A provider restored from localStorage must reveal the key row too.
      syncKeyRowVisibility();

      providerEl.addEventListener('change', () => {
        syncKeyRowVisibility();
        const p = provider();
        if (p !== FREE_PROVIDER && p !== CHROME_PROVIDER && !keyEl.value) {
          status('Paste your ' + p + ' key above, then click Save key.', 'warning');
        }
        storeKey(keyEl.value.trim());
      });

      keySaveBtn.addEventListener('click', () => {
        const key = (keyEl && keyEl.value.trim()) || '';
        storeKey(key);
        status(key ? 'API key saved locally.' : 'API key cleared.', key ? 'success' : 'warning');
      });
    }

    // Click handlers report failures through the status line instead of leaving
    // an unhandled promise rejection in the console.
    const guard = (fn) => () => {
      try {
        const result = fn();
        if (result && typeof result.catch === 'function') {
          result.catch((err) => { if (err && err.message) status('AI error: ' + err.message, 'error'); });
        }
      } catch (err) {
        if (err && err.message) status('AI error: ' + err.message, 'error');
      }
    };

    if (suggestBtn) suggestBtn.addEventListener('click', guard(suggest));
    if (generateBtn) generateBtn.addEventListener('click', guard(generate));
    if (clearBtn) clearBtn.addEventListener('click', guard(clear));
    if (copyBtn) copyBtn.addEventListener('click', guard(copyOutput));
    if (applyBtn) applyBtn.addEventListener('click', guard(apply));

    // Chat composer behaviour: Enter sends, Shift+Enter adds a line, and the
    // send buttons stay disabled while there is nothing to send.
    if (promptEl) {
      const syncComposer = () => {
        const empty = !promptEl.value.trim();
        [suggestBtn, generateBtn].forEach((b) => { if (b) b.disabled = empty; });
      };
      promptEl.addEventListener('input', syncComposer);
      promptEl.addEventListener('keydown', (e) => {
        if (e.key === 'Enter' && !e.shiftKey) {
          e.preventDefault();
          if (promptEl.value.trim()) guard(suggest)();
        }
      });
      syncComposer();
    }

    // Wire voice controls if the browser supports them (elements may be static HTML
    // or created by ensureVoiceUIElements()).
    ensureVoiceUIElements();
    if (!voiceSupported) {
      voiceSupported = detectVoiceSupport();
    }
    wireVoiceUI();
    if (!voiceSupported) {
      const micBtn = document.getElementById('aiMicBtn');
      if (micBtn) micBtn.style.display = 'none';
      status('Voice input is not available in this browser.', 'warning');
    }

    // Chrome on-device AI (Prompt API): detect availability and surface it.
    const chromePresent = isChromePromptApiPresent();
    if (chromePresent) {
      // Show the status row and probe availability in the background.
      setModelStatus('downloading', 'Checking on-device AI availability…');
      detectChromeAvailability().then((avail) => {
        // Keep the option enabled whenever the API surface exists; the generate step
        // will still give a clear error if the model is unavailable/unsupported.
        if (avail === 'available' || avail === 'downloaded') {
          setModelStatus('ready', chromeStatusMessage(avail));
        } else if (avail === 'downloading') {
          setModelStatus('downloading', chromeStatusMessage(avail));
          // Keep enabled so a user can pick it and wait, or switch away.
        } else {
          setModelStatus('unavailable', chromeStatusMessage(avail));
        }
      }).catch(() => {
        setModelStatus('unavailable', chromeStatusMessage('unavailable'));
      });
    } else {
      setModelStatus('unavailable', 'On-device AI (Chrome Gemini Nano) is not available in this browser.');
    }

    // Disable the Chrome option when the Prompt API isn't exposed at all, so users
    // don't pick a provider that can't work. Re-evaluate on provider change too.
    function updateChromeOptionDisabled() {
      if (!providerEl) return;
      const opt = providerEl.querySelector('option[value="' + CHROME_PROVIDER + '"]');
      if (opt) opt.disabled = !isChromePromptApiPresent();
    }
    updateChromeOptionDisabled();
    providerEl.addEventListener('change', updateChromeOptionDisabled);

    // Re-probe whenever the user picks the Chrome provider, since download state
    // can change between visits.
    if (providerEl) {
      providerEl.addEventListener('change', () => {
        if (provider() === CHROME_PROVIDER) {
          detectChromeAvailability().then((avail) => {
            if (avail === 'available' || avail === 'downloaded') {
              setModelStatus('ready', chromeStatusMessage(avail));
            } else if (avail === 'downloading') {
              setModelStatus('downloading', chromeStatusMessage(avail));
            } else {
              setModelStatus('unavailable', chromeStatusMessage(avail));
            }
          }).catch(() => {
            setModelStatus('unavailable', chromeStatusMessage('unavailable'));
          });
        }
      });
    }

    if (promptEl) {
      promptEl.addEventListener('focus', () => {
        if (!promptEl.value) promptEl.placeholder = 'e.g. Add a Media tile and a search bar at the top';
      });
    }

  }

  // ---- Voice control (builder-only, browser speech recognition) ----
  // This is intentionally optional. If the browser does not expose a speech
  // recognition API, the mic button stays hidden and voice is a no-op.

  function isSpeechAvailable() {
    const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
    return Boolean(SR);
  }

  function detectVoiceSupport() {
    if (!isSpeechAvailable()) return false;
    try {
      const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
      const r = new SR();
      // Some environments throw on construction or on start without a user gesture.
      return true;
    } catch (e) {
      return false;
    }
  }

  let voiceSupported = null;
  let voiceSession = null;
  let voiceListening = false;

  function getSpeechFactory() {
    return (window.SpeechRecognition || window.webkitSpeechRecognition);
  }

  function startVoiceSession(autoRunSuggest) {
    if (!voiceSupported) return false;
    if (voiceListening) return false;

    const SR = getSpeechFactory();
    if (!SR) return false;

    try {
      const recognition = new SR();
      recognition.continuous = false;
      recognition.interimResults = true;
      recognition.lang = 'en-US';
      recognition.maxAlternatives = 1;

      let finalTranscript = '';
      let interimTranscript = '';

      recognition.onresult = (event) => {
        interimTranscript = '';
        finalTranscript = '';
        for (let i = event.resultIndex; i < event.results.length; i++) {
          const result = event.results[i];
          if (result.isFinal) {
            finalTranscript += result[0].transcript;
          } else {
            interimTranscript += result[0].transcript;
          }
        }

        const display = finalTranscript || interimTranscript;
        if (promptEl) {
          if (finalTranscript) {
            promptEl.value = finalTranscript;
            status('Heard: ' + finalTranscript, 'success');
          } else {
            promptEl.value = interimTranscript;
            status('Listening\u2026 ' + interimTranscript, '');
          }
        }
      };

      recognition.onerror = (event) => {
        voiceListening = false;
        if (event.error === 'no-speech') {
          status('No speech heard \u2014 try again.', 'warning');
        } else if (event.error === 'aborted') {
          status('Voice input cancelled.', 'warning');
        } else {
          status('Voice error: ' + event.error, 'error');
        }
        voiceSession = null;
      };

      recognition.onend = () => {
        voiceListening = false;
        voiceSession = null;
        if (finalTranscript) {
          if (promptEl) promptEl.value = finalTranscript;
          status('Voice input ready \u2014 edit or send.', 'success');
          if (autoRunSuggest) {
            try {
              AI.suggest();
            } catch (e) {
              status('Voice auto-run failed: ' + e.message, 'error');
            }
          }
        } else if (!finalTranscript && interimTranscript) {
          // Session ended before a final result; keep what we heard so far.
          status('Voice input ended early \u2014 edit what was heard.', 'warning');
        } else {
          status('Voice input finished.', '');
        }
      };

      recognition.start();
      voiceSession = recognition;
      voiceListening = true;
      status('Listening\u2026', '');
      return true;
    } catch (e) {
      status('Voice start failed: ' + e.message, 'error');
      voiceListening = false;
      voiceSession = null;
      return false;
    }
  }

  function stopVoiceSession() {
    if (!voiceSession) return;
    try {
      voiceSession.stop();
    } catch (e) { /* ignore */ }
    voiceSession = null;
    voiceListening = false;
  }

  function toggleVoice(autoRunSuggest) {
    if (voiceListening) {
      stopVoiceSession();
      return;
    }
    if (!voiceSupported) {
      status('Voice input is not available in this browser.', 'warning');
      return;
    }
    return startVoiceSession(autoRunSuggest);
  }

  function ensureVoiceUIElements() {
    if (!document.getElementById('aiPanel')) return;
    if (document.getElementById('aiMicBtn')) return; // already in DOM (e.g. static HTML)

    const header = document.querySelector('#aiPanel .panel-header');
    if (!header) return;

    const micWrap = document.createElement('div');
    micWrap.className = 'ai-mic-row';
    micWrap.innerHTML =
      '<button id="aiMicBtn" class="action-button small" type="button" title="Voice input">' +
      '<i class="fa-solid fa-microphone" aria-hidden="true"></i></button>' +
      '<label class="ai-toggle-label" title="Enable voice input">' +
      '<input type="checkbox" id="aiVoiceToggle">' +
      '<span>Voice</span>' +
      '</label>' +
      '<label class="ai-toggle-label ai-voice-hint" title="Auto-run suggest after voice input">' +
      '<input type="checkbox" id="aiVoiceAutoRun">' +
      '<span>Auto</span>' +
      '</label>';

    header.appendChild(micWrap);
  }

  function wireVoiceUI() {
    const micBtn = document.getElementById('aiMicBtn');
    const voiceToggleEl = document.getElementById('aiVoiceToggle');
    const autoRunEl = document.getElementById('aiVoiceAutoRun');

    if (micBtn) {
      micBtn.addEventListener('click', () => {
        toggleVoice(false);
      });
    }

    if (voiceToggleEl) {
      voiceToggleEl.addEventListener('change', () => {
        voiceSupported = detectVoiceSupport();
        if (!voiceSupported) {
          voiceToggleEl.checked = false;
          if (micBtn) micBtn.style.display = 'none';
          status('Voice input unavailable on this browser.', 'warning');
        } else {
          if (micBtn) micBtn.style.display = '';
          status('Voice input enabled.', 'success');
        }
      });
    }

    // If the user opts into auto-run, keep behavior explicit: one tap to listen,
    // and if speech is recognized, AI.suggest() runs automatically.
    if (autoRunEl) {
      autoRunEl.addEventListener('change', () => {
        // No immediate action; next voice session will respect this setting.
        status(autoRunEl.checked ? 'Voice auto-run on.' : 'Voice auto-run off.', '');
      });
    }
  }

  function ensureVoiceUI() {
    ensureVoiceUIElements();
    if (!voiceSupported) {
      voiceSupported = detectVoiceSupport();
    }
    wireVoiceUI();

    if (!voiceSupported) {
      const micBtn = document.getElementById('aiMicBtn');
      if (micBtn) {
        micBtn.style.display = 'none';
      }
      status('Voice input is not available in this browser.', 'warning');
    }
  }

  return { init, suggest, generate, clear, apply, copyOutput };
})();


const AI_SYSTEM = [
  'You are a JukaHub GUI builder assistant. JukaHub is a config-driven GUI framework for handheld retro gaming devices (TrimUI, RK2020, etc.).',
  'Apps are defined by a JSON config with one or more scenes. Each scene has a name, an optional background color, and an array of elements.',
  'Each element has at least: type, text, x, y, width, height, color, bgColor, font.',
  'Element types you may use: button, label, input. (If asked for more, use only these three — do not invent new types.)',
  'Buttons commonly use type "button". Input fields use "input". Titles and hints use "label".',
  'Coordinates are pixels on a 1280x720 logical canvas. Keep x between 36 and 1244, y between 44 and 672. Keep sizes realistic for a handheld UI held at arm\'s length: buttons 120-300px wide, tiles 180-300px, titles ~64px tall, inputs ~46px tall.',
  'Font roles: title (biggest, for the app/title), big (card titles), medium (body), small (captions/hints). Pick one per element.',
  'Alignment: "left", "center", or "right". Most tiles are left-aligned text on a colored background.',
  'Triggers are strings that tell the runtime what to do when the element is activated. Common triggers: "change_scene:Tube", "change_scene:FileExplorer", "change_scene:Packages", "change_scene:Chat", "change_scene:Favorites", "change_scene:Settings", "change_scene:Misc", "youtube_smart", "youtube_search", "youtube_play", "youtube_trending", "play_focused", "play_video_from_var", "custom_link", "ip_stream", "iptv_load", "podcast_load", "fe_list", "fe_up", "netspeed_run", "benchmark_run", "terminal_run", "hw_load", "save_config", "cache_clear", "" (none).',
  'Colors: use readable dark-surface backgrounds for buttons (e.g. #1E3A8A, #0F766E, #9A3412, #5B21B6, #334155, #0E7490, #475569) and light text (#F8FAFC). Labels usually have no background (bgColor "") and use #F8FAFC or #9AA6B6.',
  'When asked for a scene config, return ONLY valid JSON. No markdown fences, no commentary before or after. Use this exact top-level shape: { "scenes": [ { "name": "...", "background": "#0B0F17", "elements": [ ... ] } ] }.',
  'When asked for layout advice (not a config), respond in plain text with concise, actionable suggestions.',
  'Be conservative: if the request is ambiguous, add a small sensible set rather than a huge screen. Prefer a title near the top, tiles in a grid, and a small control hint near the bottom.',  ].join('\n');

function SUGGEST_PROMPT(userText) {
  return [
    AI_SYSTEM,
    '',
    'A user wrote: "' + userText + '"\n\n' +
    'Give 3 to 6 concise suggestions for what to add or change, then list the specific elements to place ' +
    'using this shape for each one:\n' +
    '  { "type": "button", "text": "Media", "color": "#F8FAFC", "bgColor": "#1E3A8A", "font": "big", "align": "left", "w": 220, "h": 168, "x": 30, "y": 312, "trigger": "change_scene:Tube" }\n\n' +
    'Rules:\n' +
    ' - Prefer types: button, label, input only. Do not invent new types.\n' +
    ' - Use sensible colors for button backgrounds (#1E3A8A, #0F766E, #9A3412, #5B21B6, #334155, #0E7490, #475569); labels usually have no background (bgColor "").\n' +
    ' - Keep coordinates inside x=36..1244, y=44..672 and sizes realistic for a handheld UI.\n' +
    ' - If a search bar is requested, include both an input (placeholder "Search or paste a link") and a Search button.\n' +
    ' - Respond in plain text: a short bulleted suggestion list, then an "Elements to add" list with the JSON shapes above.',
  ].join('\n');
}

function GENERATE_PROMPT(userText) {
  return [
    AI_SYSTEM,
    '',
    'A user wrote: "' + userText + '"\n\n' +
    'Generate a JSON scene object for a 1280x720 canvas. Return a single scene with this exact top-level shape:\n' +
    '{\n' +
    '  "scenes": [\n' +
    '    {\n' +
    '      "name": "Suggested Scene",\n' +
    '      "background": "#0B0F17",\n' +
    '      "elements": [\n' +
    '        { "type": "label", "text": "Home", "color": "#F8FAFC", "bgColor": "", "font": "title", "align": "center", "x": 36, "y": 44, "width": 1208, "height": 64 },\n' +
    '        { "type": "button", "text": "Media", "color": "#F8FAFC", "bgColor": "#1E3A8A", "font": "big", "align": "left", "x": 36, "y": 150, "width": 220, "height": 168, "trigger": "change_scene:Tube" }\n' +
    '      ]\n' +
    '    }\n' +
    '  ]\n' +
    '}\n\n' +
    'Rules:\n' +
    ' - Return ONLY a single JSON object. No markdown fences (no ```json ... ```), no commentary before or after, no trailing text.\n' +
    ' - The response must parse as JSON with JSON.parse(). Do not include explanatory prose.\n' +
    ' - Use only these element types: button, label, input. Do not invent new types.\n' +
    ' - For each element, include only the fields you need. A label needs: type, text, color, bgColor (often ""), font, align, x, y, width, height. A button adds: trigger. An input adds: placeholder. Do not pad every element with empty placeholder/variable/command/listVariable/columns/rows/image/jsonPath/autoRefresh/externalAppPath/externalAppReturn/variableChange/variableChangeValue fields.\n' +
    ' - Keep x between 36 and 1244, y between 44 and 672. Keep sizes realistic for a handheld UI: buttons/tiles 180-300px wide and 120-180px tall, titles ~64px tall, inputs ~46px tall.\n' +
    ' - Title label near the top (y ~44), tiles in a grid below, optional control hint near the bottom (y ~672).\n' +
    ' - Use clear text labels, not short codes. Use a sensible dark surface color for button backgrounds (#1E3A8A, #0F766E, #9A3412, #5B21B6, #334155, #0E7490, #475569, #7C3AED) and light text (#F8FAFC). Labels usually have no background.\n' +
    ' - If the user asked for a search bar, include one input (placeholder "Search or paste a link") plus one button ("Search", trigger "youtube_search").\n' +
    ' - Be conservative: for an ambiguous request, add a small sensible set (title + 3-5 tiles), not a huge screen.',
  ].join('\n');
}

// Initialize AI panel once the DOM is ready, after the editor's own handler.
if (typeof AI !== 'undefined' && typeof AI.init === 'function') {
  document.addEventListener('DOMContentLoaded', () => {
    try { AI.init(); } catch (e) { console.warn('AI init failed:', e); }
  });
}

// === Quick-start cards & App Info glue for the restored editor shell ==========
// The guide's quick-start cards and the Background Image Path field are newer
// than the editor core, so they are wired here instead of inline.
(function () {
  // Keep the Background Image Path field in step with the loaded config and let
  // it set the canvas background by path (no file upload required).
  const bgPathInput = document.getElementById('backgroundPathInput');
  if (bgPathInput) {
    const syncBgField = () => {
      bgPathInput.value = backgroundPath && !backgroundPath.startsWith('data:') ? backgroundPath : '';
    };
    syncBgField();
    bgPathInput.addEventListener('change', () => {
      backgroundPath = bgPathInput.value.trim();
      canvas.style.backgroundImage = backgroundPath ? `url(${backgroundPath})` : '';
      canvas.style.backgroundSize = backgroundPath ? 'cover' : '';
    });
    const originalLoadJukaApp = loadJukaApp;
    loadJukaApp = function (data) {
      originalLoadJukaApp(data);
      syncBgField();
    };
    const originalClearAll = clearAll;
    clearAll = function () {
      originalClearAll();
      syncBgField();
    };
  }

  // "60-second demo": a controller-navigable taste of the real shell, boxed to
  // the leading-trust dark theme. It loads and jumps straight into preview with
  // D-pad emulation, so a stranger sees the device work without owning one.
  const demoTemplate = {
    title: 'JukaHub demo',
    author: 'JukaHub',
    description: 'A controller-navigable demo of the real home screen. Try the arrow keys.',
    variables: { fontSizes: { title: 48, big: 34, medium: 24, small: 18 } },
    scenes: [
      {
        name: 'Home',
        background: '#0f1115',
        elements: [
          { type: 'label', text: 'JukaHub', color: '#e8eefc', bgColor: '', font: 'title', x: 48, y: 40, width: 480, height: 64 },
          { type: 'label', text: 'Continue · Retro Drive, 45 min', color: '#8FA0B8', bgColor: '', font: 'small', x: 48, y: 104, width: 560, height: 30 },
          { type: 'button', text: 'Media', color: '#f0f2f5', bgColor: '#1E3A8A', font: 'big', x: 48, y: 160, width: 292, height: 200, sceneChange: 'Media', trigger: 'change_scene' },
          { type: 'button', text: 'Files', color: '#f0f2f5', bgColor: '#0F766E', font: 'big', x: 356, y: 160, width: 292, height: 200 },
          { type: 'button', text: 'Apps', color: '#f0f2f5', bgColor: '#7C3AED', font: 'big', x: 664, y: 160, width: 292, height: 200 },
          { type: 'button', text: 'Favorites', color: '#f0f2f5', bgColor: '#9A3412', font: 'big', x: 972, y: 160, width: 292, height: 200 },
          { type: 'button', text: 'YouTube', color: '#f0f2f5', bgColor: '#0E7490', font: 'big', x: 48, y: 384, width: 292, height: 200, trigger: 'youtube_search' },
          { type: 'button', text: 'Browser', color: '#f0f2f5', bgColor: '#334155', font: 'big', x: 356, y: 384, width: 292, height: 200 },
          { type: 'button', text: 'Settings', color: '#f0f2f5', bgColor: '#475569', font: 'big', x: 664, y: 384, width: 292, height: 200, sceneChange: 'Settings', trigger: 'change_scene' },
          { type: 'label', text: 'A · select    B · back    Start · menu', color: '#77839A', bgColor: '', font: 'small', x: 48, y: 640, width: 560, height: 30 }
        ]
      },
      {
        name: 'Media',
        background: '#0f1115',
        elements: [
          { type: 'label', text: 'Media', color: '#e8eefc', bgColor: '', font: 'title', x: 48, y: 40, width: 480, height: 64 },
          { type: 'input', text: '', placeholder: 'Search or paste a link', color: '#e8eefc', bgColor: '#141a26', font: 'medium', x: 48, y: 128, width: 880, height: 64, variable: 'search_query' },
          { type: 'button', text: 'Search', color: '#f0f2f5', bgColor: '#0E7490', font: 'medium', x: 952, y: 128, width: 280, height: 64, trigger: 'youtube_search' },
          { type: 'button', text: 'Tube', color: '#f0f2f5', bgColor: '#9A3412', font: 'big', x: 48, y: 228, width: 292, height: 200, sceneChange: 'Tube', trigger: 'change_scene' },
          { type: 'button', text: 'Shorts', color: '#f0f2f5', bgColor: '#7C3AED', font: 'big', x: 356, y: 228, width: 292, height: 200 },
          { type: 'button', text: 'Music', color: '#f0f2f5', bgColor: '#0F766E', font: 'big', x: 664, y: 228, width: 292, height: 200 },
          { type: 'button', text: 'Back', color: '#f0f2f5', bgColor: '#334155', font: 'medium', x: 48, y: 464, width: 220, height: 72, sceneChange: 'Home', trigger: 'change_scene' }
        ]
      },
      {
        name: 'Tube',
        background: '#0f1115',
        elements: [
          { type: 'label', text: 'YouTube', color: '#e8eefc', bgColor: '', font: 'title', x: 48, y: 40, width: 480, height: 64 },
          { type: 'label', text: 'Search results render here on the device', color: '#8FA0B8', bgColor: '', font: 'small', x: 48, y: 128, width: 900, height: 30 },
          { type: 'button', text: 'lofi beats to install to — 3 h mix', color: '#f0f2f5', bgColor: '#1E3A8A', font: 'medium', x: 48, y: 180, width: 380, height: 200, trigger: 'play' },       { type: 'button', text: 'Speedrun — Retro Drive any% in 12:14', color: '#f0f2f5', bgColor: '#0F766E', font: 'medium', x: 444, y: 180, width: 380, height: 200, trigger: 'play' },
          { type: 'button', text: 'Streaming on a $60 handheld', color: '#f0f2f5', bgColor: '#7C3AED', font: 'medium', x: 840, y: 180, width: 380, height: 200, trigger: 'play' },
          { type: 'button', text: 'Back', color: '#f0f2f5', bgColor: '#334155', font: 'medium', x: 48, y: 464, width: 220, height: 72, sceneChange: 'Media', trigger: 'change_scene' }
        ]
      },
      {
        name: 'Settings',
        background: '#0f1115',
        elements: [
          { type: 'label', text: 'Settings', color: '#e8eefc', bgColor: '', font: 'title', x: 48, y: 40, width: 480, height: 64 },
          { type: 'button', text: 'Theme · Dark', color: '#f0f2f5', bgColor: '#475569', font: 'medium', x: 48, y: 140, width: 420, height: 84, trigger: 'cycle_theme' },
          { type: 'button', text: 'Resolution · 720', color: '#f0f2f5', bgColor: '#475569', font: 'medium', x: 48, y: 240, width: 420, height: 84, trigger: 'cycle_resolution' },
          { type: 'button', text: 'Update · v0.4.0 installed', color: '#e9fff1', bgColor: '#0e2418', font: 'medium', x: 48, y: 340, width: 420, height: 84, trigger: 'check_updates' },
          { type: 'button', text: 'Back', color: '#f0f2f5', bgColor: '#334155', font: 'medium', x: 48, y: 464, width: 220, height: 72, sceneChange: 'Home', trigger: 'change_scene' }
        ]
      }
    ]
  };

  // "Start from template": a small two-scene project to remix.
  const starterTemplate = {
    title: 'My Juka App',
    author: '',
    description: 'A ready-made two-scene project to remix.',
    variables: { fontSizes: { title: 48, big: 36, medium: 24, small: 18 } },
    scenes: [
      {
        name: 'Main',
        elements: [
          { type: 'label', text: 'Welcome to JukaHub', color: '#F8FAFC', font: 'title', x: 36, y: 44, width: 600, height: 64 },
          { type: 'button', text: 'Settings', color: '#F8FAFC', bgColor: '#1E3A8A', font: 'big', x: 36, y: 150, width: 220, height: 168, sceneChange: 'Settings' }
        ]
      },
      {
        name: 'Settings',
        elements: [
          { type: 'label', text: 'Settings', color: '#F8FAFC', font: 'title', x: 36, y: 44, width: 600, height: 64 },
          { type: 'button', text: 'Back', color: '#F8FAFC', bgColor: '#334155', font: 'big', x: 36, y: 150, width: 220, height: 168, sceneChange: 'Main' }
        ]
      }
    ]
  };

  function applyLoadedConfig(config) {
    if (autoSaveTimer) { clearTimeout(autoSaveTimer); autoSaveTimer = null; }
    importedConfigSnapshot = JSON.parse(JSON.stringify(config));
    loadJukaApp(config);
    scheduleAutoSave();
    refreshReloadButtonState();
  }

  document.querySelectorAll('.welcome-card[data-start]').forEach(card => {
    card.addEventListener('click', () => {
      const action = card.getAttribute('data-start');
      closeGuideFn();
      if (action === 'load') {
        const input = document.getElementById('loadFile');
        if (input) input.click();
      } else if (action === 'template') {
        applyLoadedConfig(JSON.parse(JSON.stringify(starterTemplate)));
        showToast('Starter template loaded', 'success');
      } else if (action === 'blank') {
        clearAll();
      } else if (action === 'demo') {
        applyLoadedConfig(JSON.parse(JSON.stringify(demoTemplate)));
        // The whole point: the visitor lands on a working, navigable shell.
        requestAnimationFrame(() => {
          enterPreviewMode();
        });
      }
    });
  });
})();

// ============================================================================
// Editor quality-of-life wiring
// ============================================================================
// Screen-reader announcements. Toasts are visible and already live, but mode
// changes and keyboard nudges need a short spoken confirmation that does not
// repeat the toast's whole sentence.
function announce(message) {
  const live = document.getElementById('a11yStatus');
  if (!live || !message) return;
  live.textContent = '';
  setTimeout(() => { live.textContent = message; }, 40);
}

// Arrow-key nudging is one undo step per burst of key presses, not one per
// pixel: holding an arrow key should not evict the rest of the history.
let nudgeBase = null;
let nudgeTimer = null;
function nudgeElement(el, dx, dy) {
  if (!el || (!dx && !dy)) return;
  if (!nudgeBase) nudgeBase = captureWorkspace('move');
  const x = Number(el.getAttribute('data-x')) || 0;
  const y = Number(el.getAttribute('data-y')) || 0;
  const width = Number(el.getAttribute('data-width')) || el.offsetWidth || 0;
  const height = Number(el.getAttribute('data-height')) || el.offsetHeight || 0;
  const nextX = Math.max(0, Math.min(x + dx, Math.max(0, canvasWidth - width)));
  const nextY = Math.max(0, Math.min(y + dy, Math.max(0, canvasHeight - height)));
  if (nextX === x && nextY === y) return;
  el.style.left = `${nextX}px`;
  el.style.top = `${nextY}px`;
  el.setAttribute('data-x', nextX);
  el.setAttribute('data-y', nextY);
  const xInput = document.getElementById('datax');
  const yInput = document.getElementById('datay');
  if (xInput && currentElement === el) xInput.value = nextX;
  if (yInput && currentElement === el) yInput.value = nextY;
  announce(`Moved to ${nextX}, ${nextY}.`);
  scheduleAutoSave();
  scheduleValidationRefresh();
  if (nudgeTimer) clearTimeout(nudgeTimer);
  nudgeTimer = setTimeout(() => {
    commitUndoSnapshot(nudgeBase);
    nudgeBase = null;
    nudgeTimer = null;
  }, 450);
}

function trapDialogFocus(panel) {
  if (!panel) return;
  panel.addEventListener('keydown', event => {
    if (event.key !== 'Tab') return;
    const focusable = Array.from(panel.querySelectorAll(
      'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
    )).filter(el => el.offsetParent !== null);
    if (focusable.length === 0) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  });

  // Restore focus to whatever opened the dialog once it closes.
  let opener = null;
  if (typeof MutationObserver === 'function') {
    new MutationObserver(() => {
      if (panel.hasAttribute('hidden')) {
        if (opener && document.contains(opener) && typeof opener.focus === 'function') opener.focus();
        opener = null;
      } else if (!opener) {
        opener = document.activeElement;
      }
    }).observe(panel, { attributes: true, attributeFilter: ['hidden'] });
  }
}

function controlLabelFor(input) {
  if (!input) return 'edit';
  const group = input.closest('.control-group, .size-group, .field, .form-group');
  const label = group && group.querySelector('label');
  const text = (label && label.textContent) || input.getAttribute('aria-label') || input.id || 'edit';
  return text.trim().replace(/\s+/g, ' ').slice(0, 32) || 'edit';
}

function setupEditorQuality() {
  document.getElementById('undoBtn')?.addEventListener('click', undo);
  document.getElementById('redoBtn')?.addEventListener('click', redo);
  document.getElementById('validateBtn')?.addEventListener('click', () => openValidationPanel(lastImportReport));

  document.querySelectorAll('[data-close-validate]').forEach(el => {
    el.addEventListener('click', closeValidationPanel);
  });
  document.getElementById('validateRecheck')?.addEventListener('click', () => {
    renderValidationPanel(validateProject(buildEditorConfig()), lastImportReport);
    announce('Design re-checked.');
  });
  document.getElementById('validateExportAnyway')?.addEventListener('click', () => {
    const kind = pendingExportKind || 'json';
    pendingExportKind = null;
    closeValidationPanel();
    if (kind === 'xml') exportConfigXml(true);
    else exportConfig(true);
  });

  // Inspector edits are undoable even though the inputs mutate the DOM directly:
  // capture on focus, commit on change only if something actually changed.
  let inspectorBase = null;
  const inspectorPanels = [
    document.getElementById('elementProperties'),
    document.getElementById('appInfoPanel')
  ].filter(Boolean);
  inspectorPanels.forEach(panel => {
    panel.addEventListener('focusin', event => {
      const input = event.target.closest('input, select, textarea');
      if (!input) return;
      inspectorBase = captureWorkspace('edit ' + controlLabelFor(input));
    });
    panel.addEventListener('change', () => {
      if (!inspectorBase) return;
      commitUndoSnapshot(inspectorBase);
      inspectorBase = captureWorkspace('edit');
      scheduleValidationRefresh();
    });
  });

  // Library tiles are keyboard buttons: Enter/Space drops the component in the
  // middle of the canvas, so the builder is usable without a drag gesture.
  document.querySelectorAll('.left-sidebar .element[data-type]').forEach(tile => {
    tile.addEventListener('keydown', event => {
      if (event.key !== 'Enter' && event.key !== ' ') return;
      event.preventDefault();
      const type = tile.getAttribute('data-type');
      const el = addElement(type, Math.round(canvasWidth / 2 - 60), Math.round(canvasHeight / 2 - 20));
      el.click();
      announce(`Added ${type} to ${currentScene}.`);
    });
  });

  document.querySelectorAll('.guide-modal').forEach(trapDialogFocus);

  // Mobile tab strip: aria-selected was never updated, so assistive tech always
  // announced the first tab as current.
  const mobileTabs = Array.from(document.querySelectorAll('.mobile-tab'));
  mobileTabs.forEach((tab, index) => {
    tab.setAttribute('aria-selected', String(index === 0));
    tab.addEventListener('click', () => {
      mobileTabs.forEach(other => other.setAttribute('aria-selected', String(other === tab)));
    });
    tab.addEventListener('keydown', event => {
      if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return;
      event.preventDefault();
      const next = mobileTabs[(index + (event.key === 'ArrowRight' ? 1 : mobileTabs.length - 1)) % mobileTabs.length];
      next.focus();
      next.click();
    });
  });

  // Keep the validation badge honest after scene/variable changes that do not
  // go through an inspector field.
  ['sceneSelector', 'canvasSize', 'customWidth', 'customHeight'].forEach(id => {
    document.getElementById(id)?.addEventListener('change', scheduleValidationRefresh);
  });
}

// Static toolbar buttons used to carry inline onclick/onchange attributes. They
// are wired here instead so the page can adopt a strict Content-Security-Policy
// later. Only these named builder actions can be triggered from the markup.
(function bindDeclarativeActions() {
  const actions = {
    exportConfig: () => exportConfig(),
    exportConfigXml: () => exportConfigXml(),
    copyConfigJson: () => copyConfigJson(),
    addScene: () => addScene(),
    renameScene: () => renameScene(),
    deleteScene: () => deleteScene(),
    duplicateScene: () => duplicateScene(),
    changeScene: () => changeScene(),
  };
  const bind = () => {
    document.querySelectorAll('[data-click]').forEach(el => {
      const fn = actions[el.dataset.click];
      if (fn) el.addEventListener('click', fn);
    });
    document.querySelectorAll('[data-change]').forEach(el => {
      const fn = actions[el.dataset.change];
      if (fn) el.addEventListener('change', fn);
    });
  };
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', bind);
  else bind();
})();
