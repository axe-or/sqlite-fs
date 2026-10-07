-- name: InsertRoot :exec
INSERT OR IGNORE INTO nodes (id, parent_id, name, kind, created_at, modified_at)
VALUES (1, NULL, '', 0, ?, ?);

-- name: GetChild :one
SELECT id, kind FROM nodes WHERE parent_id = ? AND name = ?;

-- Drivers disagree on how a nil []byte binds (NULL vs empty blob), so
-- directories use a literal NULL and files must always pass a non-nil slice.

-- name: InsertDir :execlastid
INSERT INTO nodes (parent_id, name, kind, created_at, modified_at, data)
VALUES (?, ?, 0, ?, ?, NULL);

-- name: InsertFile :execlastid
INSERT INTO nodes (parent_id, name, kind, created_at, modified_at, data)
VALUES (?, ?, 1, ?, ?, ?);

-- name: GetData :one
SELECT data FROM nodes WHERE id = ?;

-- name: UpdateData :exec
UPDATE nodes SET data = ?, modified_at = ? WHERE id = ?;

-- name: Touch :exec
UPDATE nodes SET modified_at = ? WHERE id = ?;

-- name: DeleteNode :exec
DELETE FROM nodes WHERE id = ?;

-- name: HasChildren :one
SELECT EXISTS (SELECT 1 FROM nodes WHERE parent_id = ?);

-- name: StatNode :one
SELECT name, kind, CAST(COALESCE(length(data), 0) AS INTEGER) AS size, created_at, modified_at
FROM nodes WHERE id = ?;

-- name: ListChildren :many
SELECT name, kind, CAST(COALESCE(length(data), 0) AS INTEGER) AS size, created_at, modified_at
FROM nodes WHERE parent_id = ? ORDER BY name;

-- name: MoveNode :exec
UPDATE nodes SET parent_id = ?, name = ? WHERE id = ?;

-- name: IsAncestor :one
-- Reports whether ancestor_id is node_id itself or one of its ancestors.
WITH RECURSIVE anc(id) AS (
	SELECT CAST(sqlc.arg(node_id) AS INTEGER)
	UNION ALL
	SELECT n.parent_id FROM nodes n JOIN anc ON n.id = anc.id WHERE n.parent_id IS NOT NULL
)
SELECT COUNT(*) FROM anc WHERE id = sqlc.arg(ancestor_id);

-- DeleteSubtree lives in internal/db/queries_manual.go: sqlc cannot parse a
-- recursive CTE feeding a DELETE.
