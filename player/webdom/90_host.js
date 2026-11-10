// 90_host.js — the prelude's entry points for Go (__jhHost), deleted from the global once Go has them.
//
// webdom_runtime.go calls these in the order a browser goes through a page: boot → nextParserScript/
// execScript until null → ready('interactive') → nextDeferredScript/execScript until null →
// ready('domcontentloaded') → runTask/fetchDone/scriptLoaded until idle → ready('complete') → the loop again →
// serialize → stats. Each call is one task; goja runs its promise jobs before the call returns.

var hostErrors = [];
var hostTextBefore = 0;
var hostTextAfter = 0;

function hostNoteError(msg) {
  if (hostErrors.length < 10) hostErrors.push(str(msg).slice(0, 300));
}

// hostBusy is set while a host call runs and cleared when it returns normally. A call the watchdog
// interrupts never returns into the prelude — goja unwinds it without running finally blocks and drops the
// promise jobs it had queued — so the next call finds the flag still set and repairs what was left half-done.
var hostBusy = false;

// hostRecover undoes an interrupted call: re-entrancy flags on records, the event dispatch stack, the
// error-reporting depth, the custom-element construction state, the timer nesting level, and the two
// schedulers that ran on promise jobs (MutationObserver delivery, storage saves), which would otherwise
// never run again for the rest of the page.
function hostRecover() {
  flagsReset();
  eventsRecover();
  envReporting = 0;
  ceCreateDoc = null;
  ceUpgradeStack.length = 0;
  loopNesting = 0;
  loopChain = null;
  loopFrameChain = null;
  moRecover();
  stoRecover();
}

// hostEnter starts a host call. Every call also clears what only a running script may hold: currentScript
// and the document.write buffer.
function hostEnter() {
  if (hostBusy) hostRecover();
  hostBusy = true;
  if (mainDoc) mainDoc.cur = null;
  scrWrite = null;
}

function hostLeave(v) {
  hostBusy = false;
  return v;
}

var HOST_SKIP_TEXT = { script: 1, style: 1, noscript: 1, template: 1, head: 1, title: 1 };
var HOST_TEXT = /\S/;

// hostTextLength counts the visible text characters of the document, the measure of "did the scripts leave
// the page with content".
function hostTextLength(d) {
  var n = 0;
  if (!d) return 0;
  var r = d.f;
  while (r) {
    if (r.t === 1 && HOST_SKIP_TEXT[r.ln] === 1 && r.ns === 0) {
      r = nextSkipping(r, d);
      continue;
    }
    if (r.t === 3 && HOST_TEXT.test(r.x)) n += r.x.length;
    r = nextNode(r, d);
  }
  return n;
}

function hostNodeCount(d) {
  var n = 0;
  for (var r = d ? d.f : null; r; r = nextNode(r, d)) n++;
  return n;
}

function hostReady(state) {
  hostEnter();
  hostReadyStep(state);
  hostLeave();
}

function hostReadyStep(state) {
  var d = mainDoc;
  if (!d) return;
  if (state === 'interactive') {
    scrParsing = false;
    d.ready = 'interactive';
    fireEvent(wrap(d), 'readystatechange', {}, Event);
  } else if (state === 'domcontentloaded') {
    scrParsing = false;
    fireEvent(wrap(d), 'DOMContentLoaded', { bubbles: true }, Event);
    scrParsedLoads();
  } else if (state === 'complete') {
    // The timer, frame and skipped-time budgets start again here: what a page does at load is usually the
    // work the reader is waiting for, and an interval or poll that ran before it must not have spent it.
    loopResetBudgets();
    d.ready = 'complete';
    fireEvent(wrap(d), 'readystatechange', {}, Event);
    fireEvent(global, 'load', {}, Event);
    fireEvent(global, 'pageshow', { persisted: false }, PageTransitionEvent);
  }
}

defineProperty(global, '__jhHost', {
  configurable: true,
  value: {
    boot: function (treeJSON, url, configJSON) {
      hostEnter();
      var cfg = {};
      try { cfg = JSON.parse(str(configJSON || '{}')) || {}; } catch (e) { cfg = {}; }
      envApplyConfig(cfg);
      bootDocument(treeJSON, str(url));
      envSetDocURL(str(url));
      hostTextBefore = hostTextLength(mainDoc);
      scrBoot();
      return hostLeave(JSON.stringify({ scripts: scrPrefetch.scripts, modules: scrPrefetch.modules }));
    },
    nextParserScript: function () {
      hostEnter();
      return hostLeave(scrNextParser());
    },
    execScript: function (id, code, finalURL, errText) {
      hostEnter();
      scrExec(id, code, finalURL, errText);
      hostLeave();
    },
    nextDeferredScript: function () {
      hostEnter();
      return hostLeave(scrNextDeferred());
    },
    ready: hostReady,
    runTask: function (mayAdvance) {
      hostEnter();
      return hostLeave(loopRunTask(!!mayAdvance));
    },
    fetchDone: function (id, resp) {
      hostEnter();
      netFetchDone(id, resp);
      hostLeave();
    },
    scriptLoaded: function (id, code, finalURL, errText) {
      hostEnter();
      scrLoaded(id, code, finalURL, errText);
      hostLeave();
    },
    serialize: function () {
      hostEnter();
      hostTextAfter = hostTextLength(mainDoc);
      return hostLeave(serializeForRenderer(mainDoc));
    },
    stats: function () {
      return JSON.stringify({
        scripts: scrStats.run, scriptErrors: scrStats.failed, errors: hostErrors, timers: loopTimerRunsTotal,
        fetches: netStats.fetches, nodes: hostNodeCount(mainDoc), textBefore: hostTextBefore, textAfter: hostTextAfter
      });
    }
  }
});

if (testHooks) {
  testHooks.loop = { runTask: loopRunTask, hasWork: loopHasWork, now: loopNow };
  // breakState leaves what a watchdog interrupt leaves (goja skips finally blocks and drops promise jobs), so
  // a test can check that the next host call repairs it.
  testHooks.host = {
    recover: hostRecover,
    busy: function () { return hostBusy; },
    breakState: function () {
      hostBusy = true;
      moScheduled = true;
      envReporting = 1;
      loopNesting = 9;
      if (stoLocal) stoLocal[STO].pending = true;
    }
  };
  testHooks.env = { evalMedia: envEvalMedia, clone: envStructuredClone, parseURL: envParseURL };
  testHooks.scripts = { kind: scrKind, balanced: scrBalanced, stats: scrStats, modules: modRecords, resolve: modResolve };
}
