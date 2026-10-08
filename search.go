package sqlitefs

import (
	"context"
	"database/sql"
	"io/fs"
	"iter"
	"strings"
	"unicode/utf8"

	"github.com/axe-or/sqlite-fs/internal/db"
)

// searchPageSize is how many results Search fetches per transaction.
var searchPageSize = 64

// SearchResult is a node whose name matched a Search query.
type SearchResult struct {
	Path string // absolute path
	Info *FileInfo
}

// Search iterates over the files and directories below the directory under
// (use "/" for everything) whose names contain query, case-insensitively.
//
// Results are ranked: names equal to the query first, then names starting
// with it, then any other match; ties go to shorter names, then by name.
// Queries of three or more characters use the trigram index; shorter queries
// scan every name.
//
// Like Entries, results are fetched in pages, each in its own transaction, so
// the filesystem may be modified during iteration and changes may or may not
// be reflected. Stop early by breaking out of the loop. On failure the
// iterator yields a single non-nil error and stops.
func (f *FS) Search(ctx context.Context, query, under string) iter.Seq2[SearchResult, error] {
	return func(yield func(SearchResult, error) bool) {
		fail := func(err error) { yield(SearchResult{}, pathErr("search", under, err)) }
		if query == "" || strings.IndexByte(query, 0) >= 0 {
			fail(fs.ErrInvalid)
			return
		}

		params := db.SearchPageParams{
			Query:      query,
			UseIndex:   utf8.RuneCountInString(query) >= 3,
			AfterClass: -1,
			Limit:      int64(searchPageSize),
		}
		if params.UseIndex {
			params.Pattern = `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
		} else {
			params.Pattern = "%" + likeEscaper.Replace(query) + "%"
		}

		resolved := false
		for {
			var page []SearchResult
			var rows []db.SearchPageRow
			err := f.tx(ctx, func(_ *sql.Tx, q *db.Queries) error {
				if !resolved {
					parts, err := splitPath(under)
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
					params.ScopeID, resolved = id, true
				}
				var err error
				if rows, err = q.SearchPage(ctx, params); err != nil {
					return err
				}
				// Build paths in the same transaction so they match the rows.
				page = make([]SearchResult, len(rows))
				for i, r := range rows {
					path, err := q.NodePath(ctx, r.ID)
					if err != nil {
						return err
					}
					page[i] = SearchResult{
						Path: path,
						Info: newFileInfo(r.Name, r.Kind, r.Size, r.CreatedAt, r.ModifiedAt),
					}
				}
				return nil
			})
			if err != nil {
				fail(err)
				return
			}
			for _, res := range page {
				if !yield(res, nil) {
					return
				}
			}
			if len(rows) < searchPageSize {
				return
			}
			last := rows[len(rows)-1]
			params.AfterClass, params.AfterNameLen, params.AfterName, params.AfterID =
				last.Class, last.NameLen, last.Name, last.ID
		}
	}
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
