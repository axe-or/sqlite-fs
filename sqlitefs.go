// Package sqlitefs implements a filesystem of plain files and directories
// stored in a SQLite database.
//
// The package has no dependencies outside the standard library: callers open
// the database with whichever SQLite driver they prefer and pass the *sql.DB
// to New. Every operation runs in its own transaction.
package sqlitefs

//go:generate sqlc generate

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/axe-or/sqlite-fs/internal/db"
)

//go:embed sql/schema.sql
var schemaSQL string

const (
	schemaVersion = 1
	rootID        = 1
)

// FS is a filesystem backed by a SQLite database.
type FS struct {
	db  *sql.DB
	now func() time.Time
}

// New returns an FS stored in db, creating its tables if needed.
//
// The driver must provide FTS5 with the trigram tokenizer, which name search
// relies on; New returns ErrNoFTS5 otherwise.
//
// For concurrent writers, configure the connection with a busy timeout and,
// where the driver allows it, immediate transactions (e.g. ncruces:
// "file:x.db?_pragma=busy_timeout(5000)&_txlock=immediate").
func New(ctx context.Context, sqlDB *sql.DB) (*FS, error) {
	f := &FS{db: sqlDB, now: time.Now}
	err := f.tx(ctx, func(tx *sql.Tx, q *db.Queries) error {
		var version int
		if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
		switch version {
		case schemaVersion:
			// Every connection that writes needs FTS5 for the index triggers,
			// so check it on existing databases too.
			_, err := tx.ExecContext(ctx, "SELECT rowid FROM fs_node_name LIMIT 0")
			return checkFTS5(err)
		case 0:
		default:
			return fmt.Errorf("sqlitefs: unsupported schema version %d", version)
		}
		if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
			return checkFTS5(err)
		}
		now := f.now().UnixNano()
		if err := q.InsertRoot(ctx, db.InsertRootParams{CreatedAt: now, ModifiedAt: now}); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion))
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("sqlitefs: init: %w", err)
	}
	return f, nil
}

// checkFTS5 reports a missing FTS5 module or trigram tokenizer as ErrNoFTS5.
// SQLite's error text is the only signal drivers have in common.
func checkFTS5(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "no such module: fts5") || strings.Contains(msg, "no such tokenizer: trigram") {
		return fmt.Errorf("%w (%v)", ErrNoFTS5, err)
	}
	return err
}

func (f *FS) tx(ctx context.Context, fn func(tx *sql.Tx, q *db.Queries) error) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx, db.New(tx)); err != nil {
		return err
	}
	return tx.Commit()
}

func parentRef(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: true} }

// lookup returns the child called name of directory parent.
func lookup(ctx context.Context, q *db.Queries, parent int64, name string) (id int64, kind Kind, err error) {
	row, err := q.GetChild(ctx, db.GetChildParams{ParentID: parentRef(parent), Name: name})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, fs.ErrNotExist
	}
	if err != nil {
		return 0, 0, err
	}
	return row.ID, Kind(row.Kind), nil
}

// resolve walks parts from the root and returns the node they name.
func resolve(ctx context.Context, q *db.Queries, parts []string) (id int64, kind Kind, err error) {
	id, kind = rootID, KindDir
	for _, name := range parts {
		if kind != KindDir {
			return 0, 0, ErrNotDir
		}
		if id, kind, err = lookup(ctx, q, id, name); err != nil {
			return 0, 0, err
		}
	}
	return id, kind, nil
}

// resolveParent returns the directory containing the last element of parts.
func resolveParent(ctx context.Context, q *db.Queries, parts []string) (int64, error) {
	id, kind, err := resolve(ctx, q, parts[:len(parts)-1])
	if err != nil {
		return 0, err
	}
	if kind != KindDir {
		return 0, ErrNotDir
	}
	return id, nil
}

func (f *FS) touch(ctx context.Context, q *db.Queries, ids ...int64) error {
	now := f.now().UnixNano()
	for _, id := range ids {
		if err := q.Touch(ctx, db.TouchParams{ModifiedAt: now, ID: id}); err != nil {
			return err
		}
	}
	return nil
}

