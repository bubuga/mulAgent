-- name: CreateChatSession :one
INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, runtime_id)
VALUES ($1, $2, $3, $4, (SELECT runtime_id FROM agent WHERE id = $2))
RETURNING *;

-- name: CreateChatSessionV2 :one
-- Extended create that supports kind and orchestrator_agent_id for group chats.
INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, runtime_id, kind, orchestrator_agent_id, title_source)
VALUES ($1, $2, $3, $4, (SELECT runtime_id FROM agent WHERE id = $2), $5, $6, $7)
RETURNING *;

-- name: GetChatSession :one
SELECT * FROM chat_session
WHERE id = $1;

-- name: GetChatSessionInWorkspace :one
SELECT * FROM chat_session
WHERE id = $1 AND workspace_id = $2;

-- name: ListChatSessionsByCreator :many
-- Returns active sessions with a boolean unread flag. Unread is strictly
-- per-session: either the user has uncleared assistant replies in this
-- session or they don't. Counting messages would be misleading.
SELECT cs.*,
       (cs.unread_since IS NOT NULL)::bool AS has_unread
FROM chat_session cs
WHERE cs.workspace_id = $1 AND cs.creator_id = $2 AND cs.status = 'active'
ORDER BY cs.updated_at DESC;

-- name: ListAllChatSessionsByCreator :many
SELECT cs.*,
       (cs.unread_since IS NOT NULL)::bool AS has_unread
FROM chat_session cs
WHERE cs.workspace_id = $1 AND cs.creator_id = $2
ORDER BY cs.updated_at DESC;

-- name: UpdateChatSessionTitle :one
UPDATE chat_session SET title = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateChatSessionSession :exec
-- Updates the resume pointer for a chat session. Empty/NULL inputs are
-- ignored via COALESCE so a task that completes without a session_id (e.g.
-- the agent crashed before establishing one) cannot wipe out a previously
-- recorded resume pointer. This makes the chat memory robust against
-- intermittent agent failures.
UPDATE chat_session
SET session_id = COALESCE(sqlc.narg('session_id'), session_id),
    work_dir = COALESCE(sqlc.narg('work_dir'), work_dir),
    runtime_id = COALESCE(sqlc.narg('runtime_id'), runtime_id),
    updated_at = now()
WHERE id = sqlc.arg('id');

-- name: LockChatSessionForDelete :one
-- Acquires an exclusive (FOR UPDATE) row lock on chat_session(id). Used by
-- the delete path so that a concurrent SendChatMessage cannot enqueue a new
-- agent_task_queue row referencing this session between our cancel and
-- delete steps. The FK from agent_task_queue.chat_session_id takes a
-- KEY SHARE lock on the parent row during INSERT validation, which
-- conflicts with FOR UPDATE — concurrent inserts block here and then fail
-- their FK check after we commit the delete.
SELECT id FROM chat_session
WHERE id = $1
FOR UPDATE;

-- name: DeleteChatSession :exec
-- Hard delete. chat_message rows cascade via FK ON DELETE CASCADE; the
-- chat_session_id on agent_task_queue is set NULL by FK so completed/failed
-- task history survives the session being removed. Callers MUST run inside
-- the same transaction that holds LockChatSessionForDelete and that has
-- already cancelled any in-flight tasks (see CancelAgentTasksByChatSession)
-- so the daemon does not keep running work whose result has nowhere to
-- land.
DELETE FROM chat_session WHERE id = $1;

-- name: TouchChatSession :exec
UPDATE chat_session SET updated_at = now()
WHERE id = $1;

-- name: CreateChatMessage :one
INSERT INTO chat_message (chat_session_id, role, content, task_id, failure_reason, elapsed_ms)
VALUES ($1, $2, $3, sqlc.narg(task_id), sqlc.narg(failure_reason), sqlc.narg(elapsed_ms))
RETURNING *;

-- name: ListChatMessages :many
SELECT id, chat_session_id, role, content, task_id, created_at, failure_reason, elapsed_ms, agent_id, message_type, metadata FROM chat_message
WHERE chat_session_id = $1
ORDER BY created_at ASC;

-- name: GetChatMessage :one
SELECT * FROM chat_message
WHERE id = $1;

-- name: CreateChatTask :one
INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, chat_session_id)
VALUES ($1, $2, NULL, 'queued', $3, $4)
RETURNING *;

-- name: GetLastChatTaskSession :one
-- Returns the most recent task in this chat session that managed to record a
-- session_id. Includes both completed and failed tasks: even a failed task
-- may have established a real agent session before failing, and we'd rather
-- resume there than start over and lose conversation memory. Used as a
-- fallback when chat_session.session_id is NULL. Resume-unsafe failures are
-- excluded because replaying those sessions deterministically reproduces the
-- same terminal state.
SELECT session_id, work_dir, runtime_id FROM agent_task_queue
WHERE chat_session_id = $1
  AND (
    status = 'completed'
    OR (
      status = 'failed'
      AND COALESCE(failure_reason, '') NOT IN ('iteration_limit', 'agent_fallback_message', 'api_invalid_request', 'codex_semantic_inactivity')
      AND NOT (COALESCE(error, '') ILIKE '%400%' AND COALESCE(error, '') ILIKE '%invalid_request_error%')
    )
  )
  AND session_id IS NOT NULL
