-- Only after fleet access is disabled and all jobs are reconciled/stopped.
-- This removes fleet metadata; agent workspaces/output require separate retention handling.
DROP TABLE fleet_jobs;
DROP TABLE fleet_workspaces;
DROP TABLE fleet_grant_targets;
DROP TABLE fleet_grants;
DROP INDEX fleet_agent_home_identity;
