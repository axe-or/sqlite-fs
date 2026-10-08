-- New executes this whole file in a single Exec call.

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

-- Trigram full-text index over node names for substring search. It is an
-- external-content table reading names from fs_node, kept in sync by the
-- triggers below, so every write path (including recursive deletes) is covered.
CREATE VIRTUAL TABLE IF NOT EXISTS fs_node_name USING fts5(
	name,
	content = 'fs_node',
	content_rowid = 'id',
	tokenize = 'trigram'
);

CREATE TRIGGER IF NOT EXISTS fs_node_name_insert AFTER INSERT ON fs_node BEGIN
	INSERT INTO fs_node_name (rowid, name) VALUES (new.id, new.name);
END;

CREATE TRIGGER IF NOT EXISTS fs_node_name_delete AFTER DELETE ON fs_node BEGIN
	INSERT INTO fs_node_name (fs_node_name, rowid, name) VALUES ('delete', old.id, old.name);
END;

CREATE TRIGGER IF NOT EXISTS fs_node_name_rename AFTER UPDATE OF name ON fs_node BEGIN
	INSERT INTO fs_node_name (fs_node_name, rowid, name) VALUES ('delete', old.id, old.name);
	INSERT INTO fs_node_name (rowid, name) VALUES (new.id, new.name);
END;
