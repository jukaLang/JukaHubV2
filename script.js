// DOM Elements (cached for performance)
const canvas = document.getElementById('canvas');
darkModeToggle = darkModeToggle || document.querySelector('.dark-mode-toggle');
sceneSelector = sceneSelector || document.querySelector('#scene-selector') || document.getElementById('sceneSelector');
toggleGuide = toggleGuide || document.querySelector('#toggle-guide') || document.getElementById('toggleGuide');
closeGuide = closeGuide || document.querySelector('#close-guide') || document.getElementById('closeGuide');
canvasSizeSelect = canvasSizeSelect || document.querySelector('#canvas-size-select') || document.getElementById('canvasSize');
customWidthInput = customWidthInput || document.querySelector('#custom-width-input') || document.getElementById('customWidth');
customHeightInput = customHeightInput || document.querySelector('#custom-height-input') || document.getElementById('customHeight');
backgroundPathInput = backgroundPathInput || document.querySelector('#background-path-input') || document.getElementById('backgroundPathInput');
titleSizeInput = titleSizeInput || document.querySelector('#title-size-input') || document.getElementById('titleSize');
bigSizeInput = bigSizeInput || document.querySelector('#big-size-input') || document.getElementById('bigSize');
mediumSizeInput = mediumSizeInput || document.querySelector('#medium-size-input') || document.getElementById('mediumSize');
smallSizeInput = smallSizeInput || document.querySelector('#small-size-input') || document.getElementById('smallSize');
addVariableButton = addVariableButton || document.querySelector('#add-variable-button') || document.getElementById('addVariableButton');
variablesList = variablesList || document.querySelector('#variables-list') || document.getElementById('variablesList');
loadFileInput = loadFileInput || document.querySelector('#load-file-input') || document.getElementById('loadFile');
clearButton = clearButton || document.querySelector('#clear-button') || document.getElementById('clearButton');
propertiesTabs = propertiesTabs || document.querySelectorAll('.properties-tab');
elementPropertiesPanel = elementPropertiesPanel || document.querySelector('#element-properties-panel') || document.getElementById('elementPropertiesPanel');
appInfoPanel = appInfoPanel || document.querySelector('#app-info-panel') || document.getElementById('appInfoPanel');
videoProperties = videoProperties || document.querySelector('#video-properties') || document.getElementById('videoProperties');

// Global State
let backgroundPath = 'background.jpg';
let scenes = { 'Scene 1': [] };
let currentScene = 'Scene 1';
let variables = {};
let currentElement = null;
let canvasWidth = 1280;
let canvasHeight = 720;
let videoList = [];

// Toast notification helper for better UX than alerts
function showToast(message, subtitle = '', type = 'info') {
  const toast = document.createElement('div');
  toast.className = `toast toast-${type}`;
  toast.innerHTML = `<div class="toast-title">${message}</div><div class="toast-subtitle">${subtitle || ''}</div>`;
  document.body.appendChild(toast);
  setTimeout(() => {
    toast.style.animation = 'fadeOut 0.3s forwards';
    setTimeout(() => toast.remove(), 300);
  }, 4000);
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

  // Set up event listeners using event delegation where possible
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

  loadDefaultConfig();
  setupMobileElementAdding();
  setupMobileCanvasClick();
  setupMobileElementSelection();
});

