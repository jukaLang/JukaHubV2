# Web CSS support

The built-in reader is a text-and-box engine, not Chromium. This file is the
contract for what its CSS layer renders and what it deliberately does not. A
property that is accepted by the parser but listed as unsupported is a known
gap, not a silent promise.

## Rendered

### Stylesheet loading

- Linked stylesheets and `@import` inside them, and `@import` inside inline
  `<style>` blocks. Imports resolve relative to the page or the imported
  sheet, are bounded and cycle-safe, and keep media-qualified wrappers. Their
  `url(...)` references resolve against the imported stylesheet.
- Up to 64 linked sheets per page (16 MiB together, 4 MiB per source file),
  fetched eight at a time within a 20-second pass. A sheet past its share
  keeps complete leading rules instead of being dropped. Linked and inline
  rules share a 24 MiB budget and apply in page order; inline `<style>` blocks
  retain at least 8 MiB when linked sheets fill their allowance.
- A link's `media` query is honoured: `media="print"` is not fetched, other
  queries wrap the sheet in `@media`. `rel="alternate stylesheet"` and
  `disabled` links are skipped. The async-CSS loaders count as the sheet they
  become: `rel="preload" as="style" onload="this.rel='stylesheet'"` and
  `media="print" onload="this.media='all'"`.

### Selectors and generated content

- Type, class, id, attribute, descendant/child/sibling selectors.
- Native CSS nesting, including `&` parent references, relative descendant
  selectors and nested `@media`, `@supports`, `@container` and `@layer`
  blocks.
- `:root`, `:link`, `:visited`, `:hover`, `:focus`, `:target`, `:checked`,
  `:disabled`, `:enabled`, `:required`, `:optional`, `:read-only`,
  `:placeholder-shown`, `:empty`, `:defined`, `:lang()`, `:dir()`.
- `:not()`, `:is()`, `:where()`, `:has()`, `:first-child`, `:last-child`,
  `:only-child`, `:first-of-type`, `:last-of-type`, `:only-of-type`,
  `:is()` contributes the specificity of its most specific argument and
  `:where()` contributes none,
  `:nth-child()` with `odd`, `even`, integers and `An+B`.
- `::before`, `::after` and `::marker` with text `content`.
- `content` strings (including CSS escapes and concatenation), `attr()`,
  `counter()`, `counters()`, `open-quote`/`close-quote` and the quote negations.
- `counter-reset`, `counter-increment`, `counter-set`, nested counter scopes,
  and `counter()` number styles (`decimal`, `decimal-leading-zero`,
  `lower-alpha`, `upper-alpha`, `lower-roman`, `upper-roman`).
- `::marker` text and paint style (`color`, `font-size`, inherited typography).
  The reader has no marker background/border layer.

### Box and layout

- `display: block`, `flex`, `grid` (including explicit
  `grid-template-columns`/`grid-template-rows` tracks,
  `minmax(0, 1fr)`, `minmax(220px, 1fr)` and `minmax(auto, 300px)`,
  `repeat(auto-fit/auto-fill, ...)` with container-relative repetition
  counts (auto-fit collapses unused tracks and their gaps, auto-fill keeps
  them), named `grid-template-areas` with `grid-area: header`, item
  placement with `grid-column`/`grid-column-start`/`grid-column-end` and
  `grid-row`/`grid-row-start`/`grid-row-end` (`span 2`, `2 / 4`, `1 / 3` and
  `1 / -1`), `grid-auto-flow: row|column|dense`, implicit-track sizing with
  `grid-auto-columns`/`grid-auto-rows`, and `justify-items`/`justify-self`
  (`place-self` too) on top of `align-items`/`align-self`, plus
  `justify-content` and `align-content` track distribution). Column
  `minmax(auto, <length>)` minima come from the placed items' measured
  min-content width and remain clamped by the maximum; row auto minima come
  from content height. The table display modes
  `table`, `table-row`, `table-cell`, `table-header-group`, `table-row-group`,
  `table-footer-group`, `table-caption`.
- Flex direction, wrap, justify, align, gap, grow, shrink, basis, order,
  including `flex-flow` and `place-content` shorthands.
  `min-width: auto` gives each item a min-content floor measured from its
  longest unbreakable word; the floor participates in basis resolution,
  wrapping and the shrink pass, while `min-width: 0` disables it.
