package main

// davshare_props.go — the network drive's properties: PROPFIND and PROPPATCH
// (RFC 4918 §9.1–9.2), and the XML both answer with.
//
// PROPFIND answers depth 0 and 1 (infinity is taken as 1: no client a person
// uses needs more, and a whole card in one answer would not fit in memory)
// with allprop, propname or a list of properties. The live properties are
// the core ones — displayname, getcontentlength, getlastmodified,
// creationdate, resourcetype, getetag, getcontenttype, supportedlock,
// lockdiscovery — plus quota-available-bytes and quota-used-bytes (RFC 4331)
// for the free space Windows and Finder show, and, when asked, Windows'
// Win32FileAttributes and Win32 times (urn:schemas-microsoft-com:): the
// read-only folders say read-only there. A property the drive does not know
// is answered 404 in its own propstat.
//
// PROPPATCH stores nothing but answers every property 200, which Windows and
// Finder need before they finish a copy; Windows' Win32LastModifiedTime is
// applied to the file, so a copied file keeps its date. The answers carry no
// whitespace between elements (Windows' older clients reject it). Request
// bodies are bounded and read by encoding/xml, which expands no entities.

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	davNS   = "DAV:"
	davMSNS = "urn:schemas-microsoft-com:"
)

// davEscape writes s as XML text.
func davEscape(b *strings.Builder, s string) {
	_ = xml.EscapeText(b, []byte(s))
}

// davPropNames is a list of requested property names.
type davPropNames []xml.Name

// UnmarshalXML collects the names of prop's child elements.
func (p *davPropNames) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for {
		t, err := d.Token()
		if err != nil {
			return err
		}
		switch e := t.(type) {
		case xml.StartElement:
			if len(*p) < 256 {
				*p = append(*p, e.Name)
			}
			if err := d.Skip(); err != nil {
				return err
			}
		case xml.EndElement:
			return nil
		}
	}
}

// davPropfindReq is a PROPFIND body.
type davPropfindReq struct {
	XMLName  xml.Name     `xml:"DAV: propfind"`
	Allprop  *struct{}    `xml:"DAV: allprop"`
	Propname *struct{}    `xml:"DAV: propname"`
	Prop     davPropNames `xml:"DAV: prop"`
}

// davReadXML reads a bounded XML body; empty is nil.
func davReadXML(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, davMaxXML+1))
	if err != nil {
		return nil, err
	}
	if len(body) > davMaxXML {
		return nil, errors.New("the request is too large")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	return body, nil
}

// davAllprop is what allprop answers, in this order.
var davAllprop = []string{"displayname", "getcontentlength", "getlastmodified", "creationdate", "resourcetype", "getetag", "getcontenttype", "supportedlock", "lockdiscovery"}

// davItem is one resource of an answer.
type davItem struct {
	virtual   string
	name      string
	dir       bool
	info      os.FileInfo // nil for the root
	protected bool
	quotaDir  string // the folder whose file system the quota is read on
}