// Setup canvas event delegation for better performance
function setupEventListeners() {
  // Dark mode toggle
  darkModeToggle?.addEventListener('click', () => {
    const isDarkMode = !document.body.classList.contains('dark-mode');
    document.body.classList.toggle('dark-mode');
    darkModeToggle.innerHTML = isDarkMode ? '<i class="fas fa-sun"></i> Light Mode' : '<i class="fas fa-moon"></i> Dark Mode';

    // Preserve canvas background in dark mode
    if (isDarkMode) {
      const bgImage = canvas.style.backgroundImage || '';
      const bgSize = canvas.style.backgroundSize || '';
      canvas.style.backgroundImage = bgImage;
      canvas.style.backgroundSize = bgSize;
    }
  });

  // Guide panel toggle
  toggleGuide?.addEventListener('click', () => {
    guidePanel.style.display = guidePanel.style.display === 'block' ? 'none' : 'block';
  });

  closeGuide?.addEventListener('click', () => {
    guidePanel.style.display = 'none';
  });

  // Properties tabs using event delegation on container
  if (propertiesTabs.length > 0) {
    const propertiesContainer = document.querySelector('.properties-tabs') || document.body;
    propertiesContainer.addEventListener('click', (e) => {
      const tab = e.target.closest('.properties-tab');
      if (tab && tab.dataset.tab) {
        switchTab(tab.dataset.tab);
      }
    });
  }

  // Canvas drop zone using event delegation
  canvas.addEventListener('dragover', (e) => e.preventDefault());
  canvas.addEventListener('drop', (e) => {
    e.preventDefault();
    const type = e.dataTransfer.getData('type');
    if (!type) return;
    const rect = canvas.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const y = e.clientY - rect.top;
    addElement(type, x, y);
  });

  // Canvas click to deselect using event delegation
  canvas.addEventListener('click', (e) => {
    if (e.target === canvas || !e.target.closest('.element')) {
      currentElement = null;
      document.querySelectorAll('.element').forEach(el => el.classList.remove('selected'));
      document.body.classList.remove('element-selected');
      switchTab('app-properties');
    }
  });

  // Canvas click to select elements using event delegation
  canvas.addEventListener('click', (e) => {
    const target = e.target.closest('.element');
    if (target) {
      currentElement = target;
      document.querySelectorAll('.element').forEach(el => el.classList.remove('selected'));
      target.classList.add('selected');
      document.body.classList.add('element-selected');
      switchTab('element-properties');
    }
  });

  // Canvas size controls using event delegation
  canvasSizeSelect?.addEventListener('change', updateCanvasSize);
  customWidthInput?.addEventListener('change', updateCanvasSize);
  customHeightInput?.addEventListener('change', updateCanvasSize);

  // Background image (stored as a path string, matching the Go runtime)
  if (backgroundPathInput) {
    backgroundPathInput.addEventListener('input', setBackgroundPath);
  }

  // Add variable button
  addVariableButton?.addEventListener('click', addVariable);

  setupMobileDoubleTap();

  // Load file
  loadFileInput?.addEventListener('change', function (e) {
    const file = e.target.files[0];
    if (!file) return;

    const reader = new FileReader();
    reader.onload = e => {
      try {
        const config = JSON.parse(e.target.result);
        loadJukaApp(config);
      } catch (error) {
        showToast('Error loading config:', error.message, 'error');
      }
    };
    reader.readAsText(file);
  });

  // Clear button
  clearButton?.addEventListener('click', clearAll);
}

// Set up font size change listeners using event delegation
function setupFontSizeListeners() {
  [titleSizeInput, bigSizeInput, mediumSizeInput, smallSizeInput].forEach(input => {
    input?.addEventListener('change', updateAllFontSizes);
  });
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
    el.style.fontSize = getFontSize('small') + 'px';
  });

  document.querySelectorAll('.menu-clock').forEach(el => {
    el.style.fontSize = getFontSize('small') + 'px';
  });
}

// Switch between tabs
function switchTab(tabId) {
  // Update active tab using event delegation
  propertiesTabs.forEach(tab => {
    if (tab.dataset.tab === tabId) {
      tab.classList.add('active');
    } else {
      tab.classList.remove('active');
    }
  });

  // Show/hide panels
  if (tabId === 'app-properties') {
    appInfoPanel.style.display = 'block';
    elementPropertiesPanel.style.display = 'none';
  } else {
    appInfoPanel.style.display = 'none';
    elementPropertiesPanel.style.display = 'block';
  }
}

// Update canvas size based on selection - optimized with cached references
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

  // Apply new size to canvas
  canvas.style.width = `${canvasWidth}px`;
  canvas.style.height = `${canvasHeight}px`;

  // Update menu position using event delegation
  document.querySelectorAll('[data-type="menu"]').forEach(menu => {
    menu.style.top = `${canvasHeight - 50}px`;
  });

  // Update all elements to stay within new canvas bounds (optimized)
  const elements = document.querySelectorAll('.element');
  elements.forEach(el => {
    const x = parseInt(el.getAttribute('data-x'));
    const y = parseInt(el.getAttribute('data-y'));
    const width = parseInt(el.getAttribute('data-width')) || 100;
    const height = parseInt(el.getAttribute('data-height')) || 100;

    // Ensure element stays within canvas bounds
    const newX = Math.min(x, canvasWidth - width);
    const newY = Math.min(y, canvasHeight - height);

    el.style.left = `${newX}px`;
    el.style.top = `${newY}px`;
    el.setAttribute('data-x', newX);
    el.setAttribute('data-y', newY);
  });
}