- Width (lengths and percentages), `height`/`min-height`/`max-height` in
  pixels and percentages against a definite parent height, `min-width`/`max-width`,
  `box-sizing: content-box/border-box` (padding is included in border-box
  width and height), aspect ratio on
  replaced media, margins, paddings, logical margin/padding variants, auto
  margins on width-constrained blocks and on flex/grid items (main and cross
  axes), text indent, border radius.
- Viewport units `vw`, `vh`, `vmin`, `vmax`, their logical aliases `vi`/`vb`,
  and the modern `sv*`, `dv*` and `lv*` forms (`svh`, `dvh`, `lvh`, ...). They
  resolve against the reader's screen, including inside `calc()`, `min()`,
  `max()` and `clamp()`.
- `float: left/right` and the logical `inline-start`/`inline-end` spellings (a
  following block is narrowed beside it; `clear: left/right/both` and the
  logical spellings move a block below the float). Floats shrink to their
  widest run when no width is given.
- `position: relative/absolute/fixed`, physical and logical inset properties
  including the `inset-block`/`inset-inline` two-axis shorthands, z-index. An
  absolutely positioned auto-width box with both inline edges set stretches
  between them instead of keeping its caption-sized default.
- `calc()` over `px`, `em`, `rem`, percentages and viewport units, plus
  `min()`, `max()` and `clamp()` in ordinary lengths.
- Container queries and container-relative units.
- Images, video, forms and details/summary layout.
- Tables: row/cell flow, `colspan`/`rowspan` (clamped to 32 tracks and the
  remaining rows), `<colgroup>`/`<col>` widths and `span` values matched
  left-to-right, `table-layout: auto/fixed`, direct cell widths, empty rows
  retained for rowspan gaps, and `border-collapse: separate/collapse` with
  `border-spacing`. Collapsed borders resolve conflicts by width, style
  priority (hidden > double > solid > dashed > dotted > none), then cell over
  row over table, and each interior edge paints once.

### Typography and paint

- Colour, background colour, linear/radial/conic gradients and opacity.
- Font size, weight (faux bold), text transform, text align (including
  `justify`), line height, `font-style: italic/oblique` (synthetic italic
  face), `letter-spacing`, and `word-spacing`.
- `white-space: normal`, `nowrap`, `pre`, `pre-wrap`, `pre-line` and
  `break-spaces`. `nowrap` keeps one line and ellipsizes overflow; `pre` keeps
  authored spaces and newlines without wrapping; `pre-wrap` and `break-spaces`
  preserve both and still wrap; `pre-line` preserves newlines while collapsing
  spaces. Justified text stretches inter-word gaps on every line except the
  last, so non-final lines reach the right content edge.
- `overflow-wrap: anywhere`/`break-word`, the `word-wrap` alias, and
  `word-break: break-all`/`break-word` break long tokens into full-width text
  runs instead of replacing them with an ellipsis. `anywhere` and `break-all`
  also lower flex/grid min-content widths; `overflow-wrap: break-word` keeps
  the unbroken intrinsic floor. A single token can create at most 256
  fragments, and tokens over 8 KiB use the bounded fallback.
- `text-overflow: clip` and `ellipsis` choose how a non-wrapping block's last
  visible line ends. Without either declaration the reader keeps its existing
  ellipsis fallback.
- `text-decoration` underline/strikethrough, `text-decoration-color`, pixel
  `text-decoration-thickness`, and `solid`/`double`/`dotted`/`dashed`/`wavy`
  `text-decoration-style`.
- `text-shadow` offset and colour (blur is ignored).
- `box-shadow` outer offset and colour (blur, spread and inset are ignored).
- Borders in `solid`, `dashed`, `dotted`, `double`, `none` and `hidden`
  styles; border width and colour; outlines as a separate box edge with
  `outline-offset`; list styles and list-style none.
- `visibility: hidden/collapse` keeps the element's flow and text metrics and
  suppresses its paint; a descendant with `visibility: visible` paints again.
- `object-fit: fill/contain/cover/none/scale-down` and `object-position`
  keywords, percentages and pixel lengths on images and video cards; when
  omitted, the reader keeps its historic centre-cover crop.
