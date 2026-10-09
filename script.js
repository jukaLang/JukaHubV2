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
const videoProperties = document.getElementById('videoProperties');

// Global State
let backgroundPath = '';
let scenes = { 'Scene 1': [] };
let currentScene = 'Scene 1';
let variables = {};
let currentElement = null;
let canvasWidth = 1280;
let canvasHeight = 720;
let videoList = [];
let globalTooltip = null;

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

  // Try auto-save first, then fall back to default config
  if (!loadAutoSave()) {
    loadDefaultConfig();
  }

  // New features
  setupExportDropdowns();
  setupContextMenu();
  setupKeyboardShortcuts();
  setupAiPanelToggle();
  setupFooterHeightTracking();

  // Preview toggle button
  const previewBtn = document.getElementById('previewToggle');
  if (previewBtn) previewBtn.addEventListener('click', togglePreviewMode);

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

function pushUndo(label) {
  saveCurrentScene();
  const snapshot = {
    label,
    scenes: JSON.parse(JSON.stringify(
      Object.fromEntries(Object.entries(scenes).map(([k, v]) => [k, v.map(el => el.outerHTML)]))
    )),
    currentScene,
    variables: JSON.parse(JSON.stringify(variables)),
    title: document.getElementById('title').value,
    author: document.getElementById('author').value,
    description: document.getElementById('description').value
  };
  undoStack.push(snapshot);
  if (undoStack.length > MAX_UNDO) undoStack.shift();
  redoStack.length = 0;
}

function restoreSnapshot(snapshot) {
  // Restore scenes from HTML strings
  scenes = {};
  const sceneSelectorEl = document.getElementById('sceneSelector');
  sceneSelectorEl.innerHTML = '';

  for (const [name, htmlArr] of Object.entries(snapshot.scenes)) {
    scenes[name] = htmlArr.map(html => {
      const tmp = document.createElement('div');
      tmp.innerHTML = html;
      return tmp.firstChild;
    });
    const option = document.createElement('option');
    option.value = name;
    option.textContent = name;
    sceneSelectorEl.appendChild(option);
  }

  currentScene = snapshot.currentScene;
  sceneSelectorEl.value = currentScene;
  variables = JSON.parse(JSON.stringify(snapshot.variables));
  document.getElementById('title').value = snapshot.title;
  document.getElementById('author').value = snapshot.author;
  document.getElementById('description').value = snapshot.description;

  // Rebuild variables UI
  variablesList.innerHTML = '';
  for (const [key, val] of Object.entries(variables)) {
    const variableItem = document.createElement('div');
    variableItem.className = 'variable-item';
    variableItem.innerHTML = `
      <div>
        <span class="variable-name">${key}</span>
        <span class="variable-value">${val}</span>
      </div>
      <div class="variable-actions">
        <button onclick="editVariable('${key}')"><i class="fas fa-edit"></i></button>
        <button onclick="deleteVariable('${key}')"><i class="fas fa-trash"></i></button>
      </div>
    `;
    variablesList.appendChild(variableItem);
  }

  loadScene(currentScene);
  updateSceneBadge();
  updateSceneChangeSelector();
  updateVariableChangeSelector();
  updateAllMenuSceneButtons();
  updateAllStoredMenus();
}

function undo() {
  if (undoStack.length === 0) return;
  saveCurrentScene();
  const current = {
    label: 'redo',
    scenes: JSON.parse(JSON.stringify(
      Object.fromEntries(Object.entries(scenes).map(([k, v]) => [k, v.map(el => el.outerHTML)]))
    )),
    currentScene,
    variables: JSON.parse(JSON.stringify(variables)),
    title: document.getElementById('title').value,
    author: document.getElementById('author').value,
    description: document.getElementById('description').value
  };
  redoStack.push(current);
  const snapshot = undoStack.pop();
  restoreSnapshot(snapshot);
  showToast('Undone: ' + (snapshot.label || 'action'), 'info');
}