// Add a new scene with saved current scene first
function addScene() {
  saveCurrentScene();
  
  // Check if new scene name already exists
  const newSceneName = `Scene ${Object.keys(scenes).length + 1}`;
  if (scenes[newSceneName]) return showToast('Scene name already exists.', 'Please choose a unique name.', 'warning');

  scenes[newSceneName] = [];

  // Add scene to selector
  const option = document.createElement('option');
  option.value = newSceneName;
  option.textContent = newSceneName;
  sceneSelector.appendChild(option);
  sceneSelector.value = newSceneName;
  currentScene = newSceneName;

  loadScene(currentScene);
  
  // Update scene change selector and menu buttons
  updateSceneChangeSelector();
  updateAllMenuSceneButtons();
}

// Duplicate current scene with saved copy
function duplicateScene() {
  saveCurrentScene();
  
  const newSceneName = prompt('Name for duplicated scene:', `${currentScene} Copy`);
  if (!newSceneName || scenes[newSceneName]) {
    showToast('Invalid scene name.', 'Please choose a unique name.', 'warning');
    return;
  }

  // Deep copy scene elements
  scenes[newSceneName] = JSON.parse(JSON.stringify(scenes[currentScene]));

  const option = document.createElement('option');
  option.value = newSceneName;
  option.textContent = newSceneName;
  sceneSelector.appendChild(option);
  sceneSelector.value = newSceneName;
  currentScene = newSceneName;

  loadScene(newSceneName);
  
  // Update UI elements
  updateSceneChangeSelector();
  updateAllMenuSceneButtons();
}

// Change to selected scene
function changeScene() {
  saveCurrentScene();
  currentScene = sceneSelector.value;
  loadScene(currentScene);
  updateAllMenuSceneButtons();
}

// Load and render a scene
function loadScene(sceneName) {
  canvas.innerHTML = '';
  
  if (scenes[sceneName]) {
    scenes[sceneName].forEach(el => {
      const clonedEl = el.cloneNode(true);
      setupElementEvents(clonedEl);
      canvas.appendChild(clonedEl);
    });
  }

  // Update menu buttons for all menus in scene
  document.querySelectorAll('[data-type="menu"]').forEach(menu => {
    updateMenuSceneButtons(menu);
  });
}

// Add a new element to the canvas with proper validation
function addElement(type, x, y) {
  if (type === 'menu-element') type = 'menu'; // Convert to actual type

  const el = document.createElement('div');
  el.className = 'element';
  el.style.position = 'absolute';
  el.style.left = `${x}px`;
  el.style.top = `${y}px`;
  el.setAttribute('data-opacity', '100');
  el.style.opacity = 1;
  el.style.fontFamily = 'Roboto, sans-serif';
  el.style.fontWeight = '900';

  // Set default dimensions based on element type
  const dimensions = {
    button: { width: '120px', height: '40px' },
    label: { width: '120px', height: '40px' },
    menu: { width: '100%', height: '50px', y: canvasHeight - 50 },
    image: { width: '100px', height: '100px' },
    input: { width: '150px', height: '40px' },
    video: { width: '200px', height: '150px' },
    dynamiclist: { width: '600px', height: '40px' },
    searchresults: { width: '1160px', height: '510px' },
    textlog: { width: '600px', height: '300px' }
  };

  const { width, height } = dimensions[type] || dimensions.default;
  el.style.width = width;
  el.style.height = height;

  // Make elements larger on mobile for better touch interaction
  if (window.innerWidth <= 768) {
    if (['button', 'label', 'input'].includes(type)) {
      el.style.minHeight = '44px'; // Minimum touch target size
      el.style.minWidth = '80px';
    }
  }

  // Create element content based on type
  createElementContent(el, type);

  // Set element attributes
  const widthValue = width.replace('px', '') || '100';
  const heightValue = height.replace('px', '') || '100';
  
  el.setAttribute('data-x', x | 0);
  el.setAttribute('data-y', y | 0);
  el.setAttribute('data-width', widthValue);
  el.setAttribute('data-height', heightValue);

  // Add element to DOM
  canvas.appendChild(el);
}

