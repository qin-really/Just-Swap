// Package store is the only thing that touches the download directory.
//
// Every public method re-validates its name. Callers must not trust the names
// they hand back — a browser on the LAN can send anything, and the path built
// here is the last line of defence against traversal.
package store

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// MaxNameBytes caps a file name. Filenames longer than this almost always
	// mean the client sent a path, not a name.
	MaxNameBytes = 200

	bufSize    = 1 << 20
	partSuffix = ".part"
)

type Store struct {
	base string
	pool sync.Pool
}

func New(base string) (*Store, error) {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, err
	}
	return &Store{
		base: base,
		pool: sync.Pool{New: func() any { return make([]byte, bufSize) }},
	}, nil
}

// SafeName strips path separators, NULs, and edge-of-string dots or spaces,
// then rejects traversal, absolute paths, drive letters, Windows
// alternate-data names, and over-long names.
//
// Traversal is rejected before stripping rather than defused by it: stripping
// would turn "../../etc/passwd" into the harmless-but-odd "..etcpasswd", which
// hides that the caller sent a path. Separators that are not part of a
// traversal are still stripped, so "photos/img.png" arrives as one usable file.
func SafeName(name string) (string, error) {
	if strings.HasPrefix(name, `/`) || strings.HasPrefix(name, `\`) {
		return "", errors.New("absolute file name not allowed")
	}
	for _, part := range strings.FieldsFunc(name, func(r rune) bool {
		return r == '/' || r == '\\' || r == 0
	}) {
		if part == "." || part == ".." {
			return "", errors.New("path traversal not allowed")
		}
	}
	for _, sep := range []string{`/`, `\`, "\x00"} {
		name = strings.ReplaceAll(name, sep, "")
	}
	name = strings.TrimSpace(name)
	name = strings.TrimRight(name, ".")
	if name == "" || name == "." || name == ".." {
		return "", errors.New("invalid file name")
	}
	if strings.Contains(name, ":") {
		return "", errors.New("invalid file name")
	}
	if len(name) > MaxNameBytes {
		return "", errors.New("file name too long")
	}
	return name, nil
}

// Put streams r into base under a collision-free name and reports the bytes
// actually written. The transfer goes through a .part file first so an
// interrupted upload never leaves a truncated file that looks complete.
func (s *Store) Put(name string, r io.Reader) (string, int64, error) {
	n, err := SafeName(name)
	if err != nil {
		return "", 0, err
	}
	part := s.path(n) + partSuffix
	f, err := os.Create(part)
	if err != nil {
		return "", 0, err
	}
	buf := s.pool.Get().([]byte)
	defer s.pool.Put(buf)
	written, cerr := io.CopyBuffer(f, r, buf)
	if rerr := f.Close(); cerr == nil {
		cerr = rerr
	}
	if cerr != nil {
		_ = os.Remove(part)
		return "", 0, cerr
	}
	final := s.UniqueName(n)
	if err := os.Rename(part, s.path(final)); err != nil {
		_ = os.Remove(part)
		return "", 0, err
	}
	return final, written, nil
}

// UniqueName gives the "name (1).ext" rename used when a file is already there.
func (s *Store) UniqueName(name string) string {
	if !s.exists(name) {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		cand := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if !s.exists(cand) {
			return cand
		}
	}
}

func (s *Store) Open(name string) (*os.File, error) {
	n, err := SafeName(name)
	if err != nil {
		return nil, err
	}
	return os.Open(s.path(n))
}

// Remove is idempotent: a missing file is not an error. Expired files may have
// been cleaned by housekeeping between the list call and the delete.
func (s *Store) Remove(name string) error {
	n, err := SafeName(name)
	if err != nil {
		return err
	}
	if err := os.Remove(s.path(n)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return nil
}

// Wipe deletes everything under base. Called at startup, where any leftover
// file is an orphan whose metadata was never persisted, and on shutdown when
// the user opted into clearing files.
func (s *Store) Wipe() error {
	return filepath.WalkDir(s.base, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == s.base || d.IsDir() {
			return nil
		}
		return os.Remove(p)
	})
}

func (s *Store) path(name string) string {
	return filepath.Join(s.base, name)
}

func (s *Store) exists(name string) bool {
	_, err := os.Stat(s.path(name))
	return err == nil
}
