package sqlitefs

import (
	"io/fs"
	"time"
)

// Kind is the type of a filesystem node.
type Kind int

const (
	KindDir  Kind = 0
	KindFile Kind = 1
)

func (k Kind) String() string {
	switch k {
	case KindDir:
		return "dir"
	case KindFile:
		return "file"
	}
	return "unknown"
}

// FileInfo describes a node. It implements both fs.FileInfo and fs.DirEntry.
type FileInfo struct {
	name       string
	Kind       Kind
	size       int64
	CreatedAt  time.Time
	ModifiedAt time.Time
}

var (
	_ fs.FileInfo = (*FileInfo)(nil)
	_ fs.DirEntry = (*FileInfo)(nil)
)

func (fi *FileInfo) Name() string       { return fi.name }
func (fi *FileInfo) Size() int64        { return fi.size }
func (fi *FileInfo) ModTime() time.Time { return fi.ModifiedAt }
func (fi *FileInfo) IsDir() bool        { return fi.Kind == KindDir }
func (fi *FileInfo) Sys() any           { return nil }

func (fi *FileInfo) Mode() fs.FileMode {
	if fi.IsDir() {
		return fs.ModeDir
	}
	return 0
}

func (fi *FileInfo) Type() fs.FileMode          { return fi.Mode().Type() }
func (fi *FileInfo) Info() (fs.FileInfo, error) { return fi, nil }
func (fi *FileInfo) String() string             { return fs.FormatFileInfo(fi) }

func newFileInfo(name string, kind, size, createdAt, modifiedAt int64) *FileInfo {
	return &FileInfo{
		name:       name,
		Kind:       Kind(kind),
		size:       size,
		CreatedAt:  time.Unix(0, createdAt),
		ModifiedAt: time.Unix(0, modifiedAt),
	}
}
