package db

import (
	"context"
	"strings"
)

// Queries sqlc cannot parse. Keep these in the style of the generated code.

const deleteSubtree = `
WITH RECURSIVE sub(id) AS (
	SELECT CAST(? AS INTEGER)
	UNION ALL
	SELECT n.id FROM fs_node n JOIN sub ON n.parent_id = sub.id
)
DELETE FROM fs_node WHERE id IN (SELECT id FROM sub)
`

// DeleteSubtree deletes rootID and every node below it.
func (q *Queries) DeleteSubtree(ctx context.Context, rootID int64) error {
	_, err := q.db.ExecContext(ctx, deleteSubtree, rootID)
	return err
}

// searchPage ranks name matches as exact > prefix > substring (ASCII
// case-insensitive), then shorter names first, then by name and id. Results
// are paged by keyset on that whole tuple. {{MATCH}} is replaced with one of
// the match predicates below; it never contains user input.
//
// Parameters: ?1 query, ?2 match/like pattern, ?3 scope dir id,
// ?4..?7 the previous page's last (class, name_len, name, id), ?8 limit.
const searchPage = `
SELECT id, kind, name, size, created_at, modified_at, class, name_len FROM (
	SELECT n.id, n.kind, n.name,
		CAST(COALESCE(length(n.data), 0) AS INTEGER) AS size,
		n.created_at, n.modified_at,
		CASE
			WHEN n.name = ?1 COLLATE NOCASE THEN 0
			WHEN substr(n.name, 1, length(?1)) = ?1 COLLATE NOCASE THEN 1
			ELSE 2
		END AS class,
		length(n.name) AS name_len
	FROM fs_node n
	WHERE {{MATCH}} AND n.id != ?3 -- the scope itself (or root) is never a result
		AND (?3 = 1 OR n.id IN (
			WITH RECURSIVE sub(id) AS (
				SELECT CAST(?3 AS INTEGER)
				UNION ALL
				SELECT c.id FROM fs_node c JOIN sub ON c.parent_id = sub.id
			)
			SELECT id FROM sub
		))
)
WHERE (class, name_len, name, id) > (?4, ?5, ?6, ?7)
ORDER BY class, name_len, name, id
LIMIT ?8
`

var (
	// Queries of 3+ characters use the trigram index.
	searchPageFTS = strings.Replace(searchPage, "{{MATCH}}",
		"n.id IN (SELECT rowid FROM fs_node_name WHERE fs_node_name MATCH ?2)", 1)
	// Shorter queries have no trigram to look up and scan instead.
	searchPageScan = strings.Replace(searchPage, "{{MATCH}}",
		`n.name LIKE ?2 ESCAPE '\'`, 1)
)

type SearchPageParams struct {
	Query string
	// Pattern is an FTS5 phrase when UseIndex is set, else a LIKE pattern.
	Pattern  string
	UseIndex bool
	ScopeID  int64
	// The previous page's last row; Class -1 starts from the beginning.
	AfterClass   int64
	AfterNameLen int64
	AfterName    string
	AfterID      int64
	Limit        int64
}

type SearchPageRow struct {
	ID         int64
	Kind       int64
	Name       string
	Size       int64
	CreatedAt  int64
	ModifiedAt int64
	Class      int64
	NameLen    int64
}

func (q *Queries) SearchPage(ctx context.Context, arg SearchPageParams) ([]SearchPageRow, error) {
	query := searchPageScan
	if arg.UseIndex {
		query = searchPageFTS
	}
	rows, err := q.db.QueryContext(ctx, query,
		arg.Query, arg.Pattern, arg.ScopeID,
		arg.AfterClass, arg.AfterNameLen, arg.AfterName, arg.AfterID,
		arg.Limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SearchPageRow
	for rows.Next() {
		var i SearchPageRow
		if err := rows.Scan(
			&i.ID, &i.Kind, &i.Name, &i.Size, &i.CreatedAt, &i.ModifiedAt, &i.Class, &i.NameLen,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const nodePath = `
WITH RECURSIVE up(cur, path) AS (
	SELECT parent_id, name FROM fs_node WHERE id = ?
	UNION ALL
	SELECT p.parent_id, p.name || '/' || up.path FROM up JOIN fs_node p ON p.id = up.cur WHERE up.cur != 1
)
SELECT '/' || path FROM up WHERE cur = 1
`

// NodePath returns the absolute path of a non-root node.
func (q *Queries) NodePath(ctx context.Context, id int64) (string, error) {
	var path string
	err := q.db.QueryRowContext(ctx, nodePath, id).Scan(&path)
	return path, err
}
