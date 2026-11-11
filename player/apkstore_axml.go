package main

// apkstore_axml.go — what an APK says about itself, read without Android.
//
// An APK's AndroidManifest.xml is not text: it is Android's binary XML (a
// chunk stream with a string pool and typed attribute values), and the app's
// name and icon are usually references into resources.arsc. The APK Store needs
// a few facts out of both — the package, the version, the label, the launcher
// activity, NativeActivity's library name, the GLES version and the icon file —
// to tell a native game from a Java app before anything is staged.
//
// The layouts are the ones in AOSP's ResourceTypes.h. An APK comes from a phone
// or a download, so every read here is bounded and checked: a malformed or
// hostile file produces an error, never a panic, a huge allocation or a long
// loop. Counts are capped, every offset is checked against its chunk, and the
// entry points recover from anything that slips through.
//
// Memory is bounded as well as offsets. A pool string is decoded at most once
// per parse and shared from then on (a file that points thousands of
// attributes at one long string costs one decode, not thousands), the text a
// pool may decode in one parse has a budget, attribute values are decoded only
// when the walk asks for them, and a resource lists at most apkxMaxResValues
// configurations. Running out of memory is not a panic — no recover() catches
// it — so these bounds are what keep a hostile APK from taking the hub down.

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	apkxMaxManifestBytes = 4 << 20  // a real manifest is a few KiB to a few hundred
	apkxMaxArscBytes     = 24 << 20 // big games carry multi-MiB resource tables
	apkxMaxStrings       = 400000
	apkxMaxStringUnits   = 16 << 10 // one string, in UTF-16 units or UTF-8 bytes
	apkxMaxDepth         = 64
	apkxMaxElements      = 200000
	apkxMaxActivities    = 256
	apkxMaxRefHops       = 4
	// apkxMaxAttrs is how many attributes of one element are read; aapt writes
	// fewer than 40, so the rest of a longer list is ignored.
	apkxMaxAttrs = 128
	// apkxMaxDecoded is the text (in bytes) one pool may decode in one parse;
	// past it every further string reads as "".
	apkxMaxDecoded = 8 << 20
	// apkxMaxMemo bounds the remembered strings; past it strings are still
	// decoded (and charged to the budget) but no longer kept.
	apkxMaxMemo = 1 << 16
	// apkxMaxResValues bounds the configurations read for one resource.
	apkxMaxResValues = 256
)

// Chunk types (ResourceTypes.h).
const (
	apkxChunkStringPool   = 0x0001
	apkxChunkTable        = 0x0002
	apkxChunkXML          = 0x0003
	apkxChunkStartElement = 0x0102
	apkxChunkEndElement   = 0x0103
	apkxChunkResourceMap  = 0x0180
	apkxChunkPackage      = 0x0200
	apkxChunkType         = 0x0201
)

// Res_value data types the manifest uses.
const (
	apkxTypeNull      = 0x00
	apkxTypeReference = 0x01
	apkxTypeString    = 0x03
	apkxTypeIntDec    = 0x10
	apkxTypeIntHex    = 0x11
	apkxTypeIntBool   = 0x12
)

// ResTable_type and ResTable_entry flags.
const (
	apkxTypeFlagSparse   = 0x01
	apkxTypeFlagOffset16 = 0x02
	apkxEntryFlagComplex = 0x0001
	apkxEntryFlagCompact = 0x0008
)

// apkxAttrByID names the framework attributes the decoder reads when a
// shrunk manifest drops the attribute name strings and keeps only the
// resource map (android.R.attr values).
var apkxAttrByID = map[uint32]string{
	0x01010001: "label",
	0x01010002: "icon",
	0x01010003: "name",
	0x0101000c: "hasCode",
	0x01010024: "value",
	0x01010202: "targetActivity",
	0x0101020c: "minSdkVersion",
	0x0101021b: "versionCode",
	0x0101021c: "versionName",
	0x01010270: "targetSdkVersion",
	0x01010281: "glEsVersion",
}