ORDER BY completed_at DESC
LIMIT 1;

-- name: GetPendingChatTask :one
-- Returns the most recent in-flight task for a chat session, if any.
-- Used by the frontend to recover pending state after refresh / reopen.
-- created_at is the anchor for the chat StatusPill timer (it computes
-- elapsed = now - task.created_at), so the pill survives refresh / reopen
-- without "resetting to 0s".
SELECT id, status, created_at FROM agent_task_queue
WHERE chat_session_id = $1 AND status IN ('queued', 'dispatched', 'running')
ORDER BY created_at DESC
LIMIT 1;

-- name: ListPendingChatTasksByCreator :many
-- Aggregate view of all in-flight chat tasks owned by a given creator in a
-- workspace. Drives the FAB's "running" indicator when the chat window is
-- closed and no single session's query is active.
SELECT atq.id AS task_id, atq.status, atq.chat_session_id
FROM agent_task_queue atq
JOIN chat_session cs ON cs.id = atq.chat_session_id
WHERE cs.workspace_id = $1
  AND cs.creator_id = $2
  AND atq.status IN ('queued', 'dispatched', 'running')
ORDER BY atq.created_at DESC;

-- name: MarkChatSessionRead :exec
-- Clears unread_since, dropping the session's unread count to 0.
UPDATE chat_session SET unread_since = NULL
WHERE id = $1;

-- name: SetUnreadSinceIfNull :exec
-- Atomically stamps the first unread assistant message's arrival time.
-- No-op if the session is already in "has unread" state — keeps the earliest
-- unread boundary stable across multiple incoming replies.
UPDATE chat_session SET unread_since = now()
WHERE id = $1 AND unread_since IS NULL;

-- name: ListChatSessionsForIM :many
-- IM-first session list with last message preview and sort by activity.
SELECT
  cs.*,
  (cs.unread_since IS NOT NULL)::bool AS has_unread,
  (SELECT content FROM chat_message
   WHERE chat_session_id = cs.id
   ORDER BY created_at DESC LIMIT 1) AS last_message_preview,
  (SELECT created_at FROM chat_message
   WHERE chat_session_id = cs.id
   ORDER BY created_at DESC LIMIT 1) AS last_message_at
FROM chat_session cs
WHERE cs.workspace_id = $1
  AND cs.creator_id = $2
  AND cs.status = 'active'
ORDER BY
  COALESCE(
    (SELECT created_at FROM chat_message
     WHERE chat_session_id = cs.id
     ORDER BY created_at DESC LIMIT 1),
    cs.updated_at
  ) DESC;

