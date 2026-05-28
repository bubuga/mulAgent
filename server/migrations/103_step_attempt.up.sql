CREATE TABLE chat_execution_step_attempt (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  step_id UUID NOT NULL REFERENCES chat_execution_step(id) ON DELETE CASCADE,
  attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
  task_id UUID REFERENCES agent_task_queue(id) ON DELETE SET NULL,
  approved_prompt TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'queued'
    CHECK (status IN ('queued', 'dispatched', 'running', 'completed', 'failed', 'cancelled')),
  failure_reason TEXT,
  error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (step_id, attempt_number)
);

CREATE UNIQUE INDEX idx_step_attempt_task
  ON chat_execution_step_attempt(task_id)
  WHERE task_id IS NOT NULL;

CREATE INDEX idx_step_attempt_step
  ON chat_execution_step_attempt(step_id, attempt_number DESC);
