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
| Touch / mouse | Tap the address bar, **GO**, or a link row |

In the link picker: D-pad chooses, **A** or Enter opens, **B** or Esc closes.

## What it cannot do

This is a reading browser, not a full web engine. It does not run JavaScript,
render images, implement grid or float layouts, log in to sites, or submit
forms. A `calc()` that mixes units the layout has not measured yet
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
