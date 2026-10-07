SQLite backed filesystem.

# Usage

Open the database with whichever SQLite driver you prefer and hand the `*sql.DB` over:

```go
import (
	"database/sql"

	sqlitefs "github.com/axe-or/sqlite-fs"
	_ "github.com/ncruces/go-sqlite3/driver" // or any other SQLite driver
)

db, err := sql.Open("sqlite3", "file:myfs.db?_pragma=busy_timeout(5000)&_txlock=immediate")
fsys, err := sqlitefs.New(ctx, db)
```

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

# Explicitly out of scope

- Symlinks are strictly disallowed, the file system is acyclical graph, parent pointers are fine as implementatino detail
- UNIX permissions, this filesystem is only for files and directories, no pipes, symlinks, hardlinks, etc.

# Potentially in-scope for a v2

- CoW and streaming APIs

# Dependencies

_zero_ runtime dependencies. This library is literally just SQL + Logic

For testing and developing, sqlc and ncruces sqlite driver shall be used.

```
go generate ./...   # sqlc generate: sqlc.json -> internal/db
go test ./...
```
