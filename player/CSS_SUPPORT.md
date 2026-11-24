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
- Images, video, forms and details/summary layout. An inline-level image
  (not `display: block`, floated, positioned or a flex/grid item) is an
  unbreakable box inside its line: it wraps like a word, stands on the
  baseline or follows `vertical-align: middle/top/bottom`, and a link made of
  images is a D-pad stop. Image sizes are CSS px on the same scale as text;
  `width`/`height` attributes win over the file's natural size.
- `display: inline-block` (and `inline-flex`/`inline-grid`) on block-level
  elements: consecutive siblings form a wrapping row, each box as wide as its
  content (or its `width`), justified like the parent's `text-align`, with
  its own padding, background, border and radius. A block-level sibling ends
  the row. An `<li>` with `display: inline` or `inline-block` has no marker.
- `transform: translate()`, `translateX()`, `translateY()` and
  `translate3d()` (lengths and percentages of the box) on absolutely and
  fixed positioned boxes, after their size is known, and percentage insets
  resolved against the containing block.
- `line-clamp` and `-webkit-line-clamp` (on `display: -webkit-box` with
  `-webkit-box-orient: vertical`, the only place browsers honour the prefixed
  form): the element keeps that many lines of its text, and the last kept
  line ends in "…" when text was cut. Such a legacy vertical box is laid out
  as a block of lines, not as a flex row of its inline children.
- `text-align: -webkit-center` (also `<center>` and `align="center"` on a div
  or table part) centres narrower boxes as well as text. A table narrower than
  its column is centred by it or by `margin: auto` / `align="center"`; a rule
  (`<hr>`) narrower than its column sits in the middle.
- `border-spacing: 0` (and `cellspacing="0"`) means no gap; only a table
  with no spacing at all gets the 2px default.
- SVG: inline `<svg>`, `<img src>` SVG files, SVG `<picture>` sources and
  `data:image/svg+xml` URIs (base64, percent-encoded or utf8, including in
  CSS `url()`), rasterised by `webpage_svg.go`: paths (every command, arcs
  included), rect/circle/ellipse/line/polyline/polygon, groups, nested
  `<svg>`, `<use>` and `<symbol>` (also from sprite sheets elsewhere on the
  page, hidden ones included), transforms, presentation attributes,
  `style=""`, simple `<style>` rules (tag, `.class`, `#id`), `currentColor`
  (the element's text colour), the page's CSS `fill`/`stroke` on the
  `<svg>`, fill/stroke opacity, `fill-rule`, caps, joins and miter limits,
  and linear/radial gradients (with `href` inheritance). An `<svg>` without
  a size takes its `viewBox` size.
- Tables: row/cell flow, `colspan`/`rowspan` (clamped to 32 tracks and the
  remaining rows), `<colgroup>`/`<col>` widths and `span` values matched
  left-to-right, `table-layout: auto/fixed`, direct cell widths, empty rows
  retained for rowspan gaps, and `border-collapse: separate/collapse` with
  `border-spacing`. Collapsed borders resolve conflicts by width, style
  priority (hidden > double > solid > dashed > dotted > none), then cell over
  row over table, and each interior edge paints once.

### Typography and paint

- Colour, background colour, linear/radial/conic gradients and opacity.
  `color: inherit` / `currentColor` on a link keeps the surrounding text
  colour instead of the reader's link blue (Tailwind's reset).
- Font size (up to twice the body size), weight (a real bold face: Inter
  SemiBold, or an emboldened copy of a picked UI face; numeric weights from
  600 up are bold), text transform, text align (including `justify`), line
  height, `font-style: italic/oblique` (synthetic italic face),
  `letter-spacing`, and `word-spacing`. A font size in `em` or `%` is
  relative to the parent's size (not to the tag's default), `rem` follows the
  root element's size (`html { font-size: 62.5% }` makes 1rem 10px), and
  `inherit`/`unset` on font-size, font-weight, font-style and text-align take
  the parent's value even on a heading or `<th>`.
- Gradient text: with `background-clip: text` (or the `-webkit-` spelling)
  the glyphs take the background ramp's average colour and no background box
  is painted. A visible `-webkit-text-fill-color` is the glyph colour.
- `vertical-align: super` and `sub` raise and drop the baseline by a third and
  a fifth of the parent's size, and a relatively positioned inline box moves
  by its `top`/`bottom` (normalize.css and Bootstrap draw `<sup>` that way).
  Inline descendants move with the box, so the link inside a footnote
  `<sup>` is raised too.
- `font-family` chooses between two faces: the UI face, and a monospace face
  for `monospace`, `ui-monospace`, the common code families (Menlo, Consolas,
  Courier New, Fira Code, JetBrains Mono, …) and `<pre>`, `<code>`, `<kbd>`,
  `<samp>`, `<tt>`, when the machine has one (`termPtyMonoFontPath`: a
  `*Mono*.ttf` in JukaHub's fonts folder, Consolas or Cascadia Mono on
  Windows, DejaVu, Liberation or Noto Sans Mono on Linux). Unknown families
  keep the inherited choice.
- Every line is set on one baseline: runs of different sizes and faces, and
  inline images, align as in a browser.
- Icon fonts: Private Use Area code points (Font Awesome and the like) are
  dropped instead of drawing as stray marks, and Material Icons / Material
  Symbols ligature names (by family or by the `material-icons*` /
  `material-symbols-*` classes) become the nearest glyph Inter has (arrows,
  ×, ✓, ‹ ›, ★, …) or nothing.
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
- `text-shadow`: the first visible layer's offset and colour; its blur only
  softens the alpha.
