package sqlitefs

import (
	"context"
	"database/sql"
	"iter"

	"github.com/axe-or/sqlite-fs/internal/db"
)

// entriesPageSize is how many entries Entries fetches per transaction.
var entriesPageSize = 256

// Entries iterates over the entries of the directory at p, sorted by name.
//
// Entries are fetched in pages, each in its own short transaction, so no
// database lock is held while the loop body runs and it is safe to modify the
// filesystem during iteration. Unlike ReadDir, the result is therefore not a
// snapshot: entries added or removed mid-iteration may or may not be seen.
// The directory is resolved once, so iteration follows it if it is moved and
// ends early if it is removed.
//
// On failure the iterator yields a single non-nil error and stops.
func (f *FS) Entries(ctx context.Context, p string) iter.Seq2[*FileInfo, error] {
	return func(yield func(*FileInfo, error) bool) {
		var (
			dir      int64
			resolved bool
			after    string // every name sorts after "" (only the root is unnamed)
		)
		for {
			var page []db.ListChildrenAfterRow
			err := f.tx(ctx, func(_ *sql.Tx, q *db.Queries) error {
				if !resolved {
					parts, err := splitPath(p)
					if err != nil {
						return err
					}
					id, kind, err := resolve(ctx, q, parts)
					if err != nil {
						return err
					}
					if kind != KindDir {
						return ErrNotDir
					}
					dir, resolved = id, true
				}
				var err error
				page, err = q.ListChildrenAfter(ctx, db.ListChildrenAfterParams{
					ParentID: parentRef(dir), Name: after, Limit: int64(entriesPageSize),
				})
				return err
			})
			if err != nil {
				yield(nil, pathErr("readdir", p, err))
				return
			}
			for _, r := range page {
				if !yield(newFileInfo(r.Name, r.Kind, r.Size, r.CreatedAt, r.ModifiedAt), nil) {
					return
				}
			}
			if len(page) < entriesPageSize {
				return
			}
			after = page[len(page)-1].Name
		}
	}
}
