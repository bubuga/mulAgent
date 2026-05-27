-- Add group chat fields to chat_session.
ALTER TABLE chat_session ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'direct'
  CHECK (kind IN ('direct', 'group'));

ALTER TABLE chat_session ADD COLUMN IF NOT EXISTS orchestrator_agent_id UUID REFERENCES agent(id) ON DELETE SET NULL;

ALTER TABLE chat_session ADD COLUMN IF NOT EXISTS title_source TEXT NOT NULL DEFAULT 'participant_fallback'
  CHECK (title_source IN ('manual', 'agent_names', 'first_message', 'participant_fallback'));

-- Add message metadata fields to chat_message.
-- Expand role constraint to include 'system' for step confirmation / artifact cards.
ALTER TABLE chat_message DROP CONSTRAINT IF EXISTS chat_message_role_check;
ALTER TABLE chat_message ADD CONSTRAINT chat_message_role_check
  CHECK (role IN ('user', 'assistant', 'system'));

ALTER TABLE chat_message ADD COLUMN IF NOT EXISTS agent_id UUID REFERENCES agent(id) ON DELETE SET NULL;

ALTER TABLE chat_message ADD COLUMN IF NOT EXISTS message_type TEXT NOT NULL DEFAULT 'text';

ALTER TABLE chat_message ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Index for filtering system messages in a session.
CREATE INDEX IF NOT EXISTS idx_chat_message_session_type
  ON chat_message(chat_session_id, message_type)
  WHERE message_type != 'text';
