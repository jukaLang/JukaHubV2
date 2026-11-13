package main

// html5_inapp.go runs small Canvas 2D games in JukaHub's own browser view.
// It uses the browser's Goja JavaScript engine and SDL renderer, so the stock
// Smart Pro needs no external browser. The supported surface is intentionally
// bounded: Canvas 2D drawing, animation frames, keys, pointer input and local
// storage. Games that require WebGL or a full browser engine report an error.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/veandco/go-sdl2/sdl"
	"golang.org/x/net/html"
)

const (
	html5ScriptLimit  = 2 << 20
	html5CommandLimit = 15000
)

type html5DOMNode struct {
	Tag     string            `json:"tag"`
	ID      string            `json:"id,omitempty"`
	Dataset map[string]string `json:"dataset,omitempty"`
	Width   int               `json:"width,omitempty"`
	Height  int               `json:"height,omitempty"`
}

type html5Package struct {
	Title   string
	Width   int
	Height  int
	Nodes   []html5DOMNode
	Scripts []string
}

// All scripts and assets are opened relative to the selected game's folder.
// This also prevents an imported HTML file from reading a different game.
func loadHTML5Package(entry string) (html5Package, error) {
	var pkg html5Package
	root, err := os.OpenRoot(filepath.Dir(entry))
	if err != nil {
		return pkg, err
	}
	defer root.Close()
	f, err := root.Open(filepath.Base(entry))
	if err != nil {
		return pkg, err
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > 4<<20 {
		return pkg, errors.New("this game's HTML page is too large for the in-app browser")
	}
	tree, err := html.Parse(f)
	if err != nil {
		return pkg, err
	}
	var walk func(*html.Node) error
	walk = func(n *html.Node) error {
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			attrs := map[string]string{}
			for _, a := range n.Attr {
				attrs[strings.ToLower(a.Key)] = a.Val
			}
			switch tag {
			case "title":
				if n.FirstChild != nil {
					pkg.Title = strings.TrimSpace(n.FirstChild.Data)
				}
			case "canvas", "button", "div", "span", "input":
				item := html5DOMNode{Tag: tag, ID: attrs["id"], Dataset: map[string]string{}}
				for k, v := range attrs {
					if name, ok := strings.CutPrefix(k, "data-"); ok {
						item.Dataset[name] = v
					}
				}
				if tag == "canvas" {
					item.Width, _ = strconv.Atoi(attrs["width"])
					item.Height, _ = strconv.Atoi(attrs["height"])
					if pkg.Width == 0 {
						pkg.Width, pkg.Height = item.Width, item.Height
					}
				}
				pkg.Nodes = append(pkg.Nodes, item)
				if len(pkg.Nodes) > 5000 {
					return errors.New("this game has too many page elements for the in-app browser")
				}
			case "script":
				if scriptType := strings.ToLower(strings.TrimSpace(attrs["type"])); scriptType != "" &&
					scriptType != "text/javascript" && scriptType != "application/javascript" {
					break
				}
				if src := strings.TrimSpace(attrs["src"]); src != "" {
					if strings.Contains(src, ":") || strings.HasPrefix(src, "/") || strings.HasPrefix(src, "\\") {
						return fmt.Errorf("script source %q is not a local game file", src)
					}
					data, err := root.ReadFile(filepath.FromSlash(src))
					if err != nil {
						return fmt.Errorf("script %s: %w", src, err)
					}
					pkg.Scripts = append(pkg.Scripts, string(data))
				} else {
					var code strings.Builder
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						if c.Type == html.TextNode {
							code.WriteString(c.Data)
						}
					}
					if code.Len() > 0 {
						pkg.Scripts = append(pkg.Scripts, code.String())
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(tree); err != nil {
		return pkg, err
	}
	if pkg.Width < 1 || pkg.Height < 1 || pkg.Width > 2048 || pkg.Height > 2048 {
		return pkg, errors.New("this game needs a Canvas 2D board of at most 2048 pixels per side")
	}
	if len(pkg.Scripts) == 0 {
		return pkg, errors.New("this game has no playable JavaScript")
	}
	bytes := 0
	for _, script := range pkg.Scripts {
		bytes += len(script)
	}
	if bytes > html5ScriptLimit {
		return pkg, errors.New("this game's scripts are too large for the in-app browser")
	}
	return pkg, nil
}

// html5Point is a canvas-space point. A NaN X separates two subpaths
// (moveTo after other points), so one path can hold several shapes.
type html5Point struct{ X, Y float64 }

// html5Draw is one canvas call waiting to be drawn. Its colour is parsed once,
// when the game makes the call, and a path's points live in the view's points
// arena (P0..P1), so a frame of drawing allocates almost nothing.
type html5Draw struct {
	Kind, Color, Text, Font, Align string
	X, Y, W, H, LineWidth          float64
	RGBA                           sdl.Color
	P0, P1                         int
}

type html5GameView struct {
	game      libraryGame
	pkg       html5Package
	rt        *goja.Runtime
	step      goja.Callable
	dispatch  goja.Callable
	padSet    goja.Callable
	axisSet   goja.Callable
	commands  []html5Draw
	points    []html5Point
	path      []html5Point
	subStart  int
	colors    map[string]sdl.Color
	closed    bool
	errorText string
	axes      [2]float64
	axesDirty bool
	store     map[string]string
	storeFile string
	start     time.Time
	lastFrame time.Time
	board     sdl.Rect
	// canvas is the game's retained drawing surface: each frame only the
	// calls made since the last frame are drawn into it, the way a browser
	// canvas keeps its pixels, instead of replaying every call scaled onto
	// the screen. canvasFailed falls back to that replay where the renderer
	// has no target textures.
	canvas       *sdl.Texture
	canvasFailed bool
	noGeometry   bool
	hud          string
	hudAt        time.Time
	pressedKey   string
	// restoreCPU puts the hub's CPU profile back when the game closes: the
	// game runs inside JukaHub, so it gets the launch profile an emulator
	// would (powerprofile.go) instead of the menu's powersave clock.
	restoreCPU func()
	// Scratch buffers reused by the renderer every frame.
	verts   []sdl.Vertex
	indices []int32
	fpts    []sdl.FPoint
	rects   []sdl.Rect
	crosses []float64
}

var html5View *html5GameView

func html5SavePath(entry string) string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(html5PathKey(entry)))
	return filepath.Join(dir, "HTML5", "Saves", hex.EncodeToString(sum[:16])+".json")
}

func newHTML5GameView(game libraryGame) (*html5GameView, error) {
	pkg, err := loadHTML5Package(game.Path)
	if err != nil {
		return nil, err
	}
	v := &html5GameView{game: game, pkg: pkg, rt: goja.New(), store: map[string]string{}, storeFile: html5SavePath(game.Path),
		colors: map[string]sdl.Color{}, start: time.Now()}
	if data, err := os.ReadFile(v.storeFile); err == nil {
		_ = json.Unmarshal(data, &v.store)
	}
	if v.store == nil {
		v.store = map[string]string{}
	}
	_ = v.rt.Set("__jhDraw", v.drawCall)
	_ = v.rt.Set("__jhNow", func(goja.FunctionCall) goja.Value {
		return v.rt.ToValue(float64(time.Since(v.start)) / float64(time.Millisecond))
	})
	_ = v.rt.Set("__jhStoreGet", func(key string) goja.Value {
		if value, ok := v.store[key]; ok {
			return v.rt.ToValue(value)
		}
		return goja.Null()
	})
	_ = v.rt.Set("__jhStoreSet", func(key, value string) {
		if len(key)+len(value) > 16<<10 {
			return
		}
		v.store[key] = value
		data, _ := json.Marshal(v.store)
		if len(data) > 64<<10 || v.storeFile == "" {
			return
		}
		if os.MkdirAll(filepath.Dir(v.storeFile), 0o755) == nil {
			_ = AtomicWrite(v.storeFile, data, 0o644)
		}
	})
	nodes, _ := json.Marshal(pkg.Nodes)
	if _, err := v.run("const __jhNodeData = "+string(nodes)+";\n"+html5BrowserPrelude, 300*time.Millisecond); err != nil {
		return nil, fmt.Errorf("game browser setup: %w", err)
	}
	for i, script := range pkg.Scripts {
		if _, err := v.run(script, 500*time.Millisecond); err != nil {
			return nil, fmt.Errorf("game script %d: %w", i+1, err)
		}
	}
	v.step, _ = goja.AssertFunction(v.rt.Get("__jhStep"))
	v.dispatch, _ = goja.AssertFunction(v.rt.Get("__jhDispatch"))
	v.padSet, _ = goja.AssertFunction(v.rt.Get("__jhPadSet"))
	v.axisSet, _ = goja.AssertFunction(v.rt.Get("__jhAxisSet"))
	if v.step == nil || v.dispatch == nil || v.padSet == nil || v.axisSet == nil {
		return nil, errors.New("game browser did not start")
	}
	return v, nil
}

func (v *html5GameView) run(code string, limit time.Duration) (goja.Value, error) {
	timer := time.AfterFunc(limit, func() { v.rt.Interrupt("game script took too long") })
	result, err := v.rt.RunString(code)
	timer.Stop()
	v.rt.ClearInterrupt()
	return result, err
}

func (v *html5GameView) call(fn goja.Callable, args ...goja.Value) error {
	if fn == nil {
		return errors.New("missing game callback")
	}
	timer := time.AfterFunc(100*time.Millisecond, func() { v.rt.Interrupt("game frame took too long") })
	_, err := fn(goja.Undefined(), args...)
	timer.Stop()
	v.rt.ClearInterrupt()
	return err
}

// fail stops the game with err's message; later frames and input are ignored.
func (v *html5GameView) fail(err error) {
	if err != nil && v.errorText == "" {
		v.errorText = err.Error()
		// A stopped game no longer needs the performance clock while its
		// "Game stopped" card waits for B.
		if v.restoreCPU != nil {
			restore := v.restoreCPU
			v.restoreCPU = nil
			restore()
		}
	}
}

// html5ArcSweep is the signed angle an arc covers, as the Canvas spec reads
// arc(): a full turn when the angles are at least 2π apart the drawn way,
// otherwise the angles taken modulo 2π and walked in the asked direction.
func html5ArcSweep(start, end float64, ccw bool) float64 {
	const turn = 2 * math.Pi
	if !ccw && end-start >= turn {
		return turn
	}
	if ccw && start-end >= turn {
		return -turn
	}
	s, e := math.Mod(start, turn), math.Mod(end, turn)
	if s < 0 {
		s += turn
	}
	if e < 0 {
		e += turn
	}
	sweep := e - s
	if !ccw && sweep < 0 {
		sweep += turn
	} else if ccw && sweep > 0 {
		sweep -= turn
	}
	return sweep
}

// color parses a canvas colour once per distinct string: a game sets the same
// few fill styles hundreds of times a frame.
func (v *html5GameView) color(value string) sdl.Color {
	if c, ok := v.colors[value]; ok {
		return c
	}
	c := html5CanvasColor(value)
	if len(v.colors) >= 512 {
		clear(v.colors)
	}
	v.colors[value] = c
	return c
}

// resetCommands drops every queued call: the canvas was just covered whole,
// or the queue was drawn into the retained canvas.
func (v *html5GameView) resetCommands() {
	v.commands = v.commands[:0]
	v.points = v.points[:0]
}

func (v *html5GameView) appendDraw(cmd html5Draw) {
	if len(v.commands) < html5CommandLimit {
		v.commands = append(v.commands, cmd)
	}
}

// pathPoint adds a point to the current path. A moveTo after other points
// starts a new subpath behind a NaN separator.
func (v *html5GameView) pathPoint(x, y float64, move bool) {
	if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) || len(v.path) >= 8192 {
		return
	}
	if move && len(v.path) > 0 {
		v.path = append(v.path, html5Point{math.NaN(), 0})
	}
	if move || len(v.path) == 0 {
		v.subStart = len(v.path)
	}
	v.path = append(v.path, html5Point{x, y})
}

func (v *html5GameView) drawCall(call goja.FunctionCall) goja.Value {
	name := call.Argument(0).String()
	f := func(i int) float64 { return call.Argument(i).ToFloat() }
	s := func(i int) string { return call.Argument(i).String() }
	switch name {
	case "beginPath":
		v.path = v.path[:0]
		v.subStart = 0
	case "moveTo":
		v.pathPoint(f(1), f(2), true)
	case "lineTo":
		v.pathPoint(f(1), f(2), false)
	case "closePath":
		if n := len(v.path); n-v.subStart > 1 {
			v.path = append(v.path, v.path[v.subStart])
		}
	case "rect":
		x, y, w, h := f(1), f(2), f(3), f(4)
		v.pathPoint(x, y, true)
		v.pathPoint(x+w, y, false)
		v.pathPoint(x+w, y+h, false)
		v.pathPoint(x, y+h, false)
		v.path = append(v.path, html5Point{x, y})
	case "arc":
		x, y, radius, start, end := f(1), f(2), f(3), f(4), f(5)
		ccw := call.Argument(6).ToBoolean()
		if !(radius > 0 && radius < 4096) || math.IsNaN(start+end+x+y) || math.IsInf(start+end, 0) {
			break
		}
		sweep := html5ArcSweep(start, end, ccw)
		if sweep == 0 {
			v.pathPoint(x+math.Cos(start)*radius, y+math.Sin(start)*radius, false)
			break
		}
		steps := min(96, max(12, int(math.Abs(sweep)*radius/4)))
		for i := 0; i <= steps; i++ {
			a := start + sweep*float64(i)/float64(steps)
			v.pathPoint(x+math.Cos(a)*radius, y+math.Sin(a)*radius, false)
		}
	case "fillRect", "clearRect", "strokeRect":
		cmd := html5Draw{Kind: name, X: f(1), Y: f(2), W: f(3), H: f(4), Color: s(5), LineWidth: f(6)}
		cmd.RGBA = v.color(cmd.Color)
		// A call that covers the whole board with solid paint hides every
		// earlier one, so they are dropped instead of drawn. A translucent
		// fill (a pause overlay) keeps what is under it.
		if name != "strokeRect" && cmd.X <= 0 && cmd.Y <= 0 &&
			cmd.X+cmd.W >= float64(v.pkg.Width) && cmd.Y+cmd.H >= float64(v.pkg.Height) &&
			(name == "clearRect" || cmd.RGBA.A == 255) {
			v.resetCommands()
		}
		v.appendDraw(cmd)
	case "fill", "stroke":
		if len(v.path) > 1 && len(v.commands) < html5CommandLimit && len(v.points)+len(v.path) <= 1<<18 {
			cmd := html5Draw{Kind: name, Color: s(1), LineWidth: f(2), P0: len(v.points)}
			cmd.RGBA = v.color(cmd.Color)
			v.points = append(v.points, v.path...)
			cmd.P1 = len(v.points)
			v.appendDraw(cmd)
		}
	case "fillText":
		cmd := html5Draw{Kind: name, Text: s(1), X: f(2), Y: f(3), Color: s(4), Font: s(5), Align: s(6)}
		cmd.RGBA = v.color(cmd.Color)
		v.appendDraw(cmd)
	}
	return goja.Undefined()
}

func (v *html5GameView) event(kind, key string, x, y float64, buttons int) {
	if v == nil || v.closed || v.errorText != "" {
		return
	}
	v.fail(v.call(v.dispatch, v.rt.ToValue(kind), v.rt.ToValue(key), v.rt.ToValue(x), v.rt.ToValue(y), v.rt.ToValue(buttons)))
}

// padButton updates the standard-mapping gamepad the game polls.
func (v *html5GameView) padButton(index int, pressed bool) {
	if v == nil || v.closed || v.errorText != "" {
		return
	}
	v.fail(v.call(v.padSet, v.rt.ToValue(index), v.rt.ToValue(pressed)))
}

func (v *html5GameView) frame(now time.Time) {
	if v == nil || v.errorText != "" || v.closed {
		return
	}
	if !v.lastFrame.IsZero() && now.Sub(v.lastFrame) < 14*time.Millisecond {
		return
	}
	v.lastFrame = now
	// Stick motion arrives as a stream of events; the game only sees the
	// latest position, handed over once per frame.
	if v.axesDirty {
		v.axesDirty = false
		if err := v.call(v.axisSet, v.rt.ToValue(v.axes[0]), v.rt.ToValue(v.axes[1])); err != nil {
			v.fail(err)
			return
		}
	}
	// Without a canvas texture every queued call is replayed each frame, so
	// the queue holds one frame: games redraw their board every frame, and
	// one that never covers it whole would otherwise fill the queue and freeze.
	if v.canvasFailed {
		v.resetCommands()
	}
	v.fail(v.call(v.step, v.rt.ToValue(float64(now.Sub(v.start))/float64(time.Millisecond))))
}

func openHTML5GameInApp(game libraryGame) error {
	v, err := newHTML5GameView(game)
	if err != nil {
		return err
	}
	closeHTML5GameInApp()
	v.restoreCPU = beginLaunchPowerProfile(appConfig, game.Path)
	html5View = v
	gmetaNoteLaunch(game)
	recordGameLaunch(game)
	return nil
}

func closeHTML5GameInApp() {
	v := html5View
	if v == nil {
		return
	}
	v.closed = true
	html5View = nil
	if v.canvas != nil {
		_ = v.canvas.Destroy()
		v.canvas = nil
	}
	if v.restoreCPU != nil {
		restore := v.restoreCPU
		v.restoreCPU = nil
		restore()
	}
}

func html5GameActive() bool { return html5View != nil }

// html5FramePace is the main loop's sleep while a game is open: a 60 fps
// budget the frame's own cost comes out of, like a browser's animation frame.
// The menus' 24–30 fps cadence made games that move by the frame run at half
// speed, and ones that cap their time step (most do) crawl.
func html5FramePace(spent time.Duration) uint32 {
	const budget = 16 * time.Millisecond
	if spent >= budget {
		return 1
	}
	return uint32((budget - spent) / time.Millisecond)
}

func html5GameKey(e *sdl.KeyboardEvent) bool {
	if html5View == nil || e == nil {
		return false
	}
	if e.Type == sdl.KEYDOWN && (e.Keysym.Sym == sdl.K_ESCAPE || e.Keysym.Sym == sdl.K_BACKSPACE) {
		closeHTML5GameInApp()
		return true
	}
	key := html5KeyName(e.Keysym.Sym)
	if key != "" {
		kind := "keydown"
		if e.Type == sdl.KEYUP {
			kind = "keyup"
		}
		html5View.event(kind, key, 0, 0, 0)
	}
	return true
}

func html5KeyName(key sdl.Keycode) string {
	switch key {
	case sdl.K_UP:
		return "ArrowUp"
	case sdl.K_DOWN:
		return "ArrowDown"
	case sdl.K_LEFT:
		return "ArrowLeft"
	case sdl.K_RIGHT:
		return "ArrowRight"
	case sdl.K_RETURN:
		return "Enter"
	case sdl.K_SPACE:
		return " "
	}
	if key >= sdl.K_a && key <= sdl.K_z {
		return string(rune(key))
	}
	return ""
}

func html5GamePad(e *sdl.ControllerButtonEvent) bool {
	if html5View == nil || e == nil {
		return false
	}
	pressed := e.Type == sdl.CONTROLLERBUTTONDOWN
	if e.Button == sdl.CONTROLLER_BUTTON_B && pressed {
		closeHTML5GameInApp()
		return true
	}
	if index := html5WebGamepadButton(e.Button); index >= 0 {
		html5View.padButton(index, pressed)
	}
	key := ""
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		key = "ArrowUp"
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		key = "ArrowDown"
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		key = "ArrowLeft"
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		key = "ArrowRight"
	case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_START:
		key = "Enter"
	}
	if key != "" {
		kind := "keydown"
		if !pressed {
			kind = "keyup"
		}
		html5View.event(kind, key, 0, 0, 0)
	}
	return true
}