// apkManifest is what the store reads out of AndroidManifest.xml.
type apkManifest struct {
	Package     string
	VersionName string
	VersionCode int64
	MinSDK      int
	TargetSDK   int
	// Label is the app's name when it is written inline; LabelRef is the
	// resource id when it is a reference (resolved through resources.arsc).
	Label    string
	LabelRef uint32
	IconRef  uint32
	// HasCode is false when <application android:hasCode="false">: an app
	// with no Java code at all, which is exactly what a pure NDK game is.
	HasCode bool
	// Launcher is the fully qualified class of the activity (or the target
	// of the activity-alias) that has the MAIN/LAUNCHER intent filter.
	Launcher string
	// NativeLib is NativeActivity's android.app.lib_name meta-data ("main" →
	// libmain.so), read from the launcher activity, else from any activity.
	NativeLib string
	// GLES is <uses-feature android:glEsVersion>: 0x00030000 is OpenGL ES 3.0.
	GLES uint32
	// Activities lists every declared activity class (bounded), for the
	// engine guess: UnityPlayerActivity, SDLActivity, GodotApp and friends.
	Activities []string
	// Split is <manifest split="…">: this file is one piece of a split app
	// (an App Bundle install), such as "config.arm64_v8a". SplitsRequired is
	// the base piece saying it does not work without its splits
	// (android:isSplitRequired, android:requiredSplitTypes, or Play's
	// com.android.vending.splits.required meta-data). Either way the file
	// cannot run on its own.
	Split          string
	SplitsRequired bool
}

// apkxValue is one typed attribute value. Its strings stay pool indices until
// text() asks for them, so an attribute nobody reads costs no decoding.
type apkxValue struct {
	pool     *apkxPool
	rawIdx   uint32 // the raw string value's index, 0xFFFFFFFF for none
	dataType uint8
	data     uint32
}

// text is the value as a string: the inline string, else the raw one, else a
// decimal or hex number for integer types.
func (v apkxValue) text() string {
	if v.dataType == apkxTypeString {
		return v.pool.get(v.data)
	}
	if raw := v.pool.get(v.rawIdx); raw != "" {
		return raw
	}
	switch v.dataType {
	case apkxTypeIntDec:
		return strconv.FormatInt(int64(int32(v.data)), 10)
	case apkxTypeIntHex:
		return "0x" + strconv.FormatUint(uint64(v.data), 16)
	case apkxTypeIntBool:
		if v.data != 0 {
			return "true"
		}
		return "false"
	}
	return ""
}

// integer is the value as a number, from an integer type or a numeric string.
func (v apkxValue) integer() (int64, bool) {
	switch v.dataType {
	case apkxTypeIntDec, apkxTypeIntHex:
		return int64(int32(v.data)), true
	}
	s := strings.TrimSpace(v.text())
	if s == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(s, 0, 64); err == nil {
		return n, true
	}
	return 0, false
}

// boolean is the value as a bool, defaulting to def when it is neither a
// boolean nor "true"/"false".
func (v apkxValue) boolean(def bool) bool {
	if v.dataType == apkxTypeIntBool {
		return v.data != 0
	}
	switch strings.ToLower(strings.TrimSpace(v.text())) {
	case "true":
		return true
	case "false":
		return false
	}
	return def
}

// ── Little-endian reads with bounds ─────────────────────────────────────────

var errApkxShort = errors.New("truncated binary XML")

func apkxU16(b []byte, off int) (uint16, bool) {
	if off < 0 || off+2 > len(b) {
		return 0, false
	}
	return binary.LittleEndian.Uint16(b[off:]), true
}

func apkxU32(b []byte, off int) (uint32, bool) {
	if off < 0 || off+4 > len(b) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(b[off:]), true
}

// apkxChunk reads the 8-byte chunk header at off inside b and returns the
// chunk (header included) or an error when it does not fit.
func apkxChunk(b []byte, off int) (typ uint16, headerSize int, chunk []byte, err error) {
	t, ok1 := apkxU16(b, off)
	h, ok2 := apkxU16(b, off+2)
	size, ok3 := apkxU32(b, off+4)
	if !ok1 || !ok2 || !ok3 {
		return 0, 0, nil, errApkxShort
	}
	if size < 8 || int(h) < 8 || int64(h) > int64(size) || int64(off)+int64(size) > int64(len(b)) {
		return 0, 0, nil, fmt.Errorf("bad chunk 0x%04x at %d (size %d)", t, off, size)
	}
	return t, int(h), b[off : off+int(size)], nil
}

// ── String pools ────────────────────────────────────────────────────────────

