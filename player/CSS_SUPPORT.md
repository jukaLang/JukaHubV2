# Web CSS support

The built-in reader is a text-and-box engine, not Chromium. This file is the
contract for what its CSS layer renders and what it deliberately does not. A
property that is accepted by the parser but listed as unsupported is a known
gap, not a silent promise.

## Rendered

### Selectors and generated content

- Type, class, id, attribute, descendant/child/sibling selectors.
- `:root`, `:link`, `:visited`, `:hover`, `:focus`, `:target`, `:checked`,
  `:disabled`, `:enabled`, `:required`, `:optional`, `:read-only`,
  `:placeholder-shown`, `:empty`, `:defined`, `:lang()`, `:dir()`.
- `:not()`, `:is()`, `:where()`, `:has()`, `:first-child`, `:last-child`,
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

- `display: block`, `flex`, `grid` (including explicit columns and
  `repeat(auto-fit/auto-fill, minmax(...))`), and the table display modes
  `table`, `table-row`, `table-cell`, `table-header-group`, `table-row-group`,
  `table-footer-group`, `table-caption`.
- Flex direction, wrap, justify, align, gap, grow, shrink, basis, order.
- Width (lengths and percentages), pixel `height`, `min-width`/`max-width`,
  `min-height`/`max-height`, `box-sizing: content-box/border-box` (horizontal
  width; vertical padding is not folded into height), aspect ratio on
  replaced media, margins, paddings, logical margin/padding variants, auto
  margins on width-constrained blocks and on flex/grid items (main and cross
  axes), text indent, border radius.
- `float: left/right` and the logical `inline-start`/`inline-end` spellings (a
  following block is narrowed beside it; `clear: left/right/both` and the
  logical spellings move a block below the float). Floats shrink to their
  widest run when no width is given.
- `position: relative/absolute/fixed`, physical and logical inset properties,
  z-index.
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
- Font size, weight (faux bold), text transform, text align, line height,
  `font-style: italic/oblique` (synthetic italic face), `letter-spacing`,
  `word-spacing`, `white-space: pre*`.
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
| `display: contents` and block/inline conversion | Accepted; the reader’s block flow is chosen by the HTML tag, so only `flex`/`grid`/table display modes on block tags change container behaviour. |
| Width/min-width/max-width on a flex/grid container | Accepted; item widths use the page measure because the container itself has no separate box in this engine. |
| Percentage heights (`height: 50%`, `min-height`, `max-height`) | Accepted; percentage heights are ignored because the parent height is not known while the flow is measured. Use a pixel height. |
| aspect-ratio on an ordinary block | Accepted; only replaced media uses it, because the reader has no implicit content box ratio for text flow. |
| `position: sticky` | Accepted; treated as fixed/absolute, because the reader has no scrolling sticky state. |
| `box-shadow` blur, spread, inset; `text-shadow` blur | Accepted; only the first offset/colour is painted. |
| `text-overflow` | Accepted; the page uses its own ellipsis. |
| `transform`, `transition`, `animation`, `filter`, `clip-path` | Accepted or skipped; no visual transform. |
| `grid-template-rows`, `grid-row`, `grid-column`, `grid-area` | Accepted; only explicit columns and auto-fit rows are modelled. |
| `white-space: nowrap/pre-wrap` details | Accepted; `pre*` keeps code lines, other wrapping modes are approximated. |
| Table row groups (`thead`/`tbody`/`tfoot`) as separate layout boxes; borders/backgrounds on `colgroup`/`col` | Rows share one table grid; column width and span are applied, but column-level borders/backgrounds are not painted. |
| `list-style-image`, `list-style-position` | Accepted; markers are text only. |
| `::first-line`, `::first-letter`, `::selection`, `::placeholder`, `::backdrop`, scrollbar/form-control pseudo-elements | Not generated; their rules do not style the element. |
| `content: url(...)` and image content | Skipped; generated content is text only. |
| `counter-reset`/`counter-increment` inside `::before`/`::after` | Parsed on the pseudo-element; counter operations are applied on elements, not generated boxes. |
| `text-align: justify` | Accepted; lines align left because the reader does not redistribute inter-word space. |
| `@import`, `@charset`, `@namespace` | Not fetched or applied. |
| `@font-face`, `@keyframes`, `@page`, `@layer` ordering | Not modelled as rendering features. `@media`, `@supports` and `@container` are evaluated. |

## Adding support

A new property must do all three:

1. Parse into `webStyle` (and reset it if it is not inherited).
2. Change layout or paint, not just the style struct.
3. Add a render-output test (generated text, run geometry, layout rectangle or
   paint colour), not only a parse assertion.

The tests in `player/webpage_css_cascade_test.go` and
`player/webpage_flow_layout_test.go` are the examples to follow.
