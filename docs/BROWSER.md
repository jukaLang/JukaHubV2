# Browser

The stock TrimUI firmware has no web browser, so links in the app (GitHub
issues, release notes, documentation) had nowhere to open. **Browser** is a
small text browser built into JukaHub. It needs no X11, Wayland, WebKit or
Chromium: it fetches a page over HTTPS, extracts the readable text, and draws it
with the same SDL renderer as the rest of the app.

Open it from **Apps → Browser** or **Start → Browser**.

## What it can do

* Open a URL from the address bar, with or without `https://`. The bar opens
  with the current address selected, so typing replaces it (BACKSPACE clears
  it) instead of appending to the old one.
* Follow links on the page without typing: press **X** (or `L`) to open the
  link picker, choose with the D-pad, and press **A**.
* Go **Back** through visited pages and **Reload** the current one. Back and
  Forward are served from an in-memory page cache (up to eight pages, about
  12 MB of text and images), so returning to a page is instant instead of
  re-downloading it. **Reload** always asks the server again and replaces that
  cached copy.
* Open on the project repository, with quick links for the issue list and a
  new issue form for `jukaLang/JukaHubV2`. The start page is a real page the
  reader can draw; **Search** (Y) opens a DuckDuckGo Lite search instead.
* Draw the first part of a big page instead of refusing it: a page larger than
  8 MiB is cut at the cap and the top of the page says so.
* Read GitHub issues, release notes, README files, documentation sites and
  other server-rendered HTML.
* Keep the page's shape: type scale, colours, boxes and rules, flex rows (cards
  and icon+label pairs side by side), and absolutely positioned or fixed
  layers painted over the text in `z-index` order.
* Resolve the value layer most sites build their theme on: custom properties
  (`:root { --brand: #0af }`, `var(--brand, #fff)`), `!important`, and `calc()`
  over `px`, `em` and `rem`. A page whose palette lives in `--custom`
  properties keeps its colours instead of falling back to the reader's paper.

Pages are capped at 8 MiB and 20 seconds; anything past 8 MiB is drawn partially
from what arrived, with a note at the top of the page. Redirects must stay on
HTTP or HTTPS.

## Ads and trackers

Every subresource the reader would fetch — linked stylesheets, images and
scripts — passes through an ad/tracker filter before a connection is opened, so
a blocked request costs nothing at all: no DNS lookup, no TLS handshake, no
bytes. The filter is two lists:

* **Hosts** that only serve advertising, measurement or session replay:
  DoubleClick, Google Ads/Analytics/GTM, Criteo, Taboola, Outbrain, ad exchanges,
  mobile ad SDKs, Hotjar, Mixpanel and similar. Matching is by host suffix, so
  `ads.example.com` never matches an entry for `example.com`, and
  `notdoubleclick.net` is not mistaken for `doubleclick.net`.
* **URL paths** that only exist to serve ads or pixels (`/ads/`, `/pagead/`,
  `/adserver`, `/gtag/js`, `/gtm.js`, `/tracking/`, `/pixel/`, `/beacon/`,
  `/collect?`). Slashes are part of the pattern, so a genuine `/tracks/` page is
  left alone.

Ad `<script>` tags are also removed from the page source before the script pass,
so they are never downloaded and their inline fallback never runs. The address
bar's rightmost chip reports how many requests the page load skipped
(`12 BLOCKED`, or `0 BLOCKED` when nothing was in the way; it caps at
`99+ BLOCKED`). The plain-text fallback carries the same count
(`Blocked: 3 ad/tracker request(s)`). A page you navigate to directly is never
blocked — that is an explicit request — only its subresources are.

### Turning filtering off for one site

A page that needs the very script the filter drops (a consent wall, a checkout,
a login widget) is not a dead end. The **Filter** chip in the bottom button row,
or a tap on the toolbar counter, turns filtering off for that page's host and
reloads it. The chip then reads **FILTER OFF** in amber, and pressing it again
restores filtering.

The exception is per host (`example.com` and `www.example.com` share one
switch) and is saved in `data/browser-block.json`, so it survives a restart.
Only sites you excepted are listed; every other page stays filtered. The file
is plain JSON if you want to clear the list by hand:

```json
{
  "schemaVersion": 1,
  "disabledSites": ["example.com"]
}
```

This is a curated starter list, not EasyList: it targets the networks that
dominate ad-heavy pages without breaking sites that load their own assets.
First-party ads served from the article's own domain can still get through.
A page's images are given at most six seconds in total, so photo-heavy pages
show their text promptly and draw any image that missed the budget as a plain
placeholder. Laying out a very long article (1,500+ rows) takes a moment once,
then scrolling reuses that layout.

## Controls

| Input | Action |
|-------|--------|
| D-pad Up / Down | Scroll the page |
| L1 / R1 | Scroll up / down by a page |
| A / Enter | Edit the address bar |
| X / `x` / `l` | Open the link picker |
| B / Esc | Go back to the previous screen |
| **Filter** chip | Turn ad/tracker filtering off (or back on) for the current site |
| Touch / mouse | Tap the address bar, **GO**, the filter counter, or a link row |

In the link picker: D-pad chooses, **A** or Enter opens, **B** or Esc closes.

## What it cannot do

This is a reading browser, not a full web engine. Its JavaScript support is a
small embedded engine that only exists to splice `document.write` output into the
page; it is not a full JS runtime, so a site whose content is built by a modern
framework stays empty. It does not implement grid or float layouts, log in to
sites, or submit forms. A `calc()` that mixes units the layout has not measured yet
(`calc(100% - 20px)`) is left unresolved, and `vh`/`vw` say nothing about a
reader with no window, so they are ignored rather than guessed. GitHub issue pages and
release pages work; a site that renders its content only with JavaScript will
show little or no text. The companion app is the way to send a link from a
phone if typing on the handheld is awkward.

If a page fails, the error is shown in the content area. Check Wi-Fi first, then
try the repository URL directly:

```text
https://github.com/jukaLang/JukaHubV2
```
