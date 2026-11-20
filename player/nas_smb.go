package main

// nas_smb.go — a Windows or NAS shared folder (SMB2/3) as a network drive:
// nasFS over the read-only client in smb2*.go. JukaHub only reads: it lists
// folders and copies games to the card.

import (
	"context"
	"errors"
	"io"
	"strings"
)

// nasSMB is one signed-in session on one share.
type nasSMB struct {
	s     *smbSession
	share string
}

// nasDialSMB signs in to src's server (as a guest when no user is set).
func nasDialSMB(ctx context.Context, src nasSource) (nasFS, error) {
	if strings.TrimSpace(src.Share) == "" {
		return nil, errNasNoShare
	}
	s, err := smbDial(ctx, src.Host, src.Port, src.User, nasOpenSecret(src.Password), src.Domain)
	if err != nil {
		return nil, nasSMBError(err)
	}
	return &nasSMB{s: s, share: src.Share}, nil
}

// nasSMBShares lists the shared folders of src's server. ok is false when
// the server does not list them: the player types the share's name.
func nasSMBShares(ctx context.Context, src nasSource) (shares []string, listed bool, err error) {
	s, err := smbDial(ctx, src.Host, src.Port, src.User, nasOpenSecret(src.Password), src.Domain)
	if err != nil {
		return nil, true, nasSMBError(err)
	}
	defer s.Close()
	shares, err = s.ListShares(ctx)
	if errors.Is(err, errSMBNoShareList) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, nasSMBError(err)
	}
	return shares, true, nil
}

// nasSMBError turns the client's errors into the drive errors the pages
// explain (nasErrorText).
func nasSMBError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errSMBAuth):
		return errNasAuth
	case errors.Is(err, errSMBNotFound):
		return errNasNotFound
	}
	// The client's own plain sentence (smbErrorText), still unwrapping to
	// the error itself so nasErrorText knows a timeout.
	return &nasSMBErr{text: smbErrorText(err), err: err}
}

// nasSMBErr is an SMB failure in the client's words.
type nasSMBErr struct {
	text string
	err  error
}

func (e *nasSMBErr) Error() string { return e.text }
func (e *nasSMBErr) Unwrap() error { return e.err }

func nasSMBPath(p string) string { return strings.Trim(strings.ReplaceAll(p, `\`, "/"), "/") }

func (n *nasSMB) List(ctx context.Context, dir string) ([]nasEntry, error) {
	items, err := n.s.ReadDir(ctx, n.share, nasSMBPath(dir))
	if err != nil {
		return nil, nasSMBError(err)
	}
	out := make([]nasEntry, 0, len(items))
	for _, it := range items {
		out = append(out, nasEntry{Name: it.Name, Size: it.Size, ModTime: it.ModTime, IsDir: it.IsDir})
	}
	return out, nil
}

func (n *nasSMB) Stat(ctx context.Context, p string) (nasEntry, error) {
	it, err := n.s.Stat(ctx, n.share, nasSMBPath(p))
	if err != nil {
		return nasEntry{}, nasSMBError(err)
	}
	return nasEntry{Name: it.Name, Size: it.Size, ModTime: it.ModTime, IsDir: it.IsDir}, nil
}

func (n *nasSMB) ReadFrom(ctx context.Context, p string, off int64) (io.ReadCloser, int64, error) {
	f, err := n.s.OpenFile(ctx, n.share, nasSMBPath(p))
	if err != nil {
		return nil, 0, nasSMBError(err)
	}
	size := f.Size()
	if off < 0 || off > size {
		f.Close()
		return nil, 0, errNasPartTooBig
	}
	return &nasSectionCloser{SectionReader: io.NewSectionReader(f, off, size-off), closer: f}, size, nil
}

func (n *nasSMB) Close() error { return n.s.Close() }

// nasSectionCloser reads a section of an open file and closes the file.
type nasSectionCloser struct {
	*io.SectionReader
	closer io.Closer
}

func (s *nasSectionCloser) Close() error { return s.closer.Close() }
