package sqlitefs

import (
	"errors"
	"io/fs"
)

// Errors returned by FS operations, always wrapped in an *fs.PathError.
// Missing paths, existing destinations, and malformed paths use the io/fs
// sentinels fs.ErrNotExist, fs.ErrExist, and fs.ErrInvalid respectively.
var (
	ErrNotDir   = errors.New("not a directory")
	ErrIsDir    = errors.New("is a directory")
	ErrNotEmpty = errors.New("directory not empty")
	ErrCycle    = errors.New("cannot move a directory into itself")
)

// ErrNoFTS5 is returned by New when the SQLite driver lacks FTS5 or its
// trigram tokenizer (SQLite 3.34+).
var ErrNoFTS5 = errors.New("sqlitefs: SQLite FTS5 with the trigram tokenizer is required " +
	"(ncruces: driver.Open(dsn, fts5.Register); mattn: build with -tags sqlite_fts5; modernc: built in)")

func pathErr(op, path string, err error) error {
	if err == nil {
		return nil
	}
	return &fs.PathError{Op: op, Path: path, Err: err}
}