-- name: UpsertChatSessionUserState :one
INSERT INTO chat_session_user_state (chat_session_id, user_id, workspace_id, pinned_at, archived_at, last_read_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (chat_session_id, user_id)
DO UPDATE SET
  pinned_at = COALESCE(EXCLUDED.pinned_at, chat_session_user_state.pinned_at),
  archived_at = COALESCE(EXCLUDED.archived_at, chat_session_user_state.archived_at),
  last_read_at = COALESCE(EXCLUDED.last_read_at, chat_session_user_state.last_read_at),
  updated_at = now()
RETURNING *;

-- name: GetChatSessionUserState :one
SELECT * FROM chat_session_user_state
WHERE chat_session_id = $1 AND user_id = $2;

-- name: ClearChatSessionUserPinned :exec
UPDATE chat_session_user_state
SET pinned_at = NULL, updated_at = now()
WHERE chat_session_id = $1 AND user_id = $2;

-- name: ClearChatSessionUserArchived :exec
UPDATE chat_session_user_state
SET archived_at = NULL, updated_at = now()
WHERE chat_session_id = $1 AND user_id = $2;

-- name: ListChatSessionsForIMV2 :many
-- IM session list with user_state join for pin/archive and last message preview.
SELECT
  cs.id, cs.workspace_id, cs.agent_id, cs.creator_id, cs.title, cs.session_id, cs.work_dir, cs.status, cs.created_at, cs.updated_at, cs.unread_since, cs.runtime_id, cs.kind, cs.orchestrator_agent_id, cs.title_source,
  (cs.unread_since IS NOT NULL)::bool AS has_unread,
  us.pinned_at,
  us.archived_at,
  (SELECT content FROM chat_message
   WHERE chat_session_id = cs.id
   ORDER BY created_at DESC LIMIT 1) AS last_message_preview,
  (SELECT created_at FROM chat_message
   WHERE chat_session_id = cs.id
   ORDER BY created_at DESC LIMIT 1) AS last_message_at
FROM chat_session cs
LEFT JOIN chat_session_user_state us
  ON us.chat_session_id = cs.id AND us.user_id = $2
WHERE cs.workspace_id = $1
  AND cs.creator_id = $2
  AND (us.archived_at IS NULL OR $3::bool)
ORDER BY
  us.pinned_at DESC NULLS LAST,
  COALESCE(
    (SELECT created_at FROM chat_message
     WHERE chat_session_id = cs.id
     ORDER BY created_at DESC LIMIT 1),
    cs.updated_at
  ) DESC;

-- name: UpdateChatSessionStatus :exec
UPDATE chat_session SET status = $2, updated_at = now()
WHERE id = $1;

-- name: AddChatSessionAgent :one
INSERT INTO chat_session_agents (
  chat_session_id,
  agent_id,
  role,
  session_id,
  runtime_id,
  work_dir
)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (chat_session_id, agent_id)
DO UPDATE SET
  role = EXCLUDED.role,
  session_id = COALESCE(EXCLUDED.session_id, chat_session_agents.session_id),
  runtime_id = COALESCE(EXCLUDED.runtime_id, chat_session_agents.runtime_id),
  work_dir = COALESCE(EXCLUDED.work_dir, chat_session_agents.work_dir),
  removed_at = NULL
RETURNING *;

-- name: ListChatSessionParticipantsBySessionIDs :many
SELECT csa.chat_session_id, csa.agent_id, csa.role, a.name AS agent_name, a.avatar_url
FROM chat_session_agents csa
JOIN agent a ON a.id = csa.agent_id
WHERE csa.chat_session_id = ANY($1::uuid[])
  AND csa.removed_at IS NULL;

-- name: CreateChatSystemMessage :one
-- Dedicated query for system messages (plan_created, plan_cancelled, step_confirmation).
INSERT INTO chat_message (chat_session_id, role, content, message_type, metadata)
VALUES ($1, 'system', $2, $3, $4)
RETURNING *;

-- name: CreateExecutionPlan :one
INSERT INTO chat_execution_plan (chat_session_id, root_message_id, orchestrator_agent_id, status, execution_mode)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetExecutionPlan :one
SELECT * FROM chat_execution_plan WHERE id = $1;

-- name: GetExecutionPlanForSession :one
-- User-side read gate: join chat_session to verify workspace + creator ownership.
-- Used by GetPlan (user-facing API). If Orchestrator CLI needs plan read access
-- in the future, add a separate agent-gate query; do not relax this one.
SELECT ep.* FROM chat_execution_plan ep
JOIN chat_session cs ON cs.id = ep.chat_session_id
WHERE ep.id = $1 AND cs.workspace_id = $2 AND cs.creator_id = $3;

-- name: GetActivePlanBySession :one
SELECT * FROM chat_execution_plan
WHERE chat_session_id = $1 AND status NOT IN ('completed', 'cancelled', 'failed')
ORDER BY created_at DESC
LIMIT 1;

-- name: GetActivePlanBySessionForUpdate :one
SELECT * FROM chat_execution_plan
WHERE chat_session_id = $1 AND status NOT IN ('completed', 'cancelled', 'failed')
ORDER BY created_at DESC
LIMIT 1
FOR UPDATE;

-- name: UpdatePlanStatus :exec
UPDATE chat_execution_plan SET status = $2, updated_at = now() WHERE id = $1;

-- name: CreateExecutionStep :one
INSERT INTO chat_execution_step (plan_id, chat_session_id, sequence, agent_id, status, planned_prompt)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetExecutionStep :one
SELECT * FROM chat_execution_step WHERE id = $1;

-- name: ListStepsByPlanWithAgent :many
SELECT es.*, a.name AS agent_name
FROM chat_execution_step es
JOIN agent a ON a.id = es.agent_id
WHERE es.plan_id = $1
ORDER BY es.sequence ASC;

-- name: UpdateStepStatus :exec
UPDATE chat_execution_step SET status = $2, updated_at = now() WHERE id = $1;

-- name: ApproveStep :one
UPDATE chat_execution_step
SET status = 'queued', approved_prompt = $2, task_id = $3, updated_at = now()
WHERE id = $1 AND status = 'awaiting_approval'
RETURNING *;

-- name: GetNextPlannedStep :one
SELECT * FROM chat_execution_step
WHERE plan_id = $1 AND status = 'planned'
ORDER BY sequence ASC
LIMIT 1;

-- name: CancelNonTerminalStepsByPlan :exec
UPDATE chat_execution_step
SET status = 'cancelled', updated_at = now()
WHERE plan_id = $1 AND status NOT IN ('completed', 'cancelled', 'failed', 'skipped');