// Create content for different element types - extracted from duplicated code
function createElementContent(el, type) {
  switch (type) {
    case 'menu':
      el.style.top = `${dimensions.menu.y}px`;
      el.innerHTML = `
        <div class="menu-scene-buttons"></div>
        <div class="menu-clock">00:00</div>
        <span class="remove-button">✕</span>
      `;
      el.style.fontSize = '16px';
      el.setAttribute('data-type', 'menu');
      setupMenuEvents(el);
      updateMenuSceneButtons(el);
      updateMenuClock(el.querySelector('.menu-clock'));
      break;

    case 'dynamiclist':
      el.innerHTML = `
        <span class="text-content">Dynamic List</span>
        <span class="remove-button">✕</span>
      `;
      el.setAttribute('data-variable', '');
      setupDynamicListExecution(el);
      break;

    case 'searchresults':
      el.innerHTML = `
        <div class="searchresults-placeholder">
          <i class="fas fa-th-large"></i>
          <span>Search Results Grid</span>
        </div>
        <span class="remove-button">✕</span>
      `;
      el.setAttribute('data-results-variable', 'search_results');
      el.classList.add('searchresults-element');
      break;

    case 'input':
      const input = document.createElement('input');
      input.type = 'text';
      input.className = 'element-input';
      input.placeholder = 'Input text';
      input.addEventListener('mousedown', e => e.stopPropagation()); // Prevent dragging on input
      el.appendChild(input);
      break;

    case 'image':
      const img = document.createElement('img');
      img.className = 'element-image';
      img.src = '';
      img.draggable = false; // Prevent image dragging
      el.appendChild(img);
      break;

    default:
      const textSpan = document.createElement('span');
      textSpan.className = 'text-content';
      
      let displayText = type.charAt(0).toUpperCase() + type.slice(1);
      if (type === 'collapsedlist') displayText = 'Collapsed List';
      textSpan.textContent = displayText;

      const removeButton = document.createElement('span');
      removeButton.textContent = '✕';
      removeButton.className = 'remove-button';
      
      el.appendChild(textSpan);
      el.appendChild(removeButton);
      el.setAttribute('data-type', type);

      if (type === 'collapsedlist') {
        const listIcon = document.createElement('i');
        listIcon.className = 'fas fa-bars';
        listIcon.style.marginRight = '8px';
        textSpan.prepend(listIcon);
        
        el.setAttribute('data-list-variable', '');
      }

      if (type === 'label') {
        el.style.background = 'none';
      }
  }
}

// Setup menu-specific events using event delegation
function setupMenuEvents(menu) {
  // Use event delegation for scene buttons
  const btnContainer = menu.querySelector('.menu-scene-buttons');
  if (btnContainer) {
    btnContainer.addEventListener('click', (e) => {
      const btn = e.target.closest('[data-target="scene"]');
      if (btn) {
        changeScene(btn.dataset.target);
      }
    });
  }

  // Setup button click handlers using delegation
  menu.querySelectorAll('.button').forEach(button => {
    button.addEventListener('click', () => handleButtonClick(button));
  });
}

// Handle button clicks with proper validation
function handleButtonClick(button) {
  const target = button.getAttribute('data-target');
  if (target === 'close') {
    removeElement(button);
  } else if (target && scenes[target]) {
    changeScene(target);
  }
}

// Add a new variable to the variables list
function addVariable() {
  const newName = prompt('Enter variable name:', `variable_${Date.now()}`);
  if (!newName) return;

  if (variables[newName] !== undefined) {
    showToast('Variable already exists.', 'Please choose a unique name.', 'warning');
    return;
  }

  variables[newName] = '';
  
  // Create variable item UI element
  const variableItem = document.createElement('div');
  variableItem.className = 'variable-item';
  variableItem.innerHTML = `
    <input type="text" class="variable-value" value="${escapeHtml(newName)}">
    <button class="remove-variable-remove">✕</button>
  `;
  
  variablesList.appendChild(variableItem);

  // Setup event listeners for the new variable
  const newValueInput = variableItem.querySelector('.variable-value');
  if (newValueInput) {
    newValueInput.addEventListener('change', () => {
      variables[newName] = newValueInput.value;
      updateVariableChangeSelector();
    });
  }

  // Setup remove button
  const removeBtn = variableItem.querySelector('.remove-variable-remove');
  if (removeBtn) {
    removeBtn.addEventListener('click', () => removeVariable(newName));
  }
}

