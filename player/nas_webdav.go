package main

// nas_webdav.go — a WebDAV folder as a network drive: nasFS over the client
// Save sync already uses (webdav_client.go). The same address rules apply:
// HTTPS, or plain http:// only to a home-network address, no redirects. The
// connection has no identity, so a refused password here never marks Save
// sync's own WebDAV login refused.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type nasDAV struct {
	c    *webdavConn
	base *url.URL
}

// nasDialWebDAV prepares requests to src's address (nothing is sent yet).
func nasDialWebDAV(src nasSource) (nasFS, error) {
	base, err := webdavCleanURL(src.Host)
	if err != nil {
		return nil, err
	}
	c, err := webdavNewConn(base, src.User, nasOpenSecret(src.Password), "")
	if err != nil {
		return nil, err
	}
	return &nasDAV{c: c, base: base}, nil
}

// url is the address of p below the base, every name escaped.
func (d *nasDAV) url(p string, dir bool) string {
	var b strings.Builder
	b.WriteString(d.base.String())
	p = strings.Trim(p, "/")
	if p != "" {
		for i, seg := range strings.Split(p, "/") {
			if i > 0 {
				b.WriteByte('/')
			}
			b.WriteString(url.PathEscape(seg))
		}
		if dir {
			b.WriteByte('/')
		}
	}
	return b.String()
}

func nasDAVError(err error) error {
	switch {
	case err == nil:
		return nil
	case webdavStatusIs(err, http.StatusUnauthorized):
		var he *webdavHTTPError
		if errors.As(err, &he) && he.Digest {
			return err
		}
		return errNasAuth
	case webdavStatusIs(err, http.StatusNotFound):
		return errNasNotFound
	}
	return errors.New(webdavErrorText(err))
}

func nasDAVEntry(it webdavItem) nasEntry {
	e := nasEntry{Name: it.Rel, Size: it.Size, IsDir: it.Dir}
	if t, err := http.ParseTime(it.LastMod); err == nil {
		e.ModTime = t
	}
	return e
}

func (d *nasDAV) List(ctx context.Context, dir string) ([]nasEntry, error) {
	target := d.url(dir, true)
	ref, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	items, err := d.c.propfind(ctx, target, ref, "1")
	if err != nil {
		return nil, nasDAVError(err)
	}
	out := make([]nasEntry, 0, len(items))
	for _, it := range items {
		if it.Rel == "" || strings.Contains(it.Rel, "/") {
			continue // the folder itself, or a server that answered deeper
		}
		out = append(out, nasDAVEntry(it))
	}
	return out, nil
}

func (d *nasDAV) Stat(ctx context.Context, p string) (nasEntry, error) {
	target := d.url(p, false)
	ref, err := url.Parse(target)
	if err != nil {
		return nasEntry{}, err
	}
	items, err := d.c.propfind(ctx, target, ref, "0")
	if err != nil {
		return nasEntry{}, nasDAVError(err)
	}
	for _, it := range items {
		if it.Rel == "" {
			e := nasDAVEntry(it)
			e.Name = p[strings.LastIndex(p, "/")+1:]
			return e, nil
		}
	}
	return nasEntry{}, errNasNotFound
}

func (d *nasDAV) ReadFrom(ctx context.Context, p string, off int64) (io.ReadCloser, int64, error) {
	hdr := http.Header{}
	if off > 0 {
		hdr.Set("Range", "bytes="+strconv.FormatInt(off, 10)+"-")
	}
	resp, err := d.c.send(ctx, webdavReq{method: http.MethodGet, url: d.url(p, false), header: hdr})
	if err != nil {
		if off > 0 && webdavStatusIs(err, http.StatusRequestedRangeNotSatisfiable) {
			return nil, 0, errNasPartTooBig
		}
		return nil, 0, nasDAVError(err)
	}
	if resp.StatusCode == http.StatusPartialContent {
		size, ok := nasContentRangeTotal(resp.Header.Get("Content-Range"))
		if !ok {
			size = off + resp.ContentLength
		}
		return resp.Body, size, nil
	}
	// A server that ignores Range sends the whole file: skip what is there.
	size := resp.ContentLength
	if off > 0 {
		if size >= 0 && off > size {
			resp.Body.Close()
			return nil, 0, errNasPartTooBig
		}
		if _, err := io.CopyN(io.Discard, resp.Body, off); err != nil {
			resp.Body.Close()
			return nil, 0, fmt.Errorf("could not skip to the part not copied yet: %w", err)
		}
	}
	return resp.Body, size, nil
}

// nasContentRangeTotal reads the total of "bytes 100-199/1234".
func nasContentRangeTotal(v string) (int64, bool) {
	slash := strings.LastIndexByte(v, '/')
	if slash < 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v[slash+1:]), 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func (d *nasDAV) Close() error { return nil }
