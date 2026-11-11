// netlify/functions/bug-report.mjs — the JukaHub bug-report collector.
//
// Report a bug on the handheld (player/bugrep.go) used to end in a QR code: no
// build carried a collector, so every report had to be finished on a phone,
// signed in to GitHub. This function is that collector. The device POSTs the
// report its preview showed (bugrepReport in player/bugrepsend.go) to
// https://generate.jukalang.com/api/bug-report, and this files it as a GitHub
// issue with the maintainers' token — no phone, no account on the device.
//
// What the device can rely on:
//
//   201 {"id", "issue", "url"}       filed: the reference, issue number, address
//   200 {..., "duplicate": true}     already filed (a retry after a lost answer)
//   400 / 403 / 413 / 415 / 422      refused: sending the same report again
//                                    cannot help (422 also when GitHub itself
//                                    refuses the issue)
//   429 + Retry-After                too many reports from this address; queue
//                                    it and try later
//   502                              GitHub did not take it; queue and retry
//   503 {"fallback": "phone"}        the collector is not set up (no token, a
//                                    bad repository, a token without access):
//                                    use the phone path instead
//
// Defence in depth: the device already removes personal data before anything
// leaves it, but the text is scrubbed again here (e-mail, IPv4 and MAC
// addresses, credentials and paths in links, with or without a scheme), the
// user's words are markdown-escaped so a report cannot carry links, images or
// HTML, and what GitHub would still turn into a link or a notification — a
// bare address, "www.", @mentions, #123 and GH-123 references — gets a
// zero-width space that breaks it. Diagnostics and the log go into collapsed
// code blocks, where nothing renders.
//
// Setup (docs/BUG_REPORTS.md): JUKAHUB_GITHUB_TOKEN in the site's environment
// variables is the only required one; JUKAHUB_ISSUES_REPO, JUKAHUB_ISSUE_LABELS
// and JUKAHUB_DISCORD_WEBHOOK are optional. No dependencies: Node's own fetch.

// ── Limits ─────────────────────────────────────────────────────────────────

// The fields' own caps below bound what a report can hold; this only stops a
// flood. It leaves room for JSON escapes: an encoder may write a character of
// a log as a six-byte \u escape.
const BODY_MAX_BYTES = 256 * 1024;

// The device's caps (bugrepsend.go) are in UTF-8 bytes. A JavaScript string's
// length never exceeds its UTF-8 size, so the same numbers bound it here; the
// slack covers the "… (cut to fit)" marker bugrepClip appends after cutting.
const CUT_SLACK = 32;
const TEXT_LIMITS = {
  title: 120 + CUT_SLACK,
  description: 4000 + CUT_SLACK,
  diagnostics: (16 << 10) + CUT_SLACK,
  log: (48 << 10) + CUT_SLACK,
};

// The device facts: short strings the device always fills in. release is the
// only optional one (omitempty on the device).
const FACT_LIMITS = { version: 64, release: 120, device: 120, platform: 64, os: 32, arch: 32 };

const CATEGORIES = new Set(['crash', 'ui', 'controls', 'media', 'network', 'other']);

// "JH-7K2Q9M" (bugrepNewRef), or "JH-" and six digits when the device had no
// randomness to spare.
const REF_PATTERN = /^JH-[A-Z0-9]{6}$/;

const REPO_PATTERN = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/;

// GitHub refuses an issue body over 65536 characters; this leaves room for the
// headings and the code fences around the parts.
const ISSUE_BODY_MAX = 60000;

// Per address: five reports in ten minutes and twenty a day. Per instance:
// sixty an hour, whoever sends them. A real tester never meets these; a script
// filling the tracker does.
const RATE = {
  shortWindowMs: 10 * 60 * 1000,
  shortMax: 5,
  dayWindowMs: 24 * 60 * 60 * 1000,
  dayMax: 20,
  globalWindowMs: 60 * 60 * 1000,
  globalMax: 60,
};

