# Bug reports from the device

**Start → Device → Report a bug** (also **Tools → Community & support → Report
a bug**, and Home search) sends a report straight from the handheld. Nobody
needs a phone, a browser or a GitHub account on the device: the report goes to
the JukaHub report service, which files it as an issue on
[JukaHub's GitHub](https://github.com/jukaLang/JukaHubV2/issues), and the
device shows the issue number.

Reports used to end on a QR code that had to be finished on a phone signed in
to GitHub, because no build carried a collector. The phone is now only the
fallback for a device that cannot send the report itself.

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
   reference. The sheet also has a QR code of the issue for anyone who wants to
   follow it on a phone; that is optional.

When the report cannot go out right away, the device says why and keeps it:

| Situation | What the device does |
|-----------|----------------------|
| No network | Queues the report (`data/bugreports/outbox`) and sends it on its own when JukaHub is online again. |
| The clock is wrong (a reset to 1970) | Asks you to set the date and time; the report waits and goes afterwards. |
| The firmware cannot check the service's certificate | Offers the phone path (QR code + a copy saved in the Patch reports folder). If you already left the screen, the report is queued, a readable copy is saved, and it is tried again on the next start. |
| The report service is not answering (not deployed, not set up, or something else answers at its address — a web page, a proxy's refusal, a redirect) | Offers the phone path, or **Send later**, which keeps the report and tries again on the next start. |
| The service is busy, or it or GitHub fails for a while (429, 5xx — a gateway's own error page included) | Keeps the report and retries, waiting longer after each failure (90 s, doubling, at most 6 h). The first retry also waits, so a failed Submit is not sent again at once. |
| The service refuses the report | On Submit: says why and offers the phone path, with a readable copy saved on the device. From the outbox: retired like a month-old report (next row). |
| A queued report could not be sent for a month | Moves it to `data/bugreports/retired` and saves a readable copy in the Patch reports folder. |

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

## Privacy

- The issue is **public**: anyone can read it and add to it. It is filed by
  the report service's own GitHub account, not under your name.
- Your network address is not part of the report. Netlify, which hosts the
  service, sees the connection the way any website does; the service holds an
  address only in the function's memory (at most a day, usually much less,
  since instances are short-lived) to slow down a flood of reports, and logs
  only the reference, the issue number and error codes.
- Nothing is sent unless you press Submit (or Send in the preview), and the
  preview shows all of it.

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
| `404`, `405`, `410`, `501`, `503`, a web page (any status), `{"fallback": "phone"}`, or a `4xx` with no JSON body | No collector here: phone path or Send later. |
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

1. **Make a token.** Ideally on a separate bot account, so reports are not filed
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
2. **Add it to Netlify.** Site configuration → Environment variables → Add a
   variable, for the generate.jukalang.com site:

   | Variable | Required | Default | Purpose |
   |----------|----------|---------|---------|
   | `JUKAHUB_GITHUB_TOKEN` | yes | — | The token. Without it the service answers 503 `{"fallback": "phone"}`. |
   | `JUKAHUB_ISSUES_REPO` | no | `jukaLang/JukaHubV2` | Where issues are filed, as `owner/name`. A separate reports repository works. |
   | `JUKAHUB_ISSUE_LABELS` | no | `bug,from-device` | Comma-separated labels (up to five), applied when the token's account has triage access. Set it empty for none. |
   | `JUKAHUB_DISCORD_WEBHOOK` | no | — | A Discord webhook that gets a one-line notice with the issue link (mentions disabled, the title's markdown escaped). |

   Make sure the variables are available to Functions (the default scope).
3. **Deploy.** Push to the branch the site builds, or use *Trigger deploy*.
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

   It answers `201` with the new issue; close that issue afterwards.

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
