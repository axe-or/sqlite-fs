SQLite backed filesystem.

# Usage

Open the database with whichever SQLite driver you prefer and hand the `*sql.DB` over. The driver must provide FTS5 with the trigram tokenizer (SQLite 3.34+), which name search uses. `New` returns `sqlitefs.ErrNoFTS5` otherwise.

```go
import (
	sqlitefs "github.com/axe-or/sqlite-fs"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"
)

db, err := driver.Open("file:myfs.db?_pragma=busy_timeout(5000)&_txlock=immediate", fts5.Register)
fsys, err := sqlitefs.New(ctx, db)
```

| Driver | Enabling FTS5 |
|---|---|
| `github.com/ncruces/go-sqlite3` | `driver.Open(dsn, fts5.Register)` |
| `github.com/mattn/go-sqlite3` | build with `-tags sqlite_fts5` |
| `modernc.org/sqlite` | built in |

For concurrent writers, set a busy timeout and, if your driver supports it, immediate transactions (as above). Otherwise lock upgrades may fail with `SQLITE_BUSY`.

# Interface

Every function takes a `context.Context` for cancellation. Paths are absolute and slash-separated, and are cleaned before use, so `..` cannot escape the root. Every operation is atomic.

```
New(ctx, db) // Opens a file system, inits tables as needed

ReadFile(ctx, "/path/to/file") // Reads a whole file

WriteFile(ctx, "/path/to/file", data) // Writes data to file, creates it if doesn't exist, does NOT create dirs

Move(ctx, "/path1", "/path2") // Renames path, cannot rename into a subpath of itself! Destination must not exist

Mkdir(ctx, "/path/to/dir") // Creates a directory, creates intermediary dirs as well

RemoveFile(ctx, "/path/to/something") // Removes a file

RemoveDir(ctx, "/path/to/dir", false) // Removes a directory, bool indicates whether to recurse or stop if there's any items

Stat(ctx, "/path/to/something") // Name, Kind, Size, CreatedAt, ModifiedAt (implements fs.FileInfo)

ReadDir(ctx, "/path/to/dir") // Lists a directory, sorted by name

Entries(ctx, "/path/to/dir") // iter.Seq2[*FileInfo, error] over a directory, sorted by name, fetched in pages

Search(ctx, "report", "/path/to/dir") // iter.Seq2[SearchResult, error] of names containing "report" (case-insensitive) below the dir, ranked exact > prefix > substring, then shorter names

IOFS(ctx) // Read-only io/fs view (fs.FS, ReadFileFS, ReadDirFS, StatFS)
```

## Errors

All errors are `*fs.PathError` and can be checked with `errors.Is`:

| Error                  | When                                                                                  |
| ---------------------- | ------------------------------------------------------------------------------------- |
| `fs.ErrNotExist`       | path (or its parent) does not exist                                                   |
| `fs.ErrExist`          | `Move` destination already exists                                                     |
| `fs.ErrInvalid`        | malformed path (relative, empty, NUL byte), removing or moving the root               |
| `sqlitefs.ErrNotDir`   | a directory was expected (path component, `Mkdir` over a file, `RemoveDir` on a file) |
| `sqlitefs.ErrIsDir`    | a file was expected (`ReadFile`, `WriteFile`, `RemoveFile` on a directory)            |
| `sqlitefs.ErrNotEmpty` | non-recursive `RemoveDir` on a non-empty directory                                    |
| `sqlitefs.ErrCycle`    | `Move` of a directory into its own subtree                                            |
| `sqlitefs.ErrNoFTS5`   | returned by `New` when the driver lacks FTS5 or the trigram tokenizer                 |

# Explicitly out of scope

- Symlinks are strictly disallowed, the file system is acyclical graph
- UNIX permissions, this filesystem is only for files and directories, no pipes, symlinks, hardlinks, etc.

# Potentially in-scope for a v2

- CoW and streaming APIs

# Dependencies

_zero_ runtime dependencies. This library is literally just SQL + Logic

For testing and developing, sqlc and ncruces sqlite driver are used

```
go generate ./...   # sqlc generate: sqlc.json -> internal/db
go test ./...
```
