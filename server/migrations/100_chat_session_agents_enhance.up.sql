-- Add work_dir column for agent workspace path tracking.
ALTER TABLE chat_session_agents ADD COLUMN IF NOT EXISTS work_dir TEXT;

-- Backfill work_dir from chat_session for existing rows.
UPDATE chat_session_agents csa
SET work_dir = cs.work_dir
FROM chat_session cs
WHERE csa.chat_session_id = cs.id
  AND csa.work_dir IS NULL
  AND cs.work_dir IS NOT NULL;

-- Add orchestrator uniqueness: only one active orchestrator per chat.
CREATE UNIQUE INDEX IF NOT EXISTS idx_chat_session_agents_one_orchestrator
  ON chat_session_agents(chat_session_id)
  WHERE role = 'orchestrator' AND removed_at IS NULL;
