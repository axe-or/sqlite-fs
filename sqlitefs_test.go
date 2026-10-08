package sqlitefs

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"
)

// openDB opens file with the FTS5 extension registered on every connection.
func openDB(t *testing.T, file string) *sql.DB {
	t.Helper()
	return openDBWith(t, file, fts5.Register)
}

func openDBWith(t *testing.T, file string, init ...func(*sqlite3.Conn) error) *sql.DB {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(file) + "?_pragma=busy_timeout(10000)&_txlock=immediate"
	sqlDB, err := driver.Open(dsn, init...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return sqlDB
}

// newFS returns an FS on a fresh database whose clock advances one second
// per call, starting at the unix epoch.
func newFS(t *testing.T) (*FS, *sql.DB) {
	t.Helper()
	sqlDB := openDB(t, filepath.Join(t.TempDir(), "fs.db"))
	f, err := New(t.Context(), sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var tick int64
	f.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		tick++
		return time.Unix(tick, 0)
	}
	return f, sqlDB
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("got error %v, want %v", err, target)
	}
	var pe *fs.PathError
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not an *fs.PathError", err)
	}
}

func countNodes(t *testing.T, sqlDB *sql.DB) int {
	t.Helper()
	var n int
	must(t, sqlDB.QueryRow("SELECT count(*) FROM fs_node").Scan(&n))
	return n
}

func TestNewPersistsAndIsIdempotent(t *testing.T) {
	ctx := t.Context()
	file := filepath.Join(t.TempDir(), "fs.db")

	sqlDB := openDB(t, file)
	f, err := New(ctx, sqlDB)
	must(t, err)
	must(t, f.Mkdir(ctx, "/dir"))
	must(t, f.WriteFile(ctx, "/dir/a", []byte("hello")))
	_, err = New(ctx, sqlDB)
	must(t, err)
	must(t, sqlDB.Close())

	f, err = New(ctx, openDB(t, file))
	must(t, err)
	data, err := f.ReadFile(ctx, "/dir/a")
	must(t, err)
	if string(data) != "hello" {
		t.Fatalf("got %q after reopen", data)
	}
}

func TestNewRejectsUnknownVersion(t *testing.T) {
	sqlDB := openDB(t, filepath.Join(t.TempDir(), "fs.db"))
	_, err := sqlDB.Exec("PRAGMA user_version = 99")
	must(t, err)
	if _, err := New(t.Context(), sqlDB); err == nil {
		t.Fatal("expected error for unknown schema version")
	}
}

func TestWriteAndReadFile(t *testing.T) {
	ctx := t.Context()
	f, _ := newFS(t)

	must(t, f.WriteFile(ctx, "/a.txt", []byte("one")))
	must(t, f.WriteFile(ctx, "/a.txt", []byte("two!")))
	data, err := f.ReadFile(ctx, "/a.txt")
	must(t, err)
	if string(data) != "two!" {
		t.Fatalf("got %q", data)
	}

	for _, empty := range [][]byte{nil, {}} {
		must(t, f.WriteFile(ctx, "/empty", empty))
		data, err = f.ReadFile(ctx, "/empty")
		must(t, err)
		if len(data) != 0 {
			t.Fatalf("got %q for empty file", data)
		}
		fi, err := f.Stat(ctx, "/empty")
		must(t, err)
		if fi.Kind != KindFile || fi.Size() != 0 {
			t.Fatalf("empty file stat: %v", fi)
		}
	}
}

func TestWriteFileErrors(t *testing.T) {
	ctx := t.Context()
	f, _ := newFS(t)
	must(t, f.Mkdir(ctx, "/dir"))
	must(t, f.WriteFile(ctx, "/file", []byte("x")))

	tests := []struct {
		path string
		want error
	}{
		{"/missing/a", fs.ErrNotExist},
		{"/dir", ErrIsDir},
		{"/", ErrIsDir},
		{"/file/a", ErrNotDir},
		{"relative", fs.ErrInvalid},
		{"", fs.ErrInvalid},
		{"/nul\x00", fs.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			wantErr(t, f.WriteFile(ctx, tt.path, []byte("y")), tt.want)
		})
	}
}