- Comma-separated `background-image` / `background` layer lists, with each
  layer keeping its own image/gradient, size, position and repeat. Layers are
  painted in CSS order (the first layer on top), with a cap of four layers and
  512 background tiles per block; anything beyond that is capped rather than
  allowed to hang layout.
- `background-image: url(...)` layers under the block content, with
  `background-size` (`auto`, `cover`, `contain`, explicit lengths/percentages)
  and `background-position` keywords/percentages/lengths resolved against the
  block paint box. `background-repeat` repeats image and gradient layers
  (`repeat`, `repeat-x`, `repeat-y`, `no-repeat`, and the two-value form),
  anchored at the positioned origin and clipped to the padding box. `space`
  keeps tile size and distributes equal gaps without far-edge clipping;
  `round` rescales tiles so an integer count fills the box. Gradients accept
  the same sizes, positions and repeats.
- `vertical-align` (`baseline`, `middle`, `top`, `bottom`, `text-top`,
  `text-bottom`, plus lengths/percentages) shifts the inline run inside its
  line slot; decorations move with it.
- `overflow`, `overflow-x` and `overflow-y` with `hidden`/`clip` clip the
  block's descendant runs, background/border rects, images and videos to its
  padding box at paint time.
- Box `opacity` multiplies into text, fills, borders, gradients, images and
  video card chrome/thumbnails; `opacity: 0.5` therefore paints a different
  alpha from `1`.
- Links, focus outlines and reader selection states.

## HTML elements

- Block flow: `body`, `p`, `div`, `section`, `article`, `main`, `header`,
  `footer`, `aside`, `nav`, `form`, `fieldset`, `figure`, `figcaption`,
  `address`, `dl`, `dt`, `dd`, `li`, `tr`, `details`, `summary`, `blockquote`,
  `pre`, `h1`–`h6`, `dialog`, `hgroup`, `menu`, `search`.
- Lists: `ul`, `ol`, `li`, including nested lists and custom counters.
- Inline and text: `a`, `span`, `strong`, `b`, `em`, `i`, `u`, `ins`, `s`,
  `del`, `strike`, `small`, `mark`, `code`, `kbd`, `samp`, `var`, `q`, `cite`,
  `abbr`, `time`, `sub`, `sup`, `br`, `wbr`, and unknown/custom tags (inline).
- Replaced/media: `img`, `picture`/`source`, `hr`, `video`, `audio`, `iframe`
  embeds. `svg`, `canvas`, `object`, `embed`, and media without a playable
  source keep a real placeholder box with their resolved width, height,
  background and border. Their fallback content is not interpreted.
- Forms: `input` (text/password/checkbox/radio/submit/range/etc.), `textarea`,
  `select`/`option`, `button`, `label`, `legend`, `output`, `progress`, `meter`.
- Table structure: `table`, `thead`, `tbody`, `tfoot`, `tr`, `td`, `th`,
  `caption` render rows and cells through the table grid; `caption` is a
  centred block above the rows. `colspan`/`rowspan` claim grid slots, direct
  `width` on cells sizes columns, fixed layout reads the first row, and
  `border-collapse: collapse` leaves one shared edge between neighbours.
  `colgroup`/`col` widths (including `span`) define column tracks in fixed and
  auto layout, with a cell's own width winning where CSS gives it precedence.
  A `<tr>` with no cells still reserves a row when rowspans occupy it.
- `head` metadata and `script`/`style`/`noscript`/`title` content are not
  painted. Raw-text containers (`svg`, `canvas`, `template`, `object`) are not
  interpreted as HTML.

## Accepted but not rendered

These are the remaining visible gaps. They are listed here so they cannot be
mistaken for working CSS.

