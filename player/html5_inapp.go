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

type html5Point struct{ X, Y float64 }
type html5Draw struct {
	Kind, Color, Text, Font, Align string
	X, Y, W, H, LineWidth          float64
	Points                         []html5Point
}

type html5GameView struct {
	game       libraryGame
	pkg        html5Package
	rt         *goja.Runtime
	step       goja.Callable
	dispatch   goja.Callable
	commands   []html5Draw
	path       []html5Point
	closed     bool
	errorText  string
	buttons    [17]bool
	axes       [2]float64
	store      map[string]string
	storeFile  string
	lastFrame  time.Time
	board      sdl.Rect
	pressedKey string
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
	v := &html5GameView{game: game, pkg: pkg, rt: goja.New(), store: map[string]string{}, storeFile: html5SavePath(game.Path)}
	if data, err := os.ReadFile(v.storeFile); err == nil {
		_ = json.Unmarshal(data, &v.store)
	}
	if v.store == nil {
		v.store = map[string]string{}
	}
	_ = v.rt.Set("__jhDraw", v.drawCall)
	_ = v.rt.Set("__jhPadState", v.padState)
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
	if v.step == nil || v.dispatch == nil {
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

func (v *html5GameView) padState() string {
	buttons := make([]map[string]bool, len(v.buttons))
	for i, pressed := range v.buttons {
		buttons[i] = map[string]bool{"pressed": pressed}
	}
	data, _ := json.Marshal([]any{map[string]any{"axes": v.axes[:], "buttons": buttons}})
	return string(data)
}

func (v *html5GameView) appendDraw(cmd html5Draw) {
	if len(v.commands) < html5CommandLimit {
		v.commands = append(v.commands, cmd)
	}
}

func (v *html5GameView) drawCall(call goja.FunctionCall) goja.Value {
	name := call.Argument(0).String()
	f := func(i int) float64 { return call.Argument(i).ToFloat() }
	s := func(i int) string { return call.Argument(i).String() }
	switch name {
	case "beginPath":
		v.path = nil
	case "moveTo":
		v.path = append(v.path, html5Point{f(1), f(2)})
	case "lineTo":
		v.path = append(v.path, html5Point{f(1), f(2)})
	case "closePath":
		if len(v.path) > 1 {
			v.path = append(v.path, v.path[0])
		}
	case "arc":
		x, y, radius, start, end := f(1), f(2), f(3), f(4), f(5)
		if radius > 0 && radius < 4096 && end > start {
			steps := min(96, max(12, int((end-start)*radius/4)))
			for i := 0; i <= steps; i++ {
				a := start + (end-start)*float64(i)/float64(steps)
				v.path = append(v.path, html5Point{x + math.Cos(a)*radius, y + math.Sin(a)*radius})
			}
		}
	case "fillRect", "clearRect", "strokeRect":
		cmd := html5Draw{Kind: name, X: f(1), Y: f(2), W: f(3), H: f(4), Color: s(5), LineWidth: f(6)}
		if cmd.X <= 0 && cmd.Y <= 0 && cmd.W >= float64(v.pkg.Width) && cmd.H >= float64(v.pkg.Height) {
			v.commands = nil
		}
		v.appendDraw(cmd)
	case "fill", "stroke":
		if len(v.path) > 1 {
			v.appendDraw(html5Draw{Kind: name, Points: append([]html5Point(nil), v.path...), Color: s(1), LineWidth: f(2)})
		}
	case "fillText":
		v.appendDraw(html5Draw{Kind: name, Text: s(1), X: f(2), Y: f(3), Color: s(4), Font: s(5), Align: s(6)})
	}
	return goja.Undefined()
}

func (v *html5GameView) event(kind, key string, x, y float64, buttons int) {
	if v == nil || v.closed || v.errorText != "" {
		return
	}
	data, _ := json.Marshal(map[string]any{"key": key, "clientX": x, "clientY": y, "buttons": buttons, "pointerType": "touch"})
	if err := v.call(v.dispatch, v.rt.ToValue(kind), v.rt.ToValue(string(data))); err != nil {
		v.errorText = err.Error()
	}
}

func (v *html5GameView) frame(now time.Time) {
	if v == nil || v.errorText != "" || v.closed {
		return
	}
	if !v.lastFrame.IsZero() && now.Sub(v.lastFrame) < 14*time.Millisecond {
		return
	}
	v.lastFrame = now
	if err := v.call(v.step, v.rt.ToValue(float64(now.UnixNano())/1e6)); err != nil {
		v.errorText = err.Error()
	}
}

func openHTML5GameInApp(game libraryGame) error {
	v, err := newHTML5GameView(game)
	if err != nil {
		return err
	}
	html5View = v
	gmetaNoteLaunch(game)
	recordGameLaunch(game)
	return nil
}

func closeHTML5GameInApp() {
	if html5View != nil {
		html5View.closed = true
		html5View = nil
	}
}

func html5GameActive() bool { return html5View != nil }

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
		html5View.buttons[index] = pressed
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
		html5View.axes[e.Axis] = float64(e.Value) / 32767
	}
	return true
}

