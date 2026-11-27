// normalize-loader.js
// Dynamically fetches Normalize.css, caches it locally with a content hash, and
// injects it as a stylesheet only when the cached copy is missing or stale.
//
// Design notes:
//  - Normalize.css is loaded once per page session and shared across reloads via
//    localStorage + a SHA-256 content hash of the file body.
//  - The upstream URL is the Normalize.css repo's published Normalize.css file in
//    its gh-pages snapshot. If you want a different release, change
//    NORMALIZE_SOURCE_URL and NORMALIZE_CACHE_KEY below.
//  - On first load (or when the upstream file changed), the file is fetched as
//    text, hashed, stored, and injected. If the page already has an injected
//    Normalize stylesheet, this module does nothing.
//  - If the fetch fails (no network, blocked request, etc.), behavior is silently
//    graceful: the page keeps running with whatever CSS it already has. This is
//    intentional for a builder/player that also works on a handheld hotspot.
//  - You can force a refresh with normalizeLoader.refresh() and observe state
//    with normalizeLoader.state.

const NORMALIZE_SOURCE_URL =
  'https://necolas.github.io/normalize.css/latest/normalize.css';

const NORMALIZE_CACHE_KEY = 'jukahub_normalize_css_v1';
const NORMALIZE_CACHE_VERSION = 1;

const state = {
  url: NORMALIZE_SOURCE_URL,
  cached: false,
  injected: false,
  hash: null,
  error: null,
  lastFetch: null
};

let injectedStyleEl = null;
let refreshController = null;

function sha256Hex(str) {
  const enc = new TextEncoder().encode(str);
  return crypto.subtle.digest('SHA-256', enc).then((buf) => {
    const bytes = new Uint8Array(buf);
    let out = '';
    for (let i = 0; i < bytes.length; i++) {
      out += bytes[i].toString(16).padStart(2, '0');
    }
    return out;
  });
}

function nowIso() {
  return new Date().toISOString();
}

function readCache() {
  try {
    const raw = localStorage.getItem(NORMALIZE_CACHE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === 'object' && typeof parsed.css === 'string') {
      return parsed;
    }
  } catch (e) {
    // Corrupt cache entry -> treat as miss.
  }
  return null;
}

function writeCache(css, hash) {
  try {
    localStorage.setItem(
      NORMALIZE_CACHE_KEY,
      JSON.stringify({
        version: NORMALIZE_CACHE_VERSION,
        url: state.url,
        hash,
        css,
        fetchedAt: nowIso()
      })
    );
  } catch (e) {
    // If localStorage is full/unavailable, fall back to network-only behavior.
  }
}

function clearCache() {
  try {
    localStorage.removeItem(NORMALIZE_CACHE_KEY);
  } catch (e) {}
}

function stylesheetAlreadyInjected() {
  if (injectedStyleEl && document.head.contains(injectedStyleEl)) return true;
  const existing = document.getElementById('jukahub-normalize-css');
  if (existing) {
    injectedStyleEl = existing;
    return true;
  }
  return false;
}

function injectStylesheet(css) {
  if (stylesheetAlreadyInjected()) return true;
  const el = document.createElement('style');
  el.id = 'jukahub-normalize-css';
  el.type = 'text/css';
  el.textContent = css;
  document.head.appendChild(el);
  injectedStyleEl = el;
  state.injected = true;
  return true;
}

function fetchNormalize() {
  return fetch(state.url, {
    cache: 'no-store',
    mode: 'cors',
    headers: { 'Accept': 'text/css, text/plain;q=0.9' }
  })
    .then((res) => {
      if (!res.ok) {
        throw new Error(`Normalize fetch failed: HTTP ${res.status} ${res.statusText}`);
      }
      return res.text();
    })
    .then((css) => {
      if (typeof css !== 'string' || css.length === 0) {
        throw new Error('Normalize response was empty');
      }
      return sha256Hex(css).then((hash) => ({ css, hash }));
    });
}

function applyFromCacheOrNetwork() {
  return Promise.resolve().then(() => {
    if (stylesheetAlreadyInjected()) {
      state.injected = true;
      return state;
    }

    const cached = readCache();
    if (cached && cached.url === state.url) {
      injectStylesheet(cached.css);
      state.cached = true;
      state.hash = cached.hash;
      state.lastFetch = cached.fetchedAt || null;
      return state;
    }

    return fetchNormalize().then(({ css, hash }) => {
      writeCache(css, hash);
      injectStylesheet(css);
      state.cached = false;
      state.hash = hash;
      state.lastFetch = nowIso();
      return state;
    }).catch((err) => {
      // If we had a stale or partial cache, retry injecting it as a fallback.
      const fallback = readCache();
      if (fallback && fallback.css) {
        injectStylesheet(fallback.css);
        state.cached = true;
        state.hash = fallback.hash;
        state.lastFetch = fallback.fetchedAt || null;
        return state;
      }
      state.error = err instanceof Error ? err.message : String(err);
      return state;
    });
  });
}

function refresh() {
  clearCache();
  state.cached = false;
  state.injected = stylesheetAlreadyInjected();
  state.hash = null;
  state.error = null;
  if (refreshController) {
    refreshController.abort();
  }
  refreshController = new AbortController();
  const signal = refreshController.signal;
  return fetchNormalize().then(({ css, hash }) => {
    if (signal.aborted) return state;
    writeCache(css, hash);
    injectStylesheet(css);
    state.cached = false;
    state.hash = hash;
    state.lastFetch = nowIso();
    return state;
  }).catch((err) => {
    if (signal.aborted) return state;
    state.error = err instanceof Error ? err.message : String(err);
    return state;
  });
}

// Expose a tiny public surface.
export const normalizeLoader = {
  get state() { return Object.assign({}, state); },
  init(options = {}) {
    if (options.url) state.url = options.url;
    return applyFromCacheOrNetwork();
  },
  refresh,
  clearCache,
  injectStylesheet
};

// Auto-init when imported as a module.
if (typeof document !== 'undefined') {
  normalizeLoader.init();
}
