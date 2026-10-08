-- Refuse rollback while non-Linux enrollment records exist; preserve enrollment history.
ALTER TABLE agent_enrollments DROP CONSTRAINT agent_enrollments_platform_check;
ALTER TABLE agent_enrollments ADD CONSTRAINT agent_enrollments_platform_check CHECK (platform = 'linux');
