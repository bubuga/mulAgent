ALTER TABLE chat_execution_step_attempt
  ADD COLUMN base_revision TEXT,
  ADD COLUMN result_revision TEXT,
  ADD COLUMN revision_warnings JSONB NOT NULL DEFAULT '[]'::jsonb;
