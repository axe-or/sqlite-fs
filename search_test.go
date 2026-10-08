package sqlitefs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"testing"
)

func withSearchPageSize(t *testing.T, n int) {
	t.Helper()
	old := searchPageSize
	searchPageSize = n
	t.Cleanup(func() { searchPageSize = old })
}

func searchPaths(t *testing.T, f *FS, ctx context.Context, query, under string) []string {
	t.Helper()
	paths := []string{}
	for res, err := range f.Search(ctx, query, under) {
		must(t, err)
		paths = append(paths, res.Path)
	}
	return paths
}

// checkIndex runs FTS5's integrity check, which compares the index against
// the fs_node content table.
func checkIndex(t *testing.T, f *FS) {
	t.Helper()
	_, err := f.db.Exec("INSERT INTO fs_node_name (fs_node_name, rank) VALUES ('integrity-check', 1)")
	if err != nil {
		t.Fatalf("FTS index out of sync: %v", err)
	}
}

func searchTree(t *testing.T) *FS {
	t.Helper()
	ctx := t.Context()
	f, _ := newFS(t)
	must(t, f.Mkdir(ctx, "/docs/archive"))
	must(t, f.Mkdir(ctx, "/reports"))
	for _, p := range []string{
		"/report",
		"/docs/report",
		"/docs/Report.md",
		"/docs/archive/report_2024.pdf",
		"/docs/archive/quarterly_report.pdf",
		"/docs/notes.txt",
		"/a_b",
		"/axb",
		"/100%.txt",
		`/say "hi".txt`,
		"/Ärger.txt",
	} {
		must(t, f.WriteFile(ctx, p, []byte(p)))
	}
	return f
}

func TestSearchRanking(t *testing.T) {
	f := searchTree(t)
	want := []string{
		"/report", "/docs/report", // exact; equal names fall back to creation order (id)
		"/reports",                           // prefix, shortest first
		"/docs/Report.md",                    // prefix, case-insensitive
		"/docs/archive/report_2024.pdf",      // prefix
		"/docs/archive/quarterly_report.pdf", // substring
	}
	for _, size := range []int{1, 2, 4, 6, 64} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			withSearchPageSize(t, size)
			if got := searchPaths(t, f, t.Context(), "report", "/"); !slices.Equal(got, want) {
				t.Fatalf("got %q\nwant %q", got, want)
			}
		})
	}
}

func TestSearchResultInfo(t *testing.T) {
	f := searchTree(t)
	for res, err := range f.Search(t.Context(), "notes", "/") {
		must(t, err)
		if res.Path != "/docs/notes.txt" || res.Info.Name() != "notes.txt" ||
			res.Info.Kind != KindFile || res.Info.Size() != int64(len("/docs/notes.txt")) {
			t.Fatalf("unexpected result %q %v", res.Path, res.Info)
		}
	}
}

func TestSearchQueries(t *testing.T) {
	f := searchTree(t)
	tests := []struct {
		query string
		want  []string
	}{
		{"md", []string{"/docs/Report.md"}},    // short: LIKE scan
		{"_b", []string{"/a_b"}},               // LIKE wildcards are literal
		{"%", []string{"/100%.txt"}},           // ...in both
		{"0%.", []string{"/100%.txt"}},         // ...and through the index
		{`"hi"`, []string{`/say "hi".txt`}},    // FTS quoting
		{"ärg", []string{"/Ärger.txt"}},        // Unicode case folding via trigram
		{"zzz", []string{}},                    // no match
		{"ARCHIVE", []string{"/docs/archive"}}, // directories match too
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			if got := searchPaths(t, f, t.Context(), tt.query, "/"); !slices.Equal(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSearchScope(t *testing.T) {
	ctx := t.Context()
	f := searchTree(t)

	got := searchPaths(t, f, ctx, "report", "/docs/archive")
	want := []string{"/docs/archive/report_2024.pdf", "/docs/archive/quarterly_report.pdf"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	// The scope directory itself is not a result.
	if got := searchPaths(t, f, ctx, "archive", "/docs/archive"); len(got) != 0 {
		t.Fatalf("got %q", got)
	}

	tests := []struct {
		query, under string
		want         error
	}{
		{"report", "/docs/notes.txt", ErrNotDir},
		{"report", "/missing", fs.ErrNotExist},
		{"report", "docs", fs.ErrInvalid},
		{"", "/", fs.ErrInvalid},
		{"a\x00b", "/", fs.ErrInvalid},
	}
	for _, tt := range tests {
		calls := 0
		for res, err := range f.Search(ctx, tt.query, tt.under) {
			calls++
			if res.Info != nil {
				t.Fatalf("%q under %q: got result alongside error", tt.query, tt.under)
			}
			wantErr(t, err, tt.want)
		}
		if calls != 1 {
			t.Fatalf("%q under %q: got %d yields, want 1", tt.query, tt.under, calls)
		}
	}
}

func TestSearchIndexTracksChanges(t *testing.T) {
	ctx := t.Context()
	f := searchTree(t)
	checkIndex(t, f)

	must(t, f.Move(ctx, "/docs/archive", "/old"))
	must(t, f.Move(ctx, "/report", "/summary"))
	must(t, f.RemoveFile(ctx, "/docs/report"))
	checkIndex(t, f)

	got := searchPaths(t, f, ctx, "report", "/")
	want := []string{"/reports", "/docs/Report.md", "/old/report_2024.pdf", "/old/quarterly_report.pdf"}
	if !slices.Equal(got, want) {
		t.Fatalf("after moves: got %q, want %q", got, want)
	}

	must(t, f.RemoveDir(ctx, "/old", true))
	must(t, f.WriteFile(ctx, "/docs/report", nil))
	checkIndex(t, f)
	if got, want := searchPaths(t, f, ctx, "report", "/"), []string{"/docs/report", "/reports", "/docs/Report.md"}; !slices.Equal(got, want) {
		t.Fatalf("after delete: got %q, want %q", got, want)
	}
}

func TestSearchMutationDuringIteration(t *testing.T) {
	ctx := t.Context()
	f := searchTree(t)
	f.db.SetMaxOpenConns(1)
	withSearchPageSize(t, 2)

	var got []string
	for res, err := range f.Search(ctx, "report", "/") {
		must(t, err)
		got = append(got, res.Path)
		if len(got) == 1 {
			must(t, f.RemoveFile(ctx, "/docs/archive/report_2024.pdf")) // not yet fetched: skipped
		}
	}
	want := []string{"/report", "/docs/report", "/reports", "/docs/Report.md", "/docs/archive/quarterly_report.pdf"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSearchCancelled(t *testing.T) {
	f := searchTree(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, err := range f.Search(ctx, "report", "/") {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	}
}

func TestNewRequiresFTS5(t *testing.T) {
	ctx := t.Context()
	file := filepath.Join(t.TempDir(), "fs.db")

	_, err := New(ctx, openDBWith(t, file))
	if !errors.Is(err, ErrNoFTS5) {
		t.Fatalf("fresh database: got %v, want ErrNoFTS5", err)
	}

	// An existing database must also be refused, or its triggers would fail
	// on the first write.
	_, err = New(ctx, openDB(t, file))
	must(t, err)
	_, err = New(ctx, openDBWith(t, file))
	if !errors.Is(err, ErrNoFTS5) {
		t.Fatalf("existing database: got %v, want ErrNoFTS5", err)
	}
}