// SDL's button order differs from the standard Gamepad API after Y.
func html5WebGamepadButton(button uint8) int {
	switch button {
	case sdl.CONTROLLER_BUTTON_A:
		return 0
	case sdl.CONTROLLER_BUTTON_B:
		return 1
	case sdl.CONTROLLER_BUTTON_X:
		return 2
	case sdl.CONTROLLER_BUTTON_Y:
		return 3
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		return 4
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		return 5
	case sdl.CONTROLLER_BUTTON_BACK:
		return 8
	case sdl.CONTROLLER_BUTTON_START:
		return 9
	case sdl.CONTROLLER_BUTTON_LEFTSTICK:
		return 10
	case sdl.CONTROLLER_BUTTON_RIGHTSTICK:
		return 11
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return 12
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		return 13
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		return 14
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		return 15
	case sdl.CONTROLLER_BUTTON_GUIDE:
		return 16
	default:
		return -1
	}
}

func html5GameAxis(e *sdl.ControllerAxisEvent) bool {
	if html5View == nil || e == nil {
		return false
	}
	if e.Axis == sdl.CONTROLLER_AXIS_LEFTX || e.Axis == sdl.CONTROLLER_AXIS_LEFTY {
		value := max(-1, float64(e.Value)/32767)
		if html5View.axes[e.Axis] != value {
			html5View.axes[e.Axis] = value
			html5View.axesDirty = true
		}
	}
	return true
}

