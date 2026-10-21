// builder-gallery.js — community layouts for the web builder.
//
// Layouts ship in gallery/layouts.js as complete jukaconfig.json objects. Fork
// copies one into the editor; Apply does the same and then pushes it to the
// paired handheld, which is the one-click path from "that looks nice" to "it is
// running on my TrimUI". No network is involved beyond the live builder call.

(function () {
  'use strict';

  const layouts = Array.isArray(window.JUKA_LAYOUTS) ? window.JUKA_LAYOUTS.slice() : [];
  let activeTag = 'All';
  let query = '';
  let initialized = false;

  function el(id) { return document.getElementById(id); }

  function clone(value) {
    return JSON.parse(JSON.stringify(value));
  }

  function tags() {
    const set = new Set();
    layouts.forEach(layout => (layout.tags || []).forEach(tag => set.add(tag)));
    return ['All'].concat(Array.from(set).sort());
  }

  function thumbColour(value, fallback) {
    return typeof value === 'string' && value.trim() ? value : fallback;
  }

  function renderThumb(config, container) {
    const scene = config && Array.isArray(config.scenes) ? config.scenes[0] : null;
    if (!scene) return;
    container.style.background = thumbColour(scene.background, '#0B0F17');
    const width = Number(config.Width) || 1280;
    const height = Number(config.Height) || 720;
    const fonts = (config.variables && config.variables.fontSizes) || {};
    const roleSize = { title: 15, big: 11, medium: 8, small: 6.5 };
    (scene.elements || []).forEach(element => {
      const node = document.createElement('div');
      node.className = 'gallery-thumb-el';
      node.setAttribute('aria-hidden', 'true');
      const type = String(element.type || '').toLowerCase();
      if (type === 'menu') return;
      const x = Number(element.x) || 0;
      const y = Number(element.y) || 0;
      const w = Number(element.width) || (type === 'label' ? 400 : 180);
      const h = Number(element.height) || (type === 'label' ? 40 : 60);
      node.style.left = (x / width) * 100 + '%';
      node.style.top = (y / height) * 100 + '%';
      node.style.width = (w / width) * 100 + '%';
      node.style.height = (h / height) * 100 + '%';
      node.style.color = thumbColour(element.color, '#F8FAFC');
      if (element.bgColor) {
        node.style.background = element.bgColor;
      } else if (type === 'button') {
        node.style.background = 'rgba(255,255,255,0.10)';
      }
      if (type === 'input') {
        node.style.border = '1px solid rgba(255,255,255,0.28)';
        node.style.borderRadius = '2px';
      }
      const role = String(element.font || 'medium').toLowerCase();
      const scaled = roleSize[role] || 8;
      // Scale the thumbnail's type with the element's share of the canvas so a
      // giant tile does not get microscopic text.
      node.style.fontSize = (scaled * Math.min(1.5, Math.max(0.7, w / 280))) + 'px';
      const text = element.text || element.placeholder || '';
      if (text) node.textContent = String(text).slice(0, 48);
      container.appendChild(node);
    });
  }

  function filtered() {
    const term = query.trim().toLowerCase();
    return layouts.filter(layout => {
      if (activeTag !== 'All' && !(layout.tags || []).includes(activeTag)) return false;
      if (!term) return true;
      return [layout.name, layout.author, layout.description, (layout.tags || []).join(' ')]
        .join(' ').toLowerCase().includes(term);
    });
  }

  function status(text) {
    const node = el('galleryStatus');
    if (!node) return;
    node.hidden = !text;
    node.textContent = text || '';
  }

  function makeCard(layout) {
    const card = document.createElement('article');
    card.className = 'gallery-card';

    const thumb = document.createElement('div');
    thumb.className = 'gallery-thumb';
    thumb.setAttribute('aria-hidden', 'true');
    const inner = document.createElement('div');
    inner.className = 'gallery-thumb-inner';
    try {
      renderThumb(layout.config, inner);
    } catch (e) {
      inner.style.background = layout.accent || '#1F2937';
    }
    thumb.appendChild(inner);

    const body = document.createElement('div');
    body.className = 'gallery-card-body';
    const title = document.createElement('h3');
    title.textContent = layout.name;
    const byline = document.createElement('p');
    byline.className = 'gallery-byline';
    byline.textContent = (layout.author || 'community') + ' · ' + (layout.tags || []).join(' / ');
    const desc = document.createElement('p');
    desc.className = 'gallery-desc';
    desc.textContent = layout.description || '';
    body.append(title, byline, desc);

    const actions = document.createElement('div');
    actions.className = 'gallery-card-actions';
    const fork = document.createElement('button');
    fork.type = 'button';
    fork.className = 'action-button';
    fork.innerHTML = '<i class="fas fa-code-branch" aria-hidden="true"></i> Fork';
    fork.setAttribute('aria-label', 'Fork ' + layout.name + ' into the editor');
    fork.addEventListener('click', () => forkLayout(layout));
    const apply = document.createElement('button');
    apply.type = 'button';
    apply.className = 'action-button primary';
    apply.innerHTML = '<i class="fas fa-bolt" aria-hidden="true"></i> Apply';
    apply.setAttribute('aria-label', 'Apply ' + layout.name + ' to the handheld');
    apply.title = 'Load this layout and push it to the connected device';
    apply.addEventListener('click', () => applyLayout(layout, apply));
    actions.append(fork, apply);

    card.append(thumb, body, actions);
    return card;
  }

  function render() {
    const grid = el('galleryGrid');
    if (!grid) return;
    const list = filtered();
    grid.innerHTML = '';
    list.forEach(layout => grid.appendChild(makeCard(layout)));
    status(list.length === 0 ? 'No layouts match that filter.' : '');
    const tagWrap = el('galleryTags');
    if (tagWrap) {
      tagWrap.querySelectorAll('.gallery-tag').forEach(button => {
        button.setAttribute('aria-pressed', String(button.dataset.tag === activeTag));
      });
    }
  }

  function renderTags() {
    const wrap = el('galleryTags');
    if (!wrap) return;
    wrap.innerHTML = '';
    tags().forEach(tag => {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'gallery-tag';
      button.dataset.tag = tag;
      button.textContent = tag;
      button.setAttribute('aria-pressed', String(tag === activeTag));
      button.addEventListener('click', () => {
        activeTag = tag;
        render();
      });
      wrap.appendChild(button);
    });
  }

  function commitLayout(layout) {
    if (!layout || !layout.config) return null;
    const config = clone(layout.config);
    const check = validateProject(config);
    if (check.errors > 0) {
      const first = check.issues.find(issue => issue.severity === 'error');
      showToast('Layout is not valid: ' + (first ? first.message : 'unknown error'), 'error');
      return null;
    }
    pushUndo('apply ' + layout.name);
    importedConfigSnapshot = clone(config);
    loadJukaApp(config);
    scheduleAutoSave();
    if (typeof refreshReloadButtonState === 'function') refreshReloadButtonState();
    return config;
  }

  function forkLayout(layout) {
    const config = commitLayout(layout);
    if (!config) return;
    closeGallery();
    showToast('Forked “' + layout.name + '” into the editor', 'success');
    announce('Forked ' + layout.name + '. ' + layout.description);
  }

  async function applyLayout(layout, button) {
    const config = commitLayout(layout);
    if (!config) return;
    closeGallery();
    if (!window.JukaLiveBuilder || typeof window.JukaLiveBuilder.pushConfig !== 'function') {
      showToast('Layout loaded — live builder is unavailable', 'warning');
      return;
    }
    if (!window.JukaLiveBuilder.isConnected()) {
      showToast('Layout loaded — connect a device to push it', 'info');
      window.JukaLiveBuilder.openPanel();
      return;
    }
    if (button) {
      button.disabled = true;
      button.setAttribute('aria-busy', 'true');
    }
    const ok = await window.JukaLiveBuilder.pushConfig(config);
    if (button) {
      button.disabled = false;
      button.removeAttribute('aria-busy');
    }
    if (ok) showToast('“' + layout.name + '” is live on the device', 'success');
  }

  function openGallery() {
    const panel = el('galleryPanel');
    if (!panel) return;
    if (!initialized) init();
    render();
    panel.removeAttribute('hidden');
    panel.setAttribute('aria-hidden', 'false');
    const search = el('gallerySearch');
    setTimeout(() => search?.focus(), 0);
  }

  function closeGallery() {
    const panel = el('galleryPanel');
    if (!panel) return;
    panel.setAttribute('hidden', '');
    panel.setAttribute('aria-hidden', 'true');
    el('galleryBtn')?.focus();
  }

  function init() {
    if (initialized) return;
    initialized = true;
    el('galleryBtn')?.addEventListener('click', openGallery);
    document.querySelectorAll('[data-close-gallery]').forEach(node => node.addEventListener('click', closeGallery));
    el('gallerySearch')?.addEventListener('input', event => {
      query = event.target.value;
      render();
    });
    document.addEventListener('keydown', event => {
      if (event.key !== 'Escape') return;
      const panel = el('galleryPanel');
      if (panel && !panel.hasAttribute('hidden')) closeGallery();
    });
    renderTags();
    render();
    if (layouts.length === 0) status('No bundled layouts were found.');
  }

  window.JukaGallery = { open: openGallery, close: closeGallery, layouts: () => layouts.slice() };

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
