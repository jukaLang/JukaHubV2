package main

// webpage.go — styled HTML rendering for the built-in browser.
//
// A stock TrimUI Smart Pro has no WebKit/Chromium, no window manager and 1 GB
// of shared RAM, so embedding a browser engine is not an option. Instead this
// reader parses the page's HTML5 markup itself, resolves the part of CSS that
// decides how text looks (colour, size, weight, alignment, spacing,
// backgrounds, lists, quotes and code blocks) and draws the result with the
// app's own SDL/TTF pipeline.
//
// The parser is streaming: it never keeps a DOM, only the styled text blocks it
// is about to draw, and it stops at fixed byte/block limits. A styled page
// therefore costs a few hundred kilobytes on a 1 GB handheld instead of the
// tens of megabytes a real engine would need.

import (
	"context"
	htmlstd "html"
	"io"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// webMaxTextBytes bounds how much readable text one page may contribute.
	webMaxTextBytes = 1536 << 10
	// webMaxBlocks bounds the block list so a pathological page cannot make
	// the layout take seconds on the handheld. 1800, then 4000, cut long
	// articles and busy news front pages off part-way down.
	webMaxBlocks = 8000
	// webMaxStylesheets and webSheetByteCap bound external CSS fetching. Real
	// sites link a theme, a framework and several plugin sheets, and their main
	// sheet is often well past 256 KiB: the old limits (3 sheets, and a larger
	// sheet dropped whole) left most pages unstyled. A sheet past the cap keeps
	// its complete rules up to the cap (webCSSBudgetPrefix).
	webMaxStylesheets = 64
	webSheetByteCap   = 4 << 20
	// webMaxLinkedCSSBytes is the share of webMaxCSSBytes linked sheets may
	// take, so the page's own <style> blocks — often its critical layout CSS —
	// always keep room after them.
	webMaxLinkedCSSBytes = 16 << 20
	webMaxCSSBytes       = 24 << 20
	// A linked sheet may @import its base sheet. Keep the chain short: imports
	// are real requests too.
	webMaxStyleImports     = 16
	webMaxStyleImportDepth = 4
	// webMaxRows bounds the laid-out row count (each row is one text line).
	webMaxRows = 30000
)

// webElement is the little of an open element the CSS matcher needs. attrs is
// a pointer so every copy of the element (its frame, and each chain built while
// its descendants are styled) shares one attribute list, parsed at most once
// and only when a selector asks for an attribute. target marks the element the
// page address's fragment names, which is what :target matches.
type webElement struct {
	tag     string
	id      string
	classes []string
	attrs   *webAttrSet
	target  bool
	// node points at the selector tree node for this element. The streaming
	// parser keeps no DOM, so a page with :has() builds a small structural tree
	// ahead of the walk; ordinary pages leave this nil.
	node     *webElement
	parent   *webElement
	children []*webElement
}

// The layout enums are small integers rather than strings: they are copied into
// every block and compared while laying out, and the reader runs on a handheld.
const (
	webDisplayBlock = iota
	webDisplayFlex
	webDisplayGrid
	webDisplayTable
	webDisplayTableRow
	webDisplayTableCell
	webDisplayTableHeaderGroup
	webDisplayTableRowGroup
	webDisplayTableFooterGroup
	webDisplayTableCaption
)

// Table roles survive CSS display modes: a div with display:table-row is the
// same row block as a <tr>, because layout consumes the role rather than the
// tag name.
const (
	webTableRoleNone = iota
	webTableRoleTable
	webTableRoleRow
	webTableRoleCell
	webTableRoleHeaderGroup
	webTableRoleRowGroup
	webTableRoleFooterGroup
	webTableRoleCaption
	webTableRoleColumnGroup
	webTableRoleColumn
)

const (
	webTableLayoutAuto = iota
	webTableLayoutFixed
)

const (
	webBorderCollapseSeparate = iota
	webBorderCollapseCollapse
)

const (
	webFlexRow = iota
	webFlexColumn
)

const (
	webPositionStatic = iota
	webPositionRelative
	webPositionAbsolute
	webPositionFixed
)

const (
	webItemStart = iota
	webItemCenter
	webItemEnd
	webItemStretch
)

const (
	// Auto keeps the reader's historic cover crop when no object-fit is
	// declared; fill is an explicit author request to stretch.
	webObjectFitAuto = iota
	webObjectFitFill
	webObjectFitContain
	webObjectFitCover
	webObjectFitNone
	webObjectFitScaleDown
)

const (
	webBorderSolid = iota
	webBorderDashed
	webBorderDotted
	webBorderDouble
	webBorderNone
	webBorderHidden
)

const (
	webDecorationSolid = iota
	webDecorationDouble
	webDecorationDotted
	webDecorationDashed
	webDecorationWavy
)

const (
	webVerticalBaseline = iota
	webVerticalMiddle
	webVerticalTop
	webVerticalBottom
	webVerticalTextTop
	webVerticalTextBottom
	webVerticalLength
	// Super and sub raise or drop the baseline; the shift itself is carried
	// by webStyle.BaselineShift so inline descendants move with the box.
	webVerticalSuper
	webVerticalSub
)

const (
	webJustifyStart = iota
	webJustifyCenter
	webJustifyEnd
	webJustifyBetween
	webJustifyAround
	// Evenly puts the same space before, between and after the items; it used
	// to read as space-around, whose ends get half a gap.
	webJustifyEvenly
)

// Content alignment is the block-axis counterpart of justify-content. It
// needs its own values because space-evenly and stretch are distinct grid
// behaviours, not aliases for space-around or start.
const (
	webContentStart = iota
	webContentCenter
	webContentEnd
	webContentBetween
	webContentAround
	webContentEvenly
	webContentStretch
)

const webMaxBackgroundLayers = 4

// webWhiteSpace is the CSS white-space mode. Normal collapses runs of spaces
// and wraps; nowrap collapses but never wraps; pre preserves both spaces and
// newlines without wrapping; pre-wrap preserves both and still wraps;
// pre-line collapses spaces but keeps authored newlines.
const (
	webWhiteSpaceNormal = iota
	webWhiteSpaceNowrap
	webWhiteSpacePre
	webWhiteSpacePreWrap
	webWhiteSpacePreLine
	webWhiteSpaceBreakSpaces
)

func webWhiteSpaceValue(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "nowrap":
		return webWhiteSpaceNowrap
	case "pre":
		return webWhiteSpacePre
	case "pre-wrap":
		return webWhiteSpacePreWrap
	case "pre-line":
		return webWhiteSpacePreLine
	case "break-spaces":
		return webWhiteSpaceBreakSpaces
	}
	return webWhiteSpaceNormal
}

func webWhiteSpacePreservesSpaces(mode int) bool {
	return mode == webWhiteSpacePre || mode == webWhiteSpacePreWrap || mode == webWhiteSpaceBreakSpaces
}

func webWhiteSpaceWraps(mode int) bool {
	return mode != webWhiteSpaceNowrap && mode != webWhiteSpacePre
}

func webOverflowWrapValue(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "anywhere":
		return 1
	case "break-word":
		return 2
	default:
		return 0
	}
}

// webWordPrefixThatFits finds a UTF-8 boundary for an emergency line break.
// The first rune is always kept so even an unusually wide glyph makes progress.
func webWordPrefixThatFits(word string, width int32, measure func(string) int32) (string, string) {
	if word == "" || width <= 0 {
		return "", word
	}
	boundaries := make([]int, 0, min(len(word), 256))
	for i := range word {
		if i > 0 {
			boundaries = append(boundaries, i)
		}
	}
	boundaries = append(boundaries, len(word))
	lo, hi := 0, len(boundaries)
	for lo < hi {
		mid := (lo + hi) / 2
		if measure(word[:boundaries[mid]]) <= width {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	idx := max(0, lo-1)
	end := boundaries[idx]
	return word[:end], word[end:]
}

func webClipWordToWidth(word string, width int32, measure func(string) int32) string {
	prefix, _ := webWordPrefixThatFits(word, width, measure)
	if measure(prefix) > width {
		return ""
	}
	return prefix
}

// webPreWrapTokens splits a preserved line into measured chunks. Each run of
// spaces is attached to the word after it, so a wrapped line keeps the
// author's indentation instead of losing it at the break. A line made only of
// spaces stays one token and remains visible.
func webPreWrapTokens(s string) []string {
	var tokens []string
	var word, spaces strings.Builder
	flushWord := func() {
		if word.Len() == 0 {
			return
		}
		tokens = append(tokens, word.String())
		word.Reset()
	}
	for _, r := range s {
		if r == ' ' || r == '\t' {
			flushWord()
			if r == '\t' {
				spaces.WriteString("    ")
			} else {
				spaces.WriteRune(r)
			}
			continue
		}
		if spaces.Len() > 0 {
			word.WriteString(spaces.String())
			spaces.Reset()
		}
		word.WriteRune(r)
	}
	flushWord()
	if spaces.Len() > 0 {
		tokens = append(tokens, spaces.String())
	}
	return tokens
}

// webBackgroundLayerStyle is one CSS background layer. Pointers keep the
// style comparable, so runs can still merge on style equality.
type webBackgroundLayerStyle struct {
	Image       string
	Gradient    *webGradient
	HasGradient bool
	Size        string
	Position    string
	Repeat      string
}

// webStyle is a resolved style for one element. Zero values are meaningful:
// HasColor/HasBg distinguish "author asked for this" from "inherit".
type webStyle struct {
	Color      sdl.Color
	HasColor   bool
	Background sdl.Color
	HasBg      bool
	// Backdrop is the nearest ancestor surface (or this element's own one)
	// that text sits on. It inherits, unlike the background itself, so white
	// copy inside a dark hero keeps its author colour instead of being
	// contrast-flipped against the page's white paper.
	Backdrop    sdl.Color
	HasBackdrop bool
	// ImageBackdrop marks text over a picture: a covering background image
	// on the element or an ancestor, or a positioned overlay inside a box that
	// holds an <img>. Its real backdrop is the photo, whose colour the reader
	// does not know, so the author's text colour is kept as it is rather than
	// judged against Backdrop (webReadableColor). Inherited; an opaque fill
	// below it clears it.
	ImageBackdrop bool
	// BackgroundImage is a resolved url() background. The layout carries a
	// background layer into layout.Images so it paints under the text.
	BackgroundImage string
	// BackgroundSize keeps the raw CSS size; the layout resolves it against
	// the padding box and the image's natural dimensions.
	BackgroundSize     string
	BackgroundPosition string
	BackgroundRepeat   string
	// BackgroundLayers holds the full comma-separated layer list in CSS order
	// (first layer on top). The single fields above mirror layer zero for
	// compatibility with code and tests that predate the layer list.
	BackgroundLayers     [webMaxBackgroundLayers]webBackgroundLayerStyle
	BackgroundLayerCount int
	// Gradient is the ramp a background-image gradient resolved to. HasBg is
	// still set (to the ramp's average) so contrast, fade and inheritance keep
	// working with one colour; the ramp wins where the reader paints. It is a
	// pointer only so a style stays comparable (runs merge on style equality)
	// and so a hundred runs can share one parsed ramp.
	Gradient    *webGradient
	HasGradient bool
	FontPx      float64
	Bold        bool
	Italic      bool
	Underline   bool
	Strike      bool
	Link        bool
	Align       int
	WhiteSpace  int
	// OverflowWrap and WordBreak are inherited CSS text break controls.
	// OverflowWrap: 0 normal, 1 anywhere, 2 break-word. WordBreak: 0 normal,
	// 1 break-all. Long runs use them before the reader's ellipsis fallback.
	OverflowWrap int
	WordBreak    int
	// Pre is retained for code that only needs "preserve authored text". New
	// layout uses WhiteSpace, which distinguishes pre, pre-wrap and pre-line.
	Pre    bool
	Hidden bool
	// Invisible is CSS visibility:hidden. Unlike Hidden (display:none or an
	// aria-hidden helper), it keeps the element's box and text metrics in the
	// flow and suppresses only the paint. It inherits, and visibility:visible
	// on a descendant clears it.
	Invisible bool
	// VerticalAlign is the inline alignment mode and VerticalAlignPx its
	// explicit length. The measured run carries the resulting pixel offset.
	VerticalAlign   int
	VerticalAlignPx float64
	// OverflowX/Y: 0 visible, 1 hidden, 2 clip.
	OverflowX int
	OverflowY int
	ScrollX   bool
	// OverflowBFC is set by an overflow other than visible or clip (hidden,
	// auto, scroll) and FlowRoot by display: flow-root: either element holds
	// the floats inside it and sits beside the floats around it
	// (webpage_floats.go).
	OverflowBFC bool
	FlowRoot    bool
	// TextOverflow: 0 reader fallback, 1 clip, 2 ellipsis. Unlike word
	// breaking it belongs to the box and does not inherit.
	TextOverflow int
	Indent       float64
	TextIndent   float64
	MarginTop    float64
	MarginBot    float64
	// PadTop/PadBot are the vertical padding part of MarginTop/MarginBot.
	// The flow still reserves the combined top/bottom inset, but the painted
	// box must extend over the padding while leaving the margin outside it.
	PadTop float64
	PadBot float64
	// PadLeft/PadRight are the horizontal padding inside Indent/IndentRight,
	// which also hold the margins: an inline element's background covers its
	// padding but not its margin (webInlineInsets).
	PadLeft  float64
	PadRight float64
	// MarginTopAuto/MarginBotAuto remember `margin: auto` on the block axis.
	// Block flow ignores them; flex cross-axis and grid rows use them after
	// the item's own height is known.
	MarginTopAuto bool
	MarginBotAuto bool
	// Transform is the author's text-transform (0 none, 1 uppercase,
	// 2 lowercase, 3 capitalise). It is applied when a word is measured so
	// the wrapped width matches the glyphs that get drawn.
	Transform int
	// Borders are reported separately: a rule under a heading and a card's
	// full outline are drawn differently from a quote's left bar.
	BorderLeft     bool
	BorderRight    bool
	BoxBorder      bool
	BorderTop      bool
	BorderBottom   bool
	BorderColor    sdl.Color
	HasBorderColor bool
	// BorderStyle is the shared style for the border box and side bars;
	// none/hidden suppress paint while keeping the width state.
	BorderStyle int
	// Outline is a separate box edge from the border, so outline-offset can
	// move it without moving a border that shares the same element.
	Outline       bool
	OutlineOffset float64
	// DecorationColor paints underlines and strikethroughs when the sheet
	// sets text-decoration-color. HasDecorationColor keeps the text colour as
	// the default, which is what a browser does.
	DecorationColor    sdl.Color
	HasDecorationColor bool
	// DecorationThickness is a pixel line thickness for underlines and
	// strikethroughs. Zero keeps the historical one-pixel line.
	DecorationThickness float64
	DecorationStyle     int
	// DecorationOffset is text-underline-offset in CSS px: how far below its
	// usual place the rule sits. A negative offset pulls it toward the
	// glyphs, which is what a dotted link underline under tight type wants.
	DecorationOffset float64
	// TextShadow is one offset shadow behind the glyphs. Blur is not
	// modelled; the offset and colour are, which is what makes a heading
	// lift off a tinted hero. HasShadowColor distinguishes an author colour
	// from currentColor.
	HasTextShadow      bool
	TextShadowX        float64
	TextShadowY        float64
	TextShadowColor    sdl.Color
	HasTextShadowColor bool
	// BoxShadow is the first outer shadow of a block. Inset shadows are not
	// modelled; an outer offset still gives cards and menus their depth.
	HasBoxShadow      bool
	BoxShadowX        float64
	BoxShadowY        float64
	BoxShadowColor    sdl.Color
	HasBoxShadowColor bool
	// BorderWidth is the widest declared border in CSS px; 0 keeps the
	// reader's own default (3 for a side bar, 2 for a rule or an outline).
	// A card written as `border: 3px` used to draw 2px, which reads as a
	// different design rather than as a smaller border.
	BorderWidth float64
	// BorderSideWidth keeps each edge's own CSS width; border-collapse
	// resolves conflicts per edge, while BorderWidth stays the max for
	// ordinary painting.
	BorderSideWidth    [4]float64
	BorderSideStyle    [4]int
	BorderSideStyleSet [4]bool
	BorderSideColor    [4]sdl.Color
	BorderSideHasColor [4]bool
	ListNone           bool
	// ListStyle is the marker the author asked for (0 = the reader default).
	// It inherits like a real browser's, so `ul { list-style: none }` reaches
	// the items, which is how nav menus hide their bullets.
	ListStyle int
	// ListInside is list-style-position: inside, inherited like ListStyle.
	ListInside bool
	// ListString is the text of a string marker (webListStyleString).
	ListString string
	// SmallCaps is font-variant(-caps): small-caps and its kin, inherited:
	// lower-case letters are set as capitals a size smaller.
	SmallCaps bool
	// ListItem is display: list-item, an <li>'s default: only a list item
	// draws a marker and counts in its list (`li { display: block }` or a
	// flex <li> has none). Not inherited.
	ListItem bool
	// ListPadSet is set on a ul, ol or menu whose page sets its start padding
	// (a reset's `* { padding: 0 }`, Bootstrap's `.nav`): its items drop the
	// reader's 18px list gutter, which stands in for the UA sheet's 40px of
	// list padding the page has replaced (webParser.dropListGutter).
	ListPadSet bool
	// LineHeight is the author's line-height in CSS px; 0 leaves the reader's
	// row grid in charge. It inherits, so `body { line-height: 1.6 }` spaces
	// every paragraph the way the design intended.
	LineHeight float64
	// LetterSpacing and WordSpacing are CSS px; 0 is normal. Both inherit,
	// and both change wrapping because the measured word/space grows by the
	// same amount that drawing advances by.
	LetterSpacing float64
	WordSpacing   float64
	// Radius is border-radius in CSS px; 0 keeps the reader's crisp corners.
	// A percentage (or a large value) is clamped to half the box at draw time.
	Radius float64
	// HasOpacity/Opacity carry the element's alpha. It is applied when text,
	// fills and borders are painted, by blending toward whatever is behind
	// them; children inherit the fade, like a real opacity group.
	HasOpacity bool
	Opacity    float64
	// IndentRight is padding-right + margin-right in CSS px: it narrows the
	// measure so an inset card wraps where the author intended.
	IndentRight float64
	// MarginLeftAuto/MarginRightAuto remember `margin: auto` on the inline
	// sides. The numeric margin is zero until layout knows the used width; a
	// constrained block can then take the free space on that side (or split it
	// when both sides are auto), which is how a `max-width` page centres.
	MarginLeftAuto  bool
	MarginRightAuto bool
	// --- Flex and stacking -------------------------------------------------
	// Display is block or flex. A flex container lays its direct children out
	// side by side (row) or one under the other (column); the reader has no
	// DOM, so the parser marks each direct child as one flex item and the
	// layout stacks those items into columns.
	Display int
	// DisplayContents removes this element's layout box while its children
	// remain direct participants in the parent flex or grid container.
	DisplayContents bool
	// DisplayInline lets CSS turn a normally block-level heading or container
	// into inline content (Wikipedia's welcome heading uses this for its comma).
	DisplayInline bool
	// DisplayBlock records an explicit block-level display (block, flow-root,
	// flex, grid, table). Default display is block for every element, so the
	// parser cannot otherwise tell a styled <label> or <span> from an inline
	// one; without this flag `label{display:block}` left a form label on the
	// same line as its input.
	DisplayBlock bool
	FlexDir      int
	FlexReverse  bool
	Justify      int
	AlignItems   int
	AlignContent int
	// JustifyItems is the inline-axis item default (justify-items), and
	// JustifySelf is the item's own override (-1 means inherit).
	JustifyItems int
	JustifySelf  int
	GapX, GapY   float64
	FlexWrap     bool
	GridCols     int
	// GridMin is the minimum track width from a responsive
	// repeat(auto-fit|auto-fill, minmax(Npx, 1fr)); 0 means the track count
	// is explicit (or not a grid).
	GridMin float64
	// GridWeights and GridBases keep explicit tracks such as
	// `grid-template-columns: 240px 1fr`. A fixed track has its pixel width in
	// GridBases; a flexible one has its fr factor in GridWeights. The old
	// parser kept only the count and divided the page into equal columns, so
	// a sidebar was as wide as the article beside it.
	GridWeights [6]float64
	GridBases   [6]float64
	// GridRows and GridRowWeights/GridRowBases keep explicit
	// `grid-template-rows` tracks. A fixed row track has its CSS height in
	// GridRowBases; an auto/fr track stays content-sized.
	GridRows       int
	GridRowWeights [6]float64
	GridRowBases   [6]float64
	// GridTrackSpecs/GridRowTrackSpecs keep the full minmax bounds. They are
	// populated for explicit tracks and are used by the sizing pass; the older
	// weights/bases arrays remain the compact fallback for simple tracks.
	GridTrackSpecs    [6]webGridTrackSpec
	GridRowTrackSpecs [6]webGridTrackSpec
	// GridAutoRepeat is 1 for repeat(auto-fill, …) and 2 for
	// repeat(auto-fit, …). The repeated definition lives in GridAutoTrack and
	// its row counterpart; the repetition count is computed from the
	// container measure during layout.
	GridAutoRepeat    int
	GridAutoTrack     webGridTrackSpec
	GridRowAutoRepeat int
	GridRowAutoTrack  webGridTrackSpec
	// GridAutoColSpecs/GridAutoRowSpecs size implicit tracks created by
	// placement beyond the explicit templates. The list cycles, as CSS does.
	GridAutoColSpecs [6]webGridTrackSpec
	GridAutoColCount int
	GridAutoRowSpecs [6]webGridTrackSpec
	GridAutoRowCount int
	// GridAutoFlow is 0 for row flow and 1 for column flow. GridDense asks the
	// auto-placement pass to restart its search for every unpositioned item.
	GridAutoFlow int
	GridDense    bool
	// GridColumnStart/End/Span place this element inside its parent grid's
	// column tracks. Start and End are 1-based grid lines; Span counts tracks.
	// End == -1 means the last line, and Span == -1 fills from Start to the
	// last line.
	GridColumnStart int
	GridColumnEnd   int
	GridColumnSpan  int
	// GridRowStart/End/Span are the block-axis counterparts.
	GridRowStart int
	GridRowEnd   int
	GridRowSpan  int
	// GridAreas is the parent grid's named-area map from
	// `grid-template-areas`. GridArea names the area this item asks for with
	// `grid-area: header`; both are resolved by the grid layouter.
	GridAreas *webGridAreas
	GridArea  string
	// FlexGrow/Shrink/Basis describe the element as a flex item. Basis is -1
	// when the author did not state one (auto), which is different from 0px.
	FlexGrow   float64
	FlexShrink float64
	FlexBasis  float64
	BasisPct   float64
	// Order is the flex/grid `order` value; stable sorting keeps source order
	// for equal values, as a browser does.
	Order     int
	AlignSelf int
	// Width/WidthPct size a flex item explicitly, the way a card with
	// `width: 33%` or `width: 240px` does. On an ordinary block they bound
	// the text measure and the painted box in layoutBlocks. WidthFit is
	// width: fit-content or max-content (1) or min-content (2): the box is as
	// wide as what it holds (webLayouter.fitBoxWidth).
	Width    float64
	WidthPct float64
	WidthFit int
	Height   float64
	MinWidth float64
	// MinWidthAuto keeps `min-width: auto` distinct from an explicit
	// `min-width: 0`, which disables a flex item's content-based minimum.
	MinWidthAuto bool
	MaxWidth     float64
	MinWidthPct  float64
	MaxWidthPct  float64
	MinHeight    float64
	MaxHeight    float64
	// BoxSizing is 1 for border-box: an explicit width includes the
	// horizontal padding the parser folded into Indent/IndentRight.
	BoxSizing int
	// --- Table geometry ----------------------------------------------------
	// TableID groups a table's rows and cells; TableRole says what each block
	// is in that model. TableRow is the source row index, TableCol the resolved
	// column (assigned by layout after rowspans have claimed slots), and
	// ColSpan/RowSpan are already clamped by the parser.
	TableID          int
	TableRole        int
	TableRow         int
	TableCol         int
	ColSpan          int
	RowSpan          int
	TableLayout      int
	BorderCollapse   int
	TableSpacingX    float64
	TableSpacingY    float64
	TableWidthPx     float64
	TableWidthPct    float64
	TableColumnCount int
	TablePrepared    bool
	TableCols        *webTableColumns
	// TableRowGroup is the flex group of the row a cell sits in (a <tr> is a
	// flex row: webpage.go push), inherited by everything inside the cell. A
	// layout pass prepares a table only where its cells' blocks have that group
	// as their own: a table inside another table's cell, or inside a flex or
	// grid item, waits for that cell's or item's pass (webpage_table.go). Zero
	// for a CSS table cell with no row around it.
	TableRowGroup int
	// FloatSide is 0 none, 1 left, 2 right. Clear is 0 none, 1 left,
	// 2 right, 3 both. These are per element and do not inherit.
	FloatSide int
	Clear     int
	// AspectRatio is a CSS width/height ratio (16/9), used by replaced
	// elements when the stylesheet supplies it.
	AspectRatio float64
	// ObjectFit controls how replaced content fills its box: 0 fill, 1 contain,
	// 2 cover, 3 none, 4 scale-down. It is stored on the image box so paint can
	// crop or letterbox without re-reading CSS.
	ObjectFit int
	// ObjectPosition is the raw object-position pair; the paint path resolves
	// percentages and lengths against the fitted box.
	ObjectPosition string
	// ContainerType marks a CSS containment container: 0 normal, 1
	// inline-size and 2 size. The parser uses the nearest one to resolve
	// container-relative units in an image sizes attribute and to evaluate
	// @container size queries. ContainerName is the optional query name.
	ContainerType int
	ContainerName string
	// ZIndex and Position place an element in the stacking order. Absolute and
	// fixed elements leave the flow and are painted over the text in z order.
	ZIndex   int
	Position int
	// ZSet records an integer z-index (auto leaves it false): with a position
	// other than static it makes the element a stacking context, which orders
	// the layers inside it among themselves (webpage_layers.go).
	ZSet bool
	// Sticky marks position:sticky. The element stays in normal flow and
	// layout records a pinned copy for the renderer once it scrolls past.
	Sticky    bool
	Top       float64
	Right     float64
	Bottom    float64
	Left      float64
	HasTop    bool
	HasRight  bool
	HasBottom bool
	HasLeft   bool
	// Href is the target of the anchor this text belongs to, empty for every
	// other text. Elements inside an <a> inherit it, so a bold or spanned
	// label still knows where it points and the browser can walk links with
	// the D-pad.
	Href string
	// Control is set on the text a form control renders as (a field's
	// placeholder, a button's label), so a tap or A on it opens the keyboard
	// instead of following anything. Deliberately not inherited from a parent:
	// a control is one run, not a subtree.
	Control *webControl
	// Content is the cascaded value of the `content` property. It is only
	// consulted on a generated box (::before, ::after, ::marker); ordinary
	// elements keep it empty.
	Content string
	// CounterReset, CounterIncrement and CounterSet are the raw counter
	// operations for this element. They are applied to the parser's counter
	// scopes when the element opens, so `content: counter(...)` in the same
	// element's ::before sees the new value.
	CounterReset     string
	CounterIncrement string
	CounterSet       string
	// Vars is the custom-property scope in force for this element. Every
	// var() is already resolved by the time a style reaches a block, so this
	// only carries inheritance down the parse; it stays a pointer so a
	// webStyle is still comparable and adjacent runs still merge.
	Vars *webVarScope
	// CQ keeps the declarations that used container-relative units, so layout
	// can apply them again once a measured container size is known. It is a
	// pointer for the same reason as Vars: styles stay comparable.
	CQ *webCQContext
	// SVGFill and SVGStroke are the cascaded CSS fill and stroke, kept as raw
	// text (currentColor stays symbolic). They inherit, as in CSS, and reach an
	// inline <svg> as its root paint (webpage_svg.go): `.octicon { fill:
	// currentColor }` is how an icon takes the colour of the text beside it.
	SVGFill   string
	SVGStroke string
	// Mono, Serif and IconFont are inherited typography flags
	// (webpage_fonts.go). Mono draws the run in a monospace face: <pre>,
	// <code>, <kbd>, <samp>, <tt> or a monospace font-family. Serif draws it in
	// a serif face (a serif font-family: Georgia, Times, "Source Serif"...).
	// IconFont marks a Material Icons/Symbols ligature font, whose words are
	// icon names rather than text.
	Mono     bool
	Serif    bool
	IconFont bool
	// WebFont is 1 + the index in webDocument.WebFamilies of the web font
	// (@font-face) family the run is set in, 0 for the reader's own faces.
	// Inherited like font-family (webpage_webfont.go).
	WebFont int
	// BoxShadowBlur/BoxShadowSpread complete the first visible outer
	// box-shadow layer, in CSS px: the painter fades the shadow over the blur
	// and grows the box by the spread (webpage_paint.go); neither inherits.
	// TextShadowBlur is the text shadow's blur, which only softens its alpha;
	// it inherits with the rest of text-shadow.
	BoxShadowBlur   float64
	BoxShadowSpread float64
	TextShadowBlur  float64
	// HasTranslate/Translate* are a transform's translate() lengths in CSS px
	// and its percentages of the element's own box. Absolutely and fixed
	// positioned boxes apply them once their size is known (layoutLayer);
	// rotate/scale/skew are ignored so content stays upright and readable.
	HasTranslate  bool
	TranslateX    float64
	TranslateY    float64
	TranslatePctX float64
	TranslatePctY float64
	// Isolate marks an element that starts a stacking context through a
	// property the reader does not draw — any transform, isolation: isolate,
	// a filter, a blend mode, will-change, contain, perspective, a clip path,
	// a mask (webStackingProperty) — so a z-index inside it orders only what
	// is inside it. Not inherited.
	Isolate bool
	// InsetPct keeps percentage top/right/bottom/left values (in that order;
	// 0 for a length or auto). A positioned box resolves them against its
	// containing block, where `left: 50%` is half the anchor's width.
	InsetPct [4]float64
	// DisplayInlineBlock is display: inline-block (or inline-flex /
	// inline-grid). On a block-level element it makes the element one box in
	// a wrapping row with its inline-block siblings (openInlineRow); an li
	// with it, or with display: inline, has no list marker. Not inherited.
	DisplayInlineBlock bool
	// InlineChip marks the style of an inline box that paints a chip — a
	// background or border of its own, or its parent inline box's — so its
	// runs paint with its padding, radius and border (webpage_inlinebox.go).
	// Not inherited.
	InlineChip bool
	// CenterBlocks is text-align: -webkit-center (<center>, align="center" on a
	// div or a table part): boxes narrower than the line are centred too, not
	// only the text. Inherited, like text-align, and cleared by any other value.
	CenterBlocks bool
	// TableCenter puts a table narrower than its column in the middle (margin
	// auto or a -webkit-center parent); prepareTableStyle copies it to every
	// row and cell of the table, like the table's width.
	TableCenter bool
	// TableSpacingSet records an explicit border-spacing (or cellspacing), so
	// `border-spacing: 0` is not mistaken for the 2px default.
	TableSpacingSet bool
	// LineClamp is line-clamp / -webkit-line-clamp: the block keeps that many
	// lines and ends the last with "…". Not inherited.
	LineClamp int
	// BaselineShift is the raise (negative) or drop of an inline box in CSS px:
	// vertical-align super/sub and a relatively positioned inline element. It
	// inherits through inline descendants and a block resets it.
	BaselineShift float64
	// CellVAlign is where table cells put their content in a taller row
	// (webCellVAlign*): the vertical-align (or valign) of a table part as a
	// browser's cascade hands it to the cells — tbody and a row straight in
	// its table are middle by the UA sheet, rows and cells inherit, CSS
	// table boxes start at baseline (webCellVAlignCascade). Unset is middle.
	CellVAlign int
	// CellOwn marks the style of a table cell element itself (td, th, a
	// table-cell box), not of the elements inside it, which inherit the
	// cell's table fields: only the cell's own surface fills its row
	// (webTagCellSurface), never a badge or a card inside it. computeStyle
	// clears it, prepareTableStyle sets it.
	CellOwn bool
	// CellLastRow marks a cell of a table's last row: that row keeps the
	// padding band its cells paint below the row grid, so whatever follows
	// the table starts under the paint (layoutFlexRowItems).
	CellLastRow bool
}

// webGridAreas is the small named-area matrix a grid container declares.
// The fixed bound matches the grid track cap; an area is resolved to the
// bounding box of its cells when an item asks for it by name.
type webGridAreas struct {
	Rows, Cols int
	Names      [6][6]string
}

// webGridTrackSpec keeps the authored bounds of one grid track. Min is the
// definite floor (px), while MinAuto asks for a content-based floor. Max is a
// definite px cap, MaxPct a container-relative cap, MaxFr a flexible maximum,
// and MaxAuto lets the track grow to its content. Keeping these separate is
// what lets minmax(220px, 1fr) keep its 220px floor while still sharing free
// space, instead of treating the floor as a second fixed base.
type webGridTrackSpec struct {
	Min, Max       float64
	MinPct, MaxPct float64
	MaxFr          float64
	MinAuto        bool
	MaxAuto        bool
}

// webCQContext is the unresolved half of one element's style: the cascaded
// declarations before var()/cq* substitution, the style they cascade onto,
// and the custom properties in force. The streaming parser resolves a first
// approximation against its modelled container; layout replays this context
// when it has the container's real used size.
type webCQContext struct {
	base         webStyle
	decls        map[string]string
	vars         *webVarScope
	parentHeight float64
	// rootPx is the root font size rem lengths followed at parse time.
	rootPx float64
}

// webVarScope is one element's view of the custom properties: the ones it
// inherited plus the ones it declares, all expanded to plain text.
type webVarScope struct {
	props map[string]string
}

// lookup returns a property already expanded past var(). A nil scope is the
// document before any `--name` was seen.
func (s *webVarScope) lookup(name string) (string, bool) {
	if s == nil {
		return "", false
	}
	value, ok := s.props[name]
	return value, ok
}

// webRun is a styled span inside a block.
type webRun struct {
	Text  string
	Style webStyle
	// Image is set on the run that stands for an inline image once layout
	// has stitched an image-split paragraph back together (webpage_inline.go).
	Image *webBlock
	// Lead and Trail are the room the edges of the inline boxes that open
	// before the run's first word and close after its last take in the line,
	// in CSS px (margin, border and padding); ChipLead and ChipTrail are the
	// part of them the run's background chip covers, and OpensChip/ClosesChip
	// mark the left and right edges of a box with a surface of its own
	// (webpage_inlinebox.go).
	Lead, Trail           float64
	ChipLead, ChipTrail   float64
	OpensChip, ClosesChip bool
}

// webBlock is one laid-out unit: a paragraph, heading, list item, quote, code
// block or horizontal rule.
type webBlock struct {
	Runs  []webRun
	Style webStyle
	// ContainerIDs lists the CSS containment containers enclosing this block,
	// outermost first. It is the parse-time element ancestry, kept separately
	// from the visual style so layout can find the nearest measured size.
	ContainerIDs []int
	Align        int
	Indent       float64
	TextIndent   float64
	MarginTop    float64
	MarginBot    float64
	Bullet       string
	// MarkerHang sets the marker outside the item's text, in the list's
	// padding (list-style-position: outside on a list whose page sets that
	// padding); otherwise the text starts after it.
	MarkerHang bool
	// MarkerStyle is the resolved ::marker style for this list item. Its
	// colour and font size paint the bullet; HasMarkerStyle separates an
	// author marker from the reader's default bullet.
	MarkerStyle        webStyle
	HasMarkerStyle     bool
	WhiteSpace         int
	Pre                bool
	HasBg              bool
	Background         sdl.Color
	Gradient           webGradient
	HasGradient        bool
	BorderLeft         bool
	BorderRight        bool
	BoxBorder          bool
	BorderTop          bool
	BorderBottom       bool
	BorderColor        sdl.Color
	HasBorderColor     bool
	BorderWidth        float64
	BorderSideWidth    [4]float64
	BorderSideStyle    [4]int
	BorderSideStyleSet [4]bool
	BorderSideColor    [4]sdl.Color
	BorderSideHasColor [4]bool
	BorderStyle        int
	Outline            bool
	OutlineOffset      float64
	Rule               bool
	// FlexGroup/FlexItem tie a block to its flex container and direct child.
	// Zero means "not in a flex container". The container's own row settings
	// are copied onto every item so a sub-layout can be built without the
	// parser's element stack.
	FlexGroup   int
	FlexItem    int
	FlexDir     int
	FlexReverse bool
	FlexJustify int
	FlexAlign   int
	FlexGapX    float64
	FlexGapY    float64
	// FlexHeight is the flex container's definite block size in CSS px, when
	// the author supplied one (height, or a min-height floor). Zero means the
	// column is content-sized and has no free block space to hand out.
	FlexHeight float64
	FlexWrap   bool
	GridCols   int
	GridMin    float64
	// GridWeights/GridBases retain an explicit `240px 1fr` track list so a
	// sidebar keeps its width instead of becoming another equal column.
	GridWeights [6]float64
	GridBases   [6]float64
	// GridRows/GridRowWeights/GridRowBases retain explicit row tracks so a
	// card can reserve a fixed header, body or empty trailing row.
	GridRows          int
	GridRowWeights    [6]float64
	GridRowBases      [6]float64
	GridTrackSpecs    [6]webGridTrackSpec
	GridRowTrackSpecs [6]webGridTrackSpec
	GridAutoRepeat    int
	GridAutoTrack     webGridTrackSpec
	GridRowAutoRepeat int
	GridRowAutoTrack  webGridTrackSpec
	// GridAutoColSpecs/GridAutoRowSpecs size implicit tracks created by
	// placement beyond the explicit templates. The list cycles, as CSS does.
	GridAutoColSpecs [6]webGridTrackSpec
	GridAutoColCount int
	GridAutoRowSpecs [6]webGridTrackSpec
	GridAutoRowCount int
	GridAutoFlow     int
	GridDense        bool
	// GridAreas is the container's named-area map, copied to every direct
	// grid item so `grid-area: header` can resolve without a DOM/stack.
	GridAreas *webGridAreas
	// FlexTrail holds nested container memberships after the outermost one.
	// Each sub-layout consumes one entry, so an inner row stays inside its
	// parent card instead of being measured across the full page width.
	FlexTrail    []webFlexMembership
	FlexGrow     float64
	FlexShrink   float64
	FlexBasis    float64
	BasisPct     float64
	Order        int
	Width        float64
	WidthPct     float64
	AlignSelf    int
	AlignContent int
	JustifyItems int
	JustifySelf  int
	// Float marks an absolutely or fixed-positioned block: it is measured on
	// its own and painted over the flow in ZIndex order. Layer is the
	// positioned subtree it belongs to (webDocument.Layers, 1-based), 0 in
	// the flow: every block of one layer is measured and painted together
	// (webpage_layers.go).
	Layer int
	// TreeSeq is the parser sequence of the element the block takes its
	// style from: its place in document order, which breaks ties between
	// layers, floats and sticky bars of equal z-index.
	TreeSeq int
	// LayerMarker is set on an empty block the parser leaves in the flow where
	// that layer's root would have started: layout records the place as the
	// layer's static position and lays nothing out for it.
	LayerMarker int
	// FloatGroup is the floated subtree the block belongs to (a floated
	// figure, aside, table or column: webDocument.FloatRoots, 1-based), 0
	// outside one. A layout pass sets the group's blocks aside together, as
	// FloatBlocks of the one block that stands for them in its flow
	// (webpage_floats.go).
	FloatGroup  int
	FloatBlocks []webBlock
	// ClearMarker is set on an empty block that only moves what follows below
	// floats: a clearfix's ::after (Style.Clear says which floats), or the end
	// of an element that holds its floats (overflow: hidden, display:
	// flow-root), which clears the floats opened inside it — those after its
	// own place in the document, ClearFrom.
	ClearMarker bool
	ClearFrom   int
	// OwnsBox is set when that element is BoxOwner's element, so the paints
	// the block makes are that box's own surface rather than content inside
	// it (webSortPaintOrder).
	OwnsBox   bool
	Float     bool
	ZIndex    int
	PosTop    float64
	PosRight  float64
	PosBottom float64
	PosLeft   float64
	HasTop    bool
	HasRight  bool
	HasBottom bool
	HasLeft   bool
	// Details and DetailsShown fold a <details> section. Details is every
	// enclosing section, outermost first, because a closed outer section hides
	// an open section inside it — one owner pointer could not say that. While any
	// of them is closed the block is kept but not laid out, unless DetailsShown
	// is set because the block is the summary (or the marker) of the innermost
	// one. The states live on the controls, so opening a section re-measures the
	// page instead of re-parsing it.
	Details      []*webControl
	DetailsShown bool
	// Image marks an <img> block. ImageSrc is the resolved http(s) URL or a
	// data:image/...;base64 URI; ImageW/ImageH are the width/height attrs in
	// CSS px (zero when absent — the natural pixel size fills in at layout).
	Image    bool
	ImageSrc string
	ImageW   float64
	ImageH   float64
	// Placeholder marks a replaced element the reader cannot rasterize (svg,
	// canvas, object, embed, a media element with no playable source). It keeps
	// the element's box, background and border in the flow instead of dropping
	// its content into the surrounding text.
	Placeholder bool
	// Boxes lists the visual container boxes enclosing this block, outermost
	// first. An element that contains only child blocks (a card with an h3 and
	// a paragraph, a hero with a heading, a grid sidebar) emits no block of its
	// own, so its background, border and shadow used to disappear even though
	// its children were laid out. Layout paints these boxes around the runs
	// they contain.
	Boxes []int
	// BoxOwner is the nearest enclosing box this block represents directly.
	// A box with a direct block is already painted by that block; a box that
	// only contains child blocks is painted by the container pass.
	BoxOwner int
	// TableEmptyRow marks a <tr> that emitted no cell blocks. Layout still
	// reserves its row height, so a pure rowspan gap does not collapse.
	TableEmptyRow bool
	// TableEmptyCell marks the block of a table cell that had nothing to draw
	// (<td></td>, <td>&nbsp;</td>): it still takes its column, so the cells
	// after it stay in theirs (webParser.keepEmptyCell). CellBlankLine makes it
	// one empty line tall, as a browser draws a cell whose only text is a
	// no-break space or a <br>.
	TableEmptyCell bool
	CellBlankLine  bool
	// Spacer marks the block of an element whose only text was a no-break
	// space and whose page gives it a height: layout skips that height, rounded
	// like a margin (webParser.keepSpacer).
	Spacer bool
	// CollapseEdges is the resolved border-collapse result for this cell: at
	// most one winning edge per side, painted by layout instead of the ordinary
	// per-side border flags.
	HasCollapseEdges bool
	CollapseEdges    [4]webTableBorderEdge
	// Video marks a <video> element or a video-bearing <iframe> embed.
	// VideoSrc is the resolved http(s) URL the playback path streams through
	// yt-dlp; the layout reserves a 16:9 play card for it like an image and
	// the browser lists it among the page's links so the D-pad reaches it.
	// VideoThumb is the video's real thumbnail URL when it can be derived
	// from the source alone (YouTube's image CDN serves one per video id);
	// the image pass fetches it and the card paints it behind the glyph.
	Video      bool
	VideoSrc   string
	VideoThumb string
	VideoTitle string
	// InlineGroup ties together the blocks of one line of text an inline
	// image split apart: the text before it, the image and the text after it
	// carry the same nonzero ID, and layout sets them as one paragraph again
	// (webpage_inline.go). InlineSpace records whitespace before the block's
	// content; LineStyle is the paragraph's own style on an image block,
	// whose Style is the image's.
	InlineGroup int
	InlineSpace bool
	LineStyle   *webStyle
	// ImageAlt is an image's text alternative, which names an image-only
	// link in the link list.
	ImageAlt string
}

// webFlexMembership describes one level of a block's container ancestry.
// The first level lives in webBlock's regular Flex fields so existing layout
// paths stay cheap; only nested flex/grid descendants allocate a trail.
type webFlexMembership struct {
	group, item, dir, justify, align int
	alignContent                     int
	justifyItems                     int
	gapX, gapY                       float64
	height                           float64
	wrap                             bool
	reverse                          bool
	gridCols                         int
	gridMin                          float64
	gridWeights                      [6]float64
	gridBases                        [6]float64
	gridRows                         int
	gridRowWeights                   [6]float64
	gridRowBases                     [6]float64
	gridTrackSpecs                   [6]webGridTrackSpec
	gridRowTrackSpecs                [6]webGridTrackSpec
	gridAutoRepeat                   int
	gridAutoTrack                    webGridTrackSpec
	gridRowAutoRepeat                int
	gridRowAutoTrack                 webGridTrackSpec
	gridAutoColSpecs                 [6]webGridTrackSpec
	gridAutoColCount                 int
	gridAutoRowSpecs                 [6]webGridTrackSpec
	gridAutoRowCount                 int
	gridAutoFlow                     int
	gridDense                        bool
	gridAreas                        *webGridAreas
	grow, shrink, basis, basisPct    float64
	order                            int
	width, widthPct                  float64
	alignSelf                        int
	justifySelf                      int
}

func webFlexMembershipFromFrame(f *webFlexFrame) webFlexMembership {
	return webFlexMembership{
		group: f.group, item: f.item, dir: f.dir, justify: f.justify, align: f.align, alignContent: f.alignContent, justifyItems: f.justifyItems,
		gapX: f.gapX, gapY: f.gapY, wrap: f.wrap, gridCols: f.gridCols, gridMin: f.gridMin,
		gridWeights: f.gridWeights, gridBases: f.gridBases,
		gridRows: f.gridRows, gridRowWeights: f.gridRowWeights, gridRowBases: f.gridRowBases,
		gridTrackSpecs: f.gridTrackSpecs, gridRowTrackSpecs: f.gridRowTrackSpecs,
		gridAutoRepeat: f.gridAutoRepeat, gridAutoTrack: f.gridAutoTrack,
		gridRowAutoRepeat: f.gridRowAutoRepeat, gridRowAutoTrack: f.gridRowAutoTrack,
		gridAutoColSpecs: f.gridAutoColSpecs, gridAutoColCount: f.gridAutoColCount,
		gridAutoRowSpecs: f.gridAutoRowSpecs, gridAutoRowCount: f.gridAutoRowCount,
		gridAutoFlow: f.gridAutoFlow, gridDense: f.gridDense,
		gridAreas: f.gridAreas,
		height:    f.height,
		reverse:   f.reverse,
		grow:      f.itemStyle.FlexGrow, shrink: f.itemStyle.FlexShrink, basis: f.itemStyle.FlexBasis, basisPct: f.itemStyle.BasisPct,
		order: f.itemStyle.Order, width: f.itemStyle.Width, widthPct: f.itemStyle.WidthPct, alignSelf: f.itemStyle.AlignSelf,
		justifySelf: f.itemStyle.JustifySelf,
	}
}

func (b *webBlock) setFlexMembership(m webFlexMembership) {
	b.FlexGroup, b.FlexItem = m.group, m.item
	b.FlexDir, b.FlexReverse, b.FlexJustify, b.FlexAlign = m.dir, m.reverse, m.justify, m.align
	b.AlignContent = m.alignContent
	b.FlexGapX, b.FlexGapY, b.FlexWrap, b.GridCols, b.GridMin = m.gapX, m.gapY, m.wrap, m.gridCols, m.gridMin
	b.GridWeights, b.GridBases = m.gridWeights, m.gridBases
	b.GridRows, b.GridRowWeights, b.GridRowBases = m.gridRows, m.gridRowWeights, m.gridRowBases
	b.GridTrackSpecs, b.GridRowTrackSpecs = m.gridTrackSpecs, m.gridRowTrackSpecs
	b.GridAutoRepeat, b.GridAutoTrack = m.gridAutoRepeat, m.gridAutoTrack
	b.GridRowAutoRepeat, b.GridRowAutoTrack = m.gridRowAutoRepeat, m.gridRowAutoTrack
	b.GridAutoColSpecs, b.GridAutoColCount = m.gridAutoColSpecs, m.gridAutoColCount
	b.GridAutoRowSpecs, b.GridAutoRowCount = m.gridAutoRowSpecs, m.gridAutoRowCount
	b.GridAutoFlow, b.GridDense = m.gridAutoFlow, m.gridDense
	b.GridAreas = m.gridAreas
	b.FlexHeight = m.height
	b.FlexGrow, b.FlexShrink, b.FlexBasis, b.BasisPct = m.grow, m.shrink, m.basis, m.basisPct
	b.Order = m.order
	b.Width, b.WidthPct, b.AlignSelf = m.width, m.widthPct, m.alignSelf
	b.JustifyItems, b.JustifySelf = m.justifyItems, m.justifySelf
}

// Hidden reports a block a closed <details> section keeps out of the layout. It
// stays in the document: opening the section lays it out again.
func (b webBlock) Hidden() bool {
	if b.DetailsShown || len(b.Details) == 0 {
		return false
	}
	for _, ctrl := range b.Details {
		if !ctrl.Checked {
			return true
		}
	}
	return false
}

// webDocument is one parsed page. It holds no raw HTML — the parser keeps the
// styled blocks and the layout builds from those — plus the paper and text
// colours the page asked for.
type webDocument struct {
	URL     string
	Title   string
	Favicon string
	Blocks  []webBlock
	// Containers is every CSS containment container in document order, keyed by
	// the stable ID carried on descendant blocks. HasContainerUnits records
	// whether any style actually used cq*; pages without one skip the extra
	// layout pass entirely.
	Containers map[int]webContainerInfo
	Boxes      map[int]webBoxInfo
	// FlexBoxDepth marks how many ancestor boxes belong to a flex/grid
	// container. Item sublayouts start after that prefix so the same padding
	// and width constraints are not applied at every nesting level.
	FlexBoxDepth map[int]int
	// FlexItemBoxDepth adds a direct item's own box to the resolved prefix.
	// Its width was already chosen by the parent flex/grid track.
	FlexItemBoxDepth map[int]map[int]int
	// FlexScrollX marks a horizontally scrolling row. This reader scrolls
	// vertically, so fixed cards in such a row flow into readable rows.
	FlexScrollX       map[int]bool
	HasContainerUnits bool
	Paper             sdl.Color
	// PaperGradient is a body/html background ramp: the page under the text is
	// the one gradient that covers the whole panel, so it is painted too.
	PaperGradient    webGradient
	HasPaperGradient bool
	Text             sdl.Color

	// Images holds the fetched bytes of every <img> in the document, keyed by
	// resolved URL or data URI; ImageMeta records the natural pixel sizes the
	// layout needs. Set imageMu (the field below) guards these two maps.
	Images    map[string][]byte
	ImageMeta map[string]webImageMeta

	// FormHidden holds each form's hidden inputs by form ID. They are
	// submitted with the form but never drawn, so they live beside the blocks
	// rather than inside a run.
	FormHidden map[int][]webHiddenField

	mu     sync.Mutex
	stamp  webLayoutStamp
	layout *webLayout
	// sizesDirty is set when pictures or web fonts arrived after the layout
	// was measured, laidOutAt when it was: layoutFor measures the page again
	// at most every webTune.RelayoutEvery while they stream in. Guarded by mu.
	sizesDirty bool
	laidOutAt  time.Time

	// imageMu guards Images and ImageMeta separately from mu. They need their
	// own lock because the layouter reads the natural sizes while layoutFor
	// holds mu: sharing one mutex deadlocked the reader on the first page with
	// an image whose size the markup did not give, which froze the whole app.
	imageMu sync.Mutex
	// imagePasses counts the image passes running for this document, under
	// imageMu: while one runs, a picture with no bytes yet is still on its way
	// (webDocument.awaitImage). imageWant lists the pictures boxes on screen
	// wait for, which the pass fetches first (nextWantedImage).
	imagePasses int
	imageWant   []string

	// WebFonts are the page's @font-face rules the reader can load and
	// WebFamilies their family names (webStyle.WebFont is 1 + an index into
	// WebFamilies); webFontUse marks the families text is set in, 1 regular and
	// 2 bold. webFontPick, guarded by webFontMu, is the loaded file for each
	// family and weight (webpage_webfont.go).
	// boxCountCache is boxCounts' result (webpage_flow.go), counted once.
	boxCountOnce  sync.Once
	boxCountCache *webBoxCounts
	// DownloadLinks are the hrefs of the page's <a download> links, with the
	// file name each asks for ("" for the server's).
	DownloadLinks map[string]string
	WebFonts      []webFontFace
	WebFamilies   []string
	webFontUse    []uint8
	webFontMu     sync.Mutex
	webFontPick   map[[2]int]string

	// SVGInline holds each drawable inline <svg> under its synthetic
	// "jhsvg:" image key until the image pass rasterises it; SVGDefs keeps the
	// page's sprite sheets (<symbol>s a <use> elsewhere points at). The byte
	// counts bound both. The parser fills these, the image pass reads them
	// once and then lets them go (webpage_svg.go).
	SVGInline      map[string]webSVGSource
	SVGInlineBytes int
	SVGDefs        []string
	SVGDefBytes    int

	// FlexInlineRow marks the flex groups that are a parent's run of
	// inline-block children (openInlineRow): their items are as wide as their
	// content, as inline boxes are, instead of sharing the row.
	FlexInlineRow map[int]bool
	// FlexAdCut marks the flex and grid groups an ad item was cut out of
	// (webpage_adremove.go): a grid closes the tracks nothing is left in.
	FlexAdCut map[int]bool

	// Layers describes each positioned subtree the parser opened, indexed by
	// webBlock.Layer-1, and Stickies each position: sticky element, whose
	// subtree layout copies into a pinned bar (webpage_layers.go). Positioned
	// lists the relatively positioned elements of the flow, in document
	// order, which layout may paint in stacking order with the layers
	// (webLiftPositioned).
	Layers     []webLayerInfo
	Stickies   []webStickyInfo
	Positioned []webPositionedInfo
	// FloatRoots describes each floated subtree, indexed by
	// webBlock.FloatGroup-1 (webpage_floats.go).
	FloatRoots []webFloatRootInfo
}

// webBoxInfo is one visual container box an element contributed. direct is
// set when the element also produced a block of its own; that block already
// paints the box, so the container pass must not paint a second copy. A box
// with only child blocks has no direct block and is painted from the runs that
// carry its ID.
type webBoxInfo struct {
	style  webStyle
	direct bool
	// visual records whether the element declared a surface. A wrapper that
	// only declares padding/width/height still needs a box ID so its children
	// inherit the right measure, but it has nothing to paint itself.
	visual bool
	// anchor marks a box that exists only because its element is positioned:
	// it is a containing block for absolutely positioned descendants
	// (webpage_layers.go) and takes no part in flow, measure or paint.
	anchor bool
}

// webRunPos is a measured run, ready to draw.
type webRunPos struct {
	Text string
	X    int32
	// SpaceBefore is the automatic inter-word gap that placed this run. Text
	// justification expands these gaps; a run whose spaces are part of its own
	// text (pre-wrap) keeps SpaceBefore zero.
	SpaceBefore int32
	Font        *ttf.Font
	Color       sdl.Color
	Bold        bool
	Underline   bool
	Strike      bool
	// DecorationColor overrides the text colour for the underline and
	// strikethrough; HasDecorationColor distinguishes an author colour from
	// the default currentColor.
	DecorationColor     sdl.Color
	HasDecorationColor  bool
	DecorationThickness int32
	DecorationStyle     int
	// DecorationOffset moves the underline by text-underline-offset, in the
	// same layout pixels as the rest of the run.
	DecorationOffset int32
	// Text shadow offsets and colour, resolved to layout pixels.
	HasShadow        bool
	ShadowX, ShadowY int32
	ShadowColor      sdl.Color
	HasShadowColor   bool
	// Href is set when this run is part of a link, so the browser can outline
	// and follow it without re-parsing the page.
	Href string
	// Inline backgrounds (a <code> chip inside a paragraph) are per run; a
	// block-level background is painted once for the whole block instead.
	HasBg bool
	Bg    sdl.Color
	// Control carries the form control this run draws, if any.
	Control    *webControl
	BoxW, BoxH int32
	// Field is a text field's own border and corner radius from the page's
	// CSS (webPaintField); nil leaves the plain filled box.
	Field *webInlineChip
	// Boxes and BoxWidth are the container boxes this run belongs to and the
	// used column width at the point it was laid out. The container pass uses
	// them to paint a card, sidebar or hero around all of its child runs.
	Boxes          []int
	BoxWidth       int32
	Invisible      bool
	VerticalOffset int32
	Clip           sdl.Rect
	HasClip        bool
	// Image marks an inline image's place on a line (webpage_inline.go): the
	// run has no text, and ImageW/ImageH are its box in layout pixels. The
	// picture itself is painted from layout.Images.
	Image          *webBlock
	ImageW, ImageH int32
	// Chip is the inline box this word paints in (webpage_inlinebox.go), shared
	// by its run's words; nil keeps the plain chip of a run with a background.
	// ChipLead/ChipTrail are how far the chip reaches before and after the
	// word, and ChipLeft/ChipRight mark the box's own left and right edges.
	Chip                *webInlineChip
	ChipLead, ChipTrail int32
	ChipLeft, ChipRight bool
	// Lifted marks a word the page paints in a lifted layer instead
	// (webLiftPositioned): the flow's paint skips it, everything else that
	// reads the page's rows does not.
	Lifted bool
}

// webRow is one line slot in the uniform layout grid. Tall lines (headings)
// claim extra empty slots so nothing ever overlaps the next line.
type webRow struct {
	Y    int32
	Runs []webRunPos
}

// webRect is a background, rule or border drawn with (or behind) the text of
// a layout. Stroke selects an outline instead of a fill.
type webRect struct {
	Rect      sdl.Rect
	Color     sdl.Color
	Stroke    bool
	Thickness int32
	// BackgroundFill marks the block colour entry. It remains in Rects for
	// geometry consumers, but the painter draws it through layout.Backgrounds
	// so background layers can stack above it in CSS order.
	BackgroundFill bool
	// BorderStyle selects solid, dashed, dotted or double geometry for a
	// border/outline rect. The zero value is solid.
	BorderStyle int
	Clip        sdl.Rect
	HasClip     bool
	// Gradient paints this rect as a ramp instead of a flat fill; Color is then
	// the average, used for the corner rounding and as the fallback.
	Gradient    webGradient
	HasGradient bool
	// BoxID ties a block-painted rect to the container box that declared the
	// surface. The container pass can then replace that partial paint with
	// one union when the wrapper also contains nested blocks.
	BoxID int
	// Inner marks a paint made by content inside BoxID (an image's frame)
	// rather than by the box's own element: it paints over that box's
	// surface and is never replaced with it (webSortPaintOrder).
	Inner bool
	// Radius is the author's border-radius in design pixels, clamped to half
	// the shorter side when drawn. 0 keeps the reader's crisp corner.
	Radius int32
	// Z orders the fills and borders of the page. Text is always painted after
	// every rect, so z-index decides which background or border wins where
	// two elements overlap.
	Z int
	// Shadow marks a box-shadow. Like BackgroundFill it stays in Rects for
	// geometry consumers while the painter draws its twin entry in
	// layout.Backgrounds, under the element's own background. Rect is the box
	// moved by the shadow offset; Blur and Spread are device pixels the
	// painter fades over and grows the box by.
	Shadow bool
	Blur   int32
	Spread int32
	// cellKey and cellPart tag a table cell's own surface (webpage_table.go):
	// the paint that fills the cell (webCellSpans) or its bottom edge
	// (webCellBottom), so the row can give it the row's full height. The row
	// clears the tag when it merges the cell.
	cellKey  int64
	cellPart uint8
	// Lifted marks a paint a lifted layer draws instead of the flow
	// (webLiftPositioned).
	Lifted bool
}

// webFloat is a layer: a positioned subtree, a CSS float or a sticky bar's
// pinned copy. It is laid out at its own width, then painted in stacking
// order (webpage_layers.go). A page's badge, card overlay or cookie bar is
// exactly this. Its rows and paints are in its own coordinates: (X, Y) is its
// top-left corner on the page (for a sticky bar, its place in the flow).
type webFloat struct {
	Z    int
	X, Y int32
	// H is how far the layer reaches below Y: its rows and the surfaces and
	// pictures past them; W is how wide it is (a sticky bar's extent).
	H int32
	W int32
	// Pin holds a position: fixed layer to the screen (webPinTop, …): it is
	// painted at the same place in the view whatever the scroll, Y (or
	// Bottom) measured from the view's edge instead of the page's top.
	Pin uint8
	// Top is the sticky inset in layout pixels. A sticky overlay is painted
	// at Top once the normal flow position has scrolled above the viewport.
	Top   int32
	Rows  []webRow
	Rects []webRect
	// Backgrounds and Images live beside the rows for the same reason they do
	// on the page: a painted layer is not a box, and dropping either left an
	// absolutely positioned card transparent.
	Backgrounds []webBackgroundPaint
	Images      []webImagePos
	Videos      []webVideoPos
	// Phase picks the paint pass (webLayerAbove, webLayerUnder, …) and Key
	// orders the layer within it, the way stacking contexts nest.
	Phase uint8
	Key   webStackKey
	// Clip is the overflow: hidden containing block the layer is cut to, in
	// page coordinates, when HasClip is set.
	Clip    sdl.Rect
	HasClip bool
	// A sticky bar stays inside its containing block (LimitBox while laying
	// out): its bottom may not pass Limit nor its top rise above LimitTop, in
	// page coordinates; Limit is 0 when the page is its only limit. HasBottom
	// marks a bar that sticks to the bottom edge, Bottom px above it.
	LimitBox  int
	Limit     int32
	LimitTop  int32
	Bottom    int32
	HasBottom bool
	// Root is the box of the element the layer is made of (a positioned
	// subtree's root, a float's innermost box, a sticky bar's own box), and
	// StickyIndex the pinned bar in webLayout.Stickies (1-based) that carries
	// a copy of this layer: it is not painted at its page place while that
	// bar is pinned. Lift marks a relatively positioned part of the flow
	// painted in stacking order (webLiftPositioned): the page's rows still
	// hold its words for links, find and the cursor.
	Root        int
	StickyIndex int
	Lift        bool
	// paint caches the rows' paint runs, as webLayout.paint does for the
	// page. Render thread.
	paint []webPaintRow
}

// webLayout is the measured page for one width and font configuration.
type webLayout struct {
	Rows  []webRow
	Rects []webRect
	// Backgrounds paint in CSS order beneath borders and content: the block
	// colour first, then background layers bottom-to-top. Keeping layers in
	// one list lets image and gradient layers interleave correctly.
	Backgrounds []webBackgroundPaint
	RowH        int32
	Height      int32
	// PaintBottom is the lowest bottom the page's scroll has to reach: a
	// table cell keeps one padding row below its content rows
	// (webTagCellSurface), and the page stops at the last content row unless
	// that row is counted.
	PaintBottom int32
	// ContainerSizes holds the used border-box size of each containment
	// container, in CSS px, measured by this layout. Container units in the
	// next pass resolve against these numbers.
	ContainerSizes map[int]webContainerSize
	// Links are the page's clickable runs in reading order, one entry per
	// destination. The browser walks this list with the D-pad.
	Links []webLinkHit
	// Floats are the layers: positioned subtrees and CSS floats, sorted by
	// paint pass and stacking key (webpage_layers.go).
	Floats []webFloat
	// Stickies are pinned copies of position:sticky blocks, in stacking-key
	// order. Their normal rows remain in Rows for flow and for the
	// top-of-page rendering.
	Stickies []webFloat
	// Images are the reader's <img> boxes, in layout-local coordinates like
	// Rects. They draw after the icon rects and before the text rows.
	Images []webImagePos
	// Videos are the reader's <video>/embed play cards, in the same layout-
	// local coordinates. They draw right after the images, before the text.
	Videos []webVideoPos
	// paint is the painter's cache of each row's text joined into stretches
	// (webpage_paintfast.go), filled as rows are first drawn. Render thread.
	paint []webPaintRow
	// statics holds each layer's static position, keyed by webBlock.Layer:
	// where its root would have started in the flow (webpage_layers.go).
	statics map[int]webStaticPos
	// boxStretch is the height, in layout pixels, a flex or grid item's box is
	// stretched to (align-items: stretch, the default): the line's or the
	// track's. The container pass paints the box at least that tall, so a row
	// of cards ends level (webpage_flow.go).
	boxStretch map[int]int32
	// boxGeom is each layout box's border box across, where layout applied
	// it (applyBoxConstraints): the container pass paints that width, not the
	// width of the box's text (webpage_flow.go).
	boxGeom map[int]webBoxGeom
	// boxEdges holds the collapsed borders (border-collapse) of a table cell
	// painted as a box around several blocks: the container pass draws these
	// instead of the cell's own four borders (webpage_table.go).
	boxEdges map[int][4]webTableBorderEdge
}

type webBackgroundPaint struct {
	Rect           sdl.Rect
	Color          sdl.Color
	Gradient       webGradient
	HasGradient    bool
	BoxID          int
	Image          string
	ObjectFit      int
	ObjectPosition string
	Opacity        float64
	Radius         int32
	Z              int
	// Inner marks a paint made by content inside BoxID rather than by the
	// box's own element, as on webRect.
	Inner bool
	// Shadow paints this entry as a box-shadow (drawWebBoxShadow): Rect is
	// the box moved by the offset, grown by Spread and faded over Blur, both
	// in device pixels. It sits right before the owner's background.
	Shadow bool
	Blur   int32
	Spread int32
	// cellKey and cellPart tag a table cell's own surface, as on webRect.
	cellKey  int64
	cellPart uint8
	// Lifted marks a paint a lifted layer draws instead, as on webRect.
	Lifted bool
}

// webLinkHit is one link on a laid-out page: the row it starts on and its
// pixel extent inside that row. Href is the raw attribute; the browser
// resolves it against the document URL when the link is opened.
type webLinkHit struct {
	Row int
	X   int32
	W   int32
	// Box holds a form control's actual layout-local dimensions.
	Box  sdl.Rect
	Href string
	Text string
	// Video marks a hit generated from a <video> play card rather than an
	// anchor: activation must stream/play it instead of navigating to it.
	Video bool
	// Control marks a hit generated from a form control: activation opens the
	// keyboard and submits what is typed, rather than following a link.
	Control *webControl
	// Layer marks a hit inside a layer or float (webpage_layers.go). Its Box
	// is where it is painted, and a tap prefers it to a flow hit under it.
	// Pinned is the layer's index in Floats plus one when that layer is
	// pinned to the screen: Box.Y is then in the layer's own coordinates and
	// the hit does not scroll (webPinnedHitRect).
	Layer  bool
	Pinned int
}

// webForm is the <form> a control belongs to: where a submitted value goes,
// and the field name it submits under. Field is the first text input the form
// declared, so a submit button knows which name to fill when nothing else does.
//
// ID tells two forms apart. A page can carry two search boxes with the same
// action (or none at all, which resolves to the page's own address), and
// matching on the action alone sent both forms' fields on one press.
type webForm struct {
	ID      int
	Action  string
	Method  string
	Enctype string
	Field   string
}

// webHiddenField is a control the page submits but does not draw: a hidden
// input. Search pages lean on them (`ie=utf-8`, a site key, a locale), and a
// submission that dropped them was rejected by the page's own handler.
type webHiddenField struct {
	Name  string
	Value string
}

// webControl is one form control the reader found in the page. The reader has
// no DOM: a control is a run of text with one of these attached, and the
// browser finds it exactly the way it finds a link.
//
// Its state (Value, Checked, Choice) lives here rather than in the run's text,
// because a field that has been typed into, a box that has been ticked or a
// list that has been stepped must read back the new state from the same place
// the page was parsed into. Display turns that state into the text the reader
// paints, so a re-layout is all it takes to show the change.
type webControl struct {
	Kind        string // "text", "textarea", "select", "check", "radio" or "submit"
	Name        string // the field's name, used in the submitted query
	Label       string // <button> text; its submitted value is a separate attribute
	Placeholder string
	Value       string
	// Options and Choice are a <select>'s items and the one showing. A select
	// is stepped in place: it has no keyboard, and a handheld cannot open the
	// author's dropdown.
	Options []string
	// OptionValues is what each option submits, which is not always its label
	// ("English" shown, "en" sent). Empty entries fall back to the label.
	OptionValues []string
	Choice       int
	// Checked is a checkbox or radio button's state. It starts at the markup's
	// own checked attribute, so a page that ships a preselected option keeps it.
	Checked bool
	// Secret marks a password field. Its value is typed as usual but drawn as
	// dots: the reader paints on a shared screen, and a prefilled password
	// rendered in the clear is one glance away from being read off it.
	Secret                  bool
	// IconOnly marks a <button> whose content is empty (a search bar's
	// magnifier drawn by a background image). HTML gives an empty <button> no
	// default label at all, while <input type=submit> without a value says
	// "Submit" — so the reader must not invent a word to put on it.
	IconOnly                bool
	Form                    webForm
	Width, WidthPct, Height float64
}

// PlaceholderShown reports that a field is drawing its prompt rather than a
// value, which is what the reader dims. It is read at layout time, so a field
// that has just been filled in stops looking empty on the same frame.
func (c *webControl) PlaceholderShown() bool {
	if c == nil {
		return false
	}
	if c.Kind != "text" && c.Kind != "textarea" {
		return false
	}
	return strings.TrimSpace(c.Value) == ""
}

// Display is the text a control paints. A field shows what has been filled in,
// then the page's own placeholder, then a plain prompt; a select shows its
// chosen item; a box shows a drawn tick; a button shows its label.
func (c *webControl) Display() string {
	if c == nil {
		return ""
	}
	switch c.Kind {
	case "text", "textarea":
		if c.Secret && strings.TrimSpace(c.Value) != "" {
			return " " + strings.Repeat("•", 8) + " "
		}
		if v := strings.TrimSpace(c.Value); v != "" {
			// A textarea's value can be several lines; the box is one line, so
			// the first line is what it shows.
			if i := strings.IndexByte(v, '\n'); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
			if v != "" {
				return " " + v + " "
			}
		}
		if ph := strings.TrimSpace(c.Placeholder); ph != "" {
			return " " + ph + " "
		}
		return " Tap to type "
	case "select":
		if c.Choice >= 0 && c.Choice < len(c.Options) {
			if label := strings.TrimSpace(c.Options[c.Choice]); label != "" {
				return " " + label + " ▼ "
			}
		}
		return " Choose ▼ "
	case "check":
		// The box glyphs are the ones Inter actually carries: the ballot box
		// (U+2610) and the circled dot (U+25C9) are not in the font, and the
		// sanitiser would replace each with an ellipsis, so a tick box would
		// have drawn "…" instead of a box.
		if c.Checked {
			return " ■ "
		}
		return " □ "
	case "radio":
		if c.Checked {
			return " ● "
		}
		return " ○ "
	case "details":
		// A closed section says [+] and an open one [-]. The bundled face has no
		// reliable disclosure triangle, and a tick box would read as a form
		// field rather than a fold, so the reader uses the tree convention.
		if c.Checked {
			return " [-] "
		}
		return " [+] "
	case "submit":
		label := strings.TrimSpace(c.Label)
		if label == "" {
			label = strings.TrimSpace(c.Value)
		}
		if label == "" {
			if c.IconOnly {
				// An icon-only button draws no word and reserves no text; its
				// box is the page's own size for it.
				return ""
			}
			label = "Submit"
		}
		return " " + label + " "
	}
	return " " + strings.TrimSpace(c.Value) + " "
}

// webRule is one parsed CSS rule (one selector; groups are split apart).
type webRule struct {
	selector    string
	specificity int
	order       int
	// layer is the rule's cascade layer rank (webCSSParse.layerRank): a later
	// layer beats an earlier one, and a rule outside every layer
	// (webUnlayered) beats them all, before specificity is compared.
	layer int
	decls map[string]string
	// important holds the !important declarations of the same rule. They are
	// kept apart because they outrank every normal declaration, whatever the
	// selector's specificity.
	important map[string]string
	// sel is the selector compiled once, when the sheet is parsed. hides marks
	// a rule whose declarations would hide what it matches; the matcher only
	// applies such a rule on a definite match (see webSelector.matches).
	// reveals marks one that would show it (display other than none,
	// visibility: visible, a non-zero opacity): a reader nobody can hover or
	// focus still reaches what such a rule opens on :hover or :focus.
	sel     *webSelector
	hides   bool
	reveals bool
	// containers holds the enclosing @container conditions. They are
	// evaluated against the parser's open element stack when the rule is
	// matched, because a rule sheet is parsed before the HTML that supplies
	// the containers.
	containers []*webContainerQuery
}

// mode is how the rule is matched against an element (webSelMode).
func (r *webRule) mode() webSelMode {
	switch {
	case r.hides:
		return webSelStrict
	case r.reveals:
		return webSelReveal
	}
	return webSelLenient
}

// webRuleIndex buckets a sheet's rules by what their subject compound (the
// rightmost one) demands of the element: an id, else a class, else a tag. The
// matcher used to test every rule against every element — a real site's few
// thousand rules times a few thousand elements was most of a page's parse
// time on the handheld. Id, class and tag tests are definite in every match
// mode (webSelCompoundTest), so a rule outside an element's buckets can never
// match it and skipping it changes nothing.
type webRuleIndex struct {
	byID    map[string][]int32
	byClass map[string][]int32
	byTag   map[string][]int32
	rest    []int32
}

func newWebRuleIndex(rules []webRule) *webRuleIndex {
	if len(rules) == 0 {
		return nil // candidates answers nil for a page without CSS
	}
	ix := &webRuleIndex{
		byID:    make(map[string][]int32),
		byClass: make(map[string][]int32),
		byTag:   make(map[string][]int32),
	}
	for i := range rules {
		sel := rules[i].sel
		if sel == nil || sel.never || len(sel.compounds) == 0 {
			continue // matchesIn never matches it
		}
		subject := &sel.compounds[len(sel.compounds)-1]
		// Only ASCII names become keys: strings.ToLower does not fold every
		// pair strings.EqualFold matches (ſ and s, ς and σ), so a non-ASCII
		// name is left to the next key, or to rest.
		var id, class string
		for j := range subject.simples {
			switch s := &subject.simples[j]; s.kind {
			case webSelID:
				if id == "" && webASCIIName(s.name) {
					id = strings.ToLower(s.name)
				}
			case webSelClass:
				if class == "" && webASCIIName(s.name) {
					class = strings.ToLower(s.name)
				}
			}
		}
		n := int32(i)
		switch {
		case id != "":
			ix.byID[id] = append(ix.byID[id], n)
		case class != "":
			ix.byClass[class] = append(ix.byClass[class], n)
		case subject.tag != "":
			ix.byTag[subject.tag] = append(ix.byTag[subject.tag], n)
		default:
			ix.rest = append(ix.rest, n)
		}
	}
	return ix
}

func webASCIIName(name string) bool {
	for i := 0; i < len(name); i++ {
		if name[i] >= 0x80 {
			return false
		}
	}
	return true
}

// candidates lists, in sheet order, the rules that may match el.
func (ix *webRuleIndex) candidates(el *webElement) []int32 {
	if ix == nil {
		return nil
	}
	out := make([]int32, 0, len(ix.rest)+16)
	out = append(out, ix.rest...)
	if el.id != "" {
		out = append(out, ix.byID[strings.ToLower(el.id)]...)
	}
	for _, c := range el.classes {
		out = append(out, ix.byClass[strings.ToLower(c)]...)
	}
	out = append(out, ix.byTag[el.tag]...)
	slices.Sort(out)
	return slices.Compact(out)
}

// webFrame is an open element during the streaming parse.
type webFrame struct {
	el    webElement
	style webStyle
	kind  string // "block", "inline", "list", "skip"
	// attrs and counterResets are kept for the closing tag: ::after resolves
	// with the counters its children produced, and a counter-reset scope ends
	// with the element that opened it.
	attrs         string
	counterResets []string
	// flexGroup is non-zero when this element opened a flex container. The
	// matching frame is popped when the element closes.
	flexGroup int
	// containerID is non-zero when this element is a CSS containment
	// container. Descendant blocks copy the open IDs so layout can find them.
	containerID int
	// boxID is non-zero when this element contributed a visual container box
	// (background, border, outline or shadow). Descendant blocks carry its ID
	// so layout can paint the box even when the element has no direct text.
	boxID int
	// layer is non-zero on the root of a positioned subtree (webpage_layers.go);
	// seq is the element's parser sequence, its place in document order;
	// blockStart is how many blocks the page had when it opened; and sticky is
	// its webDocument.Stickies entry (1-based) when it is position: sticky.
	layer      int
	seq        int
	blockStart int
	sticky     int
	// sawImage is set on the open elements, up to the nearest positioned one,
	// when a picture is added inside them: an absolutely positioned box opened
	// later in that containing block is an overlay on the photo
	// (webParser.anchorSawImage).
	sawImage bool
	// An inline element's box (webpage_inlinebox.go): how many lines had been
	// flushed (webParser.lineFlushes) and how many runs the open block had
	// when it opened, so its right edge (inlineTrail, of which the chip covers
	// inlineTrailChip) lands on the last run added inside it; whether it
	// paints a surface of its own, and whether the next word was to open with
	// its left edge (startedChip).
	inlineFlushes   int
	inlineRuns      int
	inlineTrail     float64
	inlineTrailChip float64
	ownChip         bool
	startedChip     bool
	// positioned is the element's webDocument.Positioned entry (1-based) when
	// it is a relatively positioned element of the flow; flexItem
	// is set on a direct item of a flex or grid container (a z-index makes
	// it a stacking context); suspended is the line an absolutely positioned
	// inline element set aside, resumed when it closes (webpage_layers.go).
	positioned int
	flexItem   bool
	suspended  *webSuspendedLine
	// floatRoot is the element's webDocument.FloatRoots entry (1-based) when
	// it is a floated block that takes its subtree with it; floatsAt is how
	// many floats the page had opened when it opened (webParser.floatsSeen),
	// so its end knows whether any opened inside it (webpage_floats.go).
	floatRoot int
	floatsAt  int
	// blankLine is set on a table cell, or a block outside tables, when text a
	// browser draws as an empty line — no-break spaces — was found in it
	// (webParser.markBlankLine).
	blankLine bool
}

// webContainerInfo is one containment container's element facts. The size
// fields are CSS px; WidthPct is kept unresolved because its parent's used
// width is only known during layout.
type webContainerInfo struct {
	parent    int
	width     float64
	widthPct  float64
	height    float64
	flexGroup int
	flexItem  int
	// ancestors keeps the width-bearing boxes between the page column and this
	// container, including ordinary non-container wrappers. A percentage on a
	// plain parent still constrains the container's used inline size.
	ancestors []webContainerAncestor
}

type webContainerAncestor struct {
	width    float64
	widthPct float64
}

// webContainerSize is a used container size in CSS px. Height is zero until
// layout has measured the container's content (or the author set one).
type webContainerSize struct {
	Width  float64
	Height float64
}

// webFlexFrame is one open flex container while parsing. item counts its
// direct children so every block can be tagged with the item it belongs to.
type webFlexFrame struct {
	group             int
	item              int
	depth             int
	itemStyle         webStyle
	dir               int
	justify           int
	align             int
	alignContent      int
	justifyItems      int
	gapX              float64
	gapY              float64
	height            float64
	wrap              bool
	reverse           bool
	gridCols          int
	gridMin           float64
	gridWeights       [6]float64
	gridBases         [6]float64
	gridRows          int
	gridRowWeights    [6]float64
	gridRowBases      [6]float64
	gridTrackSpecs    [6]webGridTrackSpec
	gridRowTrackSpecs [6]webGridTrackSpec
	gridAutoRepeat    int
	gridAutoTrack     webGridTrackSpec
	gridRowAutoRepeat int
	gridRowAutoTrack  webGridTrackSpec
	gridAutoColSpecs  [6]webGridTrackSpec
	gridAutoColCount  int
	gridAutoRowSpecs  [6]webGridTrackSpec
	gridAutoRowCount  int
	gridAutoFlow      int
	gridDense         bool
	gridAreas         *webGridAreas
	// implicit marks the wrapping row a parent becomes for its inline-block
	// children (openInlineRow); the parent itself declared no flex.
	implicit bool
}

// webListState tracks <ul>/<ol> numbering.
type webListState struct {
	ordered bool
	n       int
	// reversed counts down (<ol reversed>): n is one above the next number.
	reversed bool
}

// --- Document store -------------------------------------------------------

var browserDocuments = struct {
	mu   sync.Mutex
	docs map[string]*webDocument
}{docs: make(map[string]*webDocument)}

// setBrowserDocument stores the parsed page for a browser variable, replacing
// the previous one so only the pages actually open stay in memory.
func setBrowserDocument(variable string, doc *webDocument) {
	if variable == "" {
		return
	}
	browserDocuments.mu.Lock()
	defer browserDocuments.mu.Unlock()
	if doc == nil {
		delete(browserDocuments.docs, variable)
		return
	}
	if len(browserDocuments.docs) >= 2 {
		if _, exists := browserDocuments.docs[variable]; !exists {
			for key := range browserDocuments.docs {
				delete(browserDocuments.docs, key)
				break
			}
		}
	}
	browserDocuments.docs[variable] = doc
}

// browserDocumentFor returns the parsed page for a variable, or nil when the
// current content is plain text.
func browserDocumentFor(variable string) *webDocument {
	if variable == "" {
		return nil
	}
	browserDocuments.mu.Lock()
	defer browserDocuments.mu.Unlock()
	return browserDocuments.docs[variable]
}

// --- CSS parsing ----------------------------------------------------------

// webMediaBlockApplies decides whether the rules inside an @media block belong
// on the reader's screen.
//
// The reader has one medium: the handheld's screen, at the size page scripts
// see too (webMediaViewport). A block that only targets `print` or `speech`
// never applies — a print sheet strips the navigation with display:none and
// re-colours the page for paper, which is how a page arrived with its header
// and menu missing — and width, height, orientation and aspect-ratio queries
// are answered for that one screen. They used to all apply at once, which was
// harmless for most sheets and wrong for paired ones: Tailwind's md:hidden and
// max-md:hidden each hid one copy of the content, and applied together they hid
// both. A feature the reader does not model (hover, pointer, colour schemes,
// resolution) still applies, as before.
func webMediaBlockApplies(query string) bool {
	w, h := webMediaViewport()
	return webMediaQueryApplies(query, w, h)
}

// webMediaViewport is the screen size in CSS pixels. The reader paints a CSS
// 16px base at 24 physical pixels (and scales it again for browser zoom), so
// using the physical width for media queries can select a two-column layout
// that does not fit the rendered text. Page scripts receive the same size.
func webMediaViewport() (float64, float64) {
	w, h := float64(screenWidth), float64(screenHeight)
	if w <= 0 || h <= 0 {
		w, h = 1280, 720
	}
	return w / webViewportPixelScale(), h / webViewportPixelScale()
}

func webViewportPixelScale() float64 {
	scale := webBaseFontSize(nil, "body") * browserZoomScale() / 16
	if scale <= 0 {
		return 1
	}
	return scale
}

// webMediaQueryApplies evaluates a media query list (with or without its
// "@media" keyword) for a viewport of w × h CSS pixels: true when any query of
// the list holds. An empty list is "all".
func webMediaQueryApplies(query string, w, h float64) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	query = strings.TrimSpace(strings.TrimPrefix(query, "@media"))
	if query == "" {
		return true
	}
	for _, part := range webSplitSelectorList(query) {
		if part = strings.TrimSpace(part); part != "" && webMediaQueryHolds(part, w, h) {
			return true
		}
	}
	return false
}

// webMediaQueryHolds evaluates one query: `[not|only] type [and condition]` or
// a bare condition.
func webMediaQueryHolds(query string, w, h float64) bool {
	words := webMediaTokens(query)
	if len(words) == 0 {
		return true
	}
	i, negate := 0, false
	switch words[0] {
	case "not":
		// `not (feature)` negates a condition, `not screen` the whole query.
		if len(words) > 1 && !strings.HasPrefix(words[1], "(") {
			negate, i = true, 1
		}
	case "only":
		i = 1
	}
	result := true
	if i < len(words) && !strings.HasPrefix(words[i], "(") && words[i] != "not" {
		switch words[i] {
		case "print", "speech":
			result = false
		}
		i++
		if i < len(words) && words[i] == "and" {
			i++
		}
	}
	if i < len(words) {
		result = result && webMediaCondition(words[i:], w, h)
	}
	if negate {
		result = !result
	}
	return result
}

// webMediaCondition evaluates `not (…)`, or groups joined by `and` or `or`.
// Anything it cannot read applies, as an unknown feature does.
func webMediaCondition(words []string, w, h float64) bool {
	if len(words) == 0 {
		return true
	}
	if words[0] == "not" {
		if len(words) == 2 {
			return !webMediaInParens(words[1], w, h)
		}
		return true
	}
	result := webMediaInParens(words[0], w, h)
	for i := 1; i+1 < len(words); i += 2 {
		next := webMediaInParens(words[i+1], w, h)
		switch words[i] {
		case "and":
			result = result && next
		case "or":
			result = result || next
		default:
			return true
		}
	}
	return result
}

// webMediaInParens evaluates one parenthesised group: a nested condition or a
// single feature.
func webMediaInParens(group string, w, h float64) bool {
	if len(group) < 2 || group[0] != '(' || group[len(group)-1] != ')' {
		return true
	}
	inner := strings.TrimSpace(group[1 : len(group)-1])
	if inner == "" {
		return true
	}
	if words := webMediaTokens(inner); len(words) > 1 || strings.HasPrefix(inner, "(") {
		if words[0] == "not" || strings.HasPrefix(words[0], "(") {
			return webMediaCondition(words, w, h)
		}
	}
	return webMediaFeature(inner, w, h)
}

// webMediaTokens splits a query into words and whole parenthesised groups.
func webMediaTokens(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		if webAttrSpaceByte(s[i]) {
			i++
			continue
		}
		start := i
		if s[i] == '(' {
			depth := 0
			for i < len(s) {
				if s[i] == '(' {
					depth++
				} else if s[i] == ')' {
					depth--
					if depth == 0 {
						i++
						break
					}
				}
				i++
			}
		} else {
			for i < len(s) && !webAttrSpaceByte(s[i]) && s[i] != '(' {
				i++
			}
		}
		out = append(out, s[start:i])
	}
	return out
}

// webMediaFeature evaluates one feature: `name: value` (with min-/max-), the
// range form (`width < 48rem`, `400px <= width <= 700px`), or a bare name.
func webMediaFeature(inner string, w, h float64) bool {
	if colon := strings.IndexByte(inner, ':'); colon >= 0 {
		name := strings.TrimPrefix(strings.TrimSpace(inner[:colon]), "-webkit-")
		value := strings.TrimSpace(inner[colon+1:])
		prefix := ""
		if strings.HasPrefix(name, "min-") || strings.HasPrefix(name, "max-") {
			prefix, name = name[:3], name[4:]
		}
		switch name {
		case "orientation":
			switch value {
			case "landscape":
				return w > h
			case "portrait":
				return h >= w
			}
			return true
		case "aspect-ratio", "device-aspect-ratio":
			want, ok := webMediaRatio(value)
			return !ok || webMediaCompare(w/h, prefix, want)
		}
		if held, known := webMediaDiscrete(name, prefix, value, false); known {
			return held
		}
		have, ok := webMediaDimension(name, w, h)
		if !ok {
			return true
		}
		want, ok := webMediaLength(value)
		return !ok || webMediaCompare(have, prefix, want)
	}
	if strings.ContainsAny(inner, "<>=") {
		return webMediaRange(inner, w, h)
	}
	if held, known := webMediaDiscrete(strings.TrimSpace(inner), "", "", true); known {
		return held
	}
	return true
}

// webMediaDiscrete answers the media features that describe the screen and
// the reader rather than the viewport's size, the way a light-mode desktop
// Chrome on an sRGB screen would, with reduced motion: the reader paints a
// light page, cannot animate (content waiting for a fade-in would stay
// invisible), takes a hovering pointer's styles (the desktop has a mouse and
// the handhelds' cursor stands in for one) and runs page scripts. Both
// halves of a pair — prefers-color-scheme dark and light, hover and none —
// used to hold at once, so the later rule won: usually the dark palette, on
// the reader's white paper. bare is the boolean form `(feature)`; known is
// false for a feature this does not model.
func webMediaDiscrete(name, prefix, value string, bare bool) (held, known bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	is := func(want string) bool { return bare || value == want }
	switch name {
	case "prefers-color-scheme":
		return is("light"), true
	case "prefers-reduced-motion":
		return is("reduce"), true
	case "prefers-contrast", "prefers-reduced-transparency", "prefers-reduced-data":
		return !bare && value == "no-preference", true
	case "forced-colors", "inverted-colors":
		return !bare && value == "none", true
	case "hover", "any-hover":
		return is("hover"), true
	case "pointer", "any-pointer":
		return is("fine"), true
	case "scripting":
		return is("enabled"), true
	case "update":
		return is("fast"), true
	case "display-mode":
		return is("browser"), true
	case "color-gamut":
		return is("srgb"), true
	case "dynamic-range", "video-dynamic-range":
		return is("standard"), true
	case "overflow-block", "overflow-inline":
		return is("scroll"), true
	case "grid":
		return !bare && value == "0", true
	case "color", "monochrome", "color-index":
		// Eight bits a channel, no monochrome or palette screen.
		have := 0.0
		if name == "color" {
			have = 8
		}
		if bare {
			return have > 0, true
		}
		want, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return false, true
		}
		return webMediaCompare(have, prefix, want), true
	case "resolution", "device-pixel-ratio":
		want, ok := webMediaResolution(value, name == "device-pixel-ratio")
		if bare {
			return true, true
		}
		if !ok {
			return false, true
		}
		return webMediaCompare(webViewportPixelScale(), prefix, want), true
	}
	return false, false
}

// webMediaResolution reads a resolution in dots per CSS pixel: 2dppx, 2x,
// 192dpi, 75.6dpcm, or the bare number of -webkit-device-pixel-ratio.
func webMediaResolution(value string, bareNumber bool) (float64, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	units := []struct {
		suffix string
		scale  float64
	}{{"dppx", 1}, {"dpcm", 2.54 / 96}, {"dpi", 1.0 / 96}, {"x", 1}}
	for _, u := range units {
		if strings.HasSuffix(value, u.suffix) {
			n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(value, u.suffix)), 64)
			return n * u.scale, err == nil
		}
	}
	if bareNumber {
		n, err := strconv.ParseFloat(value, 64)
		return n, err == nil
	}
	return 0, false
}

// webMediaDimension is the viewport size a width or height feature asks for.
func webMediaDimension(name string, w, h float64) (float64, bool) {
	switch name {
	case "width", "device-width":
		return w, true
	case "height", "device-height":
		return h, true
	}
	return 0, false
}

// webMediaRange evaluates the range form of a width, height or aspect-ratio
// feature: one comparison with the name on either side, or the name between
// two values.
func webMediaRange(inner string, w, h float64) bool {
	var parts, ops []string
	start := 0
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if c != '<' && c != '>' && c != '=' {
			continue
		}
		parts = append(parts, strings.TrimSpace(inner[start:i]))
		op := string(c)
		if c != '=' && i+1 < len(inner) && inner[i+1] == '=' {
			op += "="
			i++
		}
		ops = append(ops, op)
		start = i + 1
	}
	parts = append(parts, strings.TrimSpace(inner[start:]))
	if len(ops) == 0 || len(ops) > 2 || len(parts) != len(ops)+1 {
		return true
	}
	// The feature's own value, and a parser for the values it is compared with.
	feature := func(name string) (float64, func(string) (float64, bool), bool) {
		if name == "aspect-ratio" || name == "device-aspect-ratio" {
			return w / h, webMediaRatio, true
		}
		have, ok := webMediaDimension(name, w, h)
		return have, webMediaLength, ok
	}
	if len(ops) == 2 {
		have, parse, ok := feature(parts[1])
		if !ok {
			return true
		}
		low, okLow := parse(parts[0])
		high, okHigh := parse(parts[2])
		if !okLow || !okHigh {
			return true
		}
		return webMediaCmp(low, ops[0], have) && webMediaCmp(have, ops[1], high)
	}
	if have, parse, ok := feature(parts[0]); ok {
		want, valid := parse(parts[1])
		return !valid || webMediaCmp(have, ops[0], want)
	}
	if have, parse, ok := feature(parts[1]); ok {
		want, valid := parse(parts[0])
		return !valid || webMediaCmp(want, ops[0], have)
	}
	return true
}

// webMediaCompare applies a min-/max- prefix ("" is an exact match).
func webMediaCompare(have float64, prefix string, want float64) bool {
	switch prefix {
	case "min":
		return have >= want
	case "max":
		return have <= want
	}
	return math.Abs(have-want) < 0.01
}

// webMediaCmp applies one range operator: a op b.
func webMediaCmp(a float64, op string, b float64) bool {
	switch op {
	case "<":
		return a < b
	case "<=":
		return a <= b
	case ">":
		return a > b
	case ">=":
		return a >= b
	}
	return math.Abs(a-b) < 0.01
}

// webMediaLength reads a media query length in CSS pixels: px, em and rem
// (16px, the initial font size media queries use whatever the page sets), pt,
// or a bare 0. Anything else reads as unknown.
func webMediaLength(value string) (float64, bool) {
	value = strings.TrimSpace(value)
	end := 0
	for end < len(value) && (value[end] >= '0' && value[end] <= '9' || value[end] == '.' || value[end] == '-' || value[end] == '+') {
		end++
	}
	n, err := strconv.ParseFloat(value[:end], 64)
	if err != nil {
		return 0, false
	}
	switch unit := strings.TrimSpace(value[end:]); unit {
	case "px":
		return n, true
	case "em", "rem":
		return n * 16, true
	case "pt":
		return n * 4 / 3, true
	case "":
		return n, n == 0
	}
	return 0, false
}

// webMediaRatio reads an aspect ratio: "16/9", "16 / 9" or a plain number.
func webMediaRatio(value string) (float64, bool) {
	num, den, found := strings.Cut(value, "/")
	a, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
	if err != nil || a <= 0 {
		return 0, false
	}
	if !found {
		return a, true
	}
	b, err := strconv.ParseFloat(strings.TrimSpace(den), 64)
	if err != nil || b <= 0 {
		return 0, false
	}
	return a / b, true
}

// webContainerQuery is one parsed @container condition. The sheet is parsed
// before the HTML, so the query is kept on each enclosed rule and evaluated
// against the parser's open element stack when that rule is matched.
type webContainerQuery struct {
	name      string
	condition string
}

func webParseContainerQuery(text string) *webContainerQuery {
	text = strings.TrimSpace(text)
	lower := strings.ToLower(text)
	if !strings.HasPrefix(lower, "@container") {
		return nil
	}
	rest := strings.TrimSpace(text[len("@container"):])
	if rest == "" {
		return nil
	}
	lower = strings.ToLower(rest)
	name := ""
	if !strings.HasPrefix(lower, "(") && !strings.HasPrefix(lower, "style(") && !strings.HasPrefix(lower, "not ") {
		end := 0
		for end < len(rest) && !webAttrSpaceByte(rest[end]) && rest[end] != '(' {
			end++
		}
		candidate := strings.ToLower(rest[:end])
		if !webContainerNameIdent(candidate) || candidate == "none" || candidate == "and" || candidate == "or" || candidate == "not" {
			return nil
		}
		name = candidate
		rest = strings.TrimSpace(rest[end:])
		lower = strings.ToLower(rest)
	}
	// A style query is written as style(...) rather than a parenthesised
	// feature. Keep its body's case: custom-property names and values are
	// case-sensitive even though the feature names around them are not.
	if rest == "" || (!strings.HasPrefix(rest, "(") && !strings.HasPrefix(lower, "style(") && !strings.HasPrefix(lower, "not ")) {
		return nil
	}
	return &webContainerQuery{name: name, condition: strings.TrimSpace(rest)}
}

// webContainerConditionsApply tests all enclosing @container queries. Each
// resolves its own nearest named and typed container, as nested container
// rules do.
func (p *webParser) webContainerRulesApply(queries []*webContainerQuery) bool {
	for _, query := range queries {
		if !p.webContainerQueryApplies(query) {
			return false
		}
	}
	return true
}

func (p *webParser) webContainerQueryApplies(query *webContainerQuery) bool {
	if query == nil || query.condition == "" {
		return false
	}
	if webContainerQueryUsesStyle(query.condition) {
		idx := p.webNearestStyleContainer(query)
		if webContainerQueryUsesSize(query.condition) {
			// A mixed style/size condition must use a box that can answer
			// the size half; a plain style container has no measured size.
			idx = p.webNearestContainer(query)
		}
		if idx < 0 {
			return false
		}
		w, h := p.webContainerDimensions(idx)
		return webContainerConditionWithStyle(query.condition, w, h, p.stack[idx].style.Vars)
	}
	idx := p.webNearestContainer(query)
	if idx < 0 {
		return false
	}
	w, h := p.webContainerDimensions(idx)
	return webContainerCondition(webMediaTokens(strings.ToLower(query.condition)), w, h)
}

// webContainerQueryUsesStyle reports whether a condition contains a style()
// feature. Style features use the queried container's inherited custom
// properties, while size features use its measured box.
func webContainerQueryUsesStyle(condition string) bool {
	return strings.Contains(strings.ToLower(condition), "style(")
}

// webContainerQueryUsesSize reports whether the condition also asks for a
// box measurement, which needs a size query container rather than any style
// container.
func webContainerQueryUsesSize(condition string) bool {
	terms, _, ok := webSplitContainerLogic(condition)
	if !ok {
		return false
	}
	for _, term := range terms {
		if webContainerTermUsesSize(term) {
			return true
		}
	}
	return false
}

func webContainerTermUsesSize(term string) bool {
	term = strings.TrimSpace(term)
	lower := strings.ToLower(term)
	if strings.HasPrefix(lower, "not ") {
		return webContainerTermUsesSize(term[len("not "):])
	}
	if strings.HasPrefix(lower, "style(") {
		return false
	}
	if term != "" && term[0] == '(' {
		if close := webCSSParenEnd(term, 0); close == len(term)-1 {
			inner := strings.TrimSpace(term[1:close])
			if webContainerQueryUsesStyle(inner) {
				return webContainerQueryUsesSize(inner)
			}
		}
	}
	return true
}

// webNearestStyleContainer finds the container whose variable scope answers a
// style query. Style queries do not need size containment, so an ordinary
// ancestor (container-type: normal) is valid; a named query still requires the
// nearest ancestor with that name.
func (p *webParser) webNearestStyleContainer(query *webContainerQuery) int {
	for i := len(p.stack) - 1; i >= 0; i-- {
		if query.name != "" && p.stack[i].style.ContainerName != query.name {
			continue
		}
		return i
	}
	return -1
}

// webNearestContainer finds the inner-most ancestor that can answer this
// query. A height or aspect-ratio test needs size containment; width tests are
// answered by inline-size and size containers.
func (p *webParser) webNearestContainer(query *webContainerQuery) int {
	needsHeight := webContainerQueryNeedsHeight(query.condition)
	for i := len(p.stack) - 1; i >= 0; i-- {
		st := p.stack[i].style
		if st.ContainerType == 0 {
			continue
		}
		if query.name != "" && st.ContainerName != query.name {
			continue
		}
		if needsHeight && st.ContainerType != 2 {
			continue
		}
		return i
	}
	return -1
}

func webContainerQueryNeedsHeight(condition string) bool {
	condition = strings.ToLower(condition)
	return strings.Contains(condition, "height") || strings.Contains(condition, "block-size") ||
		strings.Contains(condition, "aspect-ratio") || strings.Contains(condition, "orientation")
}

// webContainerDimensions resolves the size of one container from the same
// width-and-height chain the image sizes code uses: the viewport is the base,
// and each ancestor's explicit width or height narrows it.
func (p *webParser) webContainerDimensions(idx int) (w, h float64) {
	w, h = webMediaViewport()
	for i := 0; i <= idx && i < len(p.stack); i++ {
		st := p.stack[i].style
		if st.WidthPct > 0 {
			w = w * st.WidthPct / 100
		} else if st.Width > 0 {
			w = st.Width
		}
		if st.Height > 0 {
			h = st.Height
		}
	}
	if w <= 0 {
		w = 1
	}
	if h <= 0 {
		h = 1
	}
	return w, h
}

func webContainerCondition(tokens []string, w, h float64) bool {
	if len(tokens) == 0 {
		return false
	}
	if tokens[0] == "not" {
		if len(tokens) == 2 {
			return !webContainerInParens(tokens[1], w, h)
		}
		return false
	}
	result := webContainerInParens(tokens[0], w, h)
	for i := 1; i+1 < len(tokens); i += 2 {
		next := webContainerInParens(tokens[i+1], w, h)
		switch tokens[i] {
		case "and":
			result = result && next
		case "or":
			result = result || next
		default:
			return false
		}
	}
	return result
}

func webContainerInParens(group string, w, h float64) bool {
	if len(group) < 2 || group[0] != '(' || group[len(group)-1] != ')' {
		return false
	}
	inner := strings.TrimSpace(group[1 : len(group)-1])
	lower := strings.ToLower(inner)
	if inner == "" || strings.HasPrefix(lower, "style(") || strings.HasPrefix(lower, "scroll-state(") {
		return false
	}
	if words := webMediaTokens(inner); len(words) > 1 || strings.HasPrefix(inner, "(") {
		if words[0] == "not" || strings.HasPrefix(words[0], "(") {
			return webContainerCondition(words, w, h)
		}
	}
	return webContainerFeature(inner, w, h)
}

// webContainerConditionWithStyle evaluates a container condition that may mix
// style() and size features. Style features look at the queried container's
// inherited variable scope; size features keep using the existing size parser.
func webContainerConditionWithStyle(condition string, w, h float64, scope *webVarScope) bool {
	condition = strings.TrimSpace(condition)
	lower := strings.ToLower(condition)
	if strings.HasPrefix(lower, "not ") {
		return !webContainerConditionWithStyle(condition[len("not "):], w, h, scope)
	}
	terms, ops, ok := webSplitContainerLogic(condition)
	if !ok || len(terms) == 0 {
		return false
	}
	result := webContainerTermWithStyle(terms[0], w, h, scope)
	for i, op := range ops {
		next := webContainerTermWithStyle(terms[i+1], w, h, scope)
		switch op {
		case "and":
			result = result && next
		case "or":
			result = result || next
		default:
			return false
		}
	}
	return result
}

// webSplitContainerLogic divides a condition on top-level and/or operators.
// Parenthesised arguments (including style() values) stay whole.
func webSplitContainerLogic(text string) ([]string, []string, bool) {
	var terms, ops []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
			continue
		case c == '\'' || c == '"':
			quote = c
			continue
		case c == '(':
			depth++
			continue
		case c == ')':
			depth--
			if depth < 0 {
				return nil, nil, false
			}
			continue
		}
		if depth != 0 {
			continue
		}
		for _, op := range []string{"and", "or"} {
			if i+len(op) > len(text) || !strings.EqualFold(text[i:i+len(op)], op) {
				continue
			}
			if i == 0 || !webAttrSpaceByte(text[i-1]) {
				continue
			}
			if i+len(op) == len(text) || !webAttrSpaceByte(text[i+len(op)]) {
				continue
			}
			terms = append(terms, strings.TrimSpace(text[start:i]))
			ops = append(ops, op)
			i += len(op) - 1
			start = i + 1
			break
		}
	}
	if depth != 0 || quote != 0 {
		return nil, nil, false
	}
	terms = append(terms, strings.TrimSpace(text[start:]))
	return terms, ops, true
}

func webContainerTermWithStyle(term string, w, h float64, scope *webVarScope) bool {
	term = strings.TrimSpace(term)
	if term == "" {
		return false
	}
	lower := strings.ToLower(term)
	if strings.HasPrefix(lower, "style(") {
		return webStyleQueryFeature(term, scope)
	}
	if strings.HasPrefix(lower, "not ") {
		return !webContainerTermWithStyle(term[len("not "):], w, h, scope)
	}
	if term[0] == '(' {
		close := webCSSParenEnd(term, 0)
		if close == len(term)-1 {
			inner := strings.TrimSpace(term[1:close])
			if strings.Contains(strings.ToLower(inner), "style(") {
				return webContainerConditionWithStyle(inner, w, h, scope)
			}
			return webContainerInParens(term, w, h)
		}
	}
	return false
}

// webStyleQueryFeature evaluates style(--name: value) or style(--name). The
// value comes from the queried element's inherited custom-property scope, so a
// declaration on an ancestor is visible here and a closer redeclaration wins.
func webStyleQueryFeature(term string, scope *webVarScope) bool {
	lower := strings.ToLower(term)
	if !strings.HasPrefix(lower, "style(") {
		return false
	}
	open := strings.IndexByte(term, '(')
	close := webCSSParenEnd(term, open)
	if open < 0 || close < 0 || strings.TrimSpace(term[close+1:]) != "" {
		return false
	}
	inner := strings.TrimSpace(term[open+1 : close])
	if inner == "" {
		return false
	}
	colon := strings.IndexByte(inner, ':')
	if colon < 0 {
		name := strings.TrimSpace(inner)
		if !strings.HasPrefix(name, "--") {
			return false
		}
		_, ok := scope.lookup(name)
		return ok
	}
	name := strings.TrimSpace(inner[:colon])
	want := strings.TrimSpace(inner[colon+1:])
	if !strings.HasPrefix(name, "--") || want == "" {
		return false
	}
	have, ok := scope.lookup(name)
	if !ok {
		return false
	}
	want = webSubstituteVars(want, scope)
	return strings.TrimSpace(have) == strings.TrimSpace(want)
}

func webContainerFeature(inner string, w, h float64) bool {
	inner = strings.ToLower(strings.TrimSpace(inner))
	if colon := strings.IndexByte(inner, ':'); colon >= 0 {
		name := strings.TrimSpace(inner[:colon])
		value := strings.TrimSpace(inner[colon+1:])
		prefix := ""
		if strings.HasPrefix(name, "min-") || strings.HasPrefix(name, "max-") {
			prefix, name = name[:3], name[4:]
		}
		name = webContainerFeatureAlias(name)
		switch name {
		case "orientation":
			switch value {
			case "landscape":
				return w > h
			case "portrait":
				return h >= w
			}
			return false
		case "aspect-ratio":
			want, ok := webMediaRatio(value)
			return ok && webMediaCompare(w/h, prefix, want)
		case "width":
			want, ok := webContainerLength(value, w, h)
			return ok && webMediaCompare(w, prefix, want)
		case "height":
			want, ok := webContainerLength(value, w, h)
			return ok && webMediaCompare(h, prefix, want)
		}
		return false
	}
	if strings.ContainsAny(inner, "<>= ") {
		return webContainerRange(inner, w, h)
	}
	switch webContainerFeatureAlias(inner) {
	case "width":
		return w > 0
	case "height":
		return h > 0
	case "aspect-ratio":
		return w > 0 && h > 0
	}
	return false
}

func webContainerFeatureAlias(name string) string {
	switch name {
	case "inline-size":
		return "width"
	case "block-size":
		return "height"
	}
	return name
}

func webContainerLength(value string, w, h float64) (float64, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, unit := range []string{"cqmin", "cqmax", "cqw", "cqi", "cqh", "cqb"} {
		if !strings.HasSuffix(value, unit) {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(value, unit)), 64)
		if err != nil {
			return 0, false
		}
		switch unit {
		case "cqw", "cqi":
			return n * w / 100, true
		case "cqh", "cqb":
			return n * h / 100, true
		case "cqmin":
			return n * min(w, h) / 100, true
		default:
			return n * max(w, h) / 100, true
		}
	}
	return webMediaLength(value)
}

func webContainerRange(inner string, w, h float64) bool {
	var parts, ops []string
	start := 0
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if c != '<' && c != '>' && c != '=' {
			continue
		}
		parts = append(parts, strings.TrimSpace(inner[start:i]))
		op := string(c)
		if c != '=' && i+1 < len(inner) && inner[i+1] == '=' {
			op += "="
			i++
		}
		ops = append(ops, op)
		start = i + 1
	}
	parts = append(parts, strings.TrimSpace(inner[start:]))
	if len(ops) == 0 || len(ops) > 2 || len(parts) != len(ops)+1 {
		return false
	}
	feature := func(name string) (float64, func(string) (float64, bool), bool) {
		switch webContainerFeatureAlias(name) {
		case "width":
			return w, func(value string) (float64, bool) { return webContainerLength(value, w, h) }, true
		case "height":
			return h, func(value string) (float64, bool) { return webContainerLength(value, w, h) }, true
		case "aspect-ratio":
			return w / h, webMediaRatio, true
		}
		return 0, nil, false
	}
	if len(ops) == 2 {
		have, parse, ok := feature(parts[1])
		if !ok {
			return false
		}
		low, okLow := parse(parts[0])
		high, okHigh := parse(parts[2])
		if !okLow || !okHigh {
			return false
		}
		return webMediaCmp(low, ops[0], have) && webMediaCmp(have, ops[1], high)
	}
	if have, parse, ok := feature(parts[0]); ok {
		want, valid := parse(parts[1])
		return valid && webMediaCmp(have, ops[0], want)
	}
	if have, parse, ok := feature(parts[1]); ok {
		want, valid := parse(parts[0])
		return valid && webMediaCmp(want, ops[0], have)
	}
	return false
}

// parseWebCSS turns a stylesheet into rules. An at-rule block is skipped unless
// it describes this screen: @media (except print/speech), @supports (evaluated
// against the declarations and selectors this reader implements), @container
// (kept as a condition on each enclosed rule and resolved against the parser's
// open container stack at match time), and @layer, whose rules carry their
// layer's place in the cascade (webCSSParse).
// Test seam (ARCH-8): only tests call it.
func parseWebCSS(src string) []webRule {
	rules := make([]webRule, 0, 32)
	ps := &webCSSParse{}
	parseWebCSSInto(stripWebCSSComments(src), 0, ps, &rules, nil, "")
	return rules
}

const (
	// webCSSMaxNesting bounds how deep conditional group rules are followed.
	// Each level re-scanned its whole body, so a sheet of nothing but
	// "@media a{" (58,000 levels in the 512 KB a page may load) cost
	// quadratic time and gigabytes of copies before the page appeared.
	webCSSMaxNesting = 8
	// webCSSMaxRules bounds the rules one page keeps; a large site uses a few
	// thousand. Matching cost no longer grows with it (webRuleIndex).
	webCSSMaxRules = 24000
)

// webUnlayered is the cascade-layer rank of a rule outside every @layer: it
// beats every layered rule of the same importance, whatever their
// specificity or order (and loses to them among !important ones).
const webUnlayered = 1 << 20

// webCSSParse is the state one stylesheet parse threads through its nested
// blocks: the source-order counter and the cascade layers, ranked in the order
// the sheet first names them — `@layer theme, base, components, utilities;` is
// how Tailwind CSS 4 orders its whole output.
type webCSSParse struct {
	order  int
	layers map[string]int
	anon   int
	// fontFaces, when set, collects the bodies of the @font-face rules in
	// force (webpage_webfont.go).
	fontFaces *[]string
	// keyframes, when set, collects each @keyframes animation's end state:
	// the declarations of its `to` / `100%` frame (webKeyframesEnd).
	keyframes map[string]map[string]string
}

// layerRank is a layer's place in the cascade, registering the name on first
// sight; "" is outside every layer.
func (ps *webCSSParse) layerRank(name string) int {
	if name == "" {
		return webUnlayered
	}
	if ps.layers == nil {
		ps.layers = make(map[string]int, 4)
	}
	if r, ok := ps.layers[name]; ok {
		return r
	}
	r := len(ps.layers) + 1
	ps.layers[name] = r
	return r
}

// blockLayer is the full name of the layer an `@layer name { … }` block opens
// inside parent: framework.base for `@layer base` in `@layer framework`. An
// anonymous block is a layer of its own.
func (ps *webCSSParse) blockLayer(atRule, parent string) string {
	name := strings.TrimSpace(atRule[len("@layer"):])
	if name == "" {
		ps.anon++
		name = "#" + strconv.Itoa(ps.anon)
	}
	if parent != "" {
		return parent + "." + name
	}
	return name
}

// noteLayerStatements registers the names of the `@layer a, b;` statements in
// front of a block, so they rank in the order the statement lists them.
func (ps *webCSSParse) noteLayerStatements(text, parent string) {
	for strings.HasPrefix(text, "@") {
		end := strings.IndexByte(text, ';')
		if end < 0 {
			return
		}
		statement := strings.TrimSpace(text[:end])
		if len(statement) > len("@layer") && asciiLower(statement[:len("@layer")]) == "@layer" {
			for _, name := range strings.Split(statement[len("@layer"):], ",") {
				if name = strings.TrimSpace(name); name != "" {
					if parent != "" {
						name = parent + "." + name
					}
					ps.layerRank(name)
				}
			}
		}
		text = strings.TrimSpace(text[end+1:])
	}
}

// parseWebCSSInto appends the rules of one (comment-free) block to rules, in
// the cascade layer named layer ("" for none).
func parseWebCSSInto(src string, depth int, ps *webCSSParse, rules *[]webRule, containers []*webContainerQuery, layer string) {
	for i := 0; i < len(src) && len(*rules) < webCSSMaxRules; {
		open := strings.IndexByte(src[i:], '{')
		if open < 0 {
			break
		}
		open += i
		prefix := strings.TrimSpace(src[i:open])
		ps.noteLayerStatements(prefix, layer)
		selectorText := webSkipCSSStatementRules(prefix)
		close := webCSSBlockEnd(src, open)
		if close < 0 {
			break
		}
		body := src[open+1 : close]
		i = close + 1
		if selectorText == "" {
			continue
		}
		if strings.HasPrefix(selectorText, "@") {
			lower := strings.ToLower(selectorText)
			if strings.HasPrefix(lower, "@keyframes") || strings.HasPrefix(lower, "@-webkit-keyframes") || strings.HasPrefix(lower, "@-moz-keyframes") {
				if ps.keyframes != nil && len(ps.keyframes) < 512 {
					if name := webKeyframesName(selectorText); name != "" {
						if end := webKeyframesEnd(body); end != nil {
							ps.keyframes[name] = end
						} else {
							delete(ps.keyframes, name)
						}
					}
				}
				continue
			}
			if strings.HasPrefix(lower, "@font-face") {
				if ps.fontFaces != nil && len(*ps.fontFaces) < webMaxFontFaceRules {
					*ps.fontFaces = append(*ps.fontFaces, body)
				}
				continue
			}
			if strings.HasPrefix(lower, "@scope") {
				// @scope (.card) { img { … } }: the rules inside apply within
				// their scope root, as if nested in it (`:scope` is the root
				// itself). The lower boundary (`to (…)`) is not modelled: the
				// scope runs to the end of the root's subtree.
				if roots := webScopeRoots(selectorText); depth < webCSSMaxNesting {
					webEmitNestedCSSBodyAt(webScopeBody(body), roots, depth+1, ps, rules, containers, layer, true)
				}
				continue
			}
			if strings.HasPrefix(lower, "@container") {
				query := webParseContainerQuery(selectorText)
				if query != nil && depth < webCSSMaxNesting {
					next := append(append([]*webContainerQuery(nil), containers...), query)
					parseWebCSSInto(body, depth+1, ps, rules, next, layer)
				}
				continue
			}
			if strings.HasPrefix(lower, "@layer") {
				inner := ps.blockLayer(selectorText, layer)
				ps.layerRank(inner)
				if depth < webCSSMaxNesting {
					parseWebCSSInto(body, depth+1, ps, rules, containers, inner)
				}
				continue
			}
			keep := false
			if strings.HasPrefix(lower, "@supports") {
				keep = webSupportsApplies(selectorText)
			}
			if strings.HasPrefix(lower, "@media") {
				keep = webMediaBlockApplies(selectorText)
			}
			// A conditional group rule nests: `@media` inside `@layer` still
			// has to be judged on its own query.
			if keep && depth < webCSSMaxNesting {
				parseWebCSSInto(body, depth+1, ps, rules, containers, layer)
			}
			continue
		}
		// CSS nesting puts child rules inside the parent's declaration block.
		// Modern component CSS uses it heavily; reading the block as only
		// declarations dropped every nested rule. Keep the fast path for the
		// ordinary flat sheet.
		if strings.IndexByte(body, '{') >= 0 {
			if webEmitNestedCSSBody(body, webSplitSelectorList(selectorText), depth, ps, rules, containers, layer) {
				continue
			}
		}
		decls, important := parseWebDeclsSplit(body)
		if len(decls) == 0 && len(important) == 0 {
			continue
		}
		webAppendCSSRule(webSplitSelectorList(selectorText), decls, important, ps, rules, containers, layer)
	}
}

// webScopeRoots is the scope root selector list of an @scope prelude, `:root`
// when it names none (an @scope inside a <style> is scoped to the element the
// sheet sits in, which a sheet parsed whole cannot know).
func webScopeRoots(prelude string) []string {
	rest := strings.TrimSpace(prelude[len("@scope"):])
	if strings.HasPrefix(rest, "(") {
		if end := webCSSParenEnd(rest, 0); end > 0 {
			if roots := webSplitSelectorList(strings.TrimSpace(rest[1:end])); len(roots) > 0 {
				return roots
			}
		}
	}
	return []string{":root"}
}

// webScopeBody rewrites :scope in an @scope body as the nesting selector &,
// which stands for the scope root there.
func webScopeBody(body string) string {
	lower := asciiLower(body)
	if !strings.Contains(lower, ":scope") {
		return body
	}
	var out strings.Builder
	last := 0
	for i := strings.Index(lower, ":scope"); i >= 0; {
		end := i + len(":scope")
		if end < len(lower) && (isWebIdentByte(lower[end]) || lower[end] == '-') {
			next := strings.Index(lower[end:], ":scope")
			if next < 0 {
				break
			}
			i = end + next
			continue
		}
		out.WriteString(body[last:i])
		out.WriteString("&")
		last = end
		next := strings.Index(lower[end:], ":scope")
		if next < 0 {
			break
		}
		i = end + next
	}
	out.WriteString(body[last:])
	return out.String()
}

// webCSSNestedBlock is one nested selector (or conditional at-rule) inside a
// declaration block. body is the inside of its braces.
type webCSSNestedBlock struct {
	selector string
	body     string
}

// webSplitCSSNestedBody separates declarations from nested rules at one level
// of a declaration block. Parentheses, brackets, strings and comments are
// skipped, so a data URI or a selector attribute containing braces cannot cut
// a rule in half.
func webSplitCSSNestedBody(body string) (declText string, nested []webCSSNestedBlock) {
	var decls strings.Builder
	start := 0
	var quote byte
	paren, bracket := 0, 0
	for i := 0; i < len(body); i++ {
		c := body[i]
		if quote != 0 {
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '(':
			paren++
		case ')':
			if paren > 0 {
				paren--
			}
		case '[':
			bracket++
		case ']':
			if bracket > 0 {
				bracket--
			}
		case ';':
			if paren == 0 && bracket == 0 {
				decls.WriteString(body[start : i+1])
				start = i + 1
			}
		case '{':
			if paren != 0 || bracket != 0 {
				continue
			}
			selector := strings.TrimSpace(body[start:i])
			close := webCSSBlockEnd(body, i)
			if close < 0 {
				continue
			}
			if selector != "" {
				nested = append(nested, webCSSNestedBlock{selector: selector, body: body[i+1 : close]})
			}
			i = close
			start = close + 1
		}
	}
	decls.WriteString(body[start:])
	return decls.String(), nested
}

// webEmitNestedCSSBody processes a parent declaration block that also holds
// nested rules. It reports whether it found any nested block, so a flat body
// can stay on the parser's original, cheaper path.
func webEmitNestedCSSBody(body string, parents []string, depth int, ps *webCSSParse, rules *[]webRule, containers []*webContainerQuery, layer string) bool {
	return webEmitNestedCSSBodyAt(body, parents, depth, ps, rules, containers, layer, false)
}

// webEmitNestedCSSBodyAt is the recursive worker. force is set for any body
// reached through a nested selector or at-rule: such a body may contain only
// declarations for its parent, while the top-level fast path may not.
func webEmitNestedCSSBodyAt(body string, parents []string, depth int, ps *webCSSParse, rules *[]webRule, containers []*webContainerQuery, layer string, force bool) bool {
	declText, nested := webSplitCSSNestedBody(body)
	if len(nested) == 0 && !force {
		return false
	}
	if depth >= webCSSMaxNesting {
		return true
	}
	if decls, important := parseWebDeclsSplit(declText); len(decls) > 0 || len(important) > 0 {
		webAppendCSSRule(parents, decls, important, ps, rules, containers, layer)
	}
	for _, block := range nested {
		selector := strings.TrimSpace(block.selector)
		if selector == "" {
			continue
		}
		if strings.HasPrefix(selector, "@") {
			lower := strings.ToLower(selector)
			switch {
			case strings.HasPrefix(lower, "@media"):
				if webMediaBlockApplies(selector) {
					webEmitNestedCSSBodyAt(block.body, parents, depth+1, ps, rules, containers, layer, true)
				}
			case strings.HasPrefix(lower, "@supports"):
				if webSupportsApplies(selector) {
					webEmitNestedCSSBodyAt(block.body, parents, depth+1, ps, rules, containers, layer, true)
				}
			case strings.HasPrefix(lower, "@container"):
				query := webParseContainerQuery(selector)
				if query != nil {
					next := append(append([]*webContainerQuery(nil), containers...), query)
					webEmitNestedCSSBodyAt(block.body, parents, depth+1, ps, rules, next, layer, true)
				}
			case strings.HasPrefix(lower, "@layer"):
				inner := ps.blockLayer(selector, layer)
				ps.layerRank(inner)
				webEmitNestedCSSBodyAt(block.body, parents, depth+1, ps, rules, containers, inner, true)
			}
			continue
		}
		combined := webCombineNestedSelectors(parents, selector)
		if len(combined) > 0 {
			webEmitNestedCSSBodyAt(block.body, combined, depth+1, ps, rules, containers, layer, true)
		}
	}
	return true
}

// webCombineNestedSelectors resolves CSS nesting's relative selectors. A
// nested `&` stands for each parent selector; a selector without one is a
// descendant of the parent, and a leading combinator stays adjacent to it.
func webCombineNestedSelectors(parents []string, nested string) []string {
	parts := webSplitSelectorList(nested)
	out := make([]string, 0, len(parents)*len(parts))
	for _, parent := range parents {
		parent = strings.TrimSpace(parent)
		if parent == "" {
			continue
		}
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if strings.Contains(part, "&") {
				out = append(out, strings.ReplaceAll(part, "&", parent))
				continue
			}
			out = append(out, parent+" "+part)
		}
	}
	return out
}

// webAppendCSSRule compiles one selector list against a declaration block and
// appends the resulting rules in source order, in the cascade layer named
// layer.
func webAppendCSSRule(selectors []string, decls, important map[string]string, ps *webCSSParse, rules *[]webRule, containers []*webContainerQuery, layer string) {
	rank := ps.layerRank(layer)
	hides := webDeclsHide(decls) || webDeclsHide(important)
	reveals := webDeclsReveal(decls) || webDeclsReveal(important)
	for _, selector := range selectors {
		selector = strings.TrimSpace(selector)
		if selector == "" {
			continue
		}
		if len(*rules) >= webCSSMaxRules {
			break
		}
		sel, specificity := webCompileSelector(selector)
		if sel.never {
			// A ::before/::after/::marker rule styles a generated box, never
			// an element. Icon fonts ship a thousand of them; dropping them
			// here keeps them out of every element's matching loop.
			continue
		}
		*rules = append(*rules, webRule{
			selector:    selector,
			specificity: specificity,
			order:       ps.order,
			layer:       rank,
			decls:       decls,
			important:   important,
			sel:         sel,
			hides:       hides,
			reveals:     reveals,
			containers:  containers,
		})
		ps.order++
	}
}

// webSupportedProperties is the CSS surface this reader implements. It is
// deliberately a declaration whitelist rather than a blanket "modern browser"
// answer: @supports exists to select fallbacks, and claiming a property we
// ignore would make the fallback styling win over the one that actually works.
var webSupportedProperties = map[string]bool{
	"align-content": true, "align-items": true, "align-self": true, "aspect-ratio": true,
	"background": true, "background-clip": true, "background-color": true, "background-image": true,
	"border": true, "border-bottom": true, "border-color": true,
	"border-left": true, "border-radius": true, "border-right": true,
	"border-top": true, "border-width": true, "color": true,
	// Logical borders are the physical sides webLogicalBorderDecls rewrites
	// them to, so an @supports test names them the way the page did.
	"border-inline": true, "border-inline-start": true, "border-inline-end": true,
	"border-block": true, "border-block-start": true, "border-block-end": true,
	"border-inline-width": true, "border-block-width": true,
	"border-inline-start-width": true, "border-inline-end-width": true,
	"border-block-start-width": true, "border-block-end-width": true,
	"border-start-start-radius": true, "border-start-end-radius": true,
	"border-end-start-radius": true, "border-end-end-radius": true,
	"text-underline-offset": true, "font-variant": true, "font-variant-caps": true,
	"column-gap": true, "container": true, "container-name": true, "container-type": true,
	"display": true, "flex": true, "flex-basis": true, "flex-direction": true, "flex-flow": true,
	"float": true, "clear": true,
	"flex-grow": true, "flex-wrap": true, "font": true, "font-size": true,
	"font-style": true, "font-weight": true, "gap": true,
	"grid-template-columns": true, "grid-column": true, "grid-column-start": true,
	"grid-column-end": true, "grid-template-rows": true, "grid-row": true,
	"grid-row-start": true, "grid-row-end": true, "grid-template-areas": true,
	"grid-area": true, "grid-auto-flow": true, "grid-auto-columns": true,
	"grid-auto-rows": true, "justify-items": true, "justify-self": true,
	"place-self": true, "place-content": true, "height": true, "inset": true,
	"inset-block": true, "inset-inline": true,
	"inset-block-start": true, "inset-block-end": true,
	"inset-inline-start": true, "inset-inline-end": true,
	"justify-content": true, "line-height": true, "margin": true,
	"margin-block": true, "margin-bottom": true, "margin-left": true,
	"margin-right": true, "margin-top": true, "opacity": true,
	"order": true, "padding": true, "padding-block": true,
	"padding-bottom": true, "padding-left": true, "padding-right": true,
	"padding-top": true, "place-items": true, "position": true,
	"overflow-wrap": true, "word-wrap": true, "word-break": true, "text-overflow": true,
	"row-gap": true, "text-align": true, "text-indent": true,
	"text-transform": true, "visibility": true, "white-space": true,
	"width": true, "z-index": true, "table-layout": true,
	"border-collapse": true, "border-spacing": true,
	"line-clamp": true,
}

// webSupportsApplies evaluates an @supports condition. It understands
// declaration tests, selector(), and the and/or/not combinators; an unreadable
// or unsupported condition is false, which lets the page's real fallback run.
func webSupportsApplies(query string) bool {
	query = strings.TrimSpace(query)
	if strings.HasPrefix(asciiLower(query), "@supports") {
		query = strings.TrimSpace(query[len("@supports"):])
	}
	if query == "" {
		return false
	}
	return webSupportsCondition(webSupportsTokens(query))
}

// webSupportsTokens splits a condition into words and whole parenthesised or
// function groups, so selector(:has(*)) stays one token.
func webSupportsTokens(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		if webAttrSpaceByte(s[i]) {
			i++
			continue
		}
		start := i
		if s[i] == '(' {
			if end := webCSSParenEnd(s, i); end >= 0 {
				i = end + 1
			} else {
				i = len(s)
			}
			out = append(out, s[start:i])
			continue
		}
		for i < len(s) && !webAttrSpaceByte(s[i]) && s[i] != '(' {
			i++
		}
		if i < len(s) && s[i] == '(' {
			if end := webCSSParenEnd(s, i); end >= 0 {
				i = end + 1
			} else {
				i = len(s)
			}
		}
		out = append(out, s[start:i])
	}
	return out
}

func webSupportsCondition(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	if strings.EqualFold(tokens[0], "not") {
		return !webSupportsCondition(tokens[1:])
	}
	result := webSupportsInParens(tokens[0])
	for i := 1; i+1 < len(tokens); i += 2 {
		next := webSupportsInParens(tokens[i+1])
		switch strings.ToLower(tokens[i]) {
		case "and":
			result = result && next
		case "or":
			result = result || next
		default:
			return false
		}
	}
	return result
}

func webSupportsInParens(token string) bool {
	if strings.HasPrefix(asciiLower(token), "selector(") && strings.HasSuffix(token, ")") {
		return webSupportsSelector(token[len("selector(") : len(token)-1])
	}
	if len(token) < 2 || token[0] != '(' || token[len(token)-1] != ')' {
		return false
	}
	inner := strings.TrimSpace(token[1 : len(token)-1])
	if inner == "" {
		return false
	}
	if strings.HasPrefix(asciiLower(inner), "selector(") && strings.HasSuffix(inner, ")") {
		return webSupportsSelector(inner[len("selector(") : len(inner)-1])
	}
	if name, value, ok := webSupportsDeclaration(inner); ok {
		return webSupportsValue(name, value)
	}
	return webSupportsCondition(webSupportsTokens(inner))
}

// webSupportsDeclaration splits a declaration test at its top-level colon. A
// nested condition such as `not (display: grid)` has only a nested colon, so
// it is not mistaken for a declaration.
func webSupportsDeclaration(text string) (name, value string, ok bool) {
	depth := 0
	var quote byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '(':
			depth++
		case ')':
			depth--
		case ':':
			if depth == 0 {
				return strings.ToLower(strings.TrimSpace(text[:i])), strings.TrimSpace(text[i+1:]), true
			}
		}
	}
	return "", "", false
}

func webSupportsValue(name, value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasSuffix(strings.ToLower(value), "!important") {
		value = strings.TrimSpace(value[:len(value)-len("!important")])
	}
	if name == "" || value == "" {
		return false
	}
	if strings.HasPrefix(name, "--") {
		return true
	}
	// Vendor-prefixed aliases share the grammar of the standard property the
	// reader implements. An unknown base name stays unsupported, so a prefix
	// cannot turn an unimplemented property into a false positive.
	if base, prefixed := webVendorPropertyBase(name); prefixed {
		name = base
	}
	lower := strings.ToLower(strings.TrimSpace(value))
	if alias, ok := webUnprefixedValueAlias(name, value); ok {
		value = alias
		lower = strings.ToLower(value)
	}
	if webSupportsCSSWideKeyword(lower) {
		// `all` exists only to apply a CSS-wide keyword to every property.
		return webSupportedProperties[name] || name == "all"
	}
	if name == "all" || !webSupportedProperties[name] {
		return false
	}
	// Keywords that mean "the engine's initial value" do not necessarily
	// change a probe style, so validate them by property group instead.
	switch lower {
	case "none":
		switch name {
		case "display", "background", "background-image", "border", "border-left", "border-right", "border-top", "border-bottom", "text-decoration", "text-decoration-line", "grid-template-columns", "flex", "container", "container-name":
			return true
		}
		return false
	case "normal":
		switch name {
		case "font-style", "font-weight", "line-height", "white-space", "container-type":
			return true
		}
		return false
	case "auto":
		switch name {
		case "width", "height", "margin", "margin-block", "margin-bottom", "margin-left", "margin-right", "margin-top", "inset", "flex-basis":
			return true
		}
		return false
	}
	if strings.Contains(lower, "var(") {
		return true
	}
	value = webResolveValue(value, nil, 16)
	if ok, handled := webSupportsParsedValue(name, value); handled {
		return ok
	}
	// Run every remaining declaration through the real property parsers. A
	// supported value changes at least one field of a probe; an unknown value
	// leaves every probe untouched. Display is varied because
	// grid-template-columns only parses on a grid, and booleans are varied so
	// both `bold` and `normal` style values are observable.
	for _, display := range []int{webDisplayBlock, webDisplayFlex, webDisplayGrid} {
		for _, flag := range []bool{false, true} {
			probe := webSupportsProbeStyle(display, flag)
			before := probe
			applyWebDecls(&probe, map[string]string{name: value})
			if !reflect.DeepEqual(before, probe) {
				return true
			}
		}
	}
	return false
}

// webSupportsProbeStyle makes every populated scalar field distinct from the
// value a declaration would normally write, so a no-op parse and a rejected
// value are both visible as a lack of change.
func webSupportsProbeStyle(display int, flag bool) webStyle {
	st := webBaseStyle()
	v := reflect.ValueOf(&st).Elem()
	colorType := reflect.TypeOf(sdl.Color{})
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if !f.CanSet() {
			continue
		}
		switch f.Kind() {
		case reflect.Bool:
			f.SetBool(flag)
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			f.SetInt(97)
		case reflect.Float32, reflect.Float64:
			f.SetFloat(97.5)
		case reflect.String:
			f.SetString("probe")
		case reflect.Struct:
			if f.Type() == colorType {
				f.Set(reflect.ValueOf(sdl.Color{R: 11, G: 22, B: 33, A: 44}))
			}
		}
	}
	st.Display = display
	return st
}

// webVendorPropertyBase strips a recognised CSS vendor prefix. The boolean
// reports whether the name actually carried one; validation still checks the
// resulting base property against the reader's supported surface.
func webVendorPropertyBase(name string) (string, bool) {
	for _, prefix := range []string{"-webkit-", "-moz-", "-ms-", "-o-"} {
		if strings.HasPrefix(name, prefix) {
			base := strings.TrimPrefix(name, prefix)
			return base, base != "" && !strings.HasPrefix(base, "-")
		}
	}
	return name, false
}

func webSupportsCSSWideKeyword(value string) bool {
	switch value {
	case "inherit", "initial", "unset", "revert", "revert-layer":
		return true
	}
	return false
}

// webUnprefixedValueAlias maps the vendor-prefixed value spellings browsers
// shipped to the standard value the reader implements. It only handles values
// with a known equivalent; anything else is left for the normal grammar check
// to reject.
func webUnprefixedValueAlias(name, value string) (string, bool) {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	switch name {
	case "display":
		switch lower {
		case "-webkit-flex":
			return "flex", true
		case "-webkit-inline-flex":
			return "inline-flex", true
		case "-webkit-box":
			return "flex", true
		case "-webkit-inline-box":
			return "inline-flex", true
		}
	case "background", "background-image":
		for _, function := range []string{
			"linear-gradient",
			"repeating-linear-gradient",
			"radial-gradient",
			"repeating-radial-gradient",
		} {
			prefix := "-webkit-" + function + "("
			if strings.HasPrefix(lower, prefix) {
				return function + "(" + value[len(prefix):], true
			}
		}
	}
	return value, false
}

// webSupportsParsedValue checks the properties whose parsers accept a broad
// value space and therefore need their own grammar test. The bool result is
// whether the declaration is supported; handled reports that the property was
// answered here and should not fall through to the probe.
func webSupportsParsedValue(name, value string) (supported, handled bool) {
	lower := strings.ToLower(strings.TrimSpace(value))
	switch name {
	case "display":
		return webSupportsEnum(lower, "block", "inline", "inline-block", "flex", "inline-flex", "grid", "inline-grid", "flow-root", "none",
			"table", "table-row", "table-cell", "table-header-group", "table-row-group", "table-footer-group", "table-caption"), true
	case "position":
		return webSupportsEnum(lower, "static", "relative", "absolute", "fixed", "sticky"), true
	case "float":
		return webSupportsEnum(lower, "none", "left", "right", "inline-start", "inline-end"), true
	case "clear":
		return webSupportsEnum(lower, "none", "left", "right", "both", "inline-start", "inline-end"), true
	case "visibility":
		return webSupportsEnum(lower, "visible", "hidden", "collapse"), true
	case "font-style":
		return webSupportsEnum(lower, "normal", "italic", "oblique") || strings.HasPrefix(lower, "oblique "), true
	case "font-weight":
		return webSupportsEnum(lower, "normal", "bold", "bolder", "lighter", "100", "200", "300", "400", "500", "600", "700", "800", "900"), true
	case "text-align":
		return webSupportsEnum(lower, "left", "right", "center", "justify", "start", "end"), true
	case "text-transform":
		return webSupportsEnum(lower, "none", "uppercase", "lowercase", "capitalize"), true
	case "white-space":
		return webSupportsEnum(lower, "normal", "nowrap", "pre", "pre-wrap", "pre-line", "break-spaces"), true
	case "overflow-wrap", "word-wrap":
		return webSupportsEnum(lower, "normal", "anywhere", "break-word"), true
	case "word-break":
		return webSupportsEnum(lower, "normal", "keep-all", "break-all", "break-word"), true
	case "text-overflow":
		return webSupportsEnum(lower, "clip", "ellipsis"), true
	case "flex-direction":
		return webSupportsEnum(lower, "row", "row-reverse", "column", "column-reverse"), true
	case "flex-wrap":
		return webSupportsEnum(lower, "nowrap", "wrap", "wrap-reverse"), true
	case "flex-flow":
		fields := webCSSSpaceFields(lower)
		if len(fields) == 0 || len(fields) > 2 {
			return false, true
		}
		for _, field := range fields {
			if !webSupportsEnum(field, "row", "row-reverse", "column", "column-reverse", "nowrap", "wrap", "wrap-reverse") {
				return false, true
			}
		}
		return true, true
	case "justify-content":
		return webSupportsEnum(lower, "start", "end", "left", "right", "center", "flex-start", "flex-end", "space-between", "space-around", "space-evenly"), true
	case "align-items", "align-self":
		return webSupportsEnum(lower, "normal", "start", "end", "center", "stretch", "baseline", "flex-start", "flex-end"), true
	case "justify-items", "justify-self":
		return webSupportsEnum(lower, "normal", "start", "end", "center", "stretch", "left", "right", "flex-start", "flex-end", "self-start", "self-end"), true
	case "align-content":
		return webSupportsEnum(lower, "normal", "start", "end", "center", "stretch", "flex-start", "flex-end", "space-between", "space-around", "space-evenly"), true
	case "place-content":
		fields := webCSSSpaceFields(lower)
		if len(fields) == 0 || len(fields) > 2 {
			return false, true
		}
		for _, field := range fields {
			if !webSupportsEnum(field, "normal", "start", "end", "center", "stretch", "flex-start", "flex-end", "space-between", "space-around", "space-evenly") {
				return false, true
			}
		}
		return true, true
	case "place-self":
		fields := webCSSSpaceFields(lower)
		if len(fields) == 0 || len(fields) > 2 {
			return false, true
		}
		for _, field := range fields {
			if !webSupportsEnum(field, "auto", "normal", "start", "end", "center", "stretch", "flex-start", "flex-end") {
				return false, true
			}
		}
		return true, true
	case "place-items":
		fields := webCSSSpaceFields(lower)
		if len(fields) == 0 || len(fields) > 2 {
			return false, true
		}
		for _, field := range fields {
			if !webSupportsEnum(field, "normal", "start", "end", "center", "stretch", "baseline", "flex-start", "flex-end") {
				return false, true
			}
		}
		return true, true
	case "container-type":
		return webSupportsEnum(lower, "normal", "inline-size", "size"), true
	case "flex":
		return webSupportsFlex(lower), true
	case "width", "height":
		return webSupportsLengthToken(lower, true), true
	case "margin", "margin-block", "inset":
		return webSupportsBoxTokens(lower, true), true
	case "margin-top", "margin-right", "margin-bottom", "margin-left":
		return webSupportsLengthToken(lower, true), true
	case "padding", "padding-block", "padding-top", "padding-right", "padding-bottom", "padding-left":
		return webSupportsBoxTokens(lower, false), true
	case "gap", "row-gap", "column-gap":
		return webSupportsLengthToken(lower, false), true
	case "text-indent":
		return webSupportsLengthToken(lower, false), true
	case "font-size":
		if webSupportsContainerUnitLength(lower) {
			return true, true
		}
		_, ok := webFontSizePx(lower, 16)
		return ok, true
	case "line-height":
		if lower == "normal" {
			return true, true
		}
		if webSupportsContainerUnitLength(lower) {
			return true, true
		}
		_, ok := webLineHeightPx(lower, 16)
		return ok, true
	case "opacity":
		_, ok := webOpacityValue(lower)
		return ok, true
	case "order", "z-index":
		if name == "z-index" && lower == "auto" {
			return true, true
		}
		_, err := strconv.Atoi(lower)
		return err == nil, true
	case "flex-grow":
		n, err := strconv.ParseFloat(lower, 64)
		return err == nil && n >= 0, true
	case "flex-basis":
		return webSupportsEnum(lower, "auto", "content", "max-content", "min-content") || webSupportsLengthToken(lower, false), true
	case "aspect-ratio":
		if lower == "auto" {
			return true, true
		}
		_, ok := webMediaRatio(lower)
		return ok, true
	case "grid-template-columns", "grid-template-rows":
		return webSupportsGridTemplate(lower), true
	case "grid-column", "grid-column-start", "grid-column-end",
		"grid-row", "grid-row-start", "grid-row-end":
		return webSupportsGridPlacement(lower), true
	case "grid-template-areas":
		return webSupportsGridAreas(value), true
	case "grid-area":
		return webSupportsGridArea(lower), true
	case "grid-auto-flow":
		return webSupportsGridAutoFlow(lower), true
	case "grid-auto-columns", "grid-auto-rows":
		return webSupportsGridTemplate(lower), true
	case "border-width":
		return webSupportsBoxTokens(lower, false), true
	case "border-radius":
		v := strings.TrimSpace(lower)
		if v == "0" || v == "0px" || webSupportsZeroLength(v) {
			return true, true
		}
		if webSupportsContainerUnitLength(v) {
			return true, true
		}
		return webFirstLength(v, 16) != 0, true
	case "color":
		_, ok := parseWebColor(lower)
		return ok, true
	case "background-clip":
		return webSupportsEnum(lower, "border-box", "padding-box", "content-box", "text"), true
	case "background-color", "border-color":
		_, ok := webColorInValue(lower)
		return ok, true
	case "background", "background-image":
		return webSupportsBackground(lower), true
	case "border", "border-top", "border-right", "border-bottom", "border-left":
		return webSupportsBorder(lower), true
	}
	return false, false
}

func webSupportsEnum(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func webSupportsLengthToken(value string, allowAuto bool) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "0" || webSupportsZeroLength(value) {
		return true
	}
	if webSupportsContainerUnitLength(value) {
		return true
	}
	if allowAuto && value == "auto" {
		return true
	}
	if pct, ok := webPercentValue(value); ok {
		return pct >= 0
	}
	return webLength(value, 16) != 0
}

// webSupportsContainerUnitLength validates cq* values for @supports, where
// there is no element/container to resolve them against. A dummy 100px
// container is enough to prove the unit and any calc() around it are readable.
func webSupportsContainerUnitLength(value string) bool {
	if !strings.Contains(value, "cq") {
		return false
	}
	resolved := webSubstituteContainerUnits(value, 100, 100)
	if resolved == value {
		return false
	}
	if strings.Contains(value, "calc(") {
		resolved = webSubstituteCalcContainer(resolved, 16, 0, 0, 100, 100)
	}
	if _, ok := webContainerLength(resolved, 100, 100); ok {
		return true
	}
	return webLength(resolved, 16) != 0
}

func webSupportsZeroLength(value string) bool {
	for _, unit := range []string{"px", "em", "rem", "ex", "ch", "pt", "pc", "in", "cm", "mm", "q", "%"} {
		if strings.HasSuffix(value, unit) {
			n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(value, unit)), 64)
			return err == nil && n == 0
		}
	}
	return false
}

func webSupportsBoxTokens(value string, allowAuto bool) bool {
	fields := webCSSSpaceFields(value)
	if len(fields) < 1 || len(fields) > 4 {
		return false
	}
	for _, field := range fields {
		if !webSupportsLengthToken(field, allowAuto) {
			return false
		}
	}
	return true
}

func webSupportsFlex(value string) bool {
	switch value {
	case "none", "auto", "initial":
		return true
	}
	fields := webCSSSpaceFields(value)
	if len(fields) < 1 || len(fields) > 3 {
		return false
	}
	if _, err := strconv.ParseFloat(fields[0], 64); err != nil {
		if _, ok := webPercentValue(fields[0]); !ok || len(fields) != 1 {
			return false
		}
	}
	if len(fields) >= 2 {
		second := fields[1]
		if _, err := strconv.ParseFloat(second, 64); err != nil {
			if second != "auto" && second != "content" && !webSupportsLengthToken(second, true) {
				return false
			}
			return len(fields) == 2
		}
	}
	if len(fields) == 3 {
		basis := fields[2]
		if basis != "auto" && basis != "content" && !webSupportsLengthToken(basis, true) {
			return false
		}
	}
	return true
}

func webSupportsBackground(value string) bool {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	// Only the aliases mapped above have an implementation here. Another
	// engine's prefix must not make an otherwise-unknown image function look
	// supported merely because the value contains "gradient(".
	for _, prefix := range []string{"-webkit-", "-moz-", "-ms-", "-o-"} {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	if value == "none" {
		return true
	}
	if _, ok := webColorInValue(value); ok {
		return true
	}
	if _, ok := webParseGradient(value); ok {
		return true
	}
	// The WebKit radial aliases normalize to these unprefixed forms, which the
	// reader paints (webpage_gradient.go), and a radial one it cannot read
	// still counts by its averaged colour. Conic gradients are accepted
	// unprefixed only: Chromium supports no prefixed conic alias.
	if strings.Contains(lower, "radial-gradient(") {
		_, ok := webGradientColor(value)
		return ok
	}
	return false
}

func webSupportsBorder(value string) bool {
	if value == "none" || value == "hidden" || value == "0" {
		return true
	}
	fields := webCSSSpaceFields(value)
	if len(fields) == 0 {
		return false
	}
	recognized := false
	for _, field := range fields {
		field = strings.ToLower(field)
		if webSupportsLengthToken(field, false) {
			recognized = true
			continue
		}
		switch field {
		case "thin", "medium", "thick", "solid", "dashed", "dotted", "double", "groove", "ridge", "inset", "outset", "none", "hidden":
			recognized = true
			continue
		}
		if _, ok := parseWebColor(field); ok {
			recognized = true
			continue
		}
		return false
	}
	return recognized
}

func webSupportsGridTemplate(value string) bool {
	if value == "none" {
		return true
	}
	if strings.HasPrefix(value, "repeat(") {
		open := len("repeat")
		end := webCSSParenEnd(value, open)
		if end <= open {
			return false
		}
		parts := webSplitTopLevel(value[open+1:end], ',')
		if len(parts) < 2 {
			return false
		}
		count := strings.TrimSpace(parts[0])
		if count != "auto-fit" && count != "auto-fill" {
			n, err := strconv.Atoi(count)
			if err != nil || n < 1 || n > 6 {
				return false
			}
		}
		return webSupportsGridTrack(strings.TrimSpace(parts[1]))
	}
	fields := webCSSSpaceFields(value)
	if len(fields) < 1 || len(fields) > 6 {
		return false
	}
	for _, field := range fields {
		if !webSupportsGridTrack(field) {
			return false
		}
	}
	return true
}

func webSupportsGridTrack(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "auto" || value == "min-content" || value == "max-content" {
		return true
	}
	if strings.HasSuffix(value, "fr") {
		n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(value, "fr")), 64)
		return err == nil && n >= 0
	}
	if strings.HasPrefix(value, "minmax(") || strings.HasPrefix(value, "fit-content(") {
		open := strings.IndexByte(value, '(')
		end := webCSSParenEnd(value, open)
		if end <= open {
			return false
		}
		parts := webSplitTopLevel(value[open+1:end], ',')
		if strings.HasPrefix(value, "fit-content(") {
			return len(parts) == 1 && webSupportsLengthToken(parts[0], false)
		}
		if len(parts) != 2 {
			return false
		}
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if !webSupportsGridTrack(part) && !webSupportsLengthToken(part, false) {
				return false
			}
		}
		return true
	}
	return webSupportsLengthToken(value, false)
}

func webSupportsSelector(selector string) bool {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return false
	}
	sel, _ := webCompileSelector(selector)
	return sel != nil && !sel.never
}

// webSkipCSSStatementRules drops the statement at-rules — @charset, @import,
// @namespace and the `@layer a, b;` order statement, which end at a semicolon
// instead of a block — from the text in front of a block. They used to be read
// as part of the next selector, so `@charset "utf-8"; body {…}` was taken for
// an at-rule block and the first real rule of nearly every stylesheet was
// thrown away (and `@layer base; body {…}` lost the body rule the same way).
// An `@layer name` without a semicolon is the next block's own header.
func webSkipCSSStatementRules(text string) string {
	for strings.HasPrefix(text, "@") {
		lower := asciiLower(text)
		if !strings.HasPrefix(lower, "@charset") && !strings.HasPrefix(lower, "@import") &&
			!strings.HasPrefix(lower, "@namespace") && !strings.HasPrefix(lower, "@layer") {
			return text
		}
		end := -1
		var quote byte
		depth := 0
		for i := 0; i < len(text) && end < 0; i++ {
			c := text[i]
			switch {
			case quote != 0:
				if c == '\\' {
					i++
				} else if c == quote {
					quote = 0
				}
			case c == '"' || c == '\'':
				quote = c
			case c == '(':
				depth++
			case c == ')':
				if depth > 0 {
					depth--
				}
			case c == ';' && depth == 0:
				end = i
			}
		}
		if end < 0 {
			return text
		}
		text = strings.TrimSpace(text[end+1:])
	}
	return text
}

func stripWebCSSComments(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	for i := 0; i < len(src); {
		if src[i] == '/' && i+1 < len(src) && src[i+1] == '*' {
			if end := strings.Index(src[i+2:], "*/"); end >= 0 {
				i += 2 + end + 2
			} else {
				break
			}
			continue
		}
		out.WriteByte(src[i])
		i++
	}
	return out.String()
}

// webCSSBlockEnd finds the closing brace of a block, skipping braces inside
// strings and comments.
func webCSSBlockEnd(src string, open int) int {
	depth := 0
	var quote byte
	for i := open; i < len(src); i++ {
		c := src[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// parseWebDeclsSplit reads one declaration block. Declarations marked
// !important come back separately so the cascade can apply them last.
func parseWebDeclsSplit(src string) (decls, important map[string]string) {
	decls = make(map[string]string, 8)
	var quote byte
	depth := 0
	start := 0
	src += ";"
	for i := 0; i < len(src); i++ {
		c := src[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ';':
			if depth != 0 {
				continue
			}
			piece := src[start:i]
			start = i + 1
			colon := strings.IndexByte(piece, ':')
			if colon <= 0 {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(piece[:colon]))
			value := strings.TrimSpace(piece[colon+1:])
			if name == "" || value == "" {
				continue
			}
			if stripped, marked := webDropImportant(value); marked {
				if stripped == "" {
					continue
				}
				if important == nil {
					important = make(map[string]string, 2)
				}
				important[name] = stripped
				continue
			}
			decls[name] = value
		}
	}
	return decls, important
}

// webDropImportant removes a trailing !important, in any letter case, and
// reports whether it was there. The flag has to be stripped before the value
// reaches a length or colour parser.
func webDropImportant(value string) (string, bool) {
	lower := asciiLower(value)
	idx := strings.LastIndex(lower, "!important")
	if idx < 0 {
		return value, false
	}
	if strings.TrimSpace(value[idx+len("!important"):]) != "" {
		return value, false
	}
	return strings.TrimSpace(value[:idx]), true
}

// --- Custom properties, var() and calc() ----------------------------------

// webVarScopeFor builds the scope one element sees. Inherited properties are
// already expanded, so only the element's own ones are resolved here.
func webVarScopeFor(parent *webVarScope, decls map[string]string) *webVarScope {
	var own map[string]string
	for name, value := range decls {
		if !strings.HasPrefix(name, "--") {
			continue
		}
		if own == nil {
			own = make(map[string]string, 4)
		}
		own[name] = value
	}
	if len(own) == 0 {
		return parent
	}
	props := make(map[string]string, len(own)+4)
	if parent != nil {
		for name, value := range parent.props {
			props[name] = value
		}
	}
	visiting := make(map[string]bool, len(own))
	for name := range own {
		if value, ok := webResolveCustomProperty(name, own, props, visiting, 0); ok {
			props[name] = value
		}
	}
	return &webVarScope{props: props}
}

// webResolveCustomProperty expands one `--name` to text with no var() left in
// it. A cycle (`--a: var(--b); --b: var(--a)`) is invalid at computed-value
// time, so it reports false and the property leaves the scope, which is what
// makes a later `var(--a, 12px)` fall back to 12px.
func webResolveCustomProperty(name string, own, props map[string]string, visiting map[string]bool, depth int) (string, bool) {
	if depth > 8 || visiting[name] {
		return "", false
	}
	raw, ok := own[name]
	if !ok {
		raw, ok = props[name]
	}
	if !ok {
		return "", false
	}
	if !strings.Contains(strings.ToLower(raw), "var(") {
		return raw, true
	}
	visiting[name] = true
	defer delete(visiting, name)
	value, changed := webReplaceVarRefs(raw, own, props, visiting, depth)
	if !changed {
		return "", false
	}
	return value, true
}

// webSubstituteVars replaces every var() in an ordinary declaration value.
// A reference that resolves to nothing and has no fallback is invalid at
// computed-value time: the whole declaration is then left as authored and the
// property parsers ignore it, which is how a browser drops it too.
func webSubstituteVars(value string, scope *webVarScope) string {
	if !strings.Contains(strings.ToLower(value), "var(") {
		return value
	}
	var props map[string]string
	if scope != nil {
		props = scope.props
	}
	current := value
	for pass := 0; pass < 8; pass++ {
		if !strings.Contains(strings.ToLower(current), "var(") {
			return current
		}
		next, changed := webReplaceVarRefs(current, nil, props, map[string]bool{}, 0)
		if !changed {
			return value
		}
		current = next
	}
	return value
}

// webReplaceVarRefs substitutes the var() calls in one value. It reports false
// when a reference cannot be resolved at all, so the caller can drop the
// declaration instead of leaving a half-substituted string behind.
func webReplaceVarRefs(value string, own, props map[string]string, visiting map[string]bool, depth int) (string, bool) {
	var out strings.Builder
	out.Grow(len(value))
	lower := asciiLower(value)
	changed := false
	for i := 0; i < len(value); {
		idx := strings.Index(lower[i:], "var(")
		if idx < 0 {
			out.WriteString(value[i:])
			break
		}
		idx += i
		out.WriteString(value[i:idx])
		end := webCSSParenEnd(value, idx+3)
		if end < 0 {
			return "", false
		}
		name, fallback, hasFallback := webVarArgs(value[idx+4 : end])
		replacement, ok := webResolveCustomProperty(name, own, props, visiting, depth+1)
		if !ok {
			if !hasFallback {
				return "", false
			}
			// A fallback may itself hold var() calls, but plain text is the
			// common case and has nothing to expand.
			if nested, nestedChanged := webReplaceVarRefs(fallback, own, props, visiting, depth+1); nestedChanged {
				replacement = nested
			} else {
				replacement = fallback
			}
		}
		out.WriteString(replacement)
		changed = true
		i = end + 1
		// Each level may reference the next one many times, so expansion is
		// exponential in the eight levels allowed ("--a: var(--b) var(--b)",
		// and so on): a few lines of CSS asked for megabytes per element.
		// Past the cap the value is invalid, like a cycle.
		if out.Len() > webVarMaxValueBytes {
			return "", false
		}
	}
	if !changed {
		return value, false
	}
	return out.String(), true
}

// webVarMaxValueBytes caps one expanded custom-property value.
const webVarMaxValueBytes = 8 << 10

// webVarArgs splits the inside of var(...) into the property name and its
// fallback. Only a comma outside brackets separates the two.
func webVarArgs(inner string) (name, fallback string, hasFallback bool) {
	depth := 0
	var quote byte
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				return strings.TrimSpace(inner[:i]), strings.TrimSpace(inner[i+1:]), true
			}
		}
	}
	return strings.TrimSpace(inner), "", false
}

// webCSSParenEnd finds the bracket matching the one at open, skipping brackets
// inside quoted strings.
func webCSSParenEnd(src string, open int) int {
	depth := 0
	var quote byte
	for i := open; i < len(src); i++ {
		c := src[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// webResolveValue prepares one declaration for the property parsers: var()
// substituted first, then any calc() that can be reduced to a px length.
func webResolveValue(value string, scope *webVarScope, em float64) string {
	value = webWrapMathFunctions(webSubstituteVars(value, scope))
	if strings.Contains(strings.ToLower(value), "calc(") {
		value = webSubstituteCalc(value, em)
	}
	return value
}

// webMathFunctionNames are CSS Values 4's math functions calc() evaluates
// (mathFactor) beside min(), max() and clamp().
var webMathFunctionNames = [...]string{"round(", "mod(", "rem(", "abs(", "sign(", "pow(", "sqrt(", "hypot(", "exp(", "log("}

// webHasMathFunction reports whether a lower-case value calls one of those.
func webHasMathFunction(lower string) bool {
	for _, name := range webMathFunctionNames {
		if strings.Contains(lower, name) {
			return true
		}
	}
	return false
}

// webWrapMathFunctions puts each of those written bare in a value (`width:
// round(up, 33.3%, 1px)`) inside calc(), the one place math is evaluated, so
// it resolves like a calc() would. A value with none comes back as it is.
func webWrapMathFunctions(value string) string {
	if strings.IndexByte(value, '(') < 0 {
		return value
	}
	lower := asciiLower(value)
	found := false
	for _, name := range webMathFunctionNames {
		if strings.Contains(lower, name) {
			found = true
			break
		}
	}
	if !found {
		return value
	}
	var out strings.Builder
	last := 0
	for i := 0; i < len(lower); i++ {
		if i > 0 && (isWebIdentByte(lower[i-1]) || lower[i-1] == '-') {
			continue
		}
		for _, name := range webMathFunctionNames {
			if !strings.HasPrefix(lower[i:], name) {
				continue
			}
			end := webCSSParenEnd(value, i+len(name)-1)
			if end < 0 {
				break
			}
			out.WriteString(value[last:i])
			out.WriteString("calc(")
			out.WriteString(value[i : end+1])
			out.WriteString(")")
			last = end + 1
			i = end
			break
		}
	}
	out.WriteString(value[last:])
	return out.String()
}

// webSubstituteCalc replaces each calc() it can evaluate with a px length. A
// calc() that mixes in a unit the reader cannot resolve yet (`100% - 20px`)
// is left as authored.
func webSubstituteCalc(value string, em float64) string {
	return webSubstituteCalcContainer(value, em, 0, 0, 0, 0)
}

// webCalcPercentsIn turns each calc(), min(), max() or clamp() in value that
// mixes a percentage with lengths — `calc(100% - 300px)`, `min(100%, 40rem)`,
// `calc(33.333% - 20px)` in a flex shorthand — into the percentage it comes
// to in a containing block base CSS px wide: the parent's modelled width
// (webContainerDimensions). The layout still scales it with the column it
// really gets, and where that column is the modelled one it comes out exact.
// Such a value used to be dropped, leaving a sidebar layout's main column the
// full width. Anything it cannot evaluate is left as written.
func webCalcPercentsIn(value string, em, base float64) string {
	if base <= 0 || !strings.Contains(value, "%") || strings.IndexByte(value, '(') < 0 {
		return value
	}
	lower := asciiLower(value)
	var out strings.Builder
	last := 0
	for i := 0; i < len(lower); i++ {
		if i > 0 && (isWebIdentByte(lower[i-1]) || lower[i-1] == '-') {
			continue
		}
		name := ""
		for _, fn := range [...]string{"calc(", "min(", "max(", "clamp("} {
			if strings.HasPrefix(lower[i:], fn) {
				name = fn
				break
			}
		}
		if name == "" {
			continue
		}
		end := webCSSParenEnd(value, i+len(name)-1)
		if end < 0 {
			break
		}
		// round() and the other stepped and exponential functions over a
		// percentage are left as written: they size a carousel's cards to a
		// fraction of a row that scrolls sideways, which the reader flows
		// into rows of the cards' own readable width instead.
		if expr := value[i : end+1]; strings.Contains(expr, "%") && !webHasMathFunction(asciiLower(expr)) {
			if px, ok := webEvalCalcContainerPercent(expr, em, 0, 0, 0, 0, base); ok && !math.IsNaN(px) && !math.IsInf(px, 0) {
				out.WriteString(value[last:i])
				out.WriteString(strconv.FormatFloat(px/base*100, 'f', 4, 64))
				out.WriteString("%")
				last = end + 1
			}
		}
		i = end
	}
	if last == 0 {
		return value
	}
	out.WriteString(value[last:])
	return out.String()
}

// webSubstituteCalcContainer is webSubstituteCalc with query-container
// dimensions available for cq* operands. Ordinary CSS values reach this after
// their bare container units have already been substituted.
func webSubstituteCalcContainer(value string, em, vw, vh, cw, ch float64) string {
	if em <= 0 {
		em = 16
	}
	if vw <= 0 || vh <= 0 {
		vw, vh = webMediaViewport()
	}
	lower := asciiLower(value)
	var out strings.Builder
	out.Grow(len(value))
	for i := 0; i < len(value); {
		idx := strings.Index(lower[i:], "calc(")
		if idx < 0 {
			out.WriteString(value[i:])
			break
		}
		idx += i
		end := webCSSParenEnd(value, idx+4)
		if end < 0 {
			out.WriteString(value[i:])
			break
		}
		out.WriteString(value[i:idx])
		if px, ok := webEvalCalcContainer(value[idx+5:end], em, vw, vh, cw, ch); ok {
			out.WriteString(strconv.FormatFloat(px, 'f', -1, 64))
			out.WriteString("px")
		} else {
			out.WriteString(value[idx : end+1])
		}
		i = end + 1
	}
	return out.String()
}

// webOrdinaryContainerSize returns the nearest ancestor query container's
// modeled inline and block size. Without a container, cq* units follow the
// same viewport fallback the image sizes path uses.
func (p *webParser) webOrdinaryContainerSize() (w, h float64) {
	w, h = webMediaViewport()
	idx := -1
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].style.ContainerType != 0 {
			idx = i
			break
		}
	}
	if idx < 0 {
		return w, h
	}
	return p.webContainerDimensions(idx)
}

// webResolveStyleValue prepares one declaration for the property parsers
// while the element's containing query container is known: var() first, then
// cq* units and calc(). The ordinary webResolveValue path is kept for callers
// such as @supports that have no element stack.
func (p *webParser) webResolveStyleValueIn(value string, scope *webVarScope, em, cw, ch float64) string {
	value = webSubstituteVars(value, scope)
	// rem follows the root's font size (`html { font-size: 62.5% }`); the
	// lengths below read a bare rem as 16px.
	if p.rootFontPx > 0 && p.rootFontPx != 16 {
		value = webSubstituteRootEm(value, p.rootFontPx)
	}
	value = webWrapMathFunctions(webSubstituteContainerUnits(value, cw, ch))
	if strings.Contains(strings.ToLower(value), "calc(") {
		value = webSubstituteCalcContainer(value, em, 0, 0, cw, ch)
	}
	return value
}

// resolve replays the declarations captured at parse time against a measured
// container. It starts from the pre-declaration style rather than the style
// already carrying modelled cq* values, so shorthand contributions such as
// padding/margin are applied once, not added twice. The element's non-CSS
// facts (link target, hidden flag, form control) are carried over from current.
func (ctx *webCQContext) resolve(current webStyle, cw, ch float64) webStyle {
	st := ctx.base
	st.CQ = ctx
	st.Vars = ctx.vars
	em := st.FontPx
	decls := make(map[string]string, len(ctx.decls))
	for name, value := range ctx.decls {
		if strings.HasPrefix(name, "--") {
			continue
		}
		decls[name] = webSubstituteVars(value, ctx.vars)
		if ctx.rootPx > 0 && ctx.rootPx != 16 {
			decls[name] = webSubstituteRootEm(decls[name], ctx.rootPx)
		}
	}
	if raw, ok := decls["font-size"]; ok {
		if px, valid := webFontSizePx(declsValueAt(decls, "font-size", raw, em, cw, ch), em); valid {
			em = px
		}
	}
	for name, value := range decls {
		decls[name] = webSubstituteContainerUnits(value, cw, ch)
		if strings.Contains(strings.ToLower(value), "calc(") {
			decls[name] = webSubstituteCalcContainer(decls[name], em, 0, 0, cw, ch)
		}
	}
	applyWebDecls(&st, decls)
	webResolvePercentageHeights(&st, decls, ctx.parentHeight)
	st.Href, st.Link, st.Hidden, st.Control = current.Href, current.Link, current.Hidden, current.Control
	// Facts computeStyle settled beyond the declarations (the inherited
	// baseline shift, `inherit` on weight, slant and alignment) do not depend
	// on the container's size.
	st.BaselineShift = current.BaselineShift
	st.Bold, st.Italic, st.Align, st.CenterBlocks = current.Bold, current.Italic, current.Align, current.CenterBlocks
	return st
}

// declsValueAt resolves var() in one value before font-size is parsed. It is a
// small helper so resolve does not duplicate the substitution order.
func declsValueAt(decls map[string]string, name, value string, em, cw, ch float64) string {
	value = webSubstituteContainerUnits(value, cw, ch)
	if strings.Contains(strings.ToLower(value), "calc(") {
		value = webSubstituteCalcContainer(value, em, 0, 0, cw, ch)
	}
	return value
}

// webSubstituteContainerUnits replaces numeric cq* tokens with px. Quoted
// strings, url() payloads and unresolved var() calls are copied untouched so
// a file name or custom-property syntax cannot be mistaken for a length.
func webSubstituteContainerUnits(value string, cw, ch float64) string {
	if cw <= 0 || ch <= 0 {
		cw, ch = webMediaViewport()
	}
	lower := strings.ToLower(value)
	var out strings.Builder
	out.Grow(len(value))
	for i := 0; i < len(value); {
		c := value[i]
		if c == '"' || c == '\'' {
			end := i + 1
			for end < len(value) {
				if value[end] == '\\' {
					end += 2
					continue
				}
				if value[end] == c {
					end++
					break
				}
				end++
			}
			out.WriteString(value[i:min(end, len(value))])
			i = end
			continue
		}
		if strings.HasPrefix(lower[i:], "url(") || strings.HasPrefix(lower[i:], "var(") {
			open := i + 3
			end := webCSSParenEnd(value, open)
			if end < 0 {
				out.WriteByte(c)
				i++
				continue
			}
			out.WriteString(value[i : end+1])
			i = end + 1
			continue
		}
		start := i
		if start > 0 && webContainerUnitIdentByte(value[start-1]) {
			if c != '+' && c != '-' {
				out.WriteByte(c)
				i++
				continue
			}
		}
		if c == '+' || c == '-' {
			if i+1 >= len(value) || !(value[i+1] >= '0' && value[i+1] <= '9' || value[i+1] == '.') {
				out.WriteByte(c)
				i++
				continue
			}
			if start >= 2 && webContainerUnitIdentByte(value[start-2]) {
				out.WriteByte(c)
				i++
				continue
			}
			i++
		}
		digits := i
		for i < len(value) && (value[i] >= '0' && value[i] <= '9' || value[i] == '.') {
			i++
		}
		if i == digits {
			out.WriteByte(c)
			i = start + 1
			continue
		}
		unitStart := i
		if unitStart < len(value) {
			for i < len(value) && ((value[i] >= 'a' && value[i] <= 'z') || (value[i] >= 'A' && value[i] <= 'Z')) {
				i++
			}
		}
		unit := strings.ToLower(value[unitStart:i])
		factor, isContainerUnit := webContainerUnitFactor(unit, cw, ch)
		if !isContainerUnit {
			out.WriteString(value[start:i])
			continue
		}
		n, err := strconv.ParseFloat(value[start:unitStart], 64)
		if err != nil {
			out.WriteString(value[start:i])
			continue
		}
		out.WriteString(strconv.FormatFloat(n*factor, 'f', -1, 64))
		out.WriteString("px")
	}
	return out.String()
}

func webContainerUnitIdentByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
}

func webContainerUnitFactor(unit string, cw, ch float64) (float64, bool) {
	switch unit {
	case "cqw", "cqi":
		return cw / 100, true
	case "cqh", "cqb":
		return ch / 100, true
	case "cqmin":
		return min(cw, ch) / 100, true
	case "cqmax":
		return max(cw, ch) / 100, true
	}
	return 0, false
}

// webCalcValue is one operand of a calc() expression. A unitless number is
// only legal against another unitless number or as a multiplier.
type webCalcValue struct {
	px       float64
	unitless bool
}

// webCalcParser evaluates the arithmetic inside calc(). It handles + - * /,
// nested brackets and px/em/rem operands; anything else fails and the caller
// keeps the original text.
type webCalcParser struct {
	src    string
	pos    int
	em     float64
	vw, vh float64
	cw, ch float64
	// percentBase is only set for a length whose percentage reference is
	// definite, such as height inside an explicitly sized containing block.
	percentBase float64
}

func webEvalCalc(expr string, em float64) (float64, bool) {
	return webEvalCalcViewport(expr, em, 0, 0)
}

// webEvalCalcViewport evaluates calc() with the page viewport available for
// vw/vh/vmin/vmax. Sizes attributes use this form because their lengths are
// resolved against the same viewport as their media conditions.
func webEvalCalcViewport(expr string, em, vw, vh float64) (float64, bool) {
	return webEvalCalcContainer(expr, em, vw, vh, vw, vh)
}

// webEvalCalcContainer is webEvalCalcViewport with the nearest query container
// dimensions available for cqw/cqh/cqi/cqb/cqmin/cqmax.
func webEvalCalcContainer(expr string, em, vw, vh, cw, ch float64) (float64, bool) {
	return webEvalCalcContainerPercent(expr, em, vw, vh, cw, ch, 0)
}

func webEvalCalcContainerPercent(expr string, em, vw, vh, cw, ch, percentBase float64) (float64, bool) {
	if vw <= 0 || vh <= 0 {
		vw, vh = webMediaViewport()
	}
	p := &webCalcParser{src: expr, em: em, vw: vw, vh: vh, cw: cw, ch: ch, percentBase: percentBase}
	value, ok := p.sum()
	if !ok {
		return 0, false
	}
	p.skipSpace()
	if p.pos != len(p.src) {
		return 0, false
	}
	return value.px, true
}

func (p *webCalcParser) skipSpace() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t' || p.src[p.pos] == '\n') {
		p.pos++
	}
}

func (p *webCalcParser) sum() (webCalcValue, bool) {
	left, ok := p.product()
	if !ok {
		return webCalcValue{}, false
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.src) || (p.src[p.pos] != '+' && p.src[p.pos] != '-') {
			return left, true
		}
		op := p.src[p.pos]
		p.pos++
		right, ok := p.product()
		if !ok || left.unitless != right.unitless {
			return webCalcValue{}, false
		}
		if op == '+' {
			left.px += right.px
		} else {
			left.px -= right.px
		}
	}
}

func (p *webCalcParser) product() (webCalcValue, bool) {
	left, ok := p.factor()
	if !ok {
		return webCalcValue{}, false
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.src) || (p.src[p.pos] != '*' && p.src[p.pos] != '/') {
			return left, true
		}
		op := p.src[p.pos]
		p.pos++
		right, ok := p.factor()
		if !ok {
			return webCalcValue{}, false
		}
		if op == '*' {
			if !left.unitless && !right.unitless {
				return webCalcValue{}, false
			}
			left.px *= right.px
			left.unitless = left.unitless && right.unitless
			continue
		}
		if !right.unitless || right.px == 0 {
			return webCalcValue{}, false
		}
		left.px /= right.px
	}
}

func (p *webCalcParser) factor() (webCalcValue, bool) {
	p.skipSpace()
	if p.pos >= len(p.src) {
		return webCalcValue{}, false
	}
	if value, ok, handled := p.functionFactor(); handled {
		return value, ok
	}
	switch p.src[p.pos] {
	case '(':
		p.pos++
		value, ok := p.sum()
		if !ok {
			return webCalcValue{}, false
		}
		p.skipSpace()
		if p.pos >= len(p.src) || p.src[p.pos] != ')' {
			return webCalcValue{}, false
		}
		p.pos++
		return value, true
	case '-', '+':
		sign := 1.0
		if p.src[p.pos] == '-' {
			sign = -1
		}
		p.pos++
		value, ok := p.factor()
		if !ok {
			return webCalcValue{}, false
		}
		value.px *= sign
		return value, true
	}
	start := p.pos
	for p.pos < len(p.src) && (p.src[p.pos] >= '0' && p.src[p.pos] <= '9' || p.src[p.pos] == '.') {
		p.pos++
	}
	if p.pos == start {
		return webCalcValue{}, false
	}
	number, err := strconv.ParseFloat(p.src[start:p.pos], 64)
	if err != nil {
		return webCalcValue{}, false
	}
	unitStart := p.pos
	for p.pos < len(p.src) && (p.src[p.pos] >= 'a' && p.src[p.pos] <= 'z' || p.src[p.pos] == '%') {
		p.pos++
	}
	switch strings.ToLower(p.src[unitStart:p.pos]) {
	case "":
		return webCalcValue{px: number, unitless: true}, true
	case "px":
		return webCalcValue{px: number}, true
	case "em":
		return webCalcValue{px: number * p.em}, true
	case "rem":
		return webCalcValue{px: number * 16}, true
	case "%":
		if p.percentBase > 0 {
			return webCalcValue{px: number * p.percentBase / 100}, true
		}
	case "vw", "vi", "svw", "svi", "dvw", "dvi", "lvw", "lvi":
		if p.vw > 0 {
			return webCalcValue{px: number * p.vw / 100}, true
		}
	case "vh", "vb", "svh", "svb", "dvh", "dvb", "lvh", "lvb":
		if p.vh > 0 {
			return webCalcValue{px: number * p.vh / 100}, true
		}
	case "vmin", "svmin", "dvmin", "lvmin":
		if p.vw > 0 && p.vh > 0 {
			return webCalcValue{px: number * min(p.vw, p.vh) / 100}, true
		}
	case "vmax", "svmax", "dvmax", "lvmax":
		if p.vw > 0 && p.vh > 0 {
			return webCalcValue{px: number * max(p.vw, p.vh) / 100}, true
		}
	case "cqw", "cqi":
		if p.cw > 0 {
			return webCalcValue{px: number * p.cw / 100}, true
		}
	case "cqh", "cqb":
		if p.ch > 0 {
			return webCalcValue{px: number * p.ch / 100}, true
		}
	case "cqmin":
		if p.cw > 0 && p.ch > 0 {
			return webCalcValue{px: number * min(p.cw, p.ch) / 100}, true
		}
	case "cqmax":
		if p.cw > 0 && p.ch > 0 {
			return webCalcValue{px: number * max(p.cw, p.ch) / 100}, true
		}
	}
	return webCalcValue{}, false
}

// functionFactor parses calc(), min(), max() and clamp() where a value is
// expected. CSS math functions nest freely, so each argument is handed back to
// the same evaluator rather than being flattened into a string first.
func (p *webCalcParser) functionFactor() (value webCalcValue, ok, handled bool) {
	lower := strings.ToLower(p.src[p.pos:])
	name := ""
	for _, candidate := range []string{"calc", "clamp", "min", "max"} {
		if strings.HasPrefix(lower, candidate+"(") {
			name = candidate
			break
		}
	}
	if name == "" {
		// The stepped-value and exponential functions of CSS Values 4.
		return p.mathFactor(lower)
	}
	open := p.pos + len(name)
	close := webCSSParenEnd(p.src, open)
	if close < 0 {
		return webCalcValue{}, false, true
	}
	inner := p.src[open+1 : close]
	eval := func(expr string) (float64, bool) {
		return webEvalCalcContainerPercent(expr, p.em, p.vw, p.vh, p.cw, p.ch, p.percentBase)
	}
	if name == "calc" {
		px, valid := eval(inner)
		if !valid {
			return webCalcValue{}, false, true
		}
		p.pos = close + 1
		return webCalcValue{px: px}, true, true
	}
	args := webSplitSelectorList(inner)
	values := make([]float64, 0, len(args))
	for _, arg := range args {
		px, valid := eval(strings.TrimSpace(arg))
		if !valid {
			return webCalcValue{}, false, true
		}
		values = append(values, px)
	}
	var px float64
	switch name {
	case "min":
		if len(values) == 0 {
			return webCalcValue{}, false, true
		}
		px = values[0]
		for _, value := range values[1:] {
			px = min(px, value)
		}
	case "max":
		if len(values) == 0 {
			return webCalcValue{}, false, true
		}
		px = values[0]
		for _, value := range values[1:] {
			px = max(px, value)
		}
	case "clamp":
		if len(values) != 3 {
			return webCalcValue{}, false, true
		}
		px = max(values[0], min(values[1], values[2]))
	}
	p.pos = close + 1
	return webCalcValue{px: px}, true, true
}

// mathFactor parses CSS Values 4's other math functions where a value is
// expected — round(), mod(), rem(), abs(), sign(), pow(), sqrt(), hypot(),
// exp(), log() — and the constants pi and e. lower is the source from here,
// lower-cased. handled is false when none starts here.
func (p *webCalcParser) mathFactor(lower string) (value webCalcValue, ok, handled bool) {
	for _, c := range []struct {
		name string
		v    float64
	}{{"pi", math.Pi}, {"e", math.E}} {
		if strings.HasPrefix(lower, c.name) && (len(lower) == len(c.name) || !isWebIdentByte(lower[len(c.name)])) {
			p.pos += len(c.name)
			return webCalcValue{px: c.v, unitless: true}, true, true
		}
	}
	name := ""
	for _, candidate := range []string{"round", "mod", "rem", "abs", "sign", "pow", "sqrt", "hypot", "exp", "log"} {
		if strings.HasPrefix(lower, candidate+"(") {
			name = candidate
			break
		}
	}
	if name == "" {
		return webCalcValue{}, false, false
	}
	open := p.pos + len(name)
	close := webCSSParenEnd(p.src, open)
	if close < 0 {
		return webCalcValue{}, false, true
	}
	args := webSplitSelectorList(p.src[open+1 : close])
	strategy := "nearest"
	if name == "round" && len(args) > 0 {
		switch first := strings.ToLower(strings.TrimSpace(args[0])); first {
		case "nearest", "up", "down", "to-zero":
			strategy, args = first, args[1:]
		}
	}
	values := make([]webCalcValue, 0, len(args))
	for _, arg := range args {
		sub := &webCalcParser{src: strings.TrimSpace(arg), em: p.em, vw: p.vw, vh: p.vh, cw: p.cw, ch: p.ch, percentBase: p.percentBase}
		v, valid := sub.sum()
		sub.skipSpace()
		if !valid || sub.pos != len(sub.src) {
			return webCalcValue{}, false, true
		}
		values = append(values, v)
	}
	need := map[string][2]int{"round": {1, 2}, "mod": {2, 2}, "rem": {2, 2}, "abs": {1, 1}, "sign": {1, 1},
		"pow": {2, 2}, "sqrt": {1, 1}, "hypot": {1, 16}, "exp": {1, 1}, "log": {1, 2}}[name]
	if len(values) < need[0] || len(values) > need[1] {
		return webCalcValue{}, false, true
	}
	a := values[0]
	out := webCalcValue{unitless: a.unitless}
	unitlessArgs := true
	for _, v := range values {
		unitlessArgs = unitlessArgs && v.unitless
	}
	switch name {
	case "round", "mod", "rem":
		step := 1.0
		if len(values) > 1 {
			step = values[1].px
		} else if !a.unitless {
			return webCalcValue{}, false, true
		}
		if step == 0 {
			return webCalcValue{}, false, true
		}
		q := a.px / step
		switch {
		case name == "mod":
			q = math.Floor(q)
		case name == "rem", strategy == "to-zero":
			q = math.Trunc(q)
		case strategy == "up":
			q = math.Ceil(q)
		case strategy == "down":
			q = math.Floor(q)
		default:
			q = math.Floor(q + 0.5)
		}
		out.px = q * step
		if name != "round" {
			out.px = a.px - out.px
		}
	case "abs":
		out.px = math.Abs(a.px)
	case "sign":
		out = webCalcValue{unitless: true}
		switch {
		case a.px > 0:
			out.px = 1
		case a.px < 0:
			out.px = -1
		}
	case "hypot":
		sum := 0.0
		for _, v := range values {
			sum += v.px * v.px
		}
		out.px = math.Sqrt(sum)
	default:
		// pow, sqrt, exp and log take and give numbers.
		if !unitlessArgs {
			return webCalcValue{}, false, true
		}
		out.unitless = true
		switch name {
		case "pow":
			out.px = math.Pow(a.px, values[1].px)
		case "sqrt":
			out.px = math.Sqrt(a.px)
		case "exp":
			out.px = math.Exp(a.px)
		case "log":
			out.px = math.Log(a.px)
			if len(values) == 2 {
				out.px /= math.Log(values[1].px)
			}
		}
	}
	if math.IsNaN(out.px) || math.IsInf(out.px, 0) {
		return webCalcValue{}, false, true
	}
	p.pos = close + 1
	return out, true, true
}

// --- CSS selectors --------------------------------------------------------
//
// Selectors are compiled once, when a sheet is parsed, and matched against the
// chain of open elements: outermost first, the element being styled last. The
// streaming parser keeps no DOM, but rules that need sibling structure
// (:first-child, :last-child, :nth-child, :nth-last-child, :nth-of-type and
// :has()) get a small selector tree
// built ahead of the walk. Selectors that still depend on content the parser
// has not reached (:empty) or on the previous sibling (+, ~) answer "maybe".
//
// The matcher used to assume that every attribute selector and every pseudo
// held. That was harmless for a colour and fatal for display:none: a reset
// sheet's `[hidden] { display: none }`, a Vue or Alpine app's `[v-cloak]` and
// `[x-cloak]`, a web-component site's `:not(:defined)` or an icon font's
// `.icon::before { display: none }` matched every element, and the page drew
// blank. Attribute selectors and the pseudo-classes a static page can answer
// are now evaluated, a selector aimed at a pseudo-element never styles the
// element itself, and a rule that would hide what it matches only applies on a
// definite match.

// webSelTri is a selector test's answer. webSelMaybe is one the streaming
// parser cannot decide from a start tag.
type webSelTri uint8

const (
	webSelNo webSelTri = iota
	webSelYes
	webSelMaybe
)

func webSelBool(ok bool) webSelTri {
	if ok {
		return webSelYes
	}
	return webSelNo
}

// webSelNot negates an answer; an undecidable one stays undecidable.
func webSelNot(r webSelTri) webSelTri {
	switch r {
	case webSelYes:
		return webSelNo
	case webSelNo:
		return webSelYes
	}
	return webSelMaybe
}

// webSelector is one selector of a rule. Matching used to split the selector
// text again for every element of the page, which on a large sheet was most of
// what styling a page cost on the handheld.
type webSelector struct {
	// compounds run left to right; compounds[i].comb joins compounds[i] to
	// compounds[i-1].
	compounds []webSelCompound
	// pseudo names the generated box this selector targets: before, after or
	// marker. Zero means an ordinary element selector. A rule for a generated
	// box never styles the originating element itself.
	pseudo uint8
	// never marks a selector that cannot match an element: one aimed at a
	// pseudo-element the reader does not generate, or one that does not parse,
	// which a browser drops too.
	never bool
}

// Generated boxes the reader knows how to lay out. Other pseudo-elements
// (::first-line, ::first-letter, ::selection, form-control parts) still mark
// their selector never, so their declarations cannot leak onto the element.
const (
	webPseudoElemNone uint8 = iota
	webPseudoElemBefore
	webPseudoElemAfter
	webPseudoElemMarker
	// webPseudoElemFirstLetter styles the first letter of an element's text
	// (a drop cap): the parser sets it as a run of its own.
	webPseudoElemFirstLetter
)

// webSelCompound is one compound selector, such as `div.note[data-x]:hover`.
type webSelCompound struct {
	tag     string // lower-case type selector; "" is any element
	comb    byte   // ' ', '>', '+' or '~' to the compound before; 0 for the first
	simples []webSelSimple
}

// webSelSimple is one simple selector inside a compound.
type webSelSimple struct {
	kind uint8 // webSelID … webSelPseudo
	op   uint8 // attribute operator or pseudo-class
	// fold compares an attribute value ASCII case-insensitively ([a=v i], or
	// an attribute HTML defines that way); value is then already lower-case.
	fold  bool
	name  string // id, class, attribute name, or the direction of :dir()
	value string
	// nthA and nthB are the An+B coefficients of an :nth-*() pseudo-class.
	nthA, nthB int
	// words are the language ranges of :lang(); args are the selectors of
	// :not(), :is() and :where(); hasArgs are the relative selectors of :has().
	words   []string
	args    []*webSelector
	hasArgs []webHasArg
	// nthOf filters the sibling list of an :nth-*() selector when it carries
	// an `of S` clause.
	nthOf []*webSelector
}

// webHasArg is one relative selector argument of :has(), with the relationship
// between the subject and the argument's leftmost compound.
type webHasArg struct {
	sel  *webSelector
	lead byte
}

// Simple selector kinds, in the order a compound tests them: the cheap checks
// come first, so an element whose class already fails never has its attributes
// parsed.
const (
	webSelID uint8 = iota
	webSelClass
	webSelAttr
	webSelPseudo
)

// Attribute selector operators.
const (
	webAttrExists uint8 = iota // [a]
	webAttrEquals              // [a=v]
	webAttrWord                // [a~=v]
	webAttrDash                // [a|=v]
	webAttrPrefix              // [a^=v]
	webAttrSuffix              // [a$=v]
	webAttrSubstr              // [a*=v]
)

// The pseudo-classes the matcher tells apart. webPseudoMaybe is every one it
// cannot decide from a start tag or the selector tree (:valid…);
// webPseudoNever is a state that is false on a page nobody is playing, and
// webPseudoUser a pointer or focus state (:hover, :focus…), false too except
// for a rule that reveals (webSelReveal). webPseudoTarget is :target, decided
// by the page address's fragment.
const (
	webPseudoMaybe uint8 = iota
	webPseudoNever
	webPseudoUser
	webPseudoTarget
	webPseudoRoot
	webPseudoLink
	webPseudoChecked
	webPseudoDisabled
	webPseudoEnabled
	webPseudoRequired
	webPseudoOptional
	webPseudoReadOnly
	webPseudoReadWrite
	webPseudoPlaceholder
	webPseudoEmpty
	webPseudoDefined
	webPseudoOpen
	webPseudoLang
	webPseudoDir
	webPseudoNot
	webPseudoIs
	webPseudoHas
	webPseudoFirstChild
	webPseudoLastChild
	webPseudoOnlyChild
	webPseudoNthChild
	webPseudoNthLastChild
	webPseudoNthOfType
	webPseudoNthLastOfType
	webPseudoOnlyOfType
)

// webSelMaxDepth bounds nested :not()/:is() arguments, so a hostile sheet
// cannot make the compiler recurse without end.
const webSelMaxDepth = 4

// webSelMode is how a rule is matched. webSelLenient is the reader's old
// leniency — an undecidable test is assumed to hold and every combinator reads
// as a descendant one — which costs nothing for a colour or a margin.
// webSelStrict is for a rule that would hide what it matches: it applies only
// on a definite match. webSelReveal is lenient too, and also answers the
// pointer and focus states "maybe": a reader nobody can hover or focus would
// otherwise never reach a sub-menu, tooltip or panel a page shows on :hover or
// :focus-within.
type webSelMode uint8

const (
	webSelLenient webSelMode = iota
	webSelStrict
	webSelReveal
)

// webSelSpec is a selector's specificity: ids; then classes, attributes and
// pseudo-classes; then types and pseudo-elements.
type webSelSpec struct{ a, b, c int }

func (s webSelSpec) less(o webSelSpec) bool {
	if s.a != o.a {
		return s.a < o.a
	}
	if s.b != o.b {
		return s.b < o.b
	}
	return s.c < o.c
}

// packed folds the three counts into one int that orders the same way. The old
// ids*100+classes*10+types sum let eleven classes outrank an id.
func (s webSelSpec) packed() int {
	return min(s.a, 999)*1000000 + min(s.b, 999)*1000 + min(s.c, 999)
}

// webSelParser reads one selector.
type webSelParser struct {
	src   string
	pos   int
	depth int
	sel   *webSelector
	spec  webSelSpec
}

// webCompileSelector compiles one selector — a list is split first, see
// webSplitSelectorList — and returns it with its packed specificity.
func webCompileSelector(text string) (*webSelector, int) {
	sel, spec := webCompileSelectorAt(text, 0)
	return sel, spec.packed()
}

func webCompileSelectorAt(text string, depth int) (*webSelector, webSelSpec) {
	p := &webSelParser{src: text, depth: depth, sel: &webSelector{}}
	var comb byte
	for {
		spaced := p.skipSpace()
		if p.pos >= len(p.src) {
			break
		}
		if c := p.src[p.pos]; c == '>' || c == '+' || c == '~' {
			// A second combinator, or one with no compound before it (a
			// relative selector, valid only inside :has()), does not parse.
			if (comb != 0 && comb != ' ') || len(p.sel.compounds) == 0 {
				p.sel.never = true
				return p.sel, p.spec
			}
			comb = c
			p.pos++
			continue
		}
		if spaced && comb == 0 && len(p.sel.compounds) > 0 {
			comb = ' '
		}
		compound, ok := p.compound()
		if !ok {
			p.sel.never = true
			return p.sel, p.spec
		}
		compound.comb = comb
		comb = 0
		p.sel.compounds = append(p.sel.compounds, compound)
	}
	if (comb != 0 && comb != ' ') || len(p.sel.compounds) == 0 {
		p.sel.never = true
	}
	return p.sel, p.spec
}

// skipSpace steps over whitespace and reports whether there was any.
func (p *webSelParser) skipSpace() bool {
	start := p.pos
	for p.pos < len(p.src) && webAttrSpaceByte(p.src[p.pos]) {
		p.pos++
	}
	return p.pos > start
}

// compound reads one compound selector: an optional type (or *), then any
// number of #id, .class, [attr] and :pseudo.
func (p *webSelParser) compound() (webSelCompound, bool) {
	var c webSelCompound
	start := p.pos
	if ch := p.src[p.pos]; ch == '*' || ch == '|' || webSelIdentStart(p.src, p.pos) {
		tag, ok := p.qualifiedName()
		if !ok {
			return c, false
		}
		if tag != "*" {
			c.tag = asciiLower(tag)
			p.spec.c++
		}
	}
	for p.pos < len(p.src) {
		ch := p.src[p.pos]
		if webAttrSpaceByte(ch) || ch == '>' || ch == '+' || ch == '~' {
			break
		}
		switch ch {
		case '#', '.':
			p.pos++
			name := p.name()
			if name == "" {
				return c, false
			}
			if ch == '#' {
				c.simples = append(c.simples, webSelSimple{kind: webSelID, name: name})
				p.spec.a++
			} else {
				c.simples = append(c.simples, webSelSimple{kind: webSelClass, name: name})
				p.spec.b++
			}
		case '[':
			simple, ok := p.attribute()
			if !ok {
				return c, false
			}
			c.simples = append(c.simples, simple)
			p.spec.b++
		case ':':
			if !p.pseudo(&c) {
				return c, false
			}
		default:
			return c, false
		}
	}
	if p.pos == start {
		return c, false
	}
	// Test order (ids, classes, attributes, pseudo-classes) by a stable
	// insertion sort: a compound holds a handful of simples, and
	// sort.SliceStable built a reflection swapper for every compound of a sheet.
	for i := 1; i < len(c.simples); i++ {
		for j := i; j > 0 && c.simples[j].kind < c.simples[j-1].kind; j-- {
			c.simples[j], c.simples[j-1] = c.simples[j-1], c.simples[j]
		}
	}
	return c, true
}

// qualifiedName reads a type selector or * with an optional namespace prefix
// (svg|rect, *|a, |a). Namespaces are not modelled: the local name decides.
func (p *webSelParser) qualifiedName() (string, bool) {
	name := ""
	if p.src[p.pos] == '*' {
		p.pos++
		name = "*"
	} else if p.src[p.pos] != '|' {
		name = p.name()
	}
	if p.pos < len(p.src) && p.src[p.pos] == '|' && (p.pos+1 >= len(p.src) || p.src[p.pos+1] != '=') {
		p.pos++
		switch {
		case p.pos < len(p.src) && p.src[p.pos] == '*':
			p.pos++
			name = "*"
		case webSelIdentStart(p.src, p.pos):
			name = p.name()
		default:
			return "", false
		}
	}
	return name, name != ""
}

// webSelIdentStart reports whether an identifier starts at s[i]: a letter, '_',
// '-', a non-ASCII byte or an escape.
func webSelIdentStart(s string, i int) bool {
	if i >= len(s) {
		return false
	}
	c := s[i]
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '-' || c >= 0x80 ||
		c == '\\' && i+1 < len(s) && s[i+1] != '\n'
}

// name reads a CSS name with its escapes decoded. Tailwind's `.md\:flex` is the
// class "md:flex"; it used to be read as the class "md" plus a stray token.
func (p *webSelParser) name() string {
	start := p.pos
	var b []byte
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if webSelectorNameByte(c) || c >= 0x80 {
			if b != nil {
				b = append(b, c)
			}
			p.pos++
			continue
		}
		if c != '\\' || p.pos+1 >= len(p.src) || p.src[p.pos+1] == '\n' {
			break
		}
		if b == nil {
			b = append([]byte(nil), p.src[start:p.pos]...)
		}
		p.pos++
		b = utf8.AppendRune(b, p.escape())
	}
	if b != nil {
		return string(b)
	}
	return p.src[start:p.pos]
}

// escape decodes the CSS escape after a backslash: up to six hex digits (and
// the one whitespace character that may end them), or the next character as
// itself.
func (p *webSelParser) escape() rune {
	if p.pos >= len(p.src) {
		return utf8.RuneError
	}
	if webSelHexVal(p.src[p.pos]) < 0 {
		r, size := utf8.DecodeRuneInString(p.src[p.pos:])
		p.pos += size
		return r
	}
	var r rune
	for n := 0; n < 6 && p.pos < len(p.src); n++ {
		v := webSelHexVal(p.src[p.pos])
		if v < 0 {
			break
		}
		r = r*16 + rune(v)
		p.pos++
	}
	if p.pos < len(p.src) && webAttrSpaceByte(p.src[p.pos]) {
		// CSS reads "\r\n" as one whitespace character.
		if p.src[p.pos] == '\r' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '\n' {
			p.pos++
		}
		p.pos++
	}
	if r == 0 || r > utf8.MaxRune || r >= 0xD800 && r <= 0xDFFF {
		return utf8.RuneError
	}
	return r
}

func webSelHexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c) - '0'
	case c >= 'a' && c <= 'f':
		return int(c) - 'a' + 10
	case c >= 'A' && c <= 'F':
		return int(c) - 'A' + 10
	}
	return -1
}

// str reads a quoted string with its escapes decoded.
func (p *webSelParser) str() (string, bool) {
	quote := p.src[p.pos]
	p.pos++
	start := p.pos
	var b []byte
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == quote:
			p.pos++
			if b != nil {
				return string(b), true
			}
			return p.src[start : p.pos-1], true
		case c == '\n':
			return "", false // an unclosed string ends at the line, like the selector
		case c == '\\':
			if b == nil {
				b = append([]byte(nil), p.src[start:p.pos]...)
			}
			p.pos++
			if p.pos < len(p.src) && p.src[p.pos] == '\n' {
				p.pos++ // an escaped newline continues the string
				continue
			}
			b = utf8.AppendRune(b, p.escape())
		default:
			if b != nil {
				b = append(b, c)
			}
			p.pos++
		}
	}
	return "", false
}

// attribute reads [name], [name op value] and [name op value i|s].
func (p *webSelParser) attribute() (webSelSimple, bool) {
	s := webSelSimple{kind: webSelAttr}
	p.pos++ // '['
	p.skipSpace()
	if p.pos >= len(p.src) {
		return s, false
	}
	name, ok := p.qualifiedName()
	if !ok || name == "*" {
		return s, false
	}
	s.name = asciiLower(name)
	p.skipSpace()
	if p.pos >= len(p.src) {
		return s, false
	}
	switch p.src[p.pos] {
	case ']':
		p.pos++
		s.op = webAttrExists
		return s, true
	case '=':
		s.op = webAttrEquals
		p.pos++
	case '~', '|', '^', '$', '*':
		if p.pos+1 >= len(p.src) || p.src[p.pos+1] != '=' {
			return s, false
		}
		switch p.src[p.pos] {
		case '~':
			s.op = webAttrWord
		case '|':
			s.op = webAttrDash
		case '^':
			s.op = webAttrPrefix
		case '$':
			s.op = webAttrSuffix
		default:
			s.op = webAttrSubstr
		}
		p.pos += 2
	default:
		return s, false
	}
	p.skipSpace()
	if p.pos >= len(p.src) {
		return s, false
	}
	if q := p.src[p.pos]; q == '"' || q == '\'' {
		if s.value, ok = p.str(); !ok {
			return s, false
		}
	} else if s.value = p.name(); s.value == "" {
		return s, false
	}
	p.skipSpace()
	s.fold = webSelFoldedAttr(s.name)
	if p.pos < len(p.src) && p.src[p.pos] != ']' {
		switch asciiLower(p.name()) {
		case "i":
			s.fold = true
		case "s":
			s.fold = false
		default:
			return s, false
		}
		p.skipSpace()
	}
	if p.pos >= len(p.src) || p.src[p.pos] != ']' {
		return s, false
	}
	p.pos++
	if s.fold {
		s.value = asciiLower(s.value)
	}
	return s, true
}

// webSelFoldedAttr lists the attributes whose values HTML compares
// case-insensitively in a selector, so `input[type=checkbox]` still matches
// type="CHECKBOX". The `s` flag turns that off.
func webSelFoldedAttr(name string) bool {
	switch name {
	case "accept", "accept-charset", "align", "alink", "axis", "bgcolor", "charset", "checked",
		"clear", "codetype", "color", "compact", "declare", "defer", "dir", "direction", "disabled",
		"enctype", "face", "frame", "hreflang", "http-equiv", "lang", "language", "link", "media",
		"method", "multiple", "nohref", "noresize", "noshade", "nowrap", "readonly", "rel", "rev",
		"rules", "scope", "scrolling", "selected", "shape", "target", "text", "type", "valign",
		"valuetype", "vlink":
		return true
	}
	return false
}

// pseudo reads a pseudo-class or a pseudo-element, with its argument if any.
func (p *webSelParser) pseudo(c *webSelCompound) bool {
	p.pos++ // ':'
	element := false
	if p.pos < len(p.src) && p.src[p.pos] == ':' {
		element = true
		p.pos++
	}
	name := asciiLower(p.name())
	if name == "" {
		return false
	}
	args, hasArgs := "", false
	if p.pos < len(p.src) && p.src[p.pos] == '(' {
		end := webSelParenEnd(p.src, p.pos)
		if end < 0 {
			return false
		}
		args, hasArgs = p.src[p.pos+1:end], true
		p.pos = end + 1
	}
	switch name {
	case "before", "after", "first-line", "first-letter":
		element = true // the CSS2 single-colon spelling of a pseudo-element
	}
	if element {
		// The declarations style a generated box, never the element itself.
		// Only before, after and marker are generated; the rest keep their
		// selector unmatchable so a ::first-line colour cannot repaint the
		// whole paragraph. A pseudo-element inside :is()/:not()/:has() is
		// not a valid element selector, so it stays unmatchable too.
		pseudo := webPseudoElemNone
		if p.depth == 0 {
			switch name {
			case "before":
				pseudo = webPseudoElemBefore
			case "after":
				pseudo = webPseudoElemAfter
			case "marker":
				pseudo = webPseudoElemMarker
			case "first-letter":
				pseudo = webPseudoElemFirstLetter
			}
		}
		if pseudo == webPseudoElemNone || p.sel.pseudo != webPseudoElemNone {
			p.sel.never = true
		} else {
			p.sel.pseudo = pseudo
		}
		p.spec.c++
		return true
	}
	s := webSelSimple{kind: webSelPseudo, op: webPseudoMaybe}
	switch name {
	case "hover", "active", "focus", "focus-visible", "focus-within":
		s.op = webPseudoUser
	case "target":
		s.op = webPseudoTarget
	case "visited", "target-within", "fullscreen", "modal", "popover-open", "picture-in-picture",
		"user-invalid", "user-valid", "autofill", "playing", "paused", "seeking", "buffering",
		"stalled", "current", "past", "future", "host", "host-context", "has-slotted",
		"active-view-transition", "active-view-transition-type":
		// :host and friends only ever match inside a shadow tree.
		s.op = webPseudoNever
	case "root", "scope":
		s.op = webPseudoRoot
	case "link", "any-link", "-webkit-any-link", "-moz-any-link":
		s.op = webPseudoLink
	case "checked":
		s.op = webPseudoChecked
	case "disabled":
		s.op = webPseudoDisabled
	case "enabled":
		s.op = webPseudoEnabled
	case "required":
		s.op = webPseudoRequired
	case "optional":
		s.op = webPseudoOptional
	case "read-only", "-moz-read-only":
		s.op = webPseudoReadOnly
	case "read-write", "-moz-read-write":
		s.op = webPseudoReadWrite
	case "placeholder-shown":
		s.op = webPseudoPlaceholder
	case "empty":
		s.op = webPseudoEmpty
	case "defined":
		s.op = webPseudoDefined
	case "open":
		s.op = webPseudoOpen
	case "lang":
		for _, part := range webSplitSelectorList(args) {
			if part = strings.Trim(strings.TrimSpace(part), `"'`); part != "" {
				s.words = append(s.words, asciiLower(part))
			}
		}
		if len(s.words) > 0 {
			s.op = webPseudoLang
		}
	case "dir":
		s.op = webPseudoDir
		s.name = asciiLower(strings.TrimSpace(args))
	case "first-child":
		if hasArgs {
			return false
		}
		s.op = webPseudoFirstChild
	case "last-child":
		if hasArgs {
			return false
		}
		s.op = webPseudoLastChild
	case "only-child":
		if hasArgs {
			return false
		}
		s.op = webPseudoOnlyChild
	case "first-of-type":
		if hasArgs {
			return false
		}
		s.op, s.nthB = webPseudoNthOfType, 1
	case "last-of-type":
		if hasArgs {
			return false
		}
		s.op, s.nthB = webPseudoNthLastOfType, 1
	case "only-of-type":
		if hasArgs {
			return false
		}
		s.op = webPseudoOnlyOfType
	case "nth-child":
		if !hasArgs {
			return false
		}
		if !p.parseNthArgs(&s, args) {
			return false
		}
		s.op = webPseudoNthChild
	case "nth-last-child":
		if !hasArgs {
			return false
		}
		if !p.parseNthArgs(&s, args) {
			return false
		}
		s.op = webPseudoNthLastChild
	case "nth-of-type":
		if !hasArgs {
			return false
		}
		if !p.parseNthArgs(&s, args) {
			return false
		}
		s.op = webPseudoNthOfType
	case "nth-last-of-type":
		if !hasArgs {
			return false
		}
		if !p.parseNthArgs(&s, args) {
			return false
		}
		s.op = webPseudoNthLastOfType
	case "not", "is", "matches", "where", "has", "-webkit-any", "-moz-any":
		if !hasArgs {
			return false
		}
		p.selectorArgs(c, s, name, args)
		return true
	default:
		if strings.HasPrefix(name, "-") {
			// Another engine's state or part (:-moz-focusring,
			// :-webkit-autofill, :-ms-input-placeholder): false here, and
			// dropped outright by every browser of another engine.
			s.op = webPseudoNever
		}
	}
	c.simples = append(c.simples, s)
	p.spec.b++
	return true
}

// selectorArgs compiles the selector list of :not(), :is(), :where() or :has().
// Its specificity is that of its most specific argument (:where() adds none).
// A :has() argument looks at descendants the parser has not seen yet, so it is
// compiled for that specificity only and the test stays undecidable.
//
// An argument that cannot match an element (a pseudo-element, or one that does
// not parse) adds no specificity. :is() and :where() forgive it — it simply
// matches nothing — but :not() does not: a browser drops `:not(::before)` and
// an empty `:not()` with the whole rule, where the matcher used to read them as
// "not this" and match every element.
func (p *webSelParser) selectorArgs(c *webSelCompound, s webSelSimple, name, args string) {
	switch name {
	case "not":
		s.op = webPseudoNot
	case "has":
		s.op = webPseudoHas
	default:
		s.op = webPseudoIs
	}
	var best webSelSpec
	if p.depth < webSelMaxDepth {
		for _, part := range webSplitSelectorList(args) {
			part = strings.TrimSpace(part)
			lead := byte(' ')
			if name == "has" && part != "" {
				switch part[0] {
				case '>', '+', '~':
					lead = part[0]
					part = strings.TrimSpace(part[1:])
				}
			}
			if part == "" {
				continue
			}
			arg, spec := webCompileSelectorAt(part, p.depth+1)
			if arg.never {
				if name == "not" {
					p.sel.never = true
				}
			} else if best.less(spec) {
				best = spec
			}
			if name == "has" {
				if !arg.never {
					s.hasArgs = append(s.hasArgs, webHasArg{sel: arg, lead: lead})
				}
			} else {
				s.args = append(s.args, arg)
			}
		}
		if (name == "not" && len(s.args) == 0) || (name == "has" && len(s.hasArgs) == 0) {
			p.sel.never = true
		}
	} else {
		s.op = webPseudoMaybe
	}
	if name != "where" {
		p.spec.a += best.a
		p.spec.b += best.b
		p.spec.c += best.c
	}
	c.simples = append(c.simples, s)
}

// webSelParenEnd finds the ')' closing the '(' at open, skipping quoted strings,
// escapes and nested brackets.
func webSelParenEnd(src string, open int) int {
	depth := 0
	var quote byte
	for i := open; i < len(src); i++ {
		c := src[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '\\':
			i++
		case c == '"' || c == '\'':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// webSplitSelectorList splits a selector list on its top-level commas.
func webSplitSelectorList(text string) []string {
	var parts []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '\\':
			i++
		case c == '"' || c == '\'':
			quote = c
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			if depth > 0 {
				depth--
			}
		case c == ',' && depth == 0:
			parts = append(parts, text[start:i])
			start = i + 1
		}
	}
	return append(parts, text[start:])
}

// webMaxSelectorTreeNodes bounds the structural copy built for selectors that
// need sibling structure. A page larger than this leaves those tests
// undecidable rather than spending the handheld's memory on a second DOM.
const webMaxSelectorTreeNodes = 8192

// webRulesPseudoMask records which generated boxes have rules. Selecting the
// pseudo-element is already part of each rule; this only lets the element walk
// skip the generated-box cascade entirely on sheets that never mention one.
func webRulesPseudoMask(rules []webRule) uint8 {
	var mask uint8
	for i := range rules {
		pseudo := rules[i].sel.pseudo
		if pseudo == webPseudoElemNone || pseudo >= 8 {
			continue
		}
		// A ::before/::after without `content` generates no box. Icon-font
		// sheets are full of colour/font rules for ::before, and those must
		// not cost every element a second rule scan. ::marker is different:
		// its colour and font paint the default bullet even without content.
		if pseudo != webPseudoElemMarker && pseudo != webPseudoElemFirstLetter {
			if _, ok := rules[i].decls["content"]; !ok {
				if _, ok := rules[i].important["content"]; !ok {
					continue
				}
			}
		}
		mask |= 1 << pseudo
	}
	return mask
}

// webRulesNeedSelectorTree reports whether any rule needs the selector tree:
// :has(), or one of the sibling-counting pseudo-classes, including one nested
// inside :is(), :not() or :where().
func webRulesNeedSelectorTree(rules []webRule) bool {
	for i := range rules {
		if webSelectorUsesTree(rules[i].sel) {
			return true
		}
	}
	return false
}

func webSelectorUsesTree(sel *webSelector) bool {
	if sel == nil {
		return false
	}
	for i := range sel.compounds {
		for j := range sel.compounds[i].simples {
			s := &sel.compounds[i].simples[j]
			if s.kind == webSelPseudo {
				switch s.op {
				case webPseudoHas, webPseudoFirstChild, webPseudoLastChild, webPseudoOnlyChild,
					webPseudoNthChild, webPseudoNthLastChild, webPseudoNthOfType,
					webPseudoNthLastOfType, webPseudoOnlyOfType:
					return true
				}
			}
			for _, arg := range s.args {
				if webSelectorUsesTree(arg) {
					return true
				}
			}
			for _, arg := range s.hasArgs {
				if webSelectorUsesTree(arg.sel) {
					return true
				}
			}
		}
	}
	return false
}

// webBuildSelectorTree parses the same markup walk sees into a lightweight
// parent/children tree. It deliberately skips raw-text elements and title/style
// exactly as walk does, so the node cursor stays aligned with startTag. Parser
// implicit closing rules are mirrored only where they affect ancestry.
func webBuildSelectorTree(source string) []*webElement {
	var nodes []*webElement
	var stack []*webElement
	// A synthetic root gives top-level elements a common parent, so :has(+ …)
	// can see their siblings even when the page omits <html> or <body>.
	root := &webElement{tag: "#document"}
	root.node = root
	for i := 0; i < len(source); {
		if source[i] != '<' {
			i++
			continue
		}
		if strings.HasPrefix(source[i:], "<!--") {
			if end := strings.Index(source[i+4:], "-->"); end >= 0 {
				i += 4 + end + 3
			} else {
				break
			}
			continue
		}
		if strings.HasPrefix(source[i:], "<!") || strings.HasPrefix(source[i:], "<?") {
			if end := strings.IndexByte(source[i:], '>'); end >= 0 {
				i += end + 1
			} else {
				break
			}
			continue
		}
		end := findTextBrowserTagEnd(source, i+1)
		if end < 0 {
			break
		}
		name, closing, selfClose, attrs := parseWebTag(source[i+1 : end])
		i = end + 1
		if name == "" {
			continue
		}
		if closing {
			for j := len(stack) - 1; j >= 0; j-- {
				if stack[j].tag == name {
					stack = stack[:j]
					break
				}
			}
			continue
		}
		if name == "title" || name == "style" || webRawTextElement(name) {
			_, i, _ = webRawElement(source, i, name)
			continue
		}
		if len(nodes) >= webMaxSelectorTreeNodes {
			return nil
		}
		if n := len(stack); n > 0 && stack[n-1].tag == "head" && name != "head" && !webHeadContentElement(name) {
			stack = stack[:n-1]
		}
		el := webNewElement(name, attrs)
		node := &el
		node.node = node
		if n := len(stack); n > 0 {
			node.parent = stack[n-1]
			stack[n-1].children = append(stack[n-1].children, node)
		} else {
			node.parent = root
			root.children = append(root.children, node)
		}
		nodes = append(nodes, node)
		if !webVoidElement(name) && !selfClose {
			stack = append(stack, node)
		}
	}
	return nodes
}

// webDeclsHide reports whether declarations would hide what they style, the
// way applyWebDecls reads display and visibility — or fade it out entirely:
// the reader paints opacity, and children inherit the fade, so `opacity: 0`
// makes text as invisible as display:none does (a web-component site's
// `:not(:defined) { opacity: 0 }` would otherwise blank every custom element).
// A value written with var() counts too: it may resolve to none or to 0.
func webDeclsHide(decls map[string]string) bool {
	for _, key := range [...]string{"display", "visibility"} {
		value, ok := decls[key]
		if !ok {
			continue
		}
		lower := asciiLower(strings.TrimSpace(value))
		if lower == "none" || lower == "hidden" || strings.Contains(lower, "var(") {
			return true
		}
	}
	if value, ok := decls["opacity"]; ok {
		if strings.Contains(asciiLower(value), "var(") {
			return true
		}
		if alpha, valid := webOpacityValue(value); valid && alpha <= 0 {
			return true
		}
	}
	return false
}

// webDeclsReveal reports whether declarations would show what they style: a
// display other than none, visibility: visible, or full opacity. A menu that
// opens on :hover or a tooltip shown on :focus is written that way, and those
// states never hold on the reader (webSelReveal). A partial opacity is not a
// reveal: `a:hover { opacity: .8 }` is a hover effect, and applying it as one
// would fade every link on the page.
func webDeclsReveal(decls map[string]string) bool {
	if value, ok := decls["display"]; ok {
		lower := asciiLower(strings.TrimSpace(value))
		if lower != "" && lower != "none" && !strings.Contains(lower, "var(") {
			return true
		}
	}
	if value, ok := decls["visibility"]; ok && asciiLower(strings.TrimSpace(value)) == "visible" {
		return true
	}
	if value, ok := decls["opacity"]; ok {
		if alpha, valid := webOpacityValue(value); valid && alpha >= 1 {
			return true
		}
	}
	return false
}

// matches reports whether the selector matches the last element of chain; the
// elements before it are its open ancestors, outermost first.
//
// hiding is set for a rule whose declarations would hide what it matches. Such
// a rule must not match on a guess: an undecidable test counts against it, `>`
// is checked against the real parent, and `+` or `~` fail it (the previous
// sibling is long gone in a streaming parse). Every other rule keeps the
// reader's old leniency — an undecidable test is assumed to hold and every
// combinator is read as a descendant one. A colour or a margin applied a little
// too widely costs nothing, and the open-element stack of a page that leaves
// its <p> and <li> unclosed is deeper than the real tree, which an exact `>`
// would punish.
func (sel *webSelector) matches(chain []webElement, hiding bool) bool {
	if hiding {
		return sel.matchesIn(chain, webSelStrict)
	}
	return sel.matchesIn(chain, webSelLenient)
}

// matchesIn is matches for a rule's own mode (webRule.mode): strict for a rule
// that hides, lenient otherwise, with the pointer and focus states answered
// "maybe" for a rule that reveals.
func (sel *webSelector) matchesIn(chain []webElement, mode webSelMode) bool {
	if sel == nil || sel.never || len(sel.compounds) == 0 || len(chain) == 0 {
		return false
	}
	if mode == webSelStrict {
		return sel.matchesExactly(chain)
	}
	last := len(sel.compounds) - 1
	if webSelCompoundTest(&sel.compounds[last], chain, len(chain)-1, mode) == webSelNo {
		return false
	}
	idx := len(chain) - 2
	for ci := last - 1; ci >= 0; ci-- {
		for idx >= 0 && webSelCompoundTest(&sel.compounds[ci], chain, idx, mode) == webSelNo {
			idx--
		}
		if idx < 0 {
			return false
		}
		idx--
	}
	return true
}

// matchesExactly is matches for a hiding rule. A run of compounds joined by `>`
// has to sit on consecutive elements of the chain. The runs between descendant
// combinators are placed nearest first, which leaves the most room above for
// the rest, so no placement is missed and nothing has to backtrack.
func (sel *webSelector) matchesExactly(chain []webElement) bool {
	end := len(sel.compounds) - 1
	top := -1 // chain index of the leftmost compound placed so far
	for {
		start := end
		for start > 0 && sel.compounds[start].comb == '>' {
			start--
		}
		span := end - start
		if top < 0 {
			// The run holding the subject ends at the element itself.
			if !sel.runAt(start, end, chain, len(chain)-1) {
				return false
			}
			top = len(chain) - 1 - span
		} else {
			pos := top - 1
			for pos-span >= 0 && !sel.runAt(start, end, chain, pos) {
				pos--
			}
			if pos-span < 0 {
				return false
			}
			top = pos - span
		}
		if start == 0 {
			return true
		}
		if sel.compounds[start].comb != ' ' {
			return false // `+` or `~`
		}
		end = start - 1
	}
}

// runAt reports whether compounds[start..end], joined by `>`, sit on the
// consecutive elements of chain that end at chain[pos], each a definite match.
func (sel *webSelector) runAt(start, end int, chain []webElement, pos int) bool {
	if pos-(end-start) < 0 {
		return false
	}
	for ci := end; ci >= start; ci-- {
		if webSelCompoundTest(&sel.compounds[ci], chain, pos-(end-ci), webSelStrict) != webSelYes {
			return false
		}
	}
	return true
}

// webSelCompoundTest tests one compound against chain[idx]. Both tags are
// lower case already (asciiLower when compiled, parseWebTag when read).
func webSelCompoundTest(c *webSelCompound, chain []webElement, idx int, mode webSelMode) webSelTri {
	el := &chain[idx]
	if c.tag != "" && c.tag != el.tag {
		return webSelNo
	}
	result := webSelYes
	for i := range c.simples {
		switch c.simples[i].test(chain, idx, mode) {
		case webSelNo:
			return webSelNo
		case webSelMaybe:
			result = webSelMaybe
		}
	}
	return result
}

// test answers one simple selector for chain[idx]. Ids and classes compare
// case-insensitively, as the reader always has.
func (s *webSelSimple) test(chain []webElement, idx int, mode webSelMode) webSelTri {
	el := &chain[idx]
	switch s.kind {
	case webSelID:
		return webSelBool(strings.EqualFold(el.id, s.name))
	case webSelClass:
		for _, have := range el.classes {
			if strings.EqualFold(have, s.name) {
				return webSelYes
			}
		}
		return webSelNo
	case webSelAttr:
		if mode == webSelStrict && s.name == "open" && el.tag == "details" {
			// The reader draws its own toggle for a <details> and keeps the body
			// either way (startTag). Primer's and normalize's
			// `details:not([open]) > :not(summary) { display: none }` would drop
			// the body's text instead, so opening the section showed nothing.
			return webSelMaybe
		}
		value, ok := el.attr(s.name)
		return webSelBool(ok && s.attrValueMatches(value))
	}
	return s.pseudoTest(chain, idx, mode)
}

// attrValueMatches applies the attribute operator to a present attribute's
// value. An empty ^=, $= or *= value matches nothing, as the spec says.
func (s *webSelSimple) attrValueMatches(value string) bool {
	if s.op == webAttrExists {
		return true
	}
	if s.fold {
		value = asciiLower(value)
	}
	want := s.value
	switch s.op {
	case webAttrEquals:
		return value == want
	case webAttrWord:
		return webSelHasWord(value, want)
	case webAttrDash:
		return value == want || len(value) > len(want) && value[len(want)] == '-' && strings.HasPrefix(value, want)
	case webAttrPrefix:
		return want != "" && strings.HasPrefix(value, want)
	case webAttrSuffix:
		return want != "" && strings.HasSuffix(value, want)
	case webAttrSubstr:
		return want != "" && strings.Contains(value, want)
	}
	return false
}

// webSelHasWord reports whether a whitespace-separated list holds word. An
// empty word, or one with whitespace in it, is in no list.
func webSelHasWord(list, word string) bool {
	if word == "" || strings.ContainsAny(word, " \t\n\r\f") {
		return false
	}
	for i := 0; i < len(list); {
		for i < len(list) && webAttrSpaceByte(list[i]) {
			i++
		}
		start := i
		for i < len(list) && !webAttrSpaceByte(list[i]) {
			i++
		}
		if i > start && list[start:i] == word {
			return true
		}
	}
	return false
}

// pseudoTest answers a pseudo-class for chain[idx].
func (s *webSelSimple) pseudoTest(chain []webElement, idx int, mode webSelMode) webSelTri {
	el := &chain[idx]
	switch s.op {
	case webPseudoNever:
		return webSelNo
	case webPseudoUser:
		if mode == webSelReveal {
			return webSelMaybe
		}
		return webSelNo
	case webPseudoTarget:
		return webSelBool(el.target)
	case webPseudoRoot:
		if el.tag == "html" {
			return webSelYes
		}
		if idx == 0 {
			// A page that leaves out <html> still has one; its first element
			// is the nearest thing to it in the chain.
			return webSelMaybe
		}
		return webSelNo
	case webPseudoLink:
		return webSelBool((el.tag == "a" || el.tag == "area") && el.hasAttr("href"))
	case webPseudoChecked:
		switch el.tag {
		case "input":
			kind := webSelInputType(el)
			return webSelBool((kind == "checkbox" || kind == "radio") && el.hasAttr("checked"))
		case "option":
			if el.hasAttr("selected") {
				return webSelYes
			}
			return webSelMaybe // a list's first option is selected when none says so
		}
		return webSelNo
	case webPseudoDisabled:
		return webSelDisabled(chain, idx)
	case webPseudoEnabled:
		if !webSelCanDisable(el.tag) {
			return webSelNo
		}
		return webSelNot(webSelDisabled(chain, idx))
	case webPseudoRequired, webPseudoOptional:
		if el.tag != "input" && el.tag != "select" && el.tag != "textarea" {
			return webSelNo
		}
		required := el.hasAttr("required")
		if el.tag == "input" {
			switch webSelInputType(el) {
			case "hidden", "range", "color", "submit", "image", "reset", "button":
				required = false // the attribute does not apply to these
			}
		}
		return webSelBool(required == (s.op == webPseudoRequired))
	case webPseudoReadWrite:
		return webSelReadWrite(chain, idx)
	case webPseudoReadOnly:
		return webSelNot(webSelReadWrite(chain, idx))
	case webPseudoPlaceholder:
		return webSelPlaceholderShown(el)
	case webPseudoEmpty:
		if webVoidElement(el.tag) {
			return webSelYes
		}
		return webSelMaybe // the content comes after the start tag
	case webPseudoDefined:
		if strings.IndexByte(el.tag, '-') >= 0 {
			return webSelMaybe // a custom element is defined once its script ran
		}
		return webSelYes
	case webPseudoOpen:
		if mode == webSelStrict && el.tag == "details" {
			return webSelMaybe // the reader's own toggle decides (see test)
		}
		return webSelBool((el.tag == "details" || el.tag == "dialog") && el.hasAttr("open"))
	case webPseudoLang:
		return s.langTest(chain, idx)
	case webPseudoDir:
		return s.dirTest(chain, idx)
	case webPseudoNot:
		return webSelNot(webSelAnyArg(s.args, chain, idx, webSelMaybe, mode))
	case webPseudoIs:
		return webSelAnyArg(s.args, chain, idx, webSelNo, mode)
	case webPseudoHas:
		if el.node == nil {
			// No structural tree was built (or the page exceeded its cap).
			// :has() cannot be affirmed without descendants, so a miss is the
			// honest answer rather than styling every candidate.
			return webSelNo
		}
		return webSelBool(s.hasMatches(el.node, mode))
	case webPseudoFirstChild, webPseudoLastChild, webPseudoOnlyChild, webPseudoNthChild, webPseudoNthLastChild:
		var position, siblings int
		var ok bool
		if len(s.nthOf) > 0 {
			position, siblings, ok = webSelFilteredPosition(el.node, chain, s.nthOf, false, mode)
		} else {
			position, siblings, ok = webSelChildPosition(el.node)
		}
		if !ok {
			return webSelMaybe
		}
		if position == 0 {
			return webSelNo
		}
		switch s.op {
		case webPseudoFirstChild:
			return webSelBool(position == 1)
		case webPseudoLastChild:
			return webSelBool(position == siblings)
		case webPseudoOnlyChild:
			return webSelBool(position == 1 && siblings == 1)
		case webPseudoNthLastChild:
			return webSelBool(webSelNthMatches(s.nthA, s.nthB, siblings-position+1))
		default:
			return webSelBool(webSelNthMatches(s.nthA, s.nthB, position))
		}
	case webPseudoNthOfType, webPseudoNthLastOfType, webPseudoOnlyOfType:
		var position, siblings int
		var ok bool
		if len(s.nthOf) > 0 {
			position, siblings, ok = webSelFilteredPosition(el.node, chain, s.nthOf, true, mode)
		} else {
			position, siblings, ok = webSelTypePosition(el.node)
		}
		if !ok {
			return webSelMaybe
		}
		if position == 0 {
			return webSelNo
		}
		if s.op == webPseudoOnlyOfType {
			return webSelBool(position == 1 && siblings == 1)
		}
		if s.op == webPseudoNthLastOfType {
			position = siblings - position + 1
		}
		return webSelBool(webSelNthMatches(s.nthA, s.nthB, position))
	}
	return webSelMaybe
}

// webSelAnyArg answers the argument list of :is() or :not(): yes as soon as one
// argument matches. Each argument is matched against the same element and full
// ancestor chain, so a complex argument such as `.card .title` checks both
// compounds instead of only its subject. An invalid argument follows the
// forgiving-list rules set by the caller.
func webSelAnyArg(args []*webSelector, chain []webElement, idx int, invalid webSelTri, mode webSelMode) webSelTri {
	result := webSelNo
	for _, arg := range args {
		r := invalid
		if arg != nil && !arg.never {
			r = webSelBool(arg.matchesIn(chain, mode))
		}
		if r == webSelYes {
			return webSelYes
		}
		if r == webSelMaybe {
			result = webSelMaybe
		}
	}
	return result
}

// hasMatches answers :has() against the structural tree. The prebuilt nodes
// give it descendants and following siblings that the open-element stack alone
// cannot see.
func (s *webSelSimple) hasMatches(anchor *webElement, mode webSelMode) bool {
	for _, arg := range s.hasArgs {
		if webSelHasRelative(anchor, arg, mode) {
			return true
		}
	}
	return false
}

func webSelHasRelative(anchor *webElement, arg webHasArg, mode webSelMode) bool {
	switch arg.lead {
	case '>':
		for _, child := range anchor.children {
			if webSelHasSubtree(child, arg, true, mode) {
				return true
			}
		}
		return false
	case '+':
		if sibling := webSelNextElement(anchor); sibling != nil {
			return webSelHasSubtree(sibling, arg, true, mode)
		}
		return false
	case '~':
		for sibling := webSelNextElement(anchor); sibling != nil; sibling = webSelNextElement(sibling) {
			if webSelHasSubtree(sibling, arg, true, mode) {
				return true
			}
		}
		return false
	default:
		for _, child := range anchor.children {
			if webSelHasSubtree(child, arg, false, mode) {
				return true
			}
		}
		return false
	}
}

// webSelHasSubtree tests one relative-selector root and everything below it.
// rootBound requires the argument's leftmost compound to sit on the root, which
// is what makes `> img` a child test and `+ .x` a next-sibling test.
func webSelHasSubtree(root *webElement, arg webHasArg, rootBound bool, mode webSelMode) bool {
	type pending struct {
		el   *webElement
		path []webElement
	}
	stack := []pending{{el: root}}
	visited := 0
	for len(stack) > 0 {
		next := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		path := append(next.path, *next.el)
		if arg.sel.matchesIn(path, mode) {
			if !rootBound || len(arg.sel.compounds) == 0 ||
				webSelCompoundTest(&arg.sel.compounds[0], path, 0, mode) != webSelNo {
				return true
			}
		}
		visited++
		if visited > webMaxSelectorTreeNodes {
			return false
		}
		for i := len(next.el.children) - 1; i >= 0; i-- {
			stack = append(stack, pending{el: next.el.children[i], path: path})
		}
	}
	return false
}

// parseNthArgs reads the An+B argument of an :nth-*() pseudo-class and
// compiles its optional `of S` selector list. The selector list contributes
// the specificity of its most specific complex selector, like :is().
func (p *webSelParser) parseNthArgs(s *webSelSimple, args string) bool {
	nth, of, hasOf := webSelSplitNthOf(args)
	a, b, ok := webSelParseNth(nth)
	if !ok {
		return false
	}
	s.nthA, s.nthB = a, b
	if !hasOf {
		return true
	}
	if p.depth >= webSelMaxDepth {
		p.sel.never = true
		return false
	}
	var best webSelSpec
	for _, part := range webSplitSelectorList(of) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		arg, spec := webCompileSelectorAt(part, p.depth+1)
		if arg.never {
			p.sel.never = true
			return false
		}
		s.nthOf = append(s.nthOf, arg)
		if best.less(spec) {
			best = spec
		}
	}
	if len(s.nthOf) == 0 {
		p.sel.never = true
		return false
	}
	p.spec.a += best.a
	p.spec.b += best.b
	p.spec.c += best.c
	return true
}

// webSelSplitNthOf splits `An+B of S` at the top-level `of` token. Parentheses,
// brackets, quoted strings and escapes are skipped, so `of` inside :is() or an
// attribute value does not split the argument.
func webSelSplitNthOf(arg string) (nth, of string, hasOf bool) {
	for i := 0; i < len(arg); {
		c := arg[i]
		if c == '\\' {
			i += 2
			continue
		}
		if c == '"' || c == '\'' {
			quote := c
			i++
			for i < len(arg) && arg[i] != quote {
				if arg[i] == '\\' {
					i++
				}
				i++
			}
			i++
			continue
		}
		if c == '(' || c == '[' {
			depth := 1
			i++
			for i < len(arg) && depth > 0 {
				switch arg[i] {
				case '\\':
					i++
				case '"', '\'':
					quote := arg[i]
					i++
					for i < len(arg) && arg[i] != quote {
						if arg[i] == '\\' {
							i++
						}
						i++
					}
				case '(', '[':
					depth++
				case ')', ']':
					depth--
				}
				i++
			}
			continue
		}
		if webAttrSpaceByte(c) {
			start := i
			for i < len(arg) && webAttrSpaceByte(arg[i]) {
				i++
			}
			token := i
			for token < len(arg) && !webAttrSpaceByte(arg[token]) && arg[token] != '(' && arg[token] != '[' {
				token++
			}
			if asciiLower(arg[i:token]) == "of" {
				return strings.TrimSpace(arg[:start]), strings.TrimSpace(arg[token:]), true
			}
			continue
		}
		i++
	}
	return strings.TrimSpace(arg), "", false
}

// webSelParseNth reads the An+B part of an :nth-*() argument. Whitespace
// around the sign is allowed as it is in CSS.
func webSelParseNth(arg string) (a, b int, ok bool) {
	text := asciiLower(arg)
	text = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r', '\f':
			return -1
		}
		return r
	}, text)
	if text == "odd" {
		return 2, 1, true
	}
	if text == "even" {
		return 2, 0, true
	}
	if text == "" {
		return 0, 0, false
	}
	if n := strings.IndexByte(text, 'n'); n >= 0 {
		coef := text[:n]
		switch coef {
		case "", "+":
			a = 1
		case "-":
			a = -1
		default:
			var err error
			if a, err = strconv.Atoi(coef); err != nil {
				return 0, 0, false
			}
		}
		rest := text[n+1:]
		if rest == "" {
			return a, 0, true
		}
		if rest[0] != '+' && rest[0] != '-' {
			return 0, 0, false
		}
		var err error
		if b, err = strconv.Atoi(rest); err != nil {
			return 0, 0, false
		}
		return a, b, true
	}
	var err error
	if b, err = strconv.Atoi(text); err != nil {
		return 0, 0, false
	}
	return 0, b, true
}

// webSelNthMatches reports whether the 1-based child position n satisfies
// An+B for some integer n >= 0.
func webSelNthMatches(a, b, position int) bool {
	if a == 0 {
		return position == b
	}
	d := position - b
	if a > 0 {
		return d >= 0 && d%a == 0
	}
	return d <= 0 && (-d)%(-a) == 0
}

// webSelChildPosition returns an element's one-based position among its
// element siblings and the sibling count.
func webSelChildPosition(el *webElement) (position, siblings int, ok bool) {
	if el == nil || el.parent == nil {
		return 0, 0, false
	}
	for i, sibling := range el.parent.children {
		if sibling == el {
			return i + 1, len(el.parent.children), true
		}
	}
	return 0, 0, false
}

// webSelTypePosition returns an element's one-based position among sibling
// elements with the same tag. CSS counts types, not all children: in
// <p>, <span>, <span> the second span is type position 2, not child position 3.
func webSelTypePosition(el *webElement) (position, siblings int, ok bool) {
	if el == nil || el.parent == nil {
		return 0, 0, false
	}
	for _, sibling := range el.parent.children {
		if sibling.tag != el.tag {
			continue
		}
		siblings++
		if sibling == el {
			position = siblings
		}
	}
	return position, siblings, true
}

// webSelFilteredPosition counts the siblings that match an `of S` list. When
// typeOnly is set, siblings of another tag are skipped first, which is how
// :nth-of-type(An+B of S) combines type and selector filtering.
func webSelFilteredPosition(el *webElement, chain []webElement, filter []*webSelector, typeOnly bool, mode webSelMode) (position, siblings int, ok bool) {
	if el == nil || el.parent == nil {
		return 0, 0, false
	}
	for _, sibling := range el.parent.children {
		if typeOnly && sibling.tag != el.tag {
			continue
		}
		if !webSelNthFilterMatches(filter, chain, sibling, mode) {
			continue
		}
		siblings++
		if sibling == el {
			position = siblings
		}
	}
	return position, siblings, true
}

// webSelNthFilterMatches tests one sibling against the `of S` selector list.
// The sibling replaces the subject at the end of the ancestor chain, so class,
// tag, attribute and nested pseudo-class filters all see the right element.
func webSelNthFilterMatches(filter []*webSelector, chain []webElement, sibling *webElement, mode webSelMode) bool {
	if len(chain) == 0 || sibling == nil {
		return false
	}
	path := make([]webElement, len(chain))
	copy(path, chain)
	path[len(path)-1] = *sibling
	for _, arg := range filter {
		if arg != nil && !arg.never && arg.matchesIn(path, mode) {
			return true
		}
	}
	return false
}

// webSelNextElement returns the next element sibling in the selector tree.
func webSelNextElement(el *webElement) *webElement {
	if el == nil || el.parent == nil {
		return nil
	}
	for i, sibling := range el.parent.children {
		if sibling == el && i+1 < len(el.parent.children) {
			return el.parent.children[i+1]
		}
	}
	return nil
}

// webSelCanDisable lists the elements :disabled and :enabled apply to.
func webSelCanDisable(tag string) bool {
	switch tag {
	case "button", "input", "select", "textarea", "optgroup", "option", "fieldset":
		return true
	}
	return false
}

// webSelDisabled answers :disabled: the attribute itself, a disabled <optgroup>
// around an option, or a disabled <fieldset> around a control — except inside
// the fieldset's first <legend>, which the parser cannot tell from a later one.
func webSelDisabled(chain []webElement, idx int) webSelTri {
	el := &chain[idx]
	if !webSelCanDisable(el.tag) {
		return webSelNo
	}
	if el.hasAttr("disabled") {
		return webSelYes
	}
	if el.tag == "option" || el.tag == "optgroup" {
		return webSelBool(el.tag == "option" && idx > 0 && chain[idx-1].tag == "optgroup" && chain[idx-1].hasAttr("disabled"))
	}
	for i := idx - 1; i >= 0; i-- {
		if chain[i].tag != "fieldset" || !chain[i].hasAttr("disabled") {
			continue
		}
		if i+1 < idx && chain[i+1].tag == "legend" {
			return webSelMaybe
		}
		return webSelYes
	}
	return webSelNo
}

// webSelInputType is an <input>'s type in lower case; a missing one is "text".
func webSelInputType(el *webElement) string {
	kind, ok := el.attr("type")
	kind = asciiLower(strings.TrimSpace(kind))
	if !ok || kind == "" {
		return "text"
	}
	return kind
}

// webSelReadWrite answers :read-write: a text field or textarea that is neither
// readonly nor disabled, or anything inside a contenteditable element.
func webSelReadWrite(chain []webElement, idx int) webSelTri {
	el := &chain[idx]
	if el.tag == "input" || el.tag == "textarea" {
		if el.tag == "input" {
			switch webSelInputType(el) {
			case "hidden", "range", "color", "checkbox", "radio", "file", "submit", "image", "reset", "button":
				return webSelNo
			}
		}
		if el.hasAttr("readonly") {
			return webSelNo
		}
		return webSelNot(webSelDisabled(chain, idx))
	}
	for i := idx; i >= 0; i-- {
		value, ok := chain[i].attr("contenteditable")
		if !ok {
			continue
		}
		switch asciiLower(strings.TrimSpace(value)) {
		case "", "true", "plaintext-only":
			return webSelYes
		case "false":
			return webSelNo
		}
	}
	return webSelNo
}

// webSelPlaceholderShown answers :placeholder-shown. A textarea's value is the
// text after its start tag, so only an <input> can be decided.
func webSelPlaceholderShown(el *webElement) webSelTri {
	placeholder, ok := el.attr("placeholder")
	switch el.tag {
	case "input":
		switch webSelInputType(el) {
		case "hidden", "range", "color", "checkbox", "radio", "file", "submit", "image", "reset", "button",
			"date", "month", "week", "time", "datetime-local":
			return webSelNo
		}
	case "textarea":
		if !ok {
			return webSelNo
		}
		return webSelMaybe
	default:
		return webSelNo
	}
	if !ok {
		return webSelNo
	}
	if placeholder == "" {
		return webSelMaybe // engines disagree about an empty placeholder
	}
	value, _ := el.attr("value")
	return webSelBool(value == "")
}

// langTest answers :lang(): the nearest lang attribute decides, matching a
// range exactly or as the prefix before a '-' ("en" matches "en-GB").
func (s *webSelSimple) langTest(chain []webElement, idx int) webSelTri {
	for i := idx; i >= 0; i-- {
		value, ok := chain[i].attr("lang")
		if !ok {
			continue
		}
		value = asciiLower(strings.TrimSpace(value))
		if value == "" {
			return webSelNo // lang="" is an unknown language
		}
		result := webSelNo
		for _, want := range s.words {
			if strings.HasPrefix(want, "*") {
				result = webSelMaybe
				continue
			}
			if value == want || len(value) > len(want) && value[len(want)] == '-' && strings.HasPrefix(value, want) {
				return webSelYes
			}
		}
		return result
	}
	return webSelMaybe // the language may come from a header or a <meta>
}

// dirTest answers :dir(): the nearest valid dir attribute decides, and a page
// that sets none runs left to right.
func (s *webSelSimple) dirTest(chain []webElement, idx int) webSelTri {
	for i := idx; i >= 0; i-- {
		value, ok := chain[i].attr("dir")
		if !ok {
			continue
		}
		switch dir := asciiLower(strings.TrimSpace(value)); dir {
		case "ltr", "rtl":
			return webSelBool(dir == s.name)
		case "auto":
			return webSelMaybe // decided by the text, which comes later
		}
	}
	return webSelBool(s.name == "ltr")
}

// webAttrSet is an element's attributes: the raw text that followed the tag
// name, parsed the first time a selector asks for an attribute.
type webAttrSet struct {
	raw    string
	parsed bool
	list   []webAttr
}

// webAttr is one attribute: the name in lower case, the value with its
// character references decoded the way htmlAttrValue does — but not trimmed,
// since a selector compares the exact value.
type webAttr struct {
	name  string
	value string
}

// webMaxElementAttrs bounds the attributes kept for one element. A real element
// has a handful; a hostile one with thousands would turn every attribute test
// into a long scan.
const webMaxElementAttrs = 64

// webNewElement builds the matcher's view of an element from its start tag.
func webNewElement(tag, attrs string) webElement {
	el := webElement{tag: tag}
	if strings.TrimSpace(attrs) == "" {
		return el
	}
	el.id = strings.TrimSpace(htmlTagAttribute(attrs, "id"))
	el.classes = strings.Fields(htmlTagAttribute(attrs, "class"))
	el.attrs = &webAttrSet{raw: attrs}
	return el
}

// attr returns an attribute's value and whether the element has it at all.
func (e *webElement) attr(name string) (string, bool) {
	set := e.attrs
	if set == nil {
		return "", false
	}
	if !set.parsed {
		set.list, set.parsed = webParseAttrs(set.raw), true
	}
	for i := range set.list {
		if set.list[i].name == name {
			return set.list[i].value, true
		}
	}
	return "", false
}

func (e *webElement) hasAttr(name string) bool {
	_, ok := e.attr(name)
	return ok
}

// webParseAttrs reads a start tag's attribute text with the tokenisation
// htmlTagAttribute and htmlTagAttributeHas use: single, double or unquoted
// values, a bare name for a boolean attribute, and a stray '>' stepped over.
// When a name repeats, the first one is found first, as in HTML.
func webParseAttrs(raw string) []webAttr {
	// Sized from the '=' count: a sheet with bare attribute selectors ([hidden],
	// [type=button]) parses nearly every element that has attributes.
	out := make([]webAttr, 0, min(strings.Count(raw, "=")+1, webMaxElementAttrs))
	for i := 0; i < len(raw) && len(out) < webMaxElementAttrs; {
		for i < len(raw) && (webAttrSpaceByte(raw[i]) || raw[i] == '/') {
			i++
		}
		start := i
		for i < len(raw) && raw[i] != '=' && raw[i] != '/' && raw[i] != '>' && !webAttrSpaceByte(raw[i]) {
			i++
		}
		if i == start && i < len(raw) && raw[i] != '=' {
			i++
			continue
		}
		name := asciiLower(raw[start:i])
		for i < len(raw) && webAttrSpaceByte(raw[i]) {
			i++
		}
		value := ""
		if i < len(raw) && raw[i] == '=' {
			i++
			for i < len(raw) && webAttrSpaceByte(raw[i]) {
				i++
			}
			if i < len(raw) && (raw[i] == '"' || raw[i] == '\'') {
				quote := raw[i]
				i++
				valueStart := i
				for i < len(raw) && raw[i] != quote {
					i++
				}
				value = raw[valueStart:i]
				if i < len(raw) {
					i++
				}
			} else {
				valueStart := i
				for i < len(raw) && raw[i] != '>' && !webAttrSpaceByte(raw[i]) {
					i++
				}
				value = raw[valueStart:i]
			}
			if strings.IndexByte(value, '&') >= 0 {
				value = htmlstd.UnescapeString(value)
			}
		}
		if name != "" {
			out = append(out, webAttr{name: name, value: value})
		}
	}
	return out
}

func webAttrSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

func webSelectorNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '-' || c == '_'
}

// --- CSS values -----------------------------------------------------------

// parseWebColor reads a CSS colour: hex, a named or system colour, or any
// colour function (webpage_color.go). Alpha is kept: a translucent gradient
// overlay is how most hero images stay readable, and dropping it painted the
// ramp as an opaque sheet over the photo. The keyword transparent reports
// false, which is what callers that look for a surface want; a colour with
// alpha 0 written any other way (#0000 in Tailwind's empty shadows) is a
// colour, so a shadow made of it stays invisible.
func parseWebColor(value string) (sdl.Color, bool) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" || v == "transparent" {
		return sdl.Color{}, false
	}
	c, ok := webCSSColor(v, 0)
	if !ok {
		return sdl.Color{}, false
	}
	return c.toSDL(), true
}

func webHexByte(text string) (uint8, bool) {
	n, err := strconv.ParseUint(text, 16, 8)
	if err != nil {
		return 0, false
	}
	return uint8(n), true
}

// webViewportLength resolves viewport-relative units. The reader draws on a
// real screen, so vw/vh and the modern svh/dvh/lvh aliases describe that screen
// rather than being dropped: `min-height: 100dvh` is a common full-panel
// section, and dropping it collapsed those sections to their text height.
var webViewportUnitSuffixes = [...]string{
	"svmin", "dvmin", "lvmin", "svmax", "dvmax", "lvmax", "vmin", "vmax",
	"svw", "dvw", "lvw", "svi", "dvi", "lvi", "vw", "vi",
	"svh", "dvh", "lvh", "svb", "dvb", "lvb", "vh", "vb",
}

func webViewportLength(value string) (float64, bool) {
	v := strings.ToLower(strings.TrimSpace(value))
	for _, unit := range webViewportUnitSuffixes {
		if !strings.HasSuffix(v, unit) {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(v, unit)), 64)
		if err != nil {
			return 0, false
		}
		w, h := webMediaViewport()
		switch unit {
		case "svmin", "dvmin", "lvmin", "vmin":
			return n * min(w, h) / 100, true
		case "svmax", "dvmax", "lvmax", "vmax":
			return n * max(w, h) / 100, true
		case "svh", "dvh", "lvh", "vh", "svb", "dvb", "lvb", "vb":
			return n * h / 100, true
		default:
			return n * w / 100, true
		}
	}
	return 0, false
}

// webLength converts a CSS length to px. Percentages resolve against rel.
func webLength(value string, rel float64) float64 {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" || v == "auto" || v == "none" {
		return 0
	}
	if px, ok := webMathLength(v, rel); ok {
		return px
	}
	if px, ok := webViewportLength(v); ok {
		return px
	}
	mult := 1.0
	switch {
	case strings.HasSuffix(v, "rem"):
		v = strings.TrimSuffix(v, "rem")
		mult = 16
	case strings.HasSuffix(v, "em"):
		v = strings.TrimSuffix(v, "em")
		mult = rel
		if mult <= 0 {
			mult = 16
		}
	case strings.HasSuffix(v, "px"):
		v = strings.TrimSuffix(v, "px")
	case strings.HasSuffix(v, "pt"):
		v = strings.TrimSuffix(v, "pt")
		mult = 1.333
	case strings.HasSuffix(v, "%"):
		v = strings.TrimSuffix(v, "%")
		mult = rel / 100
	case strings.HasSuffix(v, "ex"), strings.HasSuffix(v, "ch"):
		v = strings.TrimRightFunc(v, func(r rune) bool { return r >= 'a' && r <= 'z' })
		mult = rel / 2
	case strings.HasSuffix(v, "vw"), strings.HasSuffix(v, "vh"),
		strings.HasSuffix(v, "vmin"), strings.HasSuffix(v, "vmax"):
		return 0
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0
	}
	return n * mult
}

// webHeightLength leaves percentages for webResolvePercentageHeights, which
// can use the containing block's height instead of the element's font size.
func webHeightLength(value string, rel float64) float64 {
	v := strings.TrimSpace(value)
	if strings.HasSuffix(v, "%") {
		return 0
	}
	return webLength(v, rel)
}

// webResolvePercentageHeights replaces only declarations containing a percent.
// The general declaration parser cannot resolve them because its relative
// length argument is the font size. Leaving an indefinite percentage at zero
// preserves CSS's auto-height behaviour for content-sized ancestors.
func webResolvePercentageHeights(st *webStyle, decls map[string]string, parentHeight float64) {
	for _, property := range [...]struct {
		name   string
		target *float64
	}{
		{"height", &st.Height},
		{"min-height", &st.MinHeight},
		{"max-height", &st.MaxHeight},
	} {
		value, ok := decls[property.name]
		if !ok || !strings.Contains(value, "%") {
			continue
		}
		*property.target = 0
		if parentHeight <= 0 {
			continue
		}
		vw, vh := webMediaViewport()
		if px, valid := webEvalCalcContainerPercent(strings.TrimSpace(value), st.FontPx, vw, vh, vw, vh, parentHeight); valid && px > 0 {
			*property.target = px
		}
	}
}

// webMathLength evaluates the CSS math functions modern layouts use for fluid
// sizing: min(), max() and clamp(). The arguments are resolved against the same
// relative width as a plain length; viewport units still resolve to zero
// because the reader has no viewport-relative box of its own.
func webMathLength(v string, rel float64) (float64, bool) {
	for _, name := range []string{"clamp", "min", "max"} {
		prefix := name + "("
		if !strings.HasPrefix(v, prefix) || !strings.HasSuffix(v, ")") {
			continue
		}
		args := webSplitTopLevel(v[len(prefix):len(v)-1], ',')
		if name == "clamp" && len(args) != 3 {
			return 0, false
		}
		if len(args) == 0 {
			return 0, false
		}
		vals := make([]float64, len(args))
		for i, arg := range args {
			arg = strings.TrimSpace(arg)
			if arg == "" {
				return 0, false
			}
			vals[i] = webLength(arg, rel)
		}
		switch name {
		case "clamp":
			return max(vals[0], min(vals[1], vals[2])), true
		case "min":
			out := vals[0]
			for _, val := range vals[1:] {
				out = min(out, val)
			}
			return out, true
		default:
			out := vals[0]
			for _, val := range vals[1:] {
				out = max(out, val)
			}
			return out, true
		}
	}
	return 0, false
}

// webFontSizePx resolves font-size against the inherited size.
func webFontSizePx(value string, parent float64) (float64, bool) {
	v := strings.ToLower(strings.TrimSpace(value))
	switch v {
	case "xx-small":
		return 9, true
	case "x-small":
		return 11, true
	case "small":
		return 13, true
	case "medium", "initial":
		return 16, true
	case "large":
		return 18, true
	case "x-large":
		return 24, true
	case "xx-large":
		return 32, true
	case "smaller":
		return parent * 0.85, true
	case "larger":
		return parent * 1.2, true
	}
	px := webLength(v, parent)
	if px <= 0 {
		return 0, false
	}
	return px, true
}

// webMarginPair reads the vertical values of a margin shorthand.
func webMarginPair(value string, fontPx float64) (top, bottom float64) {
	top, _, bottom, _, valid := webBoxShorthand(value, fontPx)
	if !valid {
		return 0, 0
	}
	return top, bottom
}

// webBoxShorthand expands a one-to-four value CSS box shorthand into its top,
// right, bottom and left lengths.
func webBoxShorthand(value string, fontPx float64) (top, right, bottom, left float64, ok bool) {
	// Split on top-level whitespace so a modern `calc()`/`min()` edge stays
	// one length instead of becoming several box sides.
	fields := webCSSSpaceFields(value)
	if len(fields) == 0 {
		return 0, 0, 0, 0, false
	}
	top = webLength(fields[0], fontPx)
	switch len(fields) {
	case 1:
		right, bottom, left = top, top, top
	case 2:
		right = webLength(fields[1], fontPx)
		bottom, left = top, right
	case 3:
		right = webLength(fields[1], fontPx)
		bottom, left = webLength(fields[2], fontPx), right
	default:
		right = webLength(fields[1], fontPx)
		bottom = webLength(fields[2], fontPx)
		left = webLength(fields[3], fontPx)
	}
	return top, right, bottom, left, true
}

// webCSSAuto recognises the one margin keyword that needs layout, not a
// numeric value. The spacing helpers still resolve it to zero; the style keeps
// a separate flag so a constrained block can centre once layout knows its used
// width.
func webCSSAuto(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "auto")
}

// webAutoMarginSides reports the physical left/right auto margins of a
// one-to-four value margin shorthand. The two-value form repeats its second
// value on both inline sides, as CSS does.
func webAutoMarginSides(value string) (left, right bool) {
	fields := webCSSSpaceFields(value)
	switch len(fields) {
	case 0:
		return false, false
	case 1:
		left = webCSSAuto(fields[0])
		return left, left
	case 2, 3:
		right = webCSSAuto(fields[1])
		return right, right
	default:
		return webCSSAuto(fields[3]), webCSSAuto(fields[1])
	}
}

// webAutoInlineMarginSides is webAutoMarginSides for the two-value
// margin-inline form, whose first value is inline-start and second inline-end.
func webAutoInlineMarginSides(value string) (left, right bool) {
	fields := webCSSSpaceFields(value)
	if len(fields) == 0 {
		return false, false
	}
	if len(fields) == 1 {
		left = webCSSAuto(fields[0])
		return left, left
	}
	return webCSSAuto(fields[0]), webCSSAuto(fields[1])
}

// webAutoBlockSides reports the physical top/bottom auto margins of a
// one-to-four value margin shorthand. The two-value form repeats its first
// value on both block sides.
func webAutoBlockSides(value string) (top, bottom bool) {
	fields := webCSSSpaceFields(value)
	switch len(fields) {
	case 0:
		return false, false
	case 1:
		top = webCSSAuto(fields[0])
		return top, top
	case 2:
		top = webCSSAuto(fields[0])
		return top, top
	case 3:
		return webCSSAuto(fields[0]), webCSSAuto(fields[2])
	default:
		return webCSSAuto(fields[0]), webCSSAuto(fields[2])
	}
}

// webAutoBlockLogicalSides is webAutoBlockSides for margin-block, whose
// two-value form is block-start then block-end.
func webAutoBlockLogicalSides(value string) (top, bottom bool) {
	fields := webCSSSpaceFields(value)
	if len(fields) == 0 {
		return false, false
	}
	if len(fields) == 1 {
		top = webCSSAuto(fields[0])
		return top, top
	}
	return webCSSAuto(fields[0]), webCSSAuto(fields[1])
}

func webTextAlign(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "center", "-webkit-center", "-moz-center", "-khtml-center":
		return 1
	case "right", "end", "-webkit-right", "-moz-right":
		return 2
	case "justify":
		return 3
	}
	return 0
}

// webTextTransform decodes text-transform into the layout's small enum.
func webTextTransform(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "uppercase":
		return 1
	case "lowercase":
		return 2
	case "capitalize":
		return 3
	}
	return 0
}

func webBorderStyleInValue(value string) int {
	lower := strings.ToLower(value)
	for _, field := range strings.FieldsFunc(lower, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ',' || r == '/'
	}) {
		switch field {
		case "none":
			return webBorderNone
		case "hidden":
			return webBorderHidden
		case "dashed":
			return webBorderDashed
		case "dotted":
			return webBorderDotted
		case "double":
			return webBorderDouble
		case "solid":
			return webBorderSolid
		}
	}
	return -1
}

// webBackgroundURL pulls the URL out of the common url("...") and url(...)
// spellings. Gradients and colours deliberately return empty.
func webBackgroundURL(value string) string {
	lower := strings.ToLower(value)
	start := strings.Index(lower, "url(")
	if start < 0 {
		return ""
	}
	rest := strings.TrimSpace(value[start+4:])
	// A quoted URL ends at its closing quote, not at the first ')': an SVG
	// data URI written as utf8 markup carries transform="translate(…)" and
	// the like. The reader draws those now (webpage_svg.go), so they are kept.
	if rest != "" && (rest[0] == '"' || rest[0] == '\'') {
		quote := rest[0]
		var b strings.Builder
		for i := 1; i < len(rest); i++ {
			c := rest[i]
			if c == quote {
				return strings.TrimSpace(b.String())
			}
			if c == '\\' && i+1 < len(rest) {
				i++
				c = rest[i]
				if c == '\n' {
					continue
				}
			}
			b.WriteByte(c)
		}
		return ""
	}
	end := strings.IndexByte(rest, ')')
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

func webLayerListValue(values []string, index int) string {
	if len(values) == 0 || index < 0 {
		return ""
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return strings.TrimSpace(values[index])
}

func webSplitBackgroundSlash(value string) (string, string) {
	depth := 0
	var quote byte
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == '/' && depth == 0:
			return value[:i], value[i+1:]
		}
	}
	return value, ""
}

func webBackgroundPositionToken(token string) bool {
	lower := strings.ToLower(strings.TrimSpace(token))
	switch lower {
	case "left", "right", "top", "bottom", "center":
		return true
	case "", "auto", "scroll", "fixed", "local", "border-box", "padding-box", "content-box":
		return false
	}
	if _, ok := webPercentValue(lower); ok {
		return true
	}
	if webLength(lower, 16) != 0 || strings.HasPrefix(lower, "0") || strings.HasPrefix(lower, "calc(") || strings.HasPrefix(lower, "min(") || strings.HasPrefix(lower, "max(") {
		return true
	}
	return false
}

func webExtractBackgroundPosition(value string) string {
	var out []string
	for _, field := range webCSSSpaceFields(value) {
		if webBackgroundPositionToken(field) {
			out = append(out, field)
		}
	}
	if len(out) > 2 {
		out = out[:2]
	}
	return strings.Join(out, " ")
}

func webBackgroundShorthandLayer(segment string) webBackgroundLayerStyle {
	layer := webBackgroundLayerStyle{Repeat: "repeat"}
	if src := webBackgroundURL(segment); src != "" {
		layer.Image = src
	} else if g, valid := webParseGradient(segment); valid {
		ramp := g
		layer.Gradient, layer.HasGradient = &ramp, true
	}
	left, right := webSplitBackgroundSlash(segment)
	if right != "" {
		var size []string
		for _, field := range webCSSSpaceFields(right) {
			lower := strings.ToLower(strings.TrimSpace(field))
			if webIsBackgroundRepeat(lower) || lower == "scroll" || lower == "fixed" || lower == "local" ||
				lower == "border-box" || lower == "padding-box" || lower == "content-box" {
				continue
			}
			size = append(size, field)
		}
		layer.Size = strings.Join(size, " ")
	}
	layer.Position = webExtractBackgroundPosition(left)
	for _, field := range webCSSSpaceFields(segment) {
		if webIsBackgroundRepeat(field) {
			layer.Repeat = strings.ToLower(field)
			break
		}
	}
	return layer
}

// webBackgroundLayerRect resolves background-size against the block's paint
// box. A cover layer keeps the full box and lets object-fit crop the image;
// contain and explicit sizes produce a smaller layer, and background-position
// anchors that layer inside the box. Gradients have no intrinsic pixels, so
// they keep the box unless an explicit size was declared.
// Test seam (ARCH-8): only tests call it.
func webBackgroundLayerRect(box sdl.Rect, style webStyle, doc *webDocument, scale float64) (sdl.Rect, int) {
	layer := webBackgroundLayerStyle{Image: style.BackgroundImage, Size: style.BackgroundSize, Position: style.BackgroundPosition}
	if style.BackgroundLayerCount > 0 {
		layer = style.BackgroundLayers[0]
	}
	return webBackgroundLayerGeometry(box, layer, doc, scale)
}

func webBackgroundLayerGeometry(box sdl.Rect, layer webBackgroundLayerStyle, doc *webDocument, scale float64) (sdl.Rect, int) {
	if box.W <= 0 || box.H <= 0 {
		return box, webObjectFitFill
	}
	natW, natH := box.W, box.H
	if doc != nil && layer.Image != "" {
		if meta, ok := doc.imageMeta(layer.Image); ok && meta.HasSize {
			natW = int32(math.Round(float64(meta.W) * scale))
			natH = int32(math.Round(float64(meta.H) * scale))
		}
	}
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(layer.Size)))
	if len(fields) > 0 && fields[0] == "cover" {
		return box, webObjectFitCover
	}
	if len(fields) > 0 && fields[0] == "contain" {
		if natW > 0 && natH > 0 {
			return webObjectFitBoxAt(box, natW, natH, false, 0.5, 0.5), webObjectFitFill
		}
		return box, webObjectFitFill
	}
	w, h := natW, natH
	sized := false
	if len(fields) > 0 && fields[0] != "auto" {
		if pct, ok := webPercentValue(fields[0]); ok {
			w = int32(math.Round(float64(box.W) * pct / 100))
			sized = true
		} else if px := webLength(fields[0], 16); px > 0 {
			w = int32(math.Round(px * scale))
			sized = true
		}
	}
	if len(fields) > 1 {
		if pct, ok := webPercentValue(fields[1]); ok {
			h = int32(math.Round(float64(box.H) * pct / 100))
			sized = true
		} else if px := webLength(fields[1], 16); px > 0 {
			h = int32(math.Round(px * scale))
			sized = true
		}
	}
	if !sized {
		if layer.Image == "" || natW <= 0 || natH <= 0 {
			return box, webObjectFitFill
		}
	}
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	posX, posY := webObjectPositionFraction(layer.Position, box.W, box.H)
	return sdl.Rect{
		X: box.X + int32(math.Round(float64(box.W-w)*posX)),
		Y: box.Y + int32(math.Round(float64(box.H-h)*posY)),
		W: w, H: h,
	}, webObjectFitFill
}

const webMaxBackgroundTiles = 512

func webIsBackgroundRepeat(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "repeat", "no-repeat", "repeat-x", "repeat-y", "space", "round":
		return true
	}
	return false
}

type webRepeatMode int

const (
	webRepeatNone webRepeatMode = iota
	webRepeatTile
	webRepeatSpace
	webRepeatRound
)

func webRepeatModeFor(value string, horizontal bool) webRepeatMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "no-repeat":
		return webRepeatNone
	case "repeat-x":
		if horizontal {
			return webRepeatTile
		}
		return webRepeatNone
	case "repeat-y":
		if horizontal {
			return webRepeatNone
		}
		return webRepeatTile
	case "space":
		return webRepeatSpace
	case "round":
		return webRepeatRound
	}
	return webRepeatTile
}

func webBackgroundRepeatModes(value string) (webRepeatMode, webRepeatMode) {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(value)))
	if len(fields) == 0 {
		return webRepeatTile, webRepeatTile
	}
	if len(fields) == 1 {
		return webRepeatModeFor(fields[0], true), webRepeatModeFor(fields[0], false)
	}
	return webRepeatModeFor(fields[0], true), webRepeatModeFor(fields[1], false)
}

type webAxisTile struct {
	Pos  int32
	Size int32
}

func webRepeatAxis(origin, boxStart, boxSize, tileSize int32, mode webRepeatMode) []webAxisTile {
	if tileSize <= 0 || boxSize <= 0 {
		return nil
	}
	boxEnd := boxStart + boxSize
	switch mode {
	case webRepeatNone:
		return []webAxisTile{{Pos: origin, Size: tileSize}}
	case webRepeatSpace:
		count := int(boxSize / tileSize)
		if count < 1 {
			count = 1
		}
		if count == 1 {
			return []webAxisTile{{Pos: boxStart, Size: tileSize}}
		}
		gap := (boxSize - int32(count)*tileSize) / int32(count-1)
		out := make([]webAxisTile, 0, count)
		for i := 0; i < count; i++ {
			out = append(out, webAxisTile{Pos: boxStart + int32(i)*(tileSize+gap), Size: tileSize})
		}
		return out
	case webRepeatRound:
		count := int(math.Round(float64(boxSize) / float64(tileSize)))
		if count < 1 {
			count = 1
		}
		size := boxSize / int32(count)
		if size < 1 {
			size = 1
		}
		out := make([]webAxisTile, 0, count)
		for i := 0; i < count; i++ {
			pos := boxStart + int32(i)*size
			tileEnd := pos + size
			if i == count-1 {
				tileEnd = boxEnd
			}
			out = append(out, webAxisTile{Pos: pos, Size: tileEnd - pos})
		}
		return out
	}
	start := webTileStart(origin, boxStart, tileSize, true)
	out := make([]webAxisTile, 0, 8)
	for pos := start; pos < boxEnd && len(out) < webMaxBackgroundTiles; pos += tileSize {
		out = append(out, webAxisTile{Pos: pos, Size: tileSize})
	}
	return out
}

func webClipTile(tile, box sdl.Rect) (sdl.Rect, bool) {
	left, top := max32(tile.X, box.X), max32(tile.Y, box.Y)
	right := min32(tile.X+tile.W, box.X+box.W)
	bottom := min32(tile.Y+tile.H, box.Y+box.H)
	if right <= left || bottom <= top {
		return sdl.Rect{}, false
	}
	return sdl.Rect{X: left, Y: top, W: right - left, H: bottom - top}, true
}

func webTileStart(origin, boxStart, size int32, repeat bool) int32 {
	if !repeat || size <= 0 {
		return origin
	}
	delta := (origin - boxStart) % size
	if delta < 0 {
		delta += size
	}
	start := boxStart + delta
	if start > boxStart {
		start -= size
	}
	return start
}

// webBackgroundTiles repeats the positioned layer from its own origin, then
// clips each copy to the block's padding box. Space keeps the tile size and
// distributes equal gaps; round rescales each axis so an integer count fills
// the box. A single full-size layer (the common gradient case) yields exactly
// one tile, so tiling never changes the cover/contain or default full-box path.
func webBackgroundTiles(box, layer sdl.Rect, repeat string) []sdl.Rect {
	if box.W <= 0 || box.H <= 0 || layer.W <= 0 || layer.H <= 0 {
		return nil
	}
	modeX, modeY := webBackgroundRepeatModes(repeat)
	xs := webRepeatAxis(layer.X, box.X, box.W, layer.W, modeX)
	ys := webRepeatAxis(layer.Y, box.Y, box.H, layer.H, modeY)
	tiles := make([]sdl.Rect, 0, 8)
	for _, x := range xs {
		for _, y := range ys {
			if len(tiles) >= webMaxBackgroundTiles {
				return tiles
			}
			if tile, ok := webClipTile(sdl.Rect{X: x.Pos, Y: y.Pos, W: x.Size, H: y.Size}, box); ok {
				tiles = append(tiles, tile)
			}
		}
	}
	return tiles
}

func webDecorationStyleInValue(value string) int {
	lower := strings.ToLower(value)
	for _, field := range strings.FieldsFunc(lower, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ',' || r == '/'
	}) {
		switch field {
		case "double":
			return webDecorationDouble
		case "dotted":
			return webDecorationDotted
		case "dashed":
			return webDecorationDashed
		case "wavy":
			return webDecorationWavy
		case "solid":
			return webDecorationSolid
		}
	}
	return -1
}

func webVerticalAlignValue(value string) (int, float64) {
	lower := strings.ToLower(strings.TrimSpace(value))
	switch lower {
	case "middle":
		return webVerticalMiddle, 0
	case "top":
		return webVerticalTop, 0
	case "bottom":
		return webVerticalBottom, 0
	case "text-top":
		return webVerticalTextTop, 0
	case "text-bottom":
		return webVerticalTextBottom, 0
	case "super":
		return webVerticalSuper, 0
	case "sub":
		return webVerticalSub, 0
	case "baseline":
		return webVerticalBaseline, 0
	}
	if px := webLength(lower, 16); px != 0 {
		return webVerticalLength, px
	}
	return webVerticalBaseline, 0
}

func webVerticalOffset(style webStyle, font *ttf.Font, baseSize float64) int32 {
	if font == nil {
		return 0
	}
	// The raise or drop of a sub/super or relatively positioned inline box,
	// which its inline descendants share (webApplyBaselineShift).
	shift := int32(0)
	if baseSize > 0 {
		shift = int32(math.Round(style.BaselineShift * baseSize / 16))
	}
	h := int32(font.Height())
	if h < 1 {
		h = 1
	}
	switch style.VerticalAlign {
	case webVerticalTop:
		return shift - h/2
	case webVerticalBottom:
		return shift + h/2
	case webVerticalTextTop:
		return shift - h/4
	case webVerticalTextBottom:
		return shift + h/4
	case webVerticalLength:
		if baseSize > 0 {
			return shift - int32(math.Round(style.VerticalAlignPx*baseSize/16))
		}
	}
	return shift
}

func webOverflowValue(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "hidden":
		return 1
	case "clip":
		return 2
	}
	return 0
}

func webOverflowScrolls(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "auto", "scroll":
		return true
	}
	return false
}

func webPaintOpacity(style webStyle) float64 {
	if !style.HasOpacity {
		return 1
	}
	return webClampOpacity(style.Opacity)
}

func webScaleColorAlpha(color sdl.Color, opacity float64) sdl.Color {
	if opacity >= 0.999 {
		return color
	}
	if opacity <= 0 {
		color.A = 0
		return color
	}
	color.A = uint8(math.Round(float64(color.A) * opacity))
	return color
}

// webTransformWord applies text-transform. It runs before measurement so the
// wrapped line matches the glyphs that are drawn (uppercase is wider).
func webTransformWord(word string, mode int) string {
	switch mode {
	case 1:
		return strings.ToUpper(word)
	case 2:
		return strings.ToLower(word)
	case 3:
		runes := []rune(word)
		if len(runes) == 0 {
			return word
		}
		return strings.ToUpper(string(runes[0])) + string(runes[1:])
	}
	return word
}

// webFontShorthand reads the font shorthand's `[style] [variant] [weight]
// [stretch] size[/line-height] family` form. Only what the reader draws is
// applied: the family only chooses between the UI face and a monospace one
// (webApplyFontFamily), and a system font keyword (caption, menu, ...) carries
// no explicit size to read.
func webFontShorthand(st *webStyle, value string) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" || v == "inherit" || v == "initial" || v == "unset" {
		return
	}
	switch v {
	case "caption", "icon", "menu", "message-box", "small-caption", "status-bar":
		return
	}
	fields := strings.Fields(v)
	size := 0.0
	lineHeight := 0.0
	found := false
	sizeAt := -1
	for i, field := range fields {
		if found {
			break
		}
		sizeToken, lineToken := field, ""
		if i := strings.IndexByte(field, '/'); i >= 0 {
			sizeToken, lineToken = field[:i], field[i+1:]
		}
		if !webIsFontSizeToken(sizeToken) {
			continue
		}
		px, ok := webFontSizePx(sizeToken, st.FontPx)
		if !ok {
			continue
		}
		found = true
		sizeAt = i
		size = px
		if lineToken != "" {
			if lh, ok := webLineHeightPx(lineToken, px); ok {
				lineHeight = lh
			}
		}
	}
	if !found {
		// No size means this is not a font shorthand the reader understands;
		// guessing one would resize the whole page from a stray keyword.
		return
	}
	if size < 8 {
		size = 8
	}
	if size > 64 {
		size = 64
	}
	st.FontPx = size
	if lineHeight > 0 {
		st.LineHeight = lineHeight
	}
	// The family list follows the size (and a line-height written apart, as
	// in `16px / 1.5 monospace`).
	family := fields[sizeAt+1:]
	if !strings.Contains(fields[sizeAt], "/") && len(family) > 0 && strings.HasPrefix(family[0], "/") {
		if family[0] == "/" && len(family) > 1 {
			family = family[2:]
		} else {
			family = family[1:]
		}
	}
	if len(family) > 0 {
		webApplyFontFamily(st, strings.Join(family, " "))
	}
	for _, field := range fields {
		switch {
		case field == "italic", field == "oblique":
			st.Italic = true
		case field == "bold", field == "bolder":
			st.Bold = true
		case field == "lighter", field == "normal":
			// `normal` is the shorthand's reset for style and weight alike.
			if field == "lighter" {
				st.Bold = false
			}
		case len(field) == 3 && webAllDigits(field):
			if n, err := strconv.Atoi(field); err == nil {
				st.Bold = n >= 600
			}
		}
	}
}

// webIsFontSizeToken reports whether a token can be the size in a font
// shorthand. A bare number is a weight (`font: 700 16px Arial`), so it must not
// be read as a size.
func webIsFontSizeToken(token string) bool {
	t := strings.ToLower(strings.TrimSpace(token))
	if t == "" {
		return false
	}
	switch t {
	case "xx-small", "x-small", "small", "medium", "large", "x-large", "xx-large",
		"smaller", "larger", "small-caps":
		return true
	}
	for _, unit := range []string{"px", "em", "rem", "pt", "%", "ex", "ch", "vw", "vh"} {
		if strings.HasSuffix(t, unit) {
			return true
		}
	}
	return false
}

// webAllDigits reports whether every byte is an ASCII digit.
func webAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// webBorderWidthInValue reads the width token of a border shorthand: the first
// length in `1px solid #f00`, `solid 3px`, or a keyword. It returns 0 when the
// shorthand names no width at all (the caller keeps the reader's default).
func webBorderWidthInValue(value string, fontPx float64) float64 {
	for _, token := range strings.Fields(strings.ToLower(value)) {
		switch token {
		case "thin":
			return 1
		case "medium":
			return 3
		case "thick":
			return 5
		}
		// A colour and a border style are not lengths; webLength returns 0 for
		// both, so only a real measurement can win here.
		if px := webLength(token, fontPx); px > 0 {
			return px
		}
	}
	return 0
}

// webMaxAbs keeps the larger magnitude, so a later `border-left: 1px` does not
// shrink a card whose other edges were drawn 3px.
func webMaxAbs(a, b float64) float64 {
	if b > a {
		return b
	}
	return a
}

// webBorderVisible reports whether a border shorthand actually paints. Many
// sheets reset borders with `border: none` or `border-top: 0`, which must not
// grow a rule of its own.
func webBorderVisible(value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" || v == "0" || v == "none" || v == "hidden" {
		return false
	}
	if strings.Contains(v, "none") || strings.Contains(v, "hidden") {
		return false
	}
	return true
}

// webTransparentInValue reports a border or outline value whose colour is
// fully transparent.
func webTransparentInValue(value string) bool {
	c, ok := webColorInValueAny(value)
	return ok && c.A == 0
}

// webBorderPaints is webBorderVisible for a value with a colour to paint in:
// `border: 1px solid transparent` (Bootstrap's .btn and nav tabs) and
// `outline: 2px solid transparent` (Tailwind's outline-none) draw nothing.
// The reader's boxes do not reserve border room, so such a border is dropped.
func webBorderPaints(value string) bool {
	return webBorderVisible(value) && !webTransparentInValue(value)
}

// webResolveBorderColor lets a border-color that comes later in the cascade
// than a border shorthand replace the colour the shorthand carries, as CSS
// does: Bootstrap 4's `.btn { border: 1px solid transparent }` and
// `.btn-outline-primary { border-color: #007bff }` is a blue border. rank is
// each property's cascade position, as for webResolveBoxSides. A transparent
// border-color also takes the borders set by width alone off the page.
func webResolveBorderColor(merged map[string]string, rank map[string]int) {
	color, ok := merged["border-color"]
	if !ok {
		return
	}
	at := rank["border-color"]
	for _, key := range [...]string{"border", "border-left", "border-right", "border-top", "border-bottom"} {
		v, has := merged[key]
		if !has || rank[key] > at {
			continue
		}
		fields := webCSSSpaceFields(v)
		kept := make([]string, 0, len(fields)+1)
		for _, f := range fields {
			if _, isColor := webColorInValueAny(f); !isColor {
				kept = append(kept, f)
			}
		}
		merged[key] = strings.Join(append(kept, color), " ")
	}
	if webTransparentInValue(color) {
		for _, key := range [...]string{"border-width", "border-left-width", "border-right-width", "border-top-width", "border-bottom-width"} {
			delete(merged, key)
		}
	}
}

// webColorInValue finds the colour in a shorthand such as
// `background: #123 url(x.png) no-repeat` or `1px solid rgba(0,0,0,.2)`. The
// value is read in top-level tokens, so a colour function keeps its own spaces,
// commas and slash (rgb(0 0 0 / 50%), color-mix(in oklab, …)), and a colour
// inside a gradient or a url() is not taken for the box's own.
func webColorInValue(value string) (sdl.Color, bool) {
	return webColorInValueMode(value, false)
}

// webColorInValueAny is webColorInValue that also accepts the keyword
// transparent (alpha 0): a gradient stop, a background-color that removes a
// surface.
func webColorInValueAny(value string) (sdl.Color, bool) {
	return webColorInValueMode(value, true)
}

func webColorInValueMode(value string, transparent bool) (sdl.Color, bool) {
	parse := func(s string) (sdl.Color, bool) {
		if transparent && strings.EqualFold(strings.TrimSpace(s), "transparent") {
			return sdl.Color{}, true
		}
		return parseWebColor(s)
	}
	if c, ok := parse(value); ok {
		return c, true
	}
	for _, part := range webSplitTopLevel(value, ',') {
		for _, field := range webCSSSpaceFields(part) {
			for _, piece := range webSplitTopLevel(field, '/') {
				if c, ok := parse(piece); ok {
					return c, true
				}
			}
		}
	}
	return sdl.Color{}, false
}

// --- Style resolution -----------------------------------------------------

func webBaseStyle() webStyle {
	return webStyle{FontPx: 16, FlexShrink: 1, MinWidthAuto: true}
}

// applyWebTagDefaults gives the reader a browser's baseline: headings scale
// from their parent's size (h1 2em … h6 0.67em) with Chrome's em margins,
// paragraphs are spaced, lists are indented, <sup>/<sub> are smaller and
// raised or dropped, table headers are bold and centred, and code is
// monospaced (when the machine has a monospace face, webpage_fonts.go) on a
// tinted panel. st arrives with the inherited font size, so every relative
// size here is relative to the parent, as in CSS.
func applyWebTagDefaults(st *webStyle, tag string) {
	heading := func(scale, margin float64) {
		st.FontPx, st.Bold = math.Min(st.FontPx*scale, 64), true
		st.MarginTop, st.MarginBot = st.FontPx*margin, st.FontPx*margin
	}
	// A monospace face reads larger than the text face at the same size, so
	// browsers set code at 13px against 16px text — once: the <code> inside a
	// <pre> keeps the <pre>'s size.
	monoScale := 13.0 / 16.0
	if st.Mono {
		monoScale = 1
	}
	switch tag {
	case "h1":
		heading(2, 0.67)
	case "h2":
		heading(1.5, 0.83)
	case "h3":
		heading(1.17, 1)
	case "h4":
		heading(1, 1.33)
	case "h5":
		heading(0.83, 1.67)
	case "h6":
		heading(0.67, 2.33)
	case "p":
		// Browsers give a paragraph one em above and below; without it the
		// rows of two paragraphs merge into one undifferentiated wall.
		st.MarginTop, st.MarginBot = st.FontPx, st.FontPx
	case "blockquote":
		// A browser indents a quote by 40px on both sides and draws no rule;
		// a sheet that wants a bar says so (and can now also say none).
		st.Indent += 40
		st.IndentRight += 40
		st.MarginTop, st.MarginBot = st.FontPx, st.FontPx
	case "figure":
		st.Indent += 40
		st.IndentRight += 40
		st.MarginTop, st.MarginBot = st.FontPx, st.FontPx
	case "dd":
		st.Indent += 40
	case "pre", "listing":
		// A browser's <pre> and <code> are monospace and nothing more: the
		// grey panel the reader used to put behind them was its own, and a
		// page that wants one says so.
		st.Pre = true
		st.WhiteSpace = webWhiteSpacePre
		st.MarginTop, st.MarginBot = st.FontPx, st.FontPx
		st.FontPx *= monoScale
		st.Mono, st.IconFont, st.WebFont = true, false, 0
	case "code", "kbd", "samp":
		st.FontPx *= monoScale
		st.Mono, st.IconFont, st.WebFont = true, false, 0
	case "tt":
		st.FontPx *= monoScale
		st.Mono, st.IconFont, st.WebFont = true, false, 0
	case "sup", "sub":
		// font-size: smaller, vertical-align: super/sub. The raise itself is
		// webApplyBaselineShift's, against the parent's size.
		st.FontPx /= 1.2
		st.VerticalAlign = webVerticalSuper
		if tag == "sub" {
			st.VerticalAlign = webVerticalSub
		}
	case "big":
		st.FontPx = math.Min(st.FontPx*1.2, 64)
	case "nobr":
		st.WhiteSpace = webWhiteSpaceNowrap
	case "mat-icon":
		// Angular Material's icon element: its text is a ligature name.
		st.IconFont, st.Mono, st.WebFont = true, false, 0
	case "li":
		st.Indent += 18
		st.ListItem = true
		st.MarginTop, st.MarginBot = 2, 2
	case "a":
		st.Underline = true
		st.Link = true
	case "strong", "b":
		st.Bold = true
	case "em", "i", "cite", "dfn", "var", "address":
		st.Italic = true
	case "s", "del", "strike":
		st.Strike = true
	case "u", "ins":
		st.Underline = true
	case "center":
		// text-align: -webkit-center: the text and the boxes inside centre.
		st.Align, st.CenterBlocks = 1, true
	case "dt":
		// Definition terms are bold in every browser.
		st.Bold = true
	case "mark":
		// mark { background-color: Mark; color: MarkText }: yellow and black.
		st.HasBg = true
		st.Background = sdl.Color{R: 255, G: 255, A: 255}
		st.Color, st.HasColor = sdl.Color{A: 255}, true
	case "small":
		st.FontPx /= 1.2
	case "td", "th":
		st.Indent += 4
		if tag == "th" {
			// Header cells are bold and centred in every browser.
			st.Bold, st.Align, st.CenterBlocks = true, 1, false
		}
	case "hr":
		// hr { margin: 0.5em auto }: a rule narrower than its column (a
		// width attribute or rule) sits in the middle.
		st.MarginLeftAuto, st.MarginRightAuto = true, true
	case "tr":
		// A table row is a real row: its direct cell blocks are flex items, so
		// cells occupy columns instead of being folded into one text run.
		st.Display, st.FlexDir = webDisplayFlex, webFlexRow
		st.AlignItems = webItemStretch
	case "table":
		st.MarginTop, st.MarginBot = 8, 8
	case "caption":
		st.Align = 1
		st.MarginBot = 4
	}
}

// webLogicalBorderDecls maps the logical border properties onto the physical
// sides this reader draws. On a horizontal, left-to-right page
// border-inline-start is the left edge and border-block-end the bottom one, and
// the pages that use the logical names (RTL-aware design systems, Tailwind's
// logical utilities) expect the same box as a page naming border-left.
// A physical declaration the page also states wins, and a side declaration
// beats the axis shorthand that covers it.
func webLogicalBorderDecls(decls map[string]string) map[string]string {
	type logicalBorder struct {
		logical  string
		physical [2]string // inline/block start first; a single side for a longhand
	}
	// Least to most specific: an axis shorthand, its two sides, then widths
	// and radii, so a more specific logical name replaces a broader one.
	logical := [...]logicalBorder{
		{"border-inline", [2]string{"border-left", "border-right"}},
		{"border-block", [2]string{"border-top", "border-bottom"}},
		{"border-inline-start", [2]string{"border-left", ""}},
		{"border-inline-end", [2]string{"border-right", ""}},
		{"border-block-start", [2]string{"border-top", ""}},
		{"border-block-end", [2]string{"border-bottom", ""}},
		{"border-inline-width", [2]string{"border-left-width", "border-right-width"}},
		{"border-block-width", [2]string{"border-top-width", "border-bottom-width"}},
		{"border-inline-start-width", [2]string{"border-left-width", ""}},
		{"border-inline-end-width", [2]string{"border-right-width", ""}},
		{"border-block-start-width", [2]string{"border-top-width", ""}},
		{"border-block-end-width", [2]string{"border-bottom-width", ""}},
		{"border-start-start-radius", [2]string{"border-top-left-radius", ""}},
		{"border-start-end-radius", [2]string{"border-top-right-radius", ""}},
		{"border-end-start-radius", [2]string{"border-bottom-left-radius", ""}},
		{"border-end-end-radius", [2]string{"border-bottom-right-radius", ""}},
	}
	var out map[string]string
	set := func(name, value string) {
		if name == "" {
			return
		}
		if _, physical := decls[name]; physical {
			// The page named the physical side too: it wins, as the later
			// declaration of the same property would in a browser.
			return
		}
		if out == nil {
			out = make(map[string]string, len(decls)+4)
			for key, value := range decls {
				out[key] = value
			}
		}
		out[name] = value
	}
	for _, entry := range logical {
		value, ok := decls[entry.logical]
		if !ok {
			continue
		}
		first, second := value, value
		if entry.physical[1] != "" && strings.Contains(entry.logical, "width") {
			// An axis width shorthand takes one value for both sides or two in
			// inline/block start-then-end order.
			if fields := webCSSSpaceFields(value); len(fields) == 2 {
				first, second = fields[0], fields[1]
			}
		}
		set(entry.physical[0], first)
		set(entry.physical[1], second)
	}
	if out == nil {
		return decls
	}
	return out
}

func applyWebDecls(st *webStyle, decls map[string]string) {
	if len(decls) == 0 {
		return
	}
	// Logical border shorthands are physical sides here; rewrite them before
	// the cascade below reads any edge.
	decls = webLogicalBorderDecls(decls)
	// The `font` shorthand comes before the longhands, so a sheet that sets
	// both ends up with the longhand, the way a later declaration wins in a
	// browser. It carries the size (and therefore every em length below), the
	// weight, the style and the line-height; a reset written as
	// `font: 16px/1.6 system-ui` used to leave the whole page at the reader's
	// default size.
	if v, ok := decls["font"]; ok {
		webFontShorthand(st, v)
	}
	// font-size next: em-based lengths below resolve against it.
	if v, ok := decls["font-size"]; ok {
		if px, valid := webFontSizePx(v, st.FontPx); valid {
			if px < 8 {
				px = 8
			}
			if px > 64 {
				px = 64
			}
			st.FontPx = px
		}
	}
	if v, ok := decls["color"]; ok {
		switch lower := strings.ToLower(strings.TrimSpace(v)); {
		case lower == "transparent":
			// Text that keeps its place and paints nothing (an icon button's
			// label, a skeleton line): the layout makes such runs invisible.
			st.Color, st.HasColor = sdl.Color{}, true
		case lower == "initial":
			st.Color, st.HasColor = sdl.Color{A: 255}, true
		case webInheritKeyword(lower) || lower == "currentcolor":
			// The colour inherits (computeStyle restores the parent's for a
			// link); what the keyword changes is that a link no longer paints
			// in link blue. Tailwind's reset is `a { color: inherit }`.
			st.Link = false
		default:
			if c, valid := parseWebColor(v); valid {
				st.Color, st.HasColor = c, true
			}
		}
	}
	// background-color decides the surface when the element states one, the
	// background shorthand otherwise — and a shorthand without a colour
	// (`background: url(x)`, `none`) resets it to transparent, as a browser's
	// does. A colour with alpha 0 is no surface at all: it used to count as an
	// opaque black backdrop and turned dark text pale.
	bgDecided := false
	if v, ok := decls["background-color"]; ok {
		if c, valid := webColorInValueAny(v); valid {
			webSetBackground(st, c)
			bgDecided = true
		}
	}
	if v, ok := decls["background"]; ok && !bgDecided {
		if c, valid := webColorInValueAny(v); valid {
			webSetBackground(st, c)
		} else if webBackgroundShorthandClears(v) {
			st.Background, st.HasBg = sdl.Color{}, false
		}
	}
	// Resolve the comma-separated background layer list. Each layer keeps its
	// own image/gradient, size, position and repeat; the longhand lists repeat
	// their last value when they are shorter than the image list, as CSS does.
	_, hasBGImage := decls["background-image"]
	_, hasBGShorthand := decls["background"]
	_, hasBGSize := decls["background-size"]
	_, hasBGPosition := decls["background-position"]
	_, hasBGRepeat := decls["background-repeat"]
	backgroundDeclared := hasBGImage || hasBGShorthand || hasBGSize || hasBGPosition || hasBGRepeat
	var backgroundLayers []webBackgroundLayerStyle
	if value, ok := decls["background-image"]; ok {
		for _, part := range webSplitSelectorList(value) {
			layer := webBackgroundLayerStyle{Repeat: "repeat"}
			if src := webBackgroundURL(part); src != "" {
				layer.Image = src
			} else if g, valid := webParseGradient(part); valid {
				ramp := g
				layer.Gradient, layer.HasGradient = &ramp, true
			} else if !st.HasBg {
				if c, valid := webGradientColor(part); valid {
					st.Background, st.HasBg = c, true
				}
			}
			backgroundLayers = append(backgroundLayers, layer)
		}
	} else if value, ok := decls["background"]; ok {
		for _, part := range webSplitSelectorList(value) {
			backgroundLayers = append(backgroundLayers, webBackgroundShorthandLayer(part))
		}
	}
	if sizeList, ok := decls["background-size"]; ok {
		sizes := webSplitSelectorList(sizeList)
		for i := range backgroundLayers {
			backgroundLayers[i].Size = webLayerListValue(sizes, i)
		}
	}
	if positionList, ok := decls["background-position"]; ok {
		positions := webSplitSelectorList(positionList)
		for i := range backgroundLayers {
			backgroundLayers[i].Position = webLayerListValue(positions, i)
		}
	}
	if repeatList, ok := decls["background-repeat"]; ok {
		repeats := webSplitSelectorList(repeatList)
		for i := range backgroundLayers {
			backgroundLayers[i].Repeat = webLayerListValue(repeats, i)
		}
	}
	if len(backgroundLayers) > webMaxBackgroundLayers {
		backgroundLayers = backgroundLayers[:webMaxBackgroundLayers]
	}
	if backgroundDeclared {
		st.BackgroundLayerCount = len(backgroundLayers)
		copy(st.BackgroundLayers[:], backgroundLayers)
		if len(backgroundLayers) > 0 {
			first := backgroundLayers[0]
			st.BackgroundImage, st.BackgroundSize, st.BackgroundPosition, st.BackgroundRepeat = first.Image, first.Size, first.Position, first.Repeat
			if first.HasGradient && first.Gradient != nil {
				st.Gradient, st.HasGradient = first.Gradient, true
				st.Background, st.HasBg = webGradientAverage(*first.Gradient), true
			}
		} else {
			st.BackgroundImage, st.BackgroundSize, st.BackgroundPosition, st.BackgroundRepeat = "", "", "", ""
		}
	}
	if v, ok := decls["font-weight"]; ok {
		switch weight := strings.ToLower(strings.TrimSpace(v)); weight {
		case "bold", "bolder":
			st.Bold = true
		case "normal", "lighter", "initial":
			st.Bold = false
		default:
			// Any numeric weight, variable-font steps (650, 750) included: the
			// reader has a regular and a bold cut, and bold starts at 600.
			if n, err := strconv.ParseFloat(weight, 64); err == nil && n >= 1 && n <= 1000 {
				st.Bold = n >= 600
			}
		}
	}
	if v, ok := decls["font-style"]; ok {
		lower := strings.ToLower(strings.TrimSpace(v))
		st.Italic = strings.HasPrefix(lower, "italic") || strings.HasPrefix(lower, "oblique")
	}
	// After the font shorthand, which may have named a family too: the
	// longhand wins, as a later declaration does in a browser.
	if v, ok := decls["font-family"]; ok {
		webApplyFontFamily(st, v)
	}
	for _, key := range []string{"text-decoration", "text-decoration-line"} {
		if v, ok := decls[key]; ok {
			lower := strings.ToLower(v)
			st.Underline = strings.Contains(lower, "underline")
			st.Strike = strings.Contains(lower, "line-through")
			if style := webDecorationStyleInValue(v); style >= 0 {
				st.DecorationStyle = style
			}
			if c, valid := webColorInValue(v); valid {
				st.DecorationColor, st.HasDecorationColor = c, true
			}
			break
		}
	}
	if v, ok := decls["text-decoration-color"]; ok {
		if c, valid := webColorInValue(v); valid {
			st.DecorationColor, st.HasDecorationColor = c, true
		}
	}
	if v, ok := decls["text-decoration-thickness"]; ok {
		if px := webLength(v, st.FontPx); px > 0 {
			st.DecorationThickness = px
		}
	}
	if v, ok := decls["text-decoration-style"]; ok {
		if style := webDecorationStyleInValue(v); style >= 0 {
			st.DecorationStyle = style
		}
	}
	// text-underline-offset: modern design systems lift the rule clear of a
	// descender or tuck a dotted link underline under its text. `auto` and the
	// CSS-wide keywords leave the reader's own placement, as a browser does.
	if v, ok := decls["text-underline-offset"]; ok {
		if lower := strings.ToLower(strings.TrimSpace(v)); lower != "auto" && !webInheritKeyword(lower) {
			if px := webSpacingPx(v, st.FontPx); px != 0 {
				st.DecorationOffset = px
			}
		}
	}
	if v, ok := decls["text-shadow"]; ok {
		if s, valid := webFirstTextShadow(v, st.FontPx); valid {
			st.HasTextShadow, st.TextShadowX, st.TextShadowY = true, s.X, s.Y
			st.TextShadowBlur = s.Blur
			st.TextShadowColor, st.HasTextShadowColor = sdl.Color{}, false
			if s.HasColor {
				st.TextShadowColor, st.HasTextShadowColor = webTextShadowSoften(s.Color, s.Blur), true
			}
		} else if !webInheritKeyword(v) {
			// none, or a shadow that paints nothing: the inherited one stops here.
			st.HasTextShadow, st.TextShadowX, st.TextShadowY, st.TextShadowBlur = false, 0, 0, 0
			st.TextShadowColor, st.HasTextShadowColor = sdl.Color{}, false
		}
	}
	// The first visible outer layer of a shadow list lifts the box. An inset
	// layer (painted inside the box, not modelled) or a transparent framework
	// placeholder ahead of it no longer drops the whole declaration.
	if v, ok := decls["box-shadow"]; ok {
		if s, valid := webFirstOuterShadow(v, st.FontPx); valid {
			st.HasBoxShadow, st.BoxShadowX, st.BoxShadowY = true, s.X, s.Y
			st.BoxShadowBlur, st.BoxShadowSpread = s.Blur, s.Spread
			if s.HasColor {
				st.BoxShadowColor, st.HasBoxShadowColor = s.Color, true
			}
		}
	}
	if v, ok := decls["transform"]; ok {
		st.HasTranslate = false
		st.TranslateX, st.TranslateY, st.TranslatePctX, st.TranslatePctY = 0, 0, 0, 0
		if x, y, pctX, pctY, valid := webTranslateValue(v, st.FontPx); valid {
			st.HasTranslate = true
			st.TranslateX, st.TranslateY, st.TranslatePctX, st.TranslatePctY = x, y, pctX, pctY
		}
		// Any transform starts a stacking context, a rotation as much as a
		// translation (webStackingContext).
		if webStackingProperty("transform", v) {
			st.Isolate = true
		}
	}
	// So do these, though the reader draws none of their effects: Tailwind's
	// `isolate`, a filter or backdrop blur, a blend mode, will-change,
	// contain, perspective, a clip path or a mask (webpage_layers.go).
	for _, key := range [...]string{
		"isolation", "filter", "backdrop-filter", "-webkit-backdrop-filter", "mix-blend-mode", "will-change",
		"contain", "perspective", "clip-path", "-webkit-clip-path", "mask", "mask-image", "-webkit-mask-image",
	} {
		if v, ok := decls[key]; ok && webStackingProperty(key, v) {
			st.Isolate = true
		}
	}
	if v, ok := decls["text-transform"]; ok {
		st.Transform = webTextTransform(v)
	}
	if v, ok := decls["text-align"]; ok {
		switch lower := strings.ToLower(strings.TrimSpace(v)); lower {
		case "inherit", "unset", "revert", "revert-layer", "match-parent":
			// text-align inherits: these keep the parent's alignment.
		default:
			st.Align = webTextAlign(lower)
			st.CenterBlocks = lower == "-webkit-center" || lower == "-moz-center" || lower == "-khtml-center"
		}
	}
	if v, ok := decls["vertical-align"]; ok {
		st.VerticalAlign, st.VerticalAlignPx = webVerticalAlignValue(v)
	}
	if v, ok := decls["overflow"]; ok {
		fields := strings.Fields(v)
		if len(fields) > 0 {
			st.OverflowX = webOverflowValue(fields[0])
			st.ScrollX = webOverflowScrolls(fields[0])
			st.OverflowY = st.OverflowX
		}
		if len(fields) > 1 {
			st.OverflowY = webOverflowValue(fields[1])
		}
		for _, f := range fields {
			st.OverflowBFC = st.OverflowBFC || webOverflowHoldsFloats(f)
		}
	}
	if v, ok := decls["overflow-x"]; ok {
		st.OverflowX = webOverflowValue(v)
		st.ScrollX = webOverflowScrolls(v)
		st.OverflowBFC = st.OverflowBFC || webOverflowHoldsFloats(v)
	}
	if v, ok := decls["overflow-y"]; ok {
		st.OverflowY = webOverflowValue(v)
		st.OverflowBFC = st.OverflowBFC || webOverflowHoldsFloats(v)
	}
	if v, ok := decls["text-overflow"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "clip":
			st.TextOverflow = 1
		case "ellipsis":
			st.TextOverflow = 2
		}
	}
	if v, ok := decls["text-indent"]; ok {
		st.TextIndent = webLength(v, st.FontPx)
	}
	if v, ok := decls["white-space"]; ok {
		st.WhiteSpace = webWhiteSpaceValue(v)
		st.Pre = webWhiteSpacePreservesSpaces(st.WhiteSpace)
	}
	if v, ok := decls["word-wrap"]; ok {
		st.OverflowWrap = webOverflowWrapValue(v)
	}
	if v, ok := decls["overflow-wrap"]; ok {
		st.OverflowWrap = webOverflowWrapValue(v)
	}
	if v, ok := decls["word-break"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "break-all":
			st.WordBreak = 1
		case "break-word":
			st.WordBreak, st.OverflowWrap = 0, 1
		case "normal", "keep-all":
			st.WordBreak = 0
		}
	}
	if v, ok := decls["display"]; ok {
		if strings.EqualFold(strings.TrimSpace(v), "none") {
			st.Hidden = true
		}
	}
	if v, ok := decls["visibility"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "hidden", "collapse":
			st.Invisible = true
		case "visible":
			st.Invisible = false
		}
	}
	// The content of a generated box is resolved when that box is created,
	// because attr() needs the originating element's attributes. Keeping the
	// raw value here preserves the cascade (and var() substitution) first.
	if v, ok := decls["content"]; ok {
		st.Content = v
	}
	for name, target := range map[string]*string{
		"counter-reset":     &st.CounterReset,
		"counter-increment": &st.CounterIncrement,
		"counter-set":       &st.CounterSet,
	} {
		if v, ok := decls[name]; ok {
			*target = v
		}
	}
	if v, ok := decls["margin"]; ok {
		if top, right, bottom, left, valid := webBoxShorthand(v, st.FontPx); valid {
			st.MarginTop, st.MarginBot = top, bottom
			st.Indent += left
			st.IndentRight += right
			st.MarginLeftAuto, st.MarginRightAuto = webAutoMarginSides(v)
			st.MarginTopAuto, st.MarginBotAuto = webAutoBlockSides(v)
		} else {
			top, bottom := webMarginPair(v, st.FontPx)
			st.MarginTop, st.MarginBot = top, bottom
		}
	}
	if v, ok := decls["margin-top"]; ok {
		st.MarginTop = webLength(v, st.FontPx)
		st.MarginTopAuto = webCSSAuto(v)
	}
	if v, ok := decls["margin-bottom"]; ok {
		st.MarginBot = webLength(v, st.FontPx)
		st.MarginBotAuto = webCSSAuto(v)
	}
	if v, ok := decls["padding-left"]; ok {
		px := webLength(v, st.FontPx)
		st.Indent += px
		st.PadLeft += px
	}
	// A block's own top and bottom padding is breathing room, exactly like a
	// margin: without it a card written as `padding-top: 24px` sat flush
	// against the paragraph above it.
	if v, ok := decls["padding-top"]; ok {
		px := webLength(v, st.FontPx)
		st.MarginTop += px
		st.PadTop += px
	}
	if v, ok := decls["padding-bottom"]; ok {
		px := webLength(v, st.FontPx)
		st.MarginBot += px
		st.PadBot += px
	}
	// The logical shorthands every modern sheet uses. The reader is a
	// left-to-right column, so inline = left/right and block = top/bottom;
	// supporting them keeps a spacing writer's intent instead of dropping it.
	if v, ok := decls["margin-block"]; ok {
		if top, _, bottom, _, valid := webBoxShorthand(v, st.FontPx); valid {
			st.MarginTop += top
			st.MarginBot += bottom
			st.MarginTopAuto, st.MarginBotAuto = webAutoBlockLogicalSides(v)
		}
	}
	if v, ok := decls["padding-block"]; ok {
		if top, _, bottom, _, valid := webBoxShorthand(v, st.FontPx); valid {
			st.MarginTop += top
			st.MarginBot += bottom
			st.PadTop += top
			st.PadBot += bottom
		}
	}
	for _, key := range []string{"margin-inline", "padding-inline"} {
		if v, ok := decls[key]; ok {
			if _, right, _, left, valid := webBoxShorthand(v, st.FontPx); valid {
				st.Indent += left
				st.IndentRight += right
				if key == "margin-inline" {
					st.MarginLeftAuto, st.MarginRightAuto = webAutoInlineMarginSides(v)
				} else {
					st.PadLeft += left
					st.PadRight += right
				}
			}
		}
	}
	for key, target := range map[string]*float64{
		"margin-inline-start":  &st.Indent,
		"padding-inline-start": &st.Indent,
		"margin-inline-end":    &st.IndentRight,
		"padding-inline-end":   &st.IndentRight,
		"margin-block-start":   &st.MarginTop,
		"padding-block-start":  &st.MarginTop,
		"margin-block-end":     &st.MarginBot,
		"padding-block-end":    &st.MarginBot,
	} {
		if v, ok := decls[key]; ok {
			px := webLength(v, st.FontPx)
			*target += px
			switch key {
			case "padding-block-start":
				st.PadTop += px
			case "padding-block-end":
				st.PadBot += px
			case "padding-inline-start":
				st.PadLeft += px
			case "padding-inline-end":
				st.PadRight += px
			}
		}
	}
	if v, ok := decls["margin-inline-start"]; ok {
		st.MarginLeftAuto = webCSSAuto(v)
	}
	if v, ok := decls["margin-inline-end"]; ok {
		st.MarginRightAuto = webCSSAuto(v)
	}
	if v, ok := decls["margin-block-start"]; ok {
		st.MarginTopAuto = webCSSAuto(v)
	}
	if v, ok := decls["margin-block-end"]; ok {
		st.MarginBotAuto = webCSSAuto(v)
	}
	if v, ok := decls["padding"]; ok {
		if top, right, bottom, left, valid := webBoxShorthand(v, st.FontPx); valid {
			st.Indent += left
			st.IndentRight += right
			st.MarginTop += top
			st.MarginBot += bottom
			st.PadTop += top
			st.PadBot += bottom
			st.PadLeft += left
			st.PadRight += right
		}
	}
	if v, ok := decls["padding-right"]; ok {
		px := webLength(v, st.FontPx)
		st.IndentRight += px
		st.PadRight += px
	}
	// margin-left/margin-right are the box the paragraph sits in: a 24px
	// gutter narrows the line on that side. `auto` is kept as a flag for the
	// layout pass, which resolves it once the used width is known.
	if v, ok := decls["margin-left"]; ok {
		st.Indent += webLength(v, st.FontPx)
		st.MarginLeftAuto = webCSSAuto(v)
	}
	if v, ok := decls["margin-right"]; ok {
		st.IndentRight += webLength(v, st.FontPx)
		st.MarginRightAuto = webCSSAuto(v)
	}
	if v, ok := decls["line-height"]; ok {
		if px, valid := webLineHeightPx(v, st.FontPx); valid {
			st.LineHeight = px
		}
	}
	if v, ok := decls["letter-spacing"]; ok {
		st.LetterSpacing = webSpacingPx(v, st.FontPx)
	}
	if v, ok := decls["word-spacing"]; ok {
		st.WordSpacing = webSpacingPx(v, st.FontPx)
	}
	if v, ok := decls["border-radius"]; ok {
		st.Radius = webFirstLength(v, st.FontPx)
	}
	for _, key := range []string{"border-top-left-radius", "border-top-right-radius", "border-bottom-left-radius", "border-bottom-right-radius"} {
		if v, ok := decls[key]; ok {
			if px := webFirstLength(v, st.FontPx); px > st.Radius {
				st.Radius = px
			}
		}
	}
	if v, ok := decls["opacity"]; ok {
		if alpha, valid := webOpacityValue(v); valid {
			st.HasOpacity, st.Opacity = true, alpha
		}
	}
	if v, ok := decls["border-color"]; ok {
		if c, valid := webColorInValue(v); valid {
			st.BorderColor, st.HasBorderColor = c, true
		}
	}
	if !st.HasBorderColor && st.HasColor {
		// Browsers draw an uncoloured border in currentColor, which is what
		// `border-top: 1px solid` on a nav or card expects.
		st.BorderColor, st.HasBorderColor = st.Color, true
	}
	// A border shorthand carries its own colour (`border: 1px solid #f00`),
	// which is the only place most sheets set one.
	for _, key := range []string{"border", "border-left", "border-right", "border-top", "border-bottom", "outline"} {
		if v, ok := decls[key]; ok {
			sides := []int{0, 1, 2, 3}
			switch key {
			case "border-left":
				sides = []int{0}
			case "border-right":
				sides = []int{1}
			case "border-top":
				sides = []int{2}
			case "border-bottom":
				sides = []int{3}
			case "outline":
				sides = nil
			}
			if c, valid := webColorInValue(v); valid {
				st.BorderColor, st.HasBorderColor = c, true
				for _, side := range sides {
					st.BorderSideColor[side], st.BorderSideHasColor[side] = c, true
				}
			}
			if style := webBorderStyleInValue(v); style >= 0 {
				st.BorderStyle = style
				for _, side := range sides {
					st.BorderSideStyle[side], st.BorderSideStyleSet[side] = style, true
				}
			}
		}
	}
	if v, ok := decls["outline-color"]; ok {
		if c, valid := webColorInValue(v); valid {
			st.BorderColor, st.HasBorderColor = c, true
		}
	}
	// Each edge maps to its own side: `border-right` used to set the left
	// flag, so a sidebar divider drew on the wrong edge of the box.
	if v, ok := decls["border-left"]; ok && webBorderPaints(v) {
		st.BorderLeft = true
		st.BorderWidth = webMaxAbs(st.BorderWidth, webBorderWidthInValue(v, st.FontPx))
		st.BorderSideWidth[0] = webMaxAbs(st.BorderSideWidth[0], webBorderWidthInValue(v, st.FontPx))
	}
	if v, ok := decls["border-right"]; ok && webBorderPaints(v) {
		st.BorderRight = true
		st.BorderWidth = webMaxAbs(st.BorderWidth, webBorderWidthInValue(v, st.FontPx))
		st.BorderSideWidth[1] = webMaxAbs(st.BorderSideWidth[1], webBorderWidthInValue(v, st.FontPx))
	}
	if v, ok := decls["border-top"]; ok && webBorderPaints(v) {
		st.BorderTop = true
		st.BorderWidth = webMaxAbs(st.BorderWidth, webBorderWidthInValue(v, st.FontPx))
		st.BorderSideWidth[2] = webMaxAbs(st.BorderSideWidth[2], webBorderWidthInValue(v, st.FontPx))
	}
	if v, ok := decls["border-bottom"]; ok && webBorderPaints(v) {
		st.BorderBottom = true
		st.BorderWidth = webMaxAbs(st.BorderWidth, webBorderWidthInValue(v, st.FontPx))
		st.BorderSideWidth[3] = webMaxAbs(st.BorderSideWidth[3], webBorderWidthInValue(v, st.FontPx))
	}
	for key, target := range map[string]*bool{
		"border-left-width":   &st.BorderLeft,
		"border-right-width":  &st.BorderRight,
		"border-top-width":    &st.BorderTop,
		"border-bottom-width": &st.BorderBottom,
	} {
		if v, ok := decls[key]; ok {
			if px := webLength(v, st.FontPx); px > 0 {
				*target = true
				st.BorderWidth = webMaxAbs(st.BorderWidth, px)
				side := 0
				switch key {
				case "border-right-width":
					side = 1
				case "border-top-width":
					side = 2
				case "border-bottom-width":
					side = 3
				}
				st.BorderSideWidth[side] = webMaxAbs(st.BorderSideWidth[side], px)
			}
		}
	}
	if v, ok := decls["border"]; ok && webBorderPaints(v) {
		st.BoxBorder = true
		st.BorderWidth = webMaxAbs(st.BorderWidth, webBorderWidthInValue(v, st.FontPx))
		w := webBorderWidthInValue(v, st.FontPx)
		for i := range st.BorderSideWidth {
			st.BorderSideWidth[i] = webMaxAbs(st.BorderSideWidth[i], w)
		}
	}
	if v, ok := decls["border-width"]; ok {
		if px := webLength(v, st.FontPx); px > 0 {
			st.BoxBorder = true
			st.BorderWidth = webMaxAbs(st.BorderWidth, px)
			for i := range st.BorderSideWidth {
				st.BorderSideWidth[i] = webMaxAbs(st.BorderSideWidth[i], px)
			}
		}
	}
	if v, ok := decls["border-style"]; ok {
		if style := webBorderStyleInValue(v); style >= 0 {
			st.BorderStyle = style
		}
	}
	// Outline is its own box edge: keeping it separate from the border lets
	// outline-offset move the ring without moving the card's border.
	if v, ok := decls["outline"]; ok && webBorderPaints(v) {
		st.Outline = true
		st.BorderWidth = webMaxAbs(st.BorderWidth, webBorderWidthInValue(v, st.FontPx))
	}
	if v, ok := decls["outline-width"]; ok {
		if px := webLength(v, st.FontPx); px > 0 {
			st.Outline = true
			st.BorderWidth = webMaxAbs(st.BorderWidth, px)
		}
	}
	if v, ok := decls["outline-offset"]; ok {
		st.OutlineOffset = webLength(v, st.FontPx)
	}
	if v, ok := decls["outline-color"]; ok && webTransparentInValue(v) {
		// Tailwind's outline-none: a transparent outline draws nothing.
		st.Outline = false
	}
	for _, key := range [...]string{"font-variant-caps", "font-variant"} {
		v, ok := decls[key]
		if !ok || webInheritKeyword(v) {
			continue
		}
		lower := strings.ToLower(v)
		st.SmallCaps = strings.Contains(lower, "small-caps") || strings.Contains(lower, "petite-caps") ||
			strings.Contains(lower, "unicase") || strings.Contains(lower, "titling-caps")
		break
	}
	if v, ok := decls["list-style-position"]; ok {
		st.ListInside = strings.EqualFold(strings.TrimSpace(v), "inside")
	} else if v, ok := decls["list-style"]; ok {
		for _, f := range strings.Fields(strings.ToLower(v)) {
			switch f {
			case "inside":
				st.ListInside = true
			case "outside":
				st.ListInside = false
			}
		}
	}
	for _, key := range []string{"list-style-type", "list-style"} {
		if v, ok := decls[key]; ok {
			if marker, text := webListStyleValue(v); marker != webListStyleDefault {
				st.ListStyle, st.ListString = marker, text
				if marker == webListStyleNone {
					st.ListNone = true
				}
				break
			}
		}
	}
	// SVG paint. Only an inline <svg> reads these (webpage_svg.go).
	if v, ok := decls["fill"]; ok {
		st.SVGFill = webSVGPaintDecl(v, st.SVGFill)
	}
	if v, ok := decls["stroke"]; ok {
		st.SVGStroke = webSVGPaintDecl(v, st.SVGStroke)
	}
	// Gradient text (background-clip: text) and -webkit-text-fill-color, after
	// the colour and every background layer are known (webpage_hints.go).
	webApplyTextClip(st, decls)
	if n, ok := webLineClampValue(decls); ok {
		st.LineClamp = n
	}
	applyWebLayoutDecls(st, decls)
}

// webContainerTypeValue reads the containment type of a container-type or
// container declaration. Anything the reader does not model stays normal.
func webContainerTypeValue(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "inline-size":
		return 1
	case "size":
		return 2
	}
	return 0
}

// webContainerNameValue reads one container name. The reader tracks one name
// per container, which is enough for the common `container-name: card` form;
// `none` clears it.
func webContainerNameValue(value string) string {
	fields := webCSSSpaceFields(value)
	if len(fields) == 0 {
		return ""
	}
	name := strings.ToLower(strings.TrimSpace(fields[0]))
	if name == "none" || !webContainerNameIdent(name) {
		return ""
	}
	return name
}

func webContainerNameIdent(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

// webContainerShorthand reads `container: name / type` or its name-only and
// type-only forms. A name-only shorthand establishes a named container but no
// size containment, so size queries still walk past it.
func webContainerShorthand(value string) (name string, typ int) {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	if lower == "none" {
		return "", 0
	}
	if slash := strings.IndexByte(lower, '/'); slash >= 0 {
		name = webContainerNameValue(value[:slash])
		typ = webContainerTypeValue(value[slash+1:])
		return name, typ
	}
	fields := webCSSSpaceFields(value)
	if len(fields) == 1 {
		if t := webContainerTypeValue(fields[0]); t != 0 {
			return "", t
		}
		return webContainerNameValue(fields[0]), 0
	}
	return "", 0
}

// applyWebLayoutDecls reads the flex and stacking properties. They are kept
// separate from the text properties because the layout engine consumes them as
// a unit: a flex container's settings describe its children, not its text.
func applyWebLayoutDecls(st *webStyle, decls map[string]string) {
	if v, ok := decls["container-type"]; ok {
		st.ContainerType = webContainerTypeValue(v)
	}
	if v, ok := decls["container-name"]; ok {
		st.ContainerName = webContainerNameValue(v)
	}
	if v, ok := decls["container"]; ok {
		name, typ := webContainerShorthand(v)
		st.ContainerName, st.ContainerType = name, typ
	}
	if v, ok := decls["display"]; ok {
		lower := strings.ToLower(strings.TrimSpace(v))
		if alias, valid := webUnprefixedValueAlias("display", v); valid {
			lower = alias
		}
		// The line-clamp idiom (display: -webkit-box with a vertical box
		// orient) is a block of lines. As a flex row it set every link and
		// <b> of a card's title side by side as items of their own.
		if webLegacyVerticalBox(decls) {
			lower = "block"
			if strings.Contains(strings.ToLower(v), "inline") {
				lower = "inline-block"
			}
		}
		// The two-keyword syntax of CSS Display 3 (`block flex`, `inline
		// flow-root`, `block flow list-item`) means its one-keyword form.
		lower, listItem := webDisplayKeywords(lower)
		st.ListItem = listItem
		st.DisplayContents = lower == "contents"
		st.DisplayInline = lower == "inline"
		st.DisplayInlineBlock = lower == "inline-block" || lower == "inline-flex" || lower == "inline-grid"
		st.FlowRoot = lower == "flow-root"
		switch lower {
		case "contents":
			st.Display = webDisplayBlock
			st.DisplayBlock = false
		case "flex", "inline-flex":
			st.Display = webDisplayFlex
			st.DisplayBlock = lower == "flex"
		case "grid", "inline-grid":
			st.Display = webDisplayGrid
			st.FlexDir, st.FlexWrap = webFlexRow, true
			st.DisplayBlock = lower == "grid"
		case "table":
			st.Display = webDisplayTable
			st.DisplayBlock = true
		case "table-row":
			st.Display, st.FlexDir = webDisplayTableRow, webFlexRow
			st.DisplayBlock = true
		case "table-cell":
			st.Display = webDisplayTableCell
			st.DisplayBlock = true
		case "table-header-group":
			st.Display = webDisplayTableHeaderGroup
			st.DisplayBlock = true
		case "table-row-group":
			st.Display = webDisplayTableRowGroup
			st.DisplayBlock = true
		case "table-footer-group":
			st.Display = webDisplayTableFooterGroup
			st.DisplayBlock = true
		case "table-caption":
			st.Display = webDisplayTableCaption
			st.DisplayBlock = true
		case "block", "flow-root":
			st.Display = webDisplayBlock
			st.DisplayBlock = true
		case "inline-block":
			st.Display = webDisplayBlock
		}
	}
	if v, ok := decls["table-layout"]; ok {
		if strings.EqualFold(strings.TrimSpace(v), "fixed") {
			st.TableLayout = webTableLayoutFixed
		} else if strings.EqualFold(strings.TrimSpace(v), "auto") {
			st.TableLayout = webTableLayoutAuto
		}
	}
	if v, ok := decls["border-collapse"]; ok {
		if strings.EqualFold(strings.TrimSpace(v), "collapse") {
			st.BorderCollapse = webBorderCollapseCollapse
		} else if strings.EqualFold(strings.TrimSpace(v), "separate") {
			st.BorderCollapse = webBorderCollapseSeparate
		}
	}
	if v, ok := decls["border-spacing"]; ok {
		fields := webCSSSpaceFields(v)
		if len(fields) > 0 {
			st.TableSpacingX = webLength(fields[0], 16)
			st.TableSpacingY = st.TableSpacingX
			if len(fields) > 1 {
				st.TableSpacingY = webLength(fields[1], 16)
			}
			st.TableSpacingSet = true
		}
	}
	if v, ok := decls["grid-template-columns"]; ok && st.Display == webDisplayGrid {
		st.GridCols, st.GridMin = webGridTemplate(v)
		specs, count, repeat, auto := webGridTrackList(v)
		weights, bases, _ := webGridTracks(v)
		st.GridTrackSpecs, st.GridAutoRepeat, st.GridAutoTrack = specs, repeat, auto
		st.GridCols, st.GridWeights, st.GridBases = count, weights, bases
	}
	if v, ok := decls["grid-auto-columns"]; ok && st.Display == webDisplayGrid {
		specs, count, repeat, auto := webGridTrackList(v)
		if repeat == 0 {
			st.GridAutoColSpecs, st.GridAutoColCount = specs, count
		} else {
			st.GridAutoColSpecs, st.GridAutoColCount = [6]webGridTrackSpec{auto}, 1
		}
	}
	if v, ok := decls["grid-auto-rows"]; ok && st.Display == webDisplayGrid {
		specs, count, repeat, auto := webGridTrackList(v)
		if repeat == 0 {
			st.GridAutoRowSpecs, st.GridAutoRowCount = specs, count
		} else {
			st.GridAutoRowSpecs, st.GridAutoRowCount = [6]webGridTrackSpec{auto}, 1
		}
	}
	if v, ok := decls["grid-column"]; ok {
		webApplyGridColumn(st, v)
	}
	if v, ok := decls["grid-column-start"]; ok {
		webApplyGridColumnStart(st, v)
	}
	if v, ok := decls["grid-column-end"]; ok {
		webApplyGridColumnEnd(st, v)
	}
	if v, ok := decls["grid-template-rows"]; ok && st.Display == webDisplayGrid {
		specs, count, repeat, auto := webGridTrackList(v)
		weights, bases, _ := webGridTracks(v)
		st.GridRowTrackSpecs, st.GridRowAutoRepeat, st.GridRowAutoTrack = specs, repeat, auto
		st.GridRows, st.GridRowWeights, st.GridRowBases = count, weights, bases
	}
	if v, ok := decls["grid-row"]; ok {
		webApplyGridRow(st, v)
	}
	if v, ok := decls["grid-row-start"]; ok {
		webApplyGridRowStart(st, v)
	}
	if v, ok := decls["grid-row-end"]; ok {
		webApplyGridRowEnd(st, v)
	}
	if v, ok := decls["grid-template-areas"]; ok && st.Display == webDisplayGrid {
		st.GridAreas = webGridTemplateAreas(v)
	}
	if v, ok := decls["grid-area"]; ok {
		webApplyGridArea(st, v)
	}
	if v, ok := decls["grid-auto-flow"]; ok {
		webApplyGridAutoFlow(st, v)
	}
	if v, ok := decls["flex-flow"]; ok {
		for _, field := range webCSSSpaceFields(v) {
			lower := strings.ToLower(field)
			switch lower {
			case "row", "row-reverse", "column", "column-reverse":
				st.FlexDir = webFlexRow
				if strings.HasPrefix(lower, "column") {
					st.FlexDir = webFlexColumn
				}
				st.FlexReverse = strings.HasSuffix(lower, "-reverse")
			case "nowrap", "wrap", "wrap-reverse":
				st.FlexWrap = strings.HasPrefix(lower, "wrap")
			}
		}
	}
	if v, ok := decls["flex-direction"]; ok {
		lower := strings.ToLower(strings.TrimSpace(v))
		st.FlexDir = webFlexRow
		if strings.HasPrefix(lower, "column") {
			st.FlexDir = webFlexColumn
		}
		st.FlexReverse = strings.HasSuffix(lower, "-reverse")
	}
	if v, ok := decls["flex-wrap"]; ok {
		lower := strings.ToLower(strings.TrimSpace(v))
		st.FlexWrap = strings.HasPrefix(lower, "wrap")
	}
	if v, ok := decls["justify-content"]; ok {
		st.Justify = webJustifyValue(v)
	}
	if v, ok := decls["align-items"]; ok {
		st.AlignItems = webAlignValue(v)
	}
	if v, ok := decls["align-content"]; ok {
		st.AlignContent = webContentAlignValue(v)
	}
	// place-content is align-content followed by justify-content.
	if v, ok := decls["place-content"]; ok {
		if fields := webCSSSpaceFields(v); len(fields) > 0 {
			st.AlignContent = webContentAlignValue(fields[0])
			justify := fields[0]
			if len(fields) > 1 {
				justify = fields[1]
			}
			st.Justify = webJustifyValue(justify)
		}
	}
	if v, ok := decls["justify-items"]; ok {
		st.JustifyItems = webAlignValue(v)
	}
	// place-items is `align-items justify-items`.
	if v, ok := decls["place-items"]; ok {
		if fields := webCSSSpaceFields(v); len(fields) > 0 {
			st.AlignItems = webAlignValue(fields[0])
			if len(fields) > 1 {
				st.JustifyItems = webAlignValue(fields[1])
			}
		}
	}
	if v, ok := decls["justify-self"]; ok {
		st.JustifySelf = webAlignValue(v)
	}
	// place-self is `align-self justify-self`; auto leaves the axis inherited.
	if v, ok := decls["place-self"]; ok {
		if fields := webCSSSpaceFields(v); len(fields) > 0 {
			if !strings.EqualFold(fields[0], "auto") {
				st.AlignSelf = webAlignValue(fields[0])
			}
			if len(fields) > 1 && !strings.EqualFold(fields[1], "auto") {
				st.JustifySelf = webAlignValue(fields[1])
			}
		}
	}
	if v, ok := decls["order"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			st.Order = n
		}
	}
	if v, ok := decls["align-self"]; ok {
		st.AlignSelf = webAlignValue(v)
	}
	if v, ok := decls["gap"]; ok {
		fields := webCSSSpaceFields(v)
		if len(fields) > 0 {
			// CSS gap lists the row gap before the column gap.
			st.GapY = webLength(fields[0], st.FontPx)
			st.GapX = st.GapY
		}
		if len(fields) > 1 {
			st.GapX = webLength(fields[1], st.FontPx)
		}
	}
	if v, ok := decls["column-gap"]; ok {
		st.GapX = webLength(v, st.FontPx)
	}
	if v, ok := decls["row-gap"]; ok {
		st.GapY = webLength(v, st.FontPx)
	}
	// The flex shorthand resets grow, shrink and basis together, so it must
	// run before its longhands: a later flex-shrink on the same element wins.
	if v, ok := decls["flex"]; ok {
		webFlexShorthand(st, v)
	}
	if v, ok := decls["flex-grow"]; ok {
		if grow, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && grow >= 0 {
			st.FlexGrow = grow
		}
	}
	if v, ok := decls["flex-shrink"]; ok {
		if shrink, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && shrink >= 0 {
			st.FlexShrink = shrink
		}
	}
	if v, ok := decls["flex-basis"]; ok {
		webFlexBasis(st, v)
	}
	if v, ok := decls["width"]; ok {
		if fit := webWidthKeyword(v); fit >= 0 {
			// An intrinsic size, or stretch (which is auto for a block).
			st.WidthFit = fit
		} else if pct, valid := webPercentValue(v); valid {
			st.WidthPct = pct
		} else if px := webLength(v, st.FontPx); px > 0 {
			st.Width = px
		}
	}
	if v, ok := decls["height"]; ok {
		if px := webHeightLength(v, st.FontPx); px > 0 {
			st.Height = px
		}
	}
	if v, ok := decls["min-width"]; ok {
		st.MinWidthAuto = webCSSAuto(v)
		if pct, valid := webPercentValue(v); valid {
			st.MinWidthPct = pct
		} else if px := webLength(v, st.FontPx); px > 0 {
			st.MinWidth = px
		}
	}
	if v, ok := decls["max-width"]; ok {
		if pct, valid := webPercentValue(v); valid {
			st.MaxWidthPct = pct
		} else if px := webLength(v, st.FontPx); px > 0 {
			st.MaxWidth = px
		}
	}
	if v, ok := decls["min-height"]; ok {
		if px := webHeightLength(v, st.FontPx); px > 0 {
			st.MinHeight = px
		}
	}
	if v, ok := decls["max-height"]; ok {
		if px := webHeightLength(v, st.FontPx); px > 0 {
			st.MaxHeight = px
		}
	}
	if v, ok := decls["box-sizing"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "border-box":
			st.BoxSizing = 1
		case "content-box":
			st.BoxSizing = 0
		}
	}
	if v, ok := decls["float"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "left", "inline-start":
			st.FloatSide = 1
		case "right", "inline-end":
			st.FloatSide = 2
		}
	}
	if v, ok := decls["clear"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "left", "inline-start":
			st.Clear = 1
		case "right", "inline-end":
			st.Clear = 2
		case "both":
			st.Clear = 3
		}
	}
	if v, ok := decls["aspect-ratio"]; ok {
		if ratio, valid := webMediaRatio(strings.TrimSpace(v)); valid {
			st.AspectRatio = ratio
		}
	}
	if v, ok := decls["object-fit"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "contain":
			st.ObjectFit = webObjectFitContain
		case "cover":
			st.ObjectFit = webObjectFitCover
		case "none":
			st.ObjectFit = webObjectFitNone
		case "scale-down":
			st.ObjectFit = webObjectFitScaleDown
		default:
			st.ObjectFit = webObjectFitFill
		}
	}
	if v, ok := decls["object-position"]; ok {
		st.ObjectPosition = strings.TrimSpace(v)
	}
	if v, ok := decls["position"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "relative":
			st.Position = webPositionRelative
		case "absolute":
			st.Position = webPositionAbsolute
		case "fixed":
			// A fixed layer has the viewport as its containing block. It is
			// still painted as a layer (the reader has no scroll compositor),
			// but keeping the flag makes right/bottom offsets anchor to the
			// screen rather than the body's content inset.
			st.Position = webPositionFixed
		case "sticky":
			// A sticky header starts in normal flow. Treating it as absolute
			// painted it over the first hero/content rows at the top of the
			// page; it keeps its flow rows and layout records a pinned copy.
			st.Position = webPositionStatic
			st.Sticky = true
		}
	}
	if v, ok := decls["z-index"]; ok {
		if z, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			st.ZIndex, st.ZSet = z, true
		} else if webCSSAuto(v) {
			st.ZIndex, st.ZSet = 0, false
		}
	}
	if v, ok := decls["inset"]; ok {
		if top, right, bottom, left, valid := webBoxShorthand(v, st.FontPx); valid {
			st.Top, st.Right, st.Bottom, st.Left = top, right, bottom, left
			st.HasTop, st.HasRight, st.HasBottom, st.HasLeft = true, true, true, true
			st.InsetPct = webInsetPercents(v)
		}
	}
	// The logical two-axis shorthands (inset-block/inset-inline) are common
	// on absolutely positioned overlays. Their two-value form is start then end,
	// unlike the physical top/right/bottom/left shorthand.
	if v, ok := decls["inset-block"]; ok {
		fields := webCSSSpaceFields(v)
		if len(fields) > 0 {
			end := fields[0]
			if len(fields) > 1 {
				end = fields[1]
			}
			st.Top, st.HasTop = webLength(fields[0], st.FontPx), !webCSSAuto(fields[0])
			st.Bottom, st.HasBottom = webLength(end, st.FontPx), !webCSSAuto(end)
			st.InsetPct[0], st.InsetPct[2] = webInsetPercent(fields[0]), webInsetPercent(end)
		}
	}
	if v, ok := decls["inset-inline"]; ok {
		fields := webCSSSpaceFields(v)
		if len(fields) > 0 {
			end := fields[0]
			if len(fields) > 1 {
				end = fields[1]
			}
			st.Left, st.HasLeft = webLength(fields[0], st.FontPx), !webCSSAuto(fields[0])
			st.Right, st.HasRight = webLength(end, st.FontPx), !webCSSAuto(end)
			st.InsetPct[3], st.InsetPct[1] = webInsetPercent(fields[0]), webInsetPercent(end)
		}
	}
	for _, edge := range []struct {
		key  string
		val  *float64
		flag *bool
		pct  *float64
	}{
		{"top", &st.Top, &st.HasTop, &st.InsetPct[0]},
		{"right", &st.Right, &st.HasRight, &st.InsetPct[1]},
		{"bottom", &st.Bottom, &st.HasBottom, &st.InsetPct[2]},
		{"left", &st.Left, &st.HasLeft, &st.InsetPct[3]},
		{"inset-block-start", &st.Top, &st.HasTop, &st.InsetPct[0]},
		{"inset-block-end", &st.Bottom, &st.HasBottom, &st.InsetPct[2]},
		{"inset-inline-start", &st.Left, &st.HasLeft, &st.InsetPct[3]},
		{"inset-inline-end", &st.Right, &st.HasRight, &st.InsetPct[1]},
	} {
		v, ok := decls[edge.key]
		if !ok {
			continue
		}
		*edge.pct = webInsetPercent(v)
		if webCSSAuto(v) {
			*edge.flag = false
			continue
		}
		*edge.val = webLength(v, st.FontPx)
		*edge.flag = true
	}
}

// webCSSSpaceFields splits a value on top-level whitespace only. A length
// like calc(1rem + 4px) is one gap or grid track, not three words.
func webCSSSpaceFields(value string) []string {
	var fields []string
	start, depth := -1, 0
	var quote byte
	for i := 0; i <= len(value); i++ {
		if i == len(value) || (quote == 0 && depth == 0 && webAttrSpaceByte(value[i])) {
			if start >= 0 {
				fields = append(fields, value[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
		if quote != 0 {
			if value[i] == '\\' {
				i++
			} else if value[i] == quote {
				quote = 0
			}
			continue
		}
		switch value[i] {
		case '\'', '"':
			quote = value[i]
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		}
	}
	return fields
}

// webGridTemplate reads an explicit track count, or the minimum track width of
// a responsive auto-fit/auto-fill repeat. The latter is the modern card-grid
// recipe (`repeat(auto-fit, minmax(220px, 1fr))`); its count is not known until
// the layout measures the page.
func webGridTemplate(value string) (int, float64) {
	_, count, repeat, auto := webGridTrackList(value)
	if repeat > 0 {
		return 0, webGridTrackDefinitePx(auto, 0)
	}
	return count, 0
}

// webGridTracks reads the used width and fr weight of each explicit grid
// track. `grid-template-columns: 240px 1fr` is two tracks, not two equal
// columns: the sidebar keeps its 240px and the article column takes the rest.
// Percentages and content-sized tracks have no width until layout, so they
// are treated as one fr.
func webGridTracks(value string) (weights, bases [6]float64, count int) {
	specs, count, repeat, _ := webGridTrackList(value)
	if repeat > 0 {
		return weights, bases, 0
	}
	for i := 0; i < count; i++ {
		weights[i], bases[i] = webGridTrackWeightBase(specs[i])
	}
	return weights, bases, count
}

// webGridTrackList reads a full track list, expanding fixed repeats and
// keeping an auto-fill/auto-fit repeat as a single definition for the sizing
// pass. A page can combine a fixed repeat with ordinary tracks; both use the
// same six-track cap as the rest of the reader.
func webGridTrackList(value string) (tracks [6]webGridTrackSpec, count, repeat int, auto webGridTrackSpec) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == "none" || value == "subgrid" {
		return tracks, 0, 0, webGridTrackSpec{}
	}
	appendTrack := func(track string) {
		if count >= len(tracks) {
			return
		}
		tracks[count] = webGridTrackFromValue(track)
		count++
	}
	for _, field := range webCSSSpaceFields(value) {
		field = strings.TrimSpace(field)
		if !strings.HasPrefix(field, "repeat(") {
			appendTrack(field)
			continue
		}
		open := len("repeat")
		end := webCSSParenEnd(field, open)
		if end <= open {
			continue
		}
		parts := webSplitTopLevel(field[open+1:end], ',')
		if len(parts) < 2 {
			continue
		}
		countText := strings.TrimSpace(parts[0])
		repeated := webGridTrackFromValue(parts[1])
		if countText == "auto-fill" || countText == "auto-fit" {
			repeat, auto = 1, repeated
			if countText == "auto-fit" {
				repeat = 2
			}
			continue
		}
		n, err := strconv.Atoi(countText)
		if err != nil || n < 1 {
			continue
		}
		for i := 0; i < n; i++ {
			if count >= len(tracks) {
				break
			}
			tracks[count] = repeated
			count++
		}
	}
	return tracks, count, repeat, auto
}

// webGridTrackFromValue parses one track, including the minmax forms modern
// responsive grids use. Plain lengths are both the min and max; an fr track
// has an automatic minimum and a flexible maximum.
func webGridTrackFromValue(track string) webGridTrackSpec {
	track = strings.ToLower(strings.TrimSpace(track))
	if track == "" {
		return webGridTrackSpec{MinAuto: true, MaxAuto: true}
	}
	if strings.HasPrefix(track, "minmax(") {
		open := len("minmax")
		end := webCSSParenEnd(track, open)
		if end > open {
			parts := webSplitTopLevel(track[open+1:end], ',')
			if len(parts) == 2 {
				return webGridMinMaxSpec(parts[0], parts[1])
			}
		}
		return webGridTrackSpec{MinAuto: true, MaxAuto: true}
	}
	if strings.HasPrefix(track, "fit-content(") {
		open := len("fit-content")
		if end := webCSSParenEnd(track, open); end > open {
			inner := strings.TrimSpace(track[open+1 : end])
			spec := webGridTrackSpec{MinAuto: true}
			spec.Max, spec.MaxPct = webGridLengthBounds(inner)
			return spec
		}
	}
	if fr := webGridFr(track); fr > 0 {
		return webGridTrackSpec{MinAuto: true, MaxFr: fr}
	}
	if track == "auto" || track == "min-content" || track == "max-content" {
		return webGridTrackSpec{MinAuto: true, MaxAuto: true}
	}
	px, pct := webGridLengthBounds(track)
	if px <= 0 && pct <= 0 {
		return webGridTrackSpec{MinAuto: true, MaxAuto: true}
	}
	return webGridTrackSpec{Min: px, Max: px, MinPct: pct, MaxPct: pct}
}

func webGridMinMaxSpec(minValue, maxValue string) webGridTrackSpec {
	minValue = strings.ToLower(strings.TrimSpace(minValue))
	maxValue = strings.ToLower(strings.TrimSpace(maxValue))
	spec := webGridTrackSpec{}
	if minValue == "auto" || minValue == "min-content" || minValue == "max-content" {
		spec.MinAuto = true
	} else {
		spec.Min, spec.MinPct = webGridLengthBounds(minValue)
	}
	if maxValue == "auto" || maxValue == "min-content" || maxValue == "max-content" {
		spec.MaxAuto = true
	} else if fr := webGridFr(maxValue); fr > 0 {
		spec.MaxFr = fr
	} else {
		spec.Max, spec.MaxPct = webGridLengthBounds(maxValue)
	}
	return spec
}

// webGridLengthBounds returns a length as px or percentage. calc() values
// that can be resolved without a container are handled by webLength.
func webGridLengthBounds(value string) (px, pct float64) {
	value = strings.TrimSpace(value)
	if p, ok := webPercentValue(value); ok {
		return 0, p
	}
	return webLength(value, 16), 0
}

// webGridTrackWeightBase keeps the compact legacy representation in sync with
// a spec. The full spec is authoritative for layout; these arrays remain the
// fallback for callers that only know about (weight, base).
func webGridTrackWeightBase(spec webGridTrackSpec) (weight, base float64) {
	if spec.MaxFr > 0 {
		return spec.MaxFr, spec.Min
	}
	if spec.MinAuto && spec.MaxAuto {
		return 1, 0
	}
	if spec.MinAuto && spec.Max > 0 {
		return 0, spec.Max
	}
	return 0, math.Max(spec.Min, spec.Max)
}

// webGridTrackDefinitePx is the definite (non-fr) size used to count an
// auto-fill/auto-fit repetition. An fr-only track intentionally falls back to
// its minimum; when that minimum is automatic the track occupies one slot.
func webGridTrackDefinitePx(spec webGridTrackSpec, basis float64) float64 {
	if spec.MinPct > 0 && basis > 0 {
		return basis * spec.MinPct / 100
	}
	if spec.Min > 0 {
		return spec.Min
	}
	if spec.MinAuto && spec.Max > 0 && !spec.MaxAuto {
		return spec.Max
	}
	if spec.MaxPct > 0 && basis > 0 {
		return basis * spec.MaxPct / 100
	}
	return 0
}

func webGridFr(track string) float64 {
	track = strings.TrimSpace(strings.ToLower(track))
	if !strings.HasSuffix(track, "fr") {
		return 0
	}
	factor, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(track, "fr")), 64)
	if err != nil || factor <= 0 {
		return 0
	}
	return factor
}

// webGridTrackMin extracts the first argument of minmax(), or a plain length.
// A percentage cannot be resolved here (the container width is a layout-time
// fact), so it falls back to the width-based wrapping path.
func webGridTrackMin(track string) float64 {
	track = strings.ToLower(strings.TrimSpace(track))
	if strings.HasPrefix(track, "minmax(") {
		open := len("minmax")
		if end := webCSSParenEnd(track, open); end > open {
			parts := webSplitTopLevel(track[open+1:end], ',')
			if len(parts) >= 1 {
				track = strings.TrimSpace(parts[0])
			} else {
				return 0
			}
		}
	}
	if track == "" || strings.HasSuffix(track, "%") || track == "auto" || track == "min-content" || track == "max-content" {
		return 0
	}
	return webLength(track, 16)
}

// webGridLine reads one grid placement line: `3`, `-1`, `auto`, or
// `span 2`. A span returns (0, n); a line returns (n, 0).
func webGridLine(value string) (line, span int) {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(value)))
	if len(fields) == 2 && fields[0] == "span" {
		if n, err := strconv.Atoi(fields[1]); err == nil && n > 0 {
			return 0, n
		}
		return 0, 0
	}
	if len(fields) == 1 && fields[0] != "auto" {
		if n, err := strconv.Atoi(fields[0]); err == nil && n != 0 {
			return n, 0
		}
	}
	return 0, 0
}

func webGridPlacementParts(value string) []string {
	parts := webSplitTopLevel(strings.TrimSpace(value), '/')
	if len(parts) > 2 {
		return nil
	}
	return parts
}

// webGridAreaStrings extracts the quoted row strings of
// `grid-template-areas`. CSS area names are case-sensitive, so this keeps the
// authored casing while ignoring the whitespace and line breaks between rows.
func webGridAreaStrings(value string) []string {
	var rows []string
	for i := 0; i < len(value) && len(rows) < 6; i++ {
		quote := value[i]
		if quote != '"' && quote != '\'' {
			continue
		}
		start := i + 1
		j := start
		for ; j < len(value); j++ {
			if value[j] == '\\' {
				j++
				continue
			}
			if value[j] == quote {
				break
			}
		}
		if j <= len(value) {
			rows = append(rows, value[start:j])
		}
		i = j
	}
	return rows
}

// webGridTemplateAreas parses a `grid-template-areas` matrix. The reader caps
// grids at six tracks, so a larger authored matrix is truncated rather than
// growing the page layout budget.
func webGridTemplateAreas(value string) *webGridAreas {
	if strings.EqualFold(strings.TrimSpace(value), "none") {
		return nil
	}
	rows := webGridAreaStrings(value)
	if len(rows) == 0 {
		return nil
	}
	areas := &webGridAreas{Rows: len(rows)}
	for r, row := range rows {
		fields := strings.Fields(row)
		for c, name := range fields {
			if c >= 6 {
				break
			}
			if strings.Trim(name, ".") == "" {
				name = ""
			}
			areas.Names[r][c] = name
			if c+1 > areas.Cols {
				areas.Cols = c + 1
			}
		}
	}
	if areas.Cols == 0 {
		return nil
	}
	return areas
}

// webGridAreaBounds resolves one named area to inclusive 0-based cells turned
// into 1-based grid lines. CSS requires named areas to be rectangular; taking
// the bounding box also keeps a malformed matrix from dropping the item.
func (g *webGridAreas) bounds(name string) (rowStart, rowEnd, colStart, colEnd int, ok bool) {
	if g == nil || name == "" {
		return 0, 0, 0, 0, false
	}
	minR, maxR := g.Rows, -1
	minC, maxC := g.Cols, -1
	for r := 0; r < g.Rows; r++ {
		for c := 0; c < g.Cols; c++ {
			if g.Names[r][c] != name {
				continue
			}
			if r < minR {
				minR = r
			}
			if r > maxR {
				maxR = r
			}
			if c < minC {
				minC = c
			}
			if c > maxC {
				maxC = c
			}
		}
	}
	if maxR < 0 || maxC < 0 {
		return 0, 0, 0, 0, false
	}
	return minR + 1, maxR + 2, minC + 1, maxC + 2, true
}

// webApplyGridArea reads `grid-area`. A single custom identifier names a
// `grid-template-areas` cell; the four-line form is the row-start /
// column-start / row-end / column-end shorthand.
func webApplyGridArea(st *webStyle, value string) {
	st.GridArea = ""
	st.GridRowStart, st.GridRowEnd, st.GridRowSpan = 0, 0, 0
	st.GridColumnStart, st.GridColumnEnd, st.GridColumnSpan = 0, 0, 0
	parts := webSplitTopLevel(strings.TrimSpace(value), '/')
	if len(parts) == 1 {
		name := strings.TrimSpace(parts[0])
		line, span := webGridLine(name)
		if name != "" && line == 0 && span == 0 && !strings.EqualFold(name, "auto") {
			st.GridArea = name
			return
		}
		st.GridRowStart, st.GridRowEnd, st.GridRowSpan = line, 0, span
		return
	}
	if len(parts) != 4 {
		return
	}
	rowStart, rowSpan := webGridLine(parts[0])
	colStart, colSpan := webGridLine(parts[1])
	rowEnd, rowEndSpan := webGridLine(parts[2])
	colEnd, colEndSpan := webGridLine(parts[3])
	st.GridRowStart, st.GridColumnStart = rowStart, colStart
	st.GridRowEnd, st.GridColumnEnd = rowEnd, colEnd
	if rowSpan == 0 {
		rowSpan = rowEndSpan
	}
	if colSpan == 0 {
		colSpan = colEndSpan
	}
	st.GridRowSpan, st.GridColumnSpan = rowSpan, colSpan
}

// webApplyGridAutoFlow reads row/column flow and the optional dense packing
// hint. Both axes use the same occupancy map; only the search order changes.
func webApplyGridAutoFlow(st *webStyle, value string) {
	st.GridAutoFlow, st.GridDense = 0, false
	for _, field := range strings.Fields(strings.ToLower(strings.TrimSpace(value))) {
		switch field {
		case "column":
			st.GridAutoFlow = 1
		case "dense":
			st.GridDense = true
		case "row":
			st.GridAutoFlow = 0
		}
	}
}

func webSupportsGridAutoFlow(value string) bool {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(value)))
	if len(fields) == 0 || len(fields) > 2 {
		return false
	}
	seenAxis, seenDense := false, false
	for _, field := range fields {
		switch field {
		case "row", "column":
			if seenAxis || seenDense {
				return false
			}
			seenAxis = true
		case "dense":
			if seenDense {
				return false
			}
			seenDense = true
		default:
			return false
		}
	}
	return true
}

func webSupportsGridAreas(value string) bool {
	value = strings.TrimSpace(value)
	return strings.EqualFold(value, "none") || len(webGridAreaStrings(value)) > 0
}

func webSupportsGridArea(value string) bool {
	parts := webSplitTopLevel(strings.TrimSpace(value), '/')
	if len(parts) == 1 {
		return true
	}
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts {
		if !webSupportsGridPlacement(part) {
			return false
		}
	}
	return true
}

// webApplyGridColumn reads the `grid-column` shorthand. It keeps the three
// forms card CSS uses: `span 2`, a start line, and `start / end` or
// `start / span n`.
func webApplyGridColumn(st *webStyle, value string) {
	st.GridColumnStart, st.GridColumnEnd, st.GridColumnSpan = 0, 0, 0
	parts := webGridPlacementParts(value)
	if len(parts) == 0 || len(parts) > 2 {
		return
	}
	start, startSpan := webGridLine(parts[0])
	st.GridColumnStart = start
	if startSpan > 0 {
		st.GridColumnSpan = startSpan
	}
	if len(parts) == 1 {
		return
	}
	end, endSpan := webGridLine(parts[1])
	st.GridColumnEnd = end
	if endSpan > 0 {
		st.GridColumnSpan = endSpan
	}
}

func webApplyGridColumnStart(st *webStyle, value string) {
	line, span := webGridLine(value)
	if span > 0 {
		st.GridColumnStart, st.GridColumnEnd, st.GridColumnSpan = 0, 0, span
		return
	}
	st.GridColumnStart = line
}

// webApplyGridRow mirrors the column shorthand on the block axis.
func webApplyGridRow(st *webStyle, value string) {
	st.GridRowStart, st.GridRowEnd, st.GridRowSpan = 0, 0, 0
	parts := webGridPlacementParts(value)
	if len(parts) == 0 || len(parts) > 2 {
		return
	}
	start, startSpan := webGridLine(parts[0])
	st.GridRowStart = start
	if startSpan > 0 {
		st.GridRowSpan = startSpan
	}
	if len(parts) == 1 {
		return
	}
	end, endSpan := webGridLine(parts[1])
	st.GridRowEnd = end
	if endSpan > 0 {
		st.GridRowSpan = endSpan
	}
}

func webApplyGridRowStart(st *webStyle, value string) {
	line, span := webGridLine(value)
	if span > 0 {
		st.GridRowStart, st.GridRowEnd, st.GridRowSpan = 0, 0, span
		return
	}
	st.GridRowStart = line
}

func webApplyGridRowEnd(st *webStyle, value string) {
	line, span := webGridLine(value)
	if span > 0 {
		st.GridRowSpan = span
		return
	}
	st.GridRowEnd = line
}

func webApplyGridColumnEnd(st *webStyle, value string) {
	line, span := webGridLine(value)
	if span > 0 {
		st.GridColumnSpan = span
		return
	}
	st.GridColumnEnd = line
}

func webSupportsGridPlacement(value string) bool {
	parts := webGridPlacementParts(value)
	if len(parts) == 0 || len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		fields := strings.Fields(strings.ToLower(strings.TrimSpace(part)))
		if len(fields) == 2 && fields[0] == "span" {
			if n, err := strconv.Atoi(fields[1]); err == nil && n > 0 {
				continue
			}
			return false
		}
		if len(fields) != 1 {
			return false
		}
		if fields[0] == "auto" {
			continue
		}
		if n, err := strconv.Atoi(fields[0]); err != nil || n == 0 {
			return false
		}
	}
	return true
}

// webFlexBasis reads flex-basis, keeping a percentage unresolved: the layout
// resolves it against the container's width.
func webFlexBasis(st *webStyle, value string) {
	lower := strings.ToLower(strings.TrimSpace(value))
	if lower == "auto" || lower == "content" || lower == "max-content" || lower == "min-content" {
		st.FlexBasis = -1
		st.BasisPct = 0
		return
	}
	if pct, ok := webPercentValue(value); ok {
		st.BasisPct = pct
		st.FlexBasis = -1
		if pct == 0 {
			st.FlexBasis = 0
		}
		return
	}
	st.FlexBasis = webLength(value, st.FontPx)
	st.BasisPct = 0
}

// webFlexShorthand reads the common `flex: 1`, `flex: 1 1 200px` and
// `flex: 0 0 33%` forms. The shrink factor is kept for the row layout.
func webFlexShorthand(st *webStyle, value string) {
	lower := strings.ToLower(strings.TrimSpace(value))
	switch lower {
	case "none":
		st.FlexGrow = 0
		st.FlexShrink = 0
		st.FlexBasis = -1
		st.BasisPct = 0
		return
	case "auto":
		st.FlexGrow = 1
		st.FlexShrink = 1
		st.FlexBasis = -1
		st.BasisPct = 0
		return
	case "initial":
		st.FlexGrow = 0
		st.FlexShrink = 1
		st.FlexBasis = -1
		st.BasisPct = 0
		return
	}
	fields := strings.Fields(lower)
	if len(fields) == 0 {
		return
	}
	st.FlexShrink = 1
	if grow, err := strconv.ParseFloat(fields[0], 64); err == nil {
		st.FlexGrow = grow
	} else if pct, ok := webPercentValue(fields[0]); ok {
		// `flex: 33%` is a basis, not a grow factor.
		st.BasisPct, st.FlexBasis = pct, -1
		return
	}
	if len(fields) > 2 {
		if shrink, err := strconv.ParseFloat(fields[1], 64); err == nil {
			st.FlexShrink = shrink
		}
		webFlexBasis(st, fields[2])
		return
	}
	if len(fields) == 2 {
		if pct, ok := webPercentValue(fields[1]); ok {
			st.BasisPct, st.FlexBasis = pct, -1
			if pct == 0 {
				st.FlexBasis = 0
			}
		} else if shrink, err := strconv.ParseFloat(fields[1], 64); err == nil {
			// A plain number in the second slot is flex-shrink, not a
			// length. For `flex: 1 1`, the omitted basis is zero.
			st.FlexShrink = shrink
			st.FlexBasis, st.BasisPct = 0, 0
		} else if px := webLength(fields[1], st.FontPx); px > 0 {
			st.FlexBasis, st.BasisPct = px, 0
		}
		return
	}
	// A bare number means "grow from zero": `flex: 1` is `1 1 0%`.
	st.FlexBasis, st.BasisPct = 0, 0
}

func webPercentValue(value string) (float64, bool) {
	v := strings.TrimSpace(strings.ToLower(value))
	if !strings.HasSuffix(v, "%") {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(v, "%")), 64)
	if err != nil || f < 0 {
		return 0, false
	}
	return f, true
}

func webJustifyValue(value string) int {
	switch webAlignKeyword(value) {
	case "center":
		return webJustifyCenter
	case "flex-end", "end", "right":
		return webJustifyEnd
	case "space-between":
		return webJustifyBetween
	case "space-around":
		return webJustifyAround
	case "space-evenly":
		return webJustifyEvenly
	}
	return webJustifyStart
}

func webAlignValue(value string) int {
	switch webAlignKeyword(value) {
	case "center", "anchor-center":
		return webItemCenter
	case "flex-end", "end", "right", "self-end":
		return webItemEnd
	case "stretch", "normal":
		return webItemStretch
	}
	return webItemStart
}

// webAlignKeyword is an alignment value without its overflow keyword: `safe
// center` and `unsafe center` centre like `center` (the reader never lets an
// item overflow its line), and `first baseline` is a baseline.
func webAlignKeyword(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	for _, prefix := range [...]string{"safe ", "unsafe ", "first ", "last "} {
		v = strings.TrimSpace(strings.TrimPrefix(v, prefix))
	}
	return v
}

// webContentAlignValue maps align-content's block-axis distribution. `normal`
// behaves as `stretch` for a grid container, and flex-start/flex-end are the
// flow-relative aliases of start/end.
func webContentAlignValue(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "center":
		return webContentCenter
	case "end", "flex-end":
		return webContentEnd
	case "space-between":
		return webContentBetween
	case "space-around":
		return webContentAround
	case "space-evenly":
		return webContentEvenly
	case "normal", "stretch":
		return webContentStretch
	}
	return webContentStart
}

// --- HTML parsing ---------------------------------------------------------

// webDetailsFrame is one open <details>. inSummary marks the window between
// <summary> and </summary>, where blocks stay visible even while the section is
// closed.
type webDetailsFrame struct {
	ctrl      *webControl
	inSummary bool
}

// webPictureFrame is one open <picture>. Its sources are evaluated in order
// while the parser walks them; the first matching source is remembered for the
// <img> that follows, which is the page's fallback when none match.
type webPictureFrame struct {
	chosen   string
	selected bool
}

type webParser struct {
	doc     *webDocument
	rules   []webRule
	ruleIx  *webRuleIndex
	css     strings.Builder
	stack   []webFrame
	blocks  []webBlock
	current *webBlock
	lists   []webListState
	inHead  bool
	textLen int
	trunc   bool
	space   bool
	// pseudoMask is the set of generated boxes (webPseudoElem*) this sheet
	// has rules for. It keeps the common page, with no pseudo-element rules,
	// from scanning every rule set once more per element.
	pseudoMask uint8
	// pendingBefore carries ::before from startTag to push, where the
	// element's frame and block are in place. pendingB/pendingBStyle carry
	// the list marker the same way.
	pendingBefore      string
	pendingBeforeStyle webStyle
	pendingB           string
	pendingBStyle      webStyle
	hasPendingBStyle   bool
	pendingHang        bool
	// keyframes are the page's @keyframes end states (webApplyAnimationEnd).
	keyframes map[string]map[string]string
	// firstLetter is the ::first-letter style waiting for the first letter of
	// the element at stack depth firstLetter.depth (addTextStyled).
	firstLetter *webFirstLetter
	// olSeq numbers the <ol> start tags met so far; olCounts holds each one's
	// item count, for <ol reversed> (webReversedListCounts).
	olSeq    int
	olCounts []int
	// counters holds the active CSS counter instances. Each name is a stack:
	// counter-reset pushes a nested instance and the element's end tag pops
	// it, while counter-increment changes the nearest one.
	counters             map[string][]int
	pendingCounterResets []string
	flexSeq              int
	boxSeq               int
	treeSeq              int
	containerSeq         int
	hasContainerUnits    bool
	flexStack            []webFlexFrame
	// tables is the stack of open table formatting contexts. A real <table>
	// or any element with a table display role opens one; rows and cells get
	// its ID, row counter and inherited table settings.
	tables   []webTableParse
	tableSeq int
	// form is the <form> the parser is inside, so a control knows where its
	// value goes. Nested forms are invalid HTML, so one is enough. formSeq
	// numbers them, so a later <form> never inherits an earlier one's ID.
	form    webForm
	formSeq int
	// formHidden collects each form's hidden inputs, by form ID: they are
	// submitted without being drawn, so they belong to the form rather than to
	// any run.
	formHidden map[int][]webHiddenField
	// Controls with content collect it until their closing tag, then emit one
	// run. Their own text must not also appear as ordinary page text.
	textarea             *webControl
	textareaText         strings.Builder
	textareaStyle        webStyle
	buttonCtrl           *webControl
	buttonText           strings.Builder
	buttonStyle          webStyle
	selectCtrl           *webControl
	selectOption         string
	selectOptionText     strings.Builder
	selectOptionSelected bool
	// details is the stack of <details> sections currently open. A section's
	// body is parsed and kept either way; the stack decides which blocks are
	// laid out and which section a summary's toggle belongs to.
	details []webDetailsFrame
	// pictures is the stack of open <picture> elements. <source> children are
	// considered in document order and the chosen URL is carried to <img>.
	pictures []webPictureFrame
	// selTree is a structural copy of the markup used only when a stylesheet
	// has :has() rules, whose descendant checks cannot be answered from the
	// open-element stack alone. selTreePos advances one node per startTag.
	selTree    []*webElement
	selTreePos int
	// webFamilies maps a web font family name (lower case) to 1 + its index
	// in doc.WebFamilies.
	webFamilies map[string]int
	// target is the page address's fragment: the element whose id it names is
	// the one :target matches.
	target string
	// inlineGroup is the open chain of blocks an inline image split
	// (webpage_inline.go): the next text block joins it, and any other flush
	// ends it. inlineSeq numbers the chains.
	inlineGroup int
	inlineSeq   int
	// linkColor is <body link="…">, the colour of every link after it that
	// no sheet colours; rootFontPx is the <html> element's font size, which
	// rem lengths follow (webpage_hints.go). Zero until <html> is styled.
	linkColor  string
	rootFontPx float64
	// pendingLead is the room the left edges of the inline boxes opened since
	// the last run take before the next word; pendingChipStart is where in it
	// the chip begins, when pendingChip is set, and pendingOpens is set when a
	// box with a surface of its own opens there (webpage_inlinebox.go).
	pendingLead      float64
	pendingChipStart float64
	pendingChip      bool
	pendingOpens     bool
	// lineFlushes counts the lines flushed. A detached layer's own flushes
	// are taken back when the line it interrupted resumes, so an inline box
	// around it still finds its last run in that line (closeInlineBox).
	lineFlushes int
	// floatsSeen counts the floated elements opened so far: a clearfix or an
	// element that holds its floats only leaves a marker when a float came
	// before its end (webpage_floats.go).
	floatsSeen int
}

type webTableParse struct {
	id            int
	depth         int
	row           int
	layout        int
	collapse      int
	spacingX      float64
	spacingY      float64
	width         float64
	widthPct      float64
	cols          *webTableColumns
	colGroupStart int
	colGroupWidth float64
	colGroupPct   float64
	colGroupSpan  int
	// center: the table sits in the middle of a wider column (TableCenter).
	center bool
}

// parseWebPage parses HTML plus any already-fetched CSS into a document. Tests
// use it with fixture HTML, while the browser supplies linked sheets through
// parseWebPageWithLinkedSheets after its fetch.
//
// Every style sheet is in force for the whole document, as in a browser. The
// linked sheets and the page's own <style> blocks are gathered before the
// walk and parsed once. The walk used to add each <style> block
// when it reached it, so nothing before a block saw it — not even <html>, so
// a `:root { --accent: … }` in the head reached no element — and re-parsed the
// whole accumulated sheet at every block, which on a page with dozens of them
// was quadratic.
func parseWebPage(source, extraCSS, baseURL string) *webDocument {
	return parseWebPageWithLinkedSheets(source, extraCSS, baseURL, nil)
}

func parseWebPageWithLinkedSheets(source, extraCSS, baseURL string, sheets []webFetchedSheet) *webDocument {
	// A page that sets no colours is black text on white, as in a browser
	// (Canvas and CanvasText); the reader's own softer pair made such a page
	// look subtly unlike itself.
	doc := &webDocument{
		URL:              baseURL,
		Paper:            sdl.Color{R: 255, G: 255, B: 255, A: 255},
		Text:             sdl.Color{A: 255},
		Containers:       make(map[int]webContainerInfo),
		FlexBoxDepth:     make(map[int]int),
		FlexItemBoxDepth: make(map[int]map[int]int),
		FlexScrollX:      make(map[int]bool),
	}
	p := &webParser{doc: doc}
	p.css.WriteString(extraCSS)
	if len(sheets) > 0 {
		webCollectOrderedCSS(source, sheets, &p.css)
	} else {
		webCollectInlineCSS(source, &p.css)
	}
	if p.css.Len() > 0 {
		var faces []string
		p.rules = make([]webRule, 0, 32)
		ps := &webCSSParse{fontFaces: &faces, keyframes: make(map[string]map[string]string)}
		parseWebCSSInto(stripWebCSSComments(p.css.String()), 0, ps, &p.rules, nil, "")
		p.keyframes = ps.keyframes
		// The page's web fonts: their families are matched as styles are
		// computed, their files fetched beside the pictures.
		p.webFontSetup(faces)
	}
	p.ruleIx = newWebRuleIndex(p.rules)
	p.pseudoMask = webRulesPseudoMask(p.rules)
	if webRulesNeedSelectorTree(p.rules) {
		p.selTree = webBuildSelectorTree(source)
	}
	if strings.Contains(asciiLower(source), " reversed") {
		p.olCounts = webReversedListCounts(source)
	}
	if u, err := url.Parse(baseURL); err == nil {
		p.target = u.Fragment
	}
	p.walk(source)
	if p.buttonCtrl != nil {
		p.endButton()
	}
	// A detached layer left open at the end of the page ends there, and the
	// line it set aside with it.
	p.resumeDetachedAbove(-1)
	p.flush()
	// A sticky or positioned element left open at the end of the page owns
	// the rest of it.
	for i := range doc.Stickies {
		if doc.Stickies[i].lastBox < doc.Stickies[i].rootBox {
			doc.Stickies[i].lastBox = p.boxSeq
		}
	}
	for i := range doc.Positioned {
		if doc.Positioned[i].lastBox < doc.Positioned[i].rootBox {
			doc.Positioned[i].lastBox = p.boxSeq
		}
	}
	doc.FormHidden = p.formHidden
	doc.HasContainerUnits = p.hasContainerUnits
	// Every block is kept. The reader used to drop everything outside <main>
	// and <article> whenever a page had one: a news or shop front page whose
	// story and product cards are <article>s lost its header, menus, section
	// titles and everything between the cards, and a page with a single
	// <article> widget showed only that widget.
	doc.Blocks = p.blocks
	if p.trunc {
		// The note goes at the top: left at the end it would sit below
		// thousands of rows that the layout never reaches.
		note := webBlock{
			Style: webStyle{FontPx: 13, Italic: true},
			Runs:  []webRun{{Text: "[Page content truncated so the handheld keeps its memory.]", Style: webStyle{FontPx: 13, Italic: true}}},
		}
		doc.Blocks = append([]webBlock{note}, doc.Blocks...)
	}
	return doc
}

// fetchWebStylesheets pulls the page's linked stylesheets. External CSS is what
// makes most real pages look like themselves; a sheet or set of sheets past the
// budget contributes its complete leading rules. Tests use the combined CSS
// result to check fetching separately from the document renderer.
func fetchWebStylesheets(source string, base *url.URL) string {
	var out strings.Builder
	for _, sheet := range fetchWebStylesheetBlocks(source, base) {
		out.WriteString(sheet.css)
	}
	return out.String()
}

// webFetchedSheet keeps a linked sheet at its source position so it can be
// combined with inline style blocks in the same cascade order as the page.
type webFetchedSheet struct {
	start int
	css   string
}

func fetchWebStylesheetBlocks(source string, base *url.URL) []webFetchedSheet {
	return fetchWebStylesheetBlocksContext(context.Background(), source, base)
}

func fetchWebStylesheetBlocksContext(parent context.Context, source string, base *url.URL) []webFetchedSheet {
	if base == nil {
		return nil
	}
	sheets := webLinkedStylesheets(source, base)
	if len(sheets) == 0 {
		return nil
	}
	// Fetch in bounded batches. A modern page may link dozens of sheets; all
	// of their full bodies at once would briefly use much more memory than the
	// CSS we can apply. Stop scheduling after the CSS budget or deadline.
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	pageURL := base.String()
	var result []webFetchedSheet
	used := 0
	const batchSize = 8
	for first := 0; first < len(sheets) && ctx.Err() == nil && used < webMaxLinkedCSSBytes; first += batchSize {
		last := min(first+batchSize, len(sheets))
		bodies := make([]string, last-first)
		var wg sync.WaitGroup
		for i := first; i < last; i++ {
			wg.Add(1)
			go func(i int, address string) {
				defer wg.Done()
				bodies[i-first] = webCSSBudgetPrefix(webCachedStylesheet(ctx, pageURL, address), webMaxLinkedCSSBytes)
			}(i, sheets[i].url)
		}
		wg.Wait()
		for offset, css := range bodies {
			remaining := webMaxLinkedCSSBytes - used - 1
			if css == "" || remaining <= 0 {
				continue
			}
			i := first + offset
			media := sheets[i].media
			if media != "" {
				// The wrapper's own bytes come out of the same budget.
				remaining -= len(media) + 16
			}
			// A sheet that does not fit whole still gives its complete leading
			// rules: the base and layout rules sit at the top of a theme.
			css = webCSSBudgetPrefix(css, remaining)
			if css == "" {
				continue
			}
			var out strings.Builder
			if media != "" {
				out.WriteString("@media ")
				out.WriteString(media)
				out.WriteString(" {\n")
				out.WriteString(css)
				out.WriteString("\n}\n")
			} else {
				out.WriteString(css)
				out.WriteString("\n")
			}
			result = append(result, webFetchedSheet{start: sheets[i].start, css: out.String()})
			used += out.Len()
		}
	}
	return result
}

// webSheetCacheKey is the context key of a page load's style sheet bodies.
type webSheetCacheKey struct{}

// webSheetCache holds the sheets one page load fetched, by address, so the
// preview drawn while the page's scripts run and the page itself share one
// download of each (browserRunScripts). pending holds the sheets being
// fetched: a second asker waits for that download (browserPrefetchSheets
// starts them before anyone asks).
type webSheetCache struct {
	mu      sync.Mutex
	css     map[string]string
	pending map[string]chan struct{}
}

// webWithSheetCache gives a page load's context a sheet cache.
func webWithSheetCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, webSheetCacheKey{}, &webSheetCache{css: make(map[string]string, 8)})
}

// webCachedStylesheet is fetchWebStylesheetContext through the load's sheet
// cache, when its context carries one, and the reader's memory of the sheets
// recent pages used (browser_subres.go).
func webCachedStylesheet(ctx context.Context, pageURL, address string) string {
	cache, _ := ctx.Value(webSheetCacheKey{}).(*webSheetCache)
	if cache == nil {
		return fetchWebStylesheetContext(ctx, pageURL, address)
	}
	cache.mu.Lock()
	if css, ok := cache.css[address]; ok {
		cache.mu.Unlock()
		return css
	}
	if wait, busy := cache.pending[address]; busy {
		cache.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return ""
		}
		cache.mu.Lock()
		css, ok := cache.css[address]
		cache.mu.Unlock()
		if ok {
			return css
		}
		// That download failed: one more try of this asker's own.
		return fetchWebStylesheetContext(ctx, pageURL, address)
	}
	if cache.pending == nil {
		cache.pending = make(map[string]chan struct{}, 8)
	}
	done := make(chan struct{})
	cache.pending[address] = done
	cache.mu.Unlock()
	css, remembered := browserSheetRecall(ctx, pageURL, address)
	if !remembered {
		css = fetchWebStylesheetContext(ctx, pageURL, address)
		browserSheetKeep(pageURL, address, css)
	}
	cache.mu.Lock()
	if css != "" {
		cache.css[address] = css
	}
	delete(cache.pending, address)
	cache.mu.Unlock()
	close(done)
	return css
}

// webLinkedSheet is one <link> stylesheet: its absolute address and the media
// query it is limited to ("" for all media).
type webLinkedSheet struct {
	url   string
	media string
	start int
}

// webLinkedStylesheets lists the page's linked stylesheets in document order,
// as a browser would apply them to a screen:
//   - rel="stylesheet", but not "alternate stylesheet" or a disabled link;
//   - a media query that cannot hold on this screen (media="print") is not
//     fetched, and any other query wraps the sheet in @media;
//   - the two common async-CSS loaders count as the stylesheet they turn into
//     once loaded: rel="preload" as="style" whose onload sets rel to
//     stylesheet, and media="print" whose onload sets media to all;
//   - commented-out markup is skipped.
func webLinkedStylesheets(source string, base *url.URL) []webLinkedSheet {
	var sheets []webLinkedSheet
	seen := make(map[string]bool)
	for pos := 0; pos < len(source) && len(sheets) < webMaxStylesheets; {
		lt := strings.IndexByte(source[pos:], '<')
		if lt < 0 {
			break
		}
		start := pos + lt
		if strings.HasPrefix(source[start:], "<!--") {
			end := strings.Index(source[start+4:], "-->")
			if end < 0 {
				pos = start + 4 // an unclosed comment hides nothing
				continue
			}
			pos = start + 4 + end + 3
			continue
		}
		// A script's or style's text is not markup: "<link" or "<!--" in a
		// string literal must neither add a sheet nor hide the real ones.
		if raw := webLinkScanRawTag(source, start); raw != "" {
			end := findTextBrowserTagEnd(source, start+1)
			if end < 0 {
				break
			}
			_, pos, _ = webRawElement(source, end+1, raw)
			continue
		}
		if len(source)-start < 5 || !strings.EqualFold(source[start+1:start+5], "link") ||
			(len(source)-start > 5 && isWebIdentByte(source[start+5])) {
			pos = start + 1
			continue
		}
		end := findTextBrowserTagEnd(source, start+1)
		if end < 0 {
			break
		}
		tag := source[start+1 : end]
		pos = end + 1
		rel := " " + strings.ToLower(strings.Join(strings.Fields(htmlAttrValue(htmlTagAttribute(tag, "rel"))), " ")) + " "
		onload := strings.ToLower(htmlAttrValue(htmlTagAttribute(tag, "onload")))
		switch {
		case strings.Contains(rel, " alternate "):
			continue
		case strings.Contains(rel, " stylesheet "):
		case strings.Contains(rel, " preload ") &&
			strings.EqualFold(strings.TrimSpace(htmlAttrValue(htmlTagAttribute(tag, "as"))), "style") &&
			strings.Contains(onload, "stylesheet"):
		default:
			continue
		}
		if htmlTagAttributeHas(strings.TrimSuffix(tag, "/"), "disabled") {
			continue
		}
		media := strings.TrimSpace(htmlAttrValue(htmlTagAttribute(tag, "media")))
		if strings.Contains(onload, "media") {
			// <link media="print" onload="this.media='all'">: a load-time swap.
			media = ""
		}
		if lm := strings.ToLower(media); lm == "all" || lm == "screen" || lm == "only screen" {
			media = ""
		}
		if media != "" && !webMediaBlockApplies(media) {
			continue
		}
		href := strings.TrimSpace(htmlAttrValue(htmlTagAttribute(tag, "href")))
		if href == "" {
			continue
		}
		ref, err := url.Parse(href)
		if err != nil {
			continue
		}
		abs := base.ResolveReference(ref)
		abs.Fragment = ""
		if abs.Scheme != "http" && abs.Scheme != "https" || seen[abs.String()] {
			continue
		}
		seen[abs.String()] = true
		sheets = append(sheets, webLinkedSheet{url: abs.String(), media: media, start: start})
	}
	return sheets
}

// webLinkScanRawTag names the raw-text element (script or style) whose start
// tag begins at source[start], or "".
func webLinkScanRawTag(source string, start int) string {
	for _, name := range [...]string{"script", "style"} {
		end := start + 1 + len(name)
		if end <= len(source) && strings.EqualFold(source[start+1:end], name) &&
			(end == len(source) || !isWebIdentByte(source[end]) && source[end] != '-') {
			return name
		}
	}
	return ""
}

func fetchWebStylesheet(pageURL, address string) string {
	return fetchWebStylesheetContext(context.Background(), pageURL, address)
}

func fetchWebStylesheetContext(parent context.Context, pageURL, address string) string {
	return fetchWebStylesheetDepth(parent, pageURL, address, map[string]bool{}, 0)
}

// fetchWebStylesheetDepth fetches one sheet and folds in the sheets it imports.
// CSS @import is how a site splits its base, theme and page rules; ignoring it
// left those pages with only their last sheet. Imports go before the importing
// sheet's own rules, as the cascade requires, and a media-qualified import is
// wrapped in @media so the reader's existing media evaluation applies.
func fetchWebStylesheetDepth(parent context.Context, pageURL, address string, seen map[string]bool, depth int) string {
	address = strings.TrimSpace(address)
	if parent.Err() != nil || address == "" || seen[address] || depth > webMaxStyleImportDepth || len(seen) >= webMaxStyleImports {
		return ""
	}
	// A stylesheet from an ad or analytics host is never part of what the
	// reader draws; skipping it saves the request, not just the bytes. A page
	// the user excepted fetches everything.
	if browserRequestBlocked(pageURL, address) {
		return ""
	}
	seen[address] = true
	ctx, cancel := context.WithTimeout(parent, 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return ""
	}
	browserChromeResource(req.Header, "style", browserAcceptStyle, browserURL(pageURL), req.URL)
	webFontsSheetRequest(req)
	// Same jar as the page: a sheet served only to a session (a CDN that
	// hands out a token with the HTML) has to see the page's cookie.
	client := browserHTTPClient(6 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	browserClientHintsNote(resp.Header, resp.Request.URL)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); ct != "" && !strings.Contains(ct, "css") {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, webSheetByteCap+1))
	if err != nil {
		return ""
	}
	// A sheet past the cap used to be dropped whole, and a site's main sheet
	// is the one most likely to be that large: the page then drew unstyled.
	// Its complete rules up to the cap are kept instead.
	css := webCSSBudgetPrefix(string(body), webSheetByteCap)
	if css == "" {
		return ""
	}
	css = webRebaseCSSURLs(css, resp.Request.URL)
	return webExpandCSSImports(parent, css, resp.Request.URL, pageURL, seen, depth)
}

// webExpandCSSImports prepends the sheets imported by css. It is shared by
// linked stylesheets and inline <style> blocks so both follow the same ad
// blocking, redirects, cookies, depth and cycle rules.
func webExpandCSSImports(parent context.Context, css string, base *url.URL, pageURL string, seen map[string]bool, depth int) string {
	if base == nil || depth >= webMaxStyleImportDepth || len(seen) >= webMaxStyleImports {
		return css
	}
	var imported strings.Builder
	for _, imp := range webCSSImports(css) {
		if len(seen) >= webMaxStyleImports {
			break
		}
		ref, err := url.Parse(imp.url)
		if err != nil {
			continue
		}
		abs := base.ResolveReference(ref)
		if abs.Scheme != "http" && abs.Scheme != "https" {
			continue
		}
		if imported.Len() >= webMaxLinkedCSSBytes {
			break // the page could not use more than this anyway
		}
		child := fetchWebStylesheetDepth(parent, pageURL, abs.String(), seen, depth+1)
		if child == "" {
			continue
		}
		remaining := webMaxLinkedCSSBytes - imported.Len() - 1
		if imp.media != "" {
			remaining -= len(imp.media) + 16
		}
		if imp.layered {
			remaining -= len(imp.layer) + 16
		}
		child = webCSSBudgetPrefix(child, remaining)
		if child == "" {
			continue
		}
		if imp.layered {
			// The sheet's rules belong to that cascade layer.
			child = "@layer " + imp.layer + " {\n" + child + "\n}"
		}
		if imp.media != "" {
			imported.WriteString("@media ")
			imported.WriteString(imp.media)
			imported.WriteString(" {\n")
			imported.WriteString(child)
			imported.WriteString("\n}\n")
		} else {
			imported.WriteString(child)
			imported.WriteString("\n")
		}
	}
	if imported.Len() == 0 {
		return css
	}
	// The sheet's `@layer a, b;` order statements go in front of what it
	// imports, so the layers keep the order it states.
	return webLeadingLayerStatements(css) + imported.String() + css
}

// webLeadingLayerStatements returns the `@layer a, b;` statements at the head
// of a sheet, before its first rule (between @charset and @import).
func webLeadingLayerStatements(css string) string {
	var out strings.Builder
	rest := stripWebCSSComments(css)
	for {
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, "@") {
			break
		}
		semi := strings.IndexByte(rest, ';')
		if brace := strings.IndexByte(rest, '{'); semi < 0 || (brace >= 0 && brace < semi) {
			break
		}
		if statement := rest[:semi+1]; len(statement) > len("@layer") && strings.EqualFold(statement[:len("@layer")], "@layer") {
			out.WriteString(statement)
			out.WriteByte('\n')
		}
		rest = rest[semi+1:]
	}
	return out.String()
}

// webCSSImport is one @import target plus its optional media query and
// cascade layer. The import statement itself stays in the CSS (the parser
// ignores it), but the fetched body is inserted before the sheet that asked
// for it, inside `@layer` and `@media` blocks that say the same thing.
type webCSSImport struct {
	url   string
	media string
	// layered is set by a layer or layer(name) modifier; layer is the name,
	// empty for an anonymous layer.
	layer   string
	layered bool
}

// webCSSImports reads the @import rules in a stylesheet. Comments are removed
// before scanning, so a commented-out import cannot trigger a request. Only
// url(...) and quoted string forms are accepted. A layer / layer(name)
// modifier puts the sheet in that cascade layer (Tailwind 4 imports its parts
// that way); a supports() condition is left to the normal parser fallback
// rather than guessed at.
func webCSSImports(css string) []webCSSImport {
	stripped := stripWebCSSComments(css)
	lower := strings.ToLower(stripped)
	var out []webCSSImport
	for pos := 0; pos < len(stripped) && len(out) < webMaxStyleImports; {
		rel := strings.Index(lower[pos:], "@import")
		if rel < 0 {
			break
		}
		start := pos + rel
		after := start + len("@import")
		if after < len(stripped) && (isWebIdentByte(stripped[after]) || stripped[after] == '-') {
			pos = after
			continue
		}
		end := webCSSImportEnd(stripped, after)
		if end < 0 {
			break
		}
		rest := strings.TrimSpace(stripped[after:end])
		pos = end + 1
		if rest == "" {
			continue
		}
		var target, media string
		if strings.HasPrefix(strings.ToLower(rest), "url(") {
			close := webCSSParenEnd(rest, 3)
			if close < 0 {
				continue
			}
			target = strings.TrimSpace(rest[4:close])
			media = strings.TrimSpace(rest[close+1:])
		} else if rest[0] == '\'' || rest[0] == '"' {
			quote := rest[0]
			i := 1
			for i < len(rest) && rest[i] != quote {
				if rest[i] == '\\' && i+1 < len(rest) {
					i++
				}
				i++
			}
			if i >= len(rest) {
				continue
			}
			target = rest[1:i]
			media = strings.TrimSpace(rest[i+1:])
		} else {
			continue
		}
		target = strings.TrimSpace(strings.Trim(target, "\"'"))
		if target == "" {
			continue
		}
		imp := webCSSImport{url: target}
		lowerMedia := strings.ToLower(media)
		switch {
		case strings.HasPrefix(lowerMedia, "layer("):
			close := webCSSParenEnd(media, len("layer"))
			if close < 0 {
				continue
			}
			imp.layer, imp.layered = strings.TrimSpace(media[len("layer("):close]), true
			media = strings.TrimSpace(media[close+1:])
		case lowerMedia == "layer" || strings.HasPrefix(lowerMedia, "layer "):
			imp.layered = true
			media = strings.TrimSpace(media[len("layer"):])
		}
		if strings.Contains(strings.ToLower(media), "supports(") || strings.Contains(strings.ToLower(media), "layer(") {
			continue
		}
		imp.media = media
		out = append(out, imp)
	}
	return out
}

// webCSSImportEnd finds the semicolon ending an @import, skipping quoted values
// and parentheses so a URL query string cannot cut it short.
func webCSSImportEnd(css string, start int) int {
	var quote byte
	depth := 0
	for i := start; i < len(css); i++ {
		c := css[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ';':
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func isWebIdentByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// webRebaseCSSURLs resolves url(...) references against the stylesheet that
// declared them. Page CSS almost always lives in /assets/css/, so resolving
// `url(../img/hero.jpg)` against the page URL pointed at a file that does not
// exist and left the hero blank. Inline style blocks keep the page URL because
// they never pass through this function.
func webRebaseCSSURLs(css string, base *url.URL) string {
	if base == nil || css == "" || !strings.Contains(strings.ToLower(css), "url(") {
		return css
	}
	var out strings.Builder
	out.Grow(len(css))
	last := 0
	for pos := 0; pos < len(css); {
		rel := webIndexFold(css[pos:], "url(")
		if rel < 0 {
			break
		}
		start := pos + rel
		open := start + len("url(")
		close := webCSSParenEnd(css, start+3)
		if close < 0 {
			break
		}
		inner := strings.TrimSpace(css[open:close])
		quote := byte(0)
		if len(inner) >= 2 && (inner[0] == '\'' || inner[0] == '"') && inner[len(inner)-1] == inner[0] {
			quote = inner[0]
			inner = strings.TrimSpace(inner[1 : len(inner)-1])
		}
		lower := strings.ToLower(inner)
		if inner == "" || strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "http:") ||
			strings.HasPrefix(lower, "https:") || strings.HasPrefix(lower, "//") || strings.HasPrefix(inner, "#") {
			pos = close + 1
			continue
		}
		ref, err := url.Parse(inner)
		if err != nil {
			pos = close + 1
			continue
		}
		resolved := base.ResolveReference(ref).String()
		out.WriteString(css[last:open])
		if quote != 0 {
			out.WriteByte(quote)
		}
		out.WriteString(resolved)
		if quote != 0 {
			out.WriteByte(quote)
		}
		last = close
		pos = close + 1
	}
	if last == 0 {
		return css
	}
	out.WriteString(css[last:])
	return out.String()
}

func webIndexFold(source, sub string) int {
	for i := 0; i+len(sub) <= len(source); i++ {
		if strings.EqualFold(source[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

// webRawElement returns the raw text of a style/title/script-like element and
// the offset just past its closing tag. An <svg> may nest further <svg>s, so
// it ends at its matching close tag (webSVGElementEnd).
func webRawElement(source string, start int, tag string) (string, int, bool) {
	if tag == "svg" {
		if bodyEnd, next, ok := webSVGElementEnd(source, start); ok {
			return source[start:bodyEnd], next, true
		}
	}
	closing := "</" + tag
	idx := webIndexFold(source[start:], closing)
	if idx < 0 {
		return source[start:], len(source), true
	}
	body := source[start : start+idx]
	end := start + idx + len(closing)
	if gt := strings.IndexByte(source[end:], '>'); gt >= 0 {
		end += gt + 1
	} else {
		end = len(source)
	}
	return body, end, true
}

func parseWebTag(raw string) (name string, closing, selfClose bool, attrs string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false, false, ""
	}
	if raw[0] == '/' {
		closing = true
		raw = strings.TrimSpace(raw[1:])
	}
	if strings.HasSuffix(raw, "/") {
		selfClose = true
		raw = strings.TrimSpace(raw[:len(raw)-1])
	}
	end := 0
	for end < len(raw) && webSelectorNameByte(raw[end]) {
		end++
	}
	name = strings.ToLower(raw[:end])
	attrs = raw[end:]
	return name, closing, selfClose, attrs
}

func webVoidElement(tag string) bool {
	switch tag {
	case "br", "hr", "img", "meta", "link", "input", "area", "base", "col",
		"embed", "param", "source", "track", "wbr":
		return true
	}
	return false
}

// webHeadContentElement lists what may stand in <head>; any other start tag
// closes an open one (startTag).
func webHeadContentElement(tag string) bool {
	switch tag {
	case "base", "basefont", "bgsound", "link", "meta", "title", "style", "script", "noscript", "template":
		return true
	}
	return false
}

func webRawTextElement(tag string) bool {
	switch tag {
	case "script", "noscript", "svg", "iframe", "template", "canvas", "video", "audio", "object":
		return true
	}
	return false
}

// webPlaceholderElement lists replaced content this reader cannot rasterize.
// Their fallback text is not laid out; the element itself keeps a box. An
// <svg> is drawn instead (walk → walkSVG).
func webPlaceholderElement(tag string) bool {
	switch tag {
	case "canvas", "object", "embed", "audio":
		return true
	}
	return false
}

func (p *webParser) addPlaceholderBlock(name, attrs string) {
	if p.inHead {
		return
	}
	style := p.computeStyle(webNewElement(name, attrs), attrs)
	if style.Hidden {
		return
	}
	w := htmlTagAttributeFloat(attrs, "width")
	h := htmlTagAttributeFloat(attrs, "height")
	if style.Width <= 0 && style.WidthPct <= 0 && w > 0 {
		style.Width = w
	}
	if style.Height <= 0 && h > 0 {
		style.Height = h
	}
	if style.Width <= 0 && style.WidthPct <= 0 && style.MaxWidth <= 0 && style.MaxWidthPct <= 0 {
		style.Width = 300
	}
	if style.Height <= 0 && style.MinHeight <= 0 && style.MaxHeight <= 0 {
		style.Height = 150
		if name == "audio" {
			style.Height = 40
		}
	}
	p.flush()
	p.beginFlexLeaf(style)
	block := p.ensureBlock()
	block.Style = style
	block.Align = style.Align
	block.Indent = style.Indent
	block.Placeholder = true
	block.HasBg, block.Background = style.HasBg, style.Background
	block.BoxBorder = style.BoxBorder
	block.BorderLeft, block.BorderRight = style.BorderLeft, style.BorderRight
	block.BorderTop, block.BorderBottom = style.BorderTop, style.BorderBottom
	block.BorderWidth, block.BorderStyle = style.BorderWidth, style.BorderStyle
	block.BorderSideWidth = style.BorderSideWidth
	block.BorderSideStyle, block.BorderSideStyleSet = style.BorderSideStyle, style.BorderSideStyleSet
	block.BorderSideColor, block.BorderSideHasColor = style.BorderSideColor, style.BorderSideHasColor
	block.BorderColor, block.HasBorderColor = style.BorderColor, style.HasBorderColor
	p.flush()
}

func webBlockElement(tag string) bool {
	switch tag {
	case "html", "body", "p", "div", "section", "article", "main", "header", "footer",
		"aside", "nav", "form", "fieldset", "figure", "figcaption", "address",
		"dl", "dt", "dd", "li", "tr", "details", "summary", "blockquote", "pre",
		"h1", "h2", "h3", "h4", "h5", "h6", "table", "caption", "thead",
		"tbody", "tfoot", "td", "th", "menu", "hgroup", "dialog", "search",
		"center", "listing":
		return true
	}
	return false
}

// webCollectInlineCSS appends the page's own <style> blocks to b, in document
// order and within webMaxCSSBytes, reading the markup exactly as walk does:
// comments, <!…> and <?…> are skipped, and so is the content of every element
// walk treats as raw text (title, style, and webRawTextElement's list), so a
// <style> inside <noscript>, <template> or an <svg> stays out, as it always
// has. Each block is preceded by a newline, the separator walk used.
func webCollectInlineCSS(source string, b *strings.Builder) {
	for _, block := range webInlineCSSBlocks(source) {
		if b.Len()+1 >= webMaxCSSBytes {
			continue
		}
		// A large site can ship megabytes of component CSS in one block.
		// Skipping that block entirely left its visible cards unstyled; keep
		// complete rules from its prefix up to the handheld's budget.
		remaining := webMaxCSSBytes - b.Len() - 1
		inner := webCSSBudgetPrefix(block.css, remaining)
		if inner != "" {
			b.WriteString("\n")
			b.WriteString(inner)
		}
	}
}

// webCollectOrderedCSS combines linked and inline CSS at their positions in
// the source. Rules with equal specificity then follow the page's cascade
// instead of giving every inline block priority over every linked sheet.
func webCollectOrderedCSS(source string, sheets []webFetchedSheet, b *strings.Builder) {
	inline := webInlineCSSBlocks(source)
	for i, j := 0, 0; i < len(inline) || j < len(sheets); {
		var css string
		if j < len(sheets) && (i >= len(inline) || sheets[j].start < inline[i].start) {
			css = sheets[j].css
			j++
		} else {
			css = inline[i].css
			i++
		}
		if b.Len()+1 >= webMaxCSSBytes {
			break
		}
		css = webCSSBudgetPrefix(css, webMaxCSSBytes-b.Len()-1)
		if css != "" {
			b.WriteString("\n")
			b.WriteString(css)
		}
	}
}

// webInlineCSSBlock is one inline <style> body and the byte range of that body
// in the page source. Keeping the range lets import expansion rewrite only the
// CSS while leaving the markup exactly as the parser saw it.
type webInlineCSSBlock struct {
	start, end int
	css        string
}

// webInlineCSSBlocks finds the page's own <style> bodies in document order,
// using the same skip rules as the collector and walk: comments, declarations
// and raw-text elements (noscript, template, svg, ...) do not contribute CSS.
func webInlineCSSBlocks(source string) []webInlineCSSBlock {
	var blocks []webInlineCSSBlock
	for i := 0; i < len(source); {
		lt := strings.IndexByte(source[i:], '<')
		if lt < 0 {
			break
		}
		i += lt
		if strings.HasPrefix(source[i:], "<!--") {
			end := strings.Index(source[i+4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		if strings.HasPrefix(source[i:], "<!") || strings.HasPrefix(source[i:], "<?") {
			end := strings.IndexByte(source[i:], '>')
			if end < 0 {
				break
			}
			i += end + 1
			continue
		}
		end := findTextBrowserTagEnd(source, i+1)
		if end < 0 {
			break
		}
		name, closing, _, _ := parseWebTag(source[i+1 : end])
		i = end + 1
		if name == "" || closing {
			continue
		}
		if name == "title" || name == "style" || webRawTextElement(name) {
			inner, next, _ := webRawElement(source, i, name)
			if name == "style" {
				blocks = append(blocks, webInlineCSSBlock{start: i, end: i + len(inner), css: inner})
			}
			i = next
		}
	}
	return blocks
}

// webExpandInlineCSSImports rewrites each inline <style> body with its imported
// sheets in front. Every block is expanded independently, so an import in a
// later block cannot move ahead of an earlier block's rules. The imported
// sheets themselves go through the linked-sheet fetcher.
// Test seam (ARCH-8): only tests call it.
func webExpandInlineCSSImports(source string, pageURL *url.URL) string {
	return webExpandInlineCSSImportsContext(context.Background(), source, pageURL)
}

func webExpandInlineCSSImportsContext(ctx context.Context, source string, pageURL *url.URL) string {
	if pageURL == nil || webIndexFold(source, "<style") < 0 {
		return source
	}
	blocks := webInlineCSSBlocks(source)
	if len(blocks) == 0 {
		return source
	}
	var out strings.Builder
	out.Grow(len(source))
	last := 0
	for _, block := range blocks {
		// Each block gets the same fresh budget a linked sheet gets, so one
		// block's imports cannot starve another's.
		seen := make(map[string]bool)
		expanded := webExpandCSSImports(ctx, block.css, pageURL, pageURL.String(), seen, 0)
		out.WriteString(source[last:block.start])
		out.WriteString(expanded)
		last = block.end
	}
	out.WriteString(source[last:])
	return out.String()
}

// webCSSBudgetPrefix keeps whole rules when one inline sheet exceeds the
// handheld budget. If the cut lands inside @media or @supports, close the
// enclosing groups so the parser can still use the complete rules before it.
func webCSSBudgetPrefix(css string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(css) <= limit {
		return css
	}
	// Leave a few bytes for the open groups at the last complete rule.
	scan := limit - 16
	if scan <= 0 {
		return ""
	}
	var quote byte
	comment := false
	depth, last, lastDepth := 0, 0, 0
	for i := 0; i < scan; i++ {
		c := css[i]
		if comment {
			if c == '*' && i+1 < scan && css[i+1] == '/' {
				comment = false
				i++
			}
			continue
		}
		if quote != 0 {
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '/':
			if i+1 < scan && css[i+1] == '*' {
				comment = true
				i++
			}
		case '\'', '"':
			quote = c
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
				last, lastDepth = i+1, depth
			}
		}
	}
	if last == 0 || lastDepth > 16 {
		return ""
	}
	return css[:last] + strings.Repeat("}", lastDepth)
}

func (p *webParser) walk(source string) {
	for i := 0; i < len(source); {
		if source[i] != '<' {
			end := strings.IndexByte(source[i:], '<')
			if end < 0 {
				end = len(source)
			} else {
				end += i
			}
			p.addText(source[i:end])
			i = end
			continue
		}
		if strings.HasPrefix(source[i:], "<!--") {
			// An ad was cut out here (webpage_adremove.go).
			if strings.HasPrefix(source[i:], webAdCutMarker) {
				p.noteAdCut()
			}
			if end := strings.Index(source[i+4:], "-->"); end >= 0 {
				i += 4 + end + 3
			} else {
				i = len(source)
			}
			continue
		}
		if strings.HasPrefix(source[i:], "<!") || strings.HasPrefix(source[i:], "<?") {
			if end := strings.IndexByte(source[i:], '>'); end >= 0 {
				i += end + 1
			} else {
				i = len(source)
			}
			continue
		}
		end := findTextBrowserTagEnd(source, i+1)
		if end < 0 {
			break
		}
		raw := source[i+1 : end]
		i = end + 1
		name, closing, selfClose, attrs := parseWebTag(raw)
		if name == "" {
			continue
		}
		if closing {
			p.endTag(name)
			continue
		}
		// An inline <svg> is captured whole and drawn as an image
		// (webpage_svg.go). It is seen even under a hidden parent: a sprite
		// sheet parked in a display:none wrapper still defines the icons.
		if name == "svg" {
			i = p.walkSVG(source, end-len(raw)-1, i, attrs, selfClose)
			continue
		}
		// Raw elements bypass startTag. When their parent is hidden, consume
		// their bodies without creating a placeholder or a media card.
		if p.topStyle().Hidden && (name == "title" || name == "style" || webRawTextElement(name)) {
			_, i, _ = webRawElement(source, i, name)
			continue
		}
		if p.topStyle().Hidden && webPlaceholderElement(name) {
			continue
		}
		if name == "audio" && !selfClose {
			// An <audio> with a source is a play card the player opens; one
			// without stays the grey bar it always was (addVideoBlock).
			inner, next, _ := webRawElement(source, i, name)
			i = next
			p.addVideoBlock(name, attrs, inner)
			continue
		}
		if webPlaceholderElement(name) {
			p.addPlaceholderBlock(name, attrs)
			if !selfClose && !webVoidElement(name) {
				_, next, _ := webRawElement(source, i, name)
				i = next
			}
			continue
		}
		if name == "title" || name == "style" || webRawTextElement(name) {
			inner, next, _ := webRawElement(source, i, name)
			i = next
			switch name {
			case "title":
				if p.doc.Title == "" {
					p.doc.Title = cleanTextBrowserHTML(inner)
				}
			case "style":
				// Already in p.rules: webCollectInlineCSS gathered every block
				// before the walk began.
			case "video", "iframe":
				p.addVideoBlock(name, attrs, inner)
			}
			continue
		}
		p.startTag(name, attrs, selfClose)
	}
}

func (p *webParser) startTag(name, attrs string, selfClose bool) {
	if name == "head" {
		p.inHead = true
		el := webNewElement(name, attrs)
		if p.selTreePos < len(p.selTree) {
			el.node = p.selTree[p.selTreePos]
			p.selTreePos++
		}
		p.push(el, webBaseStyle(), "skip", attrs)
		return
	}
	// An unclosed <head> ends at the first element that cannot live in it, as
	// HTML's parser has it. Left open, its frame (styled from the base, not
	// from <html>) sat between <html> and the body, so the body inherited
	// nothing from <html> — not the custom properties of a :root rule either
	// — and a strict `html > body` missed.
	if p.inHead && !webHeadContentElement(name) {
		p.endTag("head")
		p.inHead = false
	}
	if name == "body" || name == "html" {
		p.inHead = false
	}
	el := webNewElement(name, attrs)
	if p.selTreePos < len(p.selTree) {
		el.node = p.selTree[p.selTreePos]
		p.selTreePos++
	}
	olIndex := -1
	if name == "ol" {
		olIndex = p.olSeq
		p.olSeq++
	}
	el.target = p.target != "" && el.id == p.target
	style := p.computeStyle(el, attrs)
	// A screen-reader-only or fully transparent positioned block shows nothing
	// in a browser; the reader painted it over the page (webpage_layers.go).
	if !style.Hidden && webLayerHiddenBlock(name, &style) {
		style.Hidden = true
	}
	// A hidden subtree has no layout boxes or controls. Keeping only its
	// element frames lets matching end tags restore the visible parent, while
	// descendants (including form fields) cannot leak into the page.
	if style.Hidden {
		p.pendingBefore, p.pendingBeforeStyle = "", webStyle{}
		// Hidden inputs still travel with a visible form submission even when
		// a site parks them inside a display:none wrapper.
		if name == "input" && strings.EqualFold(strings.TrimSpace(htmlTagAttribute(attrs, "type")), "hidden") && p.form.ID != 0 {
			p.addControlFromInput(attrs, style)
		}
		if !selfClose && !webVoidElement(name) {
			p.stack = append(p.stack, webFrame{el: el, style: style, kind: "skip", attrs: attrs})
		}
		return
	}
	if style.FloatSide != 0 {
		p.floatsSeen++
	}
	if name == "picture" {
		p.pictures = append(p.pictures, webPictureFrame{})
	}
	p.prepareTableStyle(name, &style, attrs)
	// Counter operations belong to the element that declares them and must be
	// in force before its ::before/::marker content is resolved. Increments
	// stay in the enclosing scope for following siblings; resets are recorded
	// on the frame and popped by its end tag.
	p.applyCounters(name, style)
	p.pendingBefore, p.pendingBeforeStyle = "", webStyle{}
	beforeSet := false
	if p.pseudoMask&(1<<webPseudoElemBefore) != 0 {
		if generated := p.pseudoContent(el, attrs, style, webPseudoElemBefore); generated.Matched && generated.HasContent {
			p.pendingBefore, p.pendingBeforeStyle = generated.Text, generated.Style
			beforeSet = true
		}
	}
	// ::first-letter of a block container (a drop cap): its first letter,
	// generated ::before text included, is set in the pseudo-element's style.
	if p.pseudoMask&(1<<webPseudoElemFirstLetter) != 0 && webFirstLetterHost(name, &style) && !selfClose {
		if generated := p.pseudoContent(el, attrs, style, webPseudoElemFirstLetter); generated.Matched && !generated.Style.Hidden {
			p.firstLetter = &webFirstLetter{style: generated.Style, depth: len(p.stack)}
		}
	}
	// <q> is quoted by the UA sheet's `q::before { content: open-quote }`,
	// unless the page's own ::before content replaces it (a reset's
	// `content: none` included).
	if name == "q" && !beforeSet {
		p.pendingBefore, p.pendingBeforeStyle = "“", style
	}
	if name == "body" || name == "html" {
		if style.HasBg {
			// The paper is opaque: a translucent page colour is what it shows
			// over the white canvas (rgba(0,0,0,.02) is a faint grey, not black).
			p.doc.Paper = webColorOver(style.Background, p.doc.Paper)
			p.doc.HasPaperGradient = style.HasGradient && style.Gradient != nil
			if p.doc.HasPaperGradient {
				p.doc.PaperGradient = *style.Gradient
			}
		}
		if style.HasColor {
			p.doc.Text = style.Color
		}
	}

	// <details> is a section a page folds away: a browser draws the summary and
	// hides the body until it is opened. Without this the reader laid out every
	// collapsed log, changelog and screenshot, so an issue whose evidence sat in
	// a closed <details> read as the evidence. The body is parsed and kept; only
	// the layout skips it.
	if name == "details" {
		p.flush()
		ctrl := &webControl{Kind: "details", Checked: htmlTagAttributeHas(attrs, "open")}
		// The marker is emitted before the section is pushed, so its block is
		// never tagged as the section's body: it has to stay visible, and the
		// summary's own text then joins it on the same line.
		p.emitControl(ctrl, style, false)
		p.details = append(p.details, webDetailsFrame{ctrl: ctrl})
		if selfClose {
			p.flush()
			// Popped by index, not by a byte slice: the stack holds frames, not
			// text, and the project's byte-slice rule exists to stop text edits
			// from cutting a rune in half.
			if n := len(p.details); n > 0 {
				p.details = p.details[:n-1]
			}
			return
		}
		p.push(el, style, "block", attrs)
		return
	}
	if name == "summary" {
		if n := len(p.details); n > 0 {
			// No flush: the summary continues the line the marker opened.
			p.details[n-1].inSummary = true
			p.push(el, style, "block", attrs)
			return
		}
	}

	// A control whose closing tag never arrived ends at the next element that
	// starts a new context, so one malformed field cannot eat the whole page.
	// Its own children are not that context: an <option> belongs to the open
	// <select>, and closing there would lose every option on the page.
	if p.textarea != nil && name != "textarea" && webControlBoundary(name) {
		p.endTextarea()
	}
	if p.selectCtrl != nil && name != "option" && name != "select" && webControlBoundary(name) {
		p.endSelect()
	}
	if p.buttonCtrl != nil && name != "button" && webControlBoundary(name) {
		p.endButton()
	}

	if name == "form" {
		// A form is where a filled control goes. A missing action means "this
		// page", the same convention a browser uses. The ID is what keeps two
		// forms with the same action from sharing a submit.
		p.formSeq++
		p.form = webForm{
			ID:      p.formSeq,
			Action:  htmlAttrValue(htmlTagAttribute(attrs, "action")),
			Method:  strings.ToLower(htmlAttrValue(htmlTagAttribute(attrs, "method"))),
			Enctype: strings.ToLower(htmlAttrValue(htmlTagAttribute(attrs, "enctype"))),
		}
	}

	switch name {
	case "br":
		p.addBreak()
		// <br clear="all"> ends the paragraph below the floats beside it
		// (webpage_floats.go).
		if c := webBreakClear(style.Clear, attrs); c != 0 && p.floatsSeen > 0 {
			p.flush()
			p.clearMarker(c, 0, len(p.stack))
		}
		return
	case "hr":
		p.flush()
		p.beginFlexLeaf(style)
		// The rule keeps its authored colour and weight: `hr { border: 3px
		// solid #c00 }` used to paint the reader's grey hairline as if the
		// sheet had said nothing, which is the one thing it did say.
		rule := webBlock{
			Rule:           true,
			MarginTop:      style.MarginTop,
			MarginBot:      style.MarginBot,
			BorderColor:    style.BorderColor,
			HasBorderColor: style.HasBorderColor,
			BorderWidth:    style.BorderWidth,
			HasBg:          style.HasBg,
			Background:     style.Background,
			Indent:         style.Indent,
		}
		// The rule's own box: its width (a width attribute or rule) and right
		// margin bound the bar, and visibility hides it. Nothing else of the
		// element's style applies, so it never floats or turns into a flex box.
		rule.Style = webStyle{
			FontPx: style.FontPx, Width: style.Width, WidthPct: style.WidthPct,
			MinWidth: style.MinWidth, MinWidthPct: style.MinWidthPct, MinWidthAuto: true,
			MaxWidth: style.MaxWidth, MaxWidthPct: style.MaxWidthPct, BoxSizing: style.BoxSizing,
			IndentRight: style.IndentRight, Invisible: style.Invisible, FlexShrink: 1,
		}
		// hr { margin: auto }: a narrower rule sits in the middle (layoutBlocks
		// reads Align for a rule).
		if style.MarginLeftAuto && style.MarginRightAuto {
			rule.Align = 1
		}
		p.tagFlexBlock(&rule, style)
		rule.Details, rule.DetailsShown = p.detailsTag()
		rule.Layer, rule.TreeSeq = p.openLayerIn(len(p.stack)), p.blockFrameSeq()
		rule.Float = rule.Layer != 0
		rule.FloatGroup = p.openFloatIn(len(p.stack))
		p.blocks = append(p.blocks, rule)
		return
	case "source":
		// <source> is only meaningful inside <picture>. Outside one it is
		// the void element the reader has always skipped.
		if len(p.pictures) > 0 {
			p.considerPictureSource(attrs)
		}
		return
	case "img":
		if p.inHead {
			return
		}
		alt := strings.TrimSpace(htmlTagAttribute(attrs, "alt"))
		// A <picture> source wins when one matched. Otherwise srcset is how
		// responsive pages offer several renditions of one image; choose the
		// one that covers the size this image will actually render at.
		resolved := p.webImageSource(attrs)
		if resolved == "" {
			// A dead or undecodable src keeps its alt text; a loadable one
			// becomes a real image block below.
			if alt != "" {
				p.addText(" " + alt + " ")
			}
			return
		}
		// An image block, set inside its line when it is inline-level
		// (webpage_inline.go).
		attrW, attrH := webImageAttrSize(&style, attrs)
		p.addImageElement(style, resolved, htmlAttrValue(alt), attrW, attrH)
		return
	case "input":
		// A form field is content, not a void tag to skip: the reader used to
		// drop it entirely, which left a search page with no search box at all.
		if p.inHead {
			return
		}
		if !strings.EqualFold(strings.TrimSpace(htmlTagAttribute(attrs, "type")), "hidden") {
			p.beginFlexLeaf(style)
		}
		p.addControlFromInput(attrs, style)
		return
	case "button":
		if p.inHead {
			return
		}
		kind := strings.ToLower(strings.TrimSpace(htmlTagAttribute(attrs, "type")))
		if kind == "" || kind == "submit" {
			p.beginFlexLeaf(style)
			p.startButton(attrs, style)
			if selfClose {
				p.endButton()
			}
			return
		}
	case "textarea":
		p.beginFlexLeaf(style)
		p.startTextarea(attrs, style)
		return
	case "select":
		p.beginFlexLeaf(style)
		p.startSelect(attrs)
		return
	case "option":
		p.startOption(attrs)
		return
	}
	if webVoidElement(name) {
		return
	}
	if name == "ul" || name == "ol" {
		state := webListState{ordered: name == "ol"}
		if style.DisplayInline && !p.flexItemBlockifies() {
			// `display: inline` on the list itself (Wikipedia's
			// `.hlist.inline ul`: "Recently featured: A · B · C", "Ongoing:
			// …") keeps it in the line it starts in. It used to end that line
			// and start one of its own; its inline items still number.
			p.lists = append(p.lists, state)
			p.pushInline(el, style, attrs, selfClose, name)
			return
		}
		if !style.DisplayInlineBlock || !p.openInlineRow(style) {
			p.closeInlineRow()
		}
		p.flush()
		// <ol start="5"> counts from 5; <ol reversed> counts down, from its
		// item count unless a start says otherwise.
		state.reversed = state.ordered && htmlTagAttributeHas(attrs, "reversed")
		if state.reversed && olIndex >= 0 && olIndex < len(p.olCounts) {
			state.n = p.olCounts[olIndex] + 1
		}
		if v, ok := el.attr("start"); state.ordered && ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > -1<<20 && n < 1<<20 {
				state.n = n - 1
				if state.reversed {
					state.n = n + 1
				}
			}
		}
		p.lists = append(p.lists, state)
		p.push(el, style, "list", attrs)
		return
	}
	if name == "li" && (style.DisplayInline || style.DisplayInlineBlock) {
		// An inline or inline-block item is not a list item: no marker, no
		// number. Inline-block items sit side by side in a wrapping row
		// (webpage_inline.go), which is how nav bars, pagination and tag lists
		// are built; inline items flow in the line like any inline element.
		// Inside a flex or grid list either kind is simply one of its items.
		if style.DisplayInlineBlock {
			p.openInlineRow(style)
		}
		if style.DisplayInlineBlock || p.parentIsFlexContainer() {
			p.dropListGutter(&style)
			p.flush()
			p.push(el, style, "block", attrs)
			return
		}
		// The reader's list gutter (18px, applyWebTagDefaults) is a list
		// item's, not room before an inline one (webpage_inlinebox.go).
		style.Indent = math.Max(0, style.Indent-18)
		p.pushInline(el, style, attrs, selfClose, name)
		return
	}
	if name == "li" {
		p.dropListGutter(&style)
		p.closeInlineRow()
		p.flush()
		// <li value="10"> renumbers its ordered list from this item on.
		if n := len(p.lists); n > 0 && p.lists[n-1].ordered {
			if v, ok := el.attr("value"); ok {
				if k, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && k > -1<<20 && k < 1<<20 {
					p.lists[n-1].n = k - 1
					if p.lists[n-1].reversed {
						p.lists[n-1].n = k + 1
					}
				}
			}
		}
		// Only a list item draws a marker and counts: `li { display: block }`
		// and a flex or grid <li> (Wikipedia's sister projects) have none.
		p.pendingB = ""
		if style.ListItem {
			p.pendingB = p.nextBullet(style)
		}
		p.pendingBStyle, p.hasPendingBStyle = webStyle{}, false
		// A list whose page sets its start padding hangs its markers in that
		// padding (list-style-position: outside), as a browser does.
		p.pendingHang = p.listMarkerHangs(style)
		// A ::marker rule can replace the bullet and style it. The list
		// counter still advances above, so ordered items keep their numbers.
		if style.ListItem && p.pseudoMask&(1<<webPseudoElemMarker) != 0 {
			if generated := p.pseudoContent(el, attrs, style, webPseudoElemMarker); generated.Matched {
				p.pendingBStyle, p.hasPendingBStyle = generated.Style, true
				if generated.HasContent {
					p.pendingB = generated.Text
				}
			}
		}
		p.push(el, style, "block", attrs)
		return
	}
	if style.DisplayContents {
		p.push(el, style, "contents", attrs)
		if selfClose {
			p.endTag(name)
		}
		return
	}
	if (webBlockElement(name) && !style.DisplayInline) || style.DisplayBlock || style.Display >= webDisplayTable {
		// A block-level inline-block (a card, a button row item) joins the
		// wrapping row its siblings form; any other block ends that row and
		// lays out full width below it.
		if !(webBlockElement(name) && style.DisplayInlineBlock && p.openInlineRow(style)) {
			p.closeInlineRow()
		}
		p.flush()
		if style.ListItem && name != "li" {
			// Any element styled `display: list-item` draws a marker too.
			p.pendingB = webListMarker(style.ListStyle, style.ListString, false, 1)
			if len(p.lists) > 0 {
				p.pendingB = p.nextBullet(style)
			} else if style.ListNone {
				p.pendingB = ""
			}
			p.pendingBStyle, p.hasPendingBStyle = webStyle{}, false
		}
		p.push(el, style, "block", attrs)
		if selfClose {
			p.endTag(name)
		}
		return
	}
	if p.flexItemBlockifies() {
		// The children of a flex or grid container are blockified (CSS
		// Display 3): a link, a span or a label straight inside a flex row is a
		// box with its own padding, background, border and radius — the
		// buttons, pills and badges of most toolbars — not a chip of text that
		// takes the row's measure.
		p.flush()
		p.push(el, style, "block", attrs)
		if selfClose {
			p.endTag(name)
		}
		return
	}
	if p.detachesInlineLayer(name, &style) {
		// An absolutely positioned inline element leaves its line too (CSS
		// 2.1 §9.7): a badge or a label over a card's photo is a layer at its
		// corner, not a word in the text (webpage_layers.go).
		p.detachInlineLayer(el, style, attrs, selfClose, name)
		return
	}
	p.pushInline(el, style, attrs, selfClose, name)
}

// flexItemBlockifies reports whether the element about to open is a direct
// child of a real flex or grid container (not the reader's implicit row of
// inline-block boxes), which makes it a block-level item whatever its display.
func (p *webParser) flexItemBlockifies() bool {
	n := len(p.flexStack)
	if n == 0 {
		return false
	}
	top := &p.flexStack[n-1]
	return !top.implicit && p.flexDirectChild(top, len(p.stack))
}

// pushInline opens an inline element. A nested inline element with no
// background of its own paints its parent's (`<mark>a <b>b</b></mark>` is one
// highlight), and its horizontal margin, border and padding are the room they
// take in a browser (webpage_inlinebox.go), so minified nav markup does not
// read "HomeAboutContact" and a padded button is as wide as it is drawn.
func (p *webParser) pushInline(el webElement, style webStyle, attrs string, selfClose bool, name string) {
	// Inside a parent's chip the element paints with the parent's background,
	// padding and border unless it has its own.
	ownChip := webInlineOwnChip(&style)
	inChip := false
	var box webStyle
	if len(p.stack) > 0 {
		if parent := p.stack[len(p.stack)-1]; parent.kind == "inline" && parent.style.InlineChip {
			inChip, box = true, parent.style
		}
	}
	room, chip := webInlineEdge(&style, ownChip, inChip, true)
	trail, trailChip := webInlineEdge(&style, ownChip, inChip, false)
	switch {
	case ownChip:
		style.InlineChip = true
	case inChip:
		webInheritInlineChip(&style, &box)
	}
	if p.pendingBefore != "" && style.InlineChip && !webInlineOwnChip(&p.pendingBeforeStyle) {
		// ::before content is inside the box: the chip goes on under it.
		webInheritInlineChip(&p.pendingBeforeStyle, &style)
	}
	started := p.openInlineBox(room, chip, ownChip)
	flushesAt, runsAt := p.lineFlushes, 0
	if p.current != nil {
		runsAt = len(p.current.Runs)
	}
	p.push(el, style, "inline", attrs)
	f := &p.stack[len(p.stack)-1]
	f.inlineFlushes, f.inlineRuns = flushesAt, runsAt
	f.inlineTrail, f.inlineTrailChip = trail, trailChip
	f.ownChip, f.startedChip = ownChip, started
	if selfClose {
		p.endTag(name)
	}
}

// webTableRoleFor maps both real table elements and CSS table display modes
// onto one role model, so a div with display:table-cell is laid out by the same
// code as a <td>.
func webTableRoleFor(name string, display int) int {
	switch name {
	case "table":
		return webTableRoleTable
	case "tr":
		return webTableRoleRow
	case "td", "th":
		return webTableRoleCell
	case "thead":
		return webTableRoleHeaderGroup
	case "tbody":
		return webTableRoleRowGroup
	case "tfoot":
		return webTableRoleFooterGroup
	case "caption":
		return webTableRoleCaption
	case "colgroup":
		return webTableRoleColumnGroup
	case "col":
		return webTableRoleColumn
	}
	switch display {
	case webDisplayTable:
		return webTableRoleTable
	case webDisplayTableRow:
		return webTableRoleRow
	case webDisplayTableCell:
		return webTableRoleCell
	case webDisplayTableHeaderGroup:
		return webTableRoleHeaderGroup
	case webDisplayTableRowGroup:
		return webTableRoleRowGroup
	case webDisplayTableFooterGroup:
		return webTableRoleFooterGroup
	case webDisplayTableCaption:
		return webTableRoleCaption
	}
	return webTableRoleNone
}

func webTableSpanAttr(attrs, name string, def, max int) int {
	n, err := strconv.Atoi(strings.TrimSpace(htmlTagAttribute(attrs, name)))
	if err != nil {
		return def
	}
	if n < 1 {
		if name == "rowspan" && n == 0 {
			return max
		}
		return def
	}
	if n > max {
		return max
	}
	return n
}

// prepareTableStyle attaches the open table context to a real table element
// or to any element using a CSS table display role. Spans are clamped here so
// no later arithmetic can overflow or loop on a hostile attribute.
func (p *webParser) prepareTableStyle(name string, style *webStyle, attrs string) {
	role := webTableRoleFor(name, style.Display)
	if role == webTableRoleNone {
		return
	}
	switch role {
	case webTableRoleTable:
		p.tableSeq++
		ctx := webTableParse{
			id: p.tableSeq, depth: len(p.stack), layout: style.TableLayout,
			collapse: style.BorderCollapse, spacingX: style.TableSpacingX,
			spacingY: style.TableSpacingY, width: style.Width, widthPct: style.WidthPct,
		}
		ctx.cols = &webTableColumns{rows: make(map[int]webTableBorderSpec)}
		ctx.cols.table = webTableBorderSpecFromStyle(*style, 0)
		// The 2px default holds only when nothing set the spacing: an explicit
		// `border-spacing: 0` (or cellspacing="0") means no gap at all.
		if style.TableSpacingX == 0 && style.TableSpacingY == 0 && !style.TableSpacingSet {
			ctx.spacingX, ctx.spacingY = 2, 2
		}
		if ctx.collapse == webBorderCollapseCollapse {
			ctx.spacingX, ctx.spacingY = 0, 0
		}
		// A table narrower than its column is centred by auto margins, or by a
		// <center>/align="center" parent (text-align: -webkit-center).
		ctx.center = style.MarginLeftAuto && style.MarginRightAuto || p.topStyle().CenterBlocks
		style.TableID, style.TableRole = ctx.id, webTableRoleTable
		style.TableLayout, style.BorderCollapse = ctx.layout, ctx.collapse
		style.TableSpacingX, style.TableSpacingY = ctx.spacingX, ctx.spacingY
		style.TableWidthPx, style.TableWidthPct = ctx.width, ctx.widthPct
		style.TableCols = ctx.cols
		style.TableCenter = ctx.center
		p.tables = append(p.tables, ctx)
		return
	}
	if len(p.tables) == 0 {
		return
	}
	ctx := &p.tables[len(p.tables)-1]
	style.TableID, style.TableRole = ctx.id, role
	style.TableLayout, style.BorderCollapse = ctx.layout, ctx.collapse
	style.TableSpacingX, style.TableSpacingY = ctx.spacingX, ctx.spacingY
	style.TableWidthPx, style.TableWidthPct = ctx.width, ctx.widthPct
	style.TableCols = ctx.cols
	style.TableCenter = ctx.center
	switch role {
	case webTableRoleColumnGroup:
		ctx.colGroupStart = len(ctx.cols.defs)
		ctx.colGroupWidth, ctx.colGroupPct = webTableColumnWidth(*style, attrs)
		ctx.colGroupSpan = webTableSpanAttr(attrs, "span", 1, webTableMaxColumns)
	case webTableRoleColumn:
		w, pct := webTableColumnWidth(*style, attrs)
		if w <= 0 && pct <= 0 && ctx.colGroupStart >= 0 {
			w, pct = ctx.colGroupWidth, ctx.colGroupPct
		}
		ctx.cols.defs = append(ctx.cols.defs, webTableColumn{
			width: w, widthPct: pct, span: webTableSpanAttr(attrs, "span", 1, webTableMaxColumns),
		})
	case webTableRoleRow:
		style.TableRow = ctx.row
		ctx.cols.rows[style.TableRow] = webTableBorderSpecFromStyle(*style, 1)
		ctx.row++
		style.Display, style.FlexDir = webDisplayTableRow, webFlexRow
		p.flush()
		// The marker belongs where the row's cells go: in a positioned
		// subtree's layer when the table sits in one (webpage_layers.go).
		marker := webBlock{Style: *style, TableEmptyRow: true, Layer: p.openLayerIn(len(p.stack)), TreeSeq: p.blockFrameSeq()}
		marker.Float = marker.Layer != 0
		marker.FloatGroup = p.openFloatIn(len(p.stack))
		// It also belongs to the cell or flex item its table sits in, so it
		// travels with that cell's blocks to the pass that lays the table out
		// (webpage_table.go).
		p.tagRowMarker(&marker)
		p.blocks = append(p.blocks, marker)
	case webTableRoleCell:
		style.TableRow = ctx.row - 1
		if style.TableRow < 0 {
			style.TableRow = 0
		}
		style.ColSpan = webTableSpanAttr(attrs, "colspan", 1, 32)
		style.RowSpan = webTableSpanAttr(attrs, "rowspan", 1, 32)
		// A cell's vertical-align places its content in a taller row (the
		// row stretches the cell's surface, webpage_table.go). It is not an
		// inline offset: Bootstrap's `td { vertical-align: top }` used to
		// lift every cell's text half a line into the border above it.
		if style.AlignSelf < 0 {
			style.AlignSelf = webCellAlignSelf(style.CellVAlign)
		}
		style.VerticalAlign, style.VerticalAlignPx = webVerticalBaseline, 0
		style.CellOwn = true
		// The row the cell sits in: its flex group is what the cell's blocks
		// carry once the passes around the table have taken theirs.
		style.TableRowGroup = 0
		for k := len(p.stack) - 1; k > ctx.depth && k >= 0; k-- {
			if f := &p.stack[k]; f.style.TableRole == webTableRoleRow && f.style.TableID == ctx.id {
				style.TableRowGroup = f.flexGroup
				break
			}
		}
	}
}

func webTableColumnWidth(style webStyle, attrs string) (float64, float64) {
	if style.WidthPct > 0 {
		return 0, style.WidthPct
	}
	if style.Width > 0 {
		return style.Width, 0
	}
	if width := htmlTagAttributeFloat(attrs, "width"); width > 0 {
		return width, 0
	}
	return 0, 0
}

// endTableColGroup commits a span-only <colgroup> once its closing tag proves
// it had no <col> children. A group with children contributes each child's
// width instead, so it does not create a phantom column.
func (p *webParser) endTableColGroup() {
	if len(p.tables) == 0 {
		return
	}
	ctx := &p.tables[len(p.tables)-1]
	if len(ctx.cols.defs) > ctx.colGroupStart {
		ctx.colGroupStart = len(ctx.cols.defs)
		return
	}
	if ctx.colGroupWidth > 0 || ctx.colGroupPct > 0 || ctx.colGroupSpan > 1 {
		ctx.cols.defs = append(ctx.cols.defs, webTableColumn{
			width: ctx.colGroupWidth, widthPct: ctx.colGroupPct, span: ctx.colGroupSpan,
		})
	}
	ctx.colGroupStart = len(ctx.cols.defs)
}

func (p *webParser) endTag(name string) {
	if len(p.stack) > 0 && p.topStyle().Hidden {
		for i := len(p.stack) - 1; i >= 0; i-- {
			if p.stack[i].el.tag != name {
				continue
			}
			if p.stack[i].style.Hidden {
				p.resumeDetachedAbove(i - 1)
				p.stack = p.stack[:i]
				return
			}
			// A malformed hidden subtree may end at an outer visible tag.
			// Drop its inert frames, then close that visible tag normally.
			p.resumeDetachedAbove(i)
			p.stack = p.stack[:i+1]
			break
		}
		if p.topStyle().Hidden {
			return
		}
	}
	if name == "colgroup" {
		p.endTableColGroup()
	}
	if name == "picture" {
		if n := len(p.pictures); n > 0 {
			p.pictures = p.pictures[:n-1]
		}
	}
	// A section's summary ends before its body, and the body's first block must
	// start after that flush or it would join the visible marker line.
	if name == "summary" {
		if n := len(p.details); n > 0 {
			p.details[n-1].inSummary = false
			p.flush()
		}
	}
	if name == "details" {
		if n := len(p.details); n > 0 {
			p.flush()
			p.details = p.details[:n-1]
		}
	}

	// The controls whose content is collected rather than drawn are closed
	// before the stack walk: they were never pushed, since their run only
	// exists once the closing tag has arrived.
	switch name {
	case "button":
		if p.buttonCtrl != nil {
			p.endButton()
			return
		}
	case "textarea":
		if p.textarea != nil {
			p.endTextarea()
			return
		}
	case "select":
		if p.selectCtrl != nil {
			p.endSelect()
			return
		}
	case "option":
		if p.selectCtrl != nil {
			p.endOption()
			return
		}
	}
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].el.tag != name {
			continue
		}
		// An absolutely positioned inline element left open inside this one
		// closes with it, and the line it set aside goes on.
		p.resumeDetachedAbove(i)
		// ::after resolves at the closing tag, after descendants have run
		// their counter increments, which is when a browser computes it.
		afterSet := false
		clearAfter := 0
		if p.pseudoMask&(1<<webPseudoElemAfter) != 0 {
			frame := &p.stack[i]
			if generated := p.pseudoContent(frame.el, frame.attrs, frame.style, webPseudoElemAfter); generated.Matched && generated.HasContent {
				afterSet = true
				clearAfter = webClearfixAfter(&generated.Style)
				if generated.Text != "" {
					st := generated.Style
					if frame.kind == "inline" && frame.style.InlineChip && !webInlineOwnChip(&st) {
						// ::after content is inside the box too (pushInline).
						webInheritInlineChip(&st, &frame.style)
					}
					p.addTextStyled(generated.Text, st)
				}
			}
		}
		if name == "q" && !afterSet {
			p.addTextStyled("”", p.stack[i].style)
		}
		// A table cell with nothing to draw still takes its column; a cell
		// left open closes with its row or table (keepEmptyCell). A spacer
		// paragraph keeps its height (keepSpacer).
		for k := len(p.stack) - 1; k >= i; k-- {
			switch f := &p.stack[k]; {
			case f.style.CellOwn && f.kind == "block":
				p.keepEmptyCell(f)
			case f.blankLine && f.style.TableRole == webTableRoleNone && (f.kind == "block" || f.kind == "list"):
				p.keepSpacer(f)
			}
		}
		if p.stack[i].kind == "block" || p.stack[i].kind == "list" || p.stack[i].kind == "contents" {
			p.flush()
		}
		if p.stack[i].kind == "block" || p.stack[i].kind == "list" {
			// A positioned container whose children all became layers keeps a
			// line of its own (webpage_layers.go).
			p.layerSpacer(i)
		}
		if p.stack[i].kind == "inline" {
			// The element's right padding, border and margin are room after its
			// last word (webpage_inlinebox.go).
			p.closeInlineBox(&p.stack[i])
		}
		if name == "head" {
			p.inHead = false
		}
		if name == "ul" || name == "ol" {
			if depth := len(p.lists); depth > 0 {
				p.lists = p.lists[:depth-1]
			}
		}
		if name == "li" {
			p.pendingB, p.pendingBStyle, p.hasPendingBStyle = "", webStyle{}, false
			p.pendingHang = false
		}
		if name == "form" {
			p.form = webForm{}
		}
		// Closing an element also closes every flex container it opened, even
		// when its own end tag was missing (real pages are full of those).
		for len(p.flexStack) > 0 && p.flexStack[len(p.flexStack)-1].depth >= i {
			p.flexStack = p.flexStack[:len(p.flexStack)-1]
		}
		for len(p.tables) > 0 && p.tables[len(p.tables)-1].depth >= i {
			p.tables = p.tables[:len(p.tables)-1]
		}
		// A clearfix's ::after moves what follows below the floats, and an
		// element that holds its floats ends below those opened inside it
		// (webpage_floats.go).
		if p.stack[i].kind == "block" || p.stack[i].kind == "list" {
			p.endFloatScope(i, clearAfter)
		}
		// A sticky or relatively positioned element's subtree owns every box
		// opened inside it (webpage_layers.go); unclosed children close with
		// it.
		for k := len(p.stack) - 1; k >= i; k-- {
			if s := p.stack[k].sticky; s > 0 && s <= len(p.doc.Stickies) {
				p.doc.Stickies[s-1].lastBox = p.boxSeq
			}
			if s := p.stack[k].positioned; s > 0 && s <= len(p.doc.Positioned) {
				p.doc.Positioned[s-1].lastBox = p.boxSeq
			}
		}
		p.popCounterResets(p.stack[i].counterResets)
		if s := p.stack[i].suspended; s != nil {
			// A detached layer is finished (flushed above): the line it
			// interrupted goes on.
			p.resumeLine(s)
		}
		p.stack = p.stack[:i]
		if p.firstLetter != nil && p.firstLetter.depth >= i {
			// The element ended with no letter in it.
			p.firstLetter = nil
		}
		return
	}
}

func (p *webParser) push(el webElement, style webStyle, kind string, attrs string) {
	depth := len(p.stack)
	p.treeSeq++
	seq := p.treeSeq
	// A block starts a new baseline: only inline boxes carry a sub/super or
	// relative raise down to their text (webApplyBaselineShift). A block's own
	// relative offset moves the whole block in layout instead.
	if kind == "block" || kind == "list" {
		style.BaselineShift = 0
	}
	containerID := 0
	if style.ContainerType != 0 {
		p.containerSeq++
		containerID = p.containerSeq
		parent := 0
		for i := len(p.stack) - 1; i >= 0; i-- {
			if p.stack[i].containerID != 0 {
				parent = p.stack[i].containerID
				break
			}
		}
		info := webContainerInfo{
			parent: parent, width: style.Width, widthPct: style.WidthPct,
			height: style.Height,
		}
		for _, frame := range p.stack {
			if frame.style.WidthPct > 0 || frame.style.Width > 0 {
				info.ancestors = append(info.ancestors, webContainerAncestor{
					width: frame.style.Width, widthPct: frame.style.WidthPct,
				})
			}
		}
		if len(p.flexStack) > 0 {
			top := p.flexStack[len(p.flexStack)-1]
			info.flexGroup, info.flexItem = top.group, top.item
		}
		if p.doc.Containers == nil {
			p.doc.Containers = make(map[int]webContainerInfo)
		}
		p.doc.Containers[containerID] = info
	}
	// A visual box belongs to the element, not to whichever child happens to
	// hold text. Record it now so descendant blocks can carry its ID even when
	// the element itself never produces a block (the card/hero/sidebar case).
	// A positioned element always gets one too: it is the containing block of
	// the absolutely positioned boxes inside it, so layout must be able to
	// find its rectangle (webpage_layers.go). So does a floated one: a pinned
	// sticky bar carries the floats whose box is inside it (webpage_floats.go).
	boxID := 0
	if (kind == "block" || kind == "list") &&
		(webStyleHasBox(style) || webStyleHasLayoutBox(style) || webStyleIsAnchor(&style) || style.FloatSide != 0) {
		p.boxSeq++
		boxID = p.boxSeq
		if p.doc.Boxes == nil {
			p.doc.Boxes = make(map[int]webBoxInfo)
		}
		p.doc.Boxes[boxID] = webBoxInfo{
			style: style, visual: webStyleHasBox(style),
			anchor: !webStyleHasBox(style) && !webStyleHasLayoutBox(style),
		}
	}
	// An absolutely positioned block opens a layer for its whole subtree,
	// unless it sits inside one already (webpage_layers.go).
	layer := 0
	if (kind == "block" || kind == "list") && len(p.doc.Layers) < webMaxLayers &&
		p.wantsLayer(&style) && p.layerDepthIn(len(p.stack)) < webMaxLayerDepth {
		layer = p.newLayer(style, boxID, seq)
	}
	// A block element pushed directly under an open flex container is one of
	// its items; nested elements belong to the same item. Text-only children
	// are tagged by ensureBlock instead, which is why the counter starts at
	// zero rather than one.
	flexItem := false
	if kind != "skip" && kind != "contents" && len(p.flexStack) > 0 {
		// The container was pushed at its own depth, so its direct children
		// are pushed one level deeper. Comparing equal depths never matched,
		// which left every item in one column.
		if top := &p.flexStack[len(p.flexStack)-1]; p.flexDirectChild(top, depth) {
			p.flush()
			top.item++
			top.itemStyle = style
			flexItem = true
			if webFlexDebug && top.group == 32 {
				println("  ITEM", top.group, top.item, el.tag, strings.Join(el.classes, "."))
			}
			if boxID != 0 {
				if p.doc.FlexItemBoxDepth[top.group] == nil {
					p.doc.FlexItemBoxDepth[top.group] = make(map[int]int)
				}
				p.doc.FlexItemBoxDepth[top.group][top.item] = len(p.boxPath()) + 1
			}
		}
	}
	// The layer leaves a marker where it would have started in the flow (its
	// static position), and a sticky element is recorded so its whole subtree
	// can be pinned (webpage_layers.go). Both see only the ancestors: the
	// frame is not pushed yet.
	if layer != 0 && !flexItem {
		p.layerMarker(layer, seq)
	}
	// A floated block takes its whole subtree with it — a figure's picture and
	// caption, an infobox table, a sidebar column (webpage_floats.go). A flex
	// or grid item does not float, nor does a positioned layer.
	floatRoot := 0
	if (kind == "block" || kind == "list") && layer == 0 && !flexItem && p.floatRootWanted(&style) {
		floatRoot = p.newFloatRoot(style, boxID, seq)
	}
	// `clear` on a block of the flow moves the whole element below the floats
	// before it, children included, not only its own text — and an empty
	// `<div style="clear: both">` moves what follows it. A float's own clear
	// only moves the float.
	if (kind == "block" || kind == "list") && layer == 0 && !flexItem && style.Clear != 0 &&
		style.FloatSide == 0 && p.floatsSeen > 0 {
		p.clearMarker(style.Clear, 0, len(p.stack))
	}
	sticky := 0
	if (kind == "block" || kind == "list") && style.Sticky && boxID != 0 {
		sticky = p.newStickyRoot(style, boxID, seq)
	}
	// A relatively positioned element of the flow paints in the positioned
	// pass, with everything in it: layout lifts it over the layers it comes
	// after (webLiftPositioned). A sticky one has its pinned copy instead.
	positioned := 0
	if (kind == "block" || kind == "list") && boxID != 0 && layer == 0 && len(p.doc.Positioned) < webMaxPositioned &&
		style.Position == webPositionRelative && !style.Sticky && p.openLayerIn(len(p.stack)) == 0 {
		p.doc.Positioned = append(p.doc.Positioned, webPositionedInfo{rootBox: boxID, key: p.stackKey(style, seq)})
		positioned = len(p.doc.Positioned)
	}
	group := 0
	if (kind == "block" || kind == "list") && (style.Display == webDisplayFlex || style.Display == webDisplayGrid || style.Display == webDisplayTableRow) {
		p.flexSeq++
		group = p.flexSeq
		if p.doc.FlexBoxDepth == nil {
			p.doc.FlexBoxDepth = make(map[int]int)
		}
		boxDepth := len(p.boxPath())
		if boxID != 0 {
			boxDepth++
		}
		p.doc.FlexBoxDepth[group] = boxDepth
		p.doc.FlexScrollX[group] = style.ScrollX
		if layer != 0 && layer <= len(p.doc.Layers) {
			p.doc.Layers[layer-1].flexGroup = group
		}
		if webFlexDebug {
			println("FLEXGROUP", group, el.tag, strings.Join(el.classes, "."), "display", style.Display, "cols", style.GridCols, "gapX", int(style.GapX), "depth", depth, "stack", len(p.stack))
		}
		p.flexStack = append(p.flexStack, webFlexFrame{
			group:             group,
			depth:             depth,
			dir:               style.FlexDir,
			justify:           style.Justify,
			align:             style.AlignItems,
			alignContent:      style.AlignContent,
			justifyItems:      style.JustifyItems,
			gapX:              style.GapX,
			gapY:              style.GapY,
			height:            webFlexContainerHeight(style),
			wrap:              style.FlexWrap,
			reverse:           style.FlexReverse,
			gridCols:          style.GridCols,
			gridMin:           style.GridMin,
			gridWeights:       style.GridWeights,
			gridBases:         style.GridBases,
			gridRows:          style.GridRows,
			gridRowWeights:    style.GridRowWeights,
			gridRowBases:      style.GridRowBases,
			gridTrackSpecs:    style.GridTrackSpecs,
			gridRowTrackSpecs: style.GridRowTrackSpecs,
			gridAutoRepeat:    style.GridAutoRepeat,
			gridAutoTrack:     style.GridAutoTrack,
			gridRowAutoRepeat: style.GridRowAutoRepeat,
			gridRowAutoTrack:  style.GridRowAutoTrack,
			gridAutoColSpecs:  style.GridAutoColSpecs,
			gridAutoColCount:  style.GridAutoColCount,
			gridAutoRowSpecs:  style.GridAutoRowSpecs,
			gridAutoRowCount:  style.GridAutoRowCount,
			gridAutoFlow:      style.GridAutoFlow,
			gridDense:         style.GridDense,
			gridAreas:         style.GridAreas,
		})
	}
	frame := webFrame{
		el: el, style: style, kind: kind, flexGroup: group, containerID: containerID, boxID: boxID,
		attrs: attrs, counterResets: p.pendingCounterResets,
		layer: layer, seq: seq, blockStart: len(p.blocks), sticky: sticky,
		positioned: positioned, flexItem: flexItem,
		floatRoot: floatRoot, floatsAt: p.floatsSeen,
	}
	p.pendingCounterResets = nil
	p.stack = append(p.stack, frame)
	// The frame is open before ::before is added, so the generated text lands
	// inside the element rather than before it.
	before, beforeStyle := p.pendingBefore, p.pendingBeforeStyle
	p.pendingBefore, p.pendingBeforeStyle = "", webStyle{}
	if before != "" {
		p.addTextStyled(before, beforeStyle)
	}
}

// beginFlexLeaf assigns a direct replaced/void child its own flex item.
// Unlike a div, img, hr and input never go through push, so without this two
// adjacent images or buttons were stacked in one cell.
func (p *webParser) beginFlexLeaf(style webStyle) {
	if len(p.flexStack) == 0 {
		return
	}
	top := &p.flexStack[len(p.flexStack)-1]
	if !p.flexDirectChild(top, len(p.stack)) {
		return
	}
	p.flush()
	top.item++
	top.itemStyle = style
}

// flexDirectChild treats display:contents wrappers as transparent to the
// parent's item counter. The wrapper itself has no flex item box; each child
// becomes one item just as if the wrapper were absent.
func (p *webParser) flexDirectChild(top *webFlexFrame, depth int) bool {
	if top == nil || depth <= top.depth {
		return false
	}
	for i := top.depth + 1; i < depth; i++ {
		if i >= len(p.stack) || !p.stack[i].style.DisplayContents {
			return false
		}
	}
	return true
}

// noteAdCut records an ad cut out of the page where the walk stands
// (webAdCutMarker). When it was an item of the flex or grid container open
// here, the grid closes the column tracks nothing is left in (layoutGridItems):
// an article beside a cut ad rail takes the rail's width.
func (p *webParser) noteAdCut() {
	n := len(p.flexStack)
	if n == 0 {
		return
	}
	top := &p.flexStack[n-1]
	if !p.flexDirectChild(top, len(p.stack)) {
		return
	}
	if p.doc.FlexAdCut == nil {
		p.doc.FlexAdCut = make(map[int]bool)
	}
	p.doc.FlexAdCut[top.group] = true
}

func (p *webParser) topStyle() webStyle {
	if len(p.stack) == 0 {
		return webBaseStyle()
	}
	return p.stack[len(p.stack)-1].style
}

// A percentage block size needs a definite containing block. An auto-height
// parent does not make its own parent's height available to descendants.
func (p *webParser) webContainingBlockHeight(el webElement) float64 {
	for i := len(p.stack) - 1; i >= 0; i-- {
		frame := p.stack[i]
		if frame.kind == "block" || frame.kind == "list" {
			return webDefiniteContentHeight(frame.style)
		}
	}
	if el.tag == "html" {
		_, height := webMediaViewport()
		return height
	}
	return 0
}

func webDefiniteContentHeight(st webStyle) float64 {
	if st.Height <= 0 {
		return 0
	}
	height := st.Height
	if st.MaxHeight > 0 && height > st.MaxHeight {
		height = st.MaxHeight
	}
	if height < st.MinHeight {
		height = st.MinHeight
	}
	return webHeightContentTarget(st, height)
}

// Height constraints target the content rows. For border-box sizing, padding
// is inside the authored height rather than extra space around those rows.
func webHeightContentTarget(st webStyle, height float64) float64 {
	if st.BoxSizing == 1 {
		height -= st.PadTop + st.PadBot
	}
	return max(0, height)
}

// nearestBlockStyle is the style of the block element the text belongs to.
func (p *webParser) nearestBlockStyle() webStyle {
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].kind == "block" {
			return p.stack[i].style
		}
	}
	return p.topStyle()
}

// webResetElementStyle clears the properties that do not inherit. It is used
// both for an element and for a generated box, whose inherited font, colour,
// alignment and line height come from the originating element.
func webResetElementStyle(st *webStyle) {
	st.HasBg, st.Background = false, sdl.Color{}
	st.BackgroundImage, st.BackgroundSize, st.BackgroundPosition = "", "", ""
	st.BackgroundRepeat = ""
	st.BackgroundLayers = [webMaxBackgroundLayers]webBackgroundLayerStyle{}
	st.BackgroundLayerCount = 0
	st.HasGradient, st.Gradient = false, nil
	st.Indent, st.TextIndent, st.MarginTop, st.MarginBot = 0, 0, 0, 0
	st.MarginLeftAuto, st.MarginRightAuto = false, false
	st.MarginTopAuto, st.MarginBotAuto = false, false
	st.MarginLeftAuto, st.MarginRightAuto = false, false
	st.BorderLeft, st.BorderRight, st.ListNone = false, false, false
	st.ListItem = false
	st.BorderWidth = 0
	st.BorderSideWidth = [4]float64{}
	st.BorderSideStyle = [4]int{}
	st.BorderSideStyleSet = [4]bool{}
	st.BorderSideColor = [4]sdl.Color{}
	st.BorderSideHasColor = [4]bool{}
	// A radius and a right gutter belong to the element that declared them;
	// line-height, the list marker and the opacity group are inherited above.
	st.Radius, st.IndentRight = 0, 0
	st.PadTop, st.PadBot, st.PadLeft, st.PadRight = 0, 0, 0, 0
	st.BoxBorder, st.BorderTop, st.BorderBottom = false, false, false
	st.BorderColor, st.HasBorderColor = sdl.Color{}, false
	st.BorderStyle, st.Outline, st.OutlineOffset = webBorderSolid, false, 0
	// Layout properties are per element too: a child of a flex container is
	// not itself a container, and an item's grow factor belongs to it alone.
	st.Display, st.DisplayBlock, st.DisplayContents, st.DisplayInline, st.FlexDir = webDisplayBlock, false, false, false, webFlexRow
	st.DisplayInlineBlock, st.InlineChip = false, false
	st.FlexReverse = false
	st.Justify, st.AlignItems, st.AlignSelf = webJustifyStart, webItemStretch, -1
	st.AlignContent = webContentStretch
	st.JustifyItems, st.JustifySelf = webItemStretch, -1
	st.GapX, st.GapY, st.FlexWrap, st.GridCols, st.GridMin = 0, 0, false, 0, 0
	st.GridAutoColSpecs, st.GridAutoColCount = [6]webGridTrackSpec{}, 0
	st.GridAutoRowSpecs, st.GridAutoRowCount = [6]webGridTrackSpec{}, 0
	st.GridWeights, st.GridBases = [6]float64{}, [6]float64{}
	// Placement is not inherited either. A grid container that is itself an
	// item (CNN's card list carries `grid-area: 1/1/auto/-1` for the wrapper's
	// grid) used to hand its own cell down to every descendant, so each card's
	// link, headline and summary block asked for the same cell and their text
	// drew on top of each other.
	st.GridRowStart, st.GridRowEnd, st.GridRowSpan = 0, 0, 0
	st.GridColumnStart, st.GridColumnEnd, st.GridColumnSpan = 0, 0, 0
	st.GridArea = ""
	st.FlexGrow, st.FlexShrink, st.FlexBasis, st.BasisPct = 0, 1, -1, 0
	st.Order = 0
	st.Width, st.WidthPct, st.WidthFit, st.Height = 0, 0, 0, 0
	st.MinWidth, st.MaxWidth, st.MinWidthPct, st.MaxWidthPct = 0, 0, 0, 0
	st.MinWidthAuto = true
	st.MinHeight, st.MaxHeight = 0, 0
	st.BoxSizing, st.FloatSide, st.Clear = 0, 0, 0
	st.AspectRatio = 0
	st.ObjectFit = 0
	st.ObjectPosition = ""
	st.ContainerType, st.ContainerName = 0, ""
	st.CQ = nil
	st.ZIndex, st.Position, st.Sticky = 0, webPositionStatic, false
	st.ZSet = false
	st.Top, st.Right, st.Bottom, st.Left = 0, 0, 0, 0
	st.HasTop, st.HasRight, st.HasBottom, st.HasLeft = false, false, false, false
	st.Content = ""
	st.CounterReset, st.CounterIncrement, st.CounterSet = "", "", ""
	// text-shadow inherits, as in CSS: a hero's shadowed heading keeps the
	// shadow on the <span> and <a> inside it.
	st.DecorationThickness = 0
	st.DecorationStyle = 0
	st.DecorationOffset = 0
	st.VerticalAlign, st.VerticalAlignPx = 0, 0
	st.OverflowX, st.OverflowY, st.ScrollX = 0, 0, false
	st.OverflowBFC, st.FlowRoot = false, false
	st.TextOverflow = 0
	st.HasBoxShadow, st.BoxShadowX, st.BoxShadowY = false, 0, 0
	st.BoxShadowColor, st.HasBoxShadowColor = sdl.Color{}, false
	st.BoxShadowBlur, st.BoxShadowSpread = 0, 0
	st.HasTranslate, st.Isolate = false, false
	st.TranslateX, st.TranslateY, st.TranslatePctX, st.TranslatePctY = 0, 0, 0, 0
	st.InsetPct = [4]float64{}
	st.LineClamp = 0
}

// Keep the last width declaration this renderer can actually measure. A
// browser discards an unsupported CSS value before it participates in the
// cascade; otherwise a newer round()/unresolved calc() can erase an earlier
// usable card width and collapse a no-shrink carousel into tiny columns.
func webSupportedWidthValue(name, value string, em float64) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "0" || webSupportsZeroLength(v) {
		return true
	}
	if v == "auto" || name == "max-width" && v == "none" {
		return true
	}
	if name == "width" && webWidthKeyword(v) >= 0 {
		return true
	}
	if pct, ok := webPercentValue(v); ok {
		return pct >= 0
	}
	// The length parser has no containing block at this stage, so a
	// percentage inside a function cannot be resolved as a width.
	if strings.Contains(v, "%") {
		return false
	}
	px := webLength(v, em)
	return px > 0 && !math.IsNaN(px) && !math.IsInf(px, 0)
}

// webWidthKeyword reads a keyword width: 1 for fit-content and max-content
// (the reader's lines never run past their column, so the two agree), 2 for
// min-content, 0 for stretch (a block's auto width), -1 for anything else.
// Each comes with the vendor spellings sheets still ship as fallbacks.
func webWidthKeyword(v string) int {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "fit-content", "-moz-fit-content", "-webkit-fit-content", "max-content", "-moz-max-content",
		"-webkit-max-content", "intrinsic":
		return 1
	case "min-content", "-moz-min-content", "-webkit-min-content", "min-intrinsic":
		return 2
	case "stretch", "-webkit-fill-available", "-moz-available", "fill-available":
		return 0
	}
	return -1
}

// webSortCascade puts matched rules in cascade order for their normal
// declarations: cascade layer first (webRule.layer), then specificity, then
// source order — the last one wins.
func webSortCascade(matched []*webRule) {
	slices.SortStableFunc(matched, func(a, b *webRule) int {
		switch {
		case a.layer != b.layer:
			return a.layer - b.layer
		case a.specificity != b.specificity:
			return a.specificity - b.specificity
		case a.order < b.order:
			return -1
		case a.order > b.order:
			return 1
		}
		return 0
	})
}

// webImportantCascade is the order matched rules' !important declarations
// apply in. Layers count backwards there: an earlier layer's important
// declaration beats a later layer's, and any layer's beats an unlayered one;
// specificity and source order then decide as for normal declarations.
// Without layered important rules it is matched itself.
func webImportantCascade(matched []*webRule) []*webRule {
	layered := false
	for _, r := range matched {
		if r.layer != webUnlayered && len(r.important) > 0 {
			layered = true
			break
		}
	}
	if !layered {
		return matched
	}
	out := slices.Clone(matched)
	slices.SortStableFunc(out, func(a, b *webRule) int {
		switch {
		case a.layer != b.layer:
			return b.layer - a.layer
		case a.specificity != b.specificity:
			return a.specificity - b.specificity
		case a.order < b.order:
			return -1
		case a.order > b.order:
			return 1
		}
		return 0
	})
	return out
}

// webBoxSideProp is one property that sets sides of a box (margin or padding):
// its name after the box's, its kind — 0 the shorthand, 1 a block/inline
// pair, 2 one side — and, for a pair, 0 block (top, bottom) or 1 inline
// (left, right), or for one side, the side (top, right, bottom, left). The
// kind is also its precedence inside one declaration block: a side's longhand
// is taken to come after the pair that covers it, and the pair after the
// shorthand, as sheets write them.
type webBoxSideProp struct {
	suffix string
	kind   int
	side   int
}

// webBoxSideProps are the side-setting properties of margin and padding, the
// shorthand first.
var webBoxSideProps = [...]webBoxSideProp{
	{"", 0, 0},
	{"-block", 1, 0}, {"-inline", 1, 1},
	{"-top", 2, 0}, {"-block-start", 2, 0},
	{"-right", 2, 1}, {"-inline-end", 2, 1},
	{"-bottom", 2, 2}, {"-block-end", 2, 2},
	{"-left", 2, 3}, {"-inline-start", 2, 3},
}

// webResolveBoxSides settles margin and padding the way the cascade does: for
// each side, the declaration that comes last wins, whether it is the shorthand
// (padding), a pair (padding-block, padding-inline) or the side itself
// (padding-top, padding-block-start). The style map holds one value per
// property name, and applyWebDecls used to add a shorthand's side to the
// longhand's, so Tailwind's `p-4 pt-2` padded the top by 24px instead of 8px
// and `padding: 2rem; padding-bottom: 0` kept its bottom padding. rank is each
// property's cascade position; the four physical longhands replace them all.
func webResolveBoxSides(merged map[string]string, rank map[string]int) {
	for _, box := range [...]string{"margin", "padding"} {
		var side [4]string
		at := [4]int{-1, -1, -1, -1}
		present := false
		for _, prop := range webBoxSideProps {
			name := box + prop.suffix
			value, ok := merged[name]
			if !ok {
				continue
			}
			present = true
			delete(merged, name)
			key := rank[name]*4 + prop.kind
			set := func(s int, v string) {
				if key >= at[s] {
					side[s], at[s] = v, key
				}
			}
			tokens := webCSSSpaceFields(strings.TrimSpace(value))
			switch prop.kind {
			case 0:
				var t, r, b, l string
				switch len(tokens) {
				case 1:
					t, r, b, l = tokens[0], tokens[0], tokens[0], tokens[0]
				case 2:
					t, r, b, l = tokens[0], tokens[1], tokens[0], tokens[1]
				case 3:
					t, r, b, l = tokens[0], tokens[1], tokens[2], tokens[1]
				case 4:
					t, r, b, l = tokens[0], tokens[1], tokens[2], tokens[3]
				default:
					continue
				}
				set(0, t)
				set(1, r)
				set(2, b)
				set(3, l)
			case 1:
				var first, second string
				switch len(tokens) {
				case 1:
					first, second = tokens[0], tokens[0]
				case 2:
					first, second = tokens[0], tokens[1]
				default:
					continue
				}
				if prop.side == 0 {
					set(0, first)
					set(2, second)
				} else {
					set(3, first)
					set(1, second)
				}
			default:
				if len(tokens) == 1 {
					set(prop.side, tokens[0])
				}
			}
		}
		if !present {
			continue
		}
		for s, suffix := range [...]string{"-top", "-right", "-bottom", "-left"} {
			if at[s] >= 0 {
				merged[box+suffix] = side[s]
			}
		}
	}
}

// webDropUAInlineMargins takes back the UA sheet's side margin of a quote, a
// figure or a definition (40px, applyWebTagDefaults) on a side the page sets
// itself. The reader adds margins and padding into one inset, so the page's
// value was added to the UA's: a reset's `blockquote, figure, dd { margin: 0 }`
// (normalize, Tailwind's preflight, GitHub's markdown) left them 40px in.
func webDropUAInlineMargins(st *webStyle, tag string, merged map[string]string) {
	left, right := 0.0, 0.0
	switch tag {
	case "blockquote", "figure":
		left, right = 40, 40
	case "dd":
		left = 40
	default:
		return
	}
	if _, ok := merged["margin-left"]; ok {
		st.Indent -= left
	}
	if _, ok := merged["margin-right"]; ok {
		st.IndentRight -= right
	}
}

func webCascadedSupportedWidth(name string, matched []*webRule, inlineDecls, inlineImportant map[string]string, supported func(string) bool) (string, bool) {
	if value, ok := inlineImportant[name]; ok && supported(value) {
		return value, true
	}
	for i := len(matched) - 1; i >= 0; i-- {
		if value, ok := matched[i].important[name]; ok && supported(value) {
			return value, true
		}
	}
	if value, ok := inlineDecls[name]; ok && supported(value) {
		return value, true
	}
	for i := len(matched) - 1; i >= 0; i-- {
		if value, ok := matched[i].decls[name]; ok && supported(value) {
			return value, true
		}
	}
	return "", false
}

func (p *webParser) computeStyle(el webElement, attrs string) webStyle {
	st := p.topStyle()
	// Non-inherited properties start clean for every element.
	webResetElementStyle(&st)
	// Link colour, underline and strikethrough are not reset: a decoration
	// on <a> or <s> has to keep showing on the <strong>/<span> inside it,
	// which is how every browser renders `<a><b>text</b></a>`.

	// A font-size in em or % is relative to the parent's size, never to the
	// tag's own default: `h2 { font-size: 1.5em }` is 24px under 16px text,
	// not 1.5 × the reader's heading size. The parent's weight, slant and
	// alignment are kept for `inherit` too.
	parentFontPx := st.FontPx
	parentBold, parentItalic := st.Bold, st.Italic
	parentAlign, parentCenter := st.Align, st.CenterBlocks
	parentColor, parentHasColor := st.Color, st.HasColor
	parentLink, parentUnderline := st.Link, st.Underline
	applyWebTagDefaults(&st, el.tag)
	webApplyIconFontClass(&st, el.classes)
	// Only an <a> with an href is a link (a:any-link). One without (<a name>,
	// <a id>) is an anchor target, drawn like the text around it. A link is
	// coloured by the UA sheet itself, so the colour of the element around it
	// does not reach it: it is link blue unless the page colours the link
	// (`color: inherit` included, restored below).
	isLink := el.tag == "a" && el.hasAttr("href")
	if el.tag == "a" && !isLink {
		st.Link, st.Underline = parentLink, parentUnderline
	}
	if isLink {
		st.HasColor = false
	}

	chain := make([]webElement, 0, len(p.stack)+1)
	for _, frame := range p.stack {
		chain = append(chain, frame.el)
	}
	chain = append(chain, el)

	// A rule that would hide the element is matched strictly, so a selector
	// the reader cannot fully decide never blanks the page, and one that would
	// reveal it also reaches what a page opens on :hover (webSelMode). The
	// matches are kept as pointers into the sheet — copying each rule for every
	// element styled was most of what this loop allocated — and sorted without
	// reflection.
	var matched []*webRule
	for _, i := range p.ruleIx.candidates(&el) {
		rule := &p.rules[i]
		if rule.sel.pseudo != webPseudoElemNone {
			continue
		}
		if !rule.sel.matchesIn(chain, rule.mode()) {
			continue
		}
		if len(rule.containers) > 0 && !p.webContainerRulesApply(rule.containers) {
			continue
		}
		matched = append(matched, rule)
	}
	webSortCascade(matched)
	// Cascade: stylesheet normals, inline normals, stylesheet important, then
	// inline important. A normal inline style must not override an important
	// stylesheet rule (including a rule that hides an element).
	// Collapsing the winners into one map also gives var()
	// and calc() the final value to resolve against, so the property parsers
	// below only ever see plain lengths and colours. The element's
	// presentation attributes (bgcolor, align, width, <font color>, …) come
	// first, below every style sheet (webpage_hints.go).
	hints := p.webPresentationalHints(&el)
	merged := make(map[string]string, 16+len(hints))
	// rank is the cascade position of the block each winning declaration came
	// from, so a shorthand and a longhand for the same side resolve by which
	// comes later (webResolveBoxSides).
	rank := make(map[string]int, 16+len(hints))
	seq := 0
	cascade := func(decls map[string]string) {
		seq++
		for name, value := range decls {
			merged[name] = value
			rank[name] = seq
		}
	}
	cascade(hints)
	for _, rule := range matched {
		cascade(rule.decls)
	}
	inlineDecls, inlineImportant := parseWebDeclsSplit(htmlTagAttribute(attrs, "style"))
	cascade(inlineDecls)
	for _, rule := range webImportantCascade(matched) {
		cascade(rule.important)
	}
	cascade(inlineImportant)
	// An animation that holds its end state (fill-mode forwards or both) shows
	// that state once it has run: a fade-in's opacity 1, a preloader's 0.
	p.applyAnimationEnd(merged)
	// Custom properties are inherited, so this element's own definitions are
	// resolved once here and carried down by the frames below it.
	st.Vars = webVarScopeFor(p.topStyle().Vars, merged)
	// Keep the cascade before cq* substitution. A declaration can hide a cq
	// unit behind var(), so the custom properties are inspected too even though
	// applyWebDecls will ignore them.
	rawDecls := make(map[string]string, len(merged))
	needsCQ := false
	for name, value := range merged {
		rawDecls[name] = value
		if strings.Contains(strings.ToLower(value), "cq") {
			needsCQ = true
		}
	}
	if !needsCQ && st.Vars != nil {
		for _, value := range st.Vars.props {
			if strings.Contains(strings.ToLower(value), "cq") {
				needsCQ = true
				break
			}
		}
	}
	for name := range merged {
		if strings.HasPrefix(name, "--") {
			delete(merged, name)
		}
	}
	cw, ch := p.webOrdinaryContainerSize()
	// A valid font-size (or font shorthand) replaces the tag's default size and
	// resolves against the parent's: the element's style starts from that size
	// again, and the declaration becomes the px it resolved to. `inherit` is
	// the parent's size, which is how Tailwind's reset sets every heading.
	if raw, ok := merged["font-size"]; ok {
		resolved := p.webResolveStyleValueIn(raw, st.Vars, parentFontPx, cw, ch)
		px, valid := webFontSizePx(resolved, parentFontPx)
		if webInheritKeyword(resolved) {
			px, valid = parentFontPx, true
		}
		if valid {
			st.FontPx = parentFontPx
			merged["font-size"] = strconv.FormatFloat(px, 'f', -1, 64) + "px"
		}
	} else if _, ok := merged["font"]; ok {
		st.FontPx = parentFontPx
	}
	base := st
	base.CQ = nil
	// `em` inside calc() is this element's font size, which font-size itself
	// has to settle first.
	em := st.FontPx
	if raw, ok := merged["font-size"]; ok {
		if px, valid := webFontSizePx(p.webResolveStyleValueIn(raw, st.Vars, em, cw, ch), em); valid {
			em = px
		}
	}
	// A width written as calc() of a percentage and lengths (`calc(100% -
	// 300px)`, `min(100%, 40rem)`) is the percentage it comes to in the
	// parent's modelled width (webCalcPercentsIn).
	calcBase := 0.0
	percentsIn := func(value string) string {
		if !strings.Contains(value, "%") || strings.IndexByte(value, '(') < 0 {
			return value
		}
		if calcBase == 0 {
			calcBase, _ = p.webContainerDimensions(len(p.stack) - 1)
		}
		return webCalcPercentsIn(value, em, calcBase)
	}
	for _, name := range [...]string{"width", "min-width", "max-width"} {
		if _, present := merged[name]; !present {
			continue
		}
		supported := func(value string) bool {
			resolved := percentsIn(p.webResolveStyleValueIn(value, st.Vars, em, cw, ch))
			return webSupportedWidthValue(name, resolved, em)
		}
		value, ok := webCascadedSupportedWidth(name, matched, inlineDecls, inlineImportant, supported)
		if !ok {
			// A presentational width (table/td width="85%") is the bottom of
			// the author cascade: it holds when no sheet gave a usable one.
			if hint, has := hints[name]; has && supported(hint) {
				value, ok = hint, true
			}
		}
		if ok {
			merged[name], rawDecls[name] = value, value
		} else {
			delete(merged, name)
			delete(rawDecls, name)
		}
	}
	for name, value := range merged {
		merged[name] = p.webResolveStyleValueIn(value, st.Vars, em, cw, ch)
		switch name {
		case "width", "min-width", "max-width", "flex-basis", "flex", "left", "right":
			merged[name] = percentsIn(merged[name])
		}
	}
	webResolveBoxSides(merged, rank)
	webResolveBorderColor(merged, rank)
	webDropUAInlineMargins(&st, el.tag, merged)
	st.ListPadSet = false
	switch el.tag {
	case "ul", "ol", "menu":
		_, st.ListPadSet = merged["padding-left"]
	}
	parentHeight := p.webContainingBlockHeight(el)
	applyWebDecls(&st, merged)
	p.applyWebFontFamily(&st, merged)
	webResolvePercentageHeights(&st, merged, parentHeight)
	// `inherit` on an inherited property is the parent's value even where the
	// tag has a default of its own (a reset's `h1 { font-weight: inherit }`).
	if v, ok := merged["font-weight"]; ok && webInheritKeyword(v) {
		st.Bold = parentBold
	}
	if v, ok := merged["font-style"]; ok && webInheritKeyword(v) {
		st.Italic = parentItalic
	}
	if v, ok := merged["text-align"]; ok && webInheritKeyword(v) {
		st.Align, st.CenterBlocks = parentAlign, parentCenter
	}
	if v, ok := merged["color"]; ok && (webInheritKeyword(v) || strings.EqualFold(strings.TrimSpace(v), "currentcolor")) {
		st.Color, st.HasColor = parentColor, parentHasColor
	}
	webApplyBaselineShift(&st, parentFontPx)
	// Where a table part puts its cells' content in a taller row, by the
	// browser's cascade for vertical-align (webpage_table.go).
	if role := webTableRoleFor(el.tag, st.Display); role != webTableRoleNone {
		parent := p.topStyle()
		valign, declared := merged["vertical-align"]
		st.CellVAlign = webCellVAlignCascade(role, webHTMLTablePart(el.tag), parent.TableRole, parent.CellVAlign, valign, declared)
	}
	st.CellOwn = false
	st.CellLastRow = false
	// rem is the root element's font size (webSubstituteRootEm).
	if el.tag == "html" {
		p.rootFontPx = st.FontPx
	}
	if st.BackgroundImage != "" {
		st.BackgroundImage = p.webImageURL(st.BackgroundImage)
		if st.BackgroundImage == "" {
			st.HasBg = false
		}
	}
	for i := 0; i < st.BackgroundLayerCount && i < webMaxBackgroundLayers; i++ {
		if st.BackgroundLayers[i].Image == "" {
			continue
		}
		st.BackgroundLayers[i].Image = p.webImageURL(st.BackgroundLayers[i].Image)
	}
	if st.BackgroundLayerCount > 0 {
		st.BackgroundImage = st.BackgroundLayers[0].Image
	}
	// Carry the surface down to the text. A translucent fill is composited
	// over what is behind it (rgba(255,255,255,.06) on a dark page is dark,
	// not white), a gradient by its average, and an opaque one hides whatever
	// was behind. A picture — a covering background image here or above, or an
	// <img> under a positioned overlay — is a backdrop of unknown colour: the
	// text on it keeps the author's colour (webReadableColor).
	under := p.doc.Paper
	if st.HasBackdrop {
		under = st.Backdrop
	}
	if (st.Position == webPositionAbsolute || st.Position == webPositionFixed) && p.anchorSawImage() {
		st.ImageBackdrop = true
	}
	switch {
	case st.HasGradient && st.Gradient != nil:
		avg := webGradientAverage(*st.Gradient)
		st.Backdrop, st.HasBackdrop = webColorOver(avg, under), true
		if avg.A == 255 {
			st.ImageBackdrop = false
		}
	case st.HasBg:
		st.Backdrop, st.HasBackdrop = webColorOver(st.Background, under), true
		if st.Background.A == 255 {
			st.ImageBackdrop = false
		}
	}
	if webStyleImageBackdrop(&st) {
		st.ImageBackdrop = true
	}
	if needsCQ {
		st.CQ = &webCQContext{base: base, decls: rawDecls, vars: st.Vars, parentHeight: parentHeight, rootPx: p.rootFontPx}
		p.hasContainerUnits = true
	}
	// The anchor target is not a CSS property and is never inherited from a
	// sibling: every <a> names its own, and its children inherit that.
	if el.tag == "a" {
		st.Href = htmlstd.UnescapeString(strings.TrimSpace(htmlTagAttribute(attrs, "href")))
		// <a download> saves its target instead of opening it (browser.go);
		// the attribute's value, when it has one, is the file's name.
		if st.Href != "" && htmlTagAttributeHas(attrs, "download") && len(p.doc.DownloadLinks) < 4096 {
			if p.doc.DownloadLinks == nil {
				p.doc.DownloadLinks = make(map[string]string, 4)
			}
			p.doc.DownloadLinks[st.Href] = htmlAttrValue(htmlTagAttribute(attrs, "download"))
		}
	}
	// Decorative icon fonts and aria-hidden helpers render as short words like
	// "navigate_next" or "menu" in a text engine; hide them.
	if strings.EqualFold(strings.TrimSpace(htmlTagAttribute(attrs, "aria-hidden")), "true") {
		st.Hidden = true
	}
	// The hidden attribute is the UA stylesheet's [hidden] { display: none }.
	// Pages park dialogs, templates and not-yet-loaded panels behind it, and the
	// script pass (webdom_runtime.go) removes it from whatever a script reveals,
	// so only content the page itself keeps hidden is skipped. "until-found" is
	// content a browser's find-in-page would open, so it stays readable.
	if htmlTagAttributeHas(attrs, "hidden") && !strings.EqualFold(strings.TrimSpace(htmlTagAttribute(attrs, "hidden")), "until-found") {
		st.Hidden = true
	}
	for _, class := range el.classes {
		switch strings.ToLower(class) {
		case "material-icons", "material-icons-outlined", "material-symbols",
			"material-symbols-outlined", "material-symbols-rounded", "material-symbols-sharp":
			st.Hidden = true
		}
	}
	// An invisible fixed/absolute panel is outside normal flow. The reader
	// cannot lay out an entire offscreen menu as a separate document layer,
	// so letting its children occupy page rows creates a blank first screen
	// (Wikipedia's closed navigation drawer is one example).
	if st.Invisible && (st.Position == webPositionFixed || st.Position == webPositionAbsolute) {
		st.Hidden = true
	}
	return st
}

// webPseudoResult is the resolved style and generated text of one pseudo
// element. Matched is true when the pseudo-element had at least one rule;
// HasContent distinguishes `content: none` (valid, empty) from no content
// declaration (which leaves ::marker with the reader's default bullet).
type webPseudoResult struct {
	Text       string
	Style      webStyle
	HasContent bool
	Matched    bool
}

// pseudoContent computes the style and text of one generated box for el. It
// starts from el's resolved style, clears what does not inherit, then applies
// only the rules whose selector names the same pseudo-element. attr() and
// counter() are resolved against the originating element and the parser's
// counter scopes, which is why this runs during the walk rather than in
// applyWebDecls.
func (p *webParser) pseudoContent(el webElement, attrs string, base webStyle, pseudo uint8) webPseudoResult {
	var result webPseudoResult
	if base.Hidden {
		return result
	}
	st := base
	webResetElementStyle(&st)
	// A generated box is not a form control, even on a <button>.
	st.Control = nil

	chain := make([]webElement, 0, len(p.stack)+1)
	for _, frame := range p.stack {
		chain = append(chain, frame.el)
	}
	chain = append(chain, el)

	var matched []*webRule
	for _, i := range p.ruleIx.candidates(&el) {
		rule := &p.rules[i]
		if rule.sel.pseudo != pseudo {
			continue
		}
		if !rule.sel.matchesIn(chain, rule.mode()) {
			continue
		}
		if len(rule.containers) > 0 && !p.webContainerRulesApply(rule.containers) {
			continue
		}
		matched = append(matched, rule)
	}
	if len(matched) == 0 {
		return result
	}
	result.Matched = true
	webSortCascade(matched)
	merged := make(map[string]string, 8)
	rank := make(map[string]int, 8)
	seq := 0
	cascade := func(decls map[string]string) {
		seq++
		for name, value := range decls {
			merged[name] = value
			rank[name] = seq
		}
	}
	for _, rule := range matched {
		cascade(rule.decls)
	}
	for _, rule := range webImportantCascade(matched) {
		cascade(rule.important)
	}
	// Custom properties inherit from the originating element, and a pseudo
	// rule may add its own. Resolve var() before the declarations are parsed.
	st.Vars = webVarScopeFor(base.Vars, merged)
	for name := range merged {
		if strings.HasPrefix(name, "--") {
			delete(merged, name)
		}
	}
	em := st.FontPx
	cw, ch := p.webOrdinaryContainerSize()
	if raw, ok := merged["font-size"]; ok {
		if px, valid := webFontSizePx(p.webResolveStyleValueIn(raw, st.Vars, em, cw, ch), em); valid {
			em = px
		}
	}
	for name, value := range merged {
		merged[name] = p.webResolveStyleValueIn(value, st.Vars, em, cw, ch)
	}
	webResolveBoxSides(merged, rank)
	webResolveBorderColor(merged, rank)
	applyWebDecls(&st, merged)
	p.applyWebFontFamily(&st, merged)
	if st.Hidden {
		return result
	}
	text, hasContent := webGeneratedContent(st.Content, attrs, p.counters)
	result.Text, result.Style, result.HasContent = text, st, hasContent
	return result
}

// webGeneratedContent turns a `content` value into the text a generated box
// shows. Strings, attr(), counter() and counters() are supported, along with
// the quote keywords; image values have no text equivalent here and are
// skipped. The second result reports whether the value was a valid content
// value at all: `none` is valid and contributes no text, while `normal` falls
// back to the element's own marker (for ::marker) and has no generated text.
func webGeneratedContent(raw, attrs string, counters map[string][]int) (string, bool) {
	s := strings.TrimSpace(raw)
	lower := asciiLower(s)
	switch lower {
	case "", "normal":
		return "", false
	case "none":
		return "", true
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if webAttrSpaceByte(s[i]) {
			i++
			continue
		}
		switch s[i] {
		case '"', '\'':
			q := webSelParser{src: s, pos: i}
			value, ok := q.str()
			if !ok {
				return "", false
			}
			b.WriteString(value)
			i = q.pos
			continue
		case '/':
			// The part after the slash is alternative text for assistive
			// technology, not something the page draws.
			return b.String(), true
		}
		rest := asciiLower(s[i:])
		switch {
		case strings.HasPrefix(rest, "open-quote"):
			b.WriteByte('"')
			i += len("open-quote")
		case strings.HasPrefix(rest, "close-quote"):
			b.WriteByte('"')
			i += len("close-quote")
		case strings.HasPrefix(rest, "no-open-quote"):
			i += len("no-open-quote")
		case strings.HasPrefix(rest, "no-close-quote"):
			i += len("no-close-quote")
		case strings.HasPrefix(rest, "attr("):
			open := i + len("attr")
			end := webSelParenEnd(s, open)
			if end < 0 {
				return "", false
			}
			name := strings.TrimSpace(s[open+1 : end])
			if fields := strings.Fields(name); len(fields) > 0 {
				name = strings.Trim(fields[0], `"'`)
			}
			if name != "" {
				b.WriteString(htmlTagAttribute(attrs, name))
			}
			i = end + 1
		case strings.HasPrefix(rest, "counters(") || strings.HasPrefix(rest, "counter("):
			word := "counter"
			many := strings.HasPrefix(rest, "counters(")
			if many {
				word = "counters"
			}
			open := i + len(word)
			end := webSelParenEnd(s, open)
			if end < 0 {
				return "", false
			}
			args := webSplitSelectorList(s[open+1 : end])
			name, sep, numberStyle := "", "", "decimal"
			if len(args) > 0 {
				name = strings.Trim(strings.TrimSpace(args[0]), `"'`)
			}
			if many {
				if len(args) > 1 {
					sep = strings.Trim(strings.TrimSpace(args[1]), `"'`)
				}
				if len(args) > 2 {
					numberStyle = strings.TrimSpace(args[2])
				}
				values := counters[name]
				parts := make([]string, len(values))
				for k, value := range values {
					parts[k] = webCounterText(value, numberStyle)
				}
				b.WriteString(strings.Join(parts, sep))
			} else {
				if len(args) > 1 {
					numberStyle = strings.TrimSpace(args[1])
				}
				value := 0
				if stack := counters[name]; len(stack) > 0 {
					value = stack[len(stack)-1]
				}
				b.WriteString(webCounterText(value, numberStyle))
			}
			i = end + 1
		case strings.HasPrefix(rest, "url("), strings.HasPrefix(rest, "image("),
			strings.HasPrefix(rest, "image-set("), strings.HasPrefix(rest, "cross-fade("),
			strings.HasPrefix(rest, "linear-gradient("), strings.HasPrefix(rest, "radial-gradient("),
			strings.HasPrefix(rest, "conic-gradient("):
			// An image has no text equivalent in this reader. Skip the
			// functional value without losing the strings around it.
			open := strings.IndexByte(s[i:], '(')
			if open < 0 {
				return "", false
			}
			open += i
			end := webSelParenEnd(s, open)
			if end < 0 {
				return "", false
			}
			i = end + 1
		default:
			return "", false
		}
	}
	return b.String(), true
}

// webCounterText formats one counter value in the list styles a page can ask
// for in counter(name, style). Unknown styles fall back to decimal, which is
// also what a browser does.
func webCounterText(value int, style string) string {
	switch asciiLower(strings.TrimSpace(style)) {
	case "none":
		return ""
	case "decimal-leading-zero":
		if value < 0 {
			return "-" + webPadCounter(-value)
		}
		return webPadCounter(value)
	case "lower-alpha", "lower-latin":
		return webAlphaCounter(value, false)
	case "upper-alpha", "upper-latin":
		return webAlphaCounter(value, true)
	case "lower-roman":
		return webRomanCounter(value, false)
	case "upper-roman":
		return webRomanCounter(value, true)
	case "lower-greek":
		return webGreekCounter(value)
	case "disc":
		return "•"
	case "circle":
		return "◦"
	case "square":
		return "▪"
	case "disclosure-open":
		return "▾"
	case "disclosure-closed":
		return "▸"
	}
	return strconv.Itoa(value)
}

// webGreekCounter counts α, β, … ω, then αα, αβ, … (CSS's lower-greek, an
// alphabetic system of the 24 letters, final sigma left out).
func webGreekCounter(value int) string {
	if value <= 0 {
		return strconv.Itoa(value)
	}
	const letters = "αβγδεζηθικλμνξοπρστυφχψω"
	alphabet := []rune(letters)
	var digits []rune
	for value > 0 {
		value--
		digits = append(digits, alphabet[value%len(alphabet)])
		value /= len(alphabet)
	}
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	return string(digits)
}

func webPadCounter(value int) string {
	s := strconv.Itoa(value)
	if len(s) < 2 {
		return "0" + s
	}
	return s
}

func webAlphaCounter(value int, upper bool) string {
	if value <= 0 {
		return strconv.Itoa(value)
	}
	var digits []byte
	for value > 0 {
		value--
		digits = append(digits, byte('a'+value%26))
		value /= 26
	}
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	if upper {
		for i := range digits {
			digits[i] -= 'a' - 'A'
		}
	}
	return string(digits)
}

func webRomanCounter(value int, upper bool) string {
	if value <= 0 || value >= 4000 {
		return strconv.Itoa(value)
	}
	values := [...]struct {
		value int
		text  string
	}{
		{1000, "m"}, {900, "cm"}, {500, "d"}, {400, "cd"},
		{100, "c"}, {90, "xc"}, {50, "l"}, {40, "xl"},
		{10, "x"}, {9, "ix"}, {5, "v"}, {4, "iv"}, {1, "i"},
	}
	var b strings.Builder
	for _, entry := range values {
		for value >= entry.value {
			b.WriteString(entry.text)
			value -= entry.value
		}
	}
	result := b.String()
	if upper {
		result = strings.ToUpper(result)
	}
	return result
}

// webCounterOp is one counter name and the amount it resets, increments or
// sets. CSS allows several operations in one declaration.
type webCounterOp struct {
	name  string
	value int
}

// webCounterOps parses `counter-reset`, `counter-increment` or `counter-set`.
// A name without a number uses the property's default. `reversed()` and other
// functions this reader does not model are skipped rather than treated as a
// name.
func webCounterOps(value string, fallback int) []webCounterOp {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	fields := webCSSSpaceFields(value)
	var ops []webCounterOp
	for i := 0; i < len(fields); {
		name := fields[i]
		i++
		switch asciiLower(name) {
		case "none", "initial", "inherit", "unset", "revert", "revert-layer":
			continue
		}
		if name == "" || !webSelIdentStart(name, 0) || strings.ContainsAny(name, "()") {
			continue
		}
		op := webCounterOp{name: name, value: fallback}
		if i < len(fields) {
			if n, err := strconv.Atoi(fields[i]); err == nil {
				op.value = n
				i++
			}
		}
		ops = append(ops, op)
	}
	return ops
}

// applyCounters runs the counter declarations of one element. A counter-reset
// pushes a nested instance, recorded on the element's frame so its end tag can
// pop it; counter-increment changes the nearest instance and therefore carries
// across following siblings. Lists get the implicit list-item counter every
// browser exposes.
func (p *webParser) applyCounters(tag string, style webStyle) {
	p.pendingCounterResets = p.pendingCounterResets[:0]
	if tag == "ul" || tag == "ol" {
		p.pushCounterReset("list-item", 0)
	}
	for _, op := range webCounterOps(style.CounterReset, 0) {
		p.pushCounterReset(op.name, op.value)
	}
	for _, op := range webCounterOps(style.CounterIncrement, 1) {
		p.incrementCounter(op.name, op.value)
	}
	for _, op := range webCounterOps(style.CounterSet, 0) {
		p.setCounter(op.name, op.value)
	}
	if tag == "li" {
		p.incrementCounter("list-item", 1)
	}
}

func (p *webParser) pushCounterReset(name string, value int) {
	if p.counters == nil {
		p.counters = make(map[string][]int, 4)
	}
	p.counters[name] = append(p.counters[name], value)
	p.pendingCounterResets = append(p.pendingCounterResets, name)
}

func (p *webParser) incrementCounter(name string, by int) {
	stack := p.counters[name]
	if len(stack) == 0 {
		stack = append(stack, 0)
	}
	stack[len(stack)-1] += by
	p.counters[name] = stack
}

func (p *webParser) setCounter(name string, value int) {
	stack := p.counters[name]
	if len(stack) == 0 {
		stack = append(stack, value)
	} else {
		stack[len(stack)-1] = value
	}
	p.counters[name] = stack
}

func (p *webParser) popCounterResets(names []string) {
	for _, name := range names {
		stack := p.counters[name]
		if len(stack) == 0 {
			continue
		}
		stack = stack[:len(stack)-1]
		if len(stack) == 0 {
			delete(p.counters, name)
		} else {
			p.counters[name] = stack
		}
	}
}

// dropListGutter takes the reader's 18px list gutter (applyWebTagDefaults) off
// an item whose list's start padding the page sets itself (ListPadSet). That
// gutter stands in for the UA sheet's 40px of list padding: a nav menu reset
// to `ul { padding: 0 }` lined its links up 18px in, and a floated menu item
// (html5test's header) lost that much of its own width to it.
func (p *webParser) dropListGutter(style *webStyle) {
	if n := len(p.stack); n > 0 && p.stack[n-1].kind == "list" && p.stack[n-1].style.ListPadSet {
		style.Indent = math.Max(0, style.Indent-18)
	}
}

// listMarkerHangs reports whether the list item about to open hangs its marker
// outside its text: its list (the frame it opens in) has a start padding the
// page set itself (ListPadSet), and the marker is not inside. Wikipedia's
// `ul { margin: 0.3em 0 0 1.6em; padding: 0 }` puts the text at 1.6em and the
// bullet left of it; the reader used to start the text after the bullet.
func (p *webParser) listMarkerHangs(style webStyle) bool {
	n := len(p.stack)
	return n > 0 && p.stack[n-1].kind == "list" && p.stack[n-1].style.ListPadSet && !style.ListInside
}

func (p *webParser) nextBullet(style webStyle) string {
	if len(p.lists) == 0 {
		return ""
	}
	list := &p.lists[len(p.lists)-1]
	// Every list item counts (the list-item counter), whatever its marker.
	if list.reversed {
		list.n--
	} else {
		list.n++
	}
	if style.ListNone {
		return ""
	}
	marker := style.ListStyle
	// A nested bulleted list steps through the UA sheet's markers: disc, then
	// circle (`ul ul`), then square (`ul ul ul`), however deep.
	if !list.ordered && marker == webListStyleDefault {
		switch depth := len(p.lists); {
		case depth == 2:
			marker = webListStyleCircle
		case depth > 2:
			marker = webListStyleSquare
		}
	}
	return webListMarker(marker, style.ListString, list.ordered, list.n)
}

func (p *webParser) ensureBlock() *webBlock {
	if p.current != nil {
		return p.current
	}
	st := p.nearestBlockStyle()
	block := &webBlock{
		Style:              st,
		ContainerIDs:       p.containerPath(),
		Align:              st.Align,
		Indent:             st.Indent,
		TextIndent:         st.TextIndent,
		MarginTop:          st.MarginTop,
		MarginBot:          st.MarginBot,
		WhiteSpace:         st.WhiteSpace,
		Pre:                st.Pre,
		HasBg:              st.HasBg,
		Background:         st.Background,
		BorderLeft:         st.BorderLeft,
		BorderRight:        st.BorderRight,
		BoxBorder:          st.BoxBorder,
		BorderTop:          st.BorderTop,
		BorderBottom:       st.BorderBottom,
		BorderWidth:        st.BorderWidth,
		BorderSideWidth:    st.BorderSideWidth,
		BorderSideStyle:    st.BorderSideStyle,
		BorderSideStyleSet: st.BorderSideStyleSet,
		BorderSideColor:    st.BorderSideColor,
		BorderSideHasColor: st.BorderSideHasColor,
		BorderStyle:        st.BorderStyle,
		Outline:            st.Outline,
		OutlineOffset:      st.OutlineOffset,
		BorderColor:        st.BorderColor,
		HasBorderColor:     st.HasBorderColor,
		Bullet:             p.pendingB,
		MarkerHang:         p.pendingHang,
		MarkerStyle:        p.pendingBStyle,
		HasMarkerStyle:     p.hasPendingBStyle,
		FlexGrow:           st.FlexGrow,
		FlexShrink:         st.FlexShrink,
		FlexBasis:          st.FlexBasis,
		BasisPct:           st.BasisPct,
		Width:              st.Width,
		WidthPct:           st.WidthPct,
		AlignSelf:          st.AlignSelf,
		ZIndex:             st.ZIndex,
		Float:              st.Position == webPositionAbsolute || st.Position == webPositionFixed,
		PosTop:             st.Top,
		PosRight:           st.Right,
		PosBottom:          st.Bottom,
		PosLeft:            st.Left,
		HasTop:             st.HasTop,
		HasRight:           st.HasRight,
		HasBottom:          st.HasBottom,
		HasLeft:            st.HasLeft,
	}
	// The ramp is copied by value onto the block: a background belongs to the
	// element that declared it, unlike the shared pointer the style carries for
	// comparison.
	if st.HasGradient && st.Gradient != nil {
		block.Gradient, block.HasGradient = *st.Gradient, true
	}
	// Everything inside a positioned subtree is measured and painted with it
	// (webpage_layers.go).
	block.Layer = p.openLayerIn(len(p.stack))
	if block.Layer != 0 {
		block.Float = true
	}
	// So is everything inside a floated subtree (webpage_floats.go).
	block.FloatGroup = p.openFloatIn(len(p.stack))
	block.TreeSeq = p.blockFrameSeq()
	block.Boxes = p.boxPath()
	if len(block.Boxes) > 0 {
		block.BoxOwner = block.Boxes[len(block.Boxes)-1]
		// Only the element that owns the box produces its direct block. A
		// paragraph inside a card inherits the card's box ID but must not mark
		// it direct, or the container pass skips the card's real surface.
		nearest := 0
		for i := len(p.stack) - 1; i >= 0; i-- {
			if p.stack[i].kind == "block" || p.stack[i].kind == "list" {
				nearest = p.stack[i].boxID
				break
			}
		}
		if nearest == block.BoxOwner {
			block.OwnsBox = true
			if info, ok := p.doc.Boxes[block.BoxOwner]; ok {
				info.direct = true
				p.doc.Boxes[block.BoxOwner] = info
			}
		}
	}
	p.tagFlexBlock(block, st)
	block.Details, block.DetailsShown = p.detailsTag()
	// Text that follows an inline image continues its line (webpage_inline.go).
	block.InlineGroup = p.inlineGroup
	p.pendingB, p.pendingBStyle, p.hasPendingBStyle = "", webStyle{}, false
	p.pendingHang = false
	p.current = block
	return block
}

// tagFlexBlock keeps every enclosing flex/grid container. The outermost
// membership is laid out first; a card's sub-layout then reveals the next one.
// containerPath returns the open containment containers, outermost first.
// The slice is only allocated on pages that actually declare one.
func (p *webParser) containerPath() []int {
	var ids []int
	for i := range p.stack {
		if id := p.stack[i].containerID; id != 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

// boxPath returns the visual container boxes enclosing the current element,
// outermost first. It mirrors containerPath for backgrounds/borders/shadows.
func (p *webParser) boxPath() []int {
	var ids []int
	for i := range p.stack {
		if id := p.stack[i].boxID; id != 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

// webStyleHasBox reports whether an element contributes a visible box that
// must survive even when all of its content is nested block elements.
func webStyleHasBox(st webStyle) bool {
	return st.HasBg || st.BackgroundLayerCount > 0 || st.BackgroundImage != "" ||
		st.HasGradient || st.BoxBorder || st.BorderLeft || st.BorderRight ||
		st.BorderTop || st.BorderBottom || st.Outline || st.HasBoxShadow || st.Radius > 0
}

// webStyleHasLayoutBox reports whether an element constrains its children's
// measure even if it has no surface of its own. An article wrapper with
// `max-width` and padding used to disappear from every child's ancestry, so
// its paragraphs ran to the page edge and the image/list inset did not line up.
func webStyleHasLayoutBox(st webStyle) bool {
	return st.Width > 0 || st.WidthPct > 0 || st.WidthFit != 0 || st.MaxWidth > 0 || st.MaxWidthPct > 0 ||
		st.MinWidth > 0 || st.MinWidthPct > 0 || st.Height > 0 || st.MinHeight > 0 ||
		st.Indent != 0 || st.IndentRight != 0
}

func (p *webParser) tagFlexBlock(block *webBlock, st webStyle) {
	for i := range p.flexStack {
		f := &p.flexStack[i]
		if f.item == 0 {
			f.item = 1
			f.itemStyle = st
		}
		membership := webFlexMembershipFromFrame(f)
		if i == 0 {
			block.setFlexMembership(membership)
		} else {
			block.FlexTrail = append(block.FlexTrail, membership)
		}
	}
}

// detailsTag reports the section a new block belongs to, and whether the block
// is drawn while that section is closed. A block in a summary is; everything
// else waits for the section to be opened. The loop covers nesting: a section
// inside a closed outer section stays hidden however its own state is set.
func (p *webParser) detailsTag() ([]*webControl, bool) {
	if len(p.details) == 0 {
		return nil, false
	}
	chain := make([]*webControl, 0, len(p.details))
	shown := true
	for _, f := range p.details {
		chain = append(chain, f.ctrl)
		if !f.ctrl.Checked && !f.inSummary {
			shown = false
		}
	}
	return chain, shown
}

func (p *webParser) addBreak() {
	block := p.ensureBlock()
	style := p.topStyle()
	block.Runs = append(block.Runs, webRun{Text: "\n", Style: style})
	p.space = false
}

// addControlFromInput turns an <input> into a run the reader draws and the
// browser can fill. A text-ish field shows its value, or its placeholder as a
// prompt, inside a field box; a tick box shows a drawn box; a submit shows its
// label as the button it is. Hidden inputs and the types the reader cannot do
// anything with (file pickers, colour wells, sliders) are still skipped, the
// way every <input> was when they were all void tags.
func (p *webParser) addControlFromInput(attrs string, style webStyle) {
	kind := strings.ToLower(strings.TrimSpace(htmlTagAttribute(attrs, "type")))
	if kind == "" {
		kind = "text"
	}
	name := htmlAttrValue(htmlTagAttribute(attrs, "name"))
	value := htmlAttrValue(htmlTagAttribute(attrs, "value"))
	placeholder := htmlAttrValue(htmlTagAttribute(attrs, "placeholder"))
	checked := htmlTagAttributeHas(attrs, "checked")

	switch kind {
	case "submit", "button":
		ctrl := &webControl{Kind: "submit", Name: name, Value: value, Form: p.form}
		p.emitControl(ctrl, style, true)
		return
	case "hidden":
		// Not drawn, but not dropped: a search page that requires `ie=utf-8`
		// or a site key answers a query without it with an error page.
		if name != "" {
			if p.formHidden == nil {
				p.formHidden = map[int][]webHiddenField{}
			}
			p.formHidden[p.form.ID] = append(p.formHidden[p.form.ID], webHiddenField{Name: name, Value: value})
		}
		return
	case "checkbox", "radio":
		// The box is its own run; the label is the text beside it in the
		// markup, which keeps drawing as normal body text.
		ctrlKind := "check"
		if kind == "radio" {
			ctrlKind = "radio"
		}
		ctrl := &webControl{Kind: ctrlKind, Name: name, Value: value, Checked: checked, Form: p.form}
		p.emitControl(ctrl, style, false)
		return
	case "text", "search", "email", "url", "tel", "number", "password":
		if name != "" && p.form.Field == "" {
			p.form.Field = name
		}
		ctrl := &webControl{
			Kind:        "text",
			Name:        name,
			Placeholder: placeholder,
			Value:       value,
			Secret:      kind == "password",
			Form:        p.form,
		}
		p.emitControl(ctrl, style, true)
		return
	}
}

// emitControl draws one control as a run and files it where the browser looks
// for it. boxed fills a field background behind the text: a field and a button
// read as a control, while a tick box is just the box glyph.
func (p *webParser) emitControl(ctrl *webControl, style webStyle, boxed bool) {
	st := style
	st.Control = ctrl
	ctrl.Width, ctrl.WidthPct, ctrl.Height = style.Width, style.WidthPct, style.Height
	button := boxed && ctrl.Kind == "submit"
	if button {
		// A submit button is an inline-block box with its padding, border and
		// radius (webpage_inlinebox.go).
		webButtonBox(&st, st.HasBg || st.HasGradient)
	}
	if boxed && !st.HasBg {
		// A filled chip sized by the text it shows, so a field cannot be
		// mistaken for body copy. An author's background (including
		// input:disabled) wins over this default. The field's text is read
		// against the chip, not the page under it.
		st.HasBg = true
		st.Background = sdl.Color{R: 232, G: 237, B: 244, A: 255}
		st.Backdrop, st.HasBackdrop, st.ImageBackdrop = st.Background, true, false
	}
	// The italic that marks an empty field's prompt is decided at layout time
	// instead (webControl.PlaceholderShown): fixing it here left the prompt
	// looking italic after the field had been filled in.
	if !button {
		p.appendRun(ctrl.Display(), st)
		p.space = true
		return
	}
	room, chip := webInlineEdge(&st, true, false, true)
	p.openInlineBox(room, chip, true)
	flushesAt, runsAt := p.lineFlushes, 0
	if p.current != nil {
		runsAt = len(p.current.Runs)
	}
	if label := ctrl.Display(); label != "" {
		p.appendRun(label, st)
	}
	trail, trailChip := webInlineEdge(&st, true, false, false)
	p.closeInlineBox(&webFrame{inlineFlushes: flushesAt, inlineRuns: runsAt, inlineTrail: trail, inlineTrailChip: trailChip, ownChip: true, startedChip: true})
	p.space = true
}

// webControlBoundary reports an element that starts a new context: a form
// control whose closing tag never came must end there, or the rest of the page
// is swallowed as the field's value (and drawn as one long field).
func webControlBoundary(name string) bool {
	switch name {
	case "form", "div", "p", "table", "tr", "td", "ul", "ol", "li",
		"button", "input", "textarea", "select", "option", "h1", "h2", "h3",
		"h4", "h5", "h6", "br", "hr", "body", "html", "main", "article",
		"section", "nav", "aside", "header", "footer":
		return true
	}
	return false
}

// htmlTagAttributeHas reports whether a boolean attribute is present. checked
// and selected carry their meaning in their presence, so htmlTagAttribute's
// "value, or the empty string" answer cannot tell "checked" from "absent" —
// and a page that ships a preselected option would always read as unselected.
func htmlTagAttributeHas(attrs, want string) bool {
	want = strings.ToLower(want)
	i := 0
	for i < len(attrs) {
		for i < len(attrs) && (attrs[i] == ' ' || attrs[i] == '\t' || attrs[i] == '\r' || attrs[i] == '\n' || attrs[i] == '/') {
			i++
		}
		start := i
		for i < len(attrs) && attrs[i] != '=' && attrs[i] != ' ' && attrs[i] != '\t' && attrs[i] != '\r' && attrs[i] != '\n' && attrs[i] != '>' {
			i++
		}
		name := strings.ToLower(attrs[start:i])
		if name == want {
			return true
		}
		// A '>' the name scan stopped on without consuming (a tag that
		// findTextBrowserTagEnd extended past a stray quote) is stepped over,
		// or the loop would never end.
		if i == start && i < len(attrs) && attrs[i] != '=' {
			i++
			continue
		}
		// Step over a value, quoted or not, so a space inside one cannot be read
		// as the start of the next attribute.
		for i < len(attrs) && (attrs[i] == ' ' || attrs[i] == '\t' || attrs[i] == '\r' || attrs[i] == '\n') {
			i++
		}
		if i < len(attrs) && attrs[i] == '=' {
			i++
			for i < len(attrs) && (attrs[i] == ' ' || attrs[i] == '\t' || attrs[i] == '\r' || attrs[i] == '\n') {
				i++
			}
			if i < len(attrs) && (attrs[i] == '"' || attrs[i] == '\'') {
				quote := attrs[i]
				i++
				for i < len(attrs) && attrs[i] != quote {
					i++
				}
				if i < len(attrs) {
					i++
				}
				continue
			}
			for i < len(attrs) && attrs[i] != ' ' && attrs[i] != '\t' && attrs[i] != '\r' && attrs[i] != '\n' && attrs[i] != '>' {
				i++
			}
		}
	}
	return false
}

// startTextarea opens a <textarea>. Its shipped value is the text between the
// tags, so the control is drawn by endTextarea once that is known.
func (p *webParser) startTextarea(attrs string, style webStyle) {
	if p.inHead {
		return
	}
	if p.textarea != nil {
		// A nested textarea is invalid markup; close the first rather than
		// losing both fields.
		p.endTextarea()
	}
	p.textarea = &webControl{
		Kind:        "textarea",
		Name:        htmlAttrValue(htmlTagAttribute(attrs, "name")),
		Placeholder: htmlAttrValue(htmlTagAttribute(attrs, "placeholder")),
		Form:        p.form,
	}
	// The element's own style (background, colour, border, :disabled state)
	// belongs to the field, not to whatever element happens to be on top when
	// the closing tag arrives.
	p.textareaStyle = style
	p.textareaText.Reset()
}

// startButton and endButton keep a submit button's visible label separate from
// its value attribute. Most login forms use <button> rather than <input>, and
// its label often differs from the value the server expects.
func (p *webParser) startButton(attrs string, style webStyle) {
	if p.buttonCtrl != nil {
		p.endButton()
	}
	p.buttonCtrl = &webControl{
		Kind: "submit", Name: htmlAttrValue(htmlTagAttribute(attrs, "name")),
		Value: htmlAttrValue(htmlTagAttribute(attrs, "value")), Form: p.form,
	}
	p.buttonStyle = style
	p.buttonText.Reset()
}

func (p *webParser) endButton() {
	ctrl := p.buttonCtrl
	if ctrl == nil {
		return
	}
	ctrl.Label = strings.Join(strings.Fields(p.buttonText.String()), " ")
	ctrl.IconOnly = ctrl.Label == ""
	p.buttonCtrl = nil
	p.buttonText.Reset()
	p.emitControl(ctrl, p.buttonStyle, true)
}

// endTextarea draws the field the open <textarea> declared.
func (p *webParser) endTextarea() {
	ctrl := p.textarea
	if ctrl == nil {
		return
	}
	value := strings.TrimSpace(p.textareaText.String())
	p.textarea = nil
	p.textareaText.Reset()
	if value != "" {
		ctrl.Value = value
	}
	if ctrl.Name != "" && p.form.Field == "" {
		p.form.Field = ctrl.Name
	}
	ctrl.Form = p.form
	p.emitControl(ctrl, p.textareaStyle, true)
}

// startSelect opens a <select>: its options are collected and drawn as one
// stepping control by endSelect.
func (p *webParser) startSelect(attrs string) {
	if p.inHead {
		return
	}
	if p.selectCtrl != nil {
		p.endSelect()
	}
	p.selectCtrl = &webControl{
		Kind: "select",
		Name: htmlAttrValue(htmlTagAttribute(attrs, "name")),
		Form: p.form,
	}
}

// startOption adds one entry to the open <select>, remembering which entry the
// markup marked as selected.
func (p *webParser) startOption(attrs string) {
	if p.selectCtrl == nil {
		p.addText(" ")
		return
	}
	p.selectOption = htmlAttrValue(htmlTagAttribute(attrs, "value"))
	p.selectOptionText.Reset()
	if htmlTagAttributeHas(attrs, "selected") && len(p.selectCtrl.Options) > 0 {
		p.selectCtrl.Choice = len(p.selectCtrl.Options)
	}
	p.selectOptionSelected = htmlTagAttributeHas(attrs, "selected")
}

// endOption files the option's label, which is what the reader shows. The
// value attribute is what the form submits, so it is kept separately on the
// control that endSelect builds.
func (p *webParser) endOption() {
	if p.selectCtrl == nil {
		return
	}
	label := strings.TrimSpace(p.selectOptionText.String())
	if label == "" {
		label = p.selectOption
	}
	p.selectCtrl.Options = append(p.selectCtrl.Options, label)
	p.selectCtrl.OptionValues = append(p.selectCtrl.OptionValues, p.selectOption)
	if p.selectOptionSelected {
		p.selectCtrl.Choice = len(p.selectCtrl.Options) - 1
	}
	p.selectOption = ""
	p.selectOptionSelected = false
	p.selectOptionText.Reset()
}

// endSelect draws the finished control.
func (p *webParser) endSelect() {
	ctrl := p.selectCtrl
	if ctrl == nil {
		return
	}
	if p.selectOption != "" || p.selectOptionText.Len() > 0 {
		p.endOption()
	}
	p.selectCtrl = nil
	if len(ctrl.Options) == 0 {
		return
	}
	if ctrl.Choice < 0 || ctrl.Choice >= len(ctrl.Options) {
		ctrl.Choice = 0
	}
	if ctrl.Name != "" && p.form.Field == "" {
		p.form.Field = ctrl.Name
	}
	ctrl.Form = p.form
	p.emitControl(ctrl, p.topStyle(), true)
}

func (p *webParser) addText(raw string) {
	p.addTextStyled(raw, p.topStyle())
}

// addTextStyled adds a text node with a style that is not necessarily the top
// of the element stack. Generated content uses the style it resolved for the
// pseudo-element; everything else goes through addText above.
func (p *webParser) addTextStyled(raw string, style webStyle) {
	if p.inHead || raw == "" || style.Hidden {
		return
	}
	if fl := p.firstLetter; fl != nil && p.textarea == nil && p.selectCtrl == nil && p.buttonCtrl == nil && !style.IconFont {
		if cut := webFirstLetterEnd(raw); cut > 0 {
			p.firstLetter = nil
			letter := fl.style
			letter.Href, letter.Link, letter.Underline = style.Href, style.Link, style.Underline
			letter.FloatSide, letter.Hidden = 0, false
			p.addTextStyled(raw[:cut], letter)
			if raw = raw[cut:]; raw == "" {
				return
			}
		}
	}
	// A field's own text belongs to the field, not to the page: a textarea's
	// shipped value and an option's label are collected here and drawn once, by
	// the closing tag, as the control's run.
	if p.textarea != nil {
		p.textareaText.WriteString(htmlstd.UnescapeString(raw))
		return
	}
	if p.selectCtrl != nil {
		p.selectOptionText.WriteString(htmlstd.UnescapeString(raw))
		return
	}
	if style.IconFont {
		// A ligature icon font (Material Icons/Symbols) spells its glyphs as
		// words: "menu" draws a hamburger, not the word. The reader's face has
		// no such ligatures, so a known name becomes the nearest glyph and the
		// rest are dropped (webpage_fonts.go), on a button's label too.
		raw = webIconFontText(raw)
		style.Transform = 0
		if raw == "" {
			return
		}
	}
	// Text set in a web font is what makes its file worth fetching.
	p.noteWebFontUse(style, raw)
	if p.buttonCtrl != nil {
		if p.buttonText.Len() < 512 {
			p.buttonText.WriteString(htmlstd.UnescapeString(raw))
		}
		return
	}
	if p.textLen >= webMaxTextBytes || len(p.blocks) >= webMaxBlocks {
		p.trunc = true
		return
	}
	text := strings.ToValidUTF8(htmlstd.UnescapeString(raw), "")
	whiteSpace := style.WhiteSpace
	if style.Pre && whiteSpace == webWhiteSpaceNormal {
		whiteSpace = webWhiteSpacePre
	}
	if whiteSpace != webWhiteSpaceNormal && whiteSpace != webWhiteSpaceNowrap {
		text = strings.ReplaceAll(text, "\r\n", "\n")
		text = strings.ReplaceAll(text, "\r", "\n")
		if text == "" {
			return
		}
		if whiteSpace == webWhiteSpacePreLine {
			// pre-line keeps the authored line structure but collapses spaces
			// inside each line, exactly like normal text does between breaks.
			lines := strings.Split(text, "\n")
			for i, line := range lines {
				lines[i] = strings.Join(strings.Fields(line), " ")
			}
			text = strings.Join(lines, "\n")
			if text == "" {
				return
			}
		}
		p.appendRun(text, style)
		return
	}
	collapsed := strings.Join(strings.Fields(text), " ")
	if collapsed == "" {
		p.space = true
		// strings.Fields drops no-break spaces too, which a browser keeps: a
		// table cell holding only &nbsp; is one empty line tall
		// (keepEmptyCell), a spacer paragraph keeps its height (keepSpacer).
		if strings.Trim(text, " \t\n\r\f") != "" {
			p.markBlankLine()
		}
		return
	}
	// Whitespace can sit on either side of a tag boundary: `<b>x</b> next`
	// starts the following text node with a space, and that space has to
	// survive so the layout does not glue "xnext" together.
	leadsWithSpace := false
	if r, _ := utf8.DecodeRuneInString(text); unicode.IsSpace(r) {
		leadsWithSpace = true
	}
	block := p.ensureBlock()
	if (p.space || leadsWithSpace) && len(block.Runs) > 0 {
		last := block.Runs[len(block.Runs)-1].Text
		if last != "" && !strings.HasSuffix(last, " ") && !strings.HasSuffix(last, "\n") {
			collapsed = " " + collapsed
		}
	}
	// The gap between an inline image and the text after it lives on the
	// text's block, which starts empty (webpage_inline.go).
	if len(block.Runs) == 0 && block.InlineGroup != 0 && (p.space || leadsWithSpace) {
		block.InlineSpace = true
	}
	p.space = false
	trimmed := strings.TrimRight(text, " \t\r\n")
	if len(trimmed) < len(text) {
		p.space = true
	}
	p.appendRun(collapsed, style)
}

func (p *webParser) appendRun(text string, style webStyle) {
	if text == "" {
		return
	}
	if p.textLen >= webMaxTextBytes {
		p.trunc = true
		return
	}
	if remaining := webMaxTextBytes - p.textLen; len(text) > remaining {
		// Keep the part that fits and flag the page as truncated; dropping the
		// whole run would lose the text that came before the cap.
		cut := remaining
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
		p.trunc = true
		if text == "" {
			return
		}
	}
	block := p.ensureBlock()
	n := len(block.Runs)
	// A run that closed an inline box (Trail), or text that opens one, is not
	// merged into its neighbour: the box's edges belong to its first and last
	// words.
	if n > 0 && block.Runs[n-1].Style == style && block.Runs[n-1].Trail == 0 && !block.Runs[n-1].ClosesChip &&
		p.pendingLead == 0 && !p.pendingChip && !p.pendingOpens {
		block.Runs[n-1].Text += text
	} else {
		run := webRun{Text: text, Style: style}
		run.Lead, run.ChipLead, run.OpensChip = p.takeInlineLead()
		block.Runs = append(block.Runs, run)
	}
	p.textLen += len(text)
}

func (p *webParser) flush() {
	// Every block boundary ends an inline-image chain; addImageElement
	// reopens it after its own flushes. The left edges of inline boxes that
	// had no word yet belonged to the line that ends here.
	p.inlineGroup = 0
	p.clearInlineLead()
	p.lineFlushes++
	if p.current == nil {
		return
	}
	block := p.current
	p.current = nil
	// Drop a block that only carried whitespace.
	if !webBlockKept(block) {
		return
	}
	for i := range block.Runs {
		if i == len(block.Runs)-1 {
			block.Runs[i].Text = strings.TrimRight(block.Runs[i].Text, " ")
		}
	}
	p.blocks = append(p.blocks, *block)
}

// webBlockKept reports whether a finished block draws anything — text, a list
// marker, a rule, a picture, a frame — or holds the column of an empty table
// cell (keepEmptyCell). flush drops the rest.
func webBlockKept(block *webBlock) bool {
	if block.Bullet != "" || block.Rule || block.Image || block.Video || block.Placeholder || block.TableEmptyCell || block.Spacer {
		return true
	}
	for _, run := range block.Runs {
		if strings.TrimSpace(strings.ReplaceAll(run.Text, "\n", "")) != "" {
			return true
		}
	}
	return false
}

// --- Layout ---------------------------------------------------------------

// webBaseFontSize mirrors getCachedFont's default so CSS px map onto the same
// body text size the rest of the UI uses.
func webBaseFontSize(config *Config, fontName string) float64 {
	if config != nil {
		if size, ok := config.Variables.FontSizes[fontName]; ok && size > 0 {
			return float64(size)
		}
	}
	return 24
}

// layoutFor returns the measured page, reusing the previous layout while the
// width, font and page colours are unchanged.
// webGradientStamp fingerprints a ramp for the layout cache. A page whose paper
// changed from one gradient to another keeps the same average colour, so the
// colours alone would let a stale layout through with the old ramp baked into
// its rects.
type webGradientStampValue struct {
	axis                 uint8 // 0 horizontal, 1 vertical, 2 radial, 3 conic, 4 angled
	stops                int
	cx, cy, from, angle  float64
	cornerX, cornerY     int8
	first, last          sdl.Color
}

func webGradientStamp(g webGradient, has bool) webGradientStampValue {
	var out webGradientStampValue
	if !has || len(g.Stops) == 0 {
		return out
	}
	out.stops = len(g.Stops)
	out.first, out.last = g.Stops[0].Color, g.Stops[len(g.Stops)-1].Color
	out.cx, out.cy, out.from, out.angle = g.CX, g.CY, g.From, g.Angle
	out.cornerX, out.cornerY = int8(g.CornerX), int8(g.CornerY)
	switch {
	case g.Conic:
		out.axis = 3
	case g.Radial:
		out.axis = 2
	case g.Angled:
		out.axis = 4
	case g.Vertical:
		out.axis = 1
	}
	return out
}

// webLayoutStamp is what a cached layout was measured for. It is compared on
// every frame, so it is a comparable struct rather than the formatted string
// it used to be (a Sprintf and its garbage per frame). The gradient part is
// only formatted when the page has a gradient paper.
type webLayoutStamp struct {
	valid    bool
	width    int32
	font     string
	textSize int
	paper    sdl.Color
	text     sdl.Color
	gradient webGradientStampValue
	zoom     float64
}

func (d *webDocument) layoutFor(config *Config, elem Element, width int32) *webLayout {
	stamp := webLayoutStamp{
		valid: true, width: width, font: elem.Font, textSize: a11yScaleFontSize(16),
		paper: sdl.Color{R: d.Paper.R, G: d.Paper.G, B: d.Paper.B},
		text:  sdl.Color{R: d.Text.R, G: d.Text.G, B: d.Text.B},
		zoom:  browserZoomScale(),
	}
	if d.HasPaperGradient {
		stamp.gradient = webGradientStamp(d.PaperGradient, true)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.layout != nil && d.stamp == stamp {
		if !d.sizesDirty {
			return d.layout
		}
		// Pictures or web fonts arrived since it was measured. It is measured
		// again, but not on every frame while a page's pictures stream in: on a
		// handheld each layout of a long page is a pause the scroll can see, and
		// a front page's pictures land one after another for seconds.
		if every := webTuneNow().RelayoutEvery; every > 0 && time.Since(d.laidOutAt) < every {
			return d.layout
		}
	}
	d.layout = d.computeLayout(config, elem, width)
	d.stamp = stamp
	d.sizesDirty = false
	d.laidOutAt = time.Now()
	return d.layout
}

// noteSizesArrived marks the layout stale for pictures or web font files that
// arrived: layoutFor measures the page again soon, not necessarily on the
// next frame (webTune.RelayoutEvery). Any thread.
func (d *webDocument) noteSizesArrived() {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.sizesDirty = true
	d.mu.Unlock()
}

// invalidateFormState drops the cached layout so the next frame measures the
// page again. A form control's drawn text comes from its own state (the value
// typed into a field, the item a select was stepped to, a tick box's tick), so
// one of those changing is the one case where the page's text changes without
// the document being replaced.
func (d *webDocument) invalidateFormState() {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.stamp = webLayoutStamp{}
	d.mu.Unlock()
}

func (d *webDocument) computeLayout(config *Config, elem Element, width int32) *webLayout {
	if width < 80 {
		return nil
	}
	// The reader's zoom scales the body size the whole page is measured from,
	// so the wrap, row height and headings all move together. The default step
	// is 1.0, leaving every other web view untouched until a reader zooms.
	baseSize := webBaseFontSize(config, elem.Font) * browserZoomScale()
	baseFont := getFontSized(config, elem.Font, int(baseSize))
	if baseFont == nil {
		return nil
	}
	rowH := int32(baseFont.Height()) + 6
	if rowH < 18 {
		rowH = 18
	}
	layout := &webLayout{RowH: rowH}

	fonts := make(map[int]*ttf.Font, 8)
	fontSize := func(cssPx float64) int {
		size := int(math.Round(cssPx * baseSize / 16))
		minSize := int(math.Round(baseSize * 0.72))
		// Twice the body size: an h1 (2em) and a hero title keep their
		// scale instead of every large heading shrinking to one 1.5em cap.
		// emit() gives a tall line as many grid rows as its face needs.
		maxSize := int(math.Round(baseSize * 2))
		if size < minSize {
			size = minSize
		}
		if size > maxSize {
			size = maxSize
		}
		if size < 8 {
			size = 8
		}
		return size
	}
	fontAt := func(cssPx float64) *ttf.Font {
		size := fontSize(cssPx)
		if font, ok := fonts[size]; ok {
			return font
		}
		font := getFontSized(config, elem.Font, size)
		fonts[size] = font
		// The size getFontSized opened it at, which a fallback face for a
		// script it lacks is opened at too (webpage_script.go).
		webNoteFaceSize(font, a11yScaleFontSize(uiLegibleFontSize(size)))
		return font
	}
	// Bold, italic and monospace runs are measured and drawn in faces of their
	// own (webpage_fonts.go), opened at the size the regular face is: a real
	// bold cut instead of a doubled regular one, and the italic no longer a
	// step smaller than its line when the text size is raised.
	styledFonts := make(map[webFaceRequest]*ttf.Font, 8)
	fontAtStyle := func(style webStyle) *ttf.Font {
		web := style.WebFont
		if style.IconFont {
			// Material ligature names are rewritten to the glyphs the reader has.
			web = 0
		}
		if !style.Bold && !style.Italic && !style.Mono && !style.Serif && web == 0 {
			return fontAt(style.FontPx)
		}
		size := fontSize(style.FontPx)
		key := webFaceRequest{size: size, bold: style.Bold, italic: style.Italic, mono: style.Mono, serif: style.Serif, web: web}
		if font, ok := styledFonts[key]; ok {
			return font
		}
		if web != 0 {
			// The page's own font, once its file has arrived
			// (webpage_webfont.go); until then the face below.
			if font := d.webFontFor(web, size, style.Bold, style.Italic); font != nil {
				styledFonts[key] = font
				return font
			}
			if !style.Bold && !style.Italic && !style.Mono && !style.Serif {
				return fontAt(style.FontPx)
			}
		}
		font := webReaderFace(config, elem.Font, size, style.Bold, style.Italic, style.Mono, style.Serif)
		if font == nil {
			font = fontAt(style.FontPx)
		}
		styledFonts[key] = font
		return font
	}

	// The page is laid out at the full width of the view, as a browser lays a
	// page out at its window's width: the handhelds' 1024-1280 px panels are
	// wide enough, and the page's media queries are answered for this same
	// width (webMediaViewport). It used to be squeezed into a 38 em reading
	// column, so a layout chosen for the screen's width was drawn in two
	// thirds of it.
	contentW := width - 12
	if contentW < 60 {
		return nil
	}
	padX := (width - contentW) / 2
	layoutOnce := func(blocks []webBlock) (*webLayout, map[int]webContainerSize) {
		layout := &webLayout{RowH: rowH}
		pageWidth := float64(contentW) * 16 / baseSize
		sizes := webNewContainerSizes(d.Containers, pageWidth)
		l := &webLayouter{
			doc: d, layout: layout, rowH: rowH, baseSize: baseSize, fontAt: fontAt, fontAtStyle: fontAtStyle,
			spaceWidths:    map[*ttf.Font]int32{},
			sanitized:      make(map[webSanitizeKey]string, 256),
			containerSizes: sizes,
			containerRows:  make(map[int]webContainerRowSpan),
		}
		// Absolutely positioned subtrees are measured on their own and painted
		// in stacking order, so they take no room in the flow
		// (webpage_layers.go). A positioned box's line split by an inline
		// image is stitched first, so it stays one layer.
		blocks = webMergeInlineChains(blocks)
		flow := make([]webBlock, 0, len(blocks))
		var floats []webBlock
		for _, b := range blocks {
			if b.Hidden() {
				continue
			}
			if b.Float {
				floats = append(floats, b)
				continue
			}
			flow = append(flow, b)
		}
		l.layoutBlocks(flow, padX, contentW)

		// A table cell paints its bottom padding a row past its content rows
		// (webTagCellSurface), so the page keeps that row: a table at the end
		// of a page was cut there at max scroll.
		for int32(len(layout.Rows))*rowH < layout.PaintBottom && len(layout.Rows) < webMaxRows {
			idx := int32(len(layout.Rows))
			layout.Rows = append(layout.Rows, webRow{Y: idx * rowH})
		}
		layout.Height = int32(len(layout.Rows)) * rowH
		l.layoutLayers(floats, padX, contentW)
		l.paintContainerBoxes()
		l.finishContainerSizes(layout)
		return layout, sizes
	}

	blocks := d.Blocks
	layout, sizes := layoutOnce(blocks)
	if d.HasContainerUnits {
		// The parser's container size is only a model. Replay cq* against the
		// size layout just measured, then lay out again. A content-sized box
		// can change height when its child's cq* margin changes, so iterate a
		// few times until the measured facts stop moving.
		for pass := 0; pass < 3; pass++ {
			refined, changed := webRefineContainerBlocks(blocks, sizes)
			if !changed {
				break
			}
			blocks = refined
			nextLayout, nextSizes := layoutOnce(blocks)
			same := webContainerSizesEqual(sizes, nextSizes)
			layout, sizes = nextLayout, nextSizes
			if same {
				break
			}
		}
		// Keep the measured styles on the document so callers inspecting the
		// laid-out page see the same lengths the renderer drew.
		d.Blocks = blocks
	}
	// Surfaces paint by z-index and then in tree order, so a section's
	// background no longer covers the code block inside it (webpage_layers.go).
	webSortPaintOrder(layout)
	// Sticky elements are copied whole from the finished page, surfaces
	// included, into the bars the painter pins.
	webCollectStickies(d, layout, &webLayouter{doc: d, layout: layout, rowH: rowH, baseSize: baseSize})
	// A relatively positioned element that CSS paints over an earlier layer
	// is painted in stacking order with the layers (webpage_layers.go).
	webLiftPositioned(d, layout)
	layout.Links = collectWebLinks(layout)
	// Play cards and form controls are cursor stops too: the same hit shape as
	// a link, with the video or the control attached instead of a destination.
	// One merge keeps every kind in reading order, so a field sits where it
	// drew and the D-pad walks the page top to bottom.
	layout.Links = append(layout.Links, collectWebVideoLinks(layout)...)
	layout.Links = append(layout.Links, collectWebControls(layout)...)
	// So are the links and fields inside layers and floats, unless the flow
	// already reaches the same destination.
	seen := make(map[string]bool, len(layout.Links))
	for _, hit := range layout.Links {
		if hit.Href != "" {
			seen[hit.Href] = true
		}
	}
	layout.Links = append(layout.Links, collectWebLayerHits(layout, seen)...)
	if len(layout.Links) > 0 {
		sort.SliceStable(layout.Links, func(i, j int) bool {
			return layout.Links[i].Row < layout.Links[j].Row
		})
	}
	return layout
}

// webNewContainerSizes seeds each container from the page's real content width.
// A percentage width resolves against its nearest container ancestor (or the
// page column when it has none); an auto width takes that parent's size. The
// flex pass later replaces these with the item's measured track width.
func webNewContainerSizes(containers map[int]webContainerInfo, pageWidth float64) map[int]webContainerSize {
	sizes := make(map[int]webContainerSize, len(containers))
	for id, info := range containers {
		width := pageWidth
		for _, ancestor := range info.ancestors {
			switch {
			case ancestor.widthPct > 0:
				width = width * ancestor.widthPct / 100
			case ancestor.width > 0:
				width = ancestor.width
			}
		}
		switch {
		case info.widthPct > 0:
			width = width * info.widthPct / 100
		case info.width > 0:
			width = info.width
		}
		sizes[id] = webContainerSize{Width: width, Height: info.height}
	}
	return sizes
}

// webNearestBlockContainer returns the measured size of the innermost container
// surrounding a block. A block outside every container keeps the parser's
// viewport fallback.
func webNearestBlockContainer(ids []int, sizes map[int]webContainerSize) (webContainerSize, bool) {
	for i := len(ids) - 1; i >= 0; i-- {
		if size, ok := sizes[ids[i]]; ok && size.Width > 0 {
			return size, true
		}
	}
	return webContainerSize{}, false
}

// webRefineContainerBlocks replays every cq* style against the measured sizes.
// It copies the slice first so the caller can compare and avoid relayout when
// nothing actually changed.
func webRefineContainerBlocks(blocks []webBlock, sizes map[int]webContainerSize) ([]webBlock, bool) {
	out := append([]webBlock(nil), blocks...)
	changed := false
	for i := range out {
		b := &out[i]
		size, ok := webNearestBlockContainer(b.ContainerIDs, sizes)
		if !ok {
			continue
		}
		if b.Style.CQ != nil {
			if st := b.Style.CQ.resolve(b.Style, size.Width, size.Height); st != b.Style {
				b.Style = st
				changed = true
			}
		}
		for j := range b.Runs {
			run := &b.Runs[j]
			if run.Style.CQ == nil {
				continue
			}
			if st := run.Style.CQ.resolve(run.Style, size.Width, size.Height); st != run.Style {
				run.Style = st
				changed = true
			}
		}
	}
	return out, changed
}

func webContainerSizesEqual(a, b map[int]webContainerSize) bool {
	if len(a) != len(b) {
		return false
	}
	for id, size := range a {
		if other, ok := b[id]; !ok || other != size {
			return false
		}
	}
	return true
}

// finishContainerSizes turns the row extents gathered during layout into the
// used CSS-px heights container units resolve against. An authored height wins;
// otherwise the span of the container's laid-out content is its used height.
func (l *webLayouter) finishContainerSizes(layout *webLayout) {
	if l.containerSizes == nil {
		return
	}
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	for id, span := range l.containerRows {
		if !span.seen {
			continue
		}
		size := l.containerSizes[id]
		if size.Height > 0 {
			continue
		}
		rows := span.last - span.first
		if rows < 1 {
			rows = 1
		}
		size.Height = float64(rows) * float64(l.rowH) / scale
		l.containerSizes[id] = size
	}
	layout.ContainerSizes = l.containerSizes
}

// recordContainerRows folds one block's vertical extent into every container
// that encloses it.
func (l *webLayouter) recordContainerRows(block webBlock, first, last int32) {
	if len(block.ContainerIDs) == 0 {
		return
	}
	if l.containerRows == nil {
		l.containerRows = make(map[int]webContainerRowSpan)
	}
	for _, id := range block.ContainerIDs {
		span := l.containerRows[id]
		if !span.seen {
			span = webContainerRowSpan{first: first, last: last, seen: true}
		} else {
			if first < span.first {
				span.first = first
			}
			if last > span.last {
				span.last = last
			}
		}
		l.containerRows[id] = span
	}
}

// absorbContainerRows folds a flex item's local row extents into the shared
// sizes after its sub-layout has been measured.
func (l *webLayouter) absorbContainerRows(rows map[int]webContainerRowSpan) {
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	for id, span := range rows {
		if !span.seen {
			continue
		}
		size := l.containerSizes[id]
		if size.Height > 0 {
			continue
		}
		rowCount := span.last - span.first
		if rowCount < 1 {
			rowCount = 1
		}
		size.Height = float64(rowCount) * float64(l.rowH) / scale
		l.containerSizes[id] = size
	}
}

// webLayouter is the geometry shared by the page and every flex item inside it:
// the row grid, the font mapper and the document whose colours the runs use. A
// flex item is laid out by a second webLayouter into its own small webLayout,
// which is then merged into the page at an offset.
type webLayouter struct {
	doc      *webDocument
	layout   *webLayout
	rowH     int32
	baseSize float64
	// constraintSkip is the ancestor box prefix already resolved by the
	// parent flex/grid layout before this item's sublayout was measured.
	constraintSkip int
	fontAt         func(float64) *ttf.Font
	// boxFloor and boxOverride are set while a positioned subtree is laid out
	// as a layer (webpage_layers.go): its container pass paints only the
	// boxes from the root's on, and the root's box is measured from an
	// override that has already spent its width.
	boxFloor    int
	boxOverride map[int]webBoxInfo
	// fontAtStyle resolves the face a run is measured and drawn in: its bold,
	// italic or monospace face (webpage_fonts.go). It is separate from fontAt
	// so metrics-only callers stay cheap.
	fontAtStyle func(webStyle) *ttf.Font

	// containerSizes is shared with every flex-item sub-layout, so a measured
	// column width is visible to the containing block after the merge.
	containerSizes map[int]webContainerSize
	// containerRows accumulates each container's vertical extent in layout
	// rows. A sub-layout keeps its own local map; the caller folds the row count
	// into the shared CSS-px size once the item's height is known.
	containerRows map[int]webContainerRowSpan

	// Measured facts reused within one layout. Wrapping asks for the width of
	// a space once per word and for the same words over and over, and both
	// TTF_SizeUTF8 and the missing-glyph scan cost real time on a 1-core
	// handheld: the caches cut the work without changing the layout.
	spaceWidths map[*ttf.Font]int32
	sanitized   map[webSanitizeKey]string
	// fitWidths caches each fit-content box's width (fitBoxWidth).
	fitWidths map[int]int32
	// boxFlow caches containing-block rectangles. Layer positioning asks for
	// the same ancestor box from several layers; without this each answer
	// rescanned every row and run on the page.
	boxFlow map[int]boxFlowCache
}

type boxFlowCache struct {
	rect sdl.Rect
	ok   bool
}

// webContainerRowSpan is one container's vertical extent in layout-grid rows.
type webContainerRowSpan struct {
	first, last int32
	seen        bool
}

// webSanitizeKey identifies one word for the sanitize cache.
type webSanitizeKey struct {
	font *ttf.Font
	text string
}

// spaceWidth returns the width of one space in font, measured once per layout.
func (l *webLayouter) spaceWidth(font *ttf.Font) int32 {
	if w, ok := l.spaceWidths[font]; ok {
		return w
	}
	w := int32(6)
	if sw, _, err := font.SizeUTF8(" "); err == nil {
		w = int32(sw)
	}
	if l.spaceWidths == nil {
		l.spaceWidths = map[*ttf.Font]int32{}
	}
	l.spaceWidths[font] = w
	return w
}

// sanitizedWord drops characters the font cannot draw, remembering the result
// so the same word is scanned once. Icon-font code points (the Private Use
// Area) go first: no reader face draws those icons, so a word made only of
// them comes back empty and the run loop skips it.
func (l *webLayouter) sanitizedWord(font *ttf.Font, text string) string {
	key := webSanitizeKey{font: font, text: text}
	if out, ok := l.sanitized[key]; ok {
		return out
	}
	// A web font (an icon font among them) draws the code points it maps,
	// its icons included (webpage_webfont.go).
	plain := text
	if !webIsWebFace(font) {
		plain = webDropPrivateUse(text)
	}
	out := sanitizeForFont(font, plain)
	if l.sanitized == nil {
		l.sanitized = make(map[webSanitizeKey]string, 256)
	}
	l.sanitized[key] = out
	return out
}

// webConstraintLength resolves a min/max length in CSS px. Percentages are
// relative to avail, the containing block's measure in device pixels.
func webConstraintLength(px, pct float64, avail int32, scale float64) float64 {
	if scale <= 0 {
		scale = 1
	}
	if pct > 0 {
		return float64(avail) * pct / 100 / scale
	}
	if px > 0 {
		return px
	}
	return 0
}

// webBlockContentWidth resolves the text measure of an ordinary block. Auto
// width fills the space left after its horizontal margins/padding. An explicit
// width (or a max-width) shrinks that measure, and min-width is applied last so
// a contradictory min>max pair resolves the way CSS does: the minimum wins.
func webBlockContentWidth(block webBlock, contentW int32, baseSize float64) int32 {
	if contentW <= 0 {
		return 0
	}
	scale := baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	cssAvail := float64(contentW) / scale
	pad := block.Indent + block.Style.IndentRight
	maxW := webConstraintLength(block.Style.MaxWidth, block.Style.MaxWidthPct, contentW, scale)
	minW := webConstraintLength(block.Style.MinWidth, block.Style.MinWidthPct, contentW, scale)
	borderBox := block.Style.BoxSizing == 1
	explicit := 0.0
	if block.Style.WidthPct > 0 {
		explicit = cssAvail * block.Style.WidthPct / 100
	} else if block.Style.Width > 0 {
		explicit = block.Style.Width
	}
	content := 0.0
	if explicit > 0 || borderBox {
		box := explicit
		if box <= 0 {
			box = cssAvail
		}
		// max-width/min-width apply to the border box in border-box mode, so
		// the padding is removed after the constraint, not before it.
		if maxW > 0 && box > maxW {
			box = maxW
		}
		if minW > 0 && box < minW {
			box = minW
		}
		content = box
		if borderBox {
			content -= pad
		}
	} else {
		// Auto width fills the containing block, then max/min bound the
		// content box for content-box sizing.
		content = cssAvail - pad
		if maxW > 0 && content > maxW {
			content = maxW
		}
		if minW > 0 && content < minW {
			content = minW
		}
	}
	if content < 0 {
		content = 0
	}
	// Keep the box inside the containing block: no negative or overflowing
	// measures when a percentage/min-width asks for more than the page has.
	limit := cssAvail - block.Indent
	if limit < 0 {
		limit = 0
	}
	if content > limit {
		content = limit
	}
	return int32(math.Round(content * scale))
}

// webFloatReserve is the part of the flow a CSS float occupies: the rows from
// startRow up to endRow, and across them x0..x1, the float's margin box in
// layout pixels. The lines beside it are shortened to clear that span (and a
// box that cannot run under it moves beside it), and clear moves a block below
// its end row. seq is the float's place in the document, which tells the
// floats inside an element from those around it (webpage_floats.go).
type webFloatReserve struct {
	side     int
	startRow int32
	endRow   int32
	x0, x1   int32
	seq      int
}

func webClearRow(reserves []webFloatReserve, row int32, clear int) int32 {
	target := row
	for _, r := range reserves {
		if r.endRow <= target {
			continue
		}
		if clear == 3 || clear == r.side {
			target = r.endRow
		}
	}
	return target
}

// webRelativeOffset converts top/right/bottom/left into the paint offset of a
// relatively positioned box. Relative offsets move the box without changing
// the flow, which is exactly what the reader needs for badges and nudged
// labels. CSS resolves left before right and top before bottom, and so does
// this.
func webRelativeOffset(st webStyle, baseSize float64) (int32, int32) {
	if st.Position != webPositionRelative || baseSize <= 0 {
		return 0, 0
	}
	scale := baseSize / 16
	dx, dy := int32(0), int32(0)
	if st.HasLeft {
		dx = int32(math.Round(st.Left * scale))
	} else if st.HasRight {
		dx = -int32(math.Round(st.Right * scale))
	}
	if st.HasTop {
		dy = int32(math.Round(st.Top * scale))
	} else if st.HasBottom {
		dy = -int32(math.Round(st.Bottom * scale))
	}
	return dx, dy
}

// webInFlowShift is the rest of an in-flow box's paint offset, known once it
// is laid out (webRelativeOffset has the lengths): a relative left or right
// in percent of the containing block's width cw, and translate(), whose
// percentages are of the box's own width w and height h. A relative top or
// bottom in percent needs a definite containing height, which a flow block
// does not have, so it stays 0 as in a browser. Absolute and fixed boxes are
// layers and move in layoutLayer.
func webInFlowShift(st webStyle, cw, w, h int32, baseSize float64) (int32, int32) {
	if baseSize <= 0 || st.Position == webPositionAbsolute || st.Position == webPositionFixed {
		return 0, 0
	}
	dx, dy := 0.0, 0.0
	if st.Position == webPositionRelative {
		switch {
		case st.HasLeft && st.InsetPct[3] != 0:
			dx += float64(cw) * st.InsetPct[3] / 100
		case !st.HasLeft && st.HasRight && st.InsetPct[1] != 0:
			dx -= float64(cw) * st.InsetPct[1] / 100
		}
	}
	if st.HasTranslate {
		scale := baseSize / 16
		dx += st.TranslateX*scale + st.TranslatePctX*float64(w)/100
		// `top: 50%; transform: translateY(-50%)` centres a box in a parent
		// whose height the reader cannot resolve here: half of the idiom alone
		// would push the box up out of place, so it stays where the flow put it.
		pctTop := st.Position == webPositionRelative &&
			((st.HasTop && st.InsetPct[0] != 0) || (!st.HasTop && st.HasBottom && st.InsetPct[2] != 0))
		if !pctTop {
			dy += st.TranslateY*scale + st.TranslatePctY*float64(h)/100
		}
	}
	return int32(math.Round(dx)), int32(math.Round(dy))
}

// webMarginRows converts a CSS vertical margin (or a box's padding) to the
// reader's row grid. The cap keeps a stray huge margin from opening a hole of
// half a screen; it used to be three lines, which squeezed the 96-128px
// padding a landing page's sections are drawn with to a third of it, and let
// a padded box paint over what came before it.
func webMarginRows(cssPx, baseSize float64, rowH int32) int32 {
	if cssPx <= 0 || baseSize <= 0 || rowH <= 0 {
		return 0
	}
	design := cssPx * baseSize / 16
	rows := int32(math.Round(design / float64(rowH)))
	if rows < 1 && design >= float64(rowH)*0.45 {
		rows = 1
	}
	if rows > webMaxMarginRows {
		rows = webMaxMarginRows
	}
	return rows
}

// webMaxMarginRows is the most text rows one margin or padding takes: about
// 180 CSS px at the reader's size.
const webMaxMarginRows = 8

// ownBoxOf is the box a block's own element paints: the one whose top inset the
// block's leading margin already carries. It is 0 for a block that only sits
// inside an ancestor's box, whose inset the box itself still has to reserve.
func ownBoxOf(block webBlock) int {
	if block.OwnsBox {
		return block.BoxOwner
	}
	return 0
}

// layoutBlocks lays out a column of blocks into l.layout. Positions are
// relative to the column's own width, with padX as its left edge, so a flex
// item can be laid out at 0 and moved later.
func (l *webLayouter) layoutBlocks(blocks []webBlock, padX, contentW int32) {
	// A paragraph the parser split around inline images is one paragraph
	// again before anything measures it (webpage_inline.go). A floated
	// subtree that starts in this pass is set aside as one block, so its table
	// is measured at the float's width, not this column's (webpage_floats.go).
	blocks = webMergeInlineChains(blocks)
	blocks = l.gatherFloatGroups(blocks)
	blocks = l.prepareWebTables(blocks, contentW)
	layout := l.layout
	rowH := l.rowH
	baseSize := l.baseSize
	fontAt := l.fontAt
	fontAtStyle := l.fontAtStyle
	if fontAtStyle == nil {
		fontAtStyle = func(style webStyle) *ttf.Font { return fontAt(style.FontPx) }
	}
	d := l.doc
	nextY := func() int32 { return int32(len(layout.Rows)) * rowH }
	skipRows := func(n int32) {
		for i := int32(0); i < n && len(layout.Rows) < webMaxRows; i++ {
			layout.Rows = append(layout.Rows, webRow{Y: nextY()})
		}
	}
	emit := func(runs []webRunPos, fontH int32) {
		if len(layout.Rows) >= webMaxRows {
			return
		}
		// A line whose glyphs fit one row takes one. The 4px of air between
		// lines is only added to a face taller than a row: text set a little
		// larger than the body with no line-height of its own (DuckDuckGo's
		// 18px result titles, an h3) used to take two rows a line for them.
		slots := int32(1)
		if need := int32(math.Ceil(float64(fontH+4) / float64(rowH))); need > slots && fontH > rowH {
			slots = need
		}
		idx := int32(len(layout.Rows))
		y := idx * rowH
		if slots > 1 {
			y += (slots*rowH - fontH) / 2
		}
		layout.Rows = append(layout.Rows, webRow{Y: y, Runs: runs})
		for i := int32(1); i < slots && len(layout.Rows) < webMaxRows; i++ {
			layout.Rows = append(layout.Rows, webRow{Y: (idx + i) * rowH})
		}
	}
	marginRows := func(cssPx float64) int32 { return webMarginRows(cssPx, baseSize, rowH) }

	// A wrapper with only child blocks (a hero, a card) has no block of its
	// own to reserve a min-height. Track the boxes opened by the first child
	// and, when the next block leaves one, pad the flow out to that box's
	// declared height. Without this a 300px hero painted its background past
	// the following paragraph instead of pushing that paragraph below it.
	// Only boxes whose owning block is in this pass may reserve their own
	// height here. A flex item's sublayout carries its container's box IDs as
	// ancestors too; reserving the container's 240px height inside a one-line
	// item consumed all the free space the item's margin-top:auto would have
	// taken, pinning it to the top of the column.
	previousMarginBot := 0.0
	ownedBoxes := make(map[int]bool, 8)
	for _, b := range blocks {
		if b.BoxOwner != 0 {
			ownedBoxes[b.BoxOwner] = true
		}
		// A wrapper whose content is a block-level child owns no block of its
		// own: Wikipedia's bookshelf heading is an `h2` (height 1px, 2.4rem
		// margins) around a `display:block` span. Its insets were dropped and
		// the next language list was laid over its last line, with the shelf
		// label pulled up into it. Boxes past the prefix an outer flex/grid
		// pass already resolved belong to this subtree, so their height and
		// margins can be reserved here too.
		for k := min(l.constraintSkip, len(b.Boxes)); k < len(b.Boxes); k++ {
			ownedBoxes[b.Boxes[k]] = true
		}
	}
	var openBoxes []int
	openStart := map[int]int32{}
	closeBox := func(id int) {
		start, ok := openStart[id]
		if !ok || d == nil {
			return
		}
		info, ok := d.Boxes[id]
		if !ok {
			return
		}
		// The wrapper's own bottom margin carries its padding. Its painted
		// surface reaches that far past the last child, so the next block must
		// start below it instead of running over the padded edge. Opening the
		// box already skipped its top inset; the bottom used to be dropped and
		// a heading could land inside a contents box's bottom padding.
		if bottom := info.style.MarginBot; bottom > previousMarginBot {
			previousMarginBot = bottom
		}
		px := max(info.style.Height, info.style.MinHeight)
		if px <= 0 || baseSize <= 0 || rowH <= 0 {
			return
		}
		rows := int32(math.Ceil(px * baseSize / 16 / float64(rowH)))
		if rows > int32(len(layout.Rows))-start {
			skipRows(rows - (int32(len(layout.Rows)) - start))
		}
	}
	// ownBox is the box this block's own element paints (block.BoxOwner when
	// block.OwnsBox). Such a block's leading margin already carries that box's
	// top padding, so opening the box must not skip the same inset a second
	// time. A child block that only inherits an ancestor's box (a heading in a
	// padded section) passes 0: the section's inset is not the heading's.
	syncBoxes := func(boxes []int, ownBox int) {
		local := make([]int, 0, len(boxes))
		for _, id := range boxes {
			// A positioned element's anchor box is only a containing block
			// (webpage_layers.go): it reserves no margin and no height.
			if ownedBoxes[id] && (d == nil || !d.Boxes[id].anchor) {
				local = append(local, id)
			}
		}
		boxes = local
		common := 0
		for common < len(openBoxes) && common < len(boxes) && openBoxes[common] == boxes[common] {
			common++
		}
		for i := len(openBoxes) - 1; i >= common; i-- {
			closeBox(openBoxes[i])
			delete(openStart, openBoxes[i])
		}
		openBoxes = openBoxes[:common]
		for _, id := range boxes[common:] {
			if _, ok := openStart[id]; !ok {
				// A wrapper that has no direct text block (a form, hero or card)
				// still owns its top padding. The first child used to start at the
				// wrapper edge, so the painted surface backed up over the previous
				// table row (or sat tight against a hero heading). A block that is
				// the box's own element skips its inset below, as its leading
				// margin, so skipping here too put two blank rows above an indented
				// or painted first paragraph and made every card a row too tall.
				if info, ok := d.Boxes[id]; ok && id != ownBox {
					if rows := marginRows(info.style.MarginTop); rows > 0 {
						skipRows(rows)
					}
				}
				openStart[id] = int32(len(layout.Rows))
			}
			openBoxes = append(openBoxes, id)
		}
	}

	// Adjacent vertical margins collapse to the larger of the two, the way CSS
	// does it. Summing them stacked a heading's own spacing on top of the
	// previous block's and filled real pages with holes on a small screen.
	// previousMarginBot is declared above syncBoxes: closing a box raises it
	// to that box's bottom inset so the gap below the wrapper covers it.
	flowPadX, flowContentW := padX, contentW
	var floatReserves []webFloatReserve
	// besideFloats places a box that cannot run under a float — a flex row, a
	// grid, a flex column, a table — beside the floats at the current row: x
	// and w are its own place, padX/contentW the column it is in. A table
	// keeps its width (table is its ID, 0 for anything else) and moves clear
	// of them; the others narrow to the room left. Without room it goes below
	// them. Every row of one table moves as its first did (tableShift), so its
	// columns stay in line (webpage_floats.go).
	var tableShift map[int]int32
	besideFloats := func(x, w int32, table int) (int32, int32) {
		if len(floatReserves) == 0 {
			return x, w
		}
		if table > 0 {
			if shift, ok := tableShift[table]; ok {
				return x + shift, w
			}
		}
		row := int32(len(layout.Rows))
		nx, nw, ok := webBesideFloats(floatReserves, row, padX, contentW, x, w, table > 0, baseSize)
		if !ok {
			if end := webFloatsEndAt(floatReserves, row); end > row {
				skipRows(end - row)
			}
			nx, nw = x, w
		}
		if table > 0 {
			if tableShift == nil {
				tableShift = make(map[int]int32, 2)
			}
			tableShift[table] = nx - x
		}
		return nx, nw
	}
	for i := 0; i < len(blocks); i++ {
		block := blocks[i]
		if len(layout.Rows) >= webMaxRows {
			break
		}
		if block.LayerMarker != 0 {
			// Where a positioned subtree would have started in the flow is its
			// static position (webpage_layers.go). It takes no room and does
			// not take part in margin collapsing.
			syncBoxes(block.Boxes, ownBoxOf(block))
			markX, _ := l.applyBoxConstraints(block, flowPadX, flowContentW)
			if layout.statics == nil {
				layout.statics = make(map[int]webStaticPos, 4)
			}
			layout.statics[block.LayerMarker] = webStaticPos{X: markX, Y: int32(len(layout.Rows)) * rowH}
			continue
		}
		if block.ClearMarker {
			// A clearfix, or the end of an element that holds its floats: what
			// follows starts below them (webpage_floats.go). It takes no room
			// of its own.
			syncBoxes(block.Boxes, 0)
			row := int32(len(layout.Rows))
			if target := webClearMarkerRow(floatReserves, row, block.Style.Clear, block.ClearFrom); target > row {
				skipRows(target - row)
				previousMarginBot = 0
			}
			continue
		}
		if block.Style.FloatSide == 0 && !block.Float {
			// A float/absolute box owns its geometry in its own sublayout;
			// opening it here made the next block close it and pad the main
			// flow by its min-height, pushing siblings below a float that had
			// never displaced them.
			syncBoxes(block.Boxes, ownBoxOf(block))
		}
		// A float only narrows the rows it overlaps. Start each block with the
		// full column and subtract the reserves that are active at its top row.
		padX, contentW = flowPadX, flowContentW
		padX, contentW = l.applyBoxConstraints(block, padX, contentW)
		blockStart := int32(len(layout.Rows))
		rectStart, imageStart := len(layout.Rects), len(layout.Images)
		bgStart := len(layout.Backgrounds)
		videoStart, floatStart := len(layout.Videos), len(layout.Floats)
		dx, dy := webRelativeOffset(block.Style, baseSize)
		shiftRelative := func() {
			if dx == 0 && dy == 0 {
				return
			}
			for i := int(blockStart); i < len(layout.Rows); i++ {
				layout.Rows[i].Y += dy
				for j := range layout.Rows[i].Runs {
					layout.Rows[i].Runs[j].X += dx
				}
			}
			for i := rectStart; i < len(layout.Rects); i++ {
				layout.Rects[i].Rect.X += dx
				layout.Rects[i].Rect.Y += dy
			}
			// The fill is painted from Backgrounds: it moves with its text
			// and borders (it used to stay behind).
			for i := bgStart; i < len(layout.Backgrounds); i++ {
				layout.Backgrounds[i].Rect.X += dx
				layout.Backgrounds[i].Rect.Y += dy
			}
			for i := imageStart; i < len(layout.Images); i++ {
				layout.Images[i].Rect.X += dx
				layout.Images[i].Rect.Y += dy
			}
			for i := videoStart; i < len(layout.Videos); i++ {
				layout.Videos[i].Rect.X += dx
				layout.Videos[i].Rect.Y += dy
			}
			for i := floatStart; i < len(layout.Floats); i++ {
				layout.Floats[i].X += dx
				layout.Floats[i].Y += dy
			}
		}
		if block.TableEmptyRow && block.FlexGroup == 0 {
			// A row made only of rowspan holes never produced a cell block. Its
			// marker still reserves the row so the table does not collapse it.
			// A marker still inside a flex item or an outer table's cell goes
			// with that item's blocks to the pass that lays its table out.
			skipRows(1)
			previousMarginBot = 0
			shiftRelative()
			continue
		}
		if block.Spacer && block.FlexGroup == 0 {
			// A spacer paragraph (webParser.keepSpacer) is its height and
			// margins, rounded like margins; its own box pads the rest. Inside
			// a flex or grid item it waits for the item's own pass.
			if len(layout.Rows) > 0 {
				skipRows(marginRows(math.Max(block.MarginTop, previousMarginBot)))
			}
			skipRows(marginRows(webHeightContentTarget(block.Style, block.Style.Height)))
			previousMarginBot = block.MarginBot
			continue
		}
		// A run of blocks tagged with one flex group is that container's
		// items. In a row they become side-by-side columns; in a column they
		// are ordinary blocks separated by the container's gap.
		if block.FlexGroup != 0 && block.FlexDir == webFlexRow {
			j := i
			for j < len(blocks) && blocks[j].FlexGroup == block.FlexGroup && blocks[j].FlexDir == webFlexRow {
				j++
			}
			gap := block.MarginTop
			if previousMarginBot > gap {
				gap = previousMarginBot
			}
			if len(layout.Rows) > 0 {
				skipRows(marginRows(gap))
			}
			// applyBoxConstraints already skipped the item's own box (its width
			// and padding are applied inside the item sublayout). What remains is
			// the container's padding/width: a section's 12px inset must shift the
			// cards, and its max-width must bound the tracks.
			groupPadX, groupW := padX, contentW
			rowW := groupW
			if block.Style.TableRole == webTableRoleCell {
				if block.Style.TableWidthPct > 0 {
					rowW = int32(float64(groupW) * block.Style.TableWidthPct / 100)
				} else if block.Style.TableWidthPx > 0 {
					rowW = int32(block.Style.TableWidthPx * baseSize / 16)
				}
				if rowW > groupW {
					rowW = groupW
				}
				if rowW < 32 {
					rowW = 32
				}
				// <center><table width="85%">, or a table with auto margins:
				// its rows sit in the middle of the column.
				if block.Style.TableCenter && rowW < groupW {
					groupPadX += (groupW - rowW) / 2
				}
				// Beside a float a table keeps its width and moves clear of it,
				// or goes below it; every row of the table the same way.
				groupPadX, rowW = besideFloats(groupPadX, rowW, block.Style.TableID)
			} else {
				// A flex row or a grid sits beside the floats around it, as
				// a block formatting context does (webpage_floats.go).
				groupPadX, rowW = besideFloats(groupPadX, rowW, 0)
			}
			l.layoutFlexRow(blocks[i:j], groupPadX, rowW)
			l.recordContainerRows(block, blockStart, int32(len(layout.Rows)))
			previousMarginBot = 0
			// No group-wide relative shift: this block is the first item's,
			// whose own offset its sub-layout already applied — shifting the
			// whole row by it moved every other item too.
			i = j - 1
			continue
		}
		if block.FlexGroup != 0 && block.FlexDir == webFlexColumn {
			j := i
			for j < len(blocks) && blocks[j].FlexGroup == block.FlexGroup && blocks[j].FlexDir == webFlexColumn {
				j++
			}
			grouped := webGroupFlexItems(blocks[i:j])
			if block.FlexReverse {
				for a, z := 0, len(grouped)-1; a < z; a, z = a+1, z-1 {
					grouped[a], grouped[z] = grouped[z], grouped[a]
				}
			}
			if len(grouped) > 0 {
				lead := grouped[0].blocks[0].MarginTop
				if previousMarginBot > lead {
					lead = previousMarginBot
				}
				if len(layout.Rows) > 0 {
					skipRows(marginRows(lead))
				}
				// A flex column sits beside the floats around it too.
				groupPadX, groupW := besideFloats(padX, contentW, 0)
				// A definite height has room to hand out: to the items' auto
				// margins, or by justify-content (layoutFlexColumnAuto).
				distributes := flexAutoCount(grouped) > 0 || block.FlexReverse || block.FlexJustify != webJustifyStart
				if heightRows := l.flexColumnHeightRows(block.FlexHeight); heightRows > 0 && distributes {
					l.layoutFlexColumnAuto(grouped, block, groupPadX, groupW, heightRows)
				} else {
					for k, item := range grouped {
						if k > 0 {
							skipRows(marginRows(block.FlexGapY))
						}
						itemBlocks := item.blocks
						if k == 0 {
							// The group's leading margin was already collapsed with
							// the preceding block, so the first child must not add it.
							itemBlocks = append([]webBlock(nil), item.blocks...)
							itemBlocks[0].MarginTop = 0
						}
						// Across the column: auto margins, or align-items center
						// and end at the item's own width (flexColumnCross).
						itemX, itemW := l.flexColumnCross(itemBlocks, groupPadX, groupW)
						l.layoutBlocksFlat(itemBlocks, itemX, itemW)
					}
				}
			}
			l.recordContainerRows(block, blockStart, int32(len(layout.Rows)))
			previousMarginBot = blocks[j-1].MarginBot
			i = j - 1
			continue
		}
		if len(block.FloatBlocks) > 0 {
			// A floated subtree (webpage_floats.go) goes where the next block
			// starts, at its side of the column, and the flow wraps round it.
			// Its margins do not collapse with the flow's, which carries on as
			// if it were not there.
			gap := previousMarginBot
			if i+1 < len(blocks) && webBlockInFlow(&blocks[i+1]) {
				gap = math.Max(gap, blocks[i+1].MarginTop)
			}
			row := int32(len(layout.Rows))
			if row > 0 {
				row += marginRows(gap)
			}
			if r, ok := l.layoutFloatGroup(block, padX, contentW, row, floatReserves); ok {
				floatReserves = append(floatReserves, r)
			}
			continue
		}
		gap := block.MarginTop
		if previousMarginBot > gap {
			gap = previousMarginBot
		}
		// The page's first block takes no leading margin row: a margin collapses
		// out of the page top. The padding it paints inside its own box stays,
		// though, or a card that opens the page lost its inset and its content
		// sat against (or over) its own top border. MarginTop holds both, so the
		// padding is taken on its own there.
		if len(layout.Rows) == 0 {
			gap = math.Max(0, block.Style.PadTop)
		}
		skipRows(marginRows(gap))
		previousMarginBot = block.MarginBot
		// `clear` moves the block below the floats it names instead of letting
		// it slide into the narrowed band beside them.
		if block.Style.Clear != 0 && len(floatReserves) > 0 {
			row := int32(len(layout.Rows))
			if target := webClearRow(floatReserves, row, block.Style.Clear); target > row {
				skipRows(target - row)
			}
		}
		row := int32(len(layout.Rows))
		if block.Style.FloatSide != 0 {
			// A floated block of its own (a floated picture) goes beside the
			// floats already there, in its containing block (webpage_floats.go).
			if r, ok := l.layoutCSSFloat(block, padX, contentW, row, floatReserves); ok {
				floatReserves = append(floatReserves, r)
			}
			previousMarginBot = 0
			shiftRelative()
			continue
		}
		// Plain text wraps round the floats line by line (wrapFloats, in the
		// line builder below): a line beside a float is shorter, a line below
		// it has the whole measure again. A box with a surface of its own, a
		// picture, a rule, preformatted text and a block that holds its floats
		// sit beside them whole, or below them when there is no room.
		wrapFloats := len(floatReserves) > 0 && webBlockWrapsFloats(&block)
		if len(floatReserves) > 0 && !wrapFloats {
			if nx, nw, ok := webBesideFloats(floatReserves, row, padX, contentW, padX, contentW, false, baseSize); ok {
				padX, contentW = nx, nw
			} else if end := webFloatsEndAt(floatReserves, row); end > row {
				skipRows(end - row)
			}
		}
		if block.Rule {
			y := nextY() + rowH/2
			color := sdl.Color{R: 200, G: 206, B: 216, A: 255}
			switch {
			case block.HasBorderColor:
				color = block.BorderColor
			case block.HasBg:
				// `hr { background: #eee }` is the other common spelling of a
				// rule, and browsers paint it the same way.
				color = block.Background
			}
			barW := int32(math.Round(block.BorderWidth))
			if barW <= 0 {
				barW = 2
			}
			if barW > 12 {
				barW = 12
			}
			indent := int32(block.Indent * baseSize / 16)
			if indent < 0 || indent > contentW/2 {
				indent = 0
			}
			ruleW := webBlockContentWidth(block, contentW, baseSize)
			if ruleW < 0 {
				ruleW = 0
			}
			ruleX := padX + indent
			// A rule narrower than its column (hr width="50%") is centred by
			// its auto margins, as in a browser.
			if block.Align == 1 {
				if free := contentW - indent - ruleW; free > 0 {
					ruleX += free / 2
				}
			}
			if !block.Style.Invisible {
				layout.Rects = append(layout.Rects, webRect{
					Rect:  sdl.Rect{X: ruleX, Y: y, W: ruleW, H: barW},
					Color: webFadeColor(color, d.Paper, block.Style),
				})
			}
			skipRows(1)
			l.recordContainerRows(block, blockStart, int32(len(layout.Rows)))
			shiftRelative()
			continue
		}
		// A replaced element's frame is content inside the box around it: it
		// paints over that box's surface in tree order (webpage_layers.go).
		if block.Placeholder {
			l.layoutPlaceholder(block, padX, contentW)
			l.recordContainerRows(block, blockStart, int32(len(layout.Rows)))
			shiftRelative()
			l.tagContentBox(block, rectStart, bgStart)
			continue
		}
		if block.Image {
			l.layoutImage(block, padX, contentW)
			l.recordContainerRows(block, blockStart, int32(len(layout.Rows)))
			shiftRelative()
			l.tagContentBox(block, rectStart, bgStart)
			continue
		}
		if block.Video {
			l.layoutVideo(block, padX, contentW)
			l.recordContainerRows(block, blockStart, int32(len(layout.Rows)))
			shiftRelative()
			l.tagContentBox(block, rectStart, bgStart)
			continue
		}
		startGrid := int32(len(layout.Rows))
		// The flow already skipped the block's top inset before startGrid.
		// MarginTop/MarginBot contain both margins and padding, but only the
		// padding belongs inside the painted surface. Growing the box upward
		// and downward by the padding keeps a card's content inset while the
		// transparent margin stays outside its background and border.
		padTop := int32(math.Round(block.Style.PadTop * baseSize / 16))
		padBot := int32(math.Round(block.Style.PadBot * baseSize / 16))
		if padTop < 0 {
			padTop = 0
		}
		if padBot < 0 {
			padBot = 0
		}
		paintTop := startGrid*rowH - padTop

		indent := int32(block.Indent * baseSize / 16)
		if indent < 0 {
			indent = 0
		}
		if indent > contentW/2 {
			indent = contentW / 2
		}
		// padding-right/margin-right narrow the measure from the other side,
		// which is what an inset card or a page gutter asks for.
		indentRight := int32(block.Style.IndentRight * baseSize / 16)
		if indentRight < 0 {
			indentRight = 0
		}
		if indentRight > contentW/3 {
			indentRight = contentW / 3
		}
		// width: fit-content, max-content or min-content: the box is as wide as
		// what it holds — the whole box when the block's element owns one with
		// more inside it (fitBoxWidth), else the block's own text.
		if block.Style.WidthFit != 0 && block.Style.Width <= 0 && block.Style.WidthPct <= 0 &&
			!block.Float && block.Style.TableRole == webTableRoleNone {
			fit := l.fitBoxWidth(block.BoxOwner)
			if fit <= 0 {
				fit = l.fitBlockWidth(&block, block.Style.WidthFit)
			}
			pad := (block.Indent + block.Style.IndentRight) * baseSize / 16
			if css := (float64(fit) - pad) * 16 / baseSize; fit > 0 && css > 0 {
				block.Style.Width, block.Style.BoxSizing = css, 0
			}
		}
		// Resolve width/min-width/max-width (and box-sizing) for this block.
		// blockW is the measure from the text's left edge; paintW is the full
		// box including the right gutter, so an unconstrained block keeps the
		// page-wide box it always had.
		blockW := webBlockContentWidth(block, contentW, baseSize)
		paintW := indent + blockW + indentRight
		if paintW > contentW {
			paintW = contentW
		}
		if paintW < 0 {
			paintW = 0
		}
		// Auto inline margins take the free space around a constrained box.
		// `max-width: 560px; margin: 0 auto` is the common page column, and
		// `width: 60%; margin-inline: auto` is the common centred card; both
		// used to sit against the left edge because auto resolved to zero.
		if (block.Style.MarginLeftAuto || block.Style.MarginRightAuto || block.Style.CenterBlocks) &&
			!block.Float && block.Style.FloatSide == 0 && block.Style.TableRole == webTableRoleNone &&
			block.FlexGroup == 0 {
			if free := contentW - paintW; free > 0 {
				switch {
				case block.Style.MarginLeftAuto && block.Style.MarginRightAuto:
					padX += free / 2
				case block.Style.MarginLeftAuto:
					padX += free
				case block.Style.CenterBlocks && !block.Style.MarginRightAuto:
					// Inside <center> or align="center" (text-align:
					// -webkit-center) a narrower box is centred too.
					padX += free / 2
				}
			}
		}
		// One radius per box, in design pixels; the draw clamps it to half the
		// box so `border-radius: 999px` is a pill rather than an error.
		radius := int32(block.Style.Radius * baseSize / 16)
		if radius < 0 {
			radius = 0
		}
		// text-indent pushes only the first line, like a print paragraph.
		firstLineIndent := int32(block.TextIndent * baseSize / 16)
		if firstLineIndent < 0 {
			firstLineIndent = 0
		}
		if firstLineIndent > contentW/2 {
			firstLineIndent = contentW / 2
		}
		bullet := ""
		bulletW := int32(0)
		markerStyle := block.Style
		if block.HasMarkerStyle {
			markerStyle = block.MarkerStyle
		}
		markerFont := fontAtStyle(markerStyle)
		if block.Bullet != "" {
			bullet = block.Bullet
			if markerFont != nil {
				if w, _, err := markerFont.SizeUTF8(bullet + " "); err == nil {
					bulletW = int32(w)
				}
			}
		}
		textIndent := indent + bulletW
		avail := blockW - bulletW
		// An outside marker hangs in the list's padding, left of the text,
		// where there is room for it; the text keeps the item's own edge.
		hang := block.MarkerHang && bulletW > 0 && padX+indent >= bulletW
		if hang {
			textIndent, avail = indent, blockW
		}
		if avail < 48 {
			textIndent = indent
			avail = blockW
		}
		if avail < 0 {
			avail = 0
		}

		var line []webRunPos
		var lineMax, lineFontH int32
		lineClipped := false
		firstLine := true
		lineStart := padX + textIndent
		lineRight := lineStart + avail
		// Beside a float only the lines next to it are shorter: each line takes
		// the room the floats leave at its row (fitLine), and a line below them
		// has the block's whole measure again (webpage_floats.go). lineShift is
		// how far a float pushed the line's start — the list marker moves with
		// it — and waitRows how many rows a line with no room beside the floats
		// goes down, taken when its first word or picture is set (takeWait), so
		// a block never ends in empty rows.
		baseStart, baseAvail := lineStart, avail
		lineShift, waitRows := int32(0), int32(0)
		fitLine := func() {
			lineStart, avail, lineShift, waitRows = baseStart, baseAvail, 0, 0
			if wrapFloats {
				lineStart, avail, lineShift, waitRows = webFitLine(floatReserves, int32(len(layout.Rows)), padX+indent, baseStart, baseAvail, baseSize)
			}
			lineRight = lineStart + avail
		}
		fitLine()
		x := lineStart + firstLineIndent
		// line-clamp: once clampLines lines are set, clampRow is the last one
		// kept. Every later line is dropped, and the first one dropped puts
		// "…" at the end of clampRow, since content was cut.
		clampLines, linesSet := block.Style.LineClamp, 0
		clampRow, clampCut := -1, false
		// takeWait moves a line down the rows fitLine said it waits below a
		// float, once its first word or picture is set; the lines a line clamp
		// drops wait for nothing.
		takeWait := func() {
			if waitRows > 0 && len(line) == 0 && clampRow < 0 {
				skipRows(waitRows)
				waitRows = 0
			}
		}
		// lineSeq counts the lines set, so a run can tell whether the line its
		// last word went on is still open; edgeClosed is set when the word
		// before closed an inline-block, whose edge is a break opportunity
		// (webpage_inlinebox.go); atomicLeft is how many runs of an
		// inline-block kept whole are still to come.
		lineSeq, edgeClosed, atomicLeft := 0, false, 0
		blockWhiteSpace := block.WhiteSpace
		if blockWhiteSpace == webWhiteSpaceNormal {
			blockWhiteSpace = block.Style.WhiteSpace
		}
		blockWraps := webWhiteSpaceWraps(blockWhiteSpace) && !block.Pre

		flushLine := func(last bool) {
			if len(line) == 0 {
				return
			}
			lineSeq++
			if clampRow >= 0 {
				if !clampCut && clampRow < len(layout.Rows) {
					webClampEllipsis(layout.Rows[clampRow].Runs, lineRight)
				}
				clampCut = true
				line = nil
				lineMax, lineFontH = 0, 0
				lineClipped = false
				x = lineStart
				return
			}
			// The words start at the line's start, after text-indent on the
			// first line only: every later line used to lose the indent from
			// its width as well, so centred and justified lines of an indented
			// paragraph ran past its right edge.
			lineX := lineStart
			if firstLine {
				lineX += firstLineIndent
			}
			lineWidth := lineMax - lineX
			if lineWidth < 0 {
				lineWidth = 0
			}
			if firstLine && bullet != "" {
				if markerFont != nil {
					markerX := padX + indent + lineShift
					if hang {
						markerX -= bulletW
					}
					line = append([]webRunPos{{
						Text:  bullet,
						X:     markerX,
						Font:  markerFont,
						Color: webResolveColor(markerStyle, d),
						Bold:  markerStyle.Bold,
					}}, line...)
				}
			}
			lineAvail := avail
			if firstLine {
				lineAvail = avail - firstLineIndent
				if lineAvail < 0 {
					lineAvail = 0
				}
			}
			// Justification stretches the automatic gaps between words on
			// every line except the last. A forced break is a last line too,
			// which matches how browsers leave a <br> line unstretched.
			if block.Align == 3 && !last && lineAvail > lineWidth {
				gaps := 0
				for i := 1; i < len(line); i++ {
					if line[i].SpaceBefore > 0 {
						gaps++
					}
				}
				if gaps > 0 {
					slack := lineAvail - lineWidth
					extra := slack / int32(gaps)
					remainder := slack % int32(gaps)
					var shift int32
					for i := 1; i < len(line); i++ {
						if line[i].SpaceBefore > 0 {
							shift += extra
							if remainder > 0 {
								shift++
								remainder--
							}
						}
						// Every glyph after the gap moves too; letter-spaced words
						// are stored as one position per glyph.
						line[i].X += shift
					}
					lineWidth = lineAvail
				}
			}
			offset := int32(0)
			switch block.Align {
			case 1:
				offset = (lineAvail - lineWidth) / 2
			case 2:
				offset = lineAvail - lineWidth
			}
			if offset < 0 {
				offset = 0
			}
			if offset > 0 {
				for i := range line {
					line[i].X += offset
				}
			}
			// Runs of different sizes and faces share one baseline, and an
			// inline image stands on it (webpage_inline.go); the line grows
			// to hold both. A line in one face is unchanged.
			metrics := webAlignLineRuns(line, rowH)
			if h := metrics.ascent + metrics.descent; h > lineFontH {
				lineFontH = h
			}
			// The author's line-height, when it is larger than the glyphs,
			// becomes the slot height: the row grid centres the text in the
			// slot, which is exactly what extra leading looks like. Capped so
			// `line-height: 400px` cannot spend the screen on one paragraph.
			slotH := lineFontH
			if lh := block.Style.LineHeight * baseSize / 16; lh > float64(slotH) {
				if limit := float64(rowH * 3); lh > limit {
					lh = limit
				}
				// Rows are discrete. A line-height only slightly larger
				// than one row should keep one row when the glyphs fit;
				// rounding it to two rows doubles ordinary article spacing.
				if lh <= float64(rowH)*1.25 && lineFontH+4 <= rowH {
					slotH = rowH - 4
				} else {
					slotH = int32(lh)
				}
			}
			rowAt := len(layout.Rows)
			emit(line, slotH)
			if rowAt < len(layout.Rows) {
				l.placeInlineImages(line, layout.Rows[rowAt].Y, metrics)
			}
			if linesSet++; clampLines > 0 && linesSet >= clampLines {
				clampRow = rowAt
			}
			firstLine = false
			line = nil
			lineMax, lineFontH = 0, 0
			lineClipped = false
			fitLine()
			x = lineStart
		}

		for runIdx, run := range block.Runs {
			if clampCut {
				// line-clamp has already cut the block; nothing after it is drawn.
				break
			}
			if atomicLeft > 0 {
				atomicLeft--
			}
			// An inline image is one unbreakable box on the line, like a long
			// word: it wraps when it does not fit and is placed once the line
			// is set (placeInlineImages).
			if run.Image != nil {
				if lineClipped {
					continue
				}
				pos, ok := l.inlineImagePos(run, block, avail, len(line) > 0)
				if !ok {
					continue
				}
				if len(line) > 0 && x+pos.SpaceBefore+pos.ImageW > lineRight && webWhiteSpaceWraps(block.Style.WhiteSpace) {
					flushLine(false)
					pos.SpaceBefore = 0
				}
				x += pos.SpaceBefore
				pos.X = x
				pos.BoxWidth = contentW
				pos.Boxes = append([]int(nil), block.Boxes...)
				takeWait()
				line = append(line, pos)
				x += pos.ImageW
				if x > lineMax {
					lineMax = x
				}
				continue
			}
			// A form control paints its current state, not the text it was
			// parsed with: a filled field, a stepped select and a ticked box all
			// read back from the control, so changing one is a re-layout rather
			// than a second copy of the run to keep in sync.
			runText := run.Text
			if run.Style.Control != nil {
				runText = run.Style.Control.Display()
			}
			// The edges of the inline boxes the run opens and closes take room
			// before its first word and after its last, and a run inside a
			// padded badge, button or <code> paints that box's chip
			// (webpage_inlinebox.go).
			leadPx := int32(math.Round(run.Lead * baseSize / 16))
			trailPx := int32(math.Round(run.Trail * baseSize / 16))
			leadChip := int32(math.Round(run.ChipLead * baseSize / 16))
			trailChip := int32(math.Round(run.ChipTrail * baseSize / 16))
			var chip *webInlineChip
			if run.Style.InlineChip {
				c := webRunChip(&run.Style, baseSize, d)
				if run.Style.Control != nil && run.Style.Height > 0 {
					if f := fontAtStyle(run.Style); f != nil {
						webChipHeight(&c, &run.Style, int32(f.Height()), baseSize, rowH)
					}
				}
				chip = &c
			}
			// An inline-block (a button, a badge) is atomic: the line may break
			// at its edges without a space, and the box goes to the next line
			// whole when it is short enough to (CSS Text 3 §5.1). A run inside
			// one already kept whole does not start another.
			atomicStart := run.Style.DisplayInlineBlock && (run.OpensChip || leadPx > 0) && atomicLeft == 0
			atomicW := int32(0)
			if atomicStart {
				if w, n := l.inlineBlockSpan(block.Runs[runIdx:]); n > 0 && w <= avail {
					atomicW, atomicLeft = w, n
				}
			}
			leadDone, trailDone := false, false
			lastPos, lastSeq := -1, -1
			runWhiteSpace := block.WhiteSpace
			if runWhiteSpace == webWhiteSpaceNormal {
				runWhiteSpace = block.Style.WhiteSpace
			}
			if run.Style.WhiteSpace != webWhiteSpaceNormal {
				runWhiteSpace = run.Style.WhiteSpace
			}
			if run.Style.Pre && runWhiteSpace == webWhiteSpaceNormal {
				runWhiteSpace = webWhiteSpacePre
			}
			segments := strings.Split(runText, "\n")
			lastSeg := len(segments) - 1
			for lastSeg > 0 && strings.TrimSpace(segments[lastSeg]) == "" {
				lastSeg--
			}
			for si, segment := range segments {
				if si > 0 {
					if len(line) == 0 {
						// An explicit <br> on an empty line is a blank row; a
						// clamped block counts it as one of its lines.
						if clampRow < 0 {
							takeWait()
							skipRows(1)
							if linesSet++; clampLines > 0 && linesSet >= clampLines {
								clampRow = len(layout.Rows) - 1
							}
							// The next line has the room the floats leave at its row.
							fitLine()
							x = lineStart
							if firstLine {
								x += firstLineIndent
							}
						}
					} else {
						flushLine(true)
					}
				}
				if segment == "" {
					continue
				}
				whiteSpace := runWhiteSpace
				preserveSpaces := webWhiteSpacePreservesSpaces(whiteSpace)
				wrap := webWhiteSpaceWraps(whiteSpace)
				var words []string
				switch {
				case whiteSpace == webWhiteSpacePre:
					// A pre line is one measured run: its spaces and tabs are
					// content, and the line clips instead of re-flowing.
					words = []string{strings.TrimRight(segment, " ")}
				case whiteSpace == webWhiteSpacePreWrap || whiteSpace == webWhiteSpaceBreakSpaces:
					// Preserve authored spaces and attach each space run to the
					// word that follows, so wrapped lines keep their indentation.
					words = webPreWrapTokens(segment)
				default:
					words = strings.Fields(segment)
				}
				if run.Style.Control != nil && len(words) > 0 {
					// A control is one box. Splitting its label at the spaces would
					// draw a field's frame as two chips and let the cursor stop on
					// each half, so the whole label is one word, ellipsized when the
					// page is too narrow to hold it.
					words = []string{strings.Join(words, " ")}
				}
				// A run only gets a space before it when the source had one.
				// Inserting one at every style boundary pushed punctuation
				// away from the word it belongs to ("price , bold ,").
				leadingSpace := !preserveSpaces && (strings.HasPrefix(segment, " ") || strings.HasPrefix(segment, "\t"))
				continuation := make([]bool, len(words))
				transformedPart := make([]bool, len(words))
				breakParts := 0
				for wi := 0; wi < len(words); wi++ {
					if clampCut {
						break
					}
					word := words[wi]
					if word == "" {
						continue
					}
					if lineClipped {
						continue
					}
					if continuation[wi] && len(line) > 0 {
						flushLine(false)
					}
					font := fontAtStyle(run.Style)
					if font == nil {
						continue
					}
					if !transformedPart[wi] {
						word = webTransformWord(word, run.Style.Transform)
					}
					if run.Style.SmallCaps {
						// Small caps: a word with lower-case letters is set in
						// capitals a size smaller.
						if upper := strings.ToUpper(word); upper != word {
							small := run.Style
							small.FontPx *= 0.8
							if f := fontAtStyle(small); f != nil {
								font = f
							}
							word = upper
						}
					}
					if run.Style.Serif && !run.Style.Mono && !webFontCoversText(font, word) {
						// A word the serif cannot set (Greek in a Lora page)
						// keeps the UI face instead of turning into substitutes.
						plain := run.Style
						plain.Serif = false
						if f := fontAtStyle(plain); f != nil {
							font = f
						}
					}
					if run.Style.WebFont != 0 && webIsWebFace(font) && !webFontCoversText(font, word) {
						// A web font that lacks a letter of the word (a Latin-only
						// subset beside a Greek word) leaves the word to the reader's
						// own face.
						plain := run.Style
						plain.WebFont = 0
						if f := fontAtStyle(plain); f != nil {
							font = f
						}
					}
					// A word in a script the face cannot draw (日本語, a Persian word) is set
					// in a face that can, a right-to-left word shaped and turned
					// for SDL_ttf's left-to-right drawing (webpage_script.go).
					font, word = webScriptWord(font, word)
					if strings.Contains(word, webSoftHyphen) {
						// &shy; is a break point, not a glyph (webpage_hints.go):
						// a word too long for the rest of the line breaks at the
						// last one that fits, with a hyphen; elsewhere it vanishes.
						if wrap && !preserveSpaces && run.Style.Control == nil && breakParts < 256 {
							gap := int32(0)
							if len(line) > 0 && (wi > 0 || leadingSpace) {
								gap = max(l.spaceWidth(font)+int32(math.Round(run.Style.WordSpacing*baseSize/16)), 0)
							}
							measureWord := func(s string) int32 {
								w, _, err := font.SizeUTF8(l.sanitizedWord(font, s))
								if err != nil {
									return 0
								}
								return int32(w)
							}
							room := lineRight - x - gap
							if whole := measureWord(strings.ReplaceAll(word, webSoftHyphen, "")); whole > room {
								prefix, rest, ok := webSoftHyphenSplit(word, room, measureWord)
								if !ok && len(line) > 0 && whole > lineRight-lineStart {
									// Nothing fits after the words already set and the
									// word is longer than a whole line: it starts the next
									// line and breaks there.
									flushLine(false)
									prefix, rest, ok = webSoftHyphenSplit(word, lineRight-x, measureWord)
								}
								if ok {
									words = append(words, "")
									copy(words[wi+2:], words[wi+1:])
									words[wi+1] = rest
									continuation = append(continuation, false)
									copy(continuation[wi+2:], continuation[wi+1:])
									continuation[wi+1] = true
									transformedPart = append(transformedPart, false)
									copy(transformedPart[wi+2:], transformedPart[wi+1:])
									transformedPart[wi+1] = true
									word = prefix
									breakParts++
								}
							}
						}
						word = strings.ReplaceAll(word, webSoftHyphen, "")
						if word == "" {
							continue
						}
					}
					text := l.sanitizedWord(font, word)
					if text == "" {
						continue
					}
					_, height, err := font.SizeUTF8(text)
					if err != nil {
						continue
					}
					letterSpacing := int32(math.Round(run.Style.LetterSpacing * baseSize / 16))
					if run.Style.Control != nil {
						letterSpacing = 0 // a field's label stays one painted box
					}
					measure := func(s string) int32 {
						w, _, err := font.SizeUTF8(s)
						if err != nil {
							return 0
						}
						total := int32(w)
						if n := utf8.RuneCountInString(s); n > 1 {
							total += letterSpacing * int32(n-1)
						}
						if total < 0 {
							return 0
						}
						return total
					}
					wordW := measure(text)
					// CSS overflow-wrap and word-break preserve a long token by
					// breaking it at glyph boundaries. The old fallback replaced
					// every such URL or identifier with an ellipsis. Bound the
					// expansion so hostile megabyte tokens cannot allocate one row
					// per character or turn insertion into quadratic work.
					if wrap && !preserveSpaces && run.Style.Control == nil &&
						(run.Style.OverflowWrap != 0 || run.Style.WordBreak != 0) &&
						len(text) <= 8192 && breakParts < 256 {
						breakWidth := avail
						if len(line) > 0 && (wordW > avail || run.Style.WordBreak != 0) {
							gap := int32(0)
							if wi > 0 || leadingSpace {
								gap = l.spaceWidth(font) + int32(math.Round(run.Style.WordSpacing*baseSize/16)) + letterSpacing
								if gap < 0 {
									gap = 0
								}
							}
							if remaining := lineRight - x - gap; remaining > 0 && wordW > remaining {
								breakWidth = remaining
							}
						}
						prefix, suffix := text, ""
						if wordW > breakWidth && (wordW > avail || run.Style.WordBreak != 0) {
							prefix, suffix = webWordPrefixThatFits(text, breakWidth, measure)
							if len(line) > 0 && (prefix == "" || measure(prefix) > breakWidth) {
								flushLine(false)
								prefix, suffix = webWordPrefixThatFits(text, avail, measure)
							}
						}
						if suffix != "" && prefix != "" {
							words = append(words, "")
							copy(words[wi+2:], words[wi+1:])
							words[wi+1] = suffix
							continuation = append(continuation, false)
							copy(continuation[wi+2:], continuation[wi+1:])
							continuation[wi+1] = true
							transformedPart = append(transformedPart, false)
							copy(transformedPart[wi+2:], transformedPart[wi+1:])
							transformedPart[wi+1] = true
							text, wordW = prefix, measure(prefix)
							breakParts++
						}
					}
					boxW, boxH := int32(0), int32(0)
					if ctrl := run.Style.Control; ctrl != nil && (ctrl.Kind == "text" || ctrl.Kind == "textarea") {
						boxW = int32(ctrl.Width * baseSize / 16)
						if ctrl.WidthPct > 0 {
							boxW = int32(float64(avail) * ctrl.WidthPct / 100)
						}
						if boxW <= 0 {
							boxW = int32(baseSize * 12)
						}
						// `width: 55%; max-width: 600px` (DuckDuckGo's search
						// box) stops growing at the maximum.
						if mw := webConstraintLength(run.Style.MaxWidth, run.Style.MaxWidthPct, avail, baseSize/16); mw > 0 {
							boxW = min(boxW, int32(math.Round(mw*baseSize/16)))
						}
						if boxW > avail {
							boxW = avail
						}
						boxH = int32(ctrl.Height * baseSize / 16)
						if boxH < int32(height)+8 {
							boxH = int32(height) + 8
						}
						if boxH > rowH*8 {
							boxH = rowH * 8
						}
						if boxW < 16 {
							boxW = 16
						}
						if wordW > boxW-12 {
							text = ellipsize(font, text, boxW-12)
						}
						wordW = boxW
					}
					// The run's first word comes after the left edges of the boxes
					// it opens, its last before the right edges of those it
					// closes, and both count when the line decides whether the
					// word fits; the first word of an inline-block kept whole
					// needs room for the whole box.
					isFirst := !leadDone
					isLast := si == lastSeg && wi == len(words)-1
					extraL, extraR := int32(0), int32(0)
					if isFirst {
						extraL = leadPx
					}
					if isLast {
						extraR = trailPx
					}
					need := extraL + wordW + extraR
					if isFirst && atomicW > 0 {
						need = max(need, atomicW)
					}
					// Inside an inline-block kept whole the line does not break.
					hold := atomicLeft > 0 && !(isFirst && atomicStart)
					spaceBefore := int32(0)
					if len(line) > 0 {
						switch {
						case preserveSpaces:
							// The token carries its own leading spaces. A wrap drops
							// the whole token to the next line, indentation included.
							if wrap && !hold && x+need > lineRight && need <= avail {
								flushLine(false)
							}
						case wi > 0 || leadingSpace:
							spaceW := l.spaceWidth(font) + int32(math.Round(run.Style.WordSpacing*baseSize/16)) + letterSpacing
							if spaceW < 0 {
								spaceW = 0
							}
							// A nowrap inline run may still break before it starts when
							// the authored space gives the line a break opportunity; it
							// may not break between its own words.
							canBreak := (wrap || (wi == 0 && leadingSpace)) && !hold
							if canBreak && x+spaceW+need > lineRight {
								flushLine(false)
							} else {
								x += spaceW
								spaceBefore = spaceW
							}
						case blockWraps && !hold && ((isFirst && (atomicStart || leadPx > 0)) || edgeClosed):
							// The edge of an inline-block is a break opportunity, and
							// so, rather than run past the page, is the start of an
							// inline box with room before it.
							if x+need > lineRight {
								flushLine(false)
							}
						}
					}
					if boxW > 0 && len(line) > 0 && x+extraL+boxW > lineRight {
						flushLine(false)
					}
					x += extraL
					if room := max(avail-extraL-extraR, 0); boxW == 0 && wordW > room {
						if block.Style.TextOverflow == 1 {
							text = webClipWordToWidth(text, room, measure)
						} else {
							text = ellipsize(font, text, room)
						}
						wordW = measure(text)
					}
					if boxW == 0 && !wrap && x+wordW+extraR > lineRight {
						// nowrap/pre keeps one line: clip the overflowing tail with
						// the requested text-overflow mode. Older pages without a
						// mode retain the reader's ellipsis fallback.
						remaining := lineRight - x - extraR
						if remaining <= 0 {
							lineClipped = true
							continue
						}
						if block.Style.TextOverflow == 1 {
							text = webClipWordToWidth(text, remaining, measure)
						} else {
							text = ellipsize(font, text, remaining)
						}
						wordW = measure(text)
						lineClipped = true
						if text == "" {
							continue
						}
					}
					color := webResolveColor(run.Style, d)
					if run.Style.Control != nil && run.Style.Control.PlaceholderShown() {
						// A field's prompt is dimmed toward the paper rather than set in
						// italic, which a field chip measured in the upright face would
						// not hold: an empty field reads as a prompt, and stops the
						// moment a value is typed.
						color = sdl.Color{
							R: uint8((int(color.R) + 2*int(d.Paper.R)) / 3),
							G: uint8((int(color.G) + 2*int(d.Paper.G)) / 3),
							B: uint8((int(color.B) + 2*int(d.Paper.B)) / 3),
							A: color.A,
						}
					}
					pos := webRunPos{
						Text:                text,
						X:                   x,
						SpaceBefore:         spaceBefore,
						Boxes:               append([]int(nil), block.Boxes...),
						BoxWidth:            contentW,
						Font:                font,
						Color:               color,
						Bold:                run.Style.Bold,
						Underline:           run.Style.Underline,
						Strike:              run.Style.Strike,
						DecorationColor:     run.Style.DecorationColor,
						HasDecorationColor:  run.Style.HasDecorationColor,
						DecorationThickness: int32(math.Round(run.Style.DecorationThickness * baseSize / 16)),
						DecorationStyle:     run.Style.DecorationStyle,
						DecorationOffset:    int32(math.Round(run.Style.DecorationOffset * baseSize / 16)),
						HasShadow:           run.Style.HasTextShadow,
						ShadowX:             int32(math.Round(run.Style.TextShadowX * baseSize / 16)),
						ShadowY:             int32(math.Round(run.Style.TextShadowY * baseSize / 16)),
						ShadowColor:         run.Style.TextShadowColor,
						HasShadowColor:      run.Style.HasTextShadowColor,
						Href:                run.Style.Href,
						HasBg:               run.Style.HasBg && (!block.HasBg || boxW > 0 || run.Style.Background != block.Background),
						Bg:                  run.Style.Background,
						Control:             run.Style.Control,
						BoxW:                boxW, BoxH: boxH,
						// color: transparent keeps the words' room and paints none.
						Invisible:      run.Style.Invisible || (run.Style.HasColor && run.Style.Color.A == 0 && run.Style.Control == nil),
						VerticalOffset: webVerticalOffset(run.Style, font, baseSize),
					}
					if chip != nil && boxW == 0 && (pos.HasBg || chip.hasBorder()) {
						pos.Chip = chip
						if isFirst {
							pos.ChipLead, pos.ChipLeft = leadChip, run.OpensChip
						}
						if isLast {
							pos.ChipTrail, pos.ChipRight = trailChip, run.ClosesChip
						}
					}
					if ctrl := run.Style.Control; boxW > 0 || (ctrl != nil && ctrl.Kind == "select" && chip == nil) {
						// A text field or a select keeps the border and corners its
						// page gives it: a search box styled white on a white page
						// used to be drawn as a bare word (webPaintField).
						if frame := webRunChip(&run.Style, baseSize, d); frame.hasBorder() || frame.radius > 0 {
							pos.Field = &frame
						}
					}
					leadDone = true
					// A line with no room beside a float starts below it.
					takeWait()
					// Letter spacing changes the advance between glyphs. A run with
					// an inline background or a control frame is kept whole so its
					// box does not become one box per character; those are rare and
					// their spacing is visually irrelevant.
					if letterSpacing != 0 && boxW == 0 && !pos.HasBg && pos.Chip == nil && pos.Control == nil {
						drawX := x
						for gi, r := range text {
							glyph := string(r)
							pos.Text, pos.X = glyph, drawX
							if gi > 0 {
								pos.SpaceBefore = 0
							}
							line = append(line, pos)
							gw, _, err := font.SizeUTF8(glyph)
							if err != nil {
								gw = 0
							}
							drawX += int32(gw) + letterSpacing
						}
					} else {
						line = append(line, pos)
					}
					lastPos, lastSeq = len(line)-1, lineSeq
					x += wordW
					if isLast {
						x += extraR
						trailDone = true
					}
					edgeClosed = isLast && run.Style.DisplayInlineBlock && (run.ClosesChip || trailPx > 0)
					if x > lineMax {
						lineMax = x
					}
					if boxH > int32(height) {
						height = int(boxH)
					}
					if int32(height) > lineFontH {
						lineFontH = int32(height)
					}
				}
			}
			if !trailDone && lastPos >= 0 && lastSeq == lineSeq && lastPos < len(line) && (trailPx > 0 || run.ClosesChip) {
				// The run's last word was not the one expected (a trailing
				// break, a dropped glyph): the box still closes after it.
				if line[lastPos].Chip != nil {
					line[lastPos].ChipTrail, line[lastPos].ChipRight = trailChip, run.ClosesChip
				}
				x += trailPx
				lineMax = max(lineMax, x)
				edgeClosed = run.Style.DisplayInlineBlock
			}
			if lineClipped && !webWhiteSpaceWraps(runWhiteSpace) {
				// The clipped run has no line of its own left to fill; start the
				// following run on a fresh line instead of dropping it.
				flushLine(true)
			}
		}
		flushLine(true)

		endGrid := int32(len(layout.Rows))
		// A table cell whose only text was a no-break space is one empty line
		// tall (webParser.keepEmptyCell).
		if block.CellBlankLine && endGrid == startGrid {
			skipRows(1)
			endGrid = int32(len(layout.Rows))
		}
		// A pixel height is a floor for the flow box: short content pads out,
		// taller content keeps its rows (CSS overflow is not clipped).
		if minBoxH := webHeightContentTarget(block.Style, block.Style.Height) * baseSize / 16; minBoxH > 0 {
			rows := int32(math.Ceil(minBoxH / float64(rowH)))
			if endGrid-startGrid < rows {
				skipRows(rows - (endGrid - startGrid))
				endGrid = int32(len(layout.Rows))
			}
		}
		// An explicit width and aspect-ratio is a definite box even when the
		// element has little text: the modern `aspect-video`/skeleton card
		// reserves its ratio. Padding belongs to the border box, so subtract
		// it from the target height. An explicit height wins over the ratio.
		if ratio := block.Style.AspectRatio; ratio > 0 && block.Style.Height <= 0 && block.Style.MinHeight <= 0 && paintW > 0 {
			target := float64(paintW)/ratio - float64(padTop+padBot)
			if target > 0 {
				rows := int32(math.Ceil(target / float64(rowH)))
				if rows > 0 && endGrid-startGrid < rows {
					skipRows(rows - (endGrid - startGrid))
					endGrid = int32(len(layout.Rows))
				}
			}
		}
		// max-height clips the block's own rows; min-height pads it out. Both
		// are expressed in CSS px and rounded on the row grid, so a box never
		// ends up with a negative or fractional height.
		if block.Style.MaxHeight > 0 {
			maxH := webHeightContentTarget(block.Style, block.Style.MaxHeight) * baseSize / 16
			rows := int32(math.Ceil(maxH / float64(rowH)))
			if rows < 1 {
				rows = 1
			}
			if endGrid-startGrid > rows {
				endGrid = startGrid + rows
				if int(endGrid) < len(layout.Rows) {
					layout.Rows = layout.Rows[:endGrid]
				}
			}
		}
		if minH := webHeightContentTarget(block.Style, block.Style.MinHeight) * baseSize / 16; minH > 0 {
			rows := int32(math.Ceil(minH / float64(rowH)))
			if endGrid-startGrid < rows {
				skipRows(rows - (endGrid - startGrid))
				endGrid = int32(len(layout.Rows))
			}
		}
		l.recordContainerRows(block, blockStart, endGrid)
		height := (endGrid-startGrid)*rowH + padTop + padBot
		if half := min32(paintW, height) / 2; radius > half {
			// A pill/circle radius is clamped on the used box, not left as the
			// author's 999px sentinel for every consumer to remember to clamp.
			radius = half
		}
		if block.Style.HasBoxShadow && !block.Style.Invisible && height > 0 {
			// The shadow follows the surface that is actually filled (the
			// background colour rect starts at the first row), or the border
			// box when the block has no colour of its own. It is emitted just
			// before that background so it paints underneath it.
			shadowTop := paintTop
			if block.HasBg {
				shadowTop = startGrid * rowH
			}
			l.emitBoxShadow(block.Style, sdl.Rect{X: padX - 4, Y: shadowTop, W: paintW + 8, H: height}, radius, block.ZIndex)
		}
		if block.HasBg && !block.Style.Invisible && height > 0 {
			layout.Rects = append(layout.Rects, webRect{
				Rect:           sdl.Rect{X: padX - 4, Y: startGrid * rowH, W: paintW + 8, H: height},
				Color:          webFadeColor(block.Background, d.Paper, block.Style),
				Z:              block.ZIndex,
				Radius:         radius,
				BackgroundFill: true,
			})
			layout.Backgrounds = append(layout.Backgrounds, webBackgroundPaint{
				Rect:   sdl.Rect{X: padX - 4, Y: startGrid * rowH, W: paintW + 8, H: height},
				Color:  webFadeColor(block.Background, d.Paper, block.Style),
				Radius: radius,
				Z:      block.ZIndex,
			})
		}
		if !block.Style.Invisible && height > 0 {
			tileBox := sdl.Rect{X: padX + indent, Y: paintTop, W: blockW + indentRight, H: height}
			layers := make([]webBackgroundLayerStyle, 0, webMaxBackgroundLayers)
			for i := 0; i < block.Style.BackgroundLayerCount && i < webMaxBackgroundLayers; i++ {
				layers = append(layers, block.Style.BackgroundLayers[i])
			}
			if len(layers) == 0 && (block.HasGradient || block.Style.BackgroundImage != "") {
				layers = append(layers, webBackgroundLayerStyle{
					Image: block.Style.BackgroundImage, Gradient: &block.Gradient,
					HasGradient: block.HasGradient, Size: block.Style.BackgroundSize,
					Position: block.Style.BackgroundPosition, Repeat: block.Style.BackgroundRepeat,
				})
			}
			remaining := webMaxBackgroundTiles
			for i := len(layers) - 1; i >= 0 && remaining > 0; i-- {
				layer := layers[i]
				if layer.Image == "" && !layer.HasGradient {
					continue
				}
				layerRect, fit := webBackgroundLayerGeometry(tileBox, layer, d, baseSize/16)
				tiles := webBackgroundTiles(tileBox, layerRect, layer.Repeat)
				if len(tiles) > remaining {
					tiles = tiles[:remaining]
				}
				remaining -= len(tiles)
				if layer.HasGradient && layer.Gradient != nil {
					layerColor := webGradientAverage(*layer.Gradient)
					for _, tile := range tiles {
						layout.Backgrounds = append(layout.Backgrounds, webBackgroundPaint{
							Rect:        tile,
							Color:       webFadeColor(layerColor, d.Paper, block.Style),
							Gradient:    webFadeGradient(*layer.Gradient, d.Paper, block.Style),
							HasGradient: true,
							Radius:      radius,
							Z:           block.ZIndex,
						})
					}
				}
				if layer.Image != "" {
					for _, tile := range tiles {
						layout.Backgrounds = append(layout.Backgrounds, webBackgroundPaint{
							Rect:           tile,
							Image:          layer.Image,
							ObjectFit:      fit,
							ObjectPosition: layer.Position,
							Opacity:        webPaintOpacity(block.Style),
							Z:              block.ZIndex,
						})
					}
				}
			}
		}
		borderPaint := block.BorderStyle != webBorderNone && block.BorderStyle != webBorderHidden && !block.HasCollapseEdges
		if block.HasCollapseEdges && !block.Style.Invisible && height > 0 {
			x0, y0 := padX+indent, startGrid*rowH
			for side, edge := range block.CollapseEdges {
				if !edge.valid || edge.width <= 0 || edge.style == webBorderNone || edge.style == webBorderHidden {
					continue
				}
				thickness := int32(math.Round(edge.width))
				if thickness < 1 {
					thickness = 1
				}
				if thickness > 32 {
					thickness = 32
				}
				color := webCurrentColor(block.Style, d)
				if edge.hasColor {
					color = edge.color
				}
				color = webFadeColor(color, d.Paper, block.Style)
				var edgeRect sdl.Rect
				switch side {
				case 0: // left
					edgeRect = sdl.Rect{X: x0, Y: y0, W: thickness, H: height}
				case 1: // right
					edgeRect = sdl.Rect{X: x0 + blockW - thickness, Y: y0, W: thickness, H: height}
				case 2: // top
					edgeRect = sdl.Rect{X: x0, Y: y0, W: blockW, H: thickness}
				case 3: // bottom
					edgeRect = sdl.Rect{X: x0, Y: y0 + height - thickness, W: blockW, H: thickness}
				}
				layout.Rects = append(layout.Rects, webRect{
					Rect: edgeRect, Color: color, Z: block.ZIndex,
					Thickness: thickness, BorderStyle: edge.style,
				})
			}
		}
		if height > 0 && !block.Style.Invisible && ((borderPaint && (block.BorderLeft || block.BorderRight || block.BoxBorder || block.BorderTop || block.BorderBottom)) || block.Outline) {
			// One colour for every edge the block draws: what the sheet asked
			// for — a `border-left: 4px solid #c00` quote used to render a grey
			// bar, which is the one thing the author definitely decided — or,
			// uncoloured, currentColor, the text's own colour, as in a browser.
			color := webCurrentColor(block.Style, d)
			if block.HasBorderColor {
				color = block.BorderColor
			}
			color = webFadeColor(color, d.Paper, block.Style)
			// A side bar defaults wider than a horizontal rule, because it is
			// the quote/sidebar idiom rather than a divider.
			barW := int32(math.Round(block.BorderWidth))
			if barW <= 0 {
				barW = 3
			}
			if barW > 12 {
				barW = 12
			}
			ruleH := barW
			if block.BorderWidth <= 0 {
				ruleH = 2
			}
			top := paintTop
			if borderPaint && block.BorderLeft {
				layout.Rects = append(layout.Rects, webRect{
					Rect:        sdl.Rect{X: padX + indent - 8, Y: top, W: barW, H: height},
					Color:       color,
					Z:           block.ZIndex,
					BorderStyle: block.BorderStyle,
				})
			}
			if borderPaint && block.BorderRight {
				// Mirrors the left bar into the same gutter on the other side.
				x := padX + indent + blockW + 5
				layout.Rects = append(layout.Rects, webRect{
					Rect:        sdl.Rect{X: x, Y: top, W: barW, H: height},
					Color:       color,
					Z:           block.ZIndex,
					BorderStyle: block.BorderStyle,
				})
			}
			if borderPaint && block.BoxBorder {
				x := padX + indent - 6
				w := blockW + indentRight + 12
				if x < padX {
					x = padX
					w = paintW
				}
				layout.Rects = append(layout.Rects, webRect{
					Rect:        sdl.Rect{X: x, Y: top, W: w, H: height},
					Color:       color,
					Stroke:      true,
					Thickness:   ruleH,
					Z:           block.ZIndex,
					Radius:      radius,
					BorderStyle: block.BorderStyle,
				})
			} else {
				if borderPaint && block.BorderTop {
					layout.Rects = append(layout.Rects, webRect{
						Rect:        sdl.Rect{X: padX, Y: top, W: paintW, H: ruleH},
						Color:       color,
						Z:           block.ZIndex,
						BorderStyle: block.BorderStyle,
					})
				}
				if borderPaint && block.BorderBottom {
					layout.Rects = append(layout.Rects, webRect{
						Rect:        sdl.Rect{X: padX, Y: top + height - ruleH, W: paintW, H: ruleH},
						Color:       color,
						Z:           block.ZIndex,
						BorderStyle: block.BorderStyle,
					})
				}
			}
			if block.Outline {
				off := int32(math.Round(block.OutlineOffset * baseSize / 16))
				x := padX + indent - 6 - off
				w := blockW + indentRight + 12 + off*2
				if x < padX && off <= 0 {
					x = padX
					w = paintW
				}
				layout.Rects = append(layout.Rects, webRect{
					Rect:      sdl.Rect{X: x, Y: top - off, W: w, H: height + off*2},
					Color:     color,
					Stroke:    true,
					Thickness: ruleH,
					Z:         block.ZIndex,
					Radius:    radius + off,
				})
			}
		}
		if block.Style.OverflowX != 0 || block.Style.OverflowY != 0 {
			clip := sdl.Rect{X: 0, Y: 0, W: contentW, H: 1 << 30}
			if block.Style.OverflowX != 0 {
				clip.X, clip.W = padX+indent, blockW+indentRight
			}
			if block.Style.OverflowY != 0 {
				clip.Y, clip.H = startGrid*rowH, height
			}
			for i := int(startGrid); i < len(layout.Rows) && i < int(endGrid); i++ {
				for j := range layout.Rows[i].Runs {
					layout.Rows[i].Runs[j].Clip, layout.Rows[i].Runs[j].HasClip = clip, true
				}
			}
			for i := rectStart; i < len(layout.Rects); i++ {
				layout.Rects[i].Clip, layout.Rects[i].HasClip = clip, true
			}
			for i := imageStart; i < len(layout.Images); i++ {
				layout.Images[i].Clip, layout.Images[i].HasClip = clip, true
			}
			for i := videoStart; i < len(layout.Videos); i++ {
				layout.Videos[i].Clip, layout.Videos[i].HasClip = clip, true
			}
		}
		if block.Style.TableRole == webTableRoleCell && block.Style.CellOwn {
			// The row gives a cell's surface its full height (webpage_table.go).
			webTagCellSurface(layout, block, rectStart, bgStart, paintTop, startGrid*rowH, height)
		}
		// A relative left/right in percent is of the containing block's width,
		// and translate() moves the box by lengths and by percentages of its
		// own size — `left: 50%; transform: translateX(-50%)` centres it, and
		// a card pulled up into the hero above by translateY(-60px) overlaps
		// it. Neither changes the flow.
		tdx, tdy := webInFlowShift(block.Style, contentW, paintW, height, baseSize)
		dx, dy = dx+tdx, dy+tdy
		shiftRelative()
		l.tagBlockBox(block, rectStart, bgStart)
	}
	// What one pass lays out — the page, a flex or grid item, a table cell, a
	// layer, a float — holds its floats, as a formatting context does: it ends
	// below the last of them, so a card is as tall as its floated picture and
	// the page scrolls to a float's foot (webpage_floats.go).
	if end := webFloatsEnd(floatReserves); end > int32(len(layout.Rows)) {
		skipRows(end - int32(len(layout.Rows)))
	}
	syncBoxes(nil, 0)
}

// tagBlockBox marks the paints this block produced for its own container box.
// The container pass uses the tag to replace a direct block's partial surface
// when the same wrapper also contains nested block content.
// A block whose element does not own that box paints content inside it
// (Inner), which the paint order puts over the box's surface.
func (l *webLayouter) tagBlockBox(block webBlock, rectStart, bgStart int) {
	l.tagPaints(block.BoxOwner, !block.OwnsBox, rectStart, bgStart)
}

// tagContentBox marks a replaced element's paints (an image's, a play card's,
// a placeholder's frame) as content inside its block's box.
func (l *webLayouter) tagContentBox(block webBlock, rectStart, bgStart int) {
	l.tagPaints(block.BoxOwner, true, rectStart, bgStart)
}

func (l *webLayouter) tagPaints(box int, inner bool, rectStart, bgStart int) {
	if l == nil || l.layout == nil || box == 0 {
		return
	}
	for i := rectStart; i < len(l.layout.Rects); i++ {
		if r := &l.layout.Rects[i]; r.BoxID == 0 {
			r.BoxID = box
			r.Inner = r.Inner || inner
		}
	}
	for i := bgStart; i < len(l.layout.Backgrounds); i++ {
		if bg := &l.layout.Backgrounds[i]; bg.BoxID == 0 {
			bg.BoxID = box
			bg.Inner = bg.Inner || inner
		}
	}
}

// webHrefFollowable reports whether an anchor target is worth a cursor stop:
// http, https or a relative path. In-page anchors and non-web schemes
// (javascript:, mailto:, data:, file:) are not pages the browser can open.
func webHrefFollowable(href string) bool {
	trimmed := strings.TrimSpace(href)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return false
	}
	if i := strings.IndexByte(trimmed, ':'); i > 0 {
		// A colon after a slash is part of a path, not a scheme.
		if !strings.ContainsAny(trimmed[:i], "/?#") {
			scheme := strings.ToLower(trimmed[:i])
			return scheme == "http" || scheme == "https"
		}
	}
	return true
}

// collectWebLinks turns the laid-out link runs into one hit per destination.
// A link that wraps across rows, or a nav entry repeated in the header and the
// footer, appears once; walking it with the D-pad would otherwise stop twice
// on the same place. In-page anchors have nothing to fetch and are skipped.
// collectWebControls finds the form controls a page drew. A control is one run
// or a few adjacent runs sharing the same *webControl, so it is grouped exactly
// the way a link's runs are, and it becomes the same kind of hit: the browser
// outlines it, walks it with the D-pad and activates it.
func collectWebControls(layout *webLayout) []webLinkHit {
	var hits []webLinkHit
	for rowIdx := 0; rowIdx < len(layout.Rows); rowIdx++ {
		row := layout.Rows[rowIdx]
		for i := 0; i < len(row.Runs); {
			ctrl := row.Runs[i].Control
			if ctrl == nil || row.Runs[i].Font == nil {
				i++
				continue
			}
			startX := row.Runs[i].X
			endX := startX
			label := ""
			j := i
			for ; j < len(row.Runs); j++ {
				cur := row.Runs[j]
				if cur.Control != ctrl || cur.Font == nil {
					break
				}
				w, _, err := cur.Font.SizeUTF8(cur.Text)
				if err != nil {
					break
				}
				label += cur.Text
				if end := cur.X + int32(w); end > endX {
					endX = end
				}
			}
			if j == i {
				j = i + 1
			}
			hit := webLinkHit{Row: rowIdx, X: startX, W: endX - startX, Text: strings.TrimSpace(label), Control: ctrl}
			run := row.Runs[i]
			// The control is drawn VerticalOffset below its row (a field in a
			// centred flex item, a grid track's edge); so is its target.
			y := row.Y + run.VerticalOffset
			if run.BoxW > 0 && run.BoxH > 0 {
				hit.W = run.BoxW
				hit.Box = sdl.Rect{X: run.X, Y: y, W: run.BoxW, H: run.BoxH}
			} else if run.Font != nil {
				// A submit button draws its label as ordinary text and has no
				// control frame of its own. Its hit box was left at 0×0, so the
				// D-pad could focus the button but not touch it. Use the measured
				// run as the hit target, just like a link uses its own extent,
				// with the button's padding and border round it.
				if w, h, err := run.Font.SizeUTF8(run.Text); err == nil && w > 0 && h > 0 {
					hit.Box = sdl.Rect{X: run.X, Y: y, W: int32(w), H: int32(h)}
					if c := run.Chip; c != nil {
						hit.Box.X -= run.ChipLead
						hit.Box.W += run.ChipLead + run.ChipTrail
						hit.Box.Y -= c.padT
						hit.Box.H += c.padT + c.padB
						hit.X, hit.W = hit.Box.X, hit.Box.W
					}
				}
			}
			hits = append(hits, hit)
			i = j
		}
	}
	return hits
}

func collectWebLinks(layout *webLayout) []webLinkHit {
	if layout == nil {
		return nil
	}
	var links []webLinkHit
	seen := map[string]bool{}
	for rowIdx := 0; rowIdx < len(layout.Rows); rowIdx++ {
		row := layout.Rows[rowIdx]
		for i := 0; i < len(row.Runs); {
			run := row.Runs[i]
			if run.Font == nil || !webHrefFollowable(run.Href) {
				i++
				continue
			}
			href := run.Href
			// A link drawn as a button or a pill is as wide as its chip
			// (webpage_inlinebox.go).
			startX := run.X - run.ChipLead
			endX := run.X
			label := ""
			j := i
			for ; j < len(row.Runs); j++ {
				cur := row.Runs[j]
				if cur.Href != href || cur.Font == nil {
					break
				}
				// An image inside the link counts with its own width, so a
				// logo or a thumbnail that is the whole link is a stop too.
				w := cur.ImageW
				if cur.Image == nil {
					tw, _, err := cur.Font.SizeUTF8(cur.Text)
					if err != nil {
						break
					}
					w = int32(tw)
				} else if cur.Image.ImageAlt != "" {
					label += " " + cur.Image.ImageAlt
				}
				label += " " + cur.Text
				if end := cur.X + w + cur.ChipTrail; end > endX {
					endX = end
				}
			}
			i = j
			if endX <= startX || seen[href] {
				continue
			}
			seen[href] = true
			hit := webLinkHit{
				Row:  rowIdx,
				X:    startX,
				W:    endX - startX,
				Href: href,
				Text: strings.Join(strings.Fields(label), " "),
			}
			if c := run.Chip; c != nil && run.Image == nil {
				// The focus ring and the tap target are the chip.
				hit.Box = sdl.Rect{X: startX, Y: row.Y + run.VerticalOffset - c.padT, W: endX - startX, H: int32(run.Font.Height()) + c.padT + c.padB}
			} else if vo := run.VerticalOffset; vo != 0 && layout.RowH > 0 {
				// Drawn below its row (a centred flex item, a grid track's exact
				// edge): the focus ring and the tap target go where the words are.
				hit.Box = sdl.Rect{X: startX - 4, Y: row.Y + vo - 2, W: endX - startX + 8, H: layout.RowH + 4}
			}
			links = append(links, hit)
		}
	}
	return links
}

func webLuminance(c sdl.Color) float64 {
	channel := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= 0.03928 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(c.R) + 0.7152*channel(c.G) + 0.0722*channel(c.B)
}

func webContrast(a, b sdl.Color) float64 {
	la, lb := webLuminance(a), webLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// webMinTextContrast is the contrast below which the reader takes a text
// colour for a mistake of its own rather than the author's design: the page
// assumed a surface the reader did not paint (a background it cannot draw),
// and the words would vanish into the paper. #aaa on white (2.3:1) is a design
// choice, a caption or a footer, and is drawn as written; white on white is not.
const webMinTextContrast = 1.5

// webReadableColor keeps the author's colour, as a browser does, unless it
// would all but vanish into the backdrop the reader knows for it — then it is
// flipped to a dark or light ink. Text over a picture (style.ImageBackdrop)
// and text with a shadow drawn to set it off are the author's own judgement
// of a backdrop the reader cannot see, and are never flipped.
func webReadableColor(fg, bg sdl.Color, style webStyle) sdl.Color {
	if fg.A == 0 || style.ImageBackdrop || webContrast(fg, bg) >= webMinTextContrast {
		return fg
	}
	if style.HasTextShadow && style.HasTextShadowColor && style.TextShadowColor.A >= 128 &&
		webContrast(fg, style.TextShadowColor) >= 3 {
		return fg
	}
	if webLuminance(bg) > 0.4 {
		return sdl.Color{R: 28, G: 32, B: 38, A: 255}
	}
	return sdl.Color{R: 242, G: 245, B: 250, A: 255}
}

// webLinkBlue and webLinkBlueDark are Chrome's LinkText on a light page
// (#0000ee) and on a dark one (#9e9eff): an unstyled link on a surface where
// the first would not read takes the second.
var (
	webLinkBlue     = sdl.Color{R: 0, G: 0, B: 238, A: 255}
	webLinkBlueDark = sdl.Color{R: 158, G: 158, B: 255, A: 255}
)

func webResolveColor(style webStyle, doc *webDocument) sdl.Color {
	// Text is judged against the surface it sits on: the element's own fill
	// composited over the surfaces behind it (Backdrop, computeStyle), or the
	// paper. A light chip on a dark page would otherwise draw light text on a
	// light fill.
	bg := doc.Paper
	switch {
	case style.HasBackdrop:
		bg = style.Backdrop
	case style.HasBg:
		bg = webColorOver(style.Background, doc.Paper)
	}
	color := doc.Text
	if style.HasColor {
		color = style.Color
	} else if style.Link {
		color = webLinkBlue
		if webContrast(webLinkBlue, bg) < 3 {
			color = webLinkBlueDark
		}
	}
	// A translucent text colour (rgba(0,0,0,.54), the secondary text of most
	// design systems) is drawn as the colour it shows on its backdrop. Over a
	// picture the alpha is kept for the glyph renderer instead.
	if color.A > 0 && color.A < 255 && !style.ImageBackdrop {
		color = webColorOver(color, bg)
	}
	// Opacity is painted by blending toward the surface the text sits on: the
	// reader has no compositing layer, and a faded label should still read.
	return webFadeColor(webReadableColor(color, bg, style), bg, style)
}

// webRectCorner is the radius a rect is painted with: the author's
// border-radius clamped to half the shorter side (so a percentage or a huge
// value is a pill, not an error), or the reader's own crisp default.
func webRectCorner(bg webRect, fallback int32) int32 {
	radius := bg.Radius
	if radius <= 0 {
		radius = fallback
	}
	limit := bg.Rect.W
	if bg.Rect.H < limit {
		limit = bg.Rect.H
	}
	if limit > 0 && radius > limit/2 {
		radius = limit / 2
	}
	if radius < 0 {
		radius = 0
	}
	return radius
}

// webGradientCornerInset is how far a rounded corner cuts into the row at dy,
// the same circle approximation gradientRoundedRect uses for the app's own
// surfaces.
func webGradientCornerInset(radius, height, dy int32) int32 {
	if radius <= 0 || dy < 0 || dy >= height {
		return 0
	}
	if dy < radius {
		return radius - int32(math.Sqrt(float64(2*radius*dy-dy*dy)))
	}
	if d := dy - (height - radius); d > 0 {
		return radius - int32(math.Sqrt(float64(2*radius*d-d*d)))
	}
	return 0
}

// webBorderPaintRects turns a border style into the rectangles the painter
// fills. Solid returns nil because the rounded stroke path already draws it;
// none/hidden return no geometry. The rectangle is layout-local, so callers
// translate it before filling.
func webBorderPaintRects(rect sdl.Rect, thickness int32, style int, closed bool) []sdl.Rect {
	if rect.W <= 0 || rect.H <= 0 || style == webBorderNone || style == webBorderHidden {
		return nil
	}
	if thickness < 1 {
		thickness = 1
	}
	if style == webBorderSolid {
		return nil
	}
	if style == webBorderDouble {
		t := thickness / 3
		if t < 1 {
			t = 1
		}
		gap := t
		out := make([]sdl.Rect, 0, 8)
		if closed {
			out = append(out,
				sdl.Rect{X: rect.X, Y: rect.Y, W: rect.W, H: t},
				sdl.Rect{X: rect.X, Y: rect.Y + 2*(t+gap), W: rect.W, H: t},
				sdl.Rect{X: rect.X, Y: rect.Y + rect.H - t, W: rect.W, H: t},
				sdl.Rect{X: rect.X, Y: rect.Y + rect.H - 2*(t+gap) - t, W: rect.W, H: t},
				sdl.Rect{X: rect.X, Y: rect.Y, W: t, H: rect.H},
				sdl.Rect{X: rect.X + 2*(t+gap), Y: rect.Y, W: t, H: rect.H},
				sdl.Rect{X: rect.X + rect.W - t, Y: rect.Y, W: t, H: rect.H},
				sdl.Rect{X: rect.X + rect.W - 2*(t+gap) - t, Y: rect.Y, W: t, H: rect.H},
			)
			return out
		}
		if rect.W >= rect.H {
			return []sdl.Rect{
				{X: rect.X, Y: rect.Y, W: rect.W, H: t},
				{X: rect.X, Y: rect.Y + rect.H - t, W: rect.W, H: t},
			}
		}
		return []sdl.Rect{
			{X: rect.X, Y: rect.Y, W: t, H: rect.H},
			{X: rect.X + rect.W - t, Y: rect.Y, W: t, H: rect.H},
		}
	}
	dotted := style == webBorderDotted
	dash := thickness * 3
	gap := thickness * 2
	if dash < 4 {
		dash = 4
	}
	if dotted {
		dash = thickness
		gap = thickness * 2
	}
	line := func(x, y, w, h int32, horizontal bool) []sdl.Rect {
		out := make([]sdl.Rect, 0, int(max32(w, h)/max32(dash+gap, 1))+1)
		length := w
		if !horizontal {
			length = h
		}
		for pos := int32(0); pos < length; pos += dash + gap {
			end := min32(pos+dash, length)
			if horizontal {
				out = append(out, sdl.Rect{X: x + pos, Y: y, W: end - pos, H: h})
			} else {
				out = append(out, sdl.Rect{X: x, Y: y + pos, W: w, H: end - pos})
			}
		}
		return out
	}
	if !closed {
		if rect.W >= rect.H {
			return line(rect.X, rect.Y, rect.W, rect.H, true)
		}
		return line(rect.X, rect.Y, rect.W, rect.H, false)
	}
	out := make([]sdl.Rect, 0, 8)
	out = append(out, line(rect.X, rect.Y, rect.W, thickness, true)...)
	out = append(out, line(rect.X, rect.Y+rect.H-thickness, rect.W, thickness, true)...)
	out = append(out, line(rect.X, rect.Y, thickness, rect.H, false)...)
	out = append(out, line(rect.X+rect.W-thickness, rect.Y, thickness, rect.H, false)...)
	return out
}

// webClipPaint applies an item's overflow clip in page coordinates and returns
// a restore function. The clip is layout-local, so it is translated by the
// page origin and the current row offset before intersecting the page rect.
func webClipPaint(renderer *sdl.Renderer, page, clip sdl.Rect, has bool, pageX, pageY int32) func() {
	if renderer == nil || !has {
		return func() {}
	}
	abs := sdl.Rect{X: pageX + clip.X, Y: pageY + clip.Y, W: clip.W, H: clip.H}
	left, top := max32(abs.X, page.X), max32(abs.Y, page.Y)
	right := min32(abs.X+abs.W, page.X+page.W)
	bottom := min32(abs.Y+abs.H, page.Y+page.H)
	if right <= left || bottom <= top {
		empty := sdl.Rect{}
		old := renderer.GetClipRect()
		renderer.SetClipRect(&empty)
		return func() { webRestoreClip(renderer, old) }
	}
	abs = sdl.Rect{X: left, Y: top, W: right - left, H: bottom - top}
	old := renderer.GetClipRect()
	renderer.SetClipRect(&abs)
	return func() { webRestoreClip(renderer, old) }
}

func webRestoreClip(renderer *sdl.Renderer, old sdl.Rect) {
	if old.W > 0 && old.H > 0 {
		renderer.SetClipRect(&old)
		return
	}
	renderer.SetClipRect(nil)
}

// webDecorationPaintRects is the underline/strikethrough equivalent of the
// border geometry helper: one solid bar, paired bars for double, and broken or
// wavy runs for the other styles. Coordinates are relative to the page rect.
// webUnderlinePaintY is where a run's underline sits: under the glyph box, as
// the reader has always drawn it, moved by text-underline-offset (a positive
// offset away from the text, a negative one toward it). The paint path and its
// tests share it, so the property cannot be parsed and then not drawn.
func webUnderlinePaintY(run *webRunPos, textY int32) int32 {
	height := int32(16)
	offset := int32(0)
	if run != nil {
		offset = run.DecorationOffset
		if run.Font != nil {
			height = int32(run.Font.Height())
		}
	}
	return textY + height - 5 + offset
}

func webDecorationPaintRects(x, y, width, thickness int32, style int) []sdl.Rect {
	if width <= 0 {
		return nil
	}
	if thickness < 1 {
		thickness = 1
	}
	switch style {
	case webDecorationDouble:
		t := thickness / 3
		if t < 1 {
			t = 1
		}
		gap := t
		return []sdl.Rect{
			{X: x, Y: y, W: width, H: t},
			{X: x, Y: y + t + gap, W: width, H: t},
		}
	case webDecorationDotted:
		step := thickness * 2
		if step < 3 {
			step = 3
		}
		out := make([]sdl.Rect, 0, int(width/step)+1)
		for pos := int32(0); pos < width; pos += step {
			w := min32(thickness, width-pos)
			out = append(out, sdl.Rect{X: x + pos, Y: y, W: w, H: thickness})
		}
		return out
	case webDecorationDashed:
		dash := thickness * 3
		if dash < 4 {
			dash = 4
		}
		gap := thickness * 2
		if gap < 2 {
			gap = 2
		}
		out := make([]sdl.Rect, 0, int(width/(dash+gap))+1)
		for pos := int32(0); pos < width; pos += dash + gap {
			w := min32(dash, width-pos)
			out = append(out, sdl.Rect{X: x + pos, Y: y, W: w, H: thickness})
		}
		return out
	case webDecorationWavy:
		step := int32(4)
		h := thickness / 2
		if h < 1 {
			h = 1
		}
		out := make([]sdl.Rect, 0, int(width/step)+1)
		for pos := int32(0); pos < width; pos += step {
			w := min32(step, width-pos)
			dy := int32(math.Round(math.Sin(float64(pos)/6) * 2))
			out = append(out, sdl.Rect{X: x + pos, Y: y + dy, W: w, H: h})
		}
		return out
	default:
		return []sdl.Rect{{X: x, Y: y, W: width, H: thickness}}
	}
}

// drawWebGradient paints a ramp inside rect, band by band. The band count is
// capped so a tall hero card costs a bounded number of fills per frame while a
// small header still gets one band per row: at 96 bands a 40-px header is
// per-pixel, and a 600-px hero is a 6-px staircase nobody can see.
func drawWebGradient(renderer *sdl.Renderer, rect sdl.Rect, radius int32, g webGradient) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 || len(g.Stops) == 0 {
		return
	}
	// One vertex strip with a pair at every stop: smooth, and one draw call
	// instead of up to 96 (webpage_paintfast.go). The bands below are the
	// fallback where SDL cannot draw geometry.
	if webPaintGradient(renderer, rect, radius, g) {
		return
	}
	if g.Radial || g.Conic {
		// Rings and slices need geometry; without it the gradient is its
		// averaged colour, as it always was.
		webPaintFill(renderer, rect.X, rect.Y, rect.W, rect.H, radius, webGradientAverage(g))
		return
	}
	const maxBands = 96
	if g.Vertical {
		bands := rect.H
		if bands > maxBands {
			bands = maxBands
		}
		if bands < 1 {
			bands = 1
		}
		for i := int32(0); i < bands; i++ {
			y0 := rect.Y + i*rect.H/bands
			y1 := rect.Y + (i+1)*rect.H/bands
			if y1 <= y0 {
				continue
			}
			// The colour is sampled at the band's middle, so a coarse band does
			// not look like the old flat fill with a seam in it.
			color := g.At((float64(i) + 0.5) / float64(bands))
			x0, w := rect.X, rect.W
			if radius > 0 {
				// The inset is sampled at the band's middle, so a rounded corner
				// steps once per band instead of once per scanline.
				inset := webGradientCornerInset(radius, rect.H, (y0+y1)/2-rect.Y)
				x0 += inset
				w -= inset * 2
			}
			if w <= 0 {
				continue
			}
			renderer.SetDrawColor(color.R, color.G, color.B, color.A)
			renderer.FillRect(&sdl.Rect{X: x0, Y: y0, W: w, H: y1 - y0})
		}
		return
	}

	bands := rect.W
	if bands > maxBands {
		bands = maxBands
	}
	if bands < 1 {
		bands = 1
	}
	for i := int32(0); i < bands; i++ {
		x0 := rect.X + i*rect.W/bands
		x1 := rect.X + (i+1)*rect.W/bands
		if x1 <= x0 {
			continue
		}
		color := g.At((float64(i) + 0.5) / float64(bands))
		y0, h := rect.Y, rect.H
		if radius > 0 {
			inset := webGradientCornerInset(radius, rect.W, (x0+x1)/2-rect.X)
			y0 += inset
			h -= inset * 2
		}
		if h <= 0 {
			continue
		}
		renderer.SetDrawColor(color.R, color.G, color.B, color.A)
		renderer.FillRect(&sdl.Rect{X: x0, Y: y0, W: x1 - x0, H: h})
	}
}

// drawWebDocument paints the styled page inside rect starting at startRow.
func drawWebDocument(renderer *sdl.Renderer, config *Config, doc *webDocument, layout *webLayout, rect sdl.Rect, startRow int) {
	if doc == nil || layout == nil || layout.RowH <= 0 || rect.W <= 0 || rect.H <= 0 {
		return
	}
	if startRow < 0 {
		startRow = 0
	}
	if startRow >= len(layout.Rows) {
		startRow = 0
	}
	// Pictures decoded off the render thread since the last frame become
	// textures first, so this frame can already draw them (webpage_decode.go).
	webDecodeUpload(renderer)
	offset := int32(startRow) * layout.RowH
	// On the handhelds the page is painted once into GPU tiles and copied to
	// the screen each frame (webpage_tiles.go); elsewhere, and on a page the
	// tiles cannot hold, it is painted here every frame.
	if webTilesDraw(renderer, config, doc, layout, rect, offset) {
		webDecodeAhead(doc, layout, rect, offset)
		return
	}
	renderWithClip(renderer, rect.X, rect.Y, rect.W, rect.H, func(r *sdl.Renderer) {
		drawWebPageContent(r, config, doc, layout, rect, offset, webPaintAll)
		// Pictures just outside the view are decoded before they scroll in.
		webDecodeAhead(doc, layout, rect, offset)
	})
}

// What drawWebPageContent paints: the whole page, or the part that moves with
// the scroll — everything but position: fixed layers and sticky bars' pinned
// copies — which is what a page tile holds (webpage_tiles.go).
const (
	webPaintAll uint8 = iota
	webPaintScrolling
)

// drawWebPageContent paints the page's paper and everything on it into rect,
// the page scrolled offset layout pixels down. The caller sets the clip.
// Render thread.
func drawWebPageContent(r *sdl.Renderer, config *Config, doc *webDocument, layout *webLayout, rect sdl.Rect, offset int32, part uint8) {
	if doc.HasPaperGradient {
		// The page background is a ramp too, not the average of one: a
		// body gradient is most of what a landing page looks like.
		drawWebGradient(r, rect, 0, doc.PaperGradient)
	} else {
		r.SetDrawColor(doc.Paper.R, doc.Paper.G, doc.Paper.B, 255)
		r.FillRect(&sdl.Rect{X: rect.X, Y: rect.Y, W: rect.W, H: rect.H})
	}

	// The page paints as a browser stacks it (webpage_layers.go): layers
	// with a negative z-index, the flow's surfaces, negative layers inside a
	// stacking context, floats, the flow's pictures and text, then the
	// positioned layers and pinned sticky bars in z order.
	drawWebLayerPhase(r, config, doc, layout, rect, offset, webLayerUnder)
	for _, bg := range layout.Backgrounds {
		if bg.Lifted {
			// Painted with its positioned element (webLiftPositioned).
			continue
		}
		y := rect.Y + bg.Rect.Y - offset
		if bg.Shadow {
			// A soft shadow reaches past its box by the blur, so it has
			// its own visibility test.
			drawWebShadowPaint(r, bg, rect.X+bg.Rect.X, y, rect)
			continue
		}
		if y+bg.Rect.H <= rect.Y || y >= rect.Y+rect.H || bg.Rect.W <= 0 || bg.Rect.H <= 0 {
			continue
		}
		if bg.Image != "" {
			drawWebImage(r, config, doc, webImagePos{
				Key: bg.Image, Rect: bg.Rect, ObjectFit: bg.ObjectFit,
				ObjectPosition: bg.ObjectPosition, Opacity: bg.Opacity,
				Radius: bg.Radius,
			}, rect.X+bg.Rect.X, y)
			continue
		}
		if bg.HasGradient {
			drawWebGradient(r, sdl.Rect{X: rect.X + bg.Rect.X, Y: y, W: bg.Rect.W, H: bg.Rect.H}, bg.Radius, bg.Gradient)
			continue
		}
		webPaintFill(r, rect.X+bg.Rect.X, y, bg.Rect.W, bg.Rect.H, bg.Radius, bg.Color)
	}
	for _, bg := range layout.Rects {
		if bg.BackgroundFill || bg.Shadow || bg.Lifted {
			// Both are painted from layout.Backgrounds, in CSS order; a
			// lifted one with its positioned element.
			continue
		}
		y := rect.Y + bg.Rect.Y - offset
		if y+bg.Rect.H <= rect.Y || y >= rect.Y+rect.H {
			continue
		}
		if bg.Stroke {
			thickness := bg.Thickness
			if thickness < 1 {
				thickness = 1
			}
			if segs := webBorderPaintRects(bg.Rect, thickness, bg.BorderStyle, true); len(segs) > 0 {
				for _, seg := range segs {
					webPaintFill(r, rect.X+seg.X, y+seg.Y, seg.W, seg.H, 0, bg.Color)
				}
				continue
			}
			webPaintStroke(r, rect.X+bg.Rect.X, y, bg.Rect.W, bg.Rect.H, webRectCorner(bg, 3), thickness, bg.Color)
			continue
		}
		if segs := webBorderPaintRects(bg.Rect, bg.Thickness, bg.BorderStyle, false); len(segs) > 0 {
			for _, seg := range segs {
				webPaintFill(r, rect.X+seg.X, y+seg.Y, seg.W, seg.H, 0, bg.Color)
			}
			continue
		}
		if bg.HasGradient {
			drawWebGradient(r, sdl.Rect{X: rect.X + bg.Rect.X, Y: y, W: bg.Rect.W, H: bg.Rect.H}, webRectCorner(bg, 2), bg.Gradient)
			continue
		}
		webPaintFill(r, rect.X+bg.Rect.X, y, bg.Rect.W, bg.Rect.H, webRectCorner(bg, 2), bg.Color)
	}
	drawWebLayerPhase(r, config, doc, layout, rect, offset, webLayerUnderText)
	drawWebLayerPhase(r, config, doc, layout, rect, offset, webLayerFloat)
	// Images draw on their own reserved rows, before the text, so a page
	// with a header image scrolls the photo away like a normal line.
	for i := range layout.Images {
		im := layout.Images[i]
		if im.Invisible || im.Lifted {
			continue
		}
		y := rect.Y + im.Rect.Y - offset
		if im.Rect.H <= 0 || y >= rect.Y+rect.H || y+im.Rect.H <= rect.Y {
			continue
		}
		restore := webClipPaint(r, rect, im.Clip, im.HasClip, rect.X, rect.Y-offset)
		drawWebImage(r, config, doc, im, rect.X+im.Rect.X, y)
		restore()
	}
	// Video play cards come after the images on the same terms: they
	// own their reserved rows and draw before the text rows.
	for i := range layout.Videos {
		v := layout.Videos[i]
		if v.Invisible {
			continue
		}
		y := rect.Y + v.Rect.Y - offset
		if v.Rect.H <= 0 || y >= rect.Y+rect.H || y+v.Rect.H <= rect.Y {
			continue
		}
		restore := webClipPaint(r, rect, v.Clip, v.HasClip, rect.X, rect.Y-offset)
		drawWebVideo(r, config, doc, v, rect.X+v.Rect.X, y)
		restore()
	}
	start := int(offset / layout.RowH)
	if part == webPaintScrolling {
		// A tile's top edge cuts through lines: a heading's glyphs reach
		// below the row they are laid out in.
		start -= webTileRowLead
	}
	for i := max(start, 0); i < len(layout.Rows); i++ {
		y := rect.Y + layout.Rows[i].Y - offset
		if y >= rect.Y+rect.H {
			break
		}
		// The row's words joined into stretches that share their paint,
		// measured once (webpage_paintfast.go): inline background chips
		// first, then the text, shadows and decorations.
		drawWebPaintRow(r, config, layout.paintRuns(i), rect.X, y, rect, offset)
	}
	if part == webPaintAll {
		drawWebTopLayers(r, config, doc, layout, rect, offset)
	} else {
		drawWebTopLayersPart(r, config, doc, layout, rect, offset, false)
	}
}

// webDocumentRows returns the row count for a styled page, or 0 when the page
// has no styled document (so callers can fall back to plain text).
func webDocumentRows(doc *webDocument, config *Config, elem Element, width int32) (*webLayout, int) {
	if doc == nil {
		return nil, 0
	}
	layout := doc.layoutFor(config, elem, width)
	if layout == nil || len(layout.Rows) == 0 {
		return nil, 0
	}
	return layout, len(layout.Rows)
}
