package main

// apkstore_testutil_test.go — builders for the APK Store parser tests: Android
// binary XML (the AXML chunk format) and a minimal resources.arsc.
// They write the layouts of AOSP's ResourceTypes.h from scratch, so the
// decoder is tested against an independent encoder rather than against
// itself.

import (
	"bytes"
	"encoding/binary"
	"unicode/utf16"
)

const axTestAndroidNS = "http://schemas.android.com/apk/res/android"

// axAttr is one attribute of a test element. resID > 0 puts the name in the
// resource map, the way aapt does for framework attributes.
type axAttr struct {
	android bool
	name    string
	resID   uint32
	typ     uint8
	data    uint32
	str     string // TYPE_STRING value (also the raw value)
}

func axString(name string, resID uint32, value string) axAttr {
	return axAttr{android: true, name: name, resID: resID, typ: apkxTypeString, str: value}
}

func axInt(name string, resID uint32, v uint32) axAttr {
	return axAttr{android: true, name: name, resID: resID, typ: apkxTypeIntDec, data: v}
}

func axHex(name string, resID uint32, v uint32) axAttr {
	return axAttr{android: true, name: name, resID: resID, typ: apkxTypeIntHex, data: v}
}

func axBool(name string, resID uint32, v bool) axAttr {
	d := uint32(0)
	if v {
		d = 0xFFFFFFFF
	}
	return axAttr{android: true, name: name, resID: resID, typ: apkxTypeIntBool, data: d}
}

func axRef(name string, resID uint32, ref uint32) axAttr {
	return axAttr{android: true, name: name, resID: resID, typ: apkxTypeReference, data: ref}
}

// axNode is one element.
type axNode struct {
	name     string
	attrs    []axAttr
	children []*axNode
}

func axEl(name string, attrs []axAttr, children ...*axNode) *axNode {
	return &axNode{name: name, attrs: attrs, children: children}
}

// axPool interns strings in the order aapt uses: resource-mapped attribute
// names first.
type axPool struct {
	strs  []string
	index map[string]uint32
}

func (p *axPool) add(s string) uint32 {
	if p.index == nil {
		p.index = map[string]uint32{}
	}
	if i, ok := p.index[s]; ok {
		return i
	}
	i := uint32(len(p.strs))
	p.strs = append(p.strs, s)
	p.index[s] = i
	return i
}

func axPad4(b *bytes.Buffer) {
	for b.Len()%4 != 0 {
		b.WriteByte(0)
	}
}

// axPoolChunk serialises a string pool chunk.
func axPoolChunk(strs []string, utf8 bool) []byte {
	var data bytes.Buffer
	offsets := make([]uint32, len(strs))
	for i, s := range strs {
		offsets[i] = uint32(data.Len())
		if utf8 {
			u16 := len(utf16.Encode([]rune(s)))
			axLen8(&data, u16)
			axLen8(&data, len(s))
			data.WriteString(s)
			data.WriteByte(0)
		} else {
			units := utf16.Encode([]rune(s))
			if len(units) > 0x7FFF {
				binary.Write(&data, binary.LittleEndian, uint16(0x8000|len(units)>>16))
				binary.Write(&data, binary.LittleEndian, uint16(len(units)&0xFFFF))
			} else {
				binary.Write(&data, binary.LittleEndian, uint16(len(units)))
			}
			for _, u := range units {
				binary.Write(&data, binary.LittleEndian, u)
			}
			binary.Write(&data, binary.LittleEndian, uint16(0))
		}
	}
	axPad4(&data)
	headerSize := 28
	stringsStart := headerSize + 4*len(strs)
	var out bytes.Buffer
	flags := uint32(0)
	if utf8 {
		flags = 1 << 8
	}
	w := func(v interface{}) { binary.Write(&out, binary.LittleEndian, v) }
	w(uint16(apkxChunkStringPool))
	w(uint16(headerSize))
	w(uint32(stringsStart + data.Len()))
	w(uint32(len(strs)))
	w(uint32(0)) // styles
	w(flags)
	w(uint32(stringsStart))
	w(uint32(0))
	for _, o := range offsets {
		w(o)
	}
	out.Write(data.Bytes())
	return out.Bytes()
}

func axLen8(b *bytes.Buffer, n int) {
	if n > 0x7F {
		b.WriteByte(byte(0x80 | n>>8))
		b.WriteByte(byte(n))
		return
	}
	b.WriteByte(byte(n))
}

