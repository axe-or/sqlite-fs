package sqlitefs

import (
	"bytes"
	"database/sql"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"testing"

	"github.com/axe-or/sqlite-fs/internal/db"
)

// Benchmarks run against an on-disk database in the default rollback-journal
// mode, configured like the tests (busy timeout, immediate transactions).
//
//	go test -run '^$' -bench . -benchmem

var benchSizes = []struct {
	name string
	n    int
}{
	{"1KiB", 1 << 10},
	{"64KiB", 64 << 10},
	{"1MiB", 1 << 20},
}

// bulkWrite creates files (and any missing parent directories) in a single
// transaction, which is far faster than one WriteFile commit per file.
func bulkWrite(tb testing.TB, f *FS, paths []string, data []byte) {
	tb.Helper()
	ctx := tb.Context()
	now := f.now().UnixNano()
	must(tb, f.tx(ctx, func(_ *sql.Tx, q *db.Queries) error {
		dirs := map[string]int64{"/": rootID}
		var dirID func(p string) (int64, error)
		dirID = func(p string) (int64, error) {
			if id, ok := dirs[p]; ok {
				return id, nil
			}
			parent, err := dirID(path.Dir(p))
			if err != nil {
				return 0, err
			}
			id, _, err := lookup(ctx, q, parent, path.Base(p))
			if err != nil {
				id, err = q.InsertDir(ctx, db.InsertDirParams{
					ParentID: parentRef(parent), Name: path.Base(p), CreatedAt: now, ModifiedAt: now,
				})
				if err != nil {
					return 0, err
				}
			}
			dirs[p] = id
			return id, nil
		}
		for _, p := range paths {
			parent, err := dirID(path.Dir(p))
			if err != nil {
				return err
			}
			_, err = q.InsertFile(ctx, db.InsertFileParams{
				ParentID: parentRef(parent), Name: path.Base(p), CreatedAt: now, ModifiedAt: now, Data: data,
			})
			if err != nil {
				return err
			}
		}
		return nil
	}))
}

// flatDir returns n file paths directly under dir.
func flatDir(dir string, n int) []string {
	paths := make([]string, n)
	for i := range paths {
		paths[i] = fmt.Sprintf("%s/file_%05d.txt", dir, i)
	}
	return paths
}

// searchCorpus is 10,000 files spread round-robin over 100 directories.
// Every 100th file is a report (all landing in /d00), so queries can hit 1,
// 100, or 9,900 names.
func searchCorpus() []string {
	paths := make([]string, 10_000)
	for i := range paths {
		name := fmt.Sprintf("file_%05d.txt", i)
		if i%100 == 0 {
			name = fmt.Sprintf("report_%05d.pdf", i)
		}
		paths[i] = fmt.Sprintf("/d%02d/%s", i%100, name)
	}
	return paths
}

func BenchmarkWriteFile(b *testing.B) {
	for _, size := range benchSizes {
		data := bytes.Repeat([]byte{'x'}, size.n)

		b.Run("create/"+size.name, func(b *testing.B) {
			f, _ := newFS(b)
			ctx := b.Context()
			must(b, f.Mkdir(ctx, "/d"))
			b.SetBytes(int64(size.n))
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				i++
				must(b, f.WriteFile(ctx, fmt.Sprintf("/d/f%d", i), data))
			}
		})

		b.Run("overwrite/"+size.name, func(b *testing.B) {
			f, _ := newFS(b)
			ctx := b.Context()
			must(b, f.WriteFile(ctx, "/f", data))
			b.SetBytes(int64(size.n))
			b.ReportAllocs()
			for b.Loop() {
				must(b, f.WriteFile(ctx, "/f", data))
			}
		})
	}
}

