CREATE TABLE IF NOT EXISTS home_quick_links (
	id TEXT PRIMARY KEY,
	home_id TEXT NOT NULL,
	title TEXT NOT NULL,
	url TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	sort_order INTEGER NOT NULL DEFAULT 0,
	health_check_enabled BOOLEAN NOT NULL DEFAULT TRUE,
	status TEXT NOT NULL DEFAULT 'unchecked' CHECK (status IN ('unchecked', 'up', 'down', 'disabled')),
	status_code INTEGER NOT NULL DEFAULT 0,
	last_checked_at TIMESTAMP NULL,
	last_error TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	updated_by TEXT NOT NULL,
	FOREIGN KEY(home_id) REFERENCES homes(id),
	FOREIGN KEY(updated_by) REFERENCES users(id)
);
CREATE INDEX IF NOT EXISTS idx_home_quick_links_home_order ON home_quick_links(home_id, sort_order, created_at);