func TestReadFileErrors(t *testing.T) {
	ctx := t.Context()
	f, _ := newFS(t)
	must(t, f.Mkdir(ctx, "/dir"))

	_, err := f.ReadFile(ctx, "/nope")
	wantErr(t, err, fs.ErrNotExist)
	_, err = f.ReadFile(ctx, "/dir")
	wantErr(t, err, ErrIsDir)
	_, err = f.ReadFile(ctx, "/")
	wantErr(t, err, ErrIsDir)
}

func TestMkdir(t *testing.T) {
	ctx := t.Context()
	f, _ := newFS(t)

	must(t, f.Mkdir(ctx, "/a/b/c"))
	must(t, f.Mkdir(ctx, "/a/b/c"))
	must(t, f.Mkdir(ctx, "/a/b"))
	must(t, f.Mkdir(ctx, "/"))
	for _, p := range []string{"/a", "/a/b", "/a/b/c"} {
		fi, err := f.Stat(ctx, p)
		must(t, err)
		if !fi.IsDir() {
			t.Fatalf("%s is not a dir", p)
		}
	}

	must(t, f.WriteFile(ctx, "/a/file", nil))
	wantErr(t, f.Mkdir(ctx, "/a/file"), ErrNotDir)
	wantErr(t, f.Mkdir(ctx, "/a/file/sub"), ErrNotDir)
	wantErr(t, f.Mkdir(ctx, "a"), fs.ErrInvalid)
}

func TestRemoveFile(t *testing.T) {
	ctx := t.Context()
	f, _ := newFS(t)
	must(t, f.Mkdir(ctx, "/dir"))
	must(t, f.WriteFile(ctx, "/dir/a", []byte("x")))

	must(t, f.RemoveFile(ctx, "/dir/a"))
	_, err := f.Stat(ctx, "/dir/a")
	wantErr(t, err, fs.ErrNotExist)
	wantErr(t, f.RemoveFile(ctx, "/dir/a"), fs.ErrNotExist)
	wantErr(t, f.RemoveFile(ctx, "/dir"), ErrIsDir)
	wantErr(t, f.RemoveFile(ctx, "/"), ErrIsDir)
}

func TestRemoveDir(t *testing.T) {
	ctx := t.Context()
	f, sqlDB := newFS(t)
	must(t, f.Mkdir(ctx, "/keep"))
	must(t, f.Mkdir(ctx, "/tree/a/b"))
	must(t, f.Mkdir(ctx, "/tree/c"))
	must(t, f.WriteFile(ctx, "/tree/a/b/f1", []byte("1")))
	must(t, f.WriteFile(ctx, "/tree/c/f2", []byte("2")))
	must(t, f.WriteFile(ctx, "/file", nil))

	wantErr(t, f.RemoveDir(ctx, "/tree", false), ErrNotEmpty)
	wantErr(t, f.RemoveDir(ctx, "/", true), fs.ErrInvalid)
	wantErr(t, f.RemoveDir(ctx, "/file", false), ErrNotDir)
	wantErr(t, f.RemoveDir(ctx, "/missing", true), fs.ErrNotExist)

	before := countNodes(t, sqlDB)
	must(t, f.RemoveDir(ctx, "/tree", true))
	if got, want := countNodes(t, sqlDB), before-6; got != want {
		t.Fatalf("nodes after recursive delete: got %d, want %d", got, want)
	}
	_, err := f.Stat(ctx, "/tree")
	wantErr(t, err, fs.ErrNotExist)

	must(t, f.RemoveDir(ctx, "/keep", false))
	_, err = f.Stat(ctx, "/keep")
	wantErr(t, err, fs.ErrNotExist)
}

