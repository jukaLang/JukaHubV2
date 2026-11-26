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

### Cascade

- Cascade layers: `@layer` blocks, `@layer a, b;` order statements, nested
  and anonymous layers, and `@import` with `layer` or `layer(name)`. Earlier
  layers lose to later ones and every layer loses to unlayered rules; for
  `!important` declarations the order is reversed, as in CSS (Tailwind 4 and
  many resets ship in layers).
- Margin and padding shorthands, the logical pairs (`padding-inline`,
  `margin-block`) and single sides (`padding-top`, `padding-inline-start`)
  settle side by side: the declaration that comes last in the cascade wins
  that side, so Tailwind's `p-4 pt-2` pads the top by 8px, not 24px.
- The UA sheet's side margins of `blockquote`, `figure` and `dd` (40px) give
  way to the page's own `margin` on that side, so a reset's
  `blockquote, figure { margin: 0 }` sets them flush.

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
  An item with no width, basis or grow of its own is as wide as its content
  (`flex-basis: auto`), so `justify-content` places the room left over — a
  header's logo at one end and its menu at the other, a row of tags packed to
  the start — and content wider than the row shrinks in proportion to each
  item's width; items the reader cannot measure (a nested row, a picture of
  unknown size) share the row. `space-evenly` is distinct from
  `space-around`, and `safe`/`unsafe` alignment keywords are read. A lone item
  is placed too (`justify-content: center` centres a single button).
- Cross-axis alignment is in pixels: an item is as tall as what it paints, a
  line as tall as its tallest item or as the container's definite `height` /
  `min-height` (a 64px header bar, a full-screen hero), and `align-items` /
  `align-self` `center` and `end` drop an item inside it — a nav's links sit
  in the middle of a taller logo. `stretch` (the default) gives every item the
  line's height, so a row of cards ends level. In a column container with a
  definite height, `justify-content` distributes the spare rows, and
  `align-items` `center`/`end` put an item without a width of its own in the
  middle or at the end of the column at its content's width.