// apkxPool is a string pool decoded lazily: a resource table can hold tens of
// thousands of strings and the store needs two of them. Each string is decoded
// at most once (memo) and the pool's whole decoding is charged to a budget.
type apkxPool struct {
	chunk        []byte
	count        int
	offsetsStart int
	stringsStart int
	utf8         bool
	memo         map[uint32]string
	budget       int // bytes of text this pool may still decode
}

// apkxParsePool validates a ResStringPool chunk.
func apkxParsePool(chunk []byte, headerSize int) (*apkxPool, error) {
	count, ok1 := apkxU32(chunk, 8)
	flags, ok2 := apkxU32(chunk, 16)
	stringsStart, ok3 := apkxU32(chunk, 20)
	if !ok1 || !ok2 || !ok3 || headerSize < 28 {
		return nil, errors.New("bad string pool header")
	}
	if count > apkxMaxStrings {
		return nil, fmt.Errorf("string pool holds %d strings", count)
	}
	if int64(headerSize)+4*int64(count) > int64(len(chunk)) {
		return nil, errors.New("string pool offsets run past the chunk")
	}
	if count > 0 && int64(stringsStart) >= int64(len(chunk)) {
		return nil, errors.New("string pool data starts past the chunk")
	}
	return &apkxPool{
		chunk:        chunk,
		count:        int(count),
		offsetsStart: headerSize,
		stringsStart: int(stringsStart),
		utf8:         flags&(1<<8) != 0,
		budget:       apkxMaxDecoded,
	}, nil
}

// get returns string i, or "" for an index or an entry that does not fit, and
// "" once the pool's decoding budget is spent. Repeated indices share the one
// string decoded the first time.
func (p *apkxPool) get(i uint32) string {
	if p == nil || i == 0xFFFFFFFF || int64(i) >= int64(p.count) {
		return ""
	}
	if s, ok := p.memo[i]; ok {
		return s
	}
	s := p.decode(i)
	if len(s) > p.budget {
		s = ""
		p.budget = 0
	}
	p.budget -= len(s)
	if p.memo == nil {
		p.memo = make(map[uint32]string)
	}
	if len(p.memo) < apkxMaxMemo {
		p.memo[i] = s
	}
	return s
}

// decode reads string i from the chunk; get is the caller.
func (p *apkxPool) decode(i uint32) string {
	if p.budget <= 0 {
		return ""
	}
	off, ok := apkxU32(p.chunk, p.offsetsStart+4*int(i))
	if !ok {
		return ""
	}
	pos := int64(p.stringsStart) + int64(off)
	if pos < 0 || pos >= int64(len(p.chunk)) {
		return ""
	}
	if p.utf8 {
		return apkxUTF8At(p.chunk, int(pos))
	}
	return apkxUTF16At(p.chunk, int(pos))
}

// apkxUTF8At decodes a UTF-8 pool entry: the UTF-16 length, the byte length
// (each one or two bytes, the high bit of the first byte marking two), then
// the bytes.
func apkxUTF8At(b []byte, pos int) string {
	lenField := func() (int, bool) {
		if pos >= len(b) {
			return 0, false
		}
		v := int(b[pos])
		pos++
		if v&0x80 != 0 {
			if pos >= len(b) {
				return 0, false
			}
			v = (v&0x7F)<<8 | int(b[pos])
			pos++
		}
		return v, true
	}
	if _, ok := lenField(); !ok { // the UTF-16 length is not needed
		return ""
	}
	n, ok := lenField()
	if !ok || n > apkxMaxStringUnits {
		return ""
	}
	if pos+n > len(b) {
		n = len(b) - pos
	}
	if n <= 0 {
		return ""
	}
	return strings.ToValidUTF8(string(b[pos:pos+n]), "�")
}

