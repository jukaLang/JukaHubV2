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
  link picker, choose with the D-pad, and press **A**. Or press **Tab** (a
  keyboard) / **L3** (left stick click on a gamepad) to switch the D-pad from
  scrolling to walking the page's links: Left/Right follow reading order,
  Up/Down follow the rows and pick the nearest column, and **A** opens the
  outlined link. The toolbar's **SCROLL** / **LINKS** chip names the mode you
  are in, so the D-pad's job is readable a frame later instead of living only in
  the toast that announced the switch; the chip is also a tap target, so touch
  can switch modes without the keyboard.
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
* Read the typography and box properties real sheets actually use: `font-size`,
  the `font` shorthand (`font: italic 700 18px/1.8 Georgia, serif` sets the
  size, weight, style and leading in one declaration; the family is the one part
  the reader drops), `font-weight`, `font-style`, `line-height` (number,
  percentage or length), `text-align`, `text-indent`, `text-transform`,
  `text-decoration`, `white-space`, `color`, `background-color`,
  `background-image` gradients, the `padding`/`margin` shorthands plus their
  per-side longhands, the logical spacing forms (`margin-inline`,
  `padding-block`, `margin-inline-start`, ...), `border` shorthands with their
  widths and colours, `border-radius`, `opacity`, and `list-style-type`
  (`decimal`, letters, roman numerals, `disc`, `circle`, `square`, `none`).
* Draw each border on the edge the sheet named: `border-left` and
  `border-right` are separate side bars, `border-top`/`border-bottom` are rules,
  `border` is an outline, and every one of them keeps the declared width (1px
  stays a hairline, 4px stays heavy) and the declared colour, including the
  colour carried by a shorthand (`border-bottom: 3px solid #c00`). An `<hr>`
  keeps its styling too: a coloured, weighted rule rather than the reader's own
  hairline.
* Approximate the properties a row grid cannot honour exactly, rather than drop
  them: a **linear gradient is painted as a real multi-stop ramp** (positions,
  `to top`/`to left` mirroring and angles are honoured; a ramp is drawn band by
  band, up to 96 bands, so a tall hero card stays cheap), while a radial or
  conic gradient — not a ramp — falls back to the average of its stops.
  `opacity` is blended into the colours that get painted, `border-radius`
  rounds the box (clamped to half its shorter side), and `line-height` becomes
  extra row slots so paragraphs get the leading the design asked for.
  `text-align: justify` is drawn left-aligned, because justification needs
  per-word spacing control the row grid does not have.
