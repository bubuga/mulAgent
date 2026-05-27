DROP INDEX IF EXISTS idx_chat_message_session_type;
ALTER TABLE chat_message DROP COLUMN IF EXISTS metadata;
ALTER TABLE chat_message DROP COLUMN IF EXISTS message_type;
ALTER TABLE chat_message DROP COLUMN IF EXISTS agent_id;
ALTER TABLE chat_message DROP CONSTRAINT IF EXISTS chat_message_role_check;
ALTER TABLE chat_message ADD CONSTRAINT chat_message_role_check CHECK (role IN ('user', 'assistant'));
ALTER TABLE chat_session DROP COLUMN IF EXISTS title_source;
ALTER TABLE chat_session DROP COLUMN IF EXISTS orchestrator_agent_id;
ALTER TABLE chat_session DROP COLUMN IF EXISTS kind;