// buildAXML serialises root as binary XML. stripNames empties every
// resource-mapped attribute name, the way a shrunk manifest does.
func buildAXML(root *axNode, utf8, stripNames bool) []byte {
	var pool axPool
	var resIDs []uint32
	var walkNames func(n *axNode)
	walkNames = func(n *axNode) {
		for _, a := range n.attrs {
			if a.resID == 0 {
				continue
			}
			name := a.name
			if stripNames {
				name = "\x00" + a.name // unique placeholder, emptied below
			}
			if _, ok := pool.index[name]; !ok {
				pool.add(name)
				resIDs = append(resIDs, a.resID)
			}
		}
		for _, c := range n.children {
			walkNames(c)
		}
	}
	walkNames(root)
	nsURI := pool.add(axTestAndroidNS)
	nsPrefix := pool.add("android")
	var body bytes.Buffer
	w := func(v interface{}) { binary.Write(&body, binary.LittleEndian, v) }
	nameIdx := func(a axAttr) uint32 {
		if a.resID != 0 {
			if stripNames {
				return pool.index["\x00"+a.name]
			}
			return pool.index[a.name]
		}
		return pool.add(a.name)
	}
	// Start namespace.
	w(uint16(0x0100))
	w(uint16(16))
	w(uint32(24))
	w(uint32(1))
	w(uint32(0xFFFFFFFF))
	w(nsPrefix)
	w(nsURI)
	var emit func(n *axNode)
	emit = func(n *axNode) {
		name := pool.add(n.name)
		w(uint16(apkxChunkStartElement))
		w(uint16(16))
		w(uint32(16 + 20 + 20*len(n.attrs)))
		w(uint32(1))
		w(uint32(0xFFFFFFFF))
		w(uint32(0xFFFFFFFF)) // ns
		w(name)
		w(uint16(20)) // attributeStart
		w(uint16(20)) // attributeSize
		w(uint16(len(n.attrs)))
		w(uint16(0))
		w(uint16(0))
		w(uint16(0))
		for _, a := range n.attrs {
			ns := uint32(0xFFFFFFFF)
			if a.android {
				ns = nsURI
			}
			w(ns)
			w(nameIdx(a))
			raw := uint32(0xFFFFFFFF)
			data := a.data
			if a.typ == apkxTypeString {
				raw = pool.add(a.str)
				data = raw
			}
			w(raw)
			w(uint16(8))
			w(uint8(0))
			w(a.typ)
			w(data)
		}
		for _, c := range n.children {
			emit(c)
		}
		w(uint16(apkxChunkEndElement))
		w(uint16(16))
		w(uint32(24))
		w(uint32(1))
		w(uint32(0xFFFFFFFF))
		w(uint32(0xFFFFFFFF))
		w(name)
	}
	emit(root)
	// End namespace.
	w(uint16(0x0101))
	w(uint16(16))
	w(uint32(24))
	w(uint32(1))
	w(uint32(0xFFFFFFFF))
	w(nsPrefix)
	w(nsURI)

	strs := append([]string(nil), pool.strs...)
	if stripNames {
		for i := range strs {
			if len(strs[i]) > 0 && strs[i][0] == 0 {
				strs[i] = ""
			}
		}
	}
	poolChunk := axPoolChunk(strs, utf8)
	var resMap bytes.Buffer
	binary.Write(&resMap, binary.LittleEndian, uint16(apkxChunkResourceMap))
	binary.Write(&resMap, binary.LittleEndian, uint16(8))
	binary.Write(&resMap, binary.LittleEndian, uint32(8+4*len(resIDs)))
	for _, id := range resIDs {
		binary.Write(&resMap, binary.LittleEndian, id)
	}
	var out bytes.Buffer
	total := 8 + len(poolChunk) + resMap.Len() + body.Len()
	binary.Write(&out, binary.LittleEndian, uint16(apkxChunkXML))
	binary.Write(&out, binary.LittleEndian, uint16(8))
	binary.Write(&out, binary.LittleEndian, uint32(total))
	out.Write(poolChunk)
	out.Write(resMap.Bytes())
	out.Write(body.Bytes())
	return out.Bytes()
}

// Framework attribute ids the tests use (android.R.attr).
const (
	axIDLabel          = 0x01010001
	axIDIcon           = 0x01010002
	axIDName           = 0x01010003
	axIDHasCode        = 0x0101000c
	axIDValue          = 0x01010024
	axIDTargetActivity = 0x01010202
	axIDMinSdk         = 0x0101020c
	axIDVersionCode    = 0x0101021b
	axIDVersionName    = 0x0101021c
	axIDTargetSdk      = 0x01010270
	axIDGLES           = 0x01010281
)

// axTestManifest is a NativeActivity game's manifest.
func axTestManifest(pkg, label string, hasCode bool, libName string) *axNode {
	appAttrs := []axAttr{axBool("hasCode", axIDHasCode, hasCode)}
	if label != "" {
		appAttrs = append(appAttrs, axString("label", axIDLabel, label))
	}
	activity := axEl("activity", []axAttr{axString("name", axIDName, "android.app.NativeActivity")},
		axEl("meta-data", []axAttr{axString("name", axIDName, "android.app.lib_name"), axString("value", axIDValue, libName)}),
		axEl("intent-filter", nil,
			axEl("action", []axAttr{axString("name", axIDName, "android.intent.action.MAIN")}),
			axEl("category", []axAttr{axString("name", axIDName, "android.intent.category.LAUNCHER")}),
		),
	)
	return axEl("manifest", []axAttr{
		{name: "package", typ: apkxTypeString, str: pkg},
		axInt("versionCode", axIDVersionCode, 42),
		axString("versionName", axIDVersionName, "1.2.3"),
	},
		axEl("uses-sdk", []axAttr{axInt("minSdkVersion", axIDMinSdk, 21), axInt("targetSdkVersion", axIDTargetSdk, 33)}),
		axEl("uses-feature", []axAttr{axHex("glEsVersion", axIDGLES, 0x00030000)}),
		axEl("application", appAttrs, activity),
	)
}

