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
    // The address the code was saved for: another address never gets it.
    codeUrl: '',
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
  // An address a link suggested (?device=…). It only fills in the address
  // field when the panel opens; it is never saved or connected to by itself.
  let suggestedUrl = '';

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
      // Saved before codeUrl existed: the code was saved with that address.
      state.codeUrl = state.code ? normalizeUrl(saved.codeUrl || saved.url || '') : '';
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
        codeUrl: state.codeUrl,
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

  function fillFields() {
    const url = el('deviceUrl');
    const code = el('deviceCode');
    const auto = el('deviceAutoPush');
    if (url) url.value = (suggestedUrl || state.url).replace(/^https?:\/\//, '');
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
      // Nothing is taken from the fields: the saved address, and the code
      // that belongs to it, stay as they were.
      state.lastError = 'invalid-url';
      updateButtons();
      return false;
    }
    const url = normalized || state.url;
    let code = codeInput ? String(codeInput.value || '').trim().replace(/\s+/g, '') : state.code;
    if (url !== state.url) {
      state.connected = false;
      state.info = null;
    }
    // A code belongs to the address it was saved for: another address never
    // inherits it, or a link that names another host would collect it. A code
    // typed now is kept, and one saved before any address goes with the first
    // address the user enters, never with one a link suggested.
    const fromLink = Boolean(suggestedUrl) && url === suggestedUrl;
    if (code && code === state.code && (state.codeUrl ? state.codeUrl !== url : fromLink)) {
      code = '';
      if (codeInput) codeInput.value = '';
    }
    state.url = url;
    if (urlInput && normalized) urlInput.value = url.replace(/^https?:\/\//, '');
    suggestedUrl = '';
    state.code = code;
    state.codeUrl = code ? url : '';
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

  // The one sentence for the one problem: an HTTPS page may not talk to a
  // plain-HTTP handheld. Opening the panel, pressing Connect and pressing Push
  // all used to say it in their own words, so the panel swapped the note for a
  // near-duplicate as soon as it tried to connect. Every path now shows this.
  const MIXED_CONTENT_NOTE = 'This page is served over HTTPS, so the browser blocks plain HTTP requests to the handheld. Open the builder over http://localhost (the local copy) to push.';

  function mixedContentNote() {
    return MIXED_CONTENT_NOTE;
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
    return 'Cannot reach ' + (state.url || 'the device') + '. Check that Live Builder is on (on the handheld, in Settings under Network), the address is right, and both devices are on the same Wi-Fi.';
  }

  // connect reaches the address in the panel's fields, or with { saved: true }
  // the device saved in this browser (the automatic reconnects use that, so
  // nothing connects to an address only a link suggested).
  // The address field holds something that is not an address: say so. The
  // saved device and its code stay as they were.
  function invalidAddress() {
    setState('That address is not valid', 'error');
    setMeta('Use an address like 192.168.1.50:8422, or a full http:// URL.');
    el('deviceUrl')?.focus();
    return false;
  }

  async function connect(options) {
    const opts = options || {};
    if (!opts.saved && captureFields() === false) return invalidAddress();
    if (!state.url) {
      setState('Enter the device address first', 'error');
      setWarning('');
      return false;
    }
    if (mixedContentBlocked()) {
      setState('Blocked by the browser', 'error');
      setWarning(mixedContentNote());
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
      setWarning(mixedContentNote());
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
      const ok = await connect({ silent: true, saved: true });
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
        setMeta('That code does not match the handheld. It is shown on the device in Settings, under Network, Live Builder.');
        if (!opts.silent) showToast('Pairing code rejected by the device', 'error');
        return false;
      }
      if (response.status === 429) {
        // Several wrong codes in a row: the handheld stops comparing codes for
        // a few minutes, so retrying now cannot help.
        setState('Too many wrong codes', 'error');
        setMeta((payload && payload.error) || 'The handheld paused pairing after several wrong codes. Wait a few minutes, then try again.');
        if (!opts.silent) showToast('The handheld paused pairing for a few minutes', 'error');
        return false;
      }
      if (response.status === 409) {
        // A design that needs the owner's approval is already waiting on the
        // handheld; nothing else lands until it is accepted or rejected there.
        setState('A design is waiting on the handheld', 'error');
        setMeta((payload && payload.error) || 'Accept or reject the waiting design on the handheld first.');
        if (!opts.silent) showToast('Accept or reject the waiting design on the handheld first', 'warning');
        return false;
      }
      if (!response.ok) {
        const detail = parseErrorDetails(payload) || ('HTTP ' + response.status);
        setState('Push rejected', 'error');
        setMeta(detail);
        if (!opts.silent) showToast('Device rejected the design: ' + detail, 'error');
        return false;
      }
      if (payload && payload.quarantined) {
        // The design can run programs or change protected settings, so the
        // handheld keeps it until its owner accepts it there. Nothing changed
        // on the device yet, so the edits stay marked as not synced.
        const reasons = Array.isArray(payload.details) ? payload.details : [];
        setState('Waiting for approval on the handheld', 'connected');
        setMeta('Accept it on the handheld to apply it' + (reasons.length
          ? ': ' + reasons[0] + (reasons.length > 1 ? ' (and ' + (reasons.length - 1) + ' more)' : '') + '.'
          : '.'));
        setWarning(payload.message || 'This design waits for approval on the handheld.');
        if (!opts.silent) showToast('The handheld asks before applying this design', 'warning');
        announce('The handheld is asking its owner to approve this design.');
        return true;
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
    if (captureFields() === false) return invalidAddress();
    return pushConfig(buildEditorConfig());
  }

  async function pull() {
    if (captureFields() === false) return invalidAddress();
    if (!state.code) {
      setState('Pairing code needed', 'error');
      el('deviceCode')?.focus();
      showToast('Enter the pairing code shown on the handheld', 'warning');
      return false;
    }
    if (!state.connected) {
      const ok = await connect({ silent: true, saved: true });
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
      if (response.status === 429) {
        setState('Too many wrong codes', 'error');
        showToast('The handheld paused pairing for a few minutes', 'error');
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

  // Called for every edit the builder sees, which during a drag is every mouse
  // move. It used to export and hash the whole design twice per call just to
  // colour the dot; now an edit marks the design unsynced straight away and
  // the comparison with the last push happens once, when the debounce fires,
  // on the same export that is then pushed.
  function noteEdit() {
    if (!state.auto || !state.connected || !state.url || !state.code) return;
    if (!state.pushing) {
      setDot('dirty');
      setMeta('Unsynced edits — auto-push will send them shortly.');
    }
    if (autoTimer) clearTimeout(autoTimer);
    autoTimer = setTimeout(() => {
      autoTimer = null;
      if (!state.auto || !state.connected || state.pushing) return;
      let design;
      try {
        design = buildEditorConfig();
      } catch (e) {
        return;
      }
      if (hashDesign(design) === state.lastHash) {
        // Edited and put back (or undone): nothing to send.
        setDot('connected');
        setMeta('No changes since the last push · auto-push on');
        return;
      }
      pushConfig(design, { silent: true, skipConnect: true });
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
    setWarning(mixedContentBlocked() ? mixedContentNote() : '');
    panel.removeAttribute('hidden');
    panel.setAttribute('aria-hidden', 'false');
    const focusTarget = state.url ? el('devicePushBtn') : el('deviceUrl');
    setTimeout(() => focusTarget?.focus(), 0);
    if (state.url && !state.connected) connect({ silent: true, saved: true });
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

    suggestFromLink(window.location.search);
    setDot('offline');

    if (state.url) {
      setTimeout(() => { if (!state.connected) connect({ silent: true, saved: true }); }, 700);
    }
  }

  // A link may suggest a device address (?device= or ?builder=, as the
  // handheld's own page links here). It only pre-fills the address field
  // when the panel opens: nothing is saved, nothing connects, and the code
  // saved in this browser is not sent there unless the user types it again.
  // A ?code= in a link is ignored.
  function suggestFromLink(search) {
    const params = new URLSearchParams(search || '');
    const url = normalizeUrl(params.get('device') || params.get('builder') || '');
    suggestedUrl = url && url !== state.url ? url : '';
    return suggestedUrl;
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
    suggestFromLink,
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
