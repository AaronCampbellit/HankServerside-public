ALTER TABLE agent_enrollments DROP CONSTRAINT agent_enrollments_platform_check;
ALTER TABLE agent_enrollments ADD CONSTRAINT agent_enrollments_platform_check CHECK (platform IN ('linux', 'macos', 'windows'));
