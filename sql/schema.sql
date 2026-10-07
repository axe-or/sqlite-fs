-- New executes this file statement by statement, splitting on semicolons,
-- so semicolons must only appear as statement terminators.

-- A single adjacency-list table holds both files and directories.
-- Node 1 is the root directory and is the only node without a parent.
CREATE TABLE IF NOT EXISTS fs_node (
	id          INTEGER PRIMARY KEY,
	parent_id   INTEGER REFERENCES fs_node(id),
	name        TEXT    NOT NULL,
	kind        INTEGER NOT NULL, -- 0 = dir, 1 = file
	created_at  INTEGER NOT NULL, -- unix nanoseconds
	modified_at INTEGER NOT NULL, -- unix nanoseconds
	data        BLOB,             -- NULL for dirs, kept last so metadata reads skip overflow pages
	CHECK ((id = 1) = (parent_id IS NULL)),
	CHECK ((kind = 0) = (data IS NULL)),
	UNIQUE (parent_id, name)
);

CREATE INDEX IF NOT EXISTS fs_node_parent ON fs_node(parent_id);