// html5BrowserPrelude is the small browser the games see. Everything a game
// calls every frame (getGamepads, performance.now, timers, animation frames)
// stays in JavaScript or makes one plain native call: the first version
// rebuilt the gamepad from JSON on every getGamepads and parsed each input
// event from JSON, which on the handheld cost more than the game's own logic.
const html5BrowserPrelude = `
const __nodes = __jhNodeData.map(n => {
  const obj = {tagName:n.tag.toUpperCase(), id:n.id||'', dataset:n.dataset||{}, width:n.width||0, height:n.height||0,
    textContent:'', style:{}, _listeners:{}, addEventListener(t,f){(this._listeners[t] ||= []).push(f)},
    removeEventListener(t,f){const l=this._listeners[t];if(l){const i=l.indexOf(f);if(i>=0)l.splice(i,1)}},
    setPointerCapture(){}, releasePointerCapture(){}, focus(){}, blur(){},
    getBoundingClientRect(){return {left:0,top:0,x:0,y:0,width:this.width,height:this.height,right:this.width,bottom:this.height}}};
  if(n.tag==='canvas') obj.getContext = kind => kind==='2d' ? __ctx : null;
  return obj;
});
const __ctx = {fillStyle:'#000000', strokeStyle:'#ffffff', lineWidth:1, font:'16px sans-serif', textAlign:'left',
  textBaseline:'alphabetic', globalAlpha:1, shadowColor:'transparent', shadowBlur:0, lineCap:'butt', lineJoin:'miter',
  fillRect(x,y,w,h){__jhDraw('fillRect',x,y,w,h,this.fillStyle,this.lineWidth)},
  clearRect(x,y,w,h){__jhDraw('clearRect',x,y,w,h,'#00000000',this.lineWidth)},
  strokeRect(x,y,w,h){__jhDraw('strokeRect',x,y,w,h,this.strokeStyle,this.lineWidth)},
  beginPath(){__jhDraw('beginPath')}, moveTo(x,y){__jhDraw('moveTo',x,y)}, lineTo(x,y){__jhDraw('lineTo',x,y)},
  rect(x,y,w,h){__jhDraw('rect',x,y,w,h)},
  arc(x,y,r,a,b,ccw){__jhDraw('arc',x,y,r,a,b,!!ccw)}, closePath(){__jhDraw('closePath')},
  fill(){__jhDraw('fill',this.fillStyle,this.lineWidth)}, stroke(){__jhDraw('stroke',this.strokeStyle,this.lineWidth)},
  fillText(t,x,y){__jhDraw('fillText',String(t),x,y,this.fillStyle,this.font,this.textAlign)},
  measureText(t){return {width:String(t).length*12}},
  save(){}, restore(){}, setLineDash(){},
};
__nodes.forEach(n => { if(n.tagName==='CANVAS') __ctx.canvas = __ctx.canvas || n; });
const __windowEvents = {};
globalThis.window = globalThis;
globalThis.addEventListener = (t,f) => (__windowEvents[t] ||= []).push(f);
globalThis.removeEventListener = (t,f) => {const l=__windowEvents[t];if(l){const i=l.indexOf(f);if(i>=0)l.splice(i,1)}};
globalThis.document = {
  getElementById(id){return __nodes.find(n=>n.id===id)||null},
  querySelector(q){if(q[0]==='#') return __nodes.find(n=>n.id===q.slice(1))||null;
    return __nodes.find(n=>n.tagName.toLowerCase()===q.toLowerCase())||null},
  querySelectorAll(q){if(q.startsWith('[data-')) {const key=q.slice(6,-1);return __nodes.filter(n=>Object.prototype.hasOwnProperty.call(n.dataset,key))}
    return __nodes.filter(n=>n.tagName.toLowerCase()===q.toLowerCase())},
  addEventListener(t,f){(__windowEvents[t] ||= []).push(f)},
  removeEventListener(t,f){globalThis.removeEventListener(t,f)},
  hidden:false, visibilityState:'visible',
};
const __pad = {id:'JukaHub controller (STANDARD GAMEPAD)', index:0, connected:true, mapping:'standard', timestamp:0, axes:[0,0,0,0], buttons:[]};
for (let i = 0; i < 17; i++) __pad.buttons.push({pressed:false, touched:false, value:0});
const __pads = [__pad];
globalThis.navigator = {getGamepads(){return __pads}, userAgent:'JukaHub', maxTouchPoints:1};
globalThis.__jhPadSet = (i,p) => {const b=__pad.buttons[i];if(b){b.pressed=b.touched=p;b.value=p?1:0;__pad.timestamp=performance.now()}};
globalThis.__jhAxisSet = (x,y) => {__pad.axes[0]=x;__pad.axes[1]=y;__pad.timestamp=performance.now()};
globalThis.localStorage = {getItem(k){return __jhStoreGet(String(k))},setItem(k,v){__jhStoreSet(String(k),String(v))},removeItem(){}};
globalThis.performance = {now:__jhNow};
globalThis.devicePixelRatio = 1;
globalThis.innerWidth = __nodes.find(n=>n.tagName==='CANVAS')?.width||960;
globalThis.innerHeight = __nodes.find(n=>n.tagName==='CANVAS')?.height||540;
let __raf = new Map(), __rafId = 0;
const __timers = new Map();
let __nextTimer = 1;
globalThis.setTimeout = (f,ms=0,...a) => {const id=__nextTimer++;__timers.set(id,{f,a,due:__jhNow()+Math.max(0,+ms||0),repeat:0});return id};
globalThis.setInterval = (f,ms=0,...a) => {const id=__nextTimer++;const repeat=Math.max(16,+ms||16);__timers.set(id,{f,a,due:__jhNow()+repeat,repeat});return id};
globalThis.clearTimeout = globalThis.clearInterval = id => {__timers.delete(id)};
globalThis.requestAnimationFrame = f => {const id=++__rafId;__raf.set(id,f);return id};
globalThis.cancelAnimationFrame = id => {__raf.delete(id)};
globalThis.__jhStep = now => {
  if (__timers.size) {
    let n = 0;
    for (const [id,t] of __timers) {
      if (t.due > now) continue;
      if (++n > 32) break;
      if (t.repeat) t.due = now + t.repeat; else __timers.delete(id);
      if (typeof t.f === 'function') t.f(...t.a);
    }
  }
  if (__raf.size) {
    const batch = __raf; __raf = new Map();
    let n = 0;
    for (const [id,f] of batch) {
      // Past 16 callbacks a frame the rest wait for the next one.
      if (++n > 16) { __raf.set(id, f); continue; }
      f(now);
    }
  }
};
const __keyCodes = {ArrowLeft:37, ArrowUp:38, ArrowRight:39, ArrowDown:40, Enter:13, ' ':32};
const __keyNames = {ArrowLeft:'ArrowLeft', ArrowUp:'ArrowUp', ArrowRight:'ArrowRight', ArrowDown:'ArrowDown', Enter:'Enter', ' ':'Space'};
const __noop = () => {};
globalThis.__jhDispatch = (kind, key, x, y, buttons) => {
  const code = __keyNames[key] || (key.length === 1 ? 'Key' + key.toUpperCase() : '');
  const keyCode = __keyCodes[key] || (key.length === 1 ? key.toUpperCase().charCodeAt(0) : 0);
  const e = {type:kind, key, code, keyCode, which:keyCode, repeat:false, clientX:x, clientY:y, offsetX:x, offsetY:y, pageX:x, pageY:y,
    buttons, button:0, pointerId:1, pointerType:'touch', isPrimary:true, preventDefault:__noop, stopPropagation:__noop};
  const w = __windowEvents[kind];
  if (w) for (const f of w.slice()) f(e);
  if (kind.startsWith('pointer')) for (const n of __nodes) { if (n.tagName !== 'CANVAS') continue; const l = n._listeners[kind]; if (l) for (const f of l.slice()) f(e); }
};
globalThis.__jhHud = () => JSON.stringify(__nodes.filter(n=>['score','lives','best'].includes(n.id)).map(n=>[n.id,String(n.textContent)]));
`
