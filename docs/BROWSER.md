# Browser

The stock TrimUI firmware has no web browser, so links in the app (GitHub
issues, release notes, documentation) had nowhere to open. **Browser** is a
small text browser built into JukaHub. It needs no X11, Wayland, WebKit or
Chromium: it fetches a page over HTTPS, runs the page's own scripts against a
small document model of its own, extracts the readable text, and draws it with
the same SDL renderer as the rest of the app.

Open it from **Apps → Browser** or **Start → Browser**.

## Full screen and the Select menu

The page fills the screen. There is no address bar, toolbar, chip row, top bar
or footer around it: on the Smart Pro's 720-line panel those took nearly half of
the height a page had. Everything they did is in the **Select menu** — press
**Select** (**M** or the Menu key on a keyboard, a right click with a mouse) and
a sheet opens at the right, with the page still in view beside it.

Across its top are **Back**, **Forward**, **Reload** and **Home**. Under them, in
this order: **Go to address**, **Search the web** (with the engine's name),
**Links on this page** (with their count), **Find on this page**, **Zoom**,
**Link mode**, **Save this page**, **Saved pages**, **Open in WPE Browser**
(see [WPE Browser](#wpe-browser)), **Site info**, **Ad &
tracker filter**, **Signed-in sites**, **Forget sign-ins**, **Full-screen
pages**, **Browser settings**, **JukaHub issues** and **Close browser**. The rows
never move: one that cannot act on the page on screen (Back with no history,
Site info with no page) is dimmed rather than removed, so the other rows stay
where your thumb learned them. The menu opens on Back — **Select** then **A**
goes back a page — or on Go to address when there is nothing to go back to. Up and
Down wrap round (Close browser is one press up from the top), L1/R1 jump five
rows, **A** acts and closes the menu, **B** or Select closes it. Two rows change
in place instead: on **Zoom**, Left/Right step the text size and **A** puts it
back to 100%; on **Find**, while a search runs, Left/Right step through its
matches and **A** starts a new one. **Forget sign-ins** asks twice, as the chip
did. A row's own shortcut on the page is named at its right — **Y** to search,
**X** for the links, **L2/R2** to zoom, and **F**, **Tab** and **I** while you
are using a keyboard — and the footer carries the clock and the battery level,
which the full-screen page has no top bar for.

What the toolbar showed is still on the page, in the top-right corner. For a
couple of seconds after a page loads a pill names its site, with a padlock for
the connection and the button that opens the menu; it comes back while the
mouse (or the right-stick pointer) is near the top of the screen, and clicking
it opens the menu. Under it sit the find count while a search runs (click it for
the next match) and **Link mode** while the D-pad walks the page's links (click
it to go back to scrolling). With a keyboard, **Backspace** or **Alt+←** goes
back, **Alt+→** forward and **F5** reloads, as in a desktop browser.

The classic layout is one switch away: **Settings → Browser → Full-screen
pages**, or the **Full-screen pages** row of the menu. Off, the address bar, the
toolbar chips, the cookie strip, the chip row and the shared bars come back as
they were, and Select still opens the menu. The choice is the
`browserFullScreen` key in `jukauser.json` (see
[Configuration](CONFIGURATION.md)).

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
  can switch modes without the keyboard. Full screen, a **Link mode** pill in
  the top-right corner says the same, and the menu's **Link mode** row switches.
* Go **Back** through visited pages and **Reload** the current one. Back and
  Forward are served from an in-memory page cache (up to eight pages, about
  12 MB of text and images), so returning to a page is instant instead of
  re-downloading it. **Reload** always asks the server again and replaces that
  cached copy.
* Open on the home page: out of the box DuckDuckGo Lite
  (`lite.duckduckgo.com/lite/`), whose page is built for a simple browser and
  so stays readable in the text browser, with the search box the first thing
  on screen — or the engine and the page chosen in **Settings → Browser** (see
  [Search engine and home page](#search-engine-and-home-page)). The first open
  in a run always goes there: a page the last run left behind in the saved
  config is not something this session chose. **Home** returns to the home page
  at any time, **Search** (Y) searches with the chosen engine, and **Issues**
  jumps to `jukaLang/JukaHubV2`'s issue list.
* Draw the first part of a big page instead of refusing it: a page larger than
  8 MiB is cut at the cap and the top of the page says so.
* Read GitHub issues, release notes, README files, documentation sites and
  other server-rendered HTML.
* Run the page's own JavaScript, so content a script builds — a framework app,
  a list fetched as JSON, lazy-loaded images, web components — is drawn too.
  See [JavaScript](#javascript).
* Keep the page's shape: type scale, colours, boxes and rules, flex rows and
  simple CSS grids (cards and icon+label pairs side by side), wrapped into
  further rows when needed, and absolutely positioned or fixed layers painted
  over the text in `z-index` order. Flex rows wrap at each card's stated
  width or basis, share spare space according to `flex-grow`, and keep nested
  rows inside their parent cards. Adjacent images directly under a flex row
  occupy separate cells. A card with several paragraphs stays one item.
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
* Apply the stylesheet rules that describe a screen. Large inline sheets are
  read through a bounded 1 MiB prefix of complete rules, so a site that puts
  all its component CSS in one multi-megabyte `<style>` block still contributes
  its first usable styles instead of losing that entire block. Every `<style>` block
  applies to the whole page, as in a browser, together with the linked sheets.
  `@media` width, height, orientation and aspect-ratio queries are answered for
  the handheld's own screen — the same size page scripts see — so a site's
  paired "phone" and "desktop" copies of a block keep exactly one; other
  feature queries apply. Blocks that only target `print` or `speech` are
  dropped: a print sheet hides the navigation with `display: none` and repaints
  the page for paper, which on screen reads as a page with its menu missing.
  `@supports` and `@layer` blocks are kept, because this reader does implement
  the properties they gate.
* Match selectors where it decides what is shown. Attribute selectors
  (`[hidden]`, `[data-theme=dark]`, Vue's `[v-cloak]`, Alpine's `[x-cloak]`) are
  evaluated, and so are the pseudo-classes a static page can answer (`:root`,
  `:link`, `:checked`, `:disabled`, `:not()`, `:is()`, `:target` …). `:hover` and
  `:focus` never hold for styling, but a rule that *reveals* content through
  them (a CSS-only sub-menu or tooltip) still applies, since the handheld has
  no pointer to hover with. Rules for `::before`/`::after` boxes are skipped
  (the reader draws no generated content), and specificity counts as it does in
  CSS. A rule that would hide an element (`display: none`, `visibility:
  hidden`, `opacity: 0`) applies only when its selector certainly matches: the
  reader used to read every `[attribute]` and `:pseudo` selector as a match, so a
  site's `[hidden] { display: none }` or a cloak rule blanked the whole page.
  An element with the `hidden` attribute stays hidden, as in a browser, and a
  tag with a stray quote in it (`<img alt=don't>`) no longer stops the page from
  loading.

* Fold what the page folds. A `<details>` section draws its summary and its
  `[+]` / `[-]` marker, and keeps the body out of the layout until the marker is
  pressed — which is what a browser does, and what makes a GitHub issue whose
  crash log or screenshot sits behind one read as the issue instead of the log.
  The body is parsed and kept, so opening it is a re-measure rather than a
  second download; a section that ships `open` starts expanded, and nesting
  follows the outer section: a closed one keeps an open section inside it
  hidden too.

Pages are capped at 8 MiB and 20 seconds; anything past 8 MiB is drawn partially
from what arrived, with a note at the top of the page.

### Redirects

A redirect reaches the reader in three shapes, and all of them are followed:

* **HTTP 3xx.** The client follows up to five hops, and every hop must stay on
  HTTP or HTTPS: a site cannot send the reader to a scheme this app cannot draw.
  The hops share the load's cookie jar, so a `Set-Cookie` on the first hop is
  already in hand on the next.
* **A page's own `<meta http-equiv="refresh">`.** "This page has moved" stubs and
  locale or login bounces arrive with a 200 and a body, so nothing follows them
  on its own — the reader would be left on the stub, whose telltale link is
  stripped along with the page's scripts. A load follows up to three of these,
  resolving the target against the page it came from and accepting it quoted,
  unquoted, spaced or as `URL=` in any case. While page scripts are on, a
  refresh inside `<noscript>` is not followed, just as a browser that runs
  JavaScript never reads `<noscript>`: that is where sites park the bounce to a
  "turn on JavaScript" or `/nojs` page, and the reader runs the page itself
  instead. With JavaScript off it follows them as before.
* **A script that navigates.** A page whose script sets `location.href`, calls
  `location.replace()`, opens `window.open(…, '_self')`, clicks a link or
  submits a GET form while the page loads goes where the script sent it (see
  [JavaScript](#javascript)), under a refresh's rules: three hops at most, never
  back to an address this load already showed, and only while three seconds of
  the load's budget are left. A site on the internet cannot send the reader to a
  LAN or `localhost` address this way.

Only a real redirect is followed. A refresh that names no target, names a
non-web scheme, asks to reload the same page, or waits longer than two seconds
(a page asking to be refreshed periodically) is left alone; a pair of pages that
point at each other stops at the hop limit instead of loading forever. All the
hops share the load's one 20-second budget, so a slow chain cannot extend it.

When a load ends somewhere other than the address it started from, the address
bar and the current Back entry move to the URL that actually loaded, and the
landing page is cached under it so Back is a map lookup rather than a second
walk through the redirect. The move happens during the render pass, never while
the address bar is being edited, and a correction for a page the reader has
already navigated past is dropped.

## JavaScript

A page's own scripts run before it is drawn, against a document the reader
builds from the page. They can read and change it (`querySelector`,
`innerHTML`, `createElement`, `classList`, `style` …), fetch data, set timers
and define web components, and what they leave behind is what the reader draws:
an article a framework renders into an empty `<div id="app">`, a list fetched
as JSON, lazy-loaded images, a cookie banner a script dismisses, `document.write`
output. The reader used to give scripts a stand-in document whose every lookup
answered `null`, so pages like that came out empty.

### What runs

* Classic scripts in document order — inline ones and downloaded ones, fetched
  side by side while the earlier ones run — then `defer` scripts,
  `DOMContentLoaded`, `async` and script-inserted scripts, `load`, and whatever
  those schedule. `document.write` inserts where the writing script stands, so
  old "write a script tag" loaders still work; a write after the page has loaded
  is ignored, as browsers do for asynchronous scripts.
* `<script type="module">` with `import`/`export`, `import()`, import maps and
  JSON or CSS modules. The embedded engine has no modules of its own, so each
  one is rewritten into a function before it runs; an imported name is a copy
  taken when the importing module starts rather than a live binding, which only
  shows inside an import cycle. `nomodule` scripts are skipped, as in any
  browser with modules.
* Timers and animation frames run on a virtual clock: a page that waits three
  seconds before showing its content costs no time, because the reader skips
  ahead to the next timer whenever nothing else is happening, and `Date` moves
  with it. A timer that asked for more than ten seconds is never skipped to —
  that is a "your session expired" notice or the next slide of a carousel, not
  content — each phase of the load skips at most 15 seconds, and one interval
  (or a timeout that re-arms itself) runs at most 100 times.
* `fetch` and `XMLHttpRequest`; `document.cookie`; `localStorage` and
  `sessionStorage`, kept in memory per site while JukaHub runs and never written
  to the card; `MutationObserver`; `IntersectionObserver`, which reports every
  element as visible — that is what makes lazy images load; `ResizeObserver`;
  custom elements with shadow DOM and slots; `history.pushState`; `URL`,
  `TextEncoder`, `crypto.getRandomValues`; and a stand-in for `Intl`, which the
  engine lacks, that formats numbers and dates the US-English way.
* Layout questions get plausible answers: an element that is shown reports a
  size on screen, so "has it scrolled into view yet?" checks pass, and
  `getComputedStyle` sees inline styles and the page's own `<style>` sheets
  (linked stylesheet files are fetched after the scripts, for the drawing).

A shadow root a component attached is drawn in its element's place, with the
light-DOM children in their slots. The declarative form
(`<template shadowrootmode="open">`) and lazy images that keep their real
address in `data-src`, `data-srcset` or one of their cousins behind a
placeholder `src` are unfolded even on a page without scripts, as long as
JavaScript is on.

### Limits

Everything is bounded for the handheld's 1 GB and four slow cores:

| Limit | Value | Past it |
|-------|-------|---------|
| The whole script pass | 10 s, network waits included | the page is drawn as the scripts left it; most pages go idle much sooner and stop there |
| One script or task | 6 s | that script is stopped and the page goes on |
| A slow request | 5 s | the pass stops waiting for it, so a long-poll that never answers cannot hold the page (script downloads are waited for until the pass ends) |
| One regular-expression match | 2 s | the match throws a `RangeError` instead of freezing the load |
| Memory | 192 MiB of growth | the page is drawn as it arrived |
| Page size | 4 MiB, 60 000 nodes | the old `document.write`-only pass runs instead |
| Scripts | 256 runs, 6 MiB of code | further scripts are skipped |
| Requests | 160 per page, 24 MiB in all, 6 at a time, 15 s and 4 MiB each (3 MiB for a script) | the request fails as a network error |
| Pages running scripts | 2 | opening a new page stops the scripts of the page you left |

A page that arrived with a fair amount of text and lost more than two thirds of
it to its scripts is drawn as it arrived: a script that tears down the server's
content and fails to rebuild it should not cost you the article. When the pass
cannot start at all, the old `document.write`-only pass runs instead, so a page
that defeats the new engine still shows what it showed before.

### What a page cannot do

The reader runs other people's code on whatever network the handheld is on, so
page scripts are kept on a short leash:

* **No local network from a public site.** A page from the internet cannot make
  requests to this device — where JukaHub's own Live Builder and companion
  services listen — or to the LAN (`192.168.x.x`, `10.x.x.x`, `.local` and
  `.lan` names …), and cannot navigate there either. The address is checked
  when the connection opens, so a site whose name starts resolving to a LAN
  address mid-visit gains nothing. A page opened from the LAN may reach the
  LAN; only a page on `localhost` may reach `localhost`.
* **Cookies stay with their site.** A script's own requests carry cookies only
  for the page's own site, the way browsers treat `SameSite=Lax` cookies; a
  response from another origin reaches the script only when that server allows
  it (CORS), and `Set-Cookie` headers never do. `document.cookie` leaves out
  `HttpOnly` cookies and cannot overwrite them, only an https page can set a
  `Secure` cookie, and an https page cannot load anything over plain http.
* **No workers, sockets or databases.** Web Workers, service workers, IndexedDB
  and the Cache API are absent, so pages that check for them take their
  fallback; `WebSocket` and `EventSource` exist but never connect; `<canvas>`
  has no drawing context; media elements refuse to play (videos open in Media
  instead, see [Videos and yt-dlp playback](#videos-and-yt-dlp-playback)); and
  permission requests (notifications, location) are answered "no".
* Every locale formats as US English, and the only real time zones are UTC, the
  device's own and the fixed `Etc/GMT±N` ones.
* Sites that check whether a full browser is visiting — Cloudflare's "checking
  your browser", Google Search (see [Google needs a full
  web browser](#google-needs-a-full-web-browser)) and other bot walls — still stop at
  their check.

The ad and tracker filter applies to everything scripts request (see
[Ads and trackers](#ads-and-trackers)).

### Turning it off

**Settings → Browser → JavaScript** switches page scripts off and on. Off,
every page is drawn as its server sent it — which is also the quickest way to
tell whether something odd on a page came from its scripts: turn JavaScript off
and press **Reload** (Back and Forward show the copy that was cached when the
page loaded). The change applies from the next page load. The setting is
`browserJavaScript` in `jukauser.json` (see [Configuration](CONFIGURATION.md)).

With debug logging on (the default on a PC; `JUKAHUB_LOG_LEVEL=debug` on the
handheld), every page that ran scripts leaves a summary line in `logs.txt`,

```text
scripts on https://example.com/news: 14 run (1 failed), 212 tasks, 37 timers, 6 requests (180 KiB, 2 blocked), 2311 nodes, 640ms
```

followed by its first few script errors (`script error on …`), console output
(`js error: …`, at most 40 lines a page) and the reason when the pass stopped
early, took the old path, or a script navigation was not followed. Addresses in
these lines never include the query string, where tokens and search terms live.

## Search engine and home page

**Settings → Browser** chooses where a search goes and which page **Home**
opens. A change applies at once, everywhere a search starts: the address bar
(words with a space, or a word with no dot, are a search), the **Search** chip
and **Y** (whose keyboard names the engine: "Search Google"), the **Search the
web** row in Home search (its second line names the engine too), and the
**Search the web** button on the "Site not found" card.

| Engine | Results address | Without JavaScript |
|--------|-----------------|--------------------|
| DuckDuckGo (the default) | `lite.duckduckgo.com/lite/?q=` | Results: the Lite page is made for simple browsers |
| Startpage | `www.startpage.com/sp/search?query=` | May show a verification challenge, including on its home page |
| Google | `www.google.com/search?q=` | No results — see [Google needs a full web browser](#google-needs-a-full-web-browser) |
| Bing | `www.bing.com/search?q=` | Results; its links are unwrapped (see [Result links](#result-links)) |
| Brave Search | `search.brave.com/search?q=` | Results |
| Yahoo | `search.yahoo.com/search?p=` | Results |
| Mojeek | `www.mojeek.com/search?q=` | Results |
| Wikipedia | `en.m.wikipedia.org/w/index.php?search=` | Opens the article when a title matches |
| Custom | the address you type | Whatever that site serves without scripts |

Search engines change what they send a small browser without notice. JukaHub
recognizes Startpage's current verification page and offers Brave Search or a
full browser instead of displaying a page stuck at “Verifying your request”.
DuckDuckGo and Mojeek can also show puzzles after many searches from one
address; the column is what each sent when this was written, not a promise.

The **Search engine** row steps through the engines with Left/Right; **A** opens
the list with a line about each, plus **Custom address…**, which opens the
keyboard on a results address of your own: write `%s` where the words go
(`https://searx.example/search?q=%s`), or end it with `=` and the words are
added at the end; the strip above the keys offers `https://`, `www.` and `.com`.
A custom engine is named after its site's host. The address counts when you
press **Save**, and only if it is usable — `http` or `https`, a real host (one
with a dot, an IP address or `localhost`) and a place for the words. Anything
else is refused with a note and the engine stays what it was; **B** leaves it
untouched too. A custom address that stops being usable (edited by hand in
`jukauser.json`) sends searches to DuckDuckGo Lite, and the row says so.

**Home page** follows the search engine out of the box: its start page, so the
DuckDuckGo default makes **Home** open `lite.duckduckgo.com/lite/`, and choosing
Google makes it open google.com. The row's list also offers the
page the Browser has open, every saved page, and **Type an address…**. **Y** in
**Saved pages** (the **Marks** chip) makes the highlighted page home without
leaving the Browser, and that page's row says **Home** beside its date. A typed
address opens holding the current home page, selected, so the first key typed
replaces it; it applies on **Save** only, and **B** keeps the old one. Without a
scheme it gains `https://`; text that is not a web address (a single word,
another scheme) is refused with a note rather than turned into a search. The
last address typed stays in the list and among the row's Left/Right stops after
another choice, so going back to it needs no retyping. Leaving the field empty
and pressing **Save** returns Home to the engine's start page.

The values live in `jukauser.json` as `browserSearchEngine`,
`browserSearchCustom`, `browserHomePage` and `browserHomeTyped` (see
[Configuration](CONFIGURATION.md)).

### Startpage verification

Startpage currently returns an Anubis verification challenge to JukaHub's
reader for its home page and results pages. The challenge needs a full browser
and its own verification process; JukaHub cannot render search results from
that response. The reader detects it before running page scripts and shows a
card instead. **Open Brave Search** opens Brave's start page from a challenged
Startpage home page; **Search on Brave Search** carries over the words from a
challenged Startpage search. **Always use Brave Search** changes the search
engine. A PC can open Startpage in its web browser; a handheld can try WPE
Browser when installed. Reload asks Startpage again in case its response
changes.

### Google needs a full web browser

Since January 2025 Google's results page is not a page of results but a robot
check (Google calls it SearchGuard): a script that inspects the browser running
it, built to admit full desktop and phone browsers and turn away everything
else. A browser without JavaScript is sent to "turn on JavaScript"; one that
does run scripts but is not a full browser — scrapers, headless tools, and this
reader's small engine (see [JavaScript](#javascript)) — fails the check the same
way, or is sent to the "unusual traffic" puzzle. Passing it would mean
disguising the reader as Chrome, which is exactly what the check exists to
catch, so the reader does not try. It recognises Google's answer before running
any of its scripts — they would only spend the page's script budget to end on
the same wall — and shows a card instead:

* On the handheld, **Search on Brave Search** runs the same words there;
  **Always use Brave Search** also makes it the search engine; **Change engine**
  opens Settings → Browser.
* On a PC, **Open in web browser** hands the search to the desktop's own
  browser, which runs Google's scripts; **Search on Brave Search** and **Change
  engine** follow it.
* On a handheld with [WPE Browser](#wpe-browser) installed, **Open in WPE
  Browser** comes first and tries the search there;
  **Search on Brave Search** and **Change engine** follow it. Where WPE Browser can
  be installed but is not, **Get WPE Browser** takes Change engine's place.

Google stays on the list because its home page loads (so it works as a home
page) and because the PC path makes it useful there. The card is never cached:
**Reload** asks Google again, should that ever change.

### Result links

Search engines wrap their result links in click-tracking hops. Before a link is
loaded, the reader reads the destination out of DuckDuckGo's `/l/?uddg=`,
Google's `/url?q=`, Bing's `/ck/a?u=a1…` (the address in base64) and Yahoo's
`…/RU=…/RK=…`, and opens the page itself: one request sooner, and the engine is
not told which result was chosen. Bing's hop is a script page this reader could
not follow at all, so on Bing this is what makes a result open. A destination
that is not an `http` or `https` page with a host (a `javascript:` link) is not
followed, and a wrapped YouTube link still plays in Media.

## WPE Browser

Some sites only work in a full browser: Google's robot check, web apps, sites
built entirely in script, and large news sites whose layouts use more CSS than
the text reader can paint. On the Smart Pro, Brick and Brick Pro there is one —
[WPE Browser](https://github.com/matweew/wpe-tsp), a community build of WPE
WebKit for these handhelds with GPU rendering, hardware video and a gamepad
pointer. JukaHub does not try to be it; it hands pages to it.

* **Open in WPE Browser** in the Select menu opens the page on screen there.
  JukaHub steps aside the way it does for a game: WPE Browser has the screen and
  the buttons until you quit it, and then you are back in the reader on the same
  page. Before it starts, JukaHub lets go of everything it can rebuild (cached
  text and icon textures, the back/forward page cache, a page script still
  running), because WebKit wants most of the handheld's 1 GB of memory — so Back
  loads the previous page again afterwards.
* On the [Google card](#google-needs-a-full-web-browser), **Open in WPE Browser**
  comes first and runs the search there.
* Only web addresses are handed over (`http://` or `https://` with a host, no
  user name or password in them); anything else opens WPE Browser on its own
  start page.

WPE Browser has to live in exactly `Apps/WPE` on the SD card
(`/mnt/SDCARD/Apps/WPE`). JukaHub recognises it there by its `launch.sh` and
starts that script with the page's address as its first argument, with the
environment the stock launcher would have given it (not JukaHub's own library
path).

### Getting and updating it

Where WPE Browser is not installed, the menu row reads **Get WPE Browser** and
the Google card offers it too. **Settings → Browser → WPE Browser** (handheld
only) shows the installed version and installs or updates it; when a newer
release is out it reads **Update: v…** (JukaHub asks GitHub at most every six
hours, and only while the row, the menu or the Google card is on screen).

The install takes the latest release from GitHub and refuses a package without
a SHA-256 checksum. It needs about three times the package's size free on the
card (the download and the unpacked app: several hundred megabytes), streams the
download to the card with progress on the install bar — cancel it there, and the
next try continues where it stopped — unpacks it in `Apps/.wpe-install`, and
moves the app into `Apps/WPE`. An update keeps `settings.conf` and everything WPE
Browser or you added to its folder (favorites, data); the old folder is removed
only once the new one is in place, and nothing is replaced while WPE Browser (or
a game) is running. The installed release is recorded in
`Apps/WPE/.jukahub-release` and the package's file list in
`Apps/WPE/.jukahub-files`. A WPE Browser you copied over by hand opens the same
way; it has no recorded release, so JukaHub offers no update for it until you
update it once from Settings.

On a PC none of this is shown: the menu row is **Open in web browser**, which
hands the page to the desktop's own browser.

## Ads and trackers

Every subresource the reader would fetch — linked stylesheets, images and
scripts — passes through an ad/tracker filter before a connection is opened, so
a blocked request costs nothing at all: no DNS lookup, no TLS handshake, no
bytes. The filter is two lists:

* **Hosts** that only serve advertising, measurement or session replay:
  DoubleClick, Google Ads/Analytics/GTM, Criteo, Taboola, Outbrain, ad exchanges,
  mobile ad SDKs, Hotjar, Mixpanel and similar — about 370 of them: JukaHub's
  own curated list of ad exchanges and ad servers, native-ad and recommendation
  widgets, pop-under and affiliate networks, data brokers, attribution SDKs,
  analytics, session replay, A/B testing and the big regional networks.
  Matching is by host suffix on dot boundaries, so an entry for
  `doubleclick.net` covers `ads.g.doubleclick.net`, and `notdoubleclick.net` is
  not mistaken for it.
* **URL paths** that only exist to serve ads or pixels (`/ads/`, `/pagead/`,
  `/adserver`, `/gtag/js`, `/gtm.js`, `/tracking/`, `/pixel/`, `/beacon/`,
  `/collect?`). Slashes are part of the pattern, so a genuine `/tracks/` page is
  left alone.

Ad `<script>` tags are also removed from the page source before the script pass,
so they are never downloaded and their inline fallback never runs. What a
page's scripts request on their own — `fetch`, `XMLHttpRequest`, the scripts
and modules they load — passes the same filter, on every redirect hop. The
address bar's rightmost chip reports how many requests the page load skipped
(`12 BLOCKED`, or `0 BLOCKED` when nothing was in the way; it caps at
`99+ BLOCKED`). The plain-text fallback carries the same count
(`Blocked: 3 ad/tracker request(s)`). A page you navigate to directly is never
blocked — that is an explicit request — only its subresources are.

A listed host is blocked only on someone else's page. On its own site — reading
criteo.com, or twitter.com loading analytics.twitter.com — its files load as
the site's own. The URL-path patterns apply everywhere, so an `/ads/` folder is
blocked on any site.

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

**Site info** (the Select menu, `I` on a keyboard, or the padlock) shows the
page's count on a line of its own — "14 ads and trackers blocked" — with **Allow
ads on this site** (**X**) beside it: the same per-site exception, and the page
reloads under it. On an excepted site it reads **Block ads on this site**.

### Turning filtering off everywhere

**Settings → Browser → Block ads and trackers** (on by default) turns the filter
off for every site: pages load every subresource they ask for, the counter reads
**FILTER OFF**, and the per-site switches have nothing to do until it is back
on. The setting is the `browserBlockAds` key in `jukauser.json` (see
[Configuration](CONFIGURATION.md)).

This is a curated list, not EasyList (whose licence and cosmetic rules do not
fit a reader): it targets the networks that dominate ad-heavy pages without
breaking sites that load their own assets — no CDNs, font or library hosts, or
video hosts are on it. Ads a site serves from its own domain can still get
through.
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

Page scripts see the jar through `document.cookie`, without the `HttpOnly`
cookies a script must never read, and they cannot overwrite those either. The
requests a script makes carry cookies only for the page's own site (see
[JavaScript](#javascript)).

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

Both numbers — when the session began and the window it is kept under — are
written into the diagnostics bundle (**Patch → Export diagnostics**) as a
`Saved cookies` section, so a bug report about being signed out arrives with
the date (with its age in days) and the policy that pruned it instead of a
description of them.

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
each with its count and its own session start — `2 cookies · since Oct 12`, the
oldest first-seen stamp among that host's cookies alone — so the age of one
site's login is visible before the press that forgets it. (The Settings row
quotes the same kind of stamp for the jar as a whole; this is that date per
host.) A host with no stamp shows its count alone rather than a date it does
not know. **Forget all N cookies** sits at the bottom, without a date: it is
not a site, and the whole jar's start is the Settings row's to quote. The same
panel opens when the strip under the page is tapped, because the strip is
already the line that names those hosts: on the Smart Pro's touchscreen the
label and the way to act on it are the same tap.

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

## Saved pages

A page you were sent back to — a support link in an issue, a release-notes page, a
doc someone pasted in chat — is otherwise a full retyping session on a D-pad to
reach a second time. **Save** puts the page on screen on the saved list, named
from the page's own title (or its host when the page had none). When the page is
already on the list, the chip reads **Saved** instead, and pressing it again
refreshes the title and moves the entry back to the top rather than adding a
second copy of it.

**Marks** opens that list and carries its size in the label (`Marks 3`), so a
page saved from a link is visible on the chip that opens it. In the panel the
D-pad chooses, **A** opens, **X** removes one entry, **Y** makes it the home
page (its row then says **Home** beside the date; see
[Search engine and home page](#search-engine-and-home-page)), and **B** closes
it. Saved pages are the user's own data, so nothing ages them out or prunes them
behind your back; the only thing that removes one is a press.

The list lives in `data/browser-bookmarks.json`, written through the durable
path (temp file, fsync, rename) with the previous version kept as `.bak`, so a
power cut mid-write cannot leave half a document and a damaged file comes back
as the last good list. Clearing the last bookmark deletes the backup with it. It
is capped at 64 entries, newest first, so a runaway save cannot grow the file
without bound.

## Finding text on a page

On a five-inch screen a long issue or release page is a minute of paging to
reach one paragraph. **Find** (or `f` on a keyboard) opens the keyboard on its
own buffer — the address bar and the web search are left alone — and jumps the
page to the first line that contains what you typed. `n` and `p` step to the
next and previous match, wrapping at both ends; **Esc** clears the search before
it means "go back", so the press that dismisses the highlight does not also
leave the browser.

Matching is per **rendered line**, because that is what the reader holds: the
text is already wrapped, so a phrase that spans a line break is not found. A
query that matches nothing says so instead of silently doing nothing, and an
empty query clears the search.

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

## Forms

The reader used to drop every `<input>` as a void tag, so a search page came out
with no box to type into — the one thing a reader needs from DuckDuckGo Lite.
A form is now content: each control is drawn where it sits in the page, listed
among the page's links so the D-pad and the link picker reach it, and it does
what its kind says when it is activated.

| Control | Drawn as | A / tap does |
|---------|----------|--------------|
| `<input type=text>` and the text-ish types (search, email, url, tel, number) | a filled field chip showing its value, else its placeholder dimmed toward the page, else **Tap to type** | opens the keyboard on that field and submits what is typed |
| `<input type=password>` | the same chip, with the value drawn as dots (it is still what gets sent) | the same |
| `<textarea>` | a field chip with the text between its tags | same, and the value it sends is the field's own text |
| `<select>` | one chip showing the chosen option's label, with ▼ | steps to the next option in place (wrapping), so the list needs no keyboard and no dropdown |
| `<input type=checkbox>` / `radio` | a drawn box, □ or ■ (○ or ●), beside the label the page already had | toggles in place; a radio clears the rest of its group |
| `<input type=submit>` / `<button type=submit>` | its label, as a button chip | sends the form; on an empty form it opens the first field instead of sending an empty search |
| `<input type=hidden>` | nothing — but it is still submitted | — |
| `<input type=file>`, colour, range | nothing | — |

The symbols are the ones the bundled face actually has: the ballot box and the
circled dot (U+2610, U+25C9) are not in Inter, and the reader's font sanitiser
would replace each with an ellipsis — a tick box would have drawn "…".

What a form sends is every named control in it — including its hidden inputs
(the `ie=utf-8`, site-key and locale constants a search handler expects) — plus
the button's own name when the button has one. Two forms on one page stay two
forms even when they point at the same action. A GET form sends those fields in
the address, as search pages expect. A form with `method=post` sends them in the
request body as `application/x-www-form-urlencoded`, or as text parts when it
declares `multipart/form-data`. The response replaces the page, and cookies set
by a redirect are kept for the landing page. A POST body is never stored in the
address bar, history or page cache. File inputs are still not available, so a
form that requires an uploaded file cannot be completed here.

On a POST form, filling a field leaves it on the page so another field can be
filled; press its submit button to send. This is how a username and password can
be entered together. A password field on a GET form is refused because its value
would enter the address. A password form targeting plain HTTP is refused because
it would send the value without encryption.

A field opens holding whatever the page prefilled, **selected** the way the
address bar is: the first character typed replaces it, so correcting a prefilled
search is one press rather than a hold-to-delete. Typing goes into its own
buffer (`browser_form_value`), so filling a field cannot overwrite the page
behind the keyboard — which is exactly what it used to do.

## Controls

| Input | Action |
|-------|--------|
| Select · M / Menu key · right click | Open (or close) the Select menu, which holds every action below; see [Full screen and the Select menu](#full-screen-and-the-select-menu) |
| Backspace · Alt+← / Alt+→ · F5 | Back / Forward / Reload (keyboard) |
| D-pad Up / Down | Scroll the page (link mode: nearest link on the row above/below) |
| D-pad Left / Right | Walk the page's links in reading order |
| Tab / L3 | Cycle scroll ⇄ link mode (**SCROLL** / **LINKS** chip shows which) |
| L1 / R1 | Scroll up / down by a page |
| L2 / R2 · `-` / `+` · zoom chip | Zoom the page text out / in (75%–160%). The toolbar chip is the one place the scale is shown and changed: it names the current scale, is highlighted when off 100%, and steps up on a tap (wrapping). Remembered across pages and relaunches. |
| A / Enter | Edit the address bar, open the outlined link in link mode, or work the focused form control |
| `f` · **Find** chip | Find on the page; `n` / `p` step to the next / previous match, **Esc** clears the search |
| **Save** chip | Save the page on screen (**Saved** when it is already on the list) |
| **Marks** chip | Open the saved-page list, with its count in the label |
| Y in the saved-page list | Make the highlighted page the home page |
| **Home** chip | Open the home page (Settings → Browser chooses it, and the search engine) |
| X / `x` / `l` | Open the link picker |
| B / Esc | Go back to the previous screen (link mode: back to scroll mode) |
| Back / Forward / Reload | Return to a page — and to the paragraph you were on — instead of the top (session-only; a fresh link or typed address starts at the top) |
| **Filter** chip | Turn ad/tracker filtering off (or back on) for the current site |
| **Forget** chip | Saved-cookie count in its label (capped at 99+); drop every saved cookie and the file on the card (press twice) |
| Cookie strip | Under the page: the hosts the saved cookies belong to, or **No saved cookies** |
| Touch / mouse | Tap the address bar, **GO**, the filter counter, the mode chip, the zoom chip, a link row, or a form control |

The chips, the address bar and the cookie strip in this table are the classic
layout's (**Full-screen pages** off); full screen, the same actions are rows of
the Select menu; a tap on the page follows links and works forms, and a tap on
the site pill, the find count or **Link mode** in its corner does what that
pill does.

In the link picker: D-pad chooses, **A** or Enter opens, **B** or Esc closes.

## What it cannot do

This is a reading browser, not a full web engine. It runs a page's scripts
against a small document model of its own (see [JavaScript](#javascript)),
which brings most script-built content onto the page, but there is no layout
for scripts to measure, no canvas, no workers or sockets, and the scripts get
ten seconds; a heavy web app can still come out empty, and sites that test for
a full browser stop at their test. It can fill and POST ordinary text forms, but
cannot upload a file or complete sign-ins that depend on browser features
outside its page model (see
[Forms](#forms)). Cookies survive a relaunch (see [Cookies](#cookies)); a session
is only saved if the site handed one out itself. A `calc()` that mixes units the layout has not measured yet
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
release pages work, and so do most pages that render their content with
JavaScript; a page whose scripts need more than the reader has (a canvas, a live
socket feed, a browser check) still shows little or no text. Google's search
results are one such page (see
[Google needs a full web browser](#google-needs-a-full-web-browser)). The companion app is the way to send a link from a
phone if typing on the handheld is awkward.

If a page fails, the error is shown in the content area. Check Wi-Fi first, then
try the repository URL directly:

```text
https://github.com/jukaLang/JukaHubV2
```
