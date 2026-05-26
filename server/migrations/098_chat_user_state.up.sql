CREATE TABLE chat_session_user_state (
  chat_session_id UUID NOT NULL REFERENCES chat_session(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
  workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
  pinned_at TIMESTAMPTZ,
  archived_at TIMESTAMPTZ,
  last_read_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (chat_session_id, user_id)
);

CREATE INDEX idx_chat_session_user_state_list
  ON chat_session_user_state(user_id, workspace_id, archived_at, pinned_at);

-- Backfill archived sessions from legacy status field.
INSERT INTO chat_session_user_state (chat_session_id, user_id, workspace_id, archived_at)
SELECT cs.id, cs.creator_id, cs.workspace_id, cs.updated_at
FROM chat_session cs
WHERE cs.status = 'archived'
ON CONFLICT (chat_session_id, user_id) DO NOTHING;