function redo() {
  if (redoStack.length === 0) return;
  saveCurrentScene();
  const current = {
    label: 'undo',
    scenes: JSON.parse(JSON.stringify(
      Object.fromEntries(Object.entries(scenes).map(([k, v]) => [k, v.map(el => el.outerHTML)]))
    )),
    currentScene,
    variables: JSON.parse(JSON.stringify(variables)),
    title: document.getElementById('title').value,
    author: document.getElementById('author').value,
    description: document.getElementById('description').value
  };
  undoStack.push(current);
  const snapshot = redoStack.pop();
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

function scheduleAutoSave() {
  if (autoSaveTimer) clearTimeout(autoSaveTimer);
  autoSaveTimer = setTimeout(() => {
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
      smallSize: smallSizeInput?.value || 18
    };
    try {
      localStorage.setItem('jukahub-autosave', JSON.stringify(data));
    } catch (e) { /* quota exceeded */ }
  }, 1500);
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
      scenes[name] = htmlArr.map(html => {
        const tmp = document.createElement('div');
        tmp.innerHTML = html;
        return tmp.firstChild;
      });
      const option = document.createElement('option');
      option.value = name;
      option.textContent = name;
      sceneSelectorEl.appendChild(option);
    }

    currentScene = data.currentScene || Object.keys(scenes)[0];
    sceneSelectorEl.value = currentScene;
    variables = data.variables || {};
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

    updateCanvasSize();
    loadScene(currentScene);

    // Rebuild variables UI
    variablesList.innerHTML = '';
    for (const [key, val] of Object.entries(variables)) {
      const variableItem = document.createElement('div');
      variableItem.className = 'variable-item';
      variableItem.innerHTML = `
        <div>
          <span class="variable-name">${key}</span>
          <span class="variable-value">${val}</span>
        </div>
        <div class="variable-actions">
          <button onclick="editVariable('${key}')"><i class="fas fa-edit"></i></button>
          <button onclick="deleteVariable('${key}')"><i class="fas fa-trash"></i></button>
        </div>
      `;
      variablesList.appendChild(variableItem);
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

let previewMode = false;

function togglePreviewMode() {
  previewMode = !previewMode;
  document.body.classList.toggle('preview-mode', previewMode);
  const btn = document.getElementById('previewToggle');
  if (btn) {
    btn.classList.toggle('active', previewMode);
    btn.innerHTML = previewMode
      ? '<i class="fas fa-pen"></i>'
      : '<i class="fas fa-eye"></i>';
  }
  // Deselect any element
  if (previewMode) {
    currentElement = null;
    document.querySelectorAll('.element').forEach(el => el.classList.remove('selected'));
    document.body.classList.remove('element-selected');
  }
  showToast(previewMode ? 'Preview mode on — click eye to exit' : 'Edit mode', 'info');
}

// ---

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

    // Ctrl/Cmd + Z = Undo
    if ((e.ctrlKey || e.metaKey) && !e.shiftKey && e.key === 'z') {
      e.preventDefault();
      undo();
      return;
    }

    // Ctrl/Cmd + Shift + Z = Redo
    if ((e.ctrlKey || e.metaKey) && e.shiftKey && e.key === 'z') {
      e.preventDefault();
      redo();
      return;
    }

    // Ctrl/Cmd + Y = Redo
    if ((e.ctrlKey || e.metaKey) && e.key === 'y') {
      e.preventDefault();
      redo();
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

    // Ctrl/Cmd + P = Toggle preview
    if ((e.ctrlKey || e.metaKey) && e.key === 'p') {
      e.preventDefault();
      togglePreviewMode();
      return;
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
        currentElement = null;
        showToast('Element deleted', 'info');
      }
      return;
    }

    // Escape = Deselect or exit preview
    if (e.key === 'Escape') {
      if (previewMode) {
        togglePreviewMode();
        return;
      }
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
      darkModeToggle.innerHTML = isLight ?
        '<i class="fas fa-sun" aria-hidden="true"></i> <span>Light Mode</span>' :
        '<i class="fas fa-moon" aria-hidden="true"></i> <span>Dark Mode</span>';
      localStorage.setItem('jukahub-theme', isLight ? 'light' : 'dark');
    });
  }

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
    const x = e.clientX - rect.left;
    const y = e.clientY - rect.top;
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

    const reader = new FileReader();
    reader.onload = e => {
      try {
        const text = e.target.result;
        let config;
        if (file.name.endsWith('.xml') || text.trim().startsWith('<?xml') || text.trim().startsWith('<')) {
          config = xmlToJson(text);
        } else {
          config = JSON.parse(text);
        }
        loadJukaApp(config);
        scheduleAutoSave();
        showToast('Configuration loaded', 'success');
      } catch (error) {
        showToast('Error loading config: ' + error.message, 'error');
      }
    };
    reader.readAsText(file);
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

