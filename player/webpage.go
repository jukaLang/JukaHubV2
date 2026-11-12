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
	"fmt"
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
	webMaxTextBytes = 384 << 10
	// webMaxBlocks bounds the block list so a pathological page cannot make
	// the layout take seconds on the handheld.
	webMaxBlocks = 1800
	// webMaxStylesheets and webSheetByteCap bound external CSS fetching: a
	// handful of small sheets is what makes real sites look like themselves.
	webMaxStylesheets = 3
	webSheetByteCap   = 256 << 10
	webMaxCSSBytes    = 1 << 20
	// webMaxRows bounds the laid-out row count (each row is one text line).
	webMaxRows = 12000
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
	webJustifyStart = iota
	webJustifyCenter
	webJustifyEnd
	webJustifyBetween
	webJustifyAround
)

// webStyle is a resolved style for one element. Zero values are meaningful:
// HasColor/HasBg distinguish "author asked for this" from "inherit".
type webStyle struct {
	Color      sdl.Color
	HasColor   bool
	Background sdl.Color
	HasBg      bool
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
	Pre         bool
	Hidden      bool
	Indent      float64
	TextIndent  float64
	MarginTop   float64
	MarginBot   float64
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
	// BorderWidth is the widest declared border in CSS px; 0 keeps the
	// reader's own default (3 for a side bar, 2 for a rule or an outline).
	// A card written as `border: 3px` used to draw 2px, which reads as a
	// different design rather than as a smaller border.
	BorderWidth float64
	ListNone    bool
	// ListStyle is the marker the author asked for (0 = the reader default).
	// It inherits like a real browser's, so `ul { list-style: none }` reaches
	// the items, which is how nav menus hide their bullets.
	ListStyle int
	// LineHeight is the author's line-height in CSS px; 0 leaves the reader's
	// row grid in charge. It inherits, so `body { line-height: 1.6 }` spaces
	// every paragraph the way the design intended.
	LineHeight float64
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
	// --- Flex and stacking -------------------------------------------------
	// Display is block or flex. A flex container lays its direct children out
	// side by side (row) or one under the other (column); the reader has no
	// DOM, so the parser marks each direct child as one flex item and the
	// layout stacks those items into columns.
	Display    int
	FlexDir    int
	Justify    int
	AlignItems int
	GapX, GapY float64
	FlexWrap   bool
	GridCols   int
	// GridMin is the minimum track width from a responsive
	// repeat(auto-fit|auto-fill, minmax(Npx, 1fr)); 0 means the track count
	// is explicit (or not a grid).
	GridMin float64
	// FlexGrow/Basis describe the element as a flex item. Basis is -1 when the
	// author did not state one (auto), which is different from 0px.
	FlexGrow  float64
	FlexBasis float64
	BasisPct  float64
	// Order is the flex/grid `order` value; stable sorting keeps source order
	// for equal values, as a browser does.
	Order     int
	AlignSelf int
	// Width/WidthPct size a flex item explicitly, the way a card with
	// `width: 33%` or `width: 240px` does.
	Width    float64
	WidthPct float64
	Height   float64
	// AspectRatio is a CSS width/height ratio (16/9), used by replaced
	// elements when the stylesheet supplies it.
	AspectRatio float64
	// ContainerType marks a CSS containment container: 0 normal, 1
	// inline-size and 2 size. The parser uses the nearest one to resolve
	// container-relative units in an image sizes attribute and to evaluate
	// @container size queries. ContainerName is the optional query name.
	ContainerType int
	ContainerName string
	// ZIndex and Position place an element in the stacking order. Absolute and
	// fixed elements leave the flow and are painted over the text in z order.
	ZIndex    int
	Position  int
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
	// Vars is the custom-property scope in force for this element. Every
	// var() is already resolved by the time a style reaches a block, so this
	// only carries inheritance down the parse; it stays a pointer so a
	// webStyle is still comparable and adjacent runs still merge.
	Vars *webVarScope
	// CQ keeps the declarations that used container-relative units, so layout
	// can apply them again once a measured container size is known. It is a
	// pointer for the same reason as Vars: styles stay comparable.
	CQ *webCQContext
}

// webCQContext is the unresolved half of one element's style: the cascaded
// declarations before var()/cq* substitution, the style they cascade onto,
// and the custom properties in force. The streaming parser resolves a first
// approximation against its modelled container; layout replays this context
// when it has the container's real used size.
type webCQContext struct {
	base  webStyle
	decls map[string]string
	vars  *webVarScope
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
}

// webBlock is one laid-out unit: a paragraph, heading, list item, quote, code
// block or horizontal rule.
type webBlock struct {
	Runs  []webRun
	Style webStyle
	// ContainerIDs lists the CSS containment containers enclosing this block,
	// outermost first. It is the parse-time element ancestry, kept separately
	// from the visual style so layout can find the nearest measured size.
	ContainerIDs   []int
	Align          int
	Indent         float64
	TextIndent     float64
	MarginTop      float64
	MarginBot      float64
	Bullet         string
	Pre            bool
	HasBg          bool
	Background     sdl.Color
	Gradient       webGradient
	HasGradient    bool
	BorderLeft     bool
	BorderRight    bool
	BoxBorder      bool
	BorderTop      bool
	BorderBottom   bool
	BorderColor    sdl.Color
	HasBorderColor bool
	BorderWidth    float64
	Rule           bool
	// FlexGroup/FlexItem tie a block to its flex container and direct child.
	// Zero means "not in a flex container". The container's own row settings
	// are copied onto every item so a sub-layout can be built without the
	// parser's element stack.
	FlexGroup   int
	FlexItem    int
	FlexDir     int
	FlexJustify int
	FlexAlign   int
	FlexGapX    float64
	FlexGapY    float64
	FlexWrap    bool
	GridCols    int
	GridMin     float64
	// FlexTrail holds nested container memberships after the outermost one.
	// Each sub-layout consumes one entry, so an inner row stays inside its
	// parent card instead of being measured across the full page width.
	FlexTrail []webFlexMembership
	FlexGrow  float64
	FlexBasis float64
	BasisPct  float64
	Order     int
	Width     float64
	WidthPct  float64
	AlignSelf int
	// Float marks an absolutely or fixed-positioned block: it is measured on
	// its own and painted over the flow in ZIndex order.
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
	// InMain marks text inside <main>/<article>. When a page has any, the
	// reader shows that content instead of the site's navigation furniture.
	InMain bool
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
}

// webFlexMembership describes one level of a block's container ancestry.
// The first level lives in webBlock's regular Flex fields so existing layout
// paths stay cheap; only nested flex/grid descendants allocate a trail.
type webFlexMembership struct {
	group, item, dir, justify, align int
	gapX, gapY                       float64
	wrap                             bool
	gridCols                         int
	gridMin                          float64
	grow, basis, basisPct            float64
	order                            int
	width, widthPct                  float64
	alignSelf                        int
}

func webFlexMembershipFromFrame(f *webFlexFrame) webFlexMembership {
	return webFlexMembership{
		group: f.group, item: f.item, dir: f.dir, justify: f.justify, align: f.align,
		gapX: f.gapX, gapY: f.gapY, wrap: f.wrap, gridCols: f.gridCols, gridMin: f.gridMin,
		grow: f.itemStyle.FlexGrow, basis: f.itemStyle.FlexBasis, basisPct: f.itemStyle.BasisPct,
		order: f.itemStyle.Order, width: f.itemStyle.Width, widthPct: f.itemStyle.WidthPct, alignSelf: f.itemStyle.AlignSelf,
	}
}

func (b *webBlock) setFlexMembership(m webFlexMembership) {
	b.FlexGroup, b.FlexItem = m.group, m.item
	b.FlexDir, b.FlexJustify, b.FlexAlign = m.dir, m.justify, m.align
	b.FlexGapX, b.FlexGapY, b.FlexWrap, b.GridCols, b.GridMin = m.gapX, m.gapY, m.wrap, m.gridCols, m.gridMin
	b.FlexGrow, b.FlexBasis, b.BasisPct = m.grow, m.basis, m.basisPct
	b.Order = m.order
	b.Width, b.WidthPct, b.AlignSelf = m.width, m.widthPct, m.alignSelf
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
	Containers        map[int]webContainerInfo
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
	stamp  string
	layout *webLayout

	// imageMu guards Images and ImageMeta separately from mu. They need their
	// own lock because the layouter reads the natural sizes while layoutFor
	// holds mu: sharing one mutex deadlocked the reader on the first page with
	// an image whose size the markup did not give, which froze the whole app.
	imageMu sync.Mutex
}

// webRunPos is a measured run, ready to draw.
type webRunPos struct {
	Text      string
	X         int32
	Font      *ttf.Font
	Color     sdl.Color
	Bold      bool
	Underline bool
	Strike    bool
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
	// Gradient paints this rect as a ramp instead of a flat fill; Color is then
	// the average, used for the corner rounding and as the fallback.
	Gradient    webGradient
	HasGradient bool
	// Radius is the author's border-radius in design pixels, clamped to half
	// the shorter side when drawn. 0 keeps the reader's crisp corner.
	Radius int32
	// Z orders the fills and borders of the page. Text is always painted after
	// every rect, so z-index decides which background or border wins where
	// two elements overlap.
	Z int
}

// webFloat is an absolutely positioned block: it is laid out at its own width,
// then painted over the flow in z order. A page's badge, sticky header or modal
// is exactly this.
type webFloat struct {
	Z      int
	X, Y   int32
	Rows   []webRow
	Rects  []webRect
	Images []webImagePos
}

// webLayout is the measured page for one width and font configuration.
type webLayout struct {
	Rows   []webRow
	Rects  []webRect
	RowH   int32
	Height int32
	// ContainerSizes holds the used border-box size of each containment
	// container, in CSS px, measured by this layout. Container units in the
	// next pass resolve against these numbers.
	ContainerSizes map[int]webContainerSize
	// Links are the page's clickable runs in reading order, one entry per
	// destination. The browser walks this list with the D-pad.
	Links []webLinkHit
	// Floats are the absolutely positioned blocks, painted over the rows in
	// ascending z order.
	Floats []webFloat
	// Images are the reader's <img> boxes, in layout-local coordinates like
	// Rects. They draw after the icon rects and before the text rows.
	Images []webImagePos
	// Videos are the reader's <video>/embed play cards, in the same layout-
	// local coordinates. They draw right after the images, before the text.
	Videos []webVideoPos
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
}

