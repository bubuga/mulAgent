ALTER TABLE chat_execution_step_attempt
  DROP COLUMN base_revision,
  DROP COLUMN result_revision,
  DROP COLUMN revision_warnings;