// apkxUTF16At decodes a UTF-16 pool entry: the length in units (one or two
// uint16s, the high bit of the first marking two), then the units.
func apkxUTF16At(b []byte, pos int) string {
	first, ok := apkxU16(b, pos)
	if !ok {
		return ""
	}
	n := int(first)
	pos += 2
	if first&0x8000 != 0 {
		second, ok := apkxU16(b, pos)
		if !ok {
			return ""
		}
		n = int(first&0x7FFF)<<16 | int(second)
		pos += 2
	}
	if n > apkxMaxStringUnits {
		n = apkxMaxStringUnits
	}
	if avail := (len(b) - pos) / 2; n > avail {
		n = avail
	}
	if n <= 0 {
		return ""
	}
	// Straight to UTF-8, with no unit or rune slice in between; a surrogate
	// that is not part of a pair becomes U+FFFD, as utf16.Decode does.
	out := make([]byte, 0, n)
	for i := 0; i < n; i++ {
		u := rune(binary.LittleEndian.Uint16(b[pos+2*i:]))
		if !utf16.IsSurrogate(u) {
			out = utf8.AppendRune(out, u)
			continue
		}
		if i+1 < n {
			if r := utf16.DecodeRune(u, rune(binary.LittleEndian.Uint16(b[pos+2*i+2:]))); r != utf8.RuneError {
				out = utf8.AppendRune(out, r)
				i++
				continue
			}
		}
		out = utf8.AppendRune(out, utf8.RuneError)
	}
	return string(out)
}

// ── Binary XML ──────────────────────────────────────────────────────────────

// apkxAttr is one attribute of a start element, by canonical name.
type apkxAttr struct {
	name  string
	value apkxValue
}

// apkxWalkXML calls visit for every start element (depth 0 is the root) and
// leave for every end element. Errors stop the walk.
func apkxWalkXML(data []byte, visit func(depth int, name string, attrs []apkxAttr), leave func(depth int, name string)) error {
	typ, headerSize, root, err := apkxChunk(data, 0)
	if err != nil {
		return err
	}
	if typ != apkxChunkXML {
		return fmt.Errorf("not binary XML (chunk 0x%04x)", typ)
	}
	var pool *apkxPool
	var resMap []uint32
	depth := 0
	elements := 0
	attrs := make([]apkxAttr, 0, 16)
	for off := headerSize; off < len(root); {
		ctyp, chs, chunk, err := apkxChunk(root, off)
		if err != nil {
			return err
		}
		off += len(chunk)
		switch ctyp {
		case apkxChunkStringPool:
			if pool == nil {
				if pool, err = apkxParsePool(chunk, chs); err != nil {
					return err
				}
			}
		case apkxChunkResourceMap:
			n := (len(chunk) - chs) / 4
			if n > apkxMaxStrings {
				n = apkxMaxStrings
			}
			resMap = make([]uint32, 0, n)
			for i := 0; i < n; i++ {
				v, _ := apkxU32(chunk, chs+4*i)
				resMap = append(resMap, v)
			}
		case apkxChunkStartElement:
			elements++
			if elements > apkxMaxElements {
				return errors.New("too many elements")
			}
			if depth >= apkxMaxDepth {
				return errors.New("elements nested too deep")
			}
			// ResXMLTree_attrExt follows the 16-byte node header.
			ext := chs
			nameIdx, ok1 := apkxU32(chunk, ext+4)
			attrStart, ok2 := apkxU16(chunk, ext+8)
			attrSize, ok3 := apkxU16(chunk, ext+10)
			attrCount, ok4 := apkxU16(chunk, ext+12)
			if !ok1 || !ok2 || !ok3 || !ok4 {
				return errApkxShort
			}
			if attrSize < 20 {
				attrSize = 20
			}
			attrs = attrs[:0]
			base := ext + int(attrStart)
			n := int(attrCount)
			if n > apkxMaxAttrs {
				n = apkxMaxAttrs
			}
			for i := 0; i < n; i++ {
				a := base + i*int(attrSize)
				an, okA := apkxU32(chunk, a+4)
				raw, okB := apkxU32(chunk, a+8)
				dt := 0
				if a+15 < len(chunk) {
					dt = int(chunk[a+15])
				}
				data, okC := apkxU32(chunk, a+16)
				if !okA || !okB || !okC {
					break
				}
				var name string
				if int64(an) < int64(len(resMap)) {
					name = apkxAttrByID[resMap[an]]
				}
				if name == "" {
					name = pool.get(an)
				}
				// The values stay indices: only the attributes the visitor
				// reads are ever decoded.
				v := apkxValue{pool: pool, rawIdx: raw, dataType: uint8(dt), data: data}
				attrs = append(attrs, apkxAttr{name: name, value: v})
			}
			if visit != nil {
				visit(depth, pool.get(nameIdx), attrs)
			}
			depth++
		case apkxChunkEndElement:
			if depth > 0 {
				depth--
			}
			if leave != nil {
				nameIdx, _ := apkxU32(chunk, chs+4)
				leave(depth, pool.get(nameIdx))
			}
		}
	}
	return nil
}

