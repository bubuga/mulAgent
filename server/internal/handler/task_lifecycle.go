package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// RecoverOrphanedTasks is called by the daemon at startup for each runtime
// it owns. It atomically fails any dispatched/running tasks the server still
// believes belong to that runtime — those are the tasks the previous daemon
// process was running when it died — and triggers MaybeRetryFailedTask for
// each so the user sees a fresh attempt instead of a permanently stuck row.
//
// This is the targeted fix for "issue stuck at in_progress when daemon
// restarts mid-task": the runtime heartbeat sweeper takes up to 75s + the
// in-process task timeout (2.5h) to notice such tasks; the daemon itself
// knows the moment it comes back up, so we let it report orphan recovery.
func (h *Handler) RecoverOrphanedTasks(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	if _, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID); !ok {
		return
	}

	rows, err := h.Queries.RecoverOrphanedTasksForRuntime(r.Context(), parseUUID(runtimeID))
	if err != nil {
		slog.Warn("recover-orphans failed", "runtime_id", runtimeID, "error", err)
		writeError(w, http.StatusInternalServerError, "recover orphans failed")
		return
	}

	// Funnel through the shared post-failure pipeline so we get the same
	// task:failed events, agent reconcile, issue rollback, and auto-retry
	// behaviour as the runtime sweeper. This was previously a fast-path
	// that bypassed those side effects, leaving the UI stale when no retry
	// was created (max_attempts exhausted, autopilot, non-retryable reason).
	retried := h.TaskService.HandleFailedTasks(r.Context(), rows)

	if len(rows) > 0 {
		slog.Info("recover-orphans completed",
			"runtime_id", runtimeID,
			"orphaned", len(rows),
			"retried", retried,
		)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"orphaned": len(rows),
		"retried":  retried,
	})
}

// PinTaskSession lets the daemon persist the agent's session_id and
// work_dir as soon as they're known — typically right after the agent
// emits its first system message — so a crash mid-run doesn't lose the
// resume pointer needed to continue the conversation on the next attempt.
//
// PR8: Also supports base_revision for step-linked tasks and participant
// state updates for group chats. All writes wrapped in a transaction.
type PinTaskSessionRequest struct {
	SessionID        string                  `json:"session_id,omitempty"`
	WorkDir          string                  `json:"work_dir,omitempty"`
	BaseRevision     *service.TaskRevisionInfo `json:"base_revision,omitempty"`
	RevisionWarnings []string                `json:"revision_warnings,omitempty"`
}

