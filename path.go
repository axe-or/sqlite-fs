package sqlitefs

import (
	"io/fs"
	"path"
	"strings"
)

// splitPath normalises an absolute slash-separated path and returns its
// components. The root "/" yields no components.
func splitPath(p string) ([]string, error) {
	if p == "" || p[0] != '/' || strings.IndexByte(p, 0) >= 0 {
		return nil, fs.ErrInvalid
	}
	p = path.Clean(p)
	if p == "/" {
		return nil, nil
	}
	return strings.Split(p[1:], "/"), nil
}