| Construct | Current behaviour |
| --- | --- |
| `font-family` | Accepted; the reader uses its one bundled face. |
| `display: inline-block` and block/inline conversion | `display: contents` and `display: inline` participate in flow, but inline-block dimensions and every block/inline conversion are not yet modelled as browser box generation. |
| Width/min-width/max-width on a flex/grid container | Accepted; item widths use the page measure because the container itself has no separate box in this engine. |
| Percentage heights (`height: 50%`, `min-height`, `max-height`) | Resolved when the containing block has a definite height, including `calc()`, `min()`, `max()` and `clamp()` with percentages. An auto-height ancestor leaves them content-sized. |
| aspect-ratio on a wrapper with no direct block | Accepted for ordinary blocks; a wrapper that only colours its children reserves its ratio through its box, not through a text row. |
| `position: sticky` | Kept in normal flow and pinned as a copy once it scrolls past its inset; nested scroll containers are not modelled. |
| `box-shadow` blur, spread, inset; `text-shadow` blur | Accepted; only the first offset/colour is painted. |
| `transform`, `transition`, `animation`, `filter`, `clip-path` | Accepted or skipped; no visual transform. |
| non-rectangular `grid-template-areas` matrices | Accepted; named areas use the bounding box of their cells. |
| Table row groups (`thead`/`tbody`/`tfoot`) as separate layout boxes; borders/backgrounds on `colgroup`/`col` | Rows share one table grid; column width and span are applied, but column-level borders/backgrounds are not painted. |
| `list-style-image`, `list-style-position` | Accepted; markers are text only. |
| `::first-line`, `::first-letter`, `::selection`, `::placeholder`, `::backdrop`, scrollbar/form-control pseudo-elements | Not generated; their rules do not style the element. |
| `content: url(...)` and image content | Skipped; generated content is text only. |
| `counter-reset`/`counter-increment` inside `::before`/`::after` | Parsed on the pseudo-element; counter operations are applied on elements, not generated boxes. |
| `@charset`, `@namespace` | Not applied. |
| `@font-face`, `@keyframes`, `@page`, `@layer` ordering | Not modelled as rendering features. `@media`, `@supports` and `@container` are evaluated. |

## Draw-path fixture coverage

`player/webpage_headless_features_test.go` drives every row below through
`parseWebPage` → `fetchWebImages` → `setBrowserDocument` →
`browserPageLayout` → `drawWebDocument` on a software renderer. Each fixture
asserts on the rasterized pixels and the laid-out geometry, so a supported
feature that regresses paint or box flow fails the suite.

| Feature area | Fixture | Known limits exercised |
| --- | --- | --- |
| Inline `strong`/`em`/`code`/links, wrapping, `white-space` | `InlineFormattingAndWhitespace` and `WhiteSpaceModes` | Tabs in preserved text use four spaces rather than `tab-size`. |
| List markers, nesting, counters | `NestedLists` | Markers are text; no `list-style-image` or marker boxes. |
| Floats and `clear` | `FloatsAndClear` | Float shrink-to-fit uses the widest run. |
| Four-side borders, styles, radius | `BordersAndRadius` | Radius is clamped to the used box; no elliptical radii. |
| Opacity | `OpacityPaintsThrough` | Opacity multiplies paint, not an isolated compositing group. |
| Transforms | `TransformsStayInFlow` | `transform` is accepted but deliberately not applied; content stays readable in normal flow. |
| Text alignment, decoration, line height, letter spacing | `TextMetricsAndDecoration` and `JustifiedText` | Justification stretches automatic word gaps; a line with no gaps stays left-aligned. |
| Table borders, collapsed separators, padding, spans | `TableSpanPaddingAndBorders` | Column-level borders/backgrounds are not painted. |
| Image dimensions, natural size, `object-fit`/`max-width` | `ImageSizingAndFit` | Natural size is capped to the reading measure and four-thirds screen height. |
| Background colour/image layers, repeat, position | `BackgroundLayersRepeatAndPosition` | Four layers and 512 tiles per block are the cap. |
| Relative/absolute/fixed offsets | `PositionedOffsets` | `fixed` anchors to the viewport-sized page box; there is no scrolling viewport. |
| Media queries and meta viewport | `MediaQueriesAndViewportMeta` | Width-based `@media` is evaluated against the reader viewport; printable/device-specific queries do not change layout. |
| Default and disabled form controls | `FormControlStates` | Native OS widgets are replaced by reader-drawn controls. |

The full list of accepted-but-not-rendered CSS remains the table above; the
fixture column names the fallback each gap degrades to rather than an
unasserted promise.

## Adding support

A new property must do all three:

1. Parse into `webStyle` (and reset it if it is not inherited).
2. Change layout or paint, not just the style struct.
3. Add a render-output test (generated text, run geometry, layout rectangle or
   paint colour), not only a parse assertion.

The tests in `player/webpage_css_cascade_test.go` and
`player/webpage_flow_layout_test.go` are the examples to follow.