func BenchmarkReadFile(b *testing.B) {
	for _, size := range benchSizes {
		b.Run(size.name, func(b *testing.B) {
			f, _ := newFS(b)
			ctx := b.Context()
			must(b, f.WriteFile(ctx, "/f", bytes.Repeat([]byte{'x'}, size.n)))
			b.SetBytes(int64(size.n))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := f.ReadFile(ctx, "/f"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkReadFileParallel(b *testing.B) {
	f, _ := newFS(b)
	ctx := b.Context()
	bulkWrite(b, f, flatDir("/d", 100), bytes.Repeat([]byte{'x'}, 1<<10))
	b.SetBytes(1 << 10)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			i++
			if _, err := f.ReadFile(ctx, fmt.Sprintf("/d/file_%05d.txt", i%100)); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

// BenchmarkStat measures path resolution, which costs one lookup per level.
func BenchmarkStat(b *testing.B) {
	for _, depth := range []int{1, 8, 32} {
		b.Run(fmt.Sprintf("depth=%d", depth), func(b *testing.B) {
			f, _ := newFS(b)
			ctx := b.Context()
			p := strings.Repeat("/dir", depth-1) + "/f"
			must(b, f.Mkdir(ctx, path.Dir(p)))
			must(b, f.WriteFile(ctx, p, []byte("x")))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := f.Stat(ctx, p); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkMkdir(b *testing.B) {
	for _, depth := range []int{1, 8} {
		b.Run(fmt.Sprintf("depth=%d", depth), func(b *testing.B) {
			f, _ := newFS(b)
			ctx := b.Context()
			suffix := strings.Repeat("/dir", depth-1)
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				i++
				must(b, f.Mkdir(ctx, fmt.Sprintf("/m%d%s", i, suffix)))
			}
		})
	}
}

func BenchmarkRemoveFile(b *testing.B) {
	f, _ := newFS(b)
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		must(b, f.WriteFile(ctx, "/f", []byte("x")))
		b.StartTimer()
		must(b, f.RemoveFile(ctx, "/f"))
	}
}

func BenchmarkRemoveDirRecursive(b *testing.B) {
	for _, n := range []int{10, 1000} {
		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			f, _ := newFS(b)
			ctx := b.Context()
			paths := flatDir("/tree/sub", n)
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				bulkWrite(b, f, paths, []byte("x"))
				b.StartTimer()
				must(b, f.RemoveDir(ctx, "/tree", true))
			}
		})
	}
}

// BenchmarkMove should not depend on subtree size: only the moved node's row
// changes. Depth matters, through path resolution and the cycle check.
func BenchmarkMove(b *testing.B) {
	for _, tc := range []struct {
		files, depth int
	}{{10, 1}, {10_000, 1}, {10, 16}} {
		b.Run(fmt.Sprintf("files=%d/depth=%d", tc.files, tc.depth), func(b *testing.B) {
			f, _ := newFS(b)
			ctx := b.Context()
			base := strings.Repeat("/dir", tc.depth)
			bulkWrite(b, f, flatDir(base+"/a", tc.files), []byte("x"))
			src, dst := base+"/a", base+"/b"
			b.ReportAllocs()
			for b.Loop() {
				must(b, f.Move(ctx, src, dst))
				src, dst = dst, src
			}
		})
	}
}

func BenchmarkListDir(b *testing.B) {
	for _, n := range []int{100, 10_000} {
		f, _ := newFS(b)
		bulkWrite(b, f, flatDir("/d", n), []byte("x"))

		b.Run(fmt.Sprintf("ReadDir/files=%d", n), func(b *testing.B) {
			ctx := b.Context()
			b.ReportAllocs()
			for b.Loop() {
				if _, err := f.ReadDir(ctx, "/d"); err != nil {
					b.Fatal(err)
				}
			}
		})

		b.Run(fmt.Sprintf("Entries/files=%d", n), func(b *testing.B) {
			ctx := b.Context()
			b.ReportAllocs()
			for b.Loop() {
				for _, err := range f.Entries(ctx, "/d") {
					if err != nil {
						b.Fatal(err)
					}
				}
			}
		})

		b.Run(fmt.Sprintf("Entries-first10/files=%d", n), func(b *testing.B) {
			ctx := b.Context()
			b.ReportAllocs()
			for b.Loop() {
				seen := 0
				for _, err := range f.Entries(ctx, "/d") {
					if err != nil {
						b.Fatal(err)
					}
					if seen++; seen == 10 {
						break
					}
				}
			}
		})
	}
}

func BenchmarkSearch(b *testing.B) {
	f, _ := newFS(b)
	bulkWrite(b, f, searchCorpus(), []byte("x"))

	for _, tc := range []struct {
		name, query, under string
		first              int // stop after this many results; 0 = all
	}{
		{"rare", "report_00400", "/", 0},    // 1 hit, index
		{"medium", "report", "/", 0},        // 100 hits, index
		{"common-first10", "file", "/", 10}, // 9,900 hits, index
		{"common-all", "file", "/", 0},
		{"scoped", "file", "/d07", 0}, // 100 of the 9,900 hits, in one dir
		{"short-scan", "42", "/", 0},  // under 3 chars: full scan, 300 hits
	} {
		b.Run(tc.name, func(b *testing.B) {
			ctx := b.Context()
			b.ReportAllocs()
			results := 0
			for b.Loop() {
				results = 0
				for _, err := range f.Search(ctx, tc.query, tc.under) {
					if err != nil {
						b.Fatal(err)
					}
					if results++; results == tc.first {
						break
					}
				}
			}
			b.ReportMetric(float64(results), "results/op")
		})
	}
}

func BenchmarkWalkDir(b *testing.B) {
	f, _ := newFS(b)
	bulkWrite(b, f, searchCorpus(), []byte("x"))
	fsys := f.IOFS(b.Context())
	b.ReportAllocs()
	for b.Loop() {
		err := fs.WalkDir(fsys, ".", func(_ string, _ fs.DirEntry, err error) error { return err })
		if err != nil {
			b.Fatal(err)
		}
	}
}