// webForm is the <form> a control belongs to: where a submitted value goes,
// and the field name it submits under. Field is the first text input the form
// declared, so a submit button knows which name to fill when nothing else does.
//
// ID tells two forms apart. A page can carry two search boxes with the same
// action (or none at all, which resolves to the page's own address), and
// matching on the action alone sent both forms' fields on one press.
type webForm struct {
	ID     int
	Action string
	Field  string
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
		label := strings.TrimSpace(c.Value)
		if label == "" {
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
	decls       map[string]string
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

// webFrame is an open element during the streaming parse.
type webFrame struct {
	el    webElement
	style webStyle
	kind  string // "block", "inline", "list", "skip"
	// flexGroup is non-zero when this element opened a flex container. The
	// matching frame is popped when the element closes.
	flexGroup int
	// containerID is non-zero when this element is a CSS containment
	// container. Descendant blocks copy the open IDs so layout can find them.
	containerID int
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
	group     int
	item      int
	depth     int
	itemStyle webStyle
	dir       int
	justify   int
	align     int
	gapX      float64
	gapY      float64
	wrap      bool
	gridCols  int
	gridMin   float64
}

// webListState tracks <ul>/<ol> numbering.
type webListState struct {
	ordered bool
	n       int
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

// webMediaViewport is the size media queries are judged against: the screen the
// reader draws on, which is also what page scripts get as innerWidth and
// innerHeight (webdom_runtime.go's configJSON), so a script's matchMedia and
// the sheet agree.
func webMediaViewport() (float64, float64) {
	w, h := float64(screenWidth), float64(screenHeight)
	if w <= 0 || h <= 0 {
		w, h = 1280, 720
	}
	return w, h
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
		name := strings.TrimSpace(inner[:colon])
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
	return true
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
	}
	if rest == "" || !strings.HasPrefix(rest, "(") {
		return nil
	}
	return &webContainerQuery{name: name, condition: strings.ToLower(rest)}
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
	idx := p.webNearestContainer(query)
	if idx < 0 {
		return false
	}
	w, h := p.webContainerDimensions(idx)
	return webContainerCondition(webMediaTokens(query.condition), w, h)
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
	if inner == "" || strings.HasPrefix(inner, "style(") || strings.HasPrefix(inner, "scroll-state(") {
		return false
	}
	if words := webMediaTokens(inner); len(words) > 1 || strings.HasPrefix(inner, "(") {
		if words[0] == "not" || strings.HasPrefix(words[0], "(") {
			return webContainerCondition(words, w, h)
		}
	}
	return webContainerFeature(inner, w, h)
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
// open container stack at match time), and @layer (the cascade order is
// approximated, but the rules are the site's real styling).
func parseWebCSS(src string) []webRule {
	rules := make([]webRule, 0, 32)
	order := 0
	parseWebCSSInto(stripWebCSSComments(src), 0, &order, &rules, nil)
	return rules
}

const (
	// webCSSMaxNesting bounds how deep conditional group rules are followed.
	// Each level re-scanned its whole body, so a sheet of nothing but
	// "@media a{" (58,000 levels in the 512 KB a page may load) cost
	// quadratic time and gigabytes of copies before the page appeared.
	webCSSMaxNesting = 8
	// webCSSMaxRules bounds the rules one page keeps; a large site uses a few
	// thousand.
	webCSSMaxRules = 16000
)

// parseWebCSSInto appends the rules of one (comment-free) block to rules.
func parseWebCSSInto(src string, depth int, order *int, rules *[]webRule, containers []*webContainerQuery) {
	for i := 0; i < len(src) && len(*rules) < webCSSMaxRules; {
		open := strings.IndexByte(src[i:], '{')
		if open < 0 {
			break
		}
		open += i
		selectorText := webSkipCSSStatementRules(strings.TrimSpace(src[i:open]))
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
			if strings.HasPrefix(lower, "@container") {
				query := webParseContainerQuery(selectorText)
				if query != nil && depth < webCSSMaxNesting {
					next := append(append([]*webContainerQuery(nil), containers...), query)
					parseWebCSSInto(body, depth+1, order, rules, next)
				}
				continue
			}
			keep := strings.HasPrefix(lower, "@layer")
			if strings.HasPrefix(lower, "@supports") {
				keep = webSupportsApplies(selectorText)
			}
			if strings.HasPrefix(lower, "@media") {
				keep = webMediaBlockApplies(selectorText)
			}
			// A conditional group rule nests: `@media` inside `@layer` still
			// has to be judged on its own query.
			if keep && depth < webCSSMaxNesting {
				parseWebCSSInto(body, depth+1, order, rules, containers)
			}
			continue
		}
		decls, important := parseWebDeclsSplit(body)
		if len(decls) == 0 && len(important) == 0 {
			continue
		}
		hides := webDeclsHide(decls) || webDeclsHide(important)
		reveals := webDeclsReveal(decls) || webDeclsReveal(important)
		// Split on top-level commas only: `:is(.a, .b)` and `[title="a,b"]`
		// are one selector each, and a plain Split cut them into broken halves.
		for _, selector := range webSplitSelectorList(selectorText) {
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
				order:       *order,
				decls:       decls,
				important:   important,
				sel:         sel,
				hides:       hides,
				reveals:     reveals,
				containers:  containers,
			})
			*order++
		}
	}
}

