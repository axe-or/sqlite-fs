-- name: InsertRoot :exec
INSERT OR IGNORE INTO fs_node (id, parent_id, name, kind, created_at, modified_at)
VALUES (1, NULL, '', 0, ?, ?);

-- name: GetChild :one
SELECT id, kind FROM fs_node WHERE parent_id = ? AND name = ?;

-- Drivers disagree on how a nil []byte binds (NULL vs empty blob), so
-- directories use a literal NULL and files must always pass a non-nil slice.

-- name: InsertDir :execlastid
INSERT INTO fs_node (parent_id, name, kind, created_at, modified_at, data)
VALUES (?, ?, 0, ?, ?, NULL);

-- name: InsertFile :execlastid
INSERT INTO fs_node (parent_id, name, kind, created_at, modified_at, data)
VALUES (?, ?, 1, ?, ?, ?);

-- name: GetData :one
SELECT data FROM fs_node WHERE id = ?;

-- name: UpdateData :exec
UPDATE fs_node SET data = ?, modified_at = ? WHERE id = ?;

-- name: Touch :exec
UPDATE fs_node SET modified_at = ? WHERE id = ?;

-- name: DeleteNode :exec
DELETE FROM fs_node WHERE id = ?;

-- name: HasChildren :one
SELECT EXISTS (SELECT 1 FROM fs_node WHERE parent_id = ?);

-- name: StatNode :one
SELECT name, kind, CAST(COALESCE(length(data), 0) AS INTEGER) AS size, created_at, modified_at
FROM fs_node WHERE id = ?;

-- name: ListChildren :many
SELECT name, kind, CAST(COALESCE(length(data), 0) AS INTEGER) AS size, created_at, modified_at
FROM fs_node WHERE parent_id = ? ORDER BY name;

-- name: ListChildrenAfter :many
-- Keyset pagination for Entries: the page of children sorted after name.
SELECT name, kind, CAST(COALESCE(length(data), 0) AS INTEGER) AS size, created_at, modified_at
FROM fs_node WHERE parent_id = ? AND name > ? ORDER BY name LIMIT ?;

-- name: MoveNode :exec
UPDATE fs_node SET parent_id = ?, name = ? WHERE id = ?;

-- name: IsAncestor :one
-- Reports whether ancestor_id is node_id itself or one of its ancestors.
WITH RECURSIVE anc(id) AS (
	SELECT CAST(sqlc.arg(node_id) AS INTEGER)
	UNION ALL
	SELECT n.parent_id FROM fs_node n JOIN anc ON n.id = anc.id WHERE n.parent_id IS NOT NULL
)
SELECT COUNT(*) FROM anc WHERE id = sqlc.arg(ancestor_id);

-- DeleteSubtree lives in internal/db/queries_manual.go: sqlc cannot parse a
-- recursive CTE feeding a DELETE.