* Keep the cookies a site hands out, for the rest of the session. See
  [Cookies](#cookies).
* Resolve the value layer most sites build their theme on: custom properties
  (`:root { --brand: #0af }`, `var(--brand, #fff)`), `!important`, and `calc()`
  over `px`, `em` and `rem`. A page whose palette lives in `--custom`
  properties keeps its colours instead of falling back to the reader's paper.
* Apply the stylesheet rules that describe a screen. `@media` blocks are kept
  (a width or feature query is a question about the reader, and the reader is
  the medium that matters) except the ones that only target `print` or
  `speech`: a print sheet hides the navigation with `display: none` and
  repaints the page for paper, which on screen reads as a page with its menu
  missing. `@supports` and `@layer` blocks are kept, because this reader does
  implement the properties they gate.

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

## Cookies

A page load is several requests: the HTML, then its stylesheets, images and
scripts. Many sites only work when those share one cookie store — a consent
answer, an age gate, or a redirect that only sticks once the session cookie is
sent back — so every browser request carries the same jar, and the redirect
hops of one load see the cookies each earlier hop set.

The jar is scoped the way a browser's is: a cookie belongs to the host that set
it (including `Domain=` subdomains, the `Path=` prefix, `Secure` and expiry) and
is never offered to a different site.

### A session survives a relaunch

The jar is saved to `data/browser-cookies.json`, so a page that handed out a
session — a consent answer, an age gate, a "continue as guest" redirect, a
search whose results only render with the cookie — still works after the
launcher restarts. That is not a small thing on this device: the app is quit
every time the handheld sleeps, and retyping anything on a D-pad is miserable.

What the file holds, per cookie: the host it belongs to, an optional `Domain`,
the path, the name and value, the `Secure`/`HttpOnly` flags, the expiry when the
site set one, and the moment the cookie was first stored (`firstSeen`, which the
retention setting below ages it against). Nothing else is written — no page
addresses, no titles, no visit times. The document's other timestamp is
`savedAt`, which says when the file was last written, not when anything was
visited.

| Rule | Why |
|------|-----|
| Written `0600` | The file holds session tokens; nothing else on the card should be able to read it (FAT/exFAT keep no mode bits, so on the handheld's own card this only matters on a shared Linux install) |
| Session cookies (no expiry) are saved too | A session that ends with the process is exactly what this file exists to prevent |
| Expired cookies are never written, and are dropped on load | A cookie that died while the app was closed must not come back |
| A page's sign-out (`Set-Cookie` with a past expiry or `Max-Age=0`) removes it from the file | Otherwise the next launch signs the user back in |
| At most 256 cookies, sorted before the cut | A broken or hostile site cannot grow the file without bound; the same file is rewritten the same way |
| A corrupt file costs the session and nothing else | It is logged and ignored, and the next cookie saves over it |

Only the browser uses the jar — feeds, update checks and the podcast fetcher
have their own clients, so a page's cookie cannot ride along on an unrelated
request. Clearing the page cache (**Clear cache** in Tools) does not touch the
jar; signing out is a deliberate press, below.

### Dropping them after a while

Settings → General → **Saved cookies** cycles a retention window: **Keep** (the
shipped default), 7, 14, 30 or 90 days, then back to Keep. With a window set, a
cookie that was first stored longer ago than that is not restored when the
session is loaded, so a consent gate answered once for a page you will never
open again does not sit on the card indefinitely. The row shows the current
stop and when the session it measures was first stored —
`Cookies · 30d · since Oct 12` — so "30 days" has a reference point instead of
being measured from whenever a page was last visited. The date is the oldest
first-seen stamp still on the card; it is re-read when Settings → General is
entered and after a prune that drops it, and it disappears with the session
itself when you press **Forget now** (the window is shortened to `30d` so the
date fits beside it). The press that changes the window also says how many
cookies it just removed — choosing 7 days prunes the 10-day-old ones
immediately, rather than waiting for the next launch, so the count on the
Browser's Forget chip never promises more than the setting keeps.

Age is measured from when a cookie was **first** stored, not from its last
refresh: a site that re-issues its token on every visit must not look new
forever. A file written before this field existed has no per-cookie stamp, so
nothing in it is dropped on the first launch after the upgrade — its clock
starts then — and the file is rewritten once with the stamps in place. The
setting lives in `jukauser.json` as `cookieRetentionDays`; deleting that key is
the same as choosing **Keep**.

### Forgetting them

The chip carries the count before anything is pressed: **Forget**, or
**Forget 3** while three cookies are saved, capped at **Forget 99+** so the
label always fits its share of the toolbar row. The number is refreshed as the
page is drawn, so a login that just set a cookie appears in the toolbar without
leaving the screen, and the chip falls back to the plain word the moment the jar
is emptied. Nothing is read from the card for this: the count is the jar the
browser is already holding, which is restored from the saved session on first
use.

A count on its own does not say *where* the session is. The strip at the bottom
of the page panel names the hosts: **Saved cookie: youtube.com**, **Saved
cookies: youtube.com, github.com**, and **+N more** after the first three, so a
busy jar stays one line. It reads the same records the count does, sorted by how
many cookies each host holds (ties by name, so the line does not shuffle), and
an empty jar says **No saved cookies** rather than leaving the strip blank —
that is the chip's plain **Forget** explained. A cookie scoped to a domain is
named by that domain, not by the page that set it (a `.google.com` cookie reads
as `google.com`), and a `www.` prefix is dropped. The strip is reserved for the
whole session, so the page does not reflow the moment a login arrives, and
nothing but hosts is stored or shown: no URLs, no page titles.

**Browser → Forget** drops every cookie, in memory and on the card, then
reloads the page so what is on screen stops being the signed-in copy. It asks
twice: the first press arms the chip and a toast names how many cookies would go
("Press Forget again to drop 3 cookies"), and a second press within five seconds
does it. This is the same two-press rule the Wi-Fi picker uses before forgetting
a saved network: losing a login on a handheld means retyping it on a controller.
With no cookies stored the chip says so instead of asking, and once the jar is
empty the file is deleted rather than left behind as a second place to look.

The same drop is one press away from Settings: **Settings → General → Forget
now**, in the bottom-right cluster above **Save**, beside the retention row it
belongs with. It runs the Browser's forget — same two-press window, same toast,
same deletion of the file — so an arm from either place can be completed at the
other, and there is only ever one "was that on purpose" state to reason about.
It exists because the old way to clear a session meant opening the Browser,
finding the toolbar chip and waiting for a page reload, which is a lot of D-pad
travel to undo a login from a menu that already offers to expire it.

### Forgetting one site

A whole-jar Forget is the wrong size for the common case — signing one site out,
or resetting one consent gate, otherwise costs every other login on the card.
**Sites** in the toolbar opens a picker listing the hosts the jar holds, one row
each with its count, plus **Forget all N cookies** at the bottom. The same panel
opens when the strip under the page is tapped, because the strip is already the
line that names those hosts: on the Smart Pro's touchscreen the label and the
way to act on it are the same tap.

A row asks twice, exactly like the chip: the first press arms it and the header
line says what would go (**Press A again to drop the cookies from
youtube.com**), the second press inside the same five seconds drops that site's
cookies and reloads the page. The rest of the jar is untouched — in memory and
in `data/browser-cookies.json`, which is rewritten with what is left, or deleted
when that site was the last one. An arm belongs to one row: arming
`youtube.com`, moving to `github.com` and pressing there arms `github.com`
rather than dropping it. **Forget all** keeps the chip's own confirm window, so
an arm taken on the chip can be finished in the picker and vice versa. **B**
closes the picker without touching anything.

The picker is part of the injected Browser scene like the rest of the toolbar;
the action behind the chip is the `browser_cookie_sites` trigger, so a config
with its own Browser scene can put it on any button.


**Forget** is part of the Browser scene the app injects. A config that ships its
own Browser scene keeps it, so that scene does not get the chip — the action
behind it is the `browser_forget_cookies` trigger, which any button can carry.

A tracking pixel that the ad filter already drops is not fetched at all, cookie
or not, so the count in the toolbar still reflects what was kept off the wire.

## Videos and yt-dlp playback

A `<video>` element or a video-bearing embed (YouTube, Vimeo, Dailymotion,
Twitch, or a direct `.mp4`/`.webm`/`.m3u8`/`.mkv`/`.mov` URL) stops being
skipped markup: the reader reserves a 16:9 play card on the same row grid
images use, with a play glyph and the video's title, and lists it among the
page's links so the D-pad reaches it exactly like an anchor. YouTube embeds
show the video's real thumbnail on the card — the video id maps to a URL on
the image CDN, which the page's own image pass fetches under the same limits;
anything else (and any failed fetch) falls back to the dark chrome card.

Activating the card (tap or A) never navigates: the URL is handed to
`playSmartURL`, which streams direct media files in the embedded player and
sends everything else (YouTube, Twitter/X, TikTok, Instagram, Reddit, Vimeo, ...)
through yt-dlp into the same player. An element with no resolvable source draws
nothing at all, and a structurally non-video iframe is skipped as before.

## Controls

| Input | Action |
|-------|--------|
| D-pad Up / Down | Scroll the page (link mode: nearest link on the row above/below) |
| D-pad Left / Right | Walk the page's links in reading order |
| Tab / L3 | Cycle scroll ⇄ link mode (**SCROLL** / **LINKS** chip shows which) |
| L1 / R1 | Scroll up / down by a page |
| A / Enter | Edit the address bar, or open the outlined link in link mode |
| X / `x` / `l` | Open the link picker |
| B / Esc | Go back to the previous screen (link mode: back to scroll mode) |
| **Filter** chip | Turn ad/tracker filtering off (or back on) for the current site |
| **Forget** chip | Saved-cookie count in its label (capped at 99+); drop every saved cookie and the file on the card (press twice) |
| Cookie strip | Under the page: the hosts the saved cookies belong to, or **No saved cookies** |
| Touch / mouse | Tap the address bar, **GO**, the filter counter, the mode chip, or a link row |

In the link picker: D-pad chooses, **A** or Enter opens, **B** or Esc closes.

## What it cannot do

This is a reading browser, not a full web engine. Its JavaScript support is a
small embedded engine that only exists to splice `document.write` output into the
page; it is not a full JS runtime, so a site whose content is built by a modern
framework stays empty. It does not implement grid or float layouts or submit
forms. Cookies now survive a relaunch (see [Cookies](#cookies)), which is enough
for a consent gate, an age gate or a session redirect — but not for a site whose
login is a form, because posting that form is the part the reader cannot do. A
session is only ever saved if the site handed one out itself. A `calc()` that mixes units the layout has not measured yet
(`calc(100% - 20px)`) is left unresolved, and `vh`/`vw` say nothing about a
reader with no window, so they are ignored rather than guessed. `float`,
`grid` and `columns` still lay out as plain blocks: the reader stacks content
one column deep, so a two-up card grid reads as a list. `letter-spacing` is
ignored (the text is drawn as measured words, not per-glyph advances), a
diagonal gradient is drawn on its dominant axis instead of at its real angle,
and inline `<span>`/`<a>` backgrounds stay flat fills rather than ramps.
`@container` queries are skipped rather than approximated: which container an
`@container` rule is asking about is exactly the structure the reader does not
keep, so applying those rules everywhere would restyle the whole page. A font
family is kept only as far as "the app's one face", so a monospace `font-family`
does not make a code block monospaced.
GitHub issue pages and
release pages work; a site that renders its content only with JavaScript will
show little or no text. The companion app is the way to send a link from a
phone if typing on the handheld is awkward.

If a page fails, the error is shown in the content area. Check Wi-Fi first, then
try the repository URL directly:

```text
https://github.com/jukaLang/JukaHubV2
```