// Remove a variable from the variables list
function removeVariable(name) {
  delete variables[name];
  
  const item = document.querySelector(`.variable-item[data-name="${escapeHtml(name)}"]`);
  if (item) {
    item.remove();
  }

  updateVariableChangeSelector();
}

// Setup mobile double-tap to add element on canvas
function setupMobileDoubleTap() {
  let touchStart = null;
  let touchTimer = null;

  canvas.addEventListener('touchstart', (e) => {
    if (e.touches.length !== 1) return;
    
    const touch = e.touches[0];
    touchStart = { x: touch.clientX, y: touch.clientY };
    
    // Start double-tap timer
    touchTimer = setTimeout(() => {
      // Double tap detected - add element at this position
      if (touchStart) {
        addElement('button', touchStart.x, touchStart.y);
        touchStart = null;
      }
    }, 500);
  });

  canvas.addEventListener('touchmove', () => {
    clearTimeout(touchTimer);
    touchTimer = null;
  });

  canvas.addEventListener('touchend', () => {
    clearTimeout(touchTimer);
    touchTimer = null;
  });
}

// Setup mobile click to select element on canvas
function setupMobileCanvasClick() {
  canvas.addEventListener('click', (e) => {
    if (!currentElement) {
      const target = e.target.closest('.element');
      if (target) {
        currentElement = target;
        document.querySelectorAll('.element').forEach(el => el.classList.remove('selected'));
        target.classList.add('selected');
        document.body.classList.add('element-selected');
        switchTab('element-properties');
      }
    }
  });

  canvas.addEventListener('contextmenu', (e) => {
    e.preventDefault(); // Prevent default context menu
    const target = e.target.closest('.element');
    if (target) {
      currentElement = target;
      document.querySelectorAll('.element').forEach(el => el.classList.remove('selected'));
      target.classList.add('selected');
      document.body.classList.add('element-selected');
      switchTab('element-properties');
    }
  });
}

// Setup mobile element selection with long-press
function setupMobileElementSelection() {
  let touchStart = null;
  let isLongPress = false;

  canvas.addEventListener('touchstart', (e) => {
    if (!currentElement && e.target.closest('.element')) {
      const target = e.target.closest('.element');
      
      // Check if it's a long press or single tap
      setTimeout(() => {
        isLongPress = true;
        currentElement = target;
        document.querySelectorAll('.element').forEach(el => el.classList.remove('selected'));
        target.classList.add('selected');
        document.body.classList.add('element-selected');
        switchTab('element-properties');
        
        clearTimeout(touchTimer);
      }, 500);
    }
  });

  canvas.addEventListener('touchmove', () => {
    isLongPress = false;
    clearTimeout(touchTimer);
  });

  canvas.addEventListener('touchend', () => {
    if (!isLongPress) {
      // Single tap - deselect
      currentElement = null;
      document.querySelectorAll('.element').forEach(el => el.classList.remove('selected'));
      document.body.classList.remove('element-selected');
    }
    
    isLongPress = false;
  });
}

// Load Juka app from config
function loadJukaApp(config) {
  if (!config || typeof config !== 'object') return;

  // Update canvas size from config
  if (config.canvasSize) {
    const [width, height] = config.canvasSize.split('x').map(Number);
    canvasWidth = width;
    canvasHeight = height;
    updateCanvasSize();
  }

  // Load scenes from config
  if (config.scenes) {
    scenes = config.scenes;
    currentScene = 'Scene 1';
    
    const option = document.createElement('option');
    option.value = 'Scene 1';
    option.textContent = 'Scene 1';
    sceneSelector.appendChild(option);
    sceneSelector.value = 'Scene 1';

    loadScene(currentScene);
  }

  // Load variables from config
  if (config.variables) {
    variables = config.variables;
    updateVariableChangeSelector();
  }

  showToast('App loaded successfully.', 'All scenes and variables have been imported.', 'success');
}

