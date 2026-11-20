package main

// nas_sftp.go — an SFTP server as a network drive (a NAS with SSH on, a
// Raspberry Pi, a Linux PC): nasFS over pkg/sftp, the client Files' remote
// transfer uses (filetransfer.go).
//
// The server's key is trusted on first use, but never silently: the first
// test on the Network drives page shows its SHA256 fingerprint and stores it
// only when the player says Trust. A background listing or download with no
// stored key, or with a key that changed, is refused (errNasHostKeyUnknown,
// nasHostKeyChanged); the page explains it and asks again.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// nasHostKeyChanged is a server whose key is not the trusted one.
type nasHostKeyChanged struct {
	Trusted, Now string
}

func (e *nasHostKeyChanged) Error() string {
	return "the SFTP server's identity changed (trusted " + e.Trusted + ", now " + e.Now + ") — if you did not reinstall it, do not trust it"
}

type nasSFTP struct {
	conn *ssh.Client
	c    *sftp.Client
}

// nasDialSFTP signs in to src. seen, when not nil, receives the server's
// fingerprint even when the key is refused (the page's trust question).
func nasDialSFTP(ctx context.Context, src nasSource, seen *string) (nasFS, error) {
	port := src.Port
	if port <= 0 {
		port = 22
	}
	addr := net.JoinHostPort(strings.TrimSpace(src.Host), strconv.Itoa(port))
	pw := nasOpenSecret(src.Password)
	trusted := strings.TrimSpace(src.HostKey)
	cfg := &ssh.ClientConfig{
		User: src.User,
		Auth: []ssh.AuthMethod{
			ssh.Password(pw),
			// Servers that ask through keyboard-interactive get the same
			// password for each question.
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = pw
				}
				return answers, nil
			}),
		},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			got := ssh.FingerprintSHA256(key)
			if seen != nil {
				*seen = got
			}
			switch {
			case trusted == "":
				return errNasHostKeyUnknown
			case trusted != got:
				return &nasHostKeyChanged{Trusted: trusted, Now: got}
			}
			return nil
		},
		Timeout: 12 * time.Second,
	}
	d := net.Dialer{Timeout: 12 * time.Second, KeepAlive: 30 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	// The handshake has no context of its own: a deadline bounds it.
	deadline := time.Now().Add(20 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = raw.SetDeadline(deadline)
	cc, chans, reqs, err := ssh.NewClientConn(raw, addr, cfg)
	if err != nil {
		raw.Close()
		return nil, nasSFTPError(err)
	}
	_ = raw.SetDeadline(time.Time{})
	client := ssh.NewClient(cc, chans, reqs)
	sc, err := sftp.NewClient(client)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("the server has no SFTP: %w", err)
	}
	return &nasSFTP{conn: client, c: sc}, nil
}

func nasSFTPError(err error) error {
	var changed *nasHostKeyChanged
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNasHostKeyUnknown):
		return errNasHostKeyUnknown
	case errors.As(err, &changed):
		return changed
	case errors.Is(err, os.ErrNotExist):
		return errNasNotFound
	case strings.Contains(err.Error(), "unable to authenticate"):
		return errNasAuth
	}
	return err
}

// full is p as the server takes it: "" is the login folder.
func (n *nasSFTP) full(p string) string {
	if p == "" {
		return "."
	}
	return p
}

func (n *nasSFTP) List(ctx context.Context, dir string) ([]nasEntry, error) {
	infos, err := n.c.ReadDirContext(ctx, n.full(dir))
	if err != nil {
		return nil, nasSFTPError(err)
	}
	out := make([]nasEntry, 0, len(infos))
	for _, fi := range infos {
		isDir := fi.IsDir()
		if fi.Mode()&os.ModeSymlink != 0 {
			// A link is followed once, to see whether it is a folder.
			if st, err := n.c.Stat(path.Join(n.full(dir), fi.Name())); err == nil {
				fi, isDir = st, st.IsDir()
			} else {
				continue
			}
		}
		if !isDir && !fi.Mode().IsRegular() {
			continue
		}
		out = append(out, nasEntry{Name: fi.Name(), Size: fi.Size(), ModTime: fi.ModTime(), IsDir: isDir})
	}
	return out, nil
}

func (n *nasSFTP) Stat(_ context.Context, p string) (nasEntry, error) {
	fi, err := n.c.Stat(n.full(p))
	if err != nil {
		return nasEntry{}, nasSFTPError(err)
	}
	return nasEntry{Name: fi.Name(), Size: fi.Size(), ModTime: fi.ModTime(), IsDir: fi.IsDir()}, nil
}

func (n *nasSFTP) ReadFrom(_ context.Context, p string, off int64) (io.ReadCloser, int64, error) {
	f, err := n.c.Open(n.full(p))
	if err != nil {
		return nil, 0, nasSFTPError(err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, nasSFTPError(err)
	}
	size := fi.Size()
	if off < 0 || off > size {
		f.Close()
		return nil, 0, errNasPartTooBig
	}
	if off > 0 {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			f.Close()
			return nil, 0, err
		}
	}
	return f, size, nil
}

func (n *nasSFTP) Close() error {
	n.c.Close()
	return n.conn.Close()
}