// apkxAttrValue finds an attribute by canonical name.
func apkxAttrValue(attrs []apkxAttr, name string) (apkxValue, bool) {
	for _, a := range attrs {
		if a.name == name {
			return a.value, true
		}
	}
	return apkxValue{}, false
}

// apkxQualify turns a manifest class name into a full one: ".Main" and "Main"
// are relative to the package.
func apkxQualify(pkg, class string) string {
	class = strings.TrimSpace(class)
	switch {
	case class == "":
		return ""
	case strings.HasPrefix(class, "."):
		return pkg + class
	case !strings.Contains(class, ".") && pkg != "":
		return pkg + "." + class
	}
	return class
}

// parseApkManifest decodes AndroidManifest.xml. It recovers from a panic in
// the walk and reports it as an error: a bad file is a verdict, not a crash.
func parseApkManifest(data []byte) (m apkManifest, err error) {
	defer func() {
		if r := recover(); r != nil {
			m, err = apkManifest{}, fmt.Errorf("unreadable manifest (%v)", r)
		}
	}()
	m.HasCode = true
	type activityState struct {
		class    string
		libName  string
		launcher bool
	}
	var stack []string
	var cur *activityState
	// One intent filter's MAIN action and LAUNCHER category, in either order:
	// the filter is judged when it ends.
	var inFilter, filterMain, filterLauncher bool
	var anyLibName string
	visit := func(depth int, name string, attrs []apkxAttr) {
		parent := ""
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		stack = append(stack, name)
		switch name {
		case "manifest":
			if depth != 0 {
				return
			}
			if v, ok := apkxAttrValue(attrs, "package"); ok {
				m.Package = strings.TrimSpace(v.text())
			}
			if v, ok := apkxAttrValue(attrs, "versionName"); ok {
				m.VersionName = strings.TrimSpace(v.text())
			}
			if v, ok := apkxAttrValue(attrs, "versionCode"); ok {
				if n, ok := v.integer(); ok {
					m.VersionCode = n
				}
			}
			// A split piece names itself (split="config.arm64_v8a"); the
			// attribute is not in the android namespace, so it is read by name.
			if v, ok := apkxAttrValue(attrs, "split"); ok {
				m.Split = strings.TrimSpace(v.text())
			}
			if v, ok := apkxAttrValue(attrs, "requiredSplitTypes"); ok && strings.TrimSpace(v.text()) != "" {
				m.SplitsRequired = true
			}
		case "uses-sdk":
			if v, ok := apkxAttrValue(attrs, "minSdkVersion"); ok {
				if n, ok := v.integer(); ok {
					m.MinSDK = int(n)
				}
			}
			if v, ok := apkxAttrValue(attrs, "targetSdkVersion"); ok {
				if n, ok := v.integer(); ok {
					m.TargetSDK = int(n)
				}
			}
		case "uses-feature":
			if v, ok := apkxAttrValue(attrs, "glEsVersion"); ok {
				if n, ok := v.integer(); ok && n > 0 && uint32(n) > m.GLES {
					m.GLES = uint32(n)
				}
			}
		case "application":
			if parent != "manifest" {
				return
			}
			if v, ok := apkxAttrValue(attrs, "label"); ok {
				if v.dataType == apkxTypeReference {
					m.LabelRef = v.data
				} else {
					m.Label = strings.TrimSpace(v.text())
				}
			}
			if v, ok := apkxAttrValue(attrs, "icon"); ok && v.dataType == apkxTypeReference {
				m.IconRef = v.data
			}
			if v, ok := apkxAttrValue(attrs, "hasCode"); ok {
				m.HasCode = v.boolean(true)
			}
			if v, ok := apkxAttrValue(attrs, "isSplitRequired"); ok && v.boolean(false) {
				m.SplitsRequired = true
			}
			if v, ok := apkxAttrValue(attrs, "requiredSplitTypes"); ok && strings.TrimSpace(v.text()) != "" {
				m.SplitsRequired = true
			}
		case "activity", "activity-alias":
			if parent != "application" {
				return
			}
			cls := ""
			if v, ok := apkxAttrValue(attrs, "name"); ok {
				cls = apkxQualify(m.Package, v.text())
			}
			if name == "activity-alias" {
				if v, ok := apkxAttrValue(attrs, "targetActivity"); ok {
					cls = apkxQualify(m.Package, v.text())
				}
			}
			if name == "activity" && cls != "" && len(m.Activities) < apkxMaxActivities {
				m.Activities = append(m.Activities, cls)
			}
			cur = &activityState{class: cls}
		case "meta-data":
			if parent == "application" {
				// Play's App Bundle installs mark the base APK this way.
				if v, ok := apkxAttrValue(attrs, "name"); ok && strings.TrimSpace(v.text()) == "com.android.vending.splits.required" {
					if val, ok := apkxAttrValue(attrs, "value"); ok && val.boolean(false) {
						m.SplitsRequired = true
					}
				}
				return
			}
			if cur == nil || parent != "activity" && parent != "activity-alias" {
				return
			}
			if v, ok := apkxAttrValue(attrs, "name"); ok && strings.TrimSpace(v.text()) == "android.app.lib_name" {
				if val, ok := apkxAttrValue(attrs, "value"); ok {
					cur.libName = strings.TrimSpace(val.text())
					if anyLibName == "" {
						anyLibName = cur.libName
					}
				}
			}
		case "intent-filter":
			if cur != nil {
				inFilter, filterMain, filterLauncher = true, false, false
			}
		case "action":
			if cur != nil && inFilter {
				if v, ok := apkxAttrValue(attrs, "name"); ok && strings.TrimSpace(v.text()) == "android.intent.action.MAIN" {
					filterMain = true
				}
			}
		case "category":
			if cur != nil && inFilter {
				if v, ok := apkxAttrValue(attrs, "name"); ok {
					switch strings.TrimSpace(v.text()) {
					case "android.intent.category.LAUNCHER", "android.intent.category.LEANBACK_LAUNCHER":
						filterLauncher = true
					}
				}
			}
		}
	}
	leave := func(depth int, name string) {
		if len(stack) > 0 {
			stack = stack[:len(stack)-1]
		}
		switch name {
		case "intent-filter":
			if cur != nil && inFilter && filterMain && filterLauncher {
				cur.launcher = true
			}
			inFilter = false
		case "activity", "activity-alias":
			if cur != nil && cur.launcher && m.Launcher == "" {
				m.Launcher = cur.class
				if cur.libName != "" {
					m.NativeLib = cur.libName
				}
			}
			cur = nil
			inFilter = false
		}
	}
	if err := apkxWalkXML(data, visit, leave); err != nil {
		return apkManifest{}, err
	}
	if m.Package == "" {
		return apkManifest{}, errors.New("the manifest names no package")
	}
	if m.NativeLib == "" {
		m.NativeLib = anyLibName
	}
	return m, nil
}

