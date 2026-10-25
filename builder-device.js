// builder-device.js — live builder over Wi-Fi.
//
// The web builder is a static page, so the handheld runs a small receiver
// (player/livebuilder.go). This module is the browser side: pair once with the
// address and six-digit code shown on the device, then push the current design.
// Auto-push turns the builder into a live canvas: every edit lands on the
// handheld about a second later and the running UI reloads in place.
//
// Nothing leaves the LAN. Device settings live in localStorage only.

(function () {
  'use strict';

  const STORAGE_KEY = 'jukahub-live-device';
  const DEFAULT_PORT = '8422';
  const REQUEST_TIMEOUT_MS = 8000;
  const AUTO_PUSH_DELAY_MS = 1500;

  const state = {
    url: '',
    code: '',
    auto: false,
    connected: false,
    info: null,
    lastPush: null,
    lastHash: '',
    lastError: '',
    pushing: false,
    pulling: false
  };

  let autoTimer = null;
  let initialized = false;

  function el(id) { return document.getElementById(id); }

  function setDot(mode) {
    const dot = el('deviceDot');
    if (!dot) return;
    dot.classList.toggle('is-connected', mode === 'connected' || mode === 'dirty');
    dot.classList.toggle('is-busy', mode === 'busy');
    dot.classList.toggle('is-error', mode === 'error');
    dot.classList.toggle('is-dirty', mode === 'dirty');
    const labels = {
      connected: 'Live builder connected',
      dirty: 'Live builder connected, edits not pushed',
      busy: 'Live builder working',
      error: 'Live builder connection problem',
      offline: 'Live builder not connected'
    };
    const btn = el('deviceBtn');
    if (btn) btn.title = labels[mode] || labels.offline;
  }

  function setState(text, kind) {
    const pill = el('deviceState');
    if (pill) pill.setAttribute('data-state', kind || 'offline');
    const label = el('deviceStateText');
    if (label) label.textContent = text;
    setDot(kind === 'connected' ? (isDirty() ? 'dirty' : 'connected') : kind === 'connecting' ? 'busy' : kind === 'error' ? 'error' : 'offline');
  }

  function setMeta(text) {
    const meta = el('deviceMeta');
    if (meta) meta.textContent = text;
  }

  function setWarning(text) {
    const warning = el('deviceWarning');
    if (!warning) return;
    warning.hidden = !text;
    warning.textContent = text || '';
  }

  function normalizeUrl(raw) {
    let value = String(raw || '').trim();
    if (!value) return '';
    // Users type what the device screen shows: 192.168.1.50:8422
    if (!/^https?:\/\//i.test(value)) value = 'http://' + value;
    let parsed;
    try {
      parsed = new URL(value);
    } catch (e) {
      return '';
    }
    if (!parsed.port) parsed.port = DEFAULT_PORT;
    parsed.hash = '';
    parsed.search = '';
    const path = parsed.pathname.replace(/\/+$/, '');
    return parsed.origin + (path && path !== '/' ? path : '');
  }

  function loadSettings() {
    try {
      const saved = JSON.parse(localStorage.getItem(STORAGE_KEY) || '{}');
      state.url = normalizeUrl(saved.url || '');
      state.code = String(saved.code || '').trim();
      state.auto = saved.auto === true;
      state.lastHash = saved.lastHash || '';
      state.lastPush = saved.lastPush || null;
    } catch (e) { /* corrupt storage: start unpaired */ }
  }

  function saveSettings() {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify({
        url: state.url,
        code: state.code,
        auto: state.auto,
        lastHash: state.lastHash,
        lastPush: state.lastPush
      }));
    } catch (e) { /* private mode */ }
  }

  function hashDesign(config) {
    const text = JSON.stringify(config);
    let hash = 5381;
    for (let i = 0; i < text.length; i++) hash = ((hash << 5) + hash + text.charCodeAt(i)) | 0;
    return String(hash >>> 0) + ':' + text.length;
  }

  function currentDesignHash() {
    try {
      return hashDesign(buildEditorConfig());
    } catch (e) {
      return '';
    }
  }

  function isDirty() {
    if (!state.connected || !state.lastHash) return false;
    const hash = currentDesignHash();
    return Boolean(hash && hash !== state.lastHash);
  }

  function refreshDirtyState() {
    if (!state.connected) return;
    setDot(isDirty() ? 'dirty' : 'connected');
    if (isDirty()) setMeta('Unsynced edits — ' + (state.auto ? 'auto-push will send them shortly.' : 'press Push to device.'));
  }

  function fillFields() {
    const url = el('deviceUrl');
    const code = el('deviceCode');
    const auto = el('deviceAutoPush');
    if (url) url.value = state.url.replace(/^https?:\/\//, '');
    if (code) code.value = state.code;
    if (auto) auto.checked = state.auto;
  }

  function captureFields() {
    const urlInput = el('deviceUrl');
    const codeInput = el('deviceCode');
    const autoInput = el('deviceAutoPush');
    const rawUrl = String((urlInput && urlInput.value) || '').trim();
    const normalized = normalizeUrl(rawUrl);
    if (rawUrl && !normalized) {
      state.lastError = 'invalid-url';
      state.url = '';
      updateButtons();
      return false;
    }
    if (normalized) {
      state.url = normalized;
      if (urlInput) urlInput.value = state.url.replace(/^https?:\/\//, '');
    }
    state.code = String((codeInput && codeInput.value) || state.code || '').trim().replace(/\s+/g, '');
    state.auto = Boolean(autoInput && autoInput.checked);
    saveSettings();
    updateButtons();
    return true;
  }

  function updateButtons() {
    const paired = Boolean(state.url);
    const ready = state.connected && Boolean(state.code);
    const connectBtn = el('deviceConnectBtn');
    const pullBtn = el('devicePullBtn');
    const pushBtn = el('devicePushBtn');
    const pageBtn = el('devicePageBtn');
    if (connectBtn) {
      connectBtn.disabled = !paired || state.pushing || state.pulling;
      connectBtn.setAttribute('aria-busy', String(state.pushing || state.pulling));
    }
    if (pullBtn) pullBtn.disabled = !ready || state.pushing || state.pulling;
    if (pushBtn) {
      pushBtn.disabled = !ready || state.pushing || state.pulling;
      pushBtn.setAttribute('aria-busy', String(state.pushing));
    }
    if (pageBtn) pageBtn.disabled = !paired;
  }

  function mixedContentBlocked() {
    return window.location.protocol === 'https:' && /^http:\/\//i.test(state.url);
  }

  async function request(path, options) {
    const opts = Object.assign({}, options || {});
    const controller = typeof AbortController === 'function' ? new AbortController() : null;
    const timer = controller ? setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS) : null;
    const headers = Object.assign({}, opts.headers || {});
    if (state.code) headers['X-Live-Builder-Code'] = state.code;
    try {
      const response = await fetch(state.url + path, Object.assign({}, opts, {
        headers,
        signal: controller ? controller.signal : undefined,
        mode: 'cors',
        cache: 'no-store'
      }));
      return response;
    } finally {
      if (timer) clearTimeout(timer);
    }
  }

  function connectionError(error) {
    if (error && error.name === 'AbortError') return 'The device did not answer within 8 seconds.';
    return 'Cannot reach ' + (state.url || 'the device') + '. Check that JukaHub is running, the address is right, and both devices are on the same Wi-Fi.';
  }

  async function connect(options) {
    const opts = options || {};
    if (captureFields() === false) {
      setState('That address is not valid', 'error');
      setMeta('Use an address like 192.168.1.50:8422, or a full http:// URL.');
      el('deviceUrl')?.focus();
      return false;
    }
    if (!state.url) {
      setState('Enter the device address first', 'error');
      setWarning('');
      return false;
    }
    if (mixedContentBlocked()) {
      setState('Blocked by the browser', 'error');
      setWarning('This page is served over HTTPS, so the browser refuses plain HTTP requests to the handheld. Serve the builder over http://localhost (or use the local copy) to push.');
      return false;
    }
    setState('Connecting to ' + state.url.replace(/^https?:\/\//, '') + '…', 'connecting');
    setWarning('');
    updateButtons();
    try {
      const response = await request('/api/health', { method: 'GET' });
      if (!response.ok) throw new Error('health ' + response.status);
      const info = await response.json();
      if (!info || !info.ok) throw new Error('not a live builder');
      state.connected = true;
      state.info = info;
      state.lastError = '';
      const deviceLabel = info.device || info.app || 'JukaHub';
      setState('Connected to ' + deviceLabel, 'connected');
      setMeta('Schema ' + (info.schemaVersion || '?') + ' · ' + (info.pushes ? info.pushes + ' push' + (info.pushes === 1 ? '' : 'es') + ' so far' : 'no pushes yet') + (state.code ? '' : ' · add the pairing code to push.'));
      updateButtons();
      saveSettings();
      if (!opts.silent) {
        showToast('Connected to ' + deviceLabel, 'success');
        announce('Live builder connected to ' + deviceLabel + '.');
      }
      return true;
    } catch (error) {
      state.connected = false;
      state.info = null;
      state.lastError = String(error && error.message || error);
      setState('Not connected', 'error');
      setMeta(connectionError(error));
      updateButtons();
      if (!opts.silent) showToast(connectionError(error), 'error');
      return false;
    }
  }

  function parseErrorDetails(payload) {
    if (!payload) return '';
    if (Array.isArray(payload.details) && payload.details.length) return payload.details.join(' · ');
    return payload.error || '';
  }

  async function pushConfig(config, options) {
    const opts = options || {};
    if (!state.url) {
      setState('No device paired', 'error');
      if (!opts.silent) showToast('Add the device address first', 'warning');
      return false;
    }
    if (!state.code) {
      setState('Pairing code needed', 'error');
      el('deviceCode')?.focus();
      if (!opts.silent) showToast('Enter the pairing code shown on the handheld', 'warning');
      return false;
    }
    if (mixedContentBlocked()) {
      setState('Blocked by the browser', 'error');
      setWarning('HTTPS pages cannot push to a plain-HTTP device address. Open the builder over http://localhost.');
      return false;
    }

    let design = config;
    if (!design) design = buildEditorConfig();
    const check = validateProject(design);
    if (check.errors > 0) {
      setState('Design has errors', 'error');
      const first = check.issues.find(issue => issue.severity === 'error');
      setMeta('Fix the design check first: ' + (first ? first.message : 'errors found') + '.');
      if (!opts.silent) {
        openValidationPanel(lastImportReport);
        showToast('Fix ' + check.errors + ' error' + (check.errors === 1 ? '' : 's') + ' before pushing', 'error');
      }
      updateButtons();
      return false;
    }

    if (!state.connected && !opts.skipConnect) {
      const ok = await connect({ silent: true });
      if (!ok) return false;
    }

    state.pushing = true;
    setState('Pushing to device…', 'connecting');
    setWarning('');
    updateButtons();
    try {
      const response = await request('/api/design', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(design)
      });
      let payload = null;
      try { payload = await response.json(); } catch (e) { /* non-JSON error page */ }
      if (response.status === 401) {
        state.connected = false;
        setState('Pairing code rejected', 'error');
        setMeta('That code does not match the handheld. Read it from the device status page or the startup message.');
        if (!opts.silent) showToast('Pairing code rejected by the device', 'error');
        return false;
      }
      if (!response.ok) {
        const detail = parseErrorDetails(payload) || ('HTTP ' + response.status);
        setState('Push rejected', 'error');
        setMeta(detail);
        if (!opts.silent) showToast('Device rejected the design: ' + detail, 'error');
        return false;
      }

      state.lastHash = hashDesign(design);
      state.lastPush = payload && payload.savedAt ? payload.savedAt : new Date().toISOString();
      saveSettings();
      setState('Live on the device', 'connected');
      setMeta((payload && payload.scenes ? payload.scenes + ' scenes · ' + payload.elements + ' elements · ' : '') +
        'saved and reloaded at ' + new Date(state.lastPush).toLocaleTimeString() +
        (state.auto ? ' · auto-push on' : ''));
      setDot('connected');
      if (!opts.silent) {
        showToast('Pushed to device — UI reloaded', 'success');
        announce('Design pushed to the handheld and reloaded.');
      } else {
        announce('Auto-pushed to the handheld.');
      }
      if (payload && Array.isArray(payload.warnings) && payload.warnings.length) {
        setWarning(payload.warnings[0]);
      }
      return true;
    } catch (error) {
      setState('Push failed', 'error');
      setMeta(connectionError(error));
      if (!opts.silent) showToast(connectionError(error), 'error');
      return false;
    } finally {
      state.pushing = false;
      updateButtons();
      if (state.connected) setDot(isDirty() ? 'dirty' : 'connected');
    }
  }

  async function push() {
    captureFields();
    return pushConfig(buildEditorConfig());
  }

  async function pull() {
    captureFields();
    if (!state.code) {
      setState('Pairing code needed', 'error');
      el('deviceCode')?.focus();
      showToast('Enter the pairing code shown on the handheld', 'warning');
      return false;
    }
    if (!state.connected) {
      const ok = await connect({ silent: true });
      if (!ok) return false;
    }
    if (!window.confirm('Replace this workspace with the design on the device?')) return false;
    state.pulling = true;
    updateButtons();
    try {
      const response = await request('/api/design', { method: 'GET' });
      if (response.status === 401) {
        setState('Pairing code rejected', 'error');
        showToast('Pairing code rejected by the device', 'error');
        return false;
      }
      if (!response.ok) {
        setState('Pull failed', 'error');
        setMeta('The device returned HTTP ' + response.status + '.');
        return false;
      }
      const raw = await response.json();
      const normalized = normalizeImportedConfig(raw);
      pushUndo('pull from device');
      importedConfigSnapshot = JSON.parse(JSON.stringify(normalized.config));
      loadJukaApp(normalized.config);
      scheduleAutoSave();
      if (typeof refreshReloadButtonState === 'function') refreshReloadButtonState();
      state.lastHash = hashDesign(buildEditorConfig());
      saveSettings();
      setState('Pulled from device', 'connected');
      setMeta(normalized.report.scenes + ' scenes · ' + normalized.report.elements + ' elements loaded into the editor.');
      showToast('Pulled the design from the handheld', 'success');
      announce('Design pulled from the handheld into the editor.');
      closePanel();
      return true;
    } catch (error) {
      setState('Pull failed', 'error');
      setMeta(connectionError(error));
      showToast(connectionError(error), 'error');
      return false;
    } finally {
      state.pulling = false;
      updateButtons();
    }
  }

  function noteEdit() {
    if (!state.auto || !state.connected || !state.url || !state.code) return;
    refreshDirtyState();
    if (autoTimer) clearTimeout(autoTimer);
    autoTimer = setTimeout(() => {
      autoTimer = null;
      if (!state.auto || !state.connected || state.pushing) return;
      const hash = currentDesignHash();
      if (!hash || hash === state.lastHash) return;
      pushConfig(buildEditorConfig(), { silent: true, skipConnect: true });
    }, AUTO_PUSH_DELAY_MS);
  }

  function openPanel() {
    const panel = el('devicePanel');
    if (!panel) return;
    fillFields();
    updateButtons();
    if (state.connected) {
      const info = state.info || {};
      setState('Connected to ' + (info.device || info.app || 'device'), 'connected');
    } else if (state.url) {
      setState('Saved device · not connected', 'offline');
      setMeta('Press Connect to reach ' + state.url.replace(/^https?:\/\//, '') + '.');
    } else {
      setState('Not connected', 'offline');
    }
    setWarning(mixedContentBlocked()
      ? 'This page is served over HTTPS; the browser will block pushes to a plain-HTTP device. Open the builder over http://localhost.'
      : '');
    panel.removeAttribute('hidden');
    panel.setAttribute('aria-hidden', 'false');
    const focusTarget = state.url ? el('devicePushBtn') : el('deviceUrl');
    setTimeout(() => focusTarget?.focus(), 0);
    if (state.url && !state.connected) connect({ silent: true });
  }

  function closePanel() {
    const panel = el('devicePanel');
    if (!panel) return;
    panel.setAttribute('hidden', '');
    panel.setAttribute('aria-hidden', 'true');
    el('deviceBtn')?.focus();
  }

  function init() {
    if (initialized) return;
    initialized = true;
    loadSettings();

    el('deviceBtn')?.addEventListener('click', openPanel);
    document.querySelectorAll('[data-close-device]').forEach(node => node.addEventListener('click', closePanel));
    el('deviceConnectBtn')?.addEventListener('click', () => connect());
    el('devicePushBtn')?.addEventListener('click', () => push());
    el('devicePullBtn')?.addEventListener('click', () => pull());
    el('devicePageBtn')?.addEventListener('click', () => {
      if (state.url) window.open(state.url, '_blank', 'noopener');
    });
    document.addEventListener('keydown', event => {
      if (event.key !== 'Escape') return;
      const panel = el('devicePanel');
      if (panel && !panel.hasAttribute('hidden')) closePanel();
    });
    el('deviceUrl')?.addEventListener('change', () => { captureFields(); });
    el('deviceCode')?.addEventListener('change', () => { captureFields(); });
    el('deviceAutoPush')?.addEventListener('change', () => {
      captureFields();
      if (state.auto && state.connected) {
        showToast('Auto-push on — edits will follow to the device', 'info');
        noteEdit();
      } else if (state.auto) {
        showToast('Auto-push on — connect a device to use it', 'info');
      }
    });
    el('deviceUrl')?.addEventListener('keydown', event => {
      if (event.key === 'Enter') { event.preventDefault(); connect(); }
    });
    el('deviceCode')?.addEventListener('keydown', event => {
      if (event.key === 'Enter') { event.preventDefault(); push(); }
    });

    const params = new URLSearchParams(window.location.search);
    const presetUrl = params.get('device') || params.get('builder');
    const presetCode = params.get('code');
    if (presetUrl) state.url = normalizeUrl(presetUrl) || state.url;
    if (presetCode) state.code = presetCode.trim();
    if (presetUrl || presetCode) saveSettings();
    setDot('offline');

    if (state.url) {
      setTimeout(() => { if (!state.connected) connect({ silent: true }); }, 700);
    }
  }

  window.JukaLiveBuilder = {
    init,
    openPanel,
    closePanel,
    connect,
    push,
    pushConfig,
    pull,
    noteEdit,
    isConnected: () => state.connected,
    isDirty,
    getInfo: () => state.info,
    getState: () => Object.assign({}, state)
  };

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