const html5BrowserPrelude = `
const __nodes = __jhNodeData.map(n => {
  const obj = {tagName:n.tag.toUpperCase(), id:n.id||'', dataset:n.dataset||{}, width:n.width||0, height:n.height||0,
    textContent:'', _listeners:{}, addEventListener(t,f){(this._listeners[t] ||= []).push(f)},
    setPointerCapture(){}, getBoundingClientRect(){return {left:0,top:0,width:this.width,height:this.height}}};
  if(n.tag==='canvas') obj.getContext = kind => kind==='2d' ? __ctx : null;
  return obj;
});
const __ctx = {fillStyle:'#000000', strokeStyle:'#ffffff', lineWidth:1, font:'16px sans-serif', textAlign:'left',
  shadowColor:'transparent', shadowBlur:0,
  fillRect(x,y,w,h){__jhDraw('fillRect',x,y,w,h,this.fillStyle,this.lineWidth)},
  clearRect(x,y,w,h){__jhDraw('clearRect',x,y,w,h,'#000000',this.lineWidth)},
  strokeRect(x,y,w,h){__jhDraw('strokeRect',x,y,w,h,this.strokeStyle,this.lineWidth)},
  beginPath(){__jhDraw('beginPath')}, moveTo(x,y){__jhDraw('moveTo',x,y)}, lineTo(x,y){__jhDraw('lineTo',x,y)},
  arc(x,y,r,a,b){__jhDraw('arc',x,y,r,a,b)}, closePath(){__jhDraw('closePath')},
  fill(){__jhDraw('fill',this.fillStyle,this.lineWidth)}, stroke(){__jhDraw('stroke',this.strokeStyle,this.lineWidth)},
  fillText(t,x,y){__jhDraw('fillText',String(t),x,y,this.fillStyle,this.font,this.textAlign)},
  measureText(t){return {width:String(t).length*12}},
};
const __windowEvents = {};
globalThis.window = globalThis;
globalThis.addEventListener = (t,f) => (__windowEvents[t] ||= []).push(f);
globalThis.document = {
  getElementById(id){return __nodes.find(n=>n.id===id)||null},
  querySelector(q){if(q[0]==='#') return __nodes.find(n=>n.id===q.slice(1))||null;
    return __nodes.find(n=>n.tagName.toLowerCase()===q.toLowerCase())||null},
  querySelectorAll(q){if(q.startsWith('[data-')) {const key=q.slice(6,-1);return __nodes.filter(n=>Object.prototype.hasOwnProperty.call(n.dataset,key))}
    return __nodes.filter(n=>n.tagName.toLowerCase()===q.toLowerCase())},
  addEventListener(t,f){(__windowEvents[t] ||= []).push(f)},
};
globalThis.navigator = {getGamepads(){return JSON.parse(__jhPadState())}};
globalThis.localStorage = {getItem(k){return __jhStoreGet(String(k))},setItem(k,v){__jhStoreSet(String(k),String(v))}};
globalThis.performance = {now(){return Date.now()}};
globalThis.innerWidth = __nodes.find(n=>n.tagName==='CANVAS')?.width||960;
globalThis.innerHeight = __nodes.find(n=>n.tagName==='CANVAS')?.height||540;
let __raf = [];
let __timers = [], __nextTimer = 1;
globalThis.setTimeout = (f,ms=0) => {const id=__nextTimer++;__timers.push({id,f,due:Date.now()+Math.max(0,+ms||0),repeat:0});return id};
globalThis.setInterval = (f,ms=0) => {const id=__nextTimer++;const repeat=Math.max(16,+ms||16);__timers.push({id,f,due:Date.now()+repeat,repeat});return id};
globalThis.clearTimeout = globalThis.clearInterval = id => {__timers=__timers.filter(t=>t.id!==id)};
globalThis.requestAnimationFrame = f => {__raf.push(f);return __raf.length};
globalThis.cancelAnimationFrame = () => {};
globalThis.__jhStep = now => {
  const due=__timers.filter(t=>t.due<=now).slice(0,32);
  __timers=__timers.filter(t=>!due.includes(t));
  for(const t of due){t.f();if(t.repeat){t.due=now+t.repeat;__timers.push(t)}}
  const batch=__raf;__raf=[];for(const f of batch.slice(0,16))f(now);
};
globalThis.__jhDispatch = (kind, json) => {
  const e=JSON.parse(json);e.preventDefault=()=>{};e.stopPropagation=()=>{};
  for(const f of __windowEvents[kind]||[])f(e);
  if(kind.startsWith('pointer')) for(const n of __nodes.filter(n=>n.tagName==='CANVAS')) for(const f of n._listeners[kind]||[])f(e);
};
globalThis.__jhHud = () => JSON.stringify(__nodes.filter(n=>['score','lives','best'].includes(n.id)).map(n=>[n.id,String(n.textContent)]));
`