func TestMove(t *testing.T) {
	ctx := t.Context()
	f, _ := newFS(t)
	must(t, f.Mkdir(ctx, "/src/inner"))
	must(t, f.Mkdir(ctx, "/dst"))
	must(t, f.WriteFile(ctx, "/src/inner/f", []byte("payload")))
	must(t, f.WriteFile(ctx, "/other", nil))

	// Rename a file in place.
	must(t, f.WriteFile(ctx, "/a", []byte("a")))
	must(t, f.Move(ctx, "/a", "/b"))
	_, err := f.Stat(ctx, "/a")
	wantErr(t, err, fs.ErrNotExist)

	// Move a directory with its subtree.
	must(t, f.Move(ctx, "/src", "/dst/moved"))
	data, err := f.ReadFile(ctx, "/dst/moved/inner/f")
	must(t, err)
	if string(data) != "payload" {
		t.Fatalf("got %q", data)
	}

	// Moving onto itself is a no-op.
	must(t, f.Move(ctx, "/dst/moved", "/dst/./moved"))

	wantErr(t, f.Move(ctx, "/dst", "/dst/moved/inner/x"), ErrCycle)
	wantErr(t, f.Move(ctx, "/dst", "/dst/x"), ErrCycle)
	wantErr(t, f.Move(ctx, "/dst/moved", "/other"), fs.ErrExist)
	wantErr(t, f.Move(ctx, "/b", "/dst"), fs.ErrExist)
	wantErr(t, f.Move(ctx, "/missing", "/x"), fs.ErrNotExist)
	wantErr(t, f.Move(ctx, "/b", "/missing/x"), fs.ErrNotExist)
	wantErr(t, f.Move(ctx, "/b", "/other/x"), ErrNotDir)
	wantErr(t, f.Move(ctx, "/", "/x"), fs.ErrInvalid)
	wantErr(t, f.Move(ctx, "/b", "/"), fs.ErrInvalid)
}

func TestStatAndTimestamps(t *testing.T) {
	ctx := t.Context()
	f, _ := newFS(t)

	must(t, f.Mkdir(ctx, "/d"))
	d0, err := f.Stat(ctx, "/d")
	must(t, err)

	must(t, f.WriteFile(ctx, "/d/f", []byte("12345")))
	fi, err := f.Stat(ctx, "/d/f")
	must(t, err)
	if fi.Name() != "f" || fi.Kind != KindFile || fi.Size() != 5 || fi.IsDir() || fi.Mode() != 0 {
		t.Fatalf("unexpected stat: %v", fi)
	}
	if !fi.CreatedAt.Equal(fi.ModifiedAt) {
		t.Fatalf("new file: created %v != modified %v", fi.CreatedAt, fi.ModifiedAt)
	}

	d1, err := f.Stat(ctx, "/d")
	must(t, err)
	if !d1.ModifiedAt.After(d0.ModifiedAt) || !d1.CreatedAt.Equal(d0.CreatedAt) {
		t.Fatalf("parent mtime not bumped on create: %v -> %v", d0, d1)
	}
	if d1.Size() != 0 || d1.Mode() != fs.ModeDir {
		t.Fatalf("unexpected dir stat: %v", d1)
	}

	must(t, f.WriteFile(ctx, "/d/f", []byte("x")))
	fi2, err := f.Stat(ctx, "/d/f")
	must(t, err)
	if !fi2.ModifiedAt.After(fi.ModifiedAt) || !fi2.CreatedAt.Equal(fi.CreatedAt) || fi2.Size() != 1 {
		t.Fatalf("overwrite stat: %v -> %v", fi, fi2)
	}

	root, err := f.Stat(ctx, "/")
	must(t, err)
	if root.Name() != "/" || !root.IsDir() {
		t.Fatalf("root stat: %v", root)
	}
}

func TestPathNormalisation(t *testing.T) {
	ctx := t.Context()
	f, _ := newFS(t)
	must(t, f.Mkdir(ctx, "//a///b/"))
	must(t, f.WriteFile(ctx, "/a/./b/../b/f", []byte("x")))
	must(t, f.WriteFile(ctx, "/../../g", []byte("y")))
	for _, p := range []string{"/a/b/f", "/g"} {
		if _, err := f.Stat(ctx, p); err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
	}
}