- `box-shadow`: the first visible outer layer, with its offset, blur (a soft
  falloff of layered translucent rects, up to 96 device px) and spread. Inset
  layers are skipped.
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
- `border-radius` on an image clips the picture itself (SDL_RenderGeometry;
  a renderer without it draws the picture square), so a `50%` avatar is a
  circle.
- Links, focus outlines and reader selection states.

## HTML elements

- Block flow: `body`, `p`, `div`, `section`, `article`, `main`, `header`,
  `footer`, `aside`, `nav`, `form`, `fieldset`, `figure`, `figcaption`,
  `address`, `dl`, `dt`, `dd`, `li`, `tr`, `details`, `summary`, `blockquote`,
  `pre`, `listing`, `h1`–`h6`, `dialog`, `hgroup`, `menu`, `search`,
  `center`.
- The browser's own defaults: `h1`–`h6` are 2, 1.5, 1.17, 1, 0.83 and 0.67
  times their parent's size with Chrome's em margins; `p`, `pre`,
  `blockquote` and `figure` have one em above and below; `blockquote` and
  `figure` are indented 40px on both sides (no rule), `dd` 40px on the left;
  `code`, `kbd`, `samp`, `tt` and `pre` are 13/16 of the surrounding size;
  `small` and `sup`/`sub` are a step smaller, `big` a step larger; `th` is
  bold and centred; `q` is quoted (“ ”) unless the page's own `::before` /
  `::after` content replaces the quotes; `abbr[title]` has a dotted
  underline; `nobr` does not wrap.
- Lists: `ul`, `ol`, `li`, including nested lists and custom counters. Nested
  bulleted lists step from disc to circle to square; `<ol start>`,
  `<li value>` and the `type` attribute (`1`, `a`, `A`, `i`, `I`, `disc`,
  `circle`, `square`) number and mark the way the page says.
- Inline and text: `a`, `span`, `strong`, `b`, `em`, `i`, `u`, `ins`, `s`,
  `del`, `strike`, `small`, `big`, `mark`, `code`, `kbd`, `samp`, `var`, `q`,
  `cite`, `abbr`, `time`, `sub`, `sup`, `font`, `nobr`, `br`, `wbr`, and
  unknown/custom tags (inline).
- Presentation attributes, as zero-specificity hints that any CSS rule
  overrides (`webpage_hints.go`): `bgcolor` (also written without `#`),
  `align` (text alignment; on `img`, `left`/`right` float and
  `middle`/`top` align; on a `table`, `center`), `width`/`height` on tables
  and cells, `width`/`size`/`color`/`noshade` on `hr`, a table's `border`
  (with `bordercolor`), `cellpadding` and `cellspacing`, `<font color size
  face>`, and `<body text link bgcolor>`.
- Replaced/media: `img`, `picture`/`source`, `svg`, `hr`, `video`, `audio`,
  `iframe` embeds. `canvas`, `object`, `embed`, an `<svg>` with nothing to
  draw but a background or border, and media without a playable source keep
  a real placeholder box with their resolved width, height, background and
  border. Their fallback content is not interpreted.
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
| `font-family` | Two faces only (the UI face and a monospace one); web fonts are not downloaded. |
| `display: inline-block` on inline elements (`a`, `span`, `button`) and other block/inline conversion | They stay inline text with a background chip; their own width, height and vertical padding are not boxes. A block-level element containing block children inside an inline row is one item; text between inline-block siblings joins the box before it. |
| Width/min-width/max-width on a flex/grid container | Accepted; item widths use the page measure because the container itself has no separate box in this engine. |
| Percentage heights (`height: 50%`, `min-height`, `max-height`) | Resolved when the containing block has a definite height, including `calc()`, `min()`, `max()` and `clamp()` with percentages. An auto-height ancestor leaves them content-sized. |
| aspect-ratio on a wrapper with no direct block | Accepted for ordinary blocks; a wrapper that only colours its children reserves its ratio through its box, not through a text row. |
| `position: sticky` | Kept in normal flow and pinned as a copy once it scrolls past its inset; nested scroll containers are not modelled. |
| `box-shadow` inset; more than one shadow layer; `text-shadow` blur | Only the first visible outer box-shadow layer is painted; a text shadow's blur only lowers its alpha. |
| `transform` (except `translate` on positioned boxes), `transition`, `animation`, `filter`, `clip-path` | Accepted or skipped; no visual transform. In-flow boxes stay where the flow puts them. |
| SVG `<text>`, `<image>`, filters, masks, clip paths, patterns, markers, dashes and `<foreignObject>`; external `<use href="sprite.svg#icon">` | Skipped; the rest of the drawing renders. |
| non-rectangular `grid-template-areas` matrices | Accepted; named areas use the bounding box of their cells. |
| Table row groups (`thead`/`tbody`/`tfoot`) as separate layout boxes; borders/backgrounds on `colgroup`/`col` | Rows share one table grid; column width and span are applied, but column-level borders/backgrounds are not painted. |
| `list-style-image`, `list-style-position` | Accepted; markers are text only. |
| `background-clip: text` | The glyphs take the gradient's average colour; the ramp is not clipped to the letter shapes. |
| `line-clamp` on a wrapper whose text sits in child blocks | Only the element's own lines are clamped; a clamp on a wrapper of paragraphs does not count their lines. |
| `vertical-align` in table cells, `valign` | Cell content stays at the top of the row; browsers centre it by default. |
| Presentation attributes not listed above (`nowrap`, `background`, `hspace`/`vspace`, `frame`/`rules`, a table's `align="left"/"right"`) | Ignored; CSS for the same properties works. |
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
