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

// ─── Undo / Redo ──────────────────────────────────────────────────────────────

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

// ─── Export Dropdown ───────────────────────────────────────────────────────────

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

// ─── Context Menu ─────────────────────────────────────────────────────────────

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

// ─── Auto-Save ────────────────────────────────────────────────────────────────

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

// ─── Preview Mode ─────────────────────────────────────────────────────────────

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

// ─── Keyboard Shortcuts ──────────────────────────────────────────────────────

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

  // Desktop sidebar toggles
  const leftToggle = document.getElementById('leftSidebarToggle');
  const rightToggle = document.getElementById('rightSidebarToggle');
  if (leftToggle) {
    leftToggle.addEventListener('click', () => {
      const sb = document.getElementById('leftSidebar');
      sb.classList.toggle('open');
      const expanded = sb.classList.contains('open') || window.innerWidth > 992;
      leftToggle.setAttribute('aria-expanded', String(expanded));
    });
  }
  if (rightToggle) {
    rightToggle.addEventListener('click', () => {
      const sb = document.getElementById('rightSidebar');
      sb.classList.toggle('open');
      const expanded = sb.classList.contains('open') || window.innerWidth > 992;
      rightToggle.setAttribute('aria-expanded', String(expanded));
    });
  }

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

// Export functionality
function createJukaApp() {
  const config = {
    title: document.getElementById('title').value,
    author: document.getElementById('author').value,
    description: document.getElementById('description').value,
    variables: {
      ...variables,
      backgroundImage: backgroundPath,
      fontSizes: {
        title: parseInt(titleSizeInput.value, 10),
        big: parseInt(bigSizeInput.value, 10),
        medium: parseInt(mediumSizeInput.value, 10),
        small: parseInt(smallSizeInput.value, 10)
      }
    },
    scenes: Object.keys(scenes).map(sceneName => ({
      name: sceneName,
      elements: scenes[sceneName].map(el => {
        const element = {
          type: el.getAttribute('data-type'),
          x: parseInt(el.getAttribute('data-x')),
          y: parseInt(el.getAttribute('data-y')),
          width: parseInt(el.getAttribute('data-width')),
          height: parseInt(el.getAttribute('data-height'))
        };

        if (el.getAttribute('data-color')) {
          element.color = el.getAttribute('data-color');
        }

        if (el.getAttribute('data-bg-color')) {
          element.bgColor = el.getAttribute('data-bg-color');
        }

        if (el.getAttribute('data-font')) {
          element.font = el.getAttribute('data-font');
        }

        if (el.getAttribute('data-opacity')) {
          element.opacity = parseInt(el.getAttribute('data-opacity')) / 100;
        }

        // Add trigger data
        if (el.getAttribute('data-trigger')) {
          element.trigger = el.getAttribute('data-trigger');

          if (element.trigger === 'change_scene') {
            element.sceneChange = el.getAttribute('data-scene-change');
          } else if (element.trigger === 'external_app') {
            element.externalAppPath = el.getAttribute('data-external-app-path');
            element.externalAppReturn = el.getAttribute('data-external-app-return');
          } else if (element.trigger === 'set_variable') {
            element.variableChange = el.getAttribute('data-variable-change');
            element.variableChangeValue = el.getAttribute('data-variable-change-value');
          } else if (element.trigger === 'play_video' || element.trigger === 'play_image') {
            element.mediaVariable = el.getAttribute('data-media-variable') || '';
          }
        }

        const type = el.getAttribute('data-type');
        if (type === 'input') {
          const input = el.querySelector('.element-input');
          if (input) element.text = input.value;
        } else {
          const textSpan = el.querySelector('.text-content');
          if (textSpan) element.text = textSpan.textContent;
        }

        if (type === 'dynamiclist') {
          element.command = el.getAttribute('data-command') || '';
          element.variable = el.getAttribute('data-variable') || '';
        }

        if (type === 'textbrowser') {
          element.variable = el.getAttribute('data-variable') || '';
          element.source = el.getAttribute('data-source') || 'system';
          element.jsonPath = el.getAttribute('data-json-path') || '';
          element.autoRefresh = el.getAttribute('data-auto-refresh') === 'true';
        }


        if (type === 'image') {
          const img = el.querySelector('.element-image');
          if (img && img.src) element.image = img.src;
        }

        if (type === 'video') {
          element.videoVariable = el.getAttribute('data-video-variable');
        }

        return element;
      })
    }))
  };

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

// ─── XML Export ───────────────────────────────────────────────────────────────

function exportConfigXml() {
  const config = {
    title: document.getElementById('title').value,
    author: document.getElementById('author').value,
    description: document.getElementById('description').value,
    variables: {
      ...variables,
      backgroundImage: backgroundPath,
      fontSizes: {
        title: parseInt(titleSizeInput.value, 10),
        big: parseInt(bigSizeInput.value, 10),
        medium: parseInt(mediumSizeInput.value, 10),
        small: parseInt(smallSizeInput.value, 10)
      }
    },
    scenes: Object.keys(scenes).map(sceneName => ({
      name: sceneName,
      elements: scenes[sceneName].map(el => {
        const element = {
          type: el.getAttribute('data-type'),
          x: parseInt(el.getAttribute('data-x')),
          y: parseInt(el.getAttribute('data-y')),
          width: parseInt(el.getAttribute('data-width')),
          height: parseInt(el.getAttribute('data-height'))
        };

        if (el.getAttribute('data-color')) element.color = el.getAttribute('data-color');
        if (el.getAttribute('data-bg-color')) element.bgColor = el.getAttribute('data-bg-color');
        if (el.getAttribute('data-font')) element.font = el.getAttribute('data-font');
        if (el.getAttribute('data-opacity')) element.opacity = parseInt(el.getAttribute('data-opacity')) / 100;

        if (el.getAttribute('data-trigger')) {
          element.trigger = el.getAttribute('data-trigger');
          if (element.trigger === 'change_scene') element.sceneChange = el.getAttribute('data-scene-change');
          else if (element.trigger === 'external_app') {
            element.externalAppPath = el.getAttribute('data-external-app-path');
            element.externalAppReturn = el.getAttribute('data-external-app-return');
          } else if (element.trigger === 'set_variable') {
            element.variableChange = el.getAttribute('data-variable-change');
            element.variableChangeValue = el.getAttribute('data-variable-change-value');
          } else if (element.trigger === 'play_video' || element.trigger === 'play_image') {
            element.mediaVariable = el.getAttribute('data-media-variable') || '';
          }
        }

        const type = el.getAttribute('data-type');
        if (type === 'input') {
          const input = el.querySelector('.element-input');
          if (input) element.text = input.value;
        } else {
          const textSpan = el.querySelector('.text-content');
          if (textSpan) element.text = textSpan.textContent;
        }

        if (type === 'dynamiclist') {
          element.command = el.getAttribute('data-command') || '';
          element.variable = el.getAttribute('data-variable') || '';
        }
        if (type === 'textbrowser') {
          element.variable = el.getAttribute('data-variable') || '';
          element.source = el.getAttribute('data-source') || 'system';
          element.jsonPath = el.getAttribute('data-json-path') || '';
          element.autoRefresh = el.getAttribute('data-auto-refresh') === 'true';
        }
        if (type === 'image') {
          const img = el.querySelector('.element-image');
          if (img && img.src) element.image = img.src;
        }
        if (type === 'video') element.videoVariable = el.getAttribute('data-video-variable');

        return element;
      })
    }))
  };

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

// ─── XML ↔ JSON helpers ──────────────────────────────────────────────────────

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

function configToXml(config) {
  let xml = '<?xml version="1.0" encoding="UTF-8"?>\n<jukaconfig>\n';

  // Top-level metadata
  xml += '  <title>' + escapeXml(config.title) + '</title>\n';
  xml += '  <author>' + escapeXml(config.author) + '</author>\n';
  xml += '  <description>' + escapeXml(config.description) + '</description>\n';

  // Variables
  if (config.variables) {
    xml += '  <variables>\n';
    for (const [key, val] of Object.entries(config.variables)) {
      if (key === 'fontSizes' && typeof val === 'object') {
        xml += '    <fontSizes>\n';
        for (const [sz, num] of Object.entries(val)) {
          xml += '      <' + xmlEscapeKey(sz) + '>' + num + '</' + xmlEscapeKey(sz) + '>\n';
        }
        xml += '    </fontSizes>\n';
      } else if (typeof val === 'object' && val !== null) {
        // Generic nested object – serialise children
        xml += '    <' + xmlEscapeKey(key) + '>\n';
        for (const [k2, v2] of Object.entries(val)) {
          xml += '      <' + xmlEscapeKey(k2) + '>' + escapeXml(v2) + '</' + xmlEscapeKey(k2) + '>\n';
        }
        xml += '    </' + xmlEscapeKey(key) + '>\n';
      } else {
        xml += '    <' + xmlEscapeKey(key) + '>' + escapeXml(val) + '</' + xmlEscapeKey(key) + '>\n';
      }
    }
    xml += '  </variables>\n';
  }

  // Scenes
  if (config.scenes) {
    xml += '  <scenes>\n';
    for (const scene of config.scenes) {
      xml += '    <scene name="' + escapeXml(scene.name) + '">\n';
      for (const el of scene.elements) {
        xml += '      <element type="' + escapeXml(el.type) + '"';
        xml += ' x="' + el.x + '"';
        xml += ' y="' + el.y + '"';
        xml += ' width="' + el.width + '"';
        xml += ' height="' + el.height + '"';
        if (el.color) xml += ' color="' + escapeXml(el.color) + '"';
        if (el.bgColor) xml += ' bgColor="' + escapeXml(el.bgColor) + '"';
        if (el.font) xml += ' font="' + escapeXml(el.font) + '"';
        if (el.opacity != null) xml += ' opacity="' + el.opacity + '"';
        if (el.trigger) xml += ' trigger="' + escapeXml(el.trigger) + '"';
        if (el.sceneChange) xml += ' sceneChange="' + escapeXml(el.sceneChange) + '"';
        if (el.externalAppPath) xml += ' externalAppPath="' + escapeXml(el.externalAppPath) + '"';
        if (el.externalAppReturn) xml += ' externalAppReturn="' + escapeXml(el.externalAppReturn) + '"';
        if (el.variableChange) xml += ' variableChange="' + escapeXml(el.variableChange) + '"';
        if (el.variableChangeValue) xml += ' variableChangeValue="' + escapeXml(el.variableChangeValue) + '"';
        if (el.mediaVariable) xml += ' mediaVariable="' + escapeXml(el.mediaVariable) + '"';
        if (el.command != null) xml += ' command="' + escapeXml(el.command) + '"';
        if (el.variable != null) xml += ' variable="' + escapeXml(el.variable) + '"';
        if (el.source) xml += ' source="' + escapeXml(el.source) + '"';
        if (el.jsonPath) xml += ' jsonPath="' + escapeXml(el.jsonPath) + '"';
        if (el.autoRefresh != null) xml += ' autoRefresh="' + el.autoRefresh + '"';
        if (el.image) xml += ' image="' + escapeXml(el.image) + '"';
        if (el.videoVariable) xml += ' videoVariable="' + escapeXml(el.videoVariable) + '"';
        if (el.text != null) {
          xml += '>' + escapeXml(el.text) + '</element>\n';
        } else {
          xml += ' />\n';
        }
      }
      xml += '    </scene>\n';
    }
    xml += '  </scenes>\n';
  }

  xml += '</jukaconfig>';
  return xml;
}

// ─── XML → JSON parser ────────────────────────────────────────────────────────

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

  // Parse <variables>
  const varsEl = root.querySelector(':scope > variables');
  if (varsEl) {
    for (const child of varsEl.children) {
      if (child.tagName === 'fontSizes') {
        config.variables.fontSizes = {};
        for (const fs of child.children) {
          config.variables.fontSizes[fs.tagName] = parseInt(getText(fs), 10) || 0;
        }
      } else {
        // Check if it has children (nested object)
        if (child.children.length > 0) {
          const obj = {};
          for (const nested of child.children) {
            obj[nested.tagName] = getText(nested);
          }
          config.variables[child.tagName] = obj;
        } else {
          config.variables[child.tagName] = getText(child);
        }
      }
    }
  }

  // Parse <scenes>
  const scenesEl = root.querySelector(':scope > scenes');
  if (scenesEl) {
    for (const sceneEl of scenesEl.querySelectorAll(':scope > scene')) {
      const scene = { name: sceneEl.getAttribute('name') || '', elements: [] };
      for (const el of sceneEl.querySelectorAll(':scope > element')) {
        const element = {
          type: el.getAttribute('type') || '',
          x: parseInt(el.getAttribute('x')) || 0,
          y: parseInt(el.getAttribute('y')) || 0,
          width: parseInt(el.getAttribute('width')) || 100,
          height: parseInt(el.getAttribute('height')) || 40
        };
        if (el.getAttribute('color')) element.color = el.getAttribute('color');
        if (el.getAttribute('bgColor')) element.bgColor = el.getAttribute('bgColor');
        if (el.getAttribute('font')) element.font = el.getAttribute('font');
        if (el.getAttribute('opacity')) element.opacity = parseFloat(el.getAttribute('opacity'));
        if (el.getAttribute('trigger')) element.trigger = el.getAttribute('trigger');
        if (el.getAttribute('sceneChange')) element.sceneChange = el.getAttribute('sceneChange');
        if (el.getAttribute('externalAppPath')) element.externalAppPath = el.getAttribute('externalAppPath');
        if (el.getAttribute('externalAppReturn')) element.externalAppReturn = el.getAttribute('externalAppReturn');
        if (el.getAttribute('variableChange')) element.variableChange = el.getAttribute('variableChange');
        if (el.getAttribute('variableChangeValue')) element.variableChangeValue = el.getAttribute('variableChangeValue');
        if (el.getAttribute('mediaVariable')) element.mediaVariable = el.getAttribute('mediaVariable');
        if (el.getAttribute('command') != null) element.command = el.getAttribute('command');
        if (el.getAttribute('variable') != null) element.variable = el.getAttribute('variable');
        if (el.getAttribute('source')) element.source = el.getAttribute('source');
        if (el.getAttribute('jsonPath')) element.jsonPath = el.getAttribute('jsonPath');
        if (el.getAttribute('autoRefresh')) element.autoRefresh = el.getAttribute('autoRefresh') === 'true';
        if (el.getAttribute('image')) element.image = el.getAttribute('image');
        if (el.getAttribute('videoVariable')) element.videoVariable = el.getAttribute('videoVariable');
        element.text = getText(el);
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
  el.setAttribute('data-width', width);
  el.setAttribute('data-height', height);

  // Add text content
  const textSpan = document.createElement('span');
  textSpan.className = 'text-content';
  textSpan.textContent = elementData.text || elementData.type.charAt(0).toUpperCase() + elementData.type.slice(1);
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
      const source = elementData.source || 'system';
      textContent.textContent = `${sourceIcons[source] || '🌐'} ${sourceNames[source] || 'Text Browser'}`;
    }
  }
  if (elementData.type === 'button') {
    el.setAttribute('data-color', elementData.color || '#000000');
    el.style.color = elementData.color || '#000000';
    el.setAttribute('data-bg-color', elementData.bgColor || '#ffffff');
    el.style.backgroundColor = elementData.bgColor || '#ffffff';
    el.setAttribute('data-font', elementData.font || 'medium');
    el.style.fontSize = getFontSize(elementData.font || 'medium') + 'px';
  } else if (elementData.type === 'label') {
    el.setAttribute('data-color', elementData.color || '#000000');
    el.style.color = elementData.color || '#000000';
    el.setAttribute('data-font', elementData.font || 'medium');
    el.style.fontSize = getFontSize(elementData.font || 'medium') + 'px';
    el.style.background = 'none';
  }

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

    // Ensure left sidebar is hidden
    document.querySelector('.left-sidebar').style.display = 'none';
  } else {
    // Show left sidebar when not on mobile
    document.querySelector('.left-sidebar').style.display = 'flex';

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
