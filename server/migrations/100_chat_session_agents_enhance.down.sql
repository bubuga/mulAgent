DROP INDEX IF EXISTS idx_chat_session_agents_one_orchestrator;
ALTER TABLE chat_session_agents DROP COLUMN IF EXISTS work_dir;
