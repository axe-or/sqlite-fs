package db

import "context"

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