// Show/hide variable change selector based on active element type
function updateVariableChangeSelector() {
  const hasDynamicList = document.querySelector('[data-type="dynamiclist"]') || 
                         document.querySelector('[data-list-variable]');
  
  if (hasDynamicList) {
    document.getElementById('variable-change-selector').style.display = 'block';
  } else {
    document.getElementById('variable-change-selector').style.display = 'none';
  }
}

// Set up dynamic list element execution
function setupDynamicListExecution(el) {
  const varInput = el.querySelector('[data-variable]');
  if (varInput) {
    varInput.addEventListener('input', () => {
      updateAllFontSizes(); // Update font sizes based on variable
    });
  }
}

// Setup dynamic list properties
function setupDynamicListProperties() {
  document.querySelectorAll('.dynamic-list-properties input').forEach(input => {
    input.addEventListener('change', (e) => {
      const parent = e.target.closest('.element');
      if (parent && parent.getAttribute('data-variable')) {
        variables[parent.getAttribute('data-variable')] = e.target.value;
      }
    });
  });
}

// Set up menu scene buttons using event delegation
function updateMenuSceneButtons(menu) {
  const btnContainer = menu.querySelector('.menu-scene-buttons');
  if (!btnContainer) return;

  // Remove old buttons and recreate with delegation
  btnContainer.innerHTML = '';
  
  document.querySelectorAll('li.scene-item').forEach(sceneItem => {
    const sceneName = sceneItem.getAttribute('data-scene');
    if (sceneName && scenes[sceneName]) {
      const button = document.createElement('button');
      button.className = 'menu-scene-button';
      button.setAttribute('data-target', sceneName);
      button.textContent = sceneName;
      
      if (currentScene === sceneName) {
        button.classList.add('active');
      }
      
      btnContainer.appendChild(button);
    }
  });
}

// Update all stored menus' scene buttons using event delegation
function updateAllStoredMenus() {
  document.querySelectorAll('[data-type="menu"]').forEach(menu => {
    const btnContainer = menu.querySelector('.menu-scene-buttons');
    if (btnContainer) {
      btnContainer.innerHTML = '';
      
      document.querySelectorAll('li.scene-item').forEach(sceneItem => {
        const sceneName = sceneItem.getAttribute('data-scene');
        if (sceneName && scenes[sceneName]) {
          const button = document.createElement('button');
          button.className = 'menu-scene-button';
          button.setAttribute('data-target', sceneName);
          button.textContent = sceneName;
          
          if (currentScene === sceneName) {
            button.classList.add('active');
          }
          
          btnContainer.appendChild(button);
        }
      });
    }
  });
}

// Update menu clock using event delegation on all menus
function updateMenuClock(menu) {
  const clock = menu.querySelector('.menu-clock');
  if (clock) {
    updateClock(clock);
  }
}

// Update clock display - use setInterval for smooth updates
let clockInterval = null;
function updateClock(clockElement) {
  clockInterval = setInterval(() => {
    const now = new Date();
    const hours = String(now.getHours()).padStart(2, '0');
    const minutes = String(now.getMinutes()).padStart(2, '0');
    clockElement.textContent = `${hours}:${minutes}`;
  }, 1000);

  // Cleanup interval when menu is removed
  const observer = new MutationObserver((mutations) => {
    if (mutations.some(mutation => mutation.type === 'childList')) {
      clearInterval(clockInterval);
      clockInterval = null;
      observer.disconnect();
    }
  });

  observer.observe(canvas, { childList: true });
}

// Update variable change selector based on active element
function updateVariableChangeSelector() {
  const hasDynamicList = document.querySelector('[data-type="dynamiclist"]') || 
                         document.querySelector('[data-list-variable]');
  
  document.getElementById('variable-change-selector').style.display = hasDynamicList ? 'block' : 'none';
}

// Setup font size change listeners using event delegation
function setupFontSizeListeners() {
  [titleSizeInput, bigSizeInput, mediumSizeInput, smallSizeInput].forEach(input => {
    if (input) input.addEventListener('change', updateAllFontSizes);
  });
}