// ── resources.arsc ──────────────────────────────────────────────────────────

// apkxResValue is one value a resource has in one configuration. A string
// value stays an index into the table's global pool until it is needed.
type apkxResValue struct {
	dataType uint8
	data     uint32
	lang     string // "" for the default locale
	density  uint16
}

// apkxTable is a parsed resources.arsc: its global string pool (decoded
// lazily, remembered and budgeted like a manifest's) and its package chunks.
// The label and the icon are looked up in one table, so they share the pool's
// decoding budget.
type apkxTable struct {
	global   *apkxPool
	packages []apkxPackage
}

// apkxPackage is one package chunk of the table.
type apkxPackage struct {
	id         uint32
	headerSize int
	chunk      []byte
}

// apkxMaxPackages bounds the package chunks a table may list.
const apkxMaxPackages = 64

// apkxOpenTable reads the table's top-level chunks; nothing is decoded yet.
// A table that does not parse yields an error.
func apkxOpenTable(arsc []byte) (t *apkxTable, err error) {
	defer func() {
		if r := recover(); r != nil {
			t, err = nil, fmt.Errorf("unreadable resource table (%v)", r)
		}
	}()
	typ, headerSize, table, err := apkxChunk(arsc, 0)
	if err != nil {
		return nil, err
	}
	if typ != apkxChunkTable {
		return nil, fmt.Errorf("not a resource table (chunk 0x%04x)", typ)
	}
	t = &apkxTable{}
	for off := headerSize; off < len(table); {
		ctyp, chs, chunk, err := apkxChunk(table, off)
		if err != nil {
			return nil, err
		}
		off += len(chunk)
		switch ctyp {
		case apkxChunkStringPool:
			if t.global == nil {
				if t.global, err = apkxParsePool(chunk, chs); err != nil {
					return nil, err
				}
			}
		case apkxChunkPackage:
			if id, ok := apkxU32(chunk, 8); ok && len(t.packages) < apkxMaxPackages {
				t.packages = append(t.packages, apkxPackage{id: id, headerSize: chs, chunk: chunk})
			}
		}
	}
	return t, nil
}