// ReadFile returns the contents of the file at p.
func (f *FS) ReadFile(ctx context.Context, p string) ([]byte, error) {
	var data []byte
	err := f.tx(ctx, func(_ *sql.Tx, q *db.Queries) error {
		parts, err := splitPath(p)
		if err != nil {
			return err
		}
		id, kind, err := resolve(ctx, q, parts)
		if err != nil {
			return err
		}
		if kind != KindFile {
			return ErrIsDir
		}
		data, err = q.GetData(ctx, id)
		return err
	})
	if err != nil {
		return nil, pathErr("read", p, err)
	}
	return data, nil
}

// WriteFile replaces the contents of the file at p, creating the file if it
// does not exist. The parent directory must already exist.
func (f *FS) WriteFile(ctx context.Context, p string, data []byte) error {
	if data == nil {
		data = []byte{}
	}
	err := f.tx(ctx, func(_ *sql.Tx, q *db.Queries) error {
		parts, err := splitPath(p)
		if err != nil {
			return err
		}
		if len(parts) == 0 {
			return ErrIsDir
		}
		parent, err := resolveParent(ctx, q, parts)
		if err != nil {
			return err
		}
		name := parts[len(parts)-1]
		now := f.now().UnixNano()
		id, kind, err := lookup(ctx, q, parent, name)
		switch {
		case err == nil && kind == KindDir:
			return ErrIsDir
		case err == nil:
			return q.UpdateData(ctx, db.UpdateDataParams{Data: data, ModifiedAt: now, ID: id})
		case errors.Is(err, fs.ErrNotExist):
			_, err = q.InsertFile(ctx, db.InsertFileParams{
				ParentID: parentRef(parent), Name: name, CreatedAt: now, ModifiedAt: now, Data: data,
			})
			if err != nil {
				return err
			}
			return f.touch(ctx, q, parent)
		default:
			return err
		}
	})
	return pathErr("write", p, err)
}

// Mkdir creates the directory at p along with any missing parents.
// It is not an error if the directory already exists.
func (f *FS) Mkdir(ctx context.Context, p string) error {
	err := f.tx(ctx, func(_ *sql.Tx, q *db.Queries) error {
		parts, err := splitPath(p)
		if err != nil {
			return err
		}
		var id int64 = rootID
		for _, name := range parts {
			child, kind, err := lookup(ctx, q, id, name)
			switch {
			case err == nil && kind != KindDir:
				return ErrNotDir
			case err == nil:
				id = child
			case errors.Is(err, fs.ErrNotExist):
				now := f.now().UnixNano()
				child, err = q.InsertDir(ctx, db.InsertDirParams{
					ParentID: parentRef(id), Name: name, CreatedAt: now, ModifiedAt: now,
				})
				if err != nil {
					return err
				}
				if err := f.touch(ctx, q, id); err != nil {
					return err
				}
				id = child
			default:
				return err
			}
		}
		return nil
	})
	return pathErr("mkdir", p, err)
}

// RemoveFile removes the file at p.
func (f *FS) RemoveFile(ctx context.Context, p string) error {
	err := f.tx(ctx, func(_ *sql.Tx, q *db.Queries) error {
		parts, err := splitPath(p)
		if err != nil {
			return err
		}
		if len(parts) == 0 {
			return ErrIsDir
		}
		parent, err := resolveParent(ctx, q, parts)
		if err != nil {
			return err
		}
		id, kind, err := lookup(ctx, q, parent, parts[len(parts)-1])
		if err != nil {
			return err
		}
		if kind != KindFile {
			return ErrIsDir
		}
		if err := q.DeleteNode(ctx, id); err != nil {
			return err
		}
		return f.touch(ctx, q, parent)
	})
	return pathErr("remove", p, err)
}