// webSupportedProperties is the CSS surface this reader implements. It is
// deliberately a declaration whitelist rather than a blanket "modern browser"
// answer: @supports exists to select fallbacks, and claiming a property we
// ignore would make the fallback styling win over the one that actually works.
var webSupportedProperties = map[string]bool{
	"align-items": true, "align-self": true, "aspect-ratio": true,
	"background": true, "background-color": true, "background-image": true,
	"border": true, "border-bottom": true, "border-color": true,
	"border-left": true, "border-radius": true, "border-right": true,
	"border-top": true, "border-width": true, "color": true,
	"column-gap": true, "container": true, "container-name": true, "container-type": true,
	"display": true, "flex": true, "flex-basis": true, "flex-direction": true,
	"flex-grow": true, "flex-wrap": true, "font": true, "font-size": true,
	"font-style": true, "font-weight": true, "gap": true,
	"grid-template-columns": true, "height": true, "inset": true,
	"justify-content": true, "line-height": true, "margin": true,
	"margin-block": true, "margin-bottom": true, "margin-left": true,
	"margin-right": true, "margin-top": true, "opacity": true,
	"order": true, "padding": true, "padding-block": true,
	"padding-bottom": true, "padding-left": true, "padding-right": true,
	"padding-top": true, "place-items": true, "position": true,
	"row-gap": true, "text-align": true, "text-indent": true,
	"text-transform": true, "visibility": true, "white-space": true,
	"width": true, "z-index": true,
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
	if !webSupportedProperties[name] {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(value))
	// Keywords that mean "the engine's initial value" do not necessarily
	// change a probe style, so validate them by property group instead.
	switch lower {
	case "inherit", "initial", "unset", "revert", "revert-layer":
		return true
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

// webSupportsParsedValue checks the properties whose parsers accept a broad
// value space and therefore need their own grammar test. The bool result is
// whether the declaration is supported; handled reports that the property was
// answered here and should not fall through to the probe.
func webSupportsParsedValue(name, value string) (supported, handled bool) {
	lower := strings.ToLower(strings.TrimSpace(value))
	switch name {
	case "display":
		return webSupportsEnum(lower, "block", "inline", "inline-block", "flex", "inline-flex", "grid", "inline-grid", "flow-root", "none"), true
	case "position":
		return webSupportsEnum(lower, "static", "relative", "absolute", "fixed", "sticky"), true
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
	case "flex-direction":
		return webSupportsEnum(lower, "row", "row-reverse", "column", "column-reverse"), true
	case "flex-wrap":
		return webSupportsEnum(lower, "nowrap", "wrap", "wrap-reverse"), true
	case "justify-content":
		return webSupportsEnum(lower, "start", "end", "left", "right", "center", "flex-start", "flex-end", "space-between", "space-around", "space-evenly"), true
	case "align-items", "align-self":
		return webSupportsEnum(lower, "start", "end", "center", "stretch", "baseline", "flex-start", "flex-end"), true
	case "place-items":
		fields := webCSSSpaceFields(lower)
		if len(fields) == 0 || len(fields) > 2 {
			return false, true
		}
		for _, field := range fields {
			if !webSupportsEnum(field, "start", "end", "center", "stretch", "baseline", "flex-start", "flex-end") {
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
	case "grid-template-columns":
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
	if value == "none" {
		return true
	}
	if _, ok := webColorInValue(value); ok {
		return true
	}
	_, ok := webParseGradient(value)
	return ok
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
// @namespace, which end at a semicolon instead of a block — from the text in
// front of a block. They used to be read as part of the next selector, so
// `@charset "utf-8"; body {…}` was taken for an at-rule block and the first
// real rule of nearly every stylesheet was thrown away.
func webSkipCSSStatementRules(text string) string {
	for strings.HasPrefix(text, "@") {
		lower := asciiLower(text)
		if !strings.HasPrefix(lower, "@charset") && !strings.HasPrefix(lower, "@import") &&
			!strings.HasPrefix(lower, "@namespace") {
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
	value = webSubstituteVars(value, scope)
	if strings.Contains(strings.ToLower(value), "calc(") {
		value = webSubstituteCalc(value, em)
	}
	return value
}

// webSubstituteCalc replaces each calc() it can evaluate with a px length. A
// calc() that mixes in a unit the reader cannot resolve yet (`100% - 20px`)
// is left as authored.
func webSubstituteCalc(value string, em float64) string {
	return webSubstituteCalcContainer(value, em, 0, 0, 0, 0)
}

// webSubstituteCalcContainer is webSubstituteCalc with query-container
// dimensions available for cq* operands. Ordinary CSS values reach this after
// their bare container units have already been substituted.
func webSubstituteCalcContainer(value string, em, vw, vh, cw, ch float64) string {
	if em <= 0 {
		em = 16
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
	value = webSubstituteContainerUnits(value, cw, ch)
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
	st.Href, st.Link, st.Hidden, st.Control = current.Href, current.Link, current.Hidden, current.Control
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
	p := &webCalcParser{src: expr, em: em, vw: vw, vh: vh, cw: cw, ch: ch}
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
	case "vw":
		if p.vw > 0 {
			return webCalcValue{px: number * p.vw / 100}, true
		}
	case "vh":
		if p.vh > 0 {
			return webCalcValue{px: number * p.vh / 100}, true
		}
	case "vmin":
		if p.vw > 0 && p.vh > 0 {
			return webCalcValue{px: number * min(p.vw, p.vh) / 100}, true
		}
	case "vmax":
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
		return webCalcValue{}, false, false
	}
	open := p.pos + len(name)
	close := webCSSParenEnd(p.src, open)
	if close < 0 {
		return webCalcValue{}, false, true
	}
	inner := p.src[open+1 : close]
	eval := func(expr string) (float64, bool) {
		return webEvalCalcContainer(expr, p.em, p.vw, p.vh, p.cw, p.ch)
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

// --- CSS selectors --------------------------------------------------------
//
// Selectors are compiled once, when a sheet is parsed, and matched against the
// chain of open elements: outermost first, the element being styled last. The
// streaming parser keeps no DOM, so a few selectors cannot be decided from a
// start tag — those that depend on a later sibling (:last-child), on the
// element's content (:empty, :has()) or on the previous sibling (+, ~). They
// answer "maybe".
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
	// never marks a selector that cannot match an element: one aimed at a
	// pseudo-element (its declarations style a generated box such as ::before,
	// never the element) or one that does not parse, which a browser drops too.
	never bool
}

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
	// words are the language ranges of :lang(); args are the selectors of
	// :not(), :is() and :where(); hasArgs are the relative selectors of :has().
	words   []string
	args    []*webSelector
	hasArgs []webHasArg
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
// cannot decide from a start tag (:nth-child(), :last-child, :has(), :valid…);
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
		// The declarations style a generated box (::before, ::marker, a
		// placeholder, a scrollbar), never the element itself.
		p.sel.never = true
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

// webMaxSelectorTreeNodes bounds the structural copy built for :has(). A page
// larger than this simply leaves :has() undecidable rather than spending the
// handheld's memory on a second DOM.
const webMaxSelectorTreeNodes = 8192

// webRulesNeedSelectorTree reports whether any rule has a :has() pseudo-class,
// including one nested inside :is(), :not() or :where().
func webRulesNeedSelectorTree(rules []webRule) bool {
	for i := range rules {
		if webSelectorUsesHas(rules[i].sel) {
			return true
		}
	}
	return false
}

func webSelectorUsesHas(sel *webSelector) bool {
	if sel == nil {
		return false
	}
	for i := range sel.compounds {
		for j := range sel.compounds[i].simples {
			s := &sel.compounds[i].simples[j]
			if s.op == webPseudoHas && s.kind == webSelPseudo {
				return true
			}
			for _, arg := range s.args {
				if webSelectorUsesHas(arg) {
					return true
				}
			}
			for _, arg := range s.hasArgs {
				if webSelectorUsesHas(arg.sel) {
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

var webNamedColors = map[string]sdl.Color{
	"black":       {R: 0, G: 0, B: 0, A: 255},
	"white":       {R: 255, G: 255, B: 255, A: 255},
	"red":         {R: 220, G: 50, B: 47, A: 255},
	"green":       {R: 40, G: 140, B: 70, A: 255},
	"blue":        {R: 30, G: 90, B: 200, A: 255},
	"yellow":      {R: 240, G: 200, B: 0, A: 255},
	"orange":      {R: 235, G: 130, B: 30, A: 255},
	"purple":      {R: 120, G: 60, B: 160, A: 255},
	"gray":        {R: 128, G: 128, B: 128, A: 255},
	"grey":        {R: 128, G: 128, B: 128, A: 255},
	"silver":      {R: 192, G: 192, B: 192, A: 255},
	"maroon":      {R: 128, G: 0, B: 0, A: 255},
	"olive":       {R: 128, G: 128, B: 0, A: 255},
	"lime":        {R: 0, G: 200, B: 0, A: 255},
	"aqua":        {R: 0, G: 180, B: 180, A: 255},
	"cyan":        {R: 0, G: 180, B: 180, A: 255},
	"teal":        {R: 0, G: 128, B: 128, A: 255},
	"navy":        {R: 0, G: 0, B: 128, A: 255},
	"fuchsia":     {R: 200, G: 40, B: 190, A: 255},
	"magenta":     {R: 200, G: 40, B: 190, A: 255},
	"brown":       {R: 140, G: 80, B: 45, A: 255},
	"pink":        {R: 235, G: 140, B: 175, A: 255},
	"gold":        {R: 215, G: 175, B: 45, A: 255},
	"beige":       {R: 245, G: 245, B: 220, A: 255},
	"ivory":       {R: 255, G: 255, B: 240, A: 255},
	"khaki":       {R: 240, G: 230, B: 140, A: 255},
	"coral":       {R: 255, G: 127, B: 80, A: 255},
	"crimson":     {R: 220, G: 20, B: 60, A: 255},
	"indigo":      {R: 75, G: 0, B: 130, A: 255},
	"violet":      {R: 200, G: 130, B: 220, A: 255},
	"turquoise":   {R: 64, G: 200, B: 190, A: 255},
	"salmon":      {R: 250, G: 128, B: 114, A: 255},
	"tomato":      {R: 255, G: 99, B: 71, A: 255},
	"steelblue":   {R: 70, G: 130, B: 180, A: 255},
	"darkgray":    {R: 90, G: 90, B: 90, A: 255},
	"darkgrey":    {R: 90, G: 90, B: 90, A: 255},
	"lightgray":   {R: 211, G: 211, B: 211, A: 255},
	"lightgrey":   {R: 211, G: 211, B: 211, A: 255},
	"whitesmoke":  {R: 245, G: 245, B: 245, A: 255},
	"gainsboro":   {R: 220, G: 220, B: 220, A: 255},
	"dimgray":     {R: 105, G: 105, B: 105, A: 255},
	"transparent": {R: 0, G: 0, B: 0, A: 0},
}

// parseWebColor handles hex, rgb()/rgba() and the common named colours. Alpha
// is ignored: the page background decides what is readable here.
func parseWebColor(value string) (sdl.Color, bool) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return sdl.Color{}, false
	}
	if strings.HasPrefix(v, "#") {
		hex := v[1:]
		switch len(hex) {
		case 3, 4:
			r, ok1 := webHexByte(string([]byte{hex[0], hex[0]}))
			g, ok2 := webHexByte(string([]byte{hex[1], hex[1]}))
			b, ok3 := webHexByte(string([]byte{hex[2], hex[2]}))
			if ok1 && ok2 && ok3 {
				return sdl.Color{R: r, G: g, B: b, A: 255}, true
			}
		case 6, 8:
			r, ok1 := webHexByte(hex[0:2])
			g, ok2 := webHexByte(hex[2:4])
			b, ok3 := webHexByte(hex[4:6])
			if ok1 && ok2 && ok3 {
				return sdl.Color{R: r, G: g, B: b, A: 255}, true
			}
		}
		return sdl.Color{}, false
	}
	if strings.HasPrefix(v, "rgb") {
		open := strings.IndexByte(v, '(')
		close := strings.LastIndexByte(v, ')')
		if open < 0 || close <= open {
			return sdl.Color{}, false
		}
		parts := strings.FieldsFunc(v[open+1:close], func(r rune) bool { return r == ',' || r == ' ' || r == '/' })
		if len(parts) < 3 {
			return sdl.Color{}, false
		}
		vals := make([]uint8, 3)
		for i := 0; i < 3; i++ {
			part := strings.TrimSpace(parts[i])
			percent := strings.HasSuffix(part, "%")
			part = strings.TrimSuffix(part, "%")
			n, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
			if err != nil {
				return sdl.Color{}, false
			}
			if percent {
				n = n * 255 / 100
			}
			n = math.Round(n)
			if n < 0 {
				n = 0
			}
			if n > 255 {
				n = 255
			}
			vals[i] = uint8(n)
		}
		return sdl.Color{R: vals[0], G: vals[1], B: vals[2], A: 255}, true
	}
	if c, ok := webNamedColors[v]; ok {
		if c.A == 0 {
			return sdl.Color{}, false
		}
		return c, true
	}
	return sdl.Color{}, false
}

func webHexByte(text string) (uint8, bool) {
	n, err := strconv.ParseUint(text, 16, 8)
	if err != nil {
		return 0, false
	}
	return uint8(n), true
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
		// Viewport units describe the page's own window; the reader has no
		// such window, so they must not turn into indentation.
		return 0
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0
	}
	return n * mult
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
	case "medium":
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

func webTextAlign(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "center":
		return 1
	case "right", "end":
		return 2
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
// applied: the family is ignored because the app ships one face, and a system
// font keyword (caption, menu, ...) carries no explicit size to read.
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
	for _, field := range fields {
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

// webColorInValue finds the colour in a shorthand such as
// `background: #123 url(x.png) no-repeat` or `1px solid rgba(0,0,0,.2)`.
func webColorInValue(value string) (sdl.Color, bool) {
	if c, ok := parseWebColor(value); ok {
		return c, true
	}
	lower := strings.ToLower(value)
	if idx := strings.Index(lower, "rgb"); idx >= 0 {
		if open := strings.IndexByte(lower[idx:], '('); open >= 0 {
			if end := strings.IndexByte(lower[idx+open:], ')'); end >= 0 {
				if c, ok := parseWebColor(lower[idx : idx+open+end+1]); ok {
					return c, true
				}
			}
		}
	}
	for _, field := range strings.FieldsFunc(value, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ',' || r == '/'
	}) {
		if c, ok := parseWebColor(field); ok {
			return c, true
		}
	}
	return sdl.Color{}, false
}

// --- Style resolution -----------------------------------------------------

func webBaseStyle() webStyle {
	return webStyle{FontPx: 16}
}

// applyWebTagDefaults gives the reader its browser-ish baseline: headings are
// large and bold, paragraphs are spaced, lists are indented, quotes carry a
// rule and code is monospaced-ish on a tinted panel.
func applyWebTagDefaults(st *webStyle, tag string) {
	switch tag {
	case "h1":
		st.FontPx, st.Bold = 30, true
		st.MarginTop, st.MarginBot = 20, 10
	case "h2":
		st.FontPx, st.Bold = 25, true
		st.MarginTop, st.MarginBot = 18, 8
	case "h3":
		st.FontPx, st.Bold = 21, true
		st.MarginTop, st.MarginBot = 16, 6
	case "h4", "h5", "h6":
		st.FontPx, st.Bold = 18, true
		st.MarginTop, st.MarginBot = 12, 4
	case "p":
		// Browsers give a paragraph one em above and below; without it the
		// rows of two paragraphs merge into one undifferentiated wall.
		st.MarginTop, st.MarginBot = 16, 16
	case "blockquote":
		st.Indent += 16
		st.MarginTop, st.MarginBot = 16, 16
		st.BorderLeft = true
	case "pre":
		st.Pre = true
		st.FontPx = 13
		st.MarginTop, st.MarginBot = 16, 16
		st.HasBg = true
		st.Background = sdl.Color{R: 238, G: 241, B: 246, A: 255}
	case "code", "kbd", "samp":
		st.FontPx = 13
		st.HasBg = true
		st.Background = sdl.Color{R: 238, G: 241, B: 246, A: 255}
	case "li":
		st.Indent += 18
		st.MarginTop, st.MarginBot = 2, 2
	case "a":
		st.Underline = true
		st.Link = true
	case "strong", "b":
		st.Bold = true
	case "em", "i":
		st.Italic = true
	case "s", "del", "strike":
		st.Strike = true
	case "u", "ins":
		st.Underline = true
	case "center":
		st.Align = 1
	case "dt":
		// Definition terms are bold in every browser.
		st.Bold = true
	case "mark":
		st.HasBg = true
		st.Background = sdl.Color{R: 255, G: 233, B: 120, A: 255}
	case "small":
		if st.FontPx > 12 {
			st.FontPx -= 2
		}
	case "td", "th":
		st.Indent += 4
		if tag == "th" {
			st.Bold = true
		}
	}
}

func applyWebDecls(st *webStyle, decls map[string]string) {
	if len(decls) == 0 {
		return
	}
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
		if c, valid := parseWebColor(v); valid {
			st.Color, st.HasColor = c, true
		}
	}
	for _, key := range []string{"background-color", "background"} {
		if v, ok := decls[key]; ok {
			if c, valid := webColorInValue(v); valid {
				st.Background, st.HasBg = c, true
				break
			}
		}
	}
	// A linear gradient becomes a ramp the reader paints band by band, so the
	// header keeps its light and dark ends instead of one flat colour. The
	// average is still stored as the background: contrast and fade decisions
	// need a single colour. A background-image paints over background-color,
	// so declaring a fallback colour must not suppress the gradient.
	for _, key := range []string{"background-image", "background"} {
		v, ok := decls[key]
		if !ok {
			continue
		}
		if g, valid := webParseGradient(v); valid {
			ramp := g
			st.Gradient, st.HasGradient = &ramp, true
			st.Background, st.HasBg = webGradientAverage(g), true
			break
		}
		if !st.HasBg {
			if c, valid := webGradientColor(v); valid {
				st.Background, st.HasBg = c, true
				break
			}
		}
	}
	if v, ok := decls["font-weight"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "bold", "bolder", "600", "700", "800", "900":
			st.Bold = true
		case "normal", "lighter", "100", "200", "300", "400", "500":
			st.Bold = false
		}
	}
	if v, ok := decls["font-style"]; ok {
		lower := strings.ToLower(strings.TrimSpace(v))
		st.Italic = strings.HasPrefix(lower, "italic") || strings.HasPrefix(lower, "oblique")
	}
	for _, key := range []string{"text-decoration", "text-decoration-line"} {
		if v, ok := decls[key]; ok {
			lower := strings.ToLower(v)
			st.Underline = strings.Contains(lower, "underline")
			st.Strike = strings.Contains(lower, "line-through")
			break
		}
	}
	if v, ok := decls["text-transform"]; ok {
		st.Transform = webTextTransform(v)
	}
	if v, ok := decls["text-align"]; ok {
		st.Align = webTextAlign(v)
	}
	if v, ok := decls["text-indent"]; ok {
		st.TextIndent = webLength(v, st.FontPx)
	}
	if v, ok := decls["white-space"]; ok {
		st.Pre = strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "pre")
	}
	for _, key := range []string{"display", "visibility"} {
		if v, ok := decls[key]; ok {
			lower := strings.ToLower(strings.TrimSpace(v))
			if lower == "none" || lower == "hidden" {
				st.Hidden = true
			}
		}
	}
	if v, ok := decls["margin"]; ok {
		if top, right, bottom, left, valid := webBoxShorthand(v, st.FontPx); valid {
			st.MarginTop, st.MarginBot = top, bottom
			st.Indent += left
			st.IndentRight += right
		} else {
			top, bottom := webMarginPair(v, st.FontPx)
			st.MarginTop, st.MarginBot = top, bottom
		}
	}
	if v, ok := decls["margin-top"]; ok {
		st.MarginTop = webLength(v, st.FontPx)
	}
	if v, ok := decls["margin-bottom"]; ok {
		st.MarginBot = webLength(v, st.FontPx)
	}
	if v, ok := decls["padding-left"]; ok {
		st.Indent += webLength(v, st.FontPx)
	}
	// A block's own top and bottom padding is breathing room, exactly like a
	// margin: without it a card written as `padding-top: 24px` sat flush
	// against the paragraph above it.
	if v, ok := decls["padding-top"]; ok {
		st.MarginTop += webLength(v, st.FontPx)
	}
	if v, ok := decls["padding-bottom"]; ok {
		st.MarginBot += webLength(v, st.FontPx)
	}
	// The logical shorthands every modern sheet uses. The reader is a
	// left-to-right column, so inline = left/right and block = top/bottom;
	// supporting them keeps a spacing writer's intent instead of dropping it.
	if v, ok := decls["margin-block"]; ok {
		if top, _, bottom, _, valid := webBoxShorthand(v, st.FontPx); valid {
			st.MarginTop += top
			st.MarginBot += bottom
		}
	}
	if v, ok := decls["padding-block"]; ok {
		if top, _, bottom, _, valid := webBoxShorthand(v, st.FontPx); valid {
			st.MarginTop += top
			st.MarginBot += bottom
		}
	}
	for _, key := range []string{"margin-inline", "padding-inline"} {
		if v, ok := decls[key]; ok {
			if _, right, _, left, valid := webBoxShorthand(v, st.FontPx); valid {
				st.Indent += left
				st.IndentRight += right
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
			*target += webLength(v, st.FontPx)
		}
	}
	if v, ok := decls["padding"]; ok {
		if top, right, bottom, left, valid := webBoxShorthand(v, st.FontPx); valid {
			st.Indent += left
			st.IndentRight += right
			st.MarginTop += top
			st.MarginBot += bottom
		}
	}
	if v, ok := decls["padding-right"]; ok {
		st.IndentRight += webLength(v, st.FontPx)
	}
	// margin-left/margin-right are the box the paragraph sits in: a 24px
	// gutter narrows the line on that side. `auto` reads as 0, which is the
	// honest result for a box the reader cannot measure.
	if v, ok := decls["margin-left"]; ok {
		st.Indent += webLength(v, st.FontPx)
	}
	if v, ok := decls["margin-right"]; ok {
		st.IndentRight += webLength(v, st.FontPx)
	}
	if v, ok := decls["line-height"]; ok {
		if px, valid := webLineHeightPx(v, st.FontPx); valid {
			st.LineHeight = px
		}
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
	for _, key := range []string{"border", "border-left", "border-right", "border-top", "border-bottom"} {
		if v, ok := decls[key]; ok {
			if c, valid := webColorInValue(v); valid {
				st.BorderColor, st.HasBorderColor = c, true
			}
		}
	}
	// Each edge maps to its own side: `border-right` used to set the left
	// flag, so a sidebar divider drew on the wrong edge of the box.
	if v, ok := decls["border-left"]; ok && webBorderVisible(v) {
		st.BorderLeft = true
		st.BorderWidth = webMaxAbs(st.BorderWidth, webBorderWidthInValue(v, st.FontPx))
	}
	if v, ok := decls["border-right"]; ok && webBorderVisible(v) {
		st.BorderRight = true
		st.BorderWidth = webMaxAbs(st.BorderWidth, webBorderWidthInValue(v, st.FontPx))
	}
	if v, ok := decls["border-top"]; ok && webBorderVisible(v) {
		st.BorderTop = true
		st.BorderWidth = webMaxAbs(st.BorderWidth, webBorderWidthInValue(v, st.FontPx))
	}
	if v, ok := decls["border-bottom"]; ok && webBorderVisible(v) {
		st.BorderBottom = true
		st.BorderWidth = webMaxAbs(st.BorderWidth, webBorderWidthInValue(v, st.FontPx))
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
			}
		}
	}
	if v, ok := decls["border"]; ok && webBorderVisible(v) {
		st.BoxBorder = true
		st.BorderWidth = webMaxAbs(st.BorderWidth, webBorderWidthInValue(v, st.FontPx))
	}
	if v, ok := decls["border-width"]; ok {
		if px := webLength(v, st.FontPx); px > 0 {
			st.BoxBorder = true
			st.BorderWidth = webMaxAbs(st.BorderWidth, px)
		}
	}
	for _, key := range []string{"list-style-type", "list-style"} {
		if v, ok := decls[key]; ok {
			if marker := webListStyleValue(v); marker != webListStyleDefault {
				st.ListStyle = marker
				if marker == webListStyleNone {
					st.ListNone = true
				}
				break
			}
		}
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
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "flex", "inline-flex":
			st.Display = webDisplayFlex
		case "grid", "inline-grid":
			st.Display = webDisplayGrid
			st.FlexDir, st.FlexWrap = webFlexRow, true
		case "block", "flow-root", "inline-block":
			st.Display = webDisplayBlock
		}
	}
	if v, ok := decls["grid-template-columns"]; ok && st.Display == webDisplayGrid {
		st.GridCols, st.GridMin = webGridTemplate(v)
	}
	if v, ok := decls["flex-direction"]; ok {
		lower := strings.ToLower(strings.TrimSpace(v))
		st.FlexDir = webFlexRow
		if strings.HasPrefix(lower, "column") {
			st.FlexDir = webFlexColumn
		}
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
	// place-items is `align-items justify-items`; the reader has no separate
	// inline axis for items, so its first value is the useful one here.
	if v, ok := decls["place-items"]; ok {
		if fields := webCSSSpaceFields(v); len(fields) > 0 {
			st.AlignItems = webAlignValue(fields[0])
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
	if v, ok := decls["flex-grow"]; ok {
		if grow, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && grow >= 0 {
			st.FlexGrow = grow
		}
	}
	if v, ok := decls["flex-basis"]; ok {
		webFlexBasis(st, v)
	}
	if v, ok := decls["flex"]; ok {
		webFlexShorthand(st, v)
	}
	if v, ok := decls["width"]; ok {
		if pct, valid := webPercentValue(v); valid {
			st.WidthPct = pct
		} else if px := webLength(v, st.FontPx); px > 0 {
			st.Width = px
		}
	}
	if v, ok := decls["height"]; ok {
		if px := webLength(v, st.FontPx); px > 0 {
			st.Height = px
		}
	}
	if v, ok := decls["aspect-ratio"]; ok {
		if ratio, valid := webMediaRatio(strings.TrimSpace(v)); valid {
			st.AspectRatio = ratio
		}
	}
	if v, ok := decls["position"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "relative":
			st.Position = webPositionRelative
		case "absolute", "fixed", "sticky":
			// The reader has no sticky scrolling; a fixed or sticky element
			// keeps its place on the page and is stacked like an absolute one.
			st.Position = webPositionAbsolute
		}
	}
	if v, ok := decls["z-index"]; ok {
		if z, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			st.ZIndex = z
		}
	}
	if v, ok := decls["inset"]; ok {
		if top, right, bottom, left, valid := webBoxShorthand(v, st.FontPx); valid {
			st.Top, st.Right, st.Bottom, st.Left = top, right, bottom, left
			st.HasTop, st.HasRight, st.HasBottom, st.HasLeft = true, true, true, true
		}
	}
	for _, edge := range []struct {
		key  string
		val  *float64
		flag *bool
	}{
		{"top", &st.Top, &st.HasTop},
		{"right", &st.Right, &st.HasRight},
		{"bottom", &st.Bottom, &st.HasBottom},
		{"left", &st.Left, &st.HasLeft},
	} {
		v, ok := decls[edge.key]
		if !ok {
			continue
		}
		if px := webLength(v, st.FontPx); px != 0 {
			*edge.val = px
			*edge.flag = true
		}
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
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == "none" || value == "subgrid" {
		return 0, 0
	}
	if strings.HasPrefix(value, "repeat(") {
		open := len("repeat")
		if end := webCSSParenEnd(value, open); end > open {
			parts := webSplitTopLevel(value[open+1:end], ',')
			if len(parts) >= 2 {
				count := strings.TrimSpace(parts[0])
				min := webGridTrackMin(strings.TrimSpace(parts[1]))
				if count == "auto-fit" || count == "auto-fill" {
					return 0, min
				}
				if n, err := strconv.Atoi(count); err == nil && n >= 1 && n <= 6 {
					return n, min
				}
			}
		}
		return 0, 0
	}
	tracks := len(webCSSSpaceFields(value))
	if tracks >= 1 && tracks <= 6 {
		return tracks, 0
	}
	return 0, 0
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
// `flex: 0 0 33%` forms. The shrink factor has no meaning in a one-pass text
// layout, so it is accepted and ignored.
func webFlexShorthand(st *webStyle, value string) {
	lower := strings.ToLower(strings.TrimSpace(value))
	switch lower {
	case "none":
		st.FlexGrow = 0
		st.FlexBasis = -1
		st.BasisPct = 0
		return
	case "auto":
		st.FlexGrow = 1
		st.FlexBasis = -1
		st.BasisPct = 0
		return
	case "initial":
		st.FlexGrow = 0
		st.FlexBasis = -1
		st.BasisPct = 0
		return
	}
	fields := strings.Fields(lower)
	if len(fields) == 0 {
		return
	}
	if grow, err := strconv.ParseFloat(fields[0], 64); err == nil {
		st.FlexGrow = grow
	} else if pct, ok := webPercentValue(fields[0]); ok {
		// `flex: 33%` is a basis, not a grow factor.
		st.BasisPct, st.FlexBasis = pct, -1
		return
	}
	if len(fields) > 2 {
		webFlexBasis(st, fields[2])
		return
	}
	if len(fields) == 2 {
		if pct, ok := webPercentValue(fields[1]); ok {
			st.BasisPct, st.FlexBasis = pct, -1
			if pct == 0 {
				st.FlexBasis = 0
			}
		} else if _, err := strconv.ParseFloat(fields[1], 64); err == nil {
			// A plain number in the second slot is flex-shrink, not a
			// length. For `flex: 1 1`, the omitted basis is zero.
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
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "center":
		return webJustifyCenter
	case "flex-end", "end", "right":
		return webJustifyEnd
	case "space-between":
		return webJustifyBetween
	case "space-around", "space-evenly":
		return webJustifyAround
	}
	return webJustifyStart
}

func webAlignValue(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "center":
		return webItemCenter
	case "flex-end", "end":
		return webItemEnd
	case "stretch":
		return webItemStretch
	}
	return webItemStart
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
	doc               *webDocument
	rules             []webRule
	css               strings.Builder
	stack             []webFrame
	blocks            []webBlock
	current           *webBlock
	lists             []webListState
	inHead            bool
	textLen           int
	trunc             bool
	space             bool
	pendingB          string
	mainDepth         int
	flexSeq           int
	containerSeq      int
	hasContainerUnits bool
	flexStack         []webFlexFrame
	// form is the <form> the parser is inside, so a control knows where its
	// value goes. Nested forms are invalid HTML, so one is enough. formSeq
	// numbers them, so a later <form> never inherits an earlier one's ID.
	form    webForm
	formSeq int
	// formHidden collects each form's hidden inputs, by form ID: they are
	// submitted without being drawn, so they belong to the form rather than to
	// any run.
	formHidden map[int][]webHiddenField
	// textarea collects a <textarea>'s shipped value between its tags, and the
	// select_ fields collect a <select>'s options. Both defer their run until
	// the closing tag, when the control's state is known; addText stays silent
	// while either is open, or the field's own text would draw twice.
	textarea             *webControl
	textareaText         strings.Builder
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
	// target is the page address's fragment: the element whose id it names is
	// the one :target matches.
	target string
}

// parseWebPage parses HTML plus any already-fetched CSS into a document.
//
// Every style sheet is in force for the whole document, as in a browser: the
// linked sheets (extraCSS) and the page's own <style> blocks are gathered
// before the walk and parsed once. The walk used to add each <style> block
// when it reached it, so nothing before a block saw it — not even <html>, so
// a `:root { --accent: … }` in the head reached no element — and re-parsed the
// whole accumulated sheet at every block, which on a page with dozens of them
// was quadratic.
func parseWebPage(source, extraCSS, baseURL string) *webDocument {
	doc := &webDocument{
		URL:        baseURL,
		Paper:      sdl.Color{R: 250, G: 251, B: 253, A: 255},
		Text:       sdl.Color{R: 28, G: 32, B: 38, A: 255},
		Containers: make(map[int]webContainerInfo),
	}
	p := &webParser{doc: doc}
	p.css.WriteString(extraCSS)
	webCollectInlineCSS(source, &p.css)
	if p.css.Len() > 0 {
		p.rules = parseWebCSS(p.css.String())
	}
	if webRulesNeedSelectorTree(p.rules) {
		p.selTree = webBuildSelectorTree(source)
	}
	if u, err := url.Parse(baseURL); err == nil {
		p.target = u.Fragment
	}
	p.walk(source)
	p.flush()
	doc.FormHidden = p.formHidden
	doc.HasContainerUnits = p.hasContainerUnits
	doc.Blocks = filterWebMainContent(p.blocks)
	if p.trunc {
		// The note goes in after the <main> filter, and at the top. Tagging it
		// as main content made the filter keep only this one sentence and drop
		// every real block, so an article past the text budget drew as a
		// single line; left at the end it would sit below thousands of rows
		// that the layout never reaches.
		note := webBlock{
			Style: webStyle{FontPx: 13, Italic: true},
			Runs:  []webRun{{Text: "[Page content truncated so the handheld keeps its memory.]", Style: webStyle{FontPx: 13, Italic: true}}},
		}
		doc.Blocks = append([]webBlock{note}, doc.Blocks...)
	}
	return doc
}

// filterWebMainContent drops navigation, headers and footers when the page
// marks its real content with <main> or <article>. Reading a docs page should
// start at the docs, not at the site's menu.
func filterWebMainContent(blocks []webBlock) []webBlock {
	hasMain := false
	for _, block := range blocks {
		if block.InMain {
			hasMain = true
			break
		}
	}
	if !hasMain {
		return blocks
	}
	kept := blocks[:0]
	for _, block := range blocks {
		if block.InMain {
			kept = append(kept, block)
		}
	}
	return kept
}

// fetchWebStylesheets pulls the page's linked stylesheets. External CSS is what
// makes most real pages look like themselves, so a few small sheets are worth
// the request; large frameworks are capped and simply contribute nothing.
func fetchWebStylesheets(source string, base *url.URL) string {
	if base == nil {
		return ""
	}
	var addresses []string
	seen := make(map[string]bool)
	pos := 0
	for pos < len(source) && len(addresses) < webMaxStylesheets {
		rel := webIndexFold(source[pos:], "<link")
		if rel < 0 {
			break
		}
		start := pos + rel
		end := findTextBrowserTagEnd(source, start+1)
		if end < 0 {
			break
		}
		tag := source[start+1 : end]
		pos = end + 1
		relAttr := strings.ToLower(htmlTagAttribute(tag, "rel"))
		if !strings.Contains(relAttr, "stylesheet") {
			continue
		}
		href := htmlAttrValue(htmlTagAttribute(tag, "href"))
		if href == "" {
			continue
		}
		ref, err := url.Parse(href)
		if err != nil {
			continue
		}
		abs := base.ResolveReference(ref)
		if abs.Scheme != "http" && abs.Scheme != "https" || seen[abs.String()] {
			continue
		}
		seen[abs.String()] = true
		addresses = append(addresses, abs.String())
	}
	if len(addresses) == 0 {
		return ""
	}
	// Fetch the sheets side by side. One slow sheet used to hold up the whole
	// page load, and on handheld wifi three sequential timeouts could keep the
	// reader on "Loading ..." for the better part of a minute.
	bodies := make([]string, len(addresses))
	var wg sync.WaitGroup
	pageURL := base.String()
	for i, address := range addresses {
		wg.Add(1)
		go func(i int, address string) {
			defer wg.Done()
			bodies[i] = fetchWebStylesheet(pageURL, address)
		}(i, address)
	}
	wg.Wait()
	var out strings.Builder
	for _, css := range bodies {
		if css == "" || out.Len() >= webMaxCSSBytes {
			continue
		}
		out.WriteString(css)
		out.WriteString("\n")
	}
	return out.String()
}

func fetchWebStylesheet(pageURL, address string) string {
	// A stylesheet from an ad or analytics host is never part of what the
	// reader draws; skipping it saves the request, not just the bytes. A page
	// the user excepted fetches everything.
	if browserRequestBlocked(pageURL, address) {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return ""
	}
	browserChromeResource(req.Header, "style", browserAcceptStyle, browserURL(pageURL), req.URL)
	// Same jar as the page: a sheet served only to a session (a CDN that
	// hands out a token with the HTML) has to see the page's cookie.
	client := browserHTTPClient(6 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); ct != "" && !strings.Contains(ct, "css") {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, webSheetByteCap+1))
	if err != nil || len(body) > webSheetByteCap {
		return ""
	}
	return string(body)
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
// the offset just past its closing tag.
func webRawElement(source string, start int, tag string) (string, int, bool) {
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

func webBlockElement(tag string) bool {
	switch tag {
	case "body", "p", "div", "section", "article", "main", "header", "footer",
		"aside", "nav", "form", "fieldset", "figure", "figcaption", "address",
		"dl", "dt", "dd", "li", "tr", "details", "summary", "blockquote", "pre",
		"h1", "h2", "h3", "h4", "h5", "h6":
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
	for i := 0; i < len(source); {
		lt := strings.IndexByte(source[i:], '<')
		if lt < 0 {
			return
		}
		i += lt
		if strings.HasPrefix(source[i:], "<!--") {
			end := strings.Index(source[i+4:], "-->")
			if end < 0 {
				return
			}
			i += 4 + end + 3
			continue
		}
		if strings.HasPrefix(source[i:], "<!") || strings.HasPrefix(source[i:], "<?") {
			end := strings.IndexByte(source[i:], '>')
			if end < 0 {
				return
			}
			i += end + 1
			continue
		}
		end := findTextBrowserTagEnd(source, i+1)
		if end < 0 {
			return
		}
		name, closing, _, _ := parseWebTag(source[i+1 : end])
		i = end + 1
		if name == "" || closing {
			continue
		}
		if name == "title" || name == "style" || webRawTextElement(name) {
			inner, next, _ := webRawElement(source, i, name)
			i = next
			if name == "style" && b.Len()+1 < webMaxCSSBytes {
				// A large site can ship megabytes of component CSS in one
				// block. Skipping that block entirely left its visible cards
				// unstyled; keep complete rules from its prefix up to the
				// handheld's bounded stylesheet budget.
				remaining := webMaxCSSBytes - b.Len() - 1
				inner = webCSSBudgetPrefix(inner, remaining)
				if inner != "" {
					b.WriteString("\n")
					b.WriteString(inner)
				}
			}
		}
	}
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
		p.push(webNewElement(name, attrs), webBaseStyle(), "skip")
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
	if name == "picture" {
		p.pictures = append(p.pictures, webPictureFrame{})
	}
	el := webNewElement(name, attrs)
	if p.selTreePos < len(p.selTree) {
		el.node = p.selTree[p.selTreePos]
		p.selTreePos++
	}
	el.target = p.target != "" && el.id == p.target
	style := p.computeStyle(el, attrs)
	if name == "body" || name == "html" {
		if style.HasBg {
			p.doc.Paper = style.Background
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
		p.push(el, style, "block")
		return
	}
	if name == "summary" {
		if n := len(p.details); n > 0 {
			// No flush: the summary continues the line the marker opened.
			p.details[n-1].inSummary = true
			p.push(el, style, "block")
			return
		}
	}

	if name == "main" || name == "article" {
		p.mainDepth++
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

	if name == "form" {
		// A form is where a filled control goes. A missing action means "this
		// page", the same convention a browser uses. The ID is what keeps two
		// forms with the same action from sharing a submit.
		p.formSeq++
		p.form = webForm{
			ID:     p.formSeq,
			Action: htmlAttrValue(htmlTagAttribute(attrs, "action")),
		}
	}

	switch name {
	case "br":
		p.addBreak()
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
		p.tagFlexBlock(&rule, style)
		rule.Details, rule.DetailsShown = p.detailsTag()
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
		p.flush()
		p.beginFlexLeaf(style)
		block := p.ensureBlock()
		block.Image = true
		// An <img> is a replaced element: its own width, height and
		// aspect-ratio belong on the image block, not on the surrounding
		// paragraph style ensureBlock inherited.
		block.Style = style
		block.ImageSrc = resolved
		block.ImageW = htmlTagAttributeFloat(attrs, "width")
		block.ImageH = htmlTagAttributeFloat(attrs, "height")
		p.flush()
		return
	case "td", "th":
		if p.current != nil && len(p.current.Runs) > 0 {
			p.addText("   ")
		}
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
	case "textarea":
		p.beginFlexLeaf(style)
		p.startTextarea(attrs)
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
		p.flush()
		p.lists = append(p.lists, webListState{ordered: name == "ol"})
		p.push(el, style, "list")
		return
	}
	if name == "li" {
		p.flush()
		p.pendingB = p.nextBullet(style)
		p.push(el, style, "block")
		return
	}
	if webBlockElement(name) {
		p.flush()
		p.push(el, style, "block")
		if selfClose {
			p.endTag(name)
		}
		return
	}
	p.push(el, style, "inline")
	if selfClose {
		p.endTag(name)
	}
}

func (p *webParser) endTag(name string) {
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
		if p.stack[i].kind == "block" || p.stack[i].kind == "list" {
			p.flush()
		}
		if name == "head" {
			p.inHead = false
		}
		if (name == "main" || name == "article") && p.mainDepth > 0 {
			p.mainDepth--
		}
		if name == "ul" || name == "ol" {
			if depth := len(p.lists); depth > 0 {
				p.lists = p.lists[:depth-1]
			}
		}
		if name == "li" {
			p.pendingB = ""
		}
		if name == "form" {
			p.form = webForm{}
		}
		// Closing an element also closes every flex container it opened, even
		// when its own end tag was missing (real pages are full of those).
		for len(p.flexStack) > 0 && p.flexStack[len(p.flexStack)-1].depth >= i {
			p.flexStack = p.flexStack[:len(p.flexStack)-1]
		}
		p.stack = p.stack[:i]
		return
	}
}

func (p *webParser) push(el webElement, style webStyle, kind string) {
	depth := len(p.stack)
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
	// A block element pushed directly under an open flex container is one of
	// its items; nested elements belong to the same item. Text-only children
	// are tagged by ensureBlock instead, which is why the counter starts at
	// zero rather than one.
	if kind != "skip" && len(p.flexStack) > 0 {
		// The container was pushed at its own depth, so its direct children
		// are pushed one level deeper. Comparing equal depths never matched,
		// which left every item in one column.
		if top := &p.flexStack[len(p.flexStack)-1]; top.depth+1 == depth {
			p.flush()
			top.item++
			top.itemStyle = style
		}
	}
	group := 0
	if (kind == "block" || kind == "list") && (style.Display == webDisplayFlex || style.Display == webDisplayGrid) {
		p.flexSeq++
		group = p.flexSeq
		p.flexStack = append(p.flexStack, webFlexFrame{
			group:    group,
			depth:    depth,
			dir:      style.FlexDir,
			justify:  style.Justify,
			align:    style.AlignItems,
			gapX:     style.GapX,
			gapY:     style.GapY,
			wrap:     style.FlexWrap,
			gridCols: style.GridCols,
			gridMin:  style.GridMin,
		})
	}
	p.stack = append(p.stack, webFrame{el: el, style: style, kind: kind, flexGroup: group, containerID: containerID})
}

// beginFlexLeaf assigns a direct replaced/void child its own flex item.
// Unlike a div, img, hr and input never go through push, so without this two
// adjacent images or buttons were stacked in one cell.
func (p *webParser) beginFlexLeaf(style webStyle) {
	if len(p.flexStack) == 0 {
		return
	}
	top := &p.flexStack[len(p.flexStack)-1]
	if top.depth+1 != len(p.stack) {
		return
	}
	p.flush()
	top.item++
	top.itemStyle = style
}

func (p *webParser) topStyle() webStyle {
	if len(p.stack) == 0 {
		return webBaseStyle()
	}
	return p.stack[len(p.stack)-1].style
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

func (p *webParser) computeStyle(el webElement, attrs string) webStyle {
	st := p.topStyle()
	// Non-inherited properties start clean for every element.
	st.HasBg, st.Background = false, sdl.Color{}
	st.HasGradient, st.Gradient = false, nil
	st.Indent, st.TextIndent, st.MarginTop, st.MarginBot = 0, 0, 0, 0
	st.BorderLeft, st.BorderRight, st.ListNone = false, false, false
	st.BorderWidth = 0
	// A radius and a right gutter belong to the element that declared them;
	// line-height, the list marker and the opacity group are inherited above.
	st.Radius, st.IndentRight = 0, 0
	st.BoxBorder, st.BorderTop, st.BorderBottom = false, false, false
	st.BorderColor, st.HasBorderColor = sdl.Color{}, false
	// Layout properties are per element too: a child of a flex container is
	// not itself a container, and an item's grow factor belongs to it alone.
	st.Display, st.FlexDir = webDisplayBlock, webFlexRow
	st.Justify, st.AlignItems, st.AlignSelf = webJustifyStart, webItemStretch, -1
	st.GapX, st.GapY, st.FlexWrap, st.GridCols, st.GridMin = 0, 0, false, 0, 0
	st.FlexGrow, st.FlexBasis, st.BasisPct = 0, -1, 0
	st.Order = 0
	st.Width, st.WidthPct, st.Height = 0, 0, 0
	st.AspectRatio = 0
	st.ContainerType, st.ContainerName = 0, ""
	st.CQ = nil
	st.ZIndex, st.Position = 0, webPositionStatic
	st.Top, st.Right, st.Bottom, st.Left = 0, 0, 0, 0
	st.HasTop, st.HasRight, st.HasBottom, st.HasLeft = false, false, false, false
	// Link colour, underline and strikethrough are not reset: a decoration
	// on <a> or <s> has to keep showing on the <strong>/<span> inside it,
	// which is how every browser renders `<a><b>text</b></a>`.

	applyWebTagDefaults(&st, el.tag)

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
	for i := range p.rules {
		rule := &p.rules[i]
		if !rule.sel.matchesIn(chain, rule.mode()) {
			continue
		}
		if len(rule.containers) > 0 && !p.webContainerRulesApply(rule.containers) {
			continue
		}
		matched = append(matched, rule)
	}
	slices.SortStableFunc(matched, func(a, b *webRule) int {
		switch {
		case a.specificity != b.specificity:
			return a.specificity - b.specificity
		case a.order < b.order:
			return -1
		case a.order > b.order:
			return 1
		}
		return 0
	})
	// Cascade: stylesheet normals, inline normals, stylesheet important, then
	// inline important. A normal inline style must not override an important
	// stylesheet rule (including a rule that hides an element).
	// Collapsing the winners into one map also gives var()
	// and calc() the final value to resolve against, so the property parsers
	// below only ever see plain lengths and colours.
	merged := make(map[string]string, 16)
	for _, rule := range matched {
		for name, value := range rule.decls {
			merged[name] = value
		}
	}
	inlineDecls, inlineImportant := parseWebDeclsSplit(htmlTagAttribute(attrs, "style"))
	for name, value := range inlineDecls {
		merged[name] = value
	}
	for _, rule := range matched {
		for name, value := range rule.important {
			merged[name] = value
		}
	}
	for name, value := range inlineImportant {
		merged[name] = value
	}
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
	base := st
	base.CQ = nil
	// `em` inside calc() is this element's font size, which font-size itself
	// has to settle first.
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
	applyWebDecls(&st, merged)
	if needsCQ {
		st.CQ = &webCQContext{base: base, decls: rawDecls, vars: st.Vars}
		p.hasContainerUnits = true
	}
	// The anchor target is not a CSS property and is never inherited from a
	// sibling: every <a> names its own, and its children inherit that.
	if el.tag == "a" {
		st.Href = htmlstd.UnescapeString(strings.TrimSpace(htmlTagAttribute(attrs, "href")))
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
	return st
}

func (p *webParser) nextBullet(style webStyle) string {
	if len(p.lists) == 0 || style.ListNone {
		return ""
	}
	list := &p.lists[len(p.lists)-1]
	if list.ordered {
		list.n++
	}
	return webListMarker(style.ListStyle, list.ordered, list.n)
}

func (p *webParser) ensureBlock() *webBlock {
	if p.current != nil {
		return p.current
	}
	st := p.nearestBlockStyle()
	block := &webBlock{
		Style:          st,
		ContainerIDs:   p.containerPath(),
		Align:          st.Align,
		Indent:         st.Indent,
		TextIndent:     st.TextIndent,
		MarginTop:      st.MarginTop,
		MarginBot:      st.MarginBot,
		Pre:            st.Pre,
		HasBg:          st.HasBg,
		Background:     st.Background,
		BorderLeft:     st.BorderLeft,
		BorderRight:    st.BorderRight,
		BoxBorder:      st.BoxBorder,
		BorderTop:      st.BorderTop,
		BorderBottom:   st.BorderBottom,
		BorderWidth:    st.BorderWidth,
		BorderColor:    st.BorderColor,
		HasBorderColor: st.HasBorderColor,
		Bullet:         p.pendingB,
		InMain:         p.mainDepth > 0,
		FlexGrow:       st.FlexGrow,
		FlexBasis:      st.FlexBasis,
		BasisPct:       st.BasisPct,
		Width:          st.Width,
		WidthPct:       st.WidthPct,
		AlignSelf:      st.AlignSelf,
		ZIndex:         st.ZIndex,
		Float:          st.Position == webPositionAbsolute,
		PosTop:         st.Top,
		PosRight:       st.Right,
		PosBottom:      st.Bottom,
		PosLeft:        st.Left,
		HasTop:         st.HasTop,
		HasRight:       st.HasRight,
		HasBottom:      st.HasBottom,
		HasLeft:        st.HasLeft,
	}
	// The ramp is copied by value onto the block: a background belongs to the
	// element that declared it, unlike the shared pointer the style carries for
	// comparison.
	if st.HasGradient && st.Gradient != nil {
		block.Gradient, block.HasGradient = *st.Gradient, true
	}
	p.tagFlexBlock(block, st)
	block.Details, block.DetailsShown = p.detailsTag()
	p.pendingB = ""
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
	if boxed {
		// The same filled chip an inline <code> gets, sized by the text it
		// shows, so a field cannot be mistaken for body copy.
		st.HasBg = true
		st.Background = sdl.Color{R: 232, G: 237, B: 244, A: 255}
	}
	// The italic that marks an empty field's prompt is decided at layout time
	// instead (webControl.PlaceholderShown): fixing it here left the prompt
	// looking italic after the field had been filled in.
	p.appendRun(ctrl.Display(), st)
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
func (p *webParser) startTextarea(attrs string) {
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
	p.textareaText.Reset()
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
	p.emitControl(ctrl, p.topStyle(), true)
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
	if p.inHead || raw == "" {
		return
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
	style := p.topStyle()
	if style.Hidden {
		return
	}
	if p.textLen >= webMaxTextBytes || len(p.blocks) >= webMaxBlocks {
		p.trunc = true
		return
	}
	text := strings.ToValidUTF8(htmlstd.UnescapeString(raw), "")
	if style.Pre {
		text = strings.ReplaceAll(text, "\r\n", "\n")
		text = strings.ReplaceAll(text, "\r", "\n")
		if text == "" {
			return
		}
		p.appendRun(text, style)
		return
	}
	collapsed := strings.Join(strings.Fields(text), " ")
	if collapsed == "" {
		p.space = true
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
	if n := len(block.Runs); n > 0 && block.Runs[n-1].Style == style {
		block.Runs[n-1].Text += text
	} else {
		block.Runs = append(block.Runs, webRun{Text: text, Style: style})
	}
	p.textLen += len(text)
}

func (p *webParser) flush() {
	if p.current == nil {
		return
	}
	block := p.current
	p.current = nil
	// Drop a block that only carried whitespace.
	hasText := false
	for _, run := range block.Runs {
		if strings.TrimSpace(strings.ReplaceAll(run.Text, "\n", "")) != "" {
			hasText = true
			break
		}
	}
	if !hasText && block.Bullet == "" && !block.Rule && !block.Image && !block.Video {
		return
	}
	for i := range block.Runs {
		if i == len(block.Runs)-1 {
			block.Runs[i].Text = strings.TrimRight(block.Runs[i].Text, " ")
		}
	}
	p.blocks = append(p.blocks, *block)
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
func webGradientStamp(g webGradient, has bool) string {
	if !has || len(g.Stops) == 0 {
		return "-"
	}
	first, last := g.Stops[0].Color, g.Stops[len(g.Stops)-1].Color
	axis := "h"
	if g.Vertical {
		axis = "v"
	}
	return fmt.Sprintf("%s%d:%02x%02x%02x-%02x%02x%02x", axis, len(g.Stops),
		first.R, first.G, first.B, last.R, last.G, last.B)
}

func (d *webDocument) layoutFor(config *Config, elem Element, width int32) *webLayout {
	stamp := fmt.Sprintf("%d|%s|%d|%d,%d,%d|%d,%d,%d|%s|%.3f",
		width, elem.Font, a11yScaleFontSize(16),
		d.Paper.R, d.Paper.G, d.Paper.B,
		d.Text.R, d.Text.G, d.Text.B,
		webGradientStamp(d.PaperGradient, d.HasPaperGradient), browserZoomScale())
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.layout != nil && d.stamp == stamp {
		return d.layout
	}
	d.layout = d.computeLayout(config, elem, width)
	d.stamp = stamp
	return d.layout
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
	d.stamp = ""
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
	fontAt := func(cssPx float64) *ttf.Font {
		size := int(math.Round(cssPx * baseSize / 16))
		minSize := int(math.Round(baseSize * 0.72))
		maxSize := int(math.Round(baseSize * 1.5))
		if size < minSize {
			size = minSize
		}
		if size > maxSize {
			size = maxSize
		}
		if size < 8 {
			size = 8
		}
		if font, ok := fonts[size]; ok {
			return font
		}
		font := getFontSized(config, elem.Font, size)
		fonts[size] = font
		return font
	}

	// Cap the measure the way a real content column does. Full-bleed lines on
	// a 1280px panel run to ~140 characters, which is tiring on a small
	// screen; 38 em keeps a comfortable line and centres the column.
	contentW := width - 12
	if limit := int32(baseSize * 38); limit > 0 && contentW > limit {
		contentW = limit
	}
	if contentW < 60 {
		return nil
	}
	padX := (width - contentW) / 2
	layoutOnce := func(blocks []webBlock) (*webLayout, map[int]webContainerSize) {
		layout := &webLayout{RowH: rowH}
		pageWidth := float64(contentW) * 16 / baseSize
		sizes := webNewContainerSizes(d.Containers, pageWidth)
		l := &webLayouter{
			doc: d, layout: layout, rowH: rowH, baseSize: baseSize, fontAt: fontAt,
			spaceWidths:    map[*ttf.Font]int32{},
			sanitized:      make(map[webSanitizeKey]string, 256),
			containerSizes: sizes,
			containerRows:  make(map[int]webContainerRowSpan),
		}
		// Absolutely positioned blocks are measured on their own and painted
		// over the flow, so they take no room in it.
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

		layout.Height = int32(len(layout.Rows)) * rowH
		l.layoutFloats(floats, padX, contentW)
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
	sort.SliceStable(layout.Rects, func(i, j int) bool { return layout.Rects[i].Z < layout.Rects[j].Z })
	layout.Links = collectWebLinks(layout)
	// Play cards and form controls are cursor stops too: the same hit shape as
	// a link, with the video or the control attached instead of a destination.
	// One merge keeps every kind in reading order, so a field sits where it
	// drew and the D-pad walks the page top to bottom.
	layout.Links = append(layout.Links, collectWebVideoLinks(layout)...)
	layout.Links = append(layout.Links, collectWebControls(layout)...)
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
	fontAt   func(float64) *ttf.Font

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
// so the same word is scanned once.
func (l *webLayouter) sanitizedWord(font *ttf.Font, text string) string {
	key := webSanitizeKey{font: font, text: text}
	if out, ok := l.sanitized[key]; ok {
		return out
	}
	out := sanitizeForFont(font, text)
	if l.sanitized == nil {
		l.sanitized = make(map[webSanitizeKey]string, 256)
	}
	l.sanitized[key] = out
	return out
}

// layoutBlocks lays out a column of blocks into l.layout. Positions are
// relative to the column's own width, with padX as its left edge, so a flex
// item can be laid out at 0 and moved later.
func (l *webLayouter) layoutBlocks(blocks []webBlock, padX, contentW int32) {
	layout := l.layout
	rowH := l.rowH
	baseSize := l.baseSize
	fontAt := l.fontAt
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
		slots := int32(1)
		if need := int32(math.Ceil(float64(fontH+4) / float64(rowH))); need > slots {
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
	marginRows := func(cssPx float64) int32 {
		if cssPx <= 0 {
			return 0
		}
		design := cssPx * baseSize / 16
		rows := int32(math.Round(design / float64(rowH)))
		if rows < 1 && design >= float64(rowH)*0.45 {
			rows = 1
		}
		if rows > 3 {
			rows = 3
		}
		return rows
	}

	// Adjacent vertical margins collapse to the larger of the two, the way CSS
	// does it. Summing them stacked a heading's own spacing on top of the
	// previous block's and filled real pages with holes on a small screen.
	previousMarginBot := 0.0
	for i := 0; i < len(blocks); i++ {
		block := blocks[i]
		if len(layout.Rows) >= webMaxRows {
			break
		}
		blockStart := int32(len(layout.Rows))
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
			l.layoutFlexRow(blocks[i:j], padX, contentW)
			l.recordContainerRows(block, blockStart, int32(len(layout.Rows)))
			previousMarginBot = 0
			i = j - 1
			continue
		}
		if block.FlexGroup != 0 && block.FlexDir == webFlexColumn {
			j := i
			for j < len(blocks) && blocks[j].FlexGroup == block.FlexGroup && blocks[j].FlexDir == webFlexColumn {
				j++
			}
			grouped := webGroupFlexItems(blocks[i:j])
			if len(grouped) > 0 {
				lead := grouped[0].blocks[0].MarginTop
				if previousMarginBot > lead {
					lead = previousMarginBot
				}
				if len(layout.Rows) > 0 {
					skipRows(marginRows(lead))
				}
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
					l.layoutBlocksFlat(itemBlocks, padX, contentW)
				}
			}
			l.recordContainerRows(block, blockStart, int32(len(layout.Rows)))
			previousMarginBot = blocks[j-1].MarginBot
			i = j - 1
			continue
		}
		gap := block.MarginTop
		if previousMarginBot > gap {
			gap = previousMarginBot
		}
		// No leading blank rows at the top of the page.
		if len(layout.Rows) > 0 {
			skipRows(marginRows(gap))
		}
		previousMarginBot = block.MarginBot
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
			layout.Rects = append(layout.Rects, webRect{
				Rect:  sdl.Rect{X: padX + indent, Y: y, W: contentW - indent, H: barW},
				Color: webFadeColor(color, d.Paper, block.Style),
			})
			skipRows(1)
			l.recordContainerRows(block, blockStart, int32(len(layout.Rows)))
			continue
		}
		if block.Image {
			l.layoutImage(block, padX, contentW)
			l.recordContainerRows(block, blockStart, int32(len(layout.Rows)))
			continue
		}
		if block.Video {
			l.layoutVideo(block, padX, contentW)
			l.recordContainerRows(block, blockStart, int32(len(layout.Rows)))
			continue
		}
		startGrid := int32(len(layout.Rows))

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
		if block.Bullet != "" {
			bullet = block.Bullet
			if font := fontAt(16); font != nil {
				if w, _, err := font.SizeUTF8(bullet + " "); err == nil {
					bulletW = int32(w)
				}
			}
		}
		textIndent := indent + bulletW
		avail := contentW - textIndent - indentRight
		if avail < 48 {
			textIndent = indent
			avail = contentW - textIndent - indentRight
		}
		if avail < 48 {
			avail = contentW - textIndent
		}

		var line []webRunPos
		var lineMax, lineFontH int32
		firstLine := true
		lineStart := padX + textIndent
		x := lineStart + firstLineIndent

		flushLine := func() {
			if len(line) == 0 {
				return
			}
			lineWidth := lineMax - (padX + textIndent + firstLineIndent)
			if lineWidth < 0 {
				lineWidth = 0
			}
			if firstLine && bullet != "" {
				if font := fontAt(16); font != nil {
					line = append([]webRunPos{{
						Text:  bullet,
						X:     padX + indent,
						Font:  font,
						Color: webResolveColor(block.Style, d),
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
			// The author's line-height, when it is larger than the glyphs,
			// becomes the slot height: the row grid centres the text in the
			// slot, which is exactly what extra leading looks like. Capped so
			// `line-height: 400px` cannot spend the screen on one paragraph.
			slotH := lineFontH
			if lh := block.Style.LineHeight * baseSize / 16; lh > float64(slotH) {
				if limit := float64(rowH * 3); lh > limit {
					lh = limit
				}
				slotH = int32(lh)
			}
			emit(line, slotH)
			firstLine = false
			line = nil
			lineMax, lineFontH = 0, 0
			x = lineStart
		}

		for _, run := range block.Runs {
			// A form control paints its current state, not the text it was
			// parsed with: a filled field, a stepped select and a ticked box all
			// read back from the control, so changing one is a re-layout rather
			// than a second copy of the run to keep in sync.
			runText := run.Text
			if run.Style.Control != nil {
				runText = run.Style.Control.Display()
			}
			segments := strings.Split(runText, "\n")
			for si, segment := range segments {
				if si > 0 {
					if len(line) == 0 {
						// An explicit <br> on an empty line is a blank row.
						skipRows(1)
					} else {
						flushLine()
					}
				}
				if segment == "" {
					continue
				}
				words := strings.Fields(segment)
				if block.Pre && len(words) > 0 {
					// Keep code lines as one measured run; a long one is
					// clipped to the page width instead of re-flowed.
					words = []string{strings.TrimRight(segment, " ")}
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
				leadingSpace := strings.HasPrefix(segment, " ") || strings.HasPrefix(segment, "\t")
				for wi, word := range words {
					if word == "" {
						continue
					}
					font := fontAt(run.Style.FontPx)
					if font == nil {
						continue
					}
					word = webTransformWord(word, run.Style.Transform)
					text := l.sanitizedWord(font, word)
					if text == "" {
						continue
					}
					width, height, err := font.SizeUTF8(text)
					if err != nil {
						continue
					}
					wordW := int32(width)
					boxW, boxH := int32(0), int32(0)
					if ctrl := run.Style.Control; ctrl != nil && (ctrl.Kind == "text" || ctrl.Kind == "textarea") {
						boxW = int32(ctrl.Width * baseSize / 16)
						if ctrl.WidthPct > 0 {
							boxW = int32(float64(avail) * ctrl.WidthPct / 100)
						}
						if boxW <= 0 {
							boxW = int32(baseSize * 12)
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
					if len(line) > 0 && (wi > 0 || leadingSpace) {
						spaceW := l.spaceWidth(font)
						if x+spaceW+wordW > padX+contentW {
							flushLine()
						} else {
							x += spaceW
						}
					}
					if boxW > 0 && len(line) > 0 && x+boxW > padX+contentW-indentRight {
						flushLine()
					}
					if boxW == 0 && wordW > avail {
						text = ellipsize(font, text, avail)
						if w2, _, e := font.SizeUTF8(text); e == nil {
							wordW = int32(w2)
						}
					}
					color := webResolveColor(run.Style, d)
					if run.Style.Control != nil && run.Style.Control.PlaceholderShown() {
						// The reader has one face, so an italic prompt is not something it
						// can draw (webStyle.Italic is parsed but never painted). Dimming
						// toward the paper is the cue that actually renders: an empty
						// field reads as a prompt, and stops the moment a value is typed.
						color = sdl.Color{
							R: uint8((int(color.R) + 2*int(d.Paper.R)) / 3),
							G: uint8((int(color.G) + 2*int(d.Paper.G)) / 3),
							B: uint8((int(color.B) + 2*int(d.Paper.B)) / 3),
							A: color.A,
						}
					}
					line = append(line, webRunPos{
						Text:      text,
						X:         x,
						Font:      font,
						Color:     color,
						Bold:      run.Style.Bold,
						Underline: run.Style.Underline,
						Strike:    run.Style.Strike,
						Href:      run.Style.Href,
						HasBg:     run.Style.HasBg && (!block.HasBg || boxW > 0),
						Bg:        run.Style.Background,
						Control:   run.Style.Control,
						BoxW:      boxW, BoxH: boxH,
					})
					x += wordW
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
		}
		flushLine()

		endGrid := int32(len(layout.Rows))
		l.recordContainerRows(block, blockStart, endGrid)
		height := (endGrid - startGrid) * rowH
		if block.HasBg && height > 0 {
			rect := webRect{
				Rect:   sdl.Rect{X: padX - 4, Y: startGrid * rowH, W: contentW + 8, H: height},
				Color:  webFadeColor(block.Background, d.Paper, block.Style),
				Z:      block.ZIndex,
				Radius: radius,
			}
			if block.HasGradient {
				rect.Gradient = webFadeGradient(block.Gradient, d.Paper, block.Style)
				rect.HasGradient = true
			}
			layout.Rects = append(layout.Rects, rect)
		}
		if height > 0 && (block.BorderLeft || block.BorderRight || block.BoxBorder || block.BorderTop || block.BorderBottom) {
			// One colour for every edge the block draws: an uncoloured border is
			// the reader's light hairline, a coloured one is what the sheet
			// asked for — a `border-left: 4px solid #c00` quote used to render a
			// grey bar, which is the one thing the author definitely decided.
			color := sdl.Color{R: 170, G: 178, B: 192, A: 255}
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
			top := startGrid * rowH
			if block.BorderLeft {
				layout.Rects = append(layout.Rects, webRect{
					Rect:  sdl.Rect{X: padX + indent - 8, Y: top, W: barW, H: height},
					Color: color,
					Z:     block.ZIndex,
				})
			}
			if block.BorderRight {
				// Mirrors the left bar into the same gutter on the other side.
				x := padX + contentW - int32(block.Style.IndentRight) + 5
				layout.Rects = append(layout.Rects, webRect{
					Rect:  sdl.Rect{X: x, Y: top, W: barW, H: height},
					Color: color,
					Z:     block.ZIndex,
				})
			}
			if block.BoxBorder {
				x := padX + indent - 6
				w := contentW - indent + 12
				if x < padX {
					x = padX
					w = contentW
				}
				layout.Rects = append(layout.Rects, webRect{
					Rect:      sdl.Rect{X: x, Y: top, W: w, H: height},
					Color:     color,
					Stroke:    true,
					Thickness: ruleH,
					Z:         block.ZIndex,
					Radius:    radius,
				})
			} else {
				if block.BorderTop {
					layout.Rects = append(layout.Rects, webRect{
						Rect:  sdl.Rect{X: padX, Y: top, W: contentW, H: ruleH},
						Color: color,
						Z:     block.ZIndex,
					})
				}
				if block.BorderBottom {
					layout.Rects = append(layout.Rects, webRect{
						Rect:  sdl.Rect{X: padX, Y: top + height - ruleH, W: contentW, H: ruleH},
						Color: color,
						Z:     block.ZIndex,
					})
				}
			}
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
			if run := row.Runs[i]; run.BoxW > 0 && run.BoxH > 0 {
				hit.W = run.BoxW
				hit.Box = sdl.Rect{X: run.X, Y: row.Y, W: run.BoxW, H: run.BoxH}
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
			startX := run.X
			endX := run.X
			label := ""
			j := i
			for ; j < len(row.Runs); j++ {
				cur := row.Runs[j]
				if cur.Href != href || cur.Font == nil {
					break
				}
				w, _, err := cur.Font.SizeUTF8(cur.Text)
				if err != nil {
					break
				}
				label += " " + cur.Text
				if end := cur.X + int32(w); end > endX {
					endX = end
				}
			}
			i = j
			if endX <= startX || seen[href] {
				continue
			}
			seen[href] = true
			links = append(links, webLinkHit{
				Row:  rowIdx,
				X:    startX,
				W:    endX - startX,
				Href: href,
				Text: strings.Join(strings.Fields(label), " "),
			})
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

// webReadableColor keeps author colours when they read on the page background
// and flips them when a page assumed its own background (a common reason a
// light page renders as invisible text).
func webReadableColor(fg, bg sdl.Color) sdl.Color {
	if webContrast(fg, bg) >= 2.4 {
		return fg
	}
	if webLuminance(bg) > 0.4 {
		return sdl.Color{R: 28, G: 32, B: 38, A: 255}
	}
	return sdl.Color{R: 242, G: 245, B: 250, A: 255}
}

func webResolveColor(style webStyle, doc *webDocument) sdl.Color {
	// An element with its own background (an inline <code> chip, a tinted
	// box) must be judged against that background, not the page: a light chip
	// on a dark page would otherwise draw light text on light fill.
	bg := doc.Paper
	if style.HasBg {
		bg = style.Background
	}
	color := doc.Text
	if style.HasColor {
		color = style.Color
	} else if style.Link {
		if webLuminance(bg) > 0.4 {
			color = sdl.Color{R: 20, G: 88, B: 200, A: 255}
		} else {
			color = sdl.Color{R: 130, G: 170, B: 255, A: 255}
		}
	}
	// Opacity is painted by blending toward the surface the text sits on: the
	// reader has no compositing layer, and a faded label should still read.
	return webFadeColor(webReadableColor(color, bg), bg, style)
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

// drawWebGradient paints a ramp inside rect, band by band. The band count is
// capped so a tall hero card costs a bounded number of fills per frame while a
// small header still gets one band per row: at 96 bands a 40-px header is
// per-pixel, and a 600-px hero is a 6-px staircase nobody can see.
func drawWebGradient(renderer *sdl.Renderer, rect sdl.Rect, radius int32, g webGradient) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 || len(g.Stops) == 0 {
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
	renderWithClip(renderer, rect.X, rect.Y, rect.W, rect.H, func(r *sdl.Renderer) {
		if doc.HasPaperGradient {
			// The page background is a ramp too, not the average of one: a
			// body gradient is most of what a landing page looks like.
			drawWebGradient(r, rect, 0, doc.PaperGradient)
		} else {
			r.SetDrawColor(doc.Paper.R, doc.Paper.G, doc.Paper.B, 255)
			r.FillRect(&sdl.Rect{X: rect.X, Y: rect.Y, W: rect.W, H: rect.H})
		}

		offset := int32(startRow) * layout.RowH
		if len(layout.Floats) > 0 {
			drawWebFloats(r, config, doc, layout, rect, offset, true)
		}
		for _, bg := range layout.Rects {
			y := rect.Y + bg.Rect.Y - offset
			if y+bg.Rect.H <= rect.Y || y >= rect.Y+rect.H {
				continue
			}
			if bg.Stroke {
				thickness := bg.Thickness
				if thickness < 1 {
					thickness = 1
				}
				strokeRoundedRect(r, rect.X+bg.Rect.X, y, bg.Rect.W, bg.Rect.H, webRectCorner(bg, 3), thickness, bg.Color)
				continue
			}
			if bg.HasGradient {
				drawWebGradient(r, sdl.Rect{X: rect.X + bg.Rect.X, Y: y, W: bg.Rect.W, H: bg.Rect.H}, webRectCorner(bg, 2), bg.Gradient)
				continue
			}
			fillRoundedRect(r, rect.X+bg.Rect.X, y, bg.Rect.W, bg.Rect.H, webRectCorner(bg, 2), bg.Color)
		}
		// Images draw on their own reserved rows, before the text, so a page
		// with a header image scrolls the photo away like a normal line.
		for i := range layout.Images {
			im := layout.Images[i]
			y := rect.Y + im.Rect.Y - offset
			if im.Rect.H <= 0 || y >= rect.Y+rect.H || y+im.Rect.H <= rect.Y {
				continue
			}
			drawWebImage(r, config, doc, im, rect.X+im.Rect.X, y)
		}
		// Video play cards come after the images on the same terms: they
		// own their reserved rows and draw before the text rows.
		for i := range layout.Videos {
			v := layout.Videos[i]
			y := rect.Y + v.Rect.Y - offset
			if v.Rect.H <= 0 || y >= rect.Y+rect.H || y+v.Rect.H <= rect.Y {
				continue
			}
			drawWebVideo(r, config, doc, v, rect.X+v.Rect.X, y)
		}
		for i := startRow; i < len(layout.Rows); i++ {
			row := layout.Rows[i]
			y := rect.Y + row.Y - offset
			if y >= rect.Y+rect.H {
				break
			}
			// Inline backgrounds first, merged across adjacent words of the
			// same style so a code phrase is one chip instead of one box per
			// word.
			for i := 0; i < len(row.Runs); i++ {
				run := row.Runs[i]
				if !run.HasBg || run.Font == nil {
					continue
				}
				if run.BoxW > 0 && run.BoxH > 0 {
					fillRoundedRect(r, rect.X+run.X, y, run.BoxW, run.BoxH, 3, run.Bg)
					continue
				}
				w, h, err := run.Font.SizeUTF8(run.Text)
				if err != nil {
					continue
				}
				startX := rect.X + run.X - 2
				endX := rect.X + run.X + int32(w)
				for j := i + 1; j < len(row.Runs); j++ {
					next := row.Runs[j]
					if !next.HasBg || next.Bg != run.Bg || next.Font == nil {
						break
					}
					nw, _, e := next.Font.SizeUTF8(next.Text)
					if e != nil {
						break
					}
					endX = rect.X + next.X + int32(nw)
					i = j
				}
				fillRoundedRect(r, startX, y, endX-startX+4, int32(h), 3, run.Bg)
			}
			for _, run := range row.Runs {
				x, textY := rect.X+run.X, y
				if run.BoxW > 0 && run.BoxH > 0 && run.Font != nil {
					x += 6
					textY += (run.BoxH - int32(run.Font.Height())) / 2
				}
				drawTextRun(r, config, run.Font, run.Text, run.Color, x, textY)
				if run.Bold {
					// One pixel of fake weight: the app ships one font face, and
					// a second offset copy reads as bold at this size.
					drawTextRun(r, config, run.Font, run.Text, run.Color, x+1, textY)
				}
				if (run.Underline || run.Strike) && run.Font != nil {
					if w, _, err := run.Font.SizeUTF8(run.Text); err == nil {
						r.SetDrawColor(run.Color.R, run.Color.G, run.Color.B, 220)
						if run.Underline {
							r.FillRect(&sdl.Rect{
								X: rect.X + run.X,
								Y: y + int32(run.Font.Height()) - 5,
								W: int32(w),
								H: 1,
							})
						}
						if run.Strike {
							r.FillRect(&sdl.Rect{
								X: rect.X + run.X,
								Y: y + int32(run.Font.Height())/2,
								W: int32(w),
								H: 1,
							})
						}
					}
				}
			}
		}
		if len(layout.Floats) > 0 {
			drawWebFloats(r, config, doc, layout, rect, offset, false)
		}
	})
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