// values lists the values resource id has, one per configuration and at most
// apkxMaxResValues of them. An id that is not there yields nothing.
func (t *apkxTable) values(id uint32) (vals []apkxResValue) {
	defer func() {
		if r := recover(); r != nil {
			vals = nil
		}
	}()
	if t == nil {
		return nil
	}
	for _, p := range t.packages {
		if p.id != id>>24 {
			continue
		}
		vals = apkxPackageValues(p.chunk, p.headerSize, (id>>16)&0xFF, id&0xFFFF, vals)
		if len(vals) >= apkxMaxResValues {
			break
		}
	}
	return vals
}

// str is a string value's text, decoded through the global pool ("" for any
// other type).
func (t *apkxTable) str(v apkxResValue) string {
	if t == nil || v.dataType != apkxTypeString {
		return ""
	}
	return t.global.get(v.data)
}

// apkxPackageValues walks one package's type chunks for (type, entry) and
// appends what it finds to out, stopping at apkxMaxResValues.
func apkxPackageValues(pkg []byte, headerSize int, wantType, wantEntry uint32, out []apkxResValue) []apkxResValue {
	for off := headerSize; off < len(pkg) && len(out) < apkxMaxResValues; {
		ctyp, chs, chunk, err := apkxChunk(pkg, off)
		if err != nil {
			return out
		}
		off += len(chunk)
		if ctyp != apkxChunkType || len(chunk) < 20 {
			continue
		}
		if uint32(chunk[8]) != wantType {
			continue
		}
		flags := chunk[9]
		entryCount, _ := apkxU32(chunk, 12)
		entriesStart, _ := apkxU32(chunk, 16)
		if entryCount > 1<<16 {
			continue
		}
		lang, density := apkxConfigOf(chunk, 20)
		entryOff, found := apkxEntryOffset(chunk, chs, flags, entryCount, wantEntry)
		if !found {
			continue
		}
		pos := int64(entriesStart) + int64(entryOff)
		if pos < 0 || pos+8 > int64(len(chunk)) {
			continue
		}
		p := int(pos)
		esize, _ := apkxU16(chunk, p)
		eflags, _ := apkxU16(chunk, p+2)
		v := apkxResValue{lang: lang, density: density}
		switch {
		case eflags&apkxEntryFlagCompact != 0:
			// Compact entry: key in the size field, the type in the flags'
			// high byte, the data in place of the key.
			v.dataType = uint8(eflags >> 8)
			v.data, _ = apkxU32(chunk, p+4)
		case eflags&apkxEntryFlagComplex != 0:
			continue // a bag (style, array): not a label or a file
		default:
			vp := p + int(esize)
			dt := 0
			if vp+3 < len(chunk) {
				dt = int(chunk[vp+3])
			}
			data, ok := apkxU32(chunk, vp+4)
			if !ok {
				continue
			}
			v.dataType, v.data = uint8(dt), data
		}
		out = append(out, v)
	}
	return out
}

// apkxEntryOffset finds entry's offset in a type chunk's offset table, in the
// dense, 16-bit or sparse layout.
func apkxEntryOffset(chunk []byte, headerSize int, flags uint8, count, entry uint32) (uint32, bool) {
	switch {
	case flags&apkxTypeFlagSparse != 0:
		// ResTable_sparseTypeEntry: {uint16 idx; uint16 offset/4}, sorted.
		lo, hi := 0, int(count)-1
		for lo <= hi {
			mid := (lo + hi) / 2
			idx, ok1 := apkxU16(chunk, headerSize+4*mid)
			o, ok2 := apkxU16(chunk, headerSize+4*mid+2)
			if !ok1 || !ok2 {
				return 0, false
			}
			switch {
			case uint32(idx) == entry:
				return uint32(o) * 4, true
			case uint32(idx) < entry:
				lo = mid + 1
			default:
				hi = mid - 1
			}
		}
		return 0, false
	case flags&apkxTypeFlagOffset16 != 0:
		if entry >= count {
			return 0, false
		}
		o, ok := apkxU16(chunk, headerSize+2*int(entry))
		if !ok || o == 0xFFFF {
			return 0, false
		}
		return uint32(o) * 4, true
	default:
		if entry >= count {
			return 0, false
		}
		o, ok := apkxU32(chunk, headerSize+4*int(entry))
		if !ok || o == 0xFFFFFFFF {
			return 0, false
		}
		return o, true
	}
}

