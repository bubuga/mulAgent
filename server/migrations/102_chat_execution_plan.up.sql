CREATE TABLE chat_execution_plan (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  chat_session_id UUID NOT NULL REFERENCES chat_session(id) ON DELETE CASCADE,
  root_message_id UUID REFERENCES chat_message(id) ON DELETE SET NULL,
  orchestrator_agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE RESTRICT,
  status TEXT NOT NULL DEFAULT 'awaiting_approval'
    CHECK (status IN ('draft', 'awaiting_approval', 'running', 'completed', 'cancelled', 'failed')),
  execution_mode TEXT NOT NULL DEFAULT 'serial'
    CHECK (execution_mode IN ('serial', 'parallel')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_chat_execution_plan_session
  ON chat_execution_plan(chat_session_id, created_at DESC);

-- DB-level guard: only one active plan per session.
CREATE UNIQUE INDEX idx_chat_execution_plan_one_active
  ON chat_execution_plan(chat_session_id)
  WHERE status NOT IN ('completed', 'cancelled', 'failed');

CREATE TABLE chat_execution_step (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  plan_id UUID NOT NULL REFERENCES chat_execution_plan(id) ON DELETE CASCADE,
  chat_session_id UUID NOT NULL REFERENCES chat_session(id) ON DELETE CASCADE,
  sequence INTEGER NOT NULL CHECK (sequence > 0),
  agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE RESTRICT,
  status TEXT NOT NULL DEFAULT 'planned'
    CHECK (status IN ('planned', 'awaiting_approval', 'queued', 'running', 'completed', 'skipped', 'cancelled', 'failed')),
  planned_prompt TEXT NOT NULL,
  approved_prompt TEXT,
  task_id UUID REFERENCES agent_task_queue(id) ON DELETE SET NULL,
  parent_step_id UUID REFERENCES chat_execution_step(id) ON DELETE SET NULL,
  parallel_group_id UUID,
  base_revision TEXT,
  result_revision TEXT,
  artifact_summary JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (plan_id, sequence)
);

CREATE INDEX idx_chat_execution_step_session_status
  ON chat_execution_step(chat_session_id, status, sequence);

-- PR7: find step by task_id, or find task by step_id.
CREATE UNIQUE INDEX idx_chat_execution_step_task
  ON chat_execution_step(task_id)
  WHERE task_id IS NOT NULL;
