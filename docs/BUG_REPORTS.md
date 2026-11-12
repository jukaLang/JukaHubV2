# Bug reports from the device

**Report a bug** in the top bar, between Notifications and Trophies (also
**Start → Device → Report a bug**, **Tools → Community & support → Report a
bug**, and Home search), sends a report from the handheld. The **Report a bug**
card in the paired phone companion accepts a report typed on the phone and
sends it through the handheld. Neither route needs a GitHub account on the
device: the report goes to the JukaHub report service, which files an issue on
[JukaHub's GitHub](https://github.com/jukaLang/JukaHubV2/issues), and the
device or phone shows the issue number.

Reports used to end on a QR code that had to be finished on a phone signed in
to GitHub, because no build carried a collector. The phone remains an option
before sending, while a report is queued, or if the service refuses it.

## What happens on Submit

1. You pick a category, type a title and a description, and decide whether the
   redacted diagnostics and the redacted recent log go along (both on by
   default). **Preview** (Y) shows exactly what will be sent and where it goes,
   including the report's reference (`JH-XXXXXX`, kept for the whole draft)
   and its creation time, which the issue's facts table shows too.
2. **Submit** posts the report as JSON to the report service. Nothing has to
   be typed: with no title and no description, the report is the device's
   diagnostics and log under a title such as *Quick report from TrimUI Smart
   Pro* (*Quick crash report …* when a category is picked), so opening Report a
   bug and pressing Submit — or **Y** and then **Send** in the preview, from
   anywhere on the form — is a whole report. With the diagnostics and the log
   both switched off, there is nothing to send until something is typed.
3. The service checks that it looks like a report from the app, scrubs it once
   more, and files a public issue titled `[Bug] <your title> (JH-XXXXXX)`.
4. The device shows **Thank you — report sent** with the issue number and the
   reference, and records the result in Notifications. The sheet also has a QR
   code of the issue for anyone who wants to follow it on a phone; that is
   optional. When a queued report sends later, Notifications shows its issue
   number and the bell gets an unread count.

When the report cannot go out right away, the device says why and keeps it.
The queued sheet has **Use phone** beside **Done**: it opens a QR code to a
prefilled GitHub issue that can be submitted from a signed-in phone. If GitHub
shows its template chooser, choose **Open a blank issue**. The phone
companion also lists saved reports with an **Open on GitHub** link. Using the
phone is optional; the saved report remains in the outbox. The link includes
the report's reference and creation time, which let the service recognize an
issue submitted by phone when it retries. Leave those markers in the GitHub
issue; if GitHub has not indexed it yet when the device retries, a duplicate
issue is still possible.

| Situation | What the device does |
|-----------|----------------------|
| No network | Queues the report (`data/bugreports/outbox`) and sends it on its own when JukaHub is online again. |
| The clock is wrong (a reset to 1970) | Asks you to set the date and time; the report waits and goes afterwards. |
| The firmware cannot check the service's certificate | Saves the report and a readable copy in the Patch reports folder, then tries again on the next start. |
| The report service is not answering (not deployed, a web page, a proxy's refusal, a redirect) | Saves the report on this device and tries again on the next start. |
| The service has no GitHub token, or its token cannot file issues | Saves the report on this device and retries automatically with backoff. The report shortcut shows the queued count. Once a maintainer fixes the service, the report sends without a phone. |
| The service is busy, or it or GitHub fails for a while (429, 5xx — a gateway's own error page included) | Keeps the report and retries, waiting longer after each failure (90 s, doubling, at most 6 h). The first retry also waits, so a failed Submit is not sent again at once. |
| The service refuses the report | On Submit: says why and offers the phone path, with a readable copy saved on the device. From the outbox: retired like a month-old report (next row). |
| A queued report could not be sent for a month | Moves it to `data/bugreports/retired` and saves a readable copy in the Patch reports folder. |

The companion's report form sends the title, description, category, device
model, build information, creation time and reference. It does not include
device diagnostics or logs; use the handheld form if those are needed. Both
forms scrub personal details before sending. A paired phone needs the
companion PIN; the GitHub token stays on the server and is never sent to it.

No report is lost, and none is retried for ever. A queued report only leaves
the outbox once a readable copy of it exists: on a full or read-only card it
stays queued (and is left alone until the next start), and a report that was
sent but whose queued file could not be deleted is not sent again in the same
session.

## What is in a report

The report is the device's `bugrepReport` (player/bugrepsend.go): your
category, title and description, the JukaHub version and release, the device
model, platform, OS and architecture, the time, a reference such as
`JH-7K2Q9M`, and — when you leave the switches on — the redacted diagnostics
report and the last few hundred log lines.

Before anything leaves the device, credentials, account names, absolute paths,
IP and MAC addresses, e-mail addresses and the paths of links (with or without
`https://`, in any letter case) are removed; a link's host stays, so
"`www.example.com/…`" still says which site it was. The service scrubs the
text again (e-mail, IPv4 and MAC addresses, link credentials and paths) and
escapes markdown, so the issue shows your words rather than links, images,
tables or HTML. What GitHub would still turn into a link or a notification —
a bare `https://` address, `www.`, `@name`, `#123` and `GH-123` — gets an
invisible zero-width space that breaks it while the text reads the same: a
report cannot ping anyone, cross-link an issue or carry a clickable link.
Diagnostics and the log go into collapsed code blocks, where nothing renders.

## Automatic informational diagnostics

**Send anonymous diagnostics** is a separate opt-in switch. About every ten
launches while it is on, the device sends a small fixed report to this service.
The service files a public `[Info] Anonymous device diagnostics` issue with
`non-issue` and `from-device` labels when those labels exist and its GitHub
account can apply them. The title and body also clearly say that no bug is
being reported. It contains the build, device model, platform, OS/architecture,
CPU core count, config scene/element/schema counts and safe-mode state. It
contains no log or full diagnostics bundle. A failed report retries on a later
launch with the same reference; turning the switch off deletes the pending
report and launch count. No Discord notification is sent for these issues.

Create the `non-issue` label in the destination repository before enabling
automatic reports if you want the label to appear; GitHub can still file the
issue when labels are unavailable, and the `[Info]` title remains.

## Privacy

- The issue is **public**: anyone can read it and add to it. It is filed by
  the report service's own GitHub account, not under your name.
- Your network address is not part of the report. Netlify, which hosts the
  service, sees the connection the way any website does; the service holds an
  address only in the function's memory (at most a day, usually much less,
  since instances are short-lived) to slow down a flood of reports, and logs
  only the reference, the issue number and error codes.
- Manual bug reports are sent only when you press Submit (or Send in the
  preview), and the preview shows all of it. Automatic informational reports
  require the separate diagnostics switch described above.

## Pointing reports elsewhere

The device picks the first of these that is set and is an `https://` address
(`http://` is accepted for a collector on the same machine, for testing):

1. the `JUKAHUB_BUGREPORT_URL` environment variable;
2. `"bugReportURL"` in the config's custom block;
3. a build flag: `-ldflags "-X main.bugrepEndpoint=https://…"`;
4. the telemetry collector (`-X main.telemetryEndpoint=…` or
   `JUKAHUB_TELEMETRY_URL`);
5. the JukaHub report service, `https://generate.jukalang.com/api/bug-report`.

Only the JukaHub report service is promised to file a public GitHub issue. For
any other collector, the line beside Submit and the preview name its host
("the report collector at reports.example") and promise nothing about what it
does with the report.

A collector receives the report as a JSON `POST` with
`User-Agent: JukaHub-BugReport/<version>` and answers:

| Answer | Meaning on the device |
|--------|-----------------------|
| `2xx`, optionally `{"id", "issue", "url"}` | Sent. `id` replaces the reference, `issue` and `url` (a GitHub issue address) are shown. |
| `404`, `405`, `410`, `501`, a web page (any status), or a `4xx` with no JSON body | No collector here: saved on the device and retried after the next start. |
| `503 {"fallback": "phone"}` or `{"configured": false}` | Service setup is missing: saved on the device and retried automatically. |
| `429`, other `5xx` | Busy or failing: queued and retried later. |
| any other `4xx` with a JSON body | Refused: the reason (`{"error": "…"}`) is shown; a queued report is retired (with a readable copy). |

Only a collector answering in JSON can refuse a report, because a refusal
retires it: a proxy's or a firewall's error page never does. Redirects are
followed only to an address the device would accept as a collector (`https://`,
or `http://` on the same machine) and at most twice; otherwise the redirect
itself counts as "no collector here".

## Running the report service (maintainers)

The service is `netlify/functions/bug-report.mjs`, a Netlify function on the
site that serves the web builder (generate.jukalang.com, deployed from this
repository). Netlify picks functions up from `netlify/functions` on every
deploy; no `netlify.toml` is needed. It has no dependencies.

1. **Make a token.** On GitHub, open your avatar → **Settings** → **Developer
   settings** → **Personal access tokens** → **Fine-grained tokens** →
   **Generate new token**. Choose `jukaLang` as the **Resource owner**, choose
   **Only select repositories** and `JukaHubV2`, then set **Repository
   permissions → Issues → Read and write**. Generate the token and copy it
   immediately; GitHub shows its value once. Ideally use a separate bot
   account, so reports are not filed
   under a maintainer's name. Which token works depends on who owns the
   repository:
   - A **fine-grained** personal access token only reaches repositories of its
     *resource owner*. For an organisation's repository (`jukaLang/JukaHubV2`)
     the token's owner must be the organisation, so a bot account has to be a
     member of it, the organisation may have to allow (and approve)
     fine-grained tokens, and the token needs *Repository access* to the
     repository with **Issues: Read and write**.
   - A **classic** token with the `public_repo` scope works from any account,
     member or not: anyone may open issues on a public repository.

   **Labels** are only applied when the account has at least triage (write)
   access to the repository. Without it GitHub files the issue and silently
   drops the labels (no error), so a non-collaborator bot's reports arrive
   unlabelled — give the bot triage access if you filter by `from-device`.
2. **Add it to Netlify.** In the Netlify site that serves
   `generate.jukalang.com`, open **Project configuration** (called **Site
   configuration** in some views) → **Environment variables** → **Add a
   variable**. Set the key to `JUKAHUB_GITHUB_TOKEN` and paste the token as
   its value. Make it available to **Functions**. Keep the token out of the
   repository, GitHub Actions secrets, the device config and the phone page.
   The service variables are:

   | Variable | Required | Default | Purpose |
   |----------|----------|---------|---------|
   | `JUKAHUB_GITHUB_TOKEN` | yes | — | The token. Without it the service answers 503 `{"fallback": "phone"}` and the device queues the report. |
   | `JUKAHUB_ISSUES_REPO` | no | `jukaLang/JukaHubV2` | Where issues are filed, as `owner/name`. A separate reports repository works. |
   | `JUKAHUB_ISSUE_LABELS` | no | `bug,from-device` | Comma-separated labels (up to five), applied when the token's account has triage access. Set it empty for none. |
   | `JUKAHUB_DIAGNOSTICS_LABELS` | no | `non-issue,from-device` | Labels for automatic informational diagnostics issues. Create them in the destination repository first. |
   | `JUKAHUB_DISCORD_WEBHOOK` | no | — | A Discord webhook that gets a one-line notice with the issue link (mentions disabled, the title's markdown escaped). |

   Make sure the variables are available to Functions (the default scope).
3. **Deploy.** Trigger a new Netlify deploy so the function reads the new
   variable. Updating a GitHub repository secret alone does not configure the
   live function.
4. **Check it.** `curl -i https://generate.jukalang.com/api/bug-report` answers
   `405` with `{"configured": true}` once the token is set. A test report:

   ```sh
   curl -i https://generate.jukalang.com/api/bug-report \
     -H 'Content-Type: application/json' \
     -H 'User-Agent: JukaHub-BugReport/test' \
     -d '{"schema":1,"kind":"bug_report","app":"JukaHub","id":"JH-TEST01","category":"other",
          "title":"Collector test","description":"Please close me.","version":"test",
          "device":"curl","platform":"curl","os":"linux","arch":"amd64",
          "created":"2026-11-10T12:00:00Z"}'
   ```

   It answers `201` with the new issue; close that issue afterwards. A normal
   report from the handheld or phone companion is the simplest end-to-end
   check after configuration.

GitHub Issues is the destination, and Netlify runs the collector. GitHub Pages
cannot run this function or hold its secret. The Netlify setup above is the
shortest route for this repository.

### Running it on your own server instead

The repository also includes `server/bug-report-server.mjs`, which runs the
same collector with Node.js 20 or newer. On a Linux server:

1. Copy or clone this repository to the server and install Node.js 20 or newer.
   No `npm install` is needed for the report service.
2. Set `JUKAHUB_GITHUB_TOKEN` in the service's private environment, using the
   token made in step 1 above. Optionally set `JUKAHUB_ISSUES_REPO` (default:
   `jukaLang/JukaHubV2`). Keep the token in a protected service environment
   file or secret manager, never in a public web directory or committed file.
3. Start `node server/bug-report-server.mjs` under your process manager. It
   listens on `127.0.0.1:8787` by default. `JUKAHUB_REPORT_PORT` changes the
   port; `JUKAHUB_REPORT_HOST` changes the bind address.
4. Put an HTTPS reverse proxy in front of it. For Nginx, add this location to
   the site's existing TLS `server` block:

   ```nginx
   location = /api/bug-report {
       proxy_set_header X-Real-IP $remote_addr;
       proxy_pass http://127.0.0.1:8787;
   }
   ```

   Keep the Node listener on loopback. It uses Nginx's `X-Real-IP` to apply
   per-address rate limits; an internet client cannot supply that header
   directly to the Node listener.
5. Visit `https://YOUR-DOMAIN/api/bug-report`. A `405` response containing
   `"configured":true` confirms the server sees its token. Put the full HTTPS
   URL in the handheld's `JUKAHUB_BUGREPORT_URL` environment variable, or its
   `bugReportURL` custom config value. Restart JukaHub and submit a normal
   report to confirm GitHub receives it. The paired phone companion uses the
   handheld's chosen report URL automatically.

The service answers:

| Status | When |
|--------|------|
| `201 {"id", "issue", "url"}` | Filed: GitHub answered with the new issue's number. |
| `200 {…, "duplicate": true}` | This report was filed already (a retry after a lost answer): an issue in the repository with the same reference in its title and the same creation time in its facts table. |
| `400` / `403` / `413` / `415` / `422` | Not JSON, not from the app, over 256 KB, not JSON content, or not a valid report — or (`422 {"error": "GitHub refused the issue"}`) GitHub refused the issue itself. |
| `429` + `Retry-After` | More than 5 reports from one address in 10 minutes or 20 in a day, more than 60 an hour per instance, or GitHub's own rate limit. |
| `502` | GitHub could not be reached, failed, did not answer in time, or answered without an issue number; the device retries. |
| `503 {"fallback": "phone"}` | No token, a bad `JUKAHUB_ISSUES_REPO`, or a token GitHub refuses (401/403/404/410). |

Labels GitHub refuses do not cost the report: the issue is filed again
without them, when there is time left for a second try (otherwise the device
retries later). All GitHub calls of one report share a budget under Netlify's
10-second limit, and the Discord notice is sent after the device has its
answer. Because the builder publishes the repository root, the function's
source is also reachable as a static file; it holds no secrets.

Tests: `tests/web/bugreport_function.test.mjs` runs the function against a
scripted `fetch` (`cd tests/web && npm test`); `player/bugrep_test.go` covers
the device's side.