// apkxConfigOf reads the locale language and the density out of the
// ResTable_config at off.
func apkxConfigOf(chunk []byte, off int) (lang string, density uint16) {
	size, ok := apkxU32(chunk, off)
	if !ok {
		return "", 0
	}
	if size >= 10 && off+10 <= len(chunk) {
		a, b := chunk[off+8], chunk[off+9]
		switch {
		case a == 0:
			// The default locale.
		case a < 0x80 && b < 0x80:
			lang = strings.ToLower(string([]byte{a, b}))
		default:
			// A packed three-letter language: some locale, not the default.
			lang = "?"
		}
	}
	if size >= 16 {
		density, _ = apkxU16(chunk, off+14)
	}
	return lang, density
}

// apkxPickString chooses the value a label shows: the default locale first,
// then English, then any.
func apkxPickString(vals []apkxResValue) (apkxResValue, bool) {
	best, rank := apkxResValue{}, 99
	for _, v := range vals {
		r := 3
		switch v.lang {
		case "":
			r = 0
		case "en":
			r = 1
		}
		if r < rank {
			best, rank = v, r
		}
	}
	return best, rank < 99
}

// label follows a label reference to its text; only the picked value is
// decoded.
func (t *apkxTable) label(id uint32) string {
	for hop := 0; hop < apkxMaxRefHops && id != 0; hop++ {
		v, ok := apkxPickString(t.values(id))
		if !ok {
			return ""
		}
		switch v.dataType {
		case apkxTypeString:
			return strings.TrimSpace(t.str(v))
		case apkxTypeReference:
			id = v.data
			continue
		}
		return ""
	}
	return ""
}

// iconPath follows an icon reference to the bitmap with the highest density
// the store can decode (PNG or JPEG); adaptive-icon XML and WebP are skipped.
// "" when there is none. A value is decoded only when its density could win.
func (t *apkxTable) iconPath(id uint32) string {
	best, bestDensity := "", -1
	for hop := 0; hop < apkxMaxRefHops && id != 0; hop++ {
		next := uint32(0)
		for _, v := range t.values(id) {
			switch v.dataType {
			case apkxTypeString:
				d := int(v.density)
				switch v.density {
				case 0xFFFF: // nodpi
					d = 160
				case 0xFFFE: // anydpi
					d = 0
				}
				if d <= bestDensity {
					continue
				}
				if name := t.str(v); apkStoreDecodableImage(name) {
					best, bestDensity = name, d
				}
			case apkxTypeReference:
				if next == 0 {
					next = v.data
				}
			}
		}
		if best != "" {
			return best
		}
		id = next
	}
	return best
}

// apkxResolveLabel opens a table and follows a label reference in it.
func apkxResolveLabel(arsc []byte, id uint32) string {
	t, err := apkxOpenTable(arsc)
	if err != nil {
		return ""
	}
	return t.label(id)
}

// apkxResolveIconPath opens a table and follows an icon reference in it.
func apkxResolveIconPath(arsc []byte, id uint32) string {
	t, err := apkxOpenTable(arsc)
	if err != nil {
		return ""
	}
	return t.iconPath(id)
}

// apkStoreDecodableImage reports whether an APK entry is a bitmap Go's
// standard library decodes.
func apkStoreDecodableImage(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".png", ".jpg", ".jpeg":
		return true
	}
	return false
}

// ── Reading from the zip ────────────────────────────────────────────────────

// apkxReadEntry reads a zip entry fully, refusing anything over limit (by its
// declared size and by what is actually read).
func apkxReadEntry(f *zip.File, limit int64) ([]byte, error) {
	if f == nil {
		return nil, errors.New("missing entry")
	}
	if f.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("%s is larger than %d bytes", f.Name, limit)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if n > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", f.Name, limit)
	}
	return buf.Bytes(), nil
}