// Theme helpers
function loadTheme() {
  const saved = localStorage.getItem('jukahub-theme');
  const prefersLight = saved === 'light';
  document.body.classList.toggle('theme-light', prefersLight);
  document.body.classList.toggle('theme-dark', !prefersLight);
  if (darkModeToggle) {
    darkModeToggle.innerHTML = prefersLight ?
      '<i class="fas fa-sun" aria-hidden="true"></i> <span>Light Mode</span>' :
      '<i class="fas fa-moon" aria-hidden="true"></i> <span>Dark Mode</span>';
  }
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
  if (readout) readout.textContent = `${canvasWidth} x ${canvasHeight}`;
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
function addScene() {
  saveCurrentScene();
  const newSceneName = `Scene ${Object.keys(scenes).length + 1}`;
  scenes[newSceneName] = [];

  const option = document.createElement('option');
  option.value = newSceneName;
  option.textContent = newSceneName;
  sceneSelector.appendChild(option);
  sceneSelector.value = newSceneName;

  currentScene = newSceneName;
  loadScene(currentScene);
  updateSceneBadge();

  // Add menu to new scene
  addElement('menu', 0, canvasHeight - 50);

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
  if (!newSceneName || scenes[newSceneName]) return;

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

// Element Management
function addElement(type, x, y) {
  if (type === 'menu-element') {
    type = 'menu'; // Convert to the actual type used on canvas
  }
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
  return el;
}

function setupElementEvents(el) {
  let isDragging = false;
  let startX, startY;
  let startTouchX, startTouchY;

  // Touch events for mobile
  el.addEventListener('touchstart', (event) => {
    if (event.touches.length === 1) {
      event.preventDefault();
      const touch = event.touches[0];
      startTouchX = touch.clientX - el.offsetLeft;
      startTouchY = touch.clientY - el.offsetTop;
      isDragging = true;
      el.style.cursor = 'grabbing';

    }
  }, { passive: false });


  document.addEventListener('touchmove', (event) => {
    if (!isDragging) return;
    event.preventDefault();

    const touch = event.touches[0];
    const canvasRect = canvas.getBoundingClientRect();
    let newX = touch.clientX - startTouchX;
    let newY = touch.clientY - startTouchY;
    const elRect = el.getBoundingClientRect();

    newX = Math.max(0, Math.min(newX, canvasRect.width - elRect.width));
    newY = Math.max(0, Math.min(newY, canvasRect.height - elRect.height));

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
    }
  }, { passive: false });


  // Mouse events for dragging and resizing
  el.addEventListener('mousedown', (event) => {
    if (event.button === 2) { // Right click for resize
      handleResize(el, event);
    } else { // Left click for drag
      // Prevent dragging if clicking on input or image
      if (event.target.tagName === 'INPUT' || event.target.tagName === 'IMG') {
        return;
      }

      isDragging = true;
      startX = event.clientX - el.offsetLeft;
      startY = event.clientY - el.offsetTop;
      el.style.cursor = 'grabbing';

      // Prevent text selection during drag
      event.preventDefault();
    }
  });

  // Mouse move for dragging
  document.addEventListener('mousemove', (event) => {
    if (!isDragging) return;

    const canvasRect = canvas.getBoundingClientRect();
    let newX = event.clientX - startX;
    let newY = event.clientY - startY;
    const elRect = el.getBoundingClientRect();

    newX = Math.max(0, Math.min(newX, canvasRect.width - elRect.width));
    newY = Math.max(0, Math.min(newY, canvasRect.height - elRect.height));

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
      scheduleAutoSave();
    }
  });

  // Context menu prevention
  el.addEventListener('contextmenu', (event) => event.preventDefault());

  // Double click for editing
  el.addEventListener('dblclick', (event) => {
    event.stopPropagation();
    const type = el.getAttribute('data-type');
    if (['button', 'label', 'video'].includes(type)) {
      const textSpan = el.querySelector('.text-content');
      const newText = prompt("Edit text:", textSpan.textContent);
      if (newText !== null) {
        textSpan.textContent = newText;
        processTextForVariables(textSpan);
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

  const onMouseMove = (e) => {
    const newWidth = Math.max(50, startWidth + (e.clientX - startX));
    const newHeight = Math.max(50, startHeight + (e.clientY - startY));
    el.style.width = `${newWidth}px`;
    el.style.height = `${newHeight}px`;
    el.setAttribute('data-width', newWidth);
    el.setAttribute('data-height', newHeight);
  };

  const onMouseUp = () => {
    el.style.cursor = 'grab';
    document.removeEventListener('mousemove', onMouseMove);
    document.removeEventListener('mouseup', onMouseUp);
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
    }

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
  const variableName = prompt('Enter variable name:');
  if (variableName && !variables[variableName]) {
    variables[variableName] = '';

    // Create variable item
    const variableItem = document.createElement('div');
    variableItem.className = 'variable-item';
    variableItem.innerHTML = `
                    <div>
                        <span class="variable-name">${variableName}</span>
                        <span class="variable-value">${variables[variableName]}</span>
                    </div>
                    <div class="variable-actions">
                        <button onclick="editVariable('${variableName}')"><i class="fas fa-edit"></i></button>
                        <button onclick="deleteVariable('${variableName}')"><i class="fas fa-trash"></i></button>
                    </div>
                `;

    variablesList.appendChild(variableItem);

    document.querySelectorAll('.dynamic-variable-selector').forEach(selector => {
      const currentValue = selector.value;
      updateVariableSelector(selector, currentValue);
    });

    updateVariableChangeSelector();
  }
}

function editVariable(name) {
  const newValue = prompt(`Enter new value for ${name}:`, variables[name]);
  if (newValue !== null) {
    variables[name] = newValue;

    // Update UI
    document.querySelectorAll('.variable-item').forEach(item => {
      if (item.querySelector('.variable-name').textContent === name) {
        item.querySelector('.variable-value').textContent = newValue;
      }
    });

    // Update all elements with variables
    document.querySelectorAll('.text-content').forEach(textEl => {
      processTextForVariables(textEl);
    });
  }
}

function deleteVariable(name) {
  if (confirm(`Delete variable ${name}?`)) {
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

const RAW_ELEMENT_ATTR = 'data-raw';
let importedConfig = { top: {}, variables: {}, scenes: {} };

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
    out[key] = imported[key];
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
  if (element.trigger === 'change_scene') {
    assign('sceneChange', attr('data-scene-change'));
  } else if (element.trigger === 'external_app') {
    assign('externalAppPath', attr('data-external-app-path'));
    assign('externalAppReturn', attr('data-external-app-return'));
  } else if (element.trigger === 'set_variable') {
    assign('variableChange', attr('data-variable-change'));
    assign('variableChangeValue', attr('data-variable-change-value'));
  } else if (element.trigger === 'play_video' || element.trigger === 'play_image') {
    assign('mediaVariable', attr('data-media-variable'));
  }

  assign('triggerValue', attr('data-trigger-value'));
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
function applyElementConfigAttributes(el, data) {
  const set = (name, value) => {
    if (value === null || value === undefined) return;
    if (value === '') { el.removeAttribute(name); return; }
    el.setAttribute(name, String(value));
  };

  set('data-trigger', data.trigger);
  set('data-trigger-value', data.triggerValue);
  set('data-scene-change', data.sceneChange);
  set('data-external-app-path', data.externalAppPath);
  set('data-external-app-return', data.externalAppReturn);
  set('data-variable-change', data.variableChange);
  set('data-variable-change-value', data.variableChangeValue);
  set('data-media-variable', data.mediaVariable);
  set('data-video-variable', data.videoVariable);
  set('data-video', data.video);
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
// never drift apart again.
function buildEditorConfig() {
  // Variables the panel owns are merged last so its current state wins; the
  // rest comes back from the import unchanged.
  const exportedVariables = Object.assign({}, preservedVariables(), variables);
  if (backgroundPath) exportedVariables.backgroundImage = backgroundPath;
  const fontSizes = exportFontSizes();
  if (Object.keys(fontSizes).length > 0) exportedVariables.fontSizes = fontSizes;

  return Object.assign({}, importedConfig.top, {
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

// Legacy alias for inline Export buttons
function exportConfig() {
  createJukaApp();
}

// ---

function exportConfigXml() {
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

// <variables> entries are all text, so a value's type has to be recovered. This
// mirrors the player's own XML loader: booleans and numbers come back typed so
// an XML import never turns a number into a string that a later JSON export
// would hand to the player in the wrong type. Anything else (including leading
// zeros and empty text) stays exactly as written.
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
        // means the player sizes the element itself.
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
    undoStack.length = 0;
    redoStack.length = 0;
    try { localStorage.removeItem('jukahub-autosave'); } catch (e) {}
    showToast('Project cleared', 'success');
  }
}

function saveCurrentScene() {
  scenes[currentScene] = Array.from(canvas.children).map(el => el.cloneNode(true));
}

// Scene management functions
function renameScene() {
  const newName = prompt('Enter new name for scene:', currentScene);
  if (!newName || scenes[newName]) return;

  // Update scenes object
  scenes[newName] = scenes[currentScene];
  delete scenes[currentScene];

  // Update scene selector
  const option = sceneSelector.querySelector(`option[value="${currentScene}"]`);
  option.value = newName;
  option.textContent = newName;

  currentScene = newName;
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
    // Find next scene to show
    const sceneNames = Object.keys(scenes);
    const currentIndex = sceneNames.indexOf(currentScene);
    const nextScene = currentIndex > 0 ? sceneNames[currentIndex - 1] : sceneNames[1];

    // Delete scene
    delete scenes[currentScene];

    // Remove from selector
    const option = sceneSelector.querySelector(`option[value="${currentScene}"]`);
    option.remove();

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
    showToast(`Scene "${currentScene}" deleted`, 'success');
  }
}

// Load initial config
function loadInitialConfig() {
  // This would typically fetch from a server
  console.log('Loading initial configuration...');
}



function loadDefaultConfig() {
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
    });
}


function loadJukaApp(data) {
  // Remember everything the builder does not model, so the export keeps it.
  resetImportedConfig(data);

  // Clear existing elements
  variableChangeSelector.innerHTML = '';
  canvas.innerHTML = '';

  // Load app info
  document.getElementById('title').value = data.title || '';
  document.getElementById('author').value = data.author || '';
  document.getElementById('description').value = data.description || '';

  // Load font sizes
  if (data.variables && data.variables.fontSizes) {
    document.getElementById('titleSize').value = data.variables.fontSizes.title || 48;
    document.getElementById('bigSize').value = data.variables.fontSizes.big || 36;
    document.getElementById('mediumSize').value = data.variables.fontSizes.medium || 24;
    document.getElementById('smallSize').value = data.variables.fontSizes.small || 18;
  }

  // Load background
  if (data.variables && data.variables.backgroundImage) {
    canvas.style.backgroundImage = `url(${data.variables.backgroundImage})`;
    canvas.style.backgroundSize = 'cover';
    backgroundPath = data.variables.backgroundImage;
  }

  // Clear existing scenes and variables
  scenes = {};
  variables = {};
  variablesList.innerHTML = '';

  // Load variables
  if (data.variables) {
    const excludedKeys = ['backgroundImage', 'fontSizes', 'buttonColor', 'labelColor', 'fonts'];
    for (const key in data.variables) {
      if (!excludedKeys.includes(key)) {
        variables[key] = data.variables[key];

        // Add variable to UI
        const variableItem = document.createElement('div');
        variableItem.className = 'variable-item';
        variableItem.innerHTML = `
                    <div>
                        <span class="variable-name">${key}</span>
                        <span class="variable-value">${data.variables[key]}</span>
                    </div>
                    <div class="variable-actions">
                        <button onclick="editVariable('${key}')"><i class="fas fa-edit"></i></button>
                        <button onclick="deleteVariable('${key}')"><i class="fas fa-trash"></i></button>
                    </div>
                `;
        variablesList.appendChild(variableItem);
      }
    }
  }

  // Load scenes
  const sceneSelector = document.getElementById('sceneSelector');
  sceneSelector.innerHTML = '';

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
  if (elementData.type === 'button') {
    el.style.color = elementData.color || '#000000';
    el.style.backgroundColor = elementData.bgColor || '#ffffff';
    el.style.fontSize = getFontSize(elementData.font || 'medium') + 'px';
    declared('data-color', elementData.color);
    declared('data-bg-color', elementData.bgColor);
    declared('data-font', elementData.font);
  } else if (elementData.type === 'label') {
    el.style.color = elementData.color || '#000000';
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
      { type: 'collapsedlist', icon: 'fas fa-bars', name: 'Collapsed List' },
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
      button.innerHTML = `<i class="${element.icon}"></i> ${element.name}`;

      button.addEventListener('click', () => {
        elementType = element.type;
        mobileMenu.style.display = 'none';
        // Add element to center of canvas
        const rect = canvas.getBoundingClientRect();
        const x = rect.width / 2 - 60;
        const y = rect.height / 2 - 20;
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

function setupDynamicListProperties(el) {
  // Remove any existing dynamic list properties
  document.querySelectorAll('.dynamic-list-properties').forEach(item => item.remove());

  // Create container for dynamic list properties
  const container = document.createElement('div');
  container.className = 'dynamic-list-properties';

  // Command Path input
  const commandGroup = document.createElement('div');
  commandGroup.className = 'control-group';
  commandGroup.innerHTML = `
    <label for="dynamicCommand"><i class="fas fa-terminal"></i> Command Path:</label>
    <input type="text" id="dynamicCommand" class="dynamic-command-input" 
           placeholder="Path to executable" value="${el.getAttribute('data-command') || ''}">
  `;
  container.appendChild(commandGroup);

  // Variable input
  const variableGroup = document.createElement('div');
  variableGroup.className = 'control-group';
  variableGroup.innerHTML = `
    <label for="dynamicVariable"><i class="fas fa-code"></i> Variable:</label>
    <input type="text" id="dynamicVariable" class="dynamic-variable-input" 
           placeholder="Variable to store selection" value="${el.getAttribute('data-variable') || ''}">
  `;
  container.appendChild(variableGroup);

  // Add event listeners
  const commandInput = container.querySelector('#dynamicCommand');
  const variableInput = container.querySelector('#dynamicVariable');

  commandInput.onchange = () => {
    el.setAttribute('data-command', commandInput.value);
  };

  variableInput.onchange = () => {
    el.setAttribute('data-variable', variableInput.value);
  };

  // Add to properties panel - insert after the style controls
  const styleControls = document.querySelector('.style-controls');
  if (styleControls) {
    styleControls.parentNode.insertBefore(container, styleControls.nextSibling);
  } else {
    elementProperties.appendChild(container);
  }
}

function executeDynamicListCommand(command, variable) {
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
    if (e.target !== el && !e.target.classList.contains('remove-button')) return;

    const command = el.getAttribute('data-command');
    const variable = el.getAttribute('data-variable');

    if (command && variable) {
      // Execute command and store result
      executeDynamicListCommand(command, variable);
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

    const arrayMatches = [...text.matchAll(/\[(?:[^\[\]]|(?:\[[^\[\]]*\]))*\]/g)].map((m) => m[0]);
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
        'Content-Type': 'application/json',
      },
      body: {
        model: 'claude-3-5-haiku-20241022',
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
      suggestions.push('Added a centered title \"' + titleText + '\" near the top.');
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
  'Be conservative: if the request is ambiguous, add a small sensible set rather than a huge screen. Prefer a title near the top, tiles in a grid, and a small control hint near the bottom.',
].join('\n');

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

// Initialize AI panel once the DOM is ready.
document.addEventListener('DOMContentLoaded', () => { if (typeof AI !== 'undefined' && typeof AI.init === 'function') { try { AI.init(); } catch (e) { console.warn('AI init failed:', e); } } });
