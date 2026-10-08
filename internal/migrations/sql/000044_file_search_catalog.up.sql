CREATE TABLE file_search_sources (
    home_id TEXT NOT NULL REFERENCES homes(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    source_id TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    readable BOOLEAN NOT NULL DEFAULT TRUE,
    allowed_prefixes JSONB NOT NULL DEFAULT '[]'::jsonb,
    blocked_prefixes JSONB NOT NULL DEFAULT '[]'::jsonb,
    discovered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (home_id, agent_id, source_id)
);

CREATE TABLE file_search_directories (
    home_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    source_id TEXT NOT NULL,
    path TEXT NOT NULL,
    depth INTEGER NOT NULL DEFAULT 0 CHECK (depth >= 0),
    scanned_at TIMESTAMPTZ,
    last_error_at TIMESTAMPTZ,
    next_scan_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (home_id, agent_id, source_id, path),
    FOREIGN KEY (home_id, agent_id, source_id)
        REFERENCES file_search_sources(home_id, agent_id, source_id) ON DELETE CASCADE
);
CREATE INDEX file_search_directories_due ON file_search_directories
    (home_id, agent_id, next_scan_at, depth);
CREATE INDEX file_search_directories_new ON file_search_directories
    (home_id, agent_id, depth, path) WHERE scanned_at IS NULL;

CREATE TABLE file_search_items (
    home_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    source_id TEXT NOT NULL,
    path TEXT NOT NULL,
    parent_path TEXT NOT NULL,
    name TEXT NOT NULL,
    is_directory BOOLEAN NOT NULL,
    size_bytes BIGINT NOT NULL DEFAULT 0,
    modified_at TIMESTAMPTZ,
    indexed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (home_id, agent_id, source_id, path),
    FOREIGN KEY (home_id, agent_id, source_id)
        REFERENCES file_search_sources(home_id, agent_id, source_id) ON DELETE CASCADE
);
CREATE INDEX file_search_items_parent ON file_search_items
    (home_id, agent_id, source_id, parent_path);
CREATE INDEX file_search_items_name_trgm ON file_search_items
    USING GIN (lower(name) gin_trgm_ops);
CREATE INDEX file_search_items_path_trgm ON file_search_items
    USING GIN (lower(path) gin_trgm_ops);