func (h *Handler) PinTaskSession(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	task, ok := h.requireDaemonTaskAccess(w, r, taskID)
	if !ok {
		return
	}

	var req PinTaskSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SessionID == "" && req.WorkDir == "" && req.BaseRevision == nil {
		writeError(w, http.StatusBadRequest, "session_id, work_dir, or base_revision required")
		return
	}

	ctx := r.Context()
	taskUUID := parseUUID(taskID)

	// D2: requireDaemonTaskAccess already loaded the task.
	// Transaction approach: h.TxStarter.Begin (nil-safe for tests).
	var qtx *db.Queries
	var tx pgx.Tx
	if h.TxStarter != nil {
		var txErr error
		tx, txErr = h.TxStarter.Begin(ctx)
		if txErr != nil {
			slog.Warn("pin-session: begin tx failed", "task_id", taskID, "error", txErr)
			writeError(w, http.StatusInternalServerError, "pin session failed")
			return
		}
		defer tx.Rollback(ctx)
		qtx = h.Queries.WithTx(tx)
	} else {
		qtx = h.Queries
	}

	// 1. Update task session (existing behavior)
	params := db.UpdateAgentTaskSessionParams{ID: taskUUID}
	if req.SessionID != "" {
		params.SessionID = pgtype.Text{String: req.SessionID, Valid: true}
	}
	if req.WorkDir != "" {
		params.WorkDir = pgtype.Text{String: req.WorkDir, Valid: true}
	}
	if err := qtx.UpdateAgentTaskSession(ctx, params); err != nil {
		slog.Warn("pin-session failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "pin session failed")
		return
	}

	// 2. PR8: If step-linked AND base_revision non-nil, update attempt + step mirror
	if task.ChatSessionID.Valid && req.BaseRevision != nil {
		if _, attErr := qtx.GetStepAttemptByTaskID(ctx, taskUUID); attErr == nil {
			baseJSON, _ := json.Marshal(req.BaseRevision)
			baseStr := string(baseJSON)
			var warningsPtr []byte
			if req.RevisionWarnings != nil {
				warningsPtr, _ = json.Marshal(req.RevisionWarnings)
			}
			_ = qtx.UpdateStepAttemptRevisionsByTaskID(ctx, db.UpdateStepAttemptRevisionsByTaskIDParams{
				TaskID:           taskUUID,
				BaseRevision:     pgtype.Text{String: baseStr, Valid: true},
				ResultRevision:   pgtype.Text{},
				RevisionWarnings: warningsPtr,
			})
			_ = qtx.UpdateStepRevisionsMirrorByTaskID(ctx, db.UpdateStepRevisionsMirrorByTaskIDParams{
				TaskID:         taskUUID,
				BaseRevision:   pgtype.Text{String: baseStr, Valid: true},
				ResultRevision: pgtype.Text{},
			})
		}
	}

	// 3. PR8: If group chat, update participant state (D6: UPSERT)
	if task.ChatSessionID.Valid {
		if cs, csErr := qtx.GetChatSession(ctx, task.ChatSessionID); csErr == nil && cs.Kind == "group" {
			_, _ = qtx.UpsertChatSessionAgentSession(ctx, db.UpsertChatSessionAgentSessionParams{
				ChatSessionID: task.ChatSessionID,
				AgentID:       task.AgentID,
				SessionID:     pgtype.Text{String: req.SessionID, Valid: req.SessionID != ""},
				RuntimeID:     task.RuntimeID,
				WorkDir:       pgtype.Text{String: req.WorkDir, Valid: req.WorkDir != ""},
			})
		}
	}

	if tx != nil {
		if err := tx.Commit(ctx); err != nil {
			slog.Warn("pin-session: commit failed", "task_id", taskID, "error", err)
			writeError(w, http.StatusInternalServerError, "pin session failed")
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// RerunIssueRequest is the optional body of POST /api/issues/{id}/rerun.
// All fields are optional; an empty body keeps the legacy "rerun the issue's
// current assignee" behaviour used by the CLI.
type RerunIssueRequest struct {
	// TaskID identifies the execution-log row the user clicked retry on.
	// When set, the rerun targets the agent that ran that specific task
	// (and reuses its leader/worker role) rather than the issue's current
	// assignee — so clicking retry on row that belonged to a now-displaced
	// agent re-fires that same agent, not the new assignee.
	TaskID string `json:"task_id,omitempty"`
}

// RerunIssue manually re-enqueues an agent run for the issue. By default it
// targets the issue's current assignee (agent or squad leader); if the
// request body carries task_id, the rerun targets the agent that ran that
// specific past task instead. The new task is flagged force_fresh_session=true:
// the daemon claim handler skips the (agent_id, issue_id) session-resume
// lookup so the agent starts a clean session. A user clicking rerun has just
// judged the prior output bad — replaying the same conversation would replay
// the same poisoned state. (Automatic retry, by contrast, intentionally
// inherits the session — that path handles infrastructure failures, not bad
// output.)
func (h *Handler) RerunIssue(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}

	// Body is optional. A zero-length body or `{}` keeps the legacy
	// assignee-driven rerun behaviour the CLI relies on.
	var req RerunIssueRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	var sourceTaskID pgtype.UUID
	if req.TaskID != "" {
		parsed, ok := parseUUIDOrBadRequest(w, req.TaskID, "task_id")
		if !ok {
			return
		}
		sourceTaskID = parsed
	}

	task, err := h.TaskService.RerunIssue(r.Context(), issue.ID, sourceTaskID, pgtype.UUID{})
	if err != nil {
		slog.Warn("issue rerun failed", "issue_id", id, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, taskToResponse(*task))
}