// Create global tooltip for variable values
function createGlobalTooltip() {
  const body = document.body || window.document.body;
  
  globalTooltip = document.createElement('div');
  globalTooltip.className = 'variable-tooltip';
  globalTooltip.style.display = 'none';
  globalTooltip.setAttribute('role', 'tooltip');
  globalTooltip.style.position = 'fixed';
  globalTooltip.style.pointerEvents = 'none';
  globalTooltip.style.zIndex = '9999';
  
  body.appendChild(globalTooltip);

  // Position tooltip on hover over variable elements
  canvas.addEventListener('mouseenter', (e) => {
    const target = e.target.closest('[data-variable]');
    if (target && target.getAttribute('data-variable')) {
      positionTooltip(target, globaltooltip.textContent || '');
    }
  });

  canvas.addEventListener('mouseleave', () => {
    globaltooltip.style.display = 'none';
  });
}

// Position tooltip near element
function positionTooltip(element, value) {
  if (!globalTooltip) return;
  
  const rect = element.getBoundingClientRect();
  globaltooltip.textContent = value || '';
  globaltooltip.style.left = `${rect.right + 10}px`;
  globaltooltip.style.top = `${rect.top}px`;
  globaltooltip.style.display = 'block';
}

// Load default configuration from localStorage or hardcoded defaults
function loadDefaultConfig() {
  try {
    const saved = localStorage.getItem('jukahub-config');
    if (saved) {
      const config = JSON.parse(saved);
      loadJukaApp(config);
      showToast('Loaded saved configuration.', 'Your previous project has been restored.', 'info');
    } else {
      // Create default config
      const defaultConfig = {
        canvasSize: '1280x720',
        scenes: { 'Scene 1': [] },
        variables: {}
      };
      
      localStorage.setItem('jukahub-config', JSON.stringify(defaultConfig));
      loadJukaApp(defaultConfig);
    }
  } catch (error) {
    showToast('Error loading saved config.', 'Please create a new project.', 'warning');
  }
}

// Clear all elements and scenes
function clearAll() {
  if (scenes['Scene 1'].length > 0 && Object.keys(scenes).length === 1) {
    showToast('Cannot clear scene.', 'There must be at least one element in a scene.', 'warning');
    return;
  }

  scenes = {};
  
  // Clear DOM elements
  canvas.innerHTML = '';
  
  // Reset UI elements
  document.querySelectorAll('.menu-scene-buttons').forEach(el => el.innerHTML = '');
  updateSceneChangeSelector();
  updateVariableChangeSelector();
  switchTab('app-properties');
}

// Save current scene to state
function saveCurrentScene() {
  scenes[currentScene] = Array.from(canvas.querySelectorAll('.element')).map(el => ({
    type: el.getAttribute('data-type'),
    x: parseInt(el.setAttribute('data-x')) || 0,
    y: parseInt(el.setAttribute('data-y')) || 0,
    width: parseInt(el.getAttribute('data-width')) || 100,
    height: parseInt(el.setAttribute('data-height')) || 100,
    opacity: el.getAttribute('data-opacity') || 100,
    // Add more properties as needed...
  }));
}

// Get font size based on font type (inferred from data or element class)
function getFontSize(fontType) {
  const sizes = {
    small: '12px',
    medium: '16px',
    large: '20px',
    extraLarge: '24px'
  };
  
  return sizes[fontType] || '16px'; // Default size
}

// Escape HTML to prevent XSS attacks when displaying variable values
function escapeHtml(text) {
  const div = document.createElement('div');
  div.textContent = text;
  return div.innerHTML;
}

// Handle element removal with proper cleanup
function removeElement(el) {
  // Remove from DOM
  el.remove();
  
  // Remove from current scene data (simplified - in production, update the full state)
  if (el.getAttribute('data-type') === 'menu') {
    const menuSceneButtons = el.querySelector('.menu-scene-buttons');
    if (menuSceneButtons) {
      menuSceneButtons.innerHTML = '';
    }
    
    // Update all stored menus to remove this scene button
    document.querySelectorAll('[data-target]').forEach(btn => {
      btn.remove();
    });
  }

  showToast('Element removed.', 'Your UI has been updated.', 'info');
}

