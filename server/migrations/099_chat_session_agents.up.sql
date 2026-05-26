CREATE TABLE chat_session_agents (
  chat_session_id UUID NOT NULL REFERENCES chat_session(id) ON DELETE CASCADE,
  agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
  role TEXT NOT NULL CHECK (role IN ('orchestrator', 'participant')),
  runtime_id UUID REFERENCES agent_runtime(id) ON DELETE SET NULL,
  session_id TEXT,
  last_seen_step_id UUID,
  last_seen_revision TEXT,
  joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  removed_at TIMESTAMPTZ,
  PRIMARY KEY (chat_session_id, agent_id)
);

CREATE INDEX idx_chat_session_agents_agent
  ON chat_session_agents(agent_id)
  WHERE removed_at IS NULL;

-- Backfill existing direct chat sessions.
INSERT INTO chat_session_agents (chat_session_id, agent_id, role, runtime_id, session_id)
SELECT cs.id, cs.agent_id, 'participant', cs.runtime_id, cs.session_id
FROM chat_session cs
WHERE cs.agent_id IS NOT NULL
ON CONFLICT (chat_session_id, agent_id) DO NOTHING;
