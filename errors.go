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

func pathErr(op, path string, err error) error {
	if err == nil {
		return nil
	}
	return &fs.PathError{Op: op, Path: path, Err: err}
}