- Width (lengths, percentages, and `fit-content`, `max-content`,
  `min-content` and `stretch` with their vendor spellings: a fitted box is as
  wide as its longest line or widest word, its padding included, and
  `margin: 0 auto` centres it), `height`/`min-height`/`max-height` in
  pixels and percentages against a definite parent height, `min-width`/`max-width`,
  `box-sizing: content-box/border-box` (padding is included in border-box
  width and height), aspect ratio on
  replaced media, margins, paddings, logical margin/padding variants, auto
  margins on width-constrained blocks and on flex/grid items (main and cross
  axes), text indent, border radius. A margin or padding takes at most eight
  text rows (about 180 CSS px at the reader's size).
- The children of a flex or grid container are blockified, as in CSS: a link,
  a span or a label straight inside a flex row is an item with its own
  padding, background, border and radius (the buttons, pills and badges of
  most toolbars).
- Inline boxes (`webpage_inlinebox.go`): an inline element's horizontal
  margin, border and padding take their room before its first word and after
  its last, its background chip covers its padding and border on every side
  with its `border-radius`, and its border is drawn (a box broken over two
  lines has no side edge at the break). Elements and `::before`/`::after`
  content inside the box share its chip. An `inline-block` / `inline-flex`
  inline element (and every `<button>` and submit input, which an unstyled
  page draws with the browser's padding) is atomic: the line may break at its
  edges without a space and the box moves to the next line whole; once its
  vertical padding and border outgrow the row's spacing the line grows to
  hold them, while a plain inline box's padding overlaps the lines around it.
- Viewport units `vw`, `vh`, `vmin`, `vmax`, their logical aliases `vi`/`vb`,
  and the modern `sv*`, `dv*` and `lv*` forms (`svh`, `dvh`, `lvh`, ...). They
  resolve against the reader's screen, including inside `calc()`, `min()`,
  `max()` and `clamp()`.
- `float: left/right` and the logical `inline-start`/`inline-end` spellings
  (`webpage_floats.go`). A floated block element floats with everything in it
  — a figure with its picture and caption, an infobox table, a caption box, a
  float grid's column, a sidebar — at its `width`, or as wide as its widest
  picture, line or row (a `display: table-caption` caption wraps under the
  picture instead of widening the figure; a box inside with a width of its own
  sets it), with its margins as the gap to the text. The lines beside a float
  are shortened by exactly the part its margin box covers and the lines below
  it have the whole measure again; a line with no room left beside it goes
  below it. A box that cannot run under a float — a background or border of
  its own, a picture, a rule, a flex row, a grid, a table (moved clear of it at
  its own width) — sits beside it, or below it when the room is too narrow.
  Floats stack: a second left float stands right of the first, a right float
  left of the earlier one, and a float that does not fit beside them drops
  below. `clear: left/right/both` (and the logical spellings) moves a block —
  its children too — below the floats; on a float it moves only the float. A
  clearfix (`::after { content: ""; clear: both }`), `<br clear="all">`, an
  element with `overflow: hidden/auto/scroll` or `display: flow-root`, every
  flex or grid item, table cell, positioned layer and float, and the page
  itself hold their floats: what follows starts below them.
- The page is laid out at the full width of the view, as a browser lays a
  page out at its window's width, and `@media` and `vw` answer for that same
  width (no narrower reading column).
- `position: relative/absolute/fixed`, physical and logical inset properties
  including the `inset-block`/`inset-inline` two-axis shorthands, z-index. An
  absolutely positioned box is one layer with everything in it: its heading,
  paragraphs, buttons and pictures are measured together and placed at its
  insets in the nearest positioned ancestor (or the page), whose text and
  pictures give it its rectangle. A box without insets keeps its static
  position, where its markup would have put it in the flow. A layer without a
  width shrinks to its widest line; one with both inline edges set stretches
  between them; both insets with a width and `margin: auto` centre it. An
  absolute box held by both its `top` and `bottom` (`inset: 0`) with no height
  of its own is as tall as the room between them: its surface fills it, and
  its own flex or grid container gets that height, so an overlay's caption
  sits at the foot of its photo and a play button in its middle; a box that
  is only a picture (`absolute inset-0` around an `h-full w-full
  object-cover` image) fills that room with it, cropped. A positioned
  container whose children all left the flow keeps a line and its height of
  its own. An absolutely or fixed positioned inline element (a `<span>`
  badge, ribbon or label) leaves its line too and is placed at its insets,
  while the text around it stays one line; a link, a label, a form control,
  a visually hidden label and one whose containing block is a positioned
  inline element keep their place in the line.
- `position: fixed` boxes are layers pinned to the screen — at their top or
  bottom inset, centred by `margin: auto`, or at their static place — and stay
  there while the page scrolls under them; their links and fields are tapped
  where they are drawn.
- Stacking: the page paints as CSS stacks it — layers with a negative
  z-index, the flow's surfaces in tree order (a box's own background under the
  boxes and pictures nested in it, and around its pictures: a card's surface
  reaches behind the photo at its top), floats, the flow's pictures and text,
  then positioned layers and pinned sticky bars by z-index. A z-index orders a
  layer inside the stacking context of a positioned ancestor with a z-index, a
  fixed or sticky one, a translucent or transformed one, one with
  `isolation: isolate`, a `filter` or `backdrop-filter`, a `mix-blend-mode`,
  `will-change`, `contain: paint`/`layout`, `perspective`, a `clip-path` or a
  mask, or a flex or grid item with a z-index, so a badge's `z-index: 9999`
  inside a `z-index: 1` card stays under a `z-index: 2` header, and a
  `-z-10` picture in an `isolate` hero stays over the hero's background. A
  relatively positioned element of the flow is painted, with everything in
  it, in that order with the layers wherever it overlaps one, so a hero's
  `relative z-10` heading is over its `absolute inset-0` picture. An
  `overflow: hidden` containing block, or a box around it, clips its layers
  (a carousel's frame clips the captions of the slides in its track). A layer
  wholly outside the page or its clip (a skip link parked above the page, an
  off-canvas drawer, a carousel's next slide), a fully transparent positioned
  block and a screen-reader-only one (the 1×1 px `sr-only`/`visually-hidden`
  box) are not painted, nor is a fixed box parked wholly past the screen's
  left, top or right edge even inside another positioned box (html5test's
  `left: -240px` index menu). Links and fields inside layers and floats are D-pad
  and tap targets, a tap goes to the topmost one, and find-in-page searches
  their text.
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
  `translate3d()` (lengths and percentages of the box), after its size is
  known: a positioned layer moves by it, and so does a box in the flow without
  leaving its place there (`left: 50%; transform: translateX(-50%)` centres a
  relatively positioned block; a card pulled up into a hero overlaps it).
  Percentage insets resolve against the containing block; a relative
  `left`/`right` in percent against its width. A relatively positioned block
  moves with its background.
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
  `border-spacing`. Every cell's background, borders and shadow reach the
  bottom of its row, and its content sits by `vertical-align` (or `valign`)
  given on the table, a row group, the row or the cell, cascaded as a
  browser does: `middle` by default in an HTML table (the UA sheet's
  `tbody`, also the implicit one), `inherit` taking the table's value
  (Bootstrap 5), `top`/`bottom` (`text-top`/`text-bottom` count as those),
  and an explicit `baseline` — as CSS resets write it — read as `top`, which
  is also where CSS table boxes (`display: table-cell`) start. A cell's own
  `vertical-align` places its content; it no longer raises or drops the
  cell's text as an inline offset. Collapsed borders resolve
  conflicts by width, style priority (hidden > double > solid > dashed >
  dotted > none), then cell over row over table, and each interior edge
  paints once. A cell with nothing to draw (`<td></td>`, `<td>&nbsp;</td>`)
  still takes its column, one blank line tall when it holds a no-break space
  or a `<br>`. An auto table measures a cell's text on one line (its
  max-content width) and, when the columns want more than the table has,
  each keeps its longest word and gives up the room above it in proportion,
  as a browser's automatic table layout does. A column whose cells are all
  empty is a 2 px sliver and takes none of the spare room.

### Typography and paint

- Words in a script the reader's face lacks (CJK, Arabic, Hebrew, Indic,
  Thai…) are set in a fallback face that has them — a downloaded Noto CJK
  face, a Noto font in JukaHub's fonts folder, or the machine's own — at the
  run's size and weight (`webpage_script.go`). Right-to-left words are
  reversed and Arabic letters joined through Unicode's presentation forms;
  there is no full bidi reordering of phrases and no Indic shaping.
- Colour, background colour, linear/radial/conic gradients and opacity, with
  every CSS colour form (`webpage_color.go`): hex, the 148 named colours at
  their exact values (`red` is #ff0000), the system colours (`Canvas`,
  `CanvasText`, `LinkText`, `ButtonFace`, `Mark`, …) as a light-mode Chrome
  has them, `rgb()`/`hsl()`/`hwb()` in the comma and space syntaxes,
  `lab()`/`lch()`/`oklab()`/`oklch()` (Tailwind CSS 4's palette), `color()`
  in the predefined spaces (display-p3 and the rest mapped into sRGB),
  `color-mix()` (Tailwind 4's opacity modifiers, mixed with premultiplied
  alpha), `light-dark()` (the light value) and relative colours
  (`rgb(from var(--brand) r g b / 50%)`).
- A page that sets no colours is black text on white, links #0000ee. Only an
  `<a>` with an `href` is a link: its colour is the link colour unless the
  page colours the link itself — the colour of the text around it does not
  reach it, as in a browser — and `color: inherit` / `currentColor` takes the
  surrounding colour (Tailwind's reset). `<a name>` is plain text. An
  unstyled link on a dark surface takes Chrome's dark-scheme link colour.
- Translucent colours are what they look like on their backdrop: a text
  colour such as `rgba(0,0,0,.54)` is drawn as that grey, a translucent fill
  (`rgba(255,255,255,.06)` on a dark page) is judged as the colour it shows,
  a background with alpha 0 (`rgba(0,0,0,0)`, `transparent`) is no surface,
  and a translucent page colour is composited over white. `color:
  transparent` keeps the words' room and paints nothing. A transparent
  gradient stop fades its neighbour in or out (premultiplied), not through
  grey.
- The reader keeps the author's text colour. It only replaces one that would
  all but vanish into the surface the reader knows for it (contrast under
  1.5:1 — the page assumed a background the reader could not draw); text over
  a picture (a covering background image, or a positioned overlay on an
  `<img>`) and text with a contrasting shadow are never changed.
- Borders and outlines without a colour are drawn in `currentColor`; a
  transparent one (Bootstrap's `.btn` and nav tabs, Tailwind's
  `outline-none`) draws nothing, and a `border-color` later in the cascade
  replaces the colour of a `border` shorthand (`.btn-outline-primary`).
  `text-shadow` inherits; `text-shadow: none` ends an inherited one.
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
- `font-family` chooses between three faces: the UI face; a monospace face
  for `monospace`, `ui-monospace`, the common code families (Menlo, Consolas,
  Courier New, Fira Code, JetBrains Mono, …) and `<pre>`, `<code>`, `<kbd>`,
  `<samp>`, `<tt>`, when the machine has one (`termPtyMonoFontPath`: a
  `*Mono*.ttf` in JukaHub's fonts folder, Consolas or Cascadia Mono on
  Windows, DejaVu, Liberation or Noto Sans Mono on Linux); and a serif face
  for `serif`, `ui-serif` and the common serif families (Georgia, Times,
  Linux Libertine, Cambria, Charter, Merriweather, Lora, "… Serif", "…
  Slab"): a `*Serif*.ttf` in JukaHub's fonts folder, Georgia or Times New
  Roman on Windows, DejaVu, Liberation or Noto Serif on Linux, and otherwise
  the embedded Library serif (Lora), so the handhelds have one too. A word the
  serif face cannot draw stays in the UI face; Legible reading font and Bold
  text keep every run in the UI face. Unknown families keep the inherited
  choice.
- Soft hyphens (`&shy;`) are break points: a word longer than the rest of its
  line breaks at the last one that fits and shows a hyphen there; elsewhere
  they are not drawn.
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
  `outline-offset`; list styles and list-style none. A list whose page sets
  its start padding (`ul { padding: 0 }`) drops the reader's 18px item
  gutter, which stands in for the browser's 40px list padding.
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
- Media features beyond the viewport size are answered as a light-mode desktop
  Chrome on an sRGB screen with reduced motion: `prefers-color-scheme: light`,
  `prefers-reduced-motion: reduce` (content waiting for a fade-in is shown),
  `hover: hover`, `pointer: fine`, `scripting: enabled`, `forced-colors: none`,
  `prefers-contrast: no-preference`, `display-mode: browser`, `color`,
  `monochrome`, `grid` and `resolution` (the reader's CSS pixel scale). A dark
  palette written for `prefers-color-scheme: dark` no longer replaces the
  light one, and a `<picture>` source for dark mode is not picked.

## HTML elements

- Block flow: `body`, `p`, `div`, `section`, `article`, `main`, `header`,
  `footer`, `aside`, `nav`, `form`, `fieldset`, `figure`, `figcaption`,
  `address`, `dl`, `dt`, `dd`, `li`, `tr`, `details`, `summary`, `blockquote`,
  `pre`, `listing`, `h1`–`h6`, `dialog`, `hgroup`, `menu`, `search`,
  `center`.
- The browser's own defaults: `h1`–`h6` are 2, 1.5, 1.17, 1, 0.83 and 0.67
  times their parent's size with Chrome's em margins; `p`, `pre`,
  `blockquote` and `figure` have one em above and below; `blockquote` and
  `figure` are indented 40px on both sides (no rule), `dd` 40px on the left,
  unless the page sets that margin itself;
  `code`, `kbd`, `samp`, `tt` and `pre` are 13/16 of the surrounding size and
  monospace, with no background of their own; `mark` is yellow with black text;
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
  (with `bordercolor`), `cellpadding` and `cellspacing`, `valign` on rows,
  row groups and cells, `nowrap` on cells, `background` (a picture) on the
  body, tables, rows and cells, `hspace`/`vspace` on images, `<font color
  size face>`, and `<body text link bgcolor background>`.
- Replaced/media: `img`, `picture`/`source`, `svg`, `hr`, `video`, `audio`,
  `iframe` embeds. `canvas`, `object`, `embed`, an `<svg>` with nothing to
  draw but a background or border, and media without a playable source keep
  a real placeholder box with their resolved width, height, background and
  border. Their fallback content is not interpreted.
- Forms: `input` (text/password/checkbox/radio/submit/range/etc.), `textarea`,
  `select`/`option`, `button`, `label`, `legend`, `output`, `progress`, `meter`.
  A text field, textarea or select keeps the border and `border-radius` its
  page gives it, a field's `max-width` caps its `width`, a button's `height`
  sizes its box (label in the middle, at most three rows), a field's text
  stands on the line's baseline beside buttons and labels, and controls side
  by side are separate boxes.
- Table structure: `table`, `thead`, `tbody`, `tfoot`, `tr`, `td`, `th`,
  `caption` render rows and cells through the table grid; `caption` is a
  centred block above the rows. `colspan`/`rowspan` claim grid slots, direct
  `width` on cells sizes columns, fixed layout reads the first row, and
  `border-collapse: collapse` leaves one shared edge between neighbours.
  `colgroup`/`col` widths (including `span`) define column tracks in fixed and
  auto layout, with a cell's own width winning where CSS gives it precedence.
  A `<tr>` with no cells still reserves a row when rowspans occupy it.
  A cell holding several blocks (paragraphs, a list, a table) is one cell
  with its blocks stacked in it, its surface painted round all of them over
  the row's height; a table inside a cell or a flex/grid item is laid out at
  that cell's or item's width. Markup a page leaves unclosed (`<td>a<td>b`,
  `<li>` without `</li>`) is closed as HTML's parser closes it, with or
  without scripts.
- `head` metadata and `script`/`style`/`noscript`/`title` content are not
  painted. Raw-text containers (`svg`, `canvas`, `template`, `object`) are not
  interpreted as HTML.

## Accepted but not rendered

These are the remaining visible gaps. They are listed here so they cannot be
mistaken for working CSS.

| Construct | Current behaviour |
| --- | --- |
| `font-family` and `@font-face` | A family of the page's `@font-face` rules is downloaded when the file is TrueType, OpenType or WOFF (a WOFF2-only source is skipped; Google Fonts is asked for TrueType), only its regular and bold weights and only a subset holding Latin letters or private-use icons; italic is the face slanted, and past the per-run caps (8 files a page, 24 MB a run on a handheld) the reader's three faces set the text. Material Icons ligature names still become the nearest glyph the reader has. Font descriptors other than family, weight, style and `unicode-range` are ignored. |
| `display: inline-block` on inline elements (`a`, `span`, `button`) and other block/inline conversion | They are inline boxes kept whole on their line, with their padding, border, radius and background; their own `width` and `height` are not applied, and a label wider than the whole line breaks like text instead of wrapping inside its box. Nested boxes with different backgrounds that end together paint the outer box's right padding in the inner one's colour. A block-level element containing block children inside an inline row is one item; text between inline-block siblings joins the box before it. |
| Width/min-width/max-width on a flex/grid container | Accepted; item widths use the page measure because the container itself has no separate box in this engine. |
| Percentage heights (`height: 50%`, `min-height`, `max-height`) | Resolved when the containing block has a definite height, including `calc()`, `min()`, `max()` and `clamp()` with percentages. An auto-height ancestor leaves them content-sized. |
| aspect-ratio on a wrapper with no direct block | Accepted for ordinary blocks; a wrapper that only colours its children reserves its ratio through its box, not through a text row. |
| `position: sticky` | Kept in normal flow and pinned as a copy of the element's whole subtree (a header's logo, menu and buttons, its floats and positioned children) once it scrolls past its inset, until its containing block (a table for a header cell) scrolls away; `bottom` alone sticks to the bottom edge. A bar taller than the view, or taller than two fifths of it across most of the width, scrolls; links in the pinned copy are reached at the bar's place in the page, and nested scroll containers are not modelled. |
| `position: fixed` too big to pin, and boxes covering the screen | A fixed box taller than the view, or most of it across most of the width (a drawer, a consent panel), scrolls with the page (one on the bottom edge after its last line). A fixed box, or an absolute one with no positioned ancestor (a positioned `<html>`/`<body>` counts as none), that covers the whole screen (`inset: 0`, or most of the viewport both ways: a modal backdrop, a cookie wall) keeps its child blocks in the flow, since the reader cannot press its close button; only its own text is a layer. A fixed box inside a positioned layer stays in that layer's flow. |
| Effects of `isolation`, `filter`, `backdrop-filter`, `will-change`, `contain`, `mix-blend-mode`, `perspective`, `clip-path`, masks | They make stacking contexts (z-index nests inside them) but are not drawn: no blur, blend, clip shape or mask. A relatively positioned element is painted over a layer only where it overlaps one; its floats and videos stay with the flow. |
| `box-shadow` inset; more than one shadow layer; `text-shadow` blur | Only the first visible outer box-shadow layer is painted; a text shadow's blur only lowers its alpha. |
| `transform` (except `translate`), `transition`, `animation`, `filter`, `clip-path` | Accepted or skipped; rotate, scale and skew are not drawn, so content stays upright (a transformed box is still a stacking context). A translated box in the flow is painted moved and keeps its place in the flow. |
| SVG `<text>`, `<image>`, filters, masks, clip paths, patterns, markers, dashes and `<foreignObject>`; external `<use href="sprite.svg#icon">` | Skipped; the rest of the drawing renders. |
| non-rectangular `grid-template-areas` matrices | Accepted; named areas use the bounding box of their cells. |
| Table row groups (`thead`/`tbody`/`tfoot`) as separate layout boxes; borders/backgrounds on `colgroup`/`col` | Rows share one table grid; column width and span are applied, but column-level borders/backgrounds are not painted. |
| `list-style-image`, `list-style-position` | Markers are text only (an image marker draws the list's text marker). `list-style-position: outside` hangs the marker left of the text when the page sets the list's own start padding (Wikipedia's `ul { margin-left: 1.6em; padding: 0 }`); a list on the browser's default padding keeps the marker before the text inside its 40px gutter. A list styled `display: inline` stays in its line (Wikipedia's `.hlist.inline`). |
| `background-clip: text` | The glyphs take the gradient's average colour; the ramp is not clipped to the letter shapes. |
| `line-clamp` on a wrapper whose text sits in child blocks | Only the element's own lines are clamped; a clamp on a wrapper of paragraphs does not count their lines. |
| `vertical-align: baseline` and lengths in table cells | Read as top. |
| Presentation attributes not listed above (`frame`/`rules`, a table's `align="left"/"right"`) | Ignored; CSS for the same properties works. |
| `::first-line`, `::first-letter`, `::selection`, `::placeholder`, `::backdrop`, scrollbar/form-control pseudo-elements | Not generated; their rules do not style the element. |
| `content: url(...)` and image content | Skipped; generated content is text only. |
| `counter-reset`/`counter-increment` inside `::before`/`::after` | Parsed on the pseudo-element; counter operations are applied on elements, not generated boxes. |
| `@charset`, `@namespace` | Not applied. |
| `@keyframes`, `@page` (and `@font-face` beyond the row above) | Not modelled as rendering features. `@media`, `@supports` and `@container` are evaluated, and `@layer` orders the cascade. |
| Floats inside a line, nested floated boxes, `shape-outside` | A floated inline element (`<span style="float:right">`) stays in its line, and a floated `<img>` floats on its own. Inside a floated box, inside a table cell, or past 600 floated boxes on a page, a floated element floats only its own text; a floated box is as wide as the floats inside it side by side (a floated menu of floated items). A block that holds its floats and starts beside a float has its lines wrapped one by one, not its whole box narrowed. `shape-outside` is ignored: floats are rectangles. |

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
| Floats and `clear` | `FloatsAndClear` | Float shrink-to-fit uses the widest picture, line or table row; text beside a float wraps line by line. |
| Four-side borders, styles, radius | `BordersAndRadius` | Radius is clamped to the used box; no elliptical radii. |
| Opacity | `OpacityPaintsThrough` | Opacity multiplies paint, not an isolated compositing group. |
| Transforms | `TransformsStayInFlow` | `translate()` moves an in-flow box without taking it out of the flow; `rotate()` is not drawn, so content stays readable. |
| Text alignment, decoration, line height, letter spacing | `TextMetricsAndDecoration` and `JustifiedText` | Justification stretches automatic word gaps; a line with no gaps stays left-aligned. |
| Table borders, collapsed separators, padding, spans | `TableSpanPaddingAndBorders` | Column-level borders/backgrounds are not painted. |
| Image dimensions, natural size, `object-fit`/`max-width` | `ImageSizingAndFit` | Natural size is capped to the page width and four-thirds screen height. |
| Background colour/image layers, repeat, position | `BackgroundLayersRepeatAndPosition` | Four layers and 512 tiles per block are the cap. |
| Relative/absolute/fixed offsets | `PositionedOffsets` | `fixed` is pinned to the view (the fixture paints at scroll 0). A positioned subtree is one layer (`StickyHeaderAndOverlay`). |
| Media queries and meta viewport | `MediaQueriesAndViewportMeta` | Width-based `@media` is evaluated against the reader viewport; print queries do not apply, and device features (colour scheme, hover, pointer, motion) answer as a light-mode desktop browser with reduced motion. |
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
