package sqlitefs

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"

	"github.com/axe-or/sqlite-fs/internal/db"
)

// IOFS returns a read-only io/fs view of f. Since io/fs methods take no
// context, every call made through the view uses ctx.
//
// Paths follow io/fs conventions: unrooted and slash-separated, with "."
// naming the root.
func (f *FS) IOFS(ctx context.Context) fs.FS {
	return &ioFS{f: f, ctx: ctx}
}

type ioFS struct {
	f   *FS
	ctx context.Context
}

var (
	_ fs.ReadFileFS = (*ioFS)(nil)
	_ fs.ReadDirFS  = (*ioFS)(nil)
	_ fs.StatFS     = (*ioFS)(nil)
)

// native converts an io/fs path to a native absolute path.
func native(op, name string) (string, error) {
	if !fs.ValidPath(name) {
		return "", &fs.PathError{Op: op, Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		return "/", nil
	}
	return "/" + name, nil
}

// rewrap replaces the native path in err with the io/fs name.
func rewrap(op, name string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return &fs.PathError{Op: op, Path: name, Err: pe.Err}
	}
	return &fs.PathError{Op: op, Path: name, Err: err}
}

func (s *ioFS) Open(name string) (fs.File, error) {
	p, err := native("open", name)
	if err != nil {
		return nil, err
	}
	parts, _ := splitPath(p)

	var (
		info    *FileInfo
		data    []byte
		entries []fs.DirEntry
	)
	err = s.f.tx(s.ctx, func(_ *sql.Tx, q *db.Queries) error {
		id, kind, err := resolve(s.ctx, q, parts)
		if err != nil {
			return err
		}
		row, err := q.StatNode(s.ctx, id)
		if err != nil {
			return err
		}
		info = newFileInfo(row.Name, row.Kind, row.Size, row.CreatedAt, row.ModifiedAt)
		if kind == KindFile {
			data, err = q.GetData(s.ctx, id)
			return err
		}
		rows, err := q.ListChildren(s.ctx, parentRef(id))
		if err != nil {
			return err
		}
		entries = make([]fs.DirEntry, len(rows))
		for i, r := range rows {
			entries[i] = newFileInfo(r.Name, r.Kind, r.Size, r.CreatedAt, r.ModifiedAt)
		}
		return nil
	})
	if err != nil {
		return nil, rewrap("open", name, err)
	}
	if name == "." {
		info.name = "."
	}
	if info.IsDir() {
		return &dirFile{info: info, entries: entries}, nil
	}
	return &file{info: info, Reader: bytes.NewReader(data)}, nil
}

func (s *ioFS) ReadFile(name string) ([]byte, error) {
	p, err := native("readfile", name)
	if err != nil {
		return nil, err
	}
	data, err := s.f.ReadFile(s.ctx, p)
	if err != nil {
		return nil, rewrap("readfile", name, err)
	}
	return data, nil
}

func (s *ioFS) Stat(name string) (fs.FileInfo, error) {
	p, err := native("stat", name)
	if err != nil {
		return nil, err
	}
	fi, err := s.f.Stat(s.ctx, p)
	if err != nil {
		return nil, rewrap("stat", name, err)
	}
	if name == "." {
		fi.name = "."
	}
	return fi, nil
}

func (s *ioFS) ReadDir(name string) ([]fs.DirEntry, error) {
	p, err := native("readdir", name)
	if err != nil {
		return nil, err
	}
	infos, err := s.f.ReadDir(s.ctx, p)
	if err != nil {
		return nil, rewrap("readdir", name, err)
	}
	entries := make([]fs.DirEntry, len(infos))
	for i, fi := range infos {
		entries[i] = fi
	}
	return entries, nil
}

// file is an open regular file; its contents are loaded on Open.
type file struct {
	info *FileInfo
	*bytes.Reader
}

func (f *file) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *file) Close() error               { return nil }

// dirFile is an open directory; its listing is loaded on Open.
type dirFile struct {
	info    *FileInfo
	entries []fs.DirEntry
	offset  int
}

func (d *dirFile) Stat() (fs.FileInfo, error) { return d.info, nil }
func (d *dirFile) Close() error               { return nil }

func (d *dirFile) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.info.Name(), Err: ErrIsDir}
}

func (d *dirFile) ReadDir(n int) ([]fs.DirEntry, error) {
	rest := d.entries[d.offset:]
	if n <= 0 {
		d.offset = len(d.entries)
		return rest, nil
	}
	if len(rest) == 0 {
		return nil, io.EOF
	}
	n = min(n, len(rest))
	d.offset += n
	return rest[:n], nil
}