// Setup mobile double-tap and long-press handlers
function setupMobileDoubleTap() {
  let touchStart = null;
  let touchTimer = null;

  canvas.addEventListener('touchstart', (e) => {
    if (e.touches.length !== 1) return;
    
    const touch = e.touches[0];
    touchStart = { x: touch.clientX, y: touch.clientY };
    
    // Start double-tap timer
    touchTimer = setTimeout(() => {
      if (touchStart) {
        addElement('button', touchStart.x, touchStart.y);
        touchStart = null;
      }
    }, 500);
  });

  canvas.addEventListener('touchmove', () => {
    clearTimeout(touchTimer);
    touchTimer = null;
  });

  canvas.addEventListener('touchend', () => {
    clearTimeout(touchTimer);
    touchTimer = null;
  });
}

// Setup mobile click to select element on canvas
function setupMobileCanvasClick() {
  canvas.addEventListener('click', (e) => {
    if (!currentElement) {
      const target = e.target.closest('.element');
      if (target) {
        currentElement = target;
        document.querySelectorAll('.element').forEach(el => el.classList.remove('selected'));
        target.classList.add('selected');
        document.body.classList.add('element-selected');
        switchTab('element-properties');
      }
    }
  });

  canvas.addEventListener('contextmenu', (e) => {
    e.preventDefault(); // Prevent default context menu
    const target = e.target.closest('.element');
    if (target) {
      currentElement = target;
      document.querySelectorAll('.element').forEach(el => el.classList.remove('selected'));
      target.classList.add('selected');
      document.body.classList.add('element-selected');
      switchTab('element-properties');
    }
  });
}

// Setup mobile element selection with long-press detection
function setupMobileElementSelection() {
  let touchStart = null;
  let isLongPress = false;

  canvas.addEventListener('touchstart', (e) => {
    if (!currentElement && e.target.closest('.element')) {
      const target = e.target.closest('.element');
      
      // Check if it's a long press or single tap
      setTimeout(() => {
        isLongPress = true;
        currentElement = target;
        document.querySelectorAll('.element').forEach(el => el.classList.remove('selected'));
        target.classList.add('selected');
        document.body.classList.add('element-selected');
        switchTab('element-properties');
        
        clearTimeout(touchTimer);
      }, 500);
    }
  });

  canvas.addEventListener('touchmove', () => {
    isLongPress = false;
    clearTimeout(touchTimer);
  });

  canvas.addEventListener('touchend', () => {
    if (!isLongPress) {
      // Single tap - deselect
      currentElement = null;
      document.querySelectorAll('.element').forEach(el => el.classList.remove('selected'));
      document.body.classList.remove('element-selected');
    }
    
    isLongPress = false;
  });
}

// Export configuration to file for backup
function exportConfig() {
  const config = {
    canvasSize: `${canvasWidth}x${canvasHeight}`,
    scenes: scenes,
    variables: variables
  };
  
  const blob = new Blob([JSON.stringify(config, null, 2)], { type: 'application/json' });
  const url = URL.createObjectURL(blob);
  
  const link = document.createElement('a');
  link.href = url;
  link.download = `jukahub-config-${Date.now()}.json`;
  
  document.body.appendChild(link);
  link.click();
  document.body.removeChild(link);
  URL.revokeObjectURL(url);
}

// Setup element removal events using event delegation on canvas
canvas.addEventListener('click', (e) => {
  const target = e.target.closest('.remove-button, .remove-variable-remove');
  
  if (target && (target.classList.contains('remove-button') || target.classList.contains('remove-variable-remove'))) {
    // Find the parent element or variable item to remove
    let parent;
    
    if (target.classList.contains('remove-button')) {
      parent = target.closest('.element');
      if (parent) removeElement(parent);
    } else {
      parent = target.parentElement;
      if (parent && parent.classList.contains('variable-item')) {
        const varName = parent.querySelector('[data-name]').textContent;
        removeVariable(varName);
      }
    }
  }
});

// Handle canvas click to deselect when clicking on non-element areas
canvas.addEventListener('click', (e) => {
  if (!currentElement && !e.target.closest('.element')) {
    currentElement = null;
    document.querySelectorAll('.element').forEach(el => el.classList.remove('selected'));
    document.body.classList.remove('element-selected');
  }
});