// RemoveDir removes the directory at p. If recursive is false the directory
// must be empty; otherwise its whole subtree is removed. The root directory
// cannot be removed.
func (f *FS) RemoveDir(ctx context.Context, p string, recursive bool) error {
	err := f.tx(ctx, func(_ *sql.Tx, q *db.Queries) error {
		parts, err := splitPath(p)
		if err != nil {
			return err
		}
		if len(parts) == 0 {
			return fs.ErrInvalid
		}
		parent, err := resolveParent(ctx, q, parts)
		if err != nil {
			return err
		}
		id, kind, err := lookup(ctx, q, parent, parts[len(parts)-1])
		if err != nil {
			return err
		}
		if kind != KindDir {
			return ErrNotDir
		}
		if recursive {
			err = q.DeleteSubtree(ctx, id)
		} else {
			var has bool
			if has, err = q.HasChildren(ctx, parentRef(id)); err != nil {
				return err
			}
			if has {
				return ErrNotEmpty
			}
			err = q.DeleteNode(ctx, id)
		}
		if err != nil {
			return err
		}
		return f.touch(ctx, q, parent)
	})
	return pathErr("remove", p, err)
}

// Move renames src to dst. The destination must not exist, its parent must
// be an existing directory, and a directory cannot be moved into its own
// subtree.
func (f *FS) Move(ctx context.Context, src, dst string) error {
	err := f.tx(ctx, func(_ *sql.Tx, q *db.Queries) error {
		srcParts, err := splitPath(src)
		if err != nil {
			return err
		}
		dstParts, err := splitPath(dst)
		if err != nil {
			return err
		}
		if len(srcParts) == 0 || len(dstParts) == 0 {
			return fs.ErrInvalid
		}
		srcParent, err := resolveParent(ctx, q, srcParts)
		if err != nil {
			return err
		}
		id, _, err := lookup(ctx, q, srcParent, srcParts[len(srcParts)-1])
		if err != nil {
			return err
		}
		dstParent, err := resolveParent(ctx, q, dstParts)
		if err != nil {
			return err
		}
		dstName := dstParts[len(dstParts)-1]
		if dstParent == srcParent && dstName == srcParts[len(srcParts)-1] {
			return nil
		}
		cycle, err := q.IsAncestor(ctx, db.IsAncestorParams{NodeID: dstParent, AncestorID: id})
		if err != nil {
			return err
		}
		if cycle != 0 {
			return ErrCycle
		}
		_, _, err = lookup(ctx, q, dstParent, dstName)
		if err == nil {
			return fs.ErrExist
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := q.MoveNode(ctx, db.MoveNodeParams{ParentID: parentRef(dstParent), Name: dstName, ID: id}); err != nil {
			return err
		}
		if srcParent == dstParent {
			return f.touch(ctx, q, srcParent)
		}
		return f.touch(ctx, q, srcParent, dstParent)
	})
	if err != nil {
		return &fs.PathError{Op: "move", Path: src + " -> " + dst, Err: err}
	}
	return nil
}

// Stat returns information about the node at p.
func (f *FS) Stat(ctx context.Context, p string) (*FileInfo, error) {
	var fi *FileInfo
	err := f.tx(ctx, func(_ *sql.Tx, q *db.Queries) error {
		parts, err := splitPath(p)
		if err != nil {
			return err
		}
		id, _, err := resolve(ctx, q, parts)
		if err != nil {
			return err
		}
		row, err := q.StatNode(ctx, id)
		if err != nil {
			return err
		}
		fi = newFileInfo(row.Name, row.Kind, row.Size, row.CreatedAt, row.ModifiedAt)
		if id == rootID {
			fi.name = "/"
		}
		return nil
	})
	if err != nil {
		return nil, pathErr("stat", p, err)
	}
	return fi, nil
}

// ReadDir returns the entries of the directory at p, sorted by name.
func (f *FS) ReadDir(ctx context.Context, p string) ([]*FileInfo, error) {
	var entries []*FileInfo
	err := f.tx(ctx, func(_ *sql.Tx, q *db.Queries) error {
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
		rows, err := q.ListChildren(ctx, parentRef(id))
		if err != nil {
			return err
		}
		entries = make([]*FileInfo, len(rows))
		for i, r := range rows {
			entries[i] = newFileInfo(r.Name, r.Kind, r.Size, r.CreatedAt, r.ModifiedAt)
		}
		return nil
	})
	if err != nil {
		return nil, pathErr("readdir", p, err)
	}
	return entries, nil
}
