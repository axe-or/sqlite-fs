package sqlitefs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"testing"
)

func withPageSize(t *testing.T, n int) {
	t.Helper()
	old := entriesPageSize
	entriesPageSize = n
	t.Cleanup(func() { entriesPageSize = old })
}

func collect(t *testing.T, f *FS, ctx context.Context, p string) []string {
	t.Helper()
	var names []string
	for fi, err := range f.Entries(ctx, p) {
		must(t, err)
		names = append(names, fi.Name())
	}
	return names
}

func TestEntriesPaging(t *testing.T) {
	ctx := t.Context()
	f, _ := newFS(t)
	must(t, f.Mkdir(ctx, "/d/sub"))
	must(t, f.Mkdir(ctx, "/empty"))
	var want []string
	for i := range 7 {
		name := fmt.Sprintf("f%d", i)
		must(t, f.WriteFile(ctx, "/d/"+name, []byte(name)))
		want = append(want, name)
	}
	want = append(want, "sub")

	// Exercise a partial last page, an exact multiple, and a single page.
	for _, size := range []int{1, 3, 4, 8, 256} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			withPageSize(t, size)
			if got := collect(t, f, ctx, "/d"); !slices.Equal(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			if got := collect(t, f, ctx, "/empty"); len(got) != 0 {
				t.Fatalf("empty dir yielded %v", got)
			}
		})
	}

	withPageSize(t, 3)
	var root []string
	for fi, err := range f.Entries(ctx, "/") {
		must(t, err)
		root = append(root, fmt.Sprintf("%s:%s", fi.Name(), fi.Kind))
	}
	if want := "[d:dir empty:dir]"; fmt.Sprint(root) != want {
		t.Fatalf("root: got %v, want %s", root, want)
	}
}

func TestEntriesEarlyBreak(t *testing.T) {
	ctx := t.Context()
	f, _ := newFS(t)
	withPageSize(t, 2)
	must(t, f.Mkdir(ctx, "/d"))
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		must(t, f.WriteFile(ctx, "/d/"+n, nil))
	}

	var got []string
	for fi, err := range f.Entries(ctx, "/d") {
		must(t, err)
		got = append(got, fi.Name())
		if len(got) == 3 {
			break
		}
	}
	if fmt.Sprint(got) != "[a b c]" {
		t.Fatalf("got %v", got)
	}
}

func TestEntriesErrors(t *testing.T) {
	ctx := t.Context()
	f, _ := newFS(t)
	must(t, f.WriteFile(ctx, "/file", nil))

	tests := []struct {
		path string
		want error
	}{
		{"/missing", fs.ErrNotExist},
		{"/file", ErrNotDir},
		{"relative", fs.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			calls := 0
			for fi, err := range f.Entries(ctx, tt.path) {
				calls++
				if fi != nil {
					t.Fatalf("got entry %v alongside error", fi)
				}
				wantErr(t, err, tt.want)
			}
			if calls != 1 {
				t.Fatalf("got %d yields, want exactly 1", calls)
			}
		})
	}
}

// Writing inside the loop must not deadlock, even when the pool has a single
// connection: no transaction may be held open while the body runs.
func TestEntriesMutationDuringIteration(t *testing.T) {
	ctx := t.Context()
	f, sqlDB := newFS(t)
	sqlDB.SetMaxOpenConns(1)
	withPageSize(t, 2)
	must(t, f.Mkdir(ctx, "/d"))
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		must(t, f.WriteFile(ctx, "/d/"+n, nil))
	}

	var got []string
	for fi, err := range f.Entries(ctx, "/d") {
		must(t, err)
		got = append(got, fi.Name())
		if fi.Name() == "a" {
			must(t, f.RemoveFile(ctx, "/d/d"))     // not yet fetched: skipped
			must(t, f.WriteFile(ctx, "/d/z", nil)) // sorts last: seen
			must(t, f.Move(ctx, "/d", "/moved"))   // iteration follows the dir
		}
	}
	if want := "[a b c e z]"; fmt.Sprint(got) != want {
		t.Fatalf("got %v, want %s", got, want)
	}
}

func TestEntriesDirRemovedMidIteration(t *testing.T) {
	ctx := t.Context()
	f, _ := newFS(t)
	withPageSize(t, 2)
	must(t, f.Mkdir(ctx, "/d"))
	for _, n := range []string{"a", "b", "c", "d"} {
		must(t, f.WriteFile(ctx, "/d/"+n, nil))
	}

	var got []string
	for fi, err := range f.Entries(ctx, "/d") {
		must(t, err)
		got = append(got, fi.Name())
		if len(got) == 1 {
			must(t, f.RemoveDir(ctx, "/d", true))
		}
	}
	// The first page was already fetched; the next one finds nothing.
	if want := "[a b]"; fmt.Sprint(got) != want {
		t.Fatalf("got %v, want %s", got, want)
	}
}

func TestEntriesCancelledMidIteration(t *testing.T) {
	f, _ := newFS(t)
	withPageSize(t, 2)
	must(t, f.Mkdir(t.Context(), "/d"))
	for _, n := range []string{"a", "b", "c", "d"} {
		must(t, f.WriteFile(t.Context(), "/d/"+n, nil))
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var names []string
	var last error
	for fi, err := range f.Entries(ctx, "/d") {
		if err != nil {
			last = err
			continue
		}
		names = append(names, fi.Name())
		cancel()
	}
	if !errors.Is(last, context.Canceled) {
		t.Fatalf("got error %v, want context.Canceled", last)
	}
	if fmt.Sprint(names) != "[a b]" {
		t.Fatalf("got %v before cancellation", names)
	}
}
