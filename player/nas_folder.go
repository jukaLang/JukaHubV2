package main

// nas_folder.go — a folder the operating system reaches itself as a network
// drive. On a PC that is a mapped drive or a \\nas\share\Games path: Windows
// signs in with its own saved credentials, so JukaHub needs none. The page
// offers this kind on Windows only; the handheld uses SMB, WebDAV or SFTP.

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type nasFolder struct {
	top string
}

func nasDialFolder(src nasSource) (nasFS, error) {
	top := strings.TrimSpace(src.Host)
	if top == "" {
		return nil, errors.New("type the folder's path first, like \\\\nas\\games")
	}
	return &nasFolder{top: filepath.Clean(top)}, nil
}

// local is p below the folder; a name that climbs out is refused.
func (f *nasFolder) local(p string) (string, error) {
	clean, ok := nasCleanRoot(p, false)
	if !ok {
		return "", errNasNotFound
	}
	if clean == "" {
		return f.top, nil
	}
	return filepath.Join(f.top, filepath.FromSlash(clean)), nil
}

func nasFolderError(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return errNasNotFound
	}
	if errors.Is(err, os.ErrPermission) {
		return errNasAuth
	}
	return err
}

func (f *nasFolder) List(_ context.Context, dir string) ([]nasEntry, error) {
	p, err := f.local(dir)
	if err != nil {
		return nil, err
	}
	items, err := os.ReadDir(p)
	if err != nil {
		return nil, nasFolderError(err)
	}
	out := make([]nasEntry, 0, len(items))
	for _, it := range items {
		info, err := it.Info()
		if err != nil {
			continue
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			continue
		}
		out = append(out, nasEntry{Name: it.Name(), Size: info.Size(), ModTime: info.ModTime(), IsDir: info.IsDir()})
	}
	return out, nil
}

func (f *nasFolder) Stat(_ context.Context, p string) (nasEntry, error) {
	lp, err := f.local(p)
	if err != nil {
		return nasEntry{}, err
	}
	info, err := os.Stat(lp)
	if err != nil {
		return nasEntry{}, nasFolderError(err)
	}
	return nasEntry{Name: info.Name(), Size: info.Size(), ModTime: info.ModTime(), IsDir: info.IsDir()}, nil
}

func (f *nasFolder) ReadFrom(_ context.Context, p string, off int64) (io.ReadCloser, int64, error) {
	lp, err := f.local(p)
	if err != nil {
		return nil, 0, err
	}
	file, err := os.Open(lp)
	if err != nil {
		return nil, 0, nasFolderError(err)
	}
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		file.Close()
		return nil, 0, errNasNotFound
	}
	if off < 0 || off > info.Size() {
		file.Close()
		return nil, 0, errNasPartTooBig
	}
	if off > 0 {
		if _, err := file.Seek(off, io.SeekStart); err != nil {
			file.Close()
			return nil, 0, err
		}
	}
	return file, info.Size(), nil
}

func (f *nasFolder) Close() error { return nil }