type arscEntryT struct {
	typ  uint8
	data uint32
}

type arscConfigT struct {
	lang    string
	density uint16
	flags   uint8 // 0, apkxTypeFlagSparse or apkxTypeFlagOffset16
	count   int   // entryCount for dense tables
	entries map[int]arscEntryT
}

type arscTypeT struct {
	id      uint8
	configs []arscConfigT
}

// buildArsc writes a table with one package (id pkgID) whose types hold the
// given entries; global is the value string pool.
func buildArsc(global []string, pkgID uint32, types []arscTypeT) []byte {
	var pkgBody bytes.Buffer
	pkgBody.Write(axPoolChunk([]string{"string", "mipmap"}, true)) // type strings
	pkgBody.Write(axPoolChunk([]string{"k0", "k1"}, true))         // key strings
	for _, t := range types {
		for _, c := range t.configs {
			pkgBody.Write(arscTypeChunk(t.id, c))
		}
	}
	var pkg bytes.Buffer
	pw := func(v interface{}) { binary.Write(&pkg, binary.LittleEndian, v) }
	pw(uint16(apkxChunkPackage))
	pw(uint16(288))
	pw(uint32(288 + pkgBody.Len()))
	pw(pkgID)
	pkg.Write(make([]byte, 256)) // name
	pw(uint32(288))              // typeStrings
	pw(uint32(0))
	pw(uint32(0)) // keyStrings (unused by the decoder)
	pw(uint32(0))
	pw(uint32(0))
	pkg.Write(pkgBody.Bytes())

	globalPool := axPoolChunk(global, false)
	var out bytes.Buffer
	ow := func(v interface{}) { binary.Write(&out, binary.LittleEndian, v) }
	ow(uint16(apkxChunkTable))
	ow(uint16(12))
	ow(uint32(12 + len(globalPool) + pkg.Len()))
	ow(uint32(1))
	out.Write(globalPool)
	out.Write(pkg.Bytes())
	return out.Bytes()
}

func arscTypeChunk(id uint8, c arscConfigT) []byte {
	const configSize = 32
	headerSize := 20 + configSize
	keys := make([]int, 0, len(c.entries))
	for k := range c.entries {
		keys = append(keys, k)
	}
	// Sorted indices (sparse tables need it).
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	var entries bytes.Buffer
	offsetOf := map[int]uint32{}
	for _, k := range keys {
		offsetOf[k] = uint32(entries.Len())
		e := c.entries[k]
		binary.Write(&entries, binary.LittleEndian, uint16(8)) // entry size
		binary.Write(&entries, binary.LittleEndian, uint16(0)) // flags
		binary.Write(&entries, binary.LittleEndian, uint32(0)) // key
		binary.Write(&entries, binary.LittleEndian, uint16(8)) // value size
		entries.WriteByte(0)
		entries.WriteByte(e.typ)
		binary.Write(&entries, binary.LittleEndian, e.data)
	}
	var offsets bytes.Buffer
	entryCount := c.count
	switch {
	case c.flags&apkxTypeFlagSparse != 0:
		entryCount = len(keys)
		for _, k := range keys {
			binary.Write(&offsets, binary.LittleEndian, uint16(k))
			binary.Write(&offsets, binary.LittleEndian, uint16(offsetOf[k]/4))
		}
	case c.flags&apkxTypeFlagOffset16 != 0:
		for i := 0; i < entryCount; i++ {
			if o, ok := offsetOf[i]; ok {
				binary.Write(&offsets, binary.LittleEndian, uint16(o/4))
			} else {
				binary.Write(&offsets, binary.LittleEndian, uint16(0xFFFF))
			}
		}
	default:
		for i := 0; i < entryCount; i++ {
			if o, ok := offsetOf[i]; ok {
				binary.Write(&offsets, binary.LittleEndian, o)
			} else {
				binary.Write(&offsets, binary.LittleEndian, uint32(0xFFFFFFFF))
			}
		}
	}
	axPad4(&offsets)
	entriesStart := headerSize + offsets.Len()
	var out bytes.Buffer
	w := func(v interface{}) { binary.Write(&out, binary.LittleEndian, v) }
	w(uint16(apkxChunkType))
	w(uint16(headerSize))
	w(uint32(entriesStart + entries.Len()))
	out.WriteByte(id)
	out.WriteByte(c.flags)
	w(uint16(0))
	w(uint32(entryCount))
	w(uint32(entriesStart))
	cfg := make([]byte, configSize)
	binary.LittleEndian.PutUint32(cfg[0:], configSize)
	if len(c.lang) == 2 {
		cfg[8], cfg[9] = c.lang[0], c.lang[1]
	}
	binary.LittleEndian.PutUint16(cfg[14:], c.density)
	out.Write(cfg)
	out.Write(offsets.Bytes())
	out.Write(entries.Bytes())
	return out.Bytes()
}

// ── APK zips ───────────────────────────────────────────────────────────────
