ALTER TABLE agents ADD COLUMN IF NOT EXISTS agent_type TEXT NOT NULL DEFAULT 'primary';

WITH ranked_agents AS (
    SELECT id,
           ROW_NUMBER() OVER (PARTITION BY home_id ORDER BY created_at ASC, id ASC) AS home_rank
    FROM agents
)
UPDATE agents
SET agent_type = CASE WHEN ranked_agents.home_rank = 1 THEN 'primary' ELSE 'worker' END
FROM ranked_agents
WHERE agents.id = ranked_agents.id;

ALTER TABLE agents DROP CONSTRAINT IF EXISTS agents_agent_type_check;
ALTER TABLE agents
    ADD CONSTRAINT agents_agent_type_check
    CHECK (agent_type IN ('primary', 'worker'));

CREATE UNIQUE INDEX IF NOT EXISTS agents_one_primary_per_home_idx
    ON agents (home_id)
    WHERE agent_type = 'primary';