func TestReadDir(t *testing.T) {
	ctx := t.Context()
	f, _ := newFS(t)
	must(t, f.Mkdir(ctx, "/d/sub"))
	must(t, f.WriteFile(ctx, "/d/b", []byte("bb")))
	must(t, f.WriteFile(ctx, "/d/a", []byte("a")))

	entries, err := f.ReadDir(ctx, "/d")
	must(t, err)
	var got []string
	for _, e := range entries {
		got = append(got, fmt.Sprintf("%s:%s:%d", e.Name(), e.Kind, e.Size()))
	}
	if want := "[a:file:1 b:file:2 sub:dir:0]"; fmt.Sprint(got) != want {
		t.Fatalf("got %v, want %s", got, want)
	}

	_, err = f.ReadDir(ctx, "/d/a")
	wantErr(t, err, ErrNotDir)
}

func TestIOFS(t *testing.T) {
	ctx := t.Context()
	f, _ := newFS(t)
	must(t, f.Mkdir(ctx, "/docs/nested"))
	must(t, f.Mkdir(ctx, "/emptydir"))
	must(t, f.WriteFile(ctx, "/root.txt", []byte("at the root")))
	must(t, f.WriteFile(ctx, "/docs/a.md", bytes.Repeat([]byte("abc"), 1000)))
	must(t, f.WriteFile(ctx, "/docs/nested/b.bin", []byte{0, 1, 2, 3}))
	must(t, f.WriteFile(ctx, "/docs/empty", nil))

	fsys := f.IOFS(ctx)
	must(t, fstest.TestFS(fsys, "root.txt", "docs/a.md", "docs/nested/b.bin", "docs/empty", "emptydir"))

	_, err := fs.ReadFile(fsys, "/root.txt")
	wantErr(t, err, fs.ErrInvalid)
	_, err = fs.Stat(fsys, "nope")
	wantErr(t, err, fs.ErrNotExist)
}

func TestCancelledContext(t *testing.T) {
	f, sqlDB := newFS(t)
	must(t, f.Mkdir(t.Context(), "/d"))
	must(t, f.WriteFile(t.Context(), "/d/f", []byte("x")))
	before := countNodes(t, sqlDB)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	ops := map[string]func() error{
		"New":        func() error { _, err := New(ctx, sqlDB); return err },
		"ReadFile":   func() error { _, err := f.ReadFile(ctx, "/d/f"); return err },
		"WriteFile":  func() error { return f.WriteFile(ctx, "/d/g", nil) },
		"Mkdir":      func() error { return f.Mkdir(ctx, "/e") },
		"RemoveFile": func() error { return f.RemoveFile(ctx, "/d/f") },
		"RemoveDir":  func() error { return f.RemoveDir(ctx, "/d", true) },
		"Move":       func() error { return f.Move(ctx, "/d", "/e") },
		"Stat":       func() error { _, err := f.Stat(ctx, "/d"); return err },
		"ReadDir":    func() error { _, err := f.ReadDir(ctx, "/d"); return err },
		"IOFS.Open":  func() error { _, err := f.IOFS(ctx).Open("d"); return err },
	}
	for name, op := range ops {
		if err := op(); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: got %v, want context.Canceled", name, err)
		}
	}
	if got := countNodes(t, sqlDB); got != before {
		t.Fatalf("cancelled ops changed the database: %d -> %d nodes", before, got)
	}
}

func TestConcurrentWriters(t *testing.T) {
	ctx := t.Context()
	f, _ := newFS(t)
	must(t, f.Mkdir(ctx, "/c"))

	const workers, perWorker = 8, 20
	var wg sync.WaitGroup
	errs := make(chan error, workers*perWorker)
	for w := range workers {
		wg.Go(func() {
			for i := range perWorker {
				p := fmt.Sprintf("/c/w%d-%d", w, i)
				if err := f.WriteFile(ctx, p, []byte(p)); err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	entries, err := f.ReadDir(ctx, "/c")
	must(t, err)
	if len(entries) != workers*perWorker {
		t.Fatalf("got %d entries, want %d", len(entries), workers*perWorker)
	}
}