const DEDUPE_MS = 24 * 60 * 60 * 1000;
const TRACKED_ADDRESSES_MAX = 5000;
const TRACKED_REPORTS_MAX = 1000;

// Netlify stops a synchronous function after 10 s. If that happened after
// GitHub had filed the issue, the device would retry and could file it twice,
// so the GitHub calls share a budget under that: the search gives up early,
// the issue gets most of the time, the label-less second try only runs when
// there is time left for it, and the Discord notice is sent after the answer.
const BUDGET_MS = 9000;
const SEARCH_MS = 2500;
const CREATE_MS = 6000;
const RETRY_MIN_MS = 2500;
const DISCORD_MS = 2000;

const GITHUB_API = 'https://api.github.com';
const USER_AGENT_PREFIX = 'JukaHub-BugReport/';

// ── Text ───────────────────────────────────────────────────────────────────

// A link in any letter case; the host is bounded like a host name, so a long
// run with no slash costs one pass. WWW is the same for a link with no scheme.
const LINK = /\b(https?|rtsp|rtmp|ftp|sftp):\/\/(?:[^/\s"'<>@]{0,256}@)?([^/\s"'<>]{1,253})[^\s"'<>]*/gi;
const WWW = /\b(www\.[^/\s"'<>]{1,253})\/[^\s"'<>]*/gi;
// Bounded like the address itself (64 before the @, 253 after) so a long run
// of letters with no @ costs one pass, not a backtrack from every position.
const EMAIL = /[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9.-]{1,253}\.[A-Za-z]{2,24}/g;
const MAC = /\b[0-9A-Fa-f]{2}(?:[:-][0-9A-Fa-f]{2}){5}\b/g;
const IPV4 = /\b(?:\d{1,3}\.){3}\d{1,3}\b/g;
// The addresses the device keeps too: they name no one.
const PUBLIC_IPS = new Set(['0.0.0.0', '127.0.0.1', '1.1.1.1', '8.8.8.8']);

// scrub removes personal data the device should already have removed: the
// credentials and the path of a link, with or without a scheme (its host
// stays), e-mail, MAC and IPv4 addresses. It is idempotent, so text the device
// scrubbed passes unchanged.
export function scrub(text) {
  return String(text ?? '')
    .replace(LINK, '$1://$2/…')
    .replace(WWW, '$1/…')
    .replace(EMAIL, '[email]')
    .replace(MAC, '[mac]')
    .replace(IPV4, ip => (PUBLIC_IPS.has(ip) ? ip : '[ip]'));
}

// cleanText drops control characters other than newlines and tabs.
function cleanText(text) {
  return String(text ?? '')
    .replace(/\r\n?/g, '\n')
    // eslint-disable-next-line no-control-regex
    .replace(/[\u0000-\u0008\u000b-\u001f\u007f]/g, '');
}

// defuse keeps a report from notifying anyone or linking anywhere. GitHub
// pings "@name", cross-links "#123" and "GH-123", and turns a bare address or
// "www.host" into a link even after markdown escaping; a zero-width space
// after the @, the # and the dash, and inside "://" and "www.", breaks each of
// them while the text still reads the same.
function defuse(text) {
  return text
    .replace(/@(?=[A-Za-z0-9_-])/g, '@\u200b')
    .replace(/#(?=\d)/g, '#\u200b')
    .replace(/\bGH-(?=\d)/gi, m => `${m}\u200b`)
    .replace(/:\/\//g, ':\u200b//')
    .replace(/\bwww\./gi, m => `${m.slice(0, 3)}\u200b.`);
}

// escapeMarkdown makes the user's words render as the words they typed: the
// characters that open links, images, emphasis, code, tables and HTML are
// backslash-escaped, a heading marker at the start of a line too, and
// mentions, references and autolinks are defused.
export function escapeMarkdown(text) {
  const escaped = String(text ?? '')
    .replace(/[\\`*_[\]<>!|~]/g, '\\$&')
    .replace(/^([ \t]*)#/gm, '$1\\#');
  return defuse(escaped);
}

// fenced puts text in a code block whose fence is longer than any run of
// backticks inside it, so nothing in a log can close the block early.
function fenced(text) {
  let longest = 0;
  for (const m of text.matchAll(/`+/g)) longest = Math.max(longest, m[0].length);
  const fence = '`'.repeat(Math.max(3, longest + 1));
  return `${fence}text\n${text}\n${fence}`;
}

function details(summary, text) {
  return `<details>\n<summary>${summary}</summary>\n\n${fenced(text)}\n\n</details>`;
}

function capitalise(word) {
  return word ? word[0].toUpperCase() + word.slice(1) : word;
}

// ── Validation ─────────────────────────────────────────────────────────────

// validateReport checks a decoded body against the device's wire format and
// returns why it is refused, or "" for a report this collector will file.
// Fields it does not know are ignored rather than refused, so a newer device
// still gets through; they are never copied into the issue.
export function validateReport(r) {
  if (!r || typeof r !== 'object' || Array.isArray(r)) return 'the report is not a JSON object';
  if (r.schema !== 1) return 'unknown report schema';
  if (r.kind !== 'bug_report') return 'not a bug report';
  if (r.app !== 'JukaHub') return 'not a JukaHub report';
  if (typeof r.id !== 'string' || !REF_PATTERN.test(r.id)) return 'bad report reference';
  if (typeof r.category !== 'string' || !CATEGORIES.has(r.category)) return 'unknown category';
  for (const [field, max] of Object.entries(TEXT_LIMITS)) {
    const value = r[field];
    if (value === undefined || value === null) continue;
    if (typeof value !== 'string') return `${field} is not text`;
    if (value.length > max) return `${field} is too long`;
  }
  if (!String(r.title ?? '').trim() && !String(r.description ?? '').trim()) {
    return 'the report has neither a title nor a description';
  }
  for (const [field, max] of Object.entries(FACT_LIMITS)) {
    const value = r[field];
    if (value === undefined || value === null) {
      if (field === 'release') continue;
      return `${field} is missing`;
    }
    if (typeof value !== 'string') return `${field} is not text`;
    if (value.length > max) return `${field} is too long`;
  }
  if (typeof r.created !== 'string' || r.created.length > 40 || Number.isNaN(Date.parse(r.created))) {
    return 'bad creation time';
  }
  return '';
}

// ── The issue ──────────────────────────────────────────────────────────────

// issueTitle is "[Bug] <title> (JH-XXXXXX)"; a report with only a description
// is named after its category, the way the phone path names it.
export function issueTitle(r) {
  let title = defuse(scrub(cleanText(r.title)).replace(/\s+/g, ' ').trim());
  if (!title) title = `${capitalise(r.category || 'other')} problem`;
  if (title.length > 120) title = `${title.slice(0, 119).trimEnd()}…`;
  return `[Bug] ${title} (${r.id})`;
}

function factRow(label, value) {
  const cell = escapeMarkdown(scrub(cleanText(value)).replace(/\s+/g, ' ').trim()) || '—';
  return `| ${label} | ${cell} |`;
}

// trimTail keeps the end of a log (the newest lines) within max characters.
function trimTail(text, max) {
  if (text.length <= max) return text;
  const marker = '… (earlier lines cut to fit the issue)\n';
  return marker + text.slice(text.length - Math.max(0, max - marker.length));
}

// trimHead keeps the start of the diagnostics within max characters.
function trimHead(text, max) {
  if (text.length <= max) return text;
  const marker = '\n… (cut to fit the issue)';
  return text.slice(0, Math.max(0, max - marker.length)) + marker;
}

// buildIssue turns a valid report into the issue's title and markdown body. The
// body stays under maxBody characters: the log gives way first (its oldest
// lines), then the diagnostics (their end).
export function buildIssue(r, maxBody = ISSUE_BODY_MAX) {
  const description = scrub(cleanText(r.description)).trim();
  let diagnostics = scrub(cleanText(r.diagnostics)).trim();
  let log = scrub(cleanText(r.log)).trim();
  const version = [r.version, r.release ? `(${r.release})` : ''].filter(Boolean).join(' ');

  const head = [
    description ? escapeMarkdown(description) : '_No description._',
    '',
    '| Field | Value |',
    '|---|---|',
    factRow('Reference', r.id),
    factRow('Category', r.category),
    factRow('JukaHub', version),
    factRow('Device', r.device),
    factRow('Platform', `${r.platform} · ${r.os}/${r.arch}`),
    factRow('Created', r.created),
  ].join('\n');
  const foot = '<sub>Sent from the device through the JukaHub report service. The reporter reviewed ' +
    'exactly this text before sending; names, paths, addresses and keys were removed on the device.</sub>';

  const assemble = () => {
    const parts = [head];
    if (diagnostics) parts.push(details('Diagnostics (redacted)', diagnostics));
    if (log) parts.push(details('Recent log (redacted)', log));
    parts.push(foot);
    return parts.join('\n\n');
  };

  let body = assemble();
  if (body.length > maxBody && log) {
    const room = log.length - (body.length - maxBody) - 64;
    log = room >= 400 ? trimTail(log, room) : '';
    body = assemble();
  }
  if (body.length > maxBody && diagnostics) {
    const room = diagnostics.length - (body.length - maxBody) - 64;
    diagnostics = room >= 400 ? trimHead(diagnostics, room) : '';
    body = assemble();
  }
  if (body.length > maxBody) body = trimHead(body, maxBody);
  return { title: issueTitle(r), body };
}

// ── Rate limit ─────────────────────────────────────────────────────────────

// createLimiter counts accepted reports per address and per instance. take
// records a report and answers 0, or answers how many seconds to wait. An
// instance is short-lived, so this only slows a burst; it is not a quota.
// Only addresses with a report still inside the day's window are kept, and at
// most TRACKED_ADDRESSES_MAX of them (the oldest go first), so a flood of new
// addresses turned away by the instance's cap cannot grow the map.
export function createLimiter(now) {
  const perAddress = new Map();
  let all = [];
  const waitFor = (hits, max, windowMs, t) =>
    hits.length >= max ? hits[hits.length - max] + windowMs - t : 0;
  const keep = (address, hits) => {
    perAddress.delete(address);
    if (hits.length) perAddress.set(address, hits);
    while (perAddress.size > TRACKED_ADDRESSES_MAX) perAddress.delete(perAddress.keys().next().value);
  };
  return {
    take(address) {
      const t = now();
      all = all.filter(x => x > t - RATE.globalWindowMs);
      const hits = (perAddress.get(address) || []).filter(x => x > t - RATE.dayWindowMs);
      const recent = hits.filter(x => x > t - RATE.shortWindowMs);
      const wait = Math.max(
        waitFor(recent, RATE.shortMax, RATE.shortWindowMs, t),
        waitFor(hits, RATE.dayMax, RATE.dayWindowMs, t),
        waitFor(all, RATE.globalMax, RATE.globalWindowMs, t),
      );
      if (wait > 0) {
        keep(address, hits);
        return Math.max(1, Math.ceil(wait / 1000));
      }
      hits.push(t);
      all.push(t);
      keep(address, hits);
      return 0;
    },
    // size is how many addresses are tracked, for the tests.
    size() {
      return perAddress.size;
    },
  };
}

// ── Handler ────────────────────────────────────────────────────────────────

function json(status, payload, headers = {}) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { 'content-type': 'application/json; charset=utf-8', 'cache-control': 'no-store', ...headers },
  });
}

function readEnv(name) {
  const netlify = globalThis.Netlify;
  if (netlify && netlify.env && typeof netlify.env.get === 'function') {
    const value = netlify.env.get(name);
    if (value !== undefined && value !== null && value !== '') return String(value);
  }
  return typeof process !== 'undefined' && process.env ? process.env[name] : undefined;
}

function timeout(ms) {
  return typeof AbortSignal !== 'undefined' && typeof AbortSignal.timeout === 'function'
    ? AbortSignal.timeout(Math.max(1, Math.round(ms)))
    : undefined;
}

// discordText keeps Discord from formatting a title: its markdown characters
// are escaped, so a "[text](address)" in it cannot become a masked link.
function discordText(text) {
  return String(text ?? '').replace(/[\\*_~`|>[\]()]/g, '\\$&');
}

function clientAddress(req, context) {
  if (context && typeof context.ip === 'string' && context.ip) return context.ip;
  const direct = req.headers.get('x-nf-client-connection-ip');
  if (direct) return direct.trim();
  const forwarded = req.headers.get('x-forwarded-for');
  if (forwarded) return forwarded.split(',')[0].trim();
  return 'unknown';
}

function byteLength(text) {
  return typeof Buffer !== 'undefined' ? Buffer.byteLength(text, 'utf8') : new TextEncoder().encode(text).length;
}

function githubHeaders(token) {
  return {
    'Authorization': `Bearer ${token}`,
    'Accept': 'application/vnd.github+json',
    'X-GitHub-Api-Version': '2022-11-28',
    'User-Agent': 'jukahub-bug-collector',
    'Content-Type': 'application/json',
  };
}

// githubRetryAfter reads how long GitHub asked to wait, in seconds, when a 403
// or 429 is its rate limit rather than a permission problem; 0 otherwise.
function githubRetryAfter(resp, now) {
  const after = Number(resp.headers.get('retry-after'));
  if (Number.isFinite(after) && after > 0) return Math.ceil(after);
  if (resp.headers.get('x-ratelimit-remaining') === '0') {
    const reset = Number(resp.headers.get('x-ratelimit-reset'));
    if (Number.isFinite(reset) && reset > 0) return Math.max(1, Math.ceil(reset - now() / 1000));
    return 60;
  }
  return resp.status === 429 ? 60 : 0;
}

// createHandler builds the collector around its environment, its fetch and its
// clock, so the tests can run it without Netlify or GitHub. The default export
// uses the real ones.
export function createHandler(options = {}) {
  const env = options.env || readEnv;
  const fetchImpl = options.fetch || ((...args) => globalThis.fetch(...args));
  const now = options.now || (() => Date.now());
  const limiter = createLimiter(now);
  const filed = new Map(); // report id -> { created, issue, url, at }

  // A report is the same report when its reference and its creation time
  // match: a reference is only six letters, so two reports can share one.
  const remember = (report, issue, url) => {
    const t = now();
    for (const [key, entry] of filed) {
      if (entry.at > t - DEDUPE_MS && filed.size <= TRACKED_REPORTS_MAX) break;
      filed.delete(key);
    }
    filed.delete(report.id);
    filed.set(report.id, { created: report.created, issue, url, at: t });
  };
  const recall = report => {
    const entry = filed.get(report.id);
    return entry && entry.created === report.created && entry.at > now() - DEDUPE_MS ? entry : null;
  };

  // left is how long the next GitHub call may take: at most cap, never past
  // the request's budget, and never less than floor.
  const left = (deadline, cap, floor) => Math.max(floor, Math.min(cap, deadline - now()));

  // findFiled asks GitHub whether the report is already an issue: one in this
  // repository with its reference in the title and its creation time in the
  // facts table (the same reference on someone else's report is not a match).
  // Any failure means "not found": the search only spares a duplicate and must
  // never stand between a report and the tracker.
  const findFiled = async (repo, token, report, deadline) => {
    try {
      const q = encodeURIComponent(`"${report.id}" in:title is:issue repo:${repo}`);
      const resp = await fetchImpl(`${GITHUB_API}/search/issues?q=${q}&per_page=5`, {
        method: 'GET',
        headers: githubHeaders(token),
        signal: timeout(left(deadline, SEARCH_MS, 500)),
      });
      if (!resp.ok) return null;
      const data = await resp.json();
      const createdRow = factRow('Created', report.created);
      const repoTail = `/repos/${repo}`.toLowerCase();
      const hit = (data && Array.isArray(data.items) ? data.items : []).find(item => item &&
        typeof item.title === 'string' && item.title.includes(`(${report.id})`) &&
        typeof item.repository_url === 'string' && item.repository_url.toLowerCase().endsWith(repoTail) &&
        typeof item.body === 'string' && item.body.includes(createdRow));
      return hit && Number.isInteger(hit.number) && hit.number > 0
        ? { issue: hit.number, url: String(hit.html_url || '') }
        : null;
    } catch {
      return null;
    }
  };

  const createIssue = (repo, token, issue, labels, deadline) => fetchImpl(`${GITHUB_API}/repos/${repo}/issues`, {
    method: 'POST',
    headers: githubHeaders(token),
    body: JSON.stringify(labels.length ? { ...issue, labels } : issue),
    signal: timeout(left(deadline, CREATE_MS, 1000)),
  });

  const notifyDiscord = async (webhook, id, title, url) => {
    if (!/^https:\/\/(?:discord|discordapp)\.com\/api\/webhooks\//.test(webhook || '')) return;
    try {
      await fetchImpl(webhook, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          content: `New JukaHub bug report ${id}: ${discordText(title)}\n${url}`.slice(0, 1900),
          allowed_mentions: { parse: [] },
        }),
        signal: timeout(DISCORD_MS),
      });
    } catch {
      // A missed chat notice must not fail a report that is already filed.
    }
  };

  return async function collect(req, context) {
    const deadline = now() + BUDGET_MS;
    const token = String(env('JUKAHUB_GITHUB_TOKEN') || '').trim();
    const repo = String(env('JUKAHUB_ISSUES_REPO') || 'jukaLang/JukaHubV2').trim();
    const configured = token !== '' && REPO_PATTERN.test(repo);

    if (req.method !== 'POST') {
      return json(405, { error: 'POST a JukaHub bug report here', configured }, { 'allow': 'POST' });
    }
    if (!configured) {
      console.error('bug-report: not configured (JUKAHUB_GITHUB_TOKEN missing or JUKAHUB_ISSUES_REPO invalid)');
      return json(503, { error: 'the report service is not set up', fallback: 'phone' });
    }
    if (!(req.headers.get('user-agent') || '').startsWith(USER_AGENT_PREFIX)) {
      return json(403, { error: 'only the JukaHub app sends reports here' });
    }
    if (!(req.headers.get('content-type') || '').toLowerCase().includes('application/json')) {
      return json(415, { error: 'send the report as JSON' });
    }
    const declared = Number(req.headers.get('content-length'));
    if (Number.isFinite(declared) && declared > BODY_MAX_BYTES) {
      return json(413, { error: 'the report is too large' });
    }
    let text;
    try {
      text = await req.text();
    } catch {
      return json(400, { error: 'the report could not be read' });
    }
    if (byteLength(text) > BODY_MAX_BYTES) return json(413, { error: 'the report is too large' });
    let report;
    try {
      report = JSON.parse(text);
    } catch {
      return json(400, { error: 'the report is not valid JSON' });
    }
    const problem = validateReport(report);
    if (problem) {
      console.log(`bug-report: refused (${problem})`);
      return json(422, { error: problem });
    }
    const id = report.id;

    const known = recall(report);
    if (known) return json(200, { id, issue: known.issue, url: known.url, duplicate: true });

    const wait = limiter.take(clientAddress(req, context));
    if (wait > 0) {
      return json(429, { error: 'too many reports from this address; try again later' }, { 'retry-after': String(wait) });
    }

    const existing = await findFiled(repo, token, report, deadline);
    if (existing) {
      remember(report, existing.issue, existing.url);
      return json(200, { id, issue: existing.issue, url: existing.url, duplicate: true });
    }

    const issue = buildIssue(report);
    const labels = String(env('JUKAHUB_ISSUE_LABELS') ?? 'bug,from-device')
      .split(',').map(s => s.trim()).filter(Boolean).slice(0, 5).map(s => s.slice(0, 50));
    let resp;
    try {
      resp = await createIssue(repo, token, issue, labels, deadline);
      if (resp.status === 422 && labels.length) {
        // A label the token may not create is GitHub's usual 422: the report
        // matters more than its labels. Without time left for a second try
        // the device retries later, rather than retiring a report that is
        // only missing its labels.
        if (deadline - now() < RETRY_MIN_MS) {
          console.error(`bug-report: ${id}: GitHub refused the labels and there is no time left to retry`);
          return json(502, { error: 'GitHub did not take the report in time; try again later' });
        }
        resp = await createIssue(repo, token, issue, [], deadline);
      }
    } catch (err) {
      console.error(`bug-report: ${id}: GitHub unreachable (${err && err.name ? err.name : 'error'})`);
      return json(502, { error: 'GitHub could not be reached; try again later' });
    }
    if (resp.status === 201) {
      let data = null;
      try {
        data = await resp.json();
      } catch {
        data = null;
      }
      // Only an issue with a number is filed: the device deletes a report it
      // is told was filed, so anything less is a failure to retry.
      const number = data && Number.isInteger(data.number) && data.number > 0 ? data.number : 0;
      if (number > 0) {
        const url = typeof data.html_url === 'string' ? data.html_url : '';
        remember(report, number, url);
        console.log(`bug-report: ${id}: filed as #${number}`);
        const notice = notifyDiscord(env('JUKAHUB_DISCORD_WEBHOOK'), id, issue.title, url);
        if (context && typeof context.waitUntil === 'function') {
          // The device gets its answer now; the chat notice finishes after.
          context.waitUntil(notice);
        } else {
          await notice;
        }
        return json(201, { id, issue: number, url });
      }
      console.error(`bug-report: ${id}: GitHub answered 201 without an issue number`);
      return json(502, { error: 'GitHub did not confirm the issue; try again later' });
    }
    if (resp.status === 422 || resp.status === 400) {
      // GitHub refuses this issue itself (and not only its labels): sending
      // it again cannot help, so the device keeps a copy instead of blocking
      // every newer report behind it.
      console.error(`bug-report: ${id}: GitHub refused the issue (${resp.status})`);
      return json(422, { error: 'GitHub refused the issue' });
    }
    const retryAfter = (resp.status === 403 || resp.status === 429) ? githubRetryAfter(resp, now) : 0;
    if (retryAfter > 0) {
      console.error(`bug-report: ${id}: GitHub rate limit (${resp.status})`);
      return json(429, { error: 'GitHub is busy; try again later' }, { 'retry-after': String(retryAfter) });
    }
    if ([401, 403, 404, 410].includes(resp.status)) {
      // The token is wrong or cannot write to the repository, or the
      // repository has no issues: retrying cannot fix that, the phone can.
      console.error(`bug-report: ${id}: GitHub refused the token or repository (${resp.status})`);
      return json(503, { error: 'the report service cannot file issues right now', fallback: 'phone' });
    }
    console.error(`bug-report: ${id}: GitHub answered ${resp.status}`);
    return json(502, { error: 'GitHub did not take the report; try again later' });
  };
}

let defaultCollector;

// The deployed function: the real environment, fetch and clock. The
// collector (and its rate limit) lives as long as the function instance.
export default async function collectBugReport(req, context) {
  if (!defaultCollector) defaultCollector = createHandler();
  return defaultCollector(req, context);
}

export const config = { path: '/api/bug-report' };