// propfind answers PROPFIND.
func (q *davReq) propfind(w http.ResponseWriter, r *http.Request) {
	p, ok := q.resolve(w, q.rel)
	if !ok {
		return
	}
	body, err := davReadXML(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	var req davPropfindReq
	if body != nil {
		if err := xml.Unmarshal(body, &req); err != nil {
			http.Error(w, "The request could not be read.", http.StatusBadRequest)
			return
		}
	}
	mode := "allprop"
	switch {
	case req.Propname != nil:
		mode = "propname"
	case len(req.Prop) > 0:
		mode = "prop"
	}
	self, ok := q.item(p)
	if !ok {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	// Whether each child is read-only is only worked out (a walk through
	// its links) when Windows asks for the attributes that show it.
	needProt := false
	for _, n := range req.Prop {
		if n == (xml.Name{Space: davMSNS, Local: "Win32FileAttributes"}) {
			needProt = mode == "prop"
		}
	}
	items := []davItem{self}
	if self.dir && strings.TrimSpace(r.Header.Get("Depth")) != "0" {
		entries, err := q.children(p)
		if err != nil {
			http.Error(w, "This folder cannot be read.", http.StatusInternalServerError)
			return
		}
		for _, e := range entries {
			v := path.Join(p.virtual, e.name)
			it := davItem{virtual: v, name: e.name, dir: e.dir, info: e.info, protected: self.protected, quotaDir: self.quotaDir}
			if p.isRoot() {
				if sh := q.set.find(e.name); sh != nil {
					it.quotaDir = sh.dir
					it.protected = false
				}
			} else if needProt && !self.protected {
				if cp, err := q.set.resolvePath(p.virtual, e.name); err == nil {
					it.protected = cp.protected
				}
			}
			items = append(items, it)
		}
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:" xmlns:Z="urn:schemas-microsoft-com:">`)
	quota := map[string][2]int64{}
	for _, it := range items {
		q.writeResponse(&b, it, mode, req.Prop, quota)
	}
	b.WriteString(`</D:multistatus>`)
	davWriteMulti(w, b.String())
}

// davWriteMulti sends a 207 Multi-Status body.
func davWriteMulti(w http.ResponseWriter, body string) {
	h := w.Header()
	h.Set("Content-Type", `application/xml; charset="utf-8"`)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = io.WriteString(w, body)
}

// davEmptyRoot is the root as Windows sees it before signing in: a folder
// with nothing in it.
func davEmptyRoot(w http.ResponseWriter) {
	davWriteMulti(w, `<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:"><D:response><D:href>/</D:href>`+
		`<D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype><D:displayname>JukaHub</D:displayname></D:prop>`+
		`<D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`)
}

// item is p as an answer's resource.
func (q *davReq) item(p ftpPath) (davItem, bool) {
	if p.isRoot() {
		base := ""
		if len(q.set.list) > 0 {
			base = q.set.list[0].dir
		}
		return davItem{virtual: "/", name: "JukaHub", dir: true, protected: true, quotaDir: base}, true
	}
	info, err := p.share.root.Stat(p.rel)
	if err != nil {
		return davItem{}, false
	}
	return davItem{virtual: p.virtual, name: path.Base(p.virtual), dir: info.IsDir(), info: info,
		protected: p.protected || companionFilesAliasProtected(p), quotaDir: p.share.dir}, true
}

// writeResponse writes one D:response for it.
func (q *davReq) writeResponse(b *strings.Builder, it davItem, mode string, want davPropNames, quota map[string][2]int64) {
	b.WriteString(`<D:response><D:href>`)
	davEscape(b, q.href(it.virtual, it.dir))
	b.WriteString(`</D:href>`)
	if mode == "propname" {
		b.WriteString(`<D:propstat><D:prop>`)
		for _, n := range davAllprop {
			if !it.dir || (n != "getcontentlength" && n != "getcontenttype") {
				b.WriteString(`<D:` + n + `/>`)
			}
		}
		if it.dir {
			b.WriteString(`<D:quota-available-bytes/><D:quota-used-bytes/>`)
		}
		b.WriteString(`<Z:Win32FileAttributes/></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`)
		return
	}
	names := want
	if mode == "allprop" {
		names = names[:0:0]
		for _, n := range davAllprop {
			names = append(names, xml.Name{Space: davNS, Local: n})
		}
	}
	var found, missing strings.Builder
	for _, n := range names {
		v, ok := q.prop(it, n, quota)
		switch {
		case ok:
			found.WriteString(v)
		case mode == "prop":
			davEmptyElement(&missing, n)
		}
	}
	if found.Len() > 0 {
		b.WriteString(`<D:propstat><D:prop>` + found.String() + `</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`)
	}
	if missing.Len() > 0 {
		b.WriteString(`<D:propstat><D:prop>` + missing.String() + `</D:prop><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat>`)
	}
	b.WriteString(`</D:response>`)
}

// davEmptyElement writes <name/> in its own namespace.
func davEmptyElement(b *strings.Builder, n xml.Name) {
	switch n.Space {
	case davNS:
		b.WriteString(`<D:` + n.Local + `/>`)
	case davMSNS:
		b.WriteString(`<Z:` + n.Local + `/>`)
	case "":
		b.WriteString(`<` + n.Local + ` xmlns=""/>`)
	default:
		b.WriteString(`<x:` + n.Local + ` xmlns:x="`)
		davEscape(b, n.Space)
		b.WriteString(`"/>`)
	}
}

// davTimes is a resource's modification time (the zero time for the root).
func davTimes(it davItem) time.Time {
	if it.info == nil {
		return time.Time{}
	}
	return it.info.ModTime()
}

// prop is one property of it as XML, and whether it has it.
func (q *davReq) prop(it davItem, n xml.Name, quota map[string][2]int64) (string, bool) {
	mod := davTimes(it)
	el := func(tag, value string) string {
		var b strings.Builder
		b.WriteString(`<` + tag + `>`)
		davEscape(&b, value)
		b.WriteString(`</` + tag + `>`)
		return b.String()
	}
	switch n.Space {
	case davNS:
		switch n.Local {
		case "displayname":
			return el("D:displayname", it.name), true
		case "getcontentlength":
			if it.dir || it.info == nil {
				return "", false
			}
			return el("D:getcontentlength", strconv.FormatInt(it.info.Size(), 10)), true
		case "getlastmodified":
			if mod.IsZero() {
				return "", false
			}
			return el("D:getlastmodified", mod.UTC().Format(http.TimeFormat)), true
		case "creationdate":
			if mod.IsZero() {
				return "", false
			}
			return el("D:creationdate", mod.UTC().Format(time.RFC3339)), true
		case "resourcetype":
			if it.dir {
				return `<D:resourcetype><D:collection/></D:resourcetype>`, true
			}
			return `<D:resourcetype/>`, true
		case "getetag":
			if it.dir || it.info == nil {
				return "", false
			}
			return el("D:getetag", davETag(it.info)), true
		case "getcontenttype":
			if it.dir {
				return "", false
			}
			return el("D:getcontenttype", davContentType(it.name)), true
		case "supportedlock":
			return `<D:supportedlock><D:lockentry><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype></D:lockentry>` +
				`<D:lockentry><D:lockscope><D:shared/></D:lockscope><D:locktype><D:write/></D:locktype></D:lockentry></D:supportedlock>`, true
		case "lockdiscovery":
			var b strings.Builder
			b.WriteString(`<D:lockdiscovery>`)
			for _, l := range q.s.locks.covering(davLockKey(it.virtual), false) {
				davActiveLockXML(&b, l)
			}
			b.WriteString(`</D:lockdiscovery>`)
			return b.String(), true
		case "quota-available-bytes", "quota-used-bytes":
			if !it.dir || it.quotaDir == "" {
				return "", false
			}
			qv, ok := quota[it.quotaDir]
			if !ok {
				if total, free, _, err := sdMoveStatfs(it.quotaDir); err == nil {
					qv = [2]int64{free, total - free}
				} else {
					qv = [2]int64{-1, -1}
				}
				quota[it.quotaDir] = qv
			}
			v := qv[0]
			if n.Local == "quota-used-bytes" {
				v = qv[1]
			}
			if v < 0 {
				return "", false
			}
			return el("D:"+n.Local, strconv.FormatInt(v, 10)), true
		}
	case davMSNS:
		switch n.Local {
		case "Win32FileAttributes":
			attr := 0x20 // archive
			if it.dir {
				attr = 0x10
			}
			if it.protected {
				attr |= 0x01
			}
			if strings.HasPrefix(it.name, ".") {
				attr |= 0x02
			}
			return el("Z:Win32FileAttributes", fmt.Sprintf("%08x", attr)), true
		case "Win32CreationTime", "Win32LastAccessTime", "Win32LastModifiedTime":
			if mod.IsZero() {
				return "", false
			}
			return el("Z:"+n.Local, mod.UTC().Format(http.TimeFormat)), true
		}
	}
	return "", false
}

// davPatchProp is one property a PROPPATCH sets or removes.
type davPatchProp struct {
	name  xml.Name
	value string
}

// davParsePropertyUpdate reads a propertyupdate body: every property under
// DAV:set/DAV:prop and DAV:remove/DAV:prop, with its text.
func davParsePropertyUpdate(body []byte) ([]davPatchProp, error) {
	d := xml.NewDecoder(bytes.NewReader(body))
	var stack []xml.Name
	var out []davPatchProp
	sawRoot := false
	for {
		t, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch e := t.(type) {
		case xml.StartElement:
			if len(stack) == 0 {
				if e.Name.Space != davNS || e.Name.Local != "propertyupdate" {
					return nil, errors.New("not a propertyupdate")
				}
				sawRoot = true
			}
			if len(stack) == 3 && stack[2] == (xml.Name{Space: davNS, Local: "prop"}) &&
				(stack[1] == (xml.Name{Space: davNS, Local: "set"}) || stack[1] == (xml.Name{Space: davNS, Local: "remove"})) {
				text, err := davElementText(d)
				if err != nil {
					return nil, err
				}
				if len(out) < 256 {
					out = append(out, davPatchProp{name: e.Name, value: strings.TrimSpace(text)})
				}
				continue
			}
			stack = append(stack, e.Name)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if !sawRoot {
		return nil, errors.New("empty propertyupdate")
	}
	return out, nil
}

// davElementText reads the rest of an element (its start already read) and
// returns all the text in it.
func davElementText(d *xml.Decoder) (string, error) {
	var b strings.Builder
	depth := 1
	for depth > 0 {
		t, err := d.Token()
		if err != nil {
			return "", err
		}
		switch e := t.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if b.Len() < 4096 {
				b.Write(e)
			}
		}
	}
	return b.String(), nil
}

// proppatch answers PROPPATCH.
func (q *davReq) proppatch(w http.ResponseWriter, r *http.Request) {
	p, ok := q.resolve(w, q.rel)
	if !ok {
		return
	}
	body, err := davReadXML(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	props, err := davParsePropertyUpdate(body)
	if err != nil {
		http.Error(w, "The request could not be read.", http.StatusBadRequest)
		return
	}
	it, ok := q.item(p)
	if !ok {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	writable := !p.isRoot() && !p.isShareTop() && !it.protected
	if writable && !q.lockOK(w, r, p.virtual, false) {
		return
	}
	for _, pp := range props {
		if !writable || pp.name.Space != davMSNS || pp.name.Local != "Win32LastModifiedTime" {
			continue
		}
		if t, err := http.ParseTime(pp.value); err == nil {
			if err := p.share.root.Chtimes(p.rel, time.Time{}, t); err != nil {
				logDebug("davshare", "could not set the time of %s: %v", p.display(), err)
			}
		}
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:" xmlns:Z="urn:schemas-microsoft-com:"><D:response><D:href>`)
	davEscape(&b, q.href(it.virtual, it.dir))
	b.WriteString(`</D:href><D:propstat><D:prop>`)
	for _, pp := range props {
		davEmptyElement(&b, pp.name)
	}
	status := "HTTP/1.1 200 OK"
	if !writable && len(props) > 0 {
		status = "HTTP/1.1 403 Forbidden"
	}
	b.WriteString(`</D:prop><D:status>` + status + `</D:status></D:propstat></D:response></D:multistatus>`)
	davWriteMulti(w, b.String())
}
