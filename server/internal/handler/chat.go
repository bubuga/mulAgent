package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// chatSessionTitleMaxLen caps the rename input. Long enough to fit a
// meaningful summary, short enough to keep the dropdown row scannable.
const chatSessionTitleMaxLen = 200

// ---------------------------------------------------------------------------
// Chat Sessions
// ---------------------------------------------------------------------------

type CreateChatSessionRequest struct {
	Kind                string   `json:"kind"`                  // "direct" (default) or "group"
	AgentID             string   `json:"agent_id"`              // for direct
	AgentIDs            []string `json:"agent_ids"`             // for group
	OrchestratorAgentID string   `json:"orchestrator_agent_id"` // for group
	Title               string   `json:"title"`
}

func (h *Handler) CreateChatSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())

	var req CreateChatSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	kind := req.Kind
	if kind == "" {
		kind = "direct"
	}
	if kind != "direct" && kind != "group" {
		writeError(w, http.StatusBadRequest, "kind must be 'direct' or 'group'")
		return
	}

	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	if kind == "direct" {
		h.createDirectChat(w, r, userID, workspaceUUID, req)
	} else {
		h.createGroupChat(w, r, userID, workspaceUUID, req)
	}
}

func (h *Handler) createDirectChat(w http.ResponseWriter, r *http.Request, userID string, workspaceUUID pgtype.UUID, req CreateChatSessionRequest) {
	if req.AgentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required for direct chat")
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, req.AgentID, "agent_id")
	if !ok {
		return
	}

	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if agent.ArchivedAt.Valid {
		writeError(w, http.StatusBadRequest, "agent is archived")
		return
	}
	actorType, actorID := h.resolveActor(r, userID, uuidToString(workspaceUUID))
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, uuidToString(workspaceUUID)) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	session, err := qtx.CreateChatSession(r.Context(), db.CreateChatSessionParams{
		WorkspaceID: workspaceUUID,
		AgentID:     agentID,
		CreatorID:   parseUUID(userID),
		Title:       req.Title,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create chat session")
		return
	}

	_, err = qtx.AddChatSessionAgent(r.Context(), db.AddChatSessionAgentParams{
		ChatSessionID: session.ID,
		AgentID:       agentID,
		Role:          "participant",
		RuntimeID:     session.RuntimeID,
		SessionID:     session.SessionID,
		WorkDir:       session.WorkDir,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create chat participant")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit transaction")
		return
	}

	writeJSON(w, http.StatusCreated, chatSessionToResponse(session))
}

func (h *Handler) createGroupChat(w http.ResponseWriter, r *http.Request, userID string, workspaceUUID pgtype.UUID, req CreateChatSessionRequest) {
	if len(req.AgentIDs) < 2 {
		writeError(w, http.StatusBadRequest, "group chat requires at least 2 agents")
		return
	}
	if req.OrchestratorAgentID == "" {
		writeError(w, http.StatusBadRequest, "orchestrator_agent_id is required for group chat")
		return
	}

	// Deduplicate agent IDs.
	seen := make(map[string]bool)
	uniqueIDs := make([]string, 0, len(req.AgentIDs))
	for _, id := range req.AgentIDs {
		if !seen[id] {
			seen[id] = true
			uniqueIDs = append(uniqueIDs, id)
		}
	}
	if len(uniqueIDs) < 2 {
		writeError(w, http.StatusBadRequest, "group chat requires at least 2 distinct agents")
		return
	}

	// Validate orchestrator is one of the agents.
	isOrchestrator := false
	for _, id := range uniqueIDs {
		if id == req.OrchestratorAgentID {
			isOrchestrator = true
			break
		}
	}
	if !isOrchestrator {
		writeError(w, http.StatusBadRequest, "orchestrator_agent_id must be one of agent_ids")
		return
	}

	orchestratorID, ok := parseUUIDOrBadRequest(w, req.OrchestratorAgentID, "orchestrator_agent_id")
	if !ok {
		return
	}

	// Validate all agents exist, are not archived, and are accessible.
	actorType, actorID := h.resolveActor(r, userID, uuidToString(workspaceUUID))
	agentUUIDs := make([]pgtype.UUID, 0, len(uniqueIDs))
	for _, idStr := range uniqueIDs {
		agentUUID, ok := parseUUIDOrBadRequest(w, idStr, "agent_id")
		if !ok {
			return
		}
		agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
			ID:          agentUUID,
			WorkspaceID: workspaceUUID,
		})
		if err != nil {
			writeError(w, http.StatusNotFound, "agent not found: "+idStr)
			return
		}
		if agent.ArchivedAt.Valid {
			writeError(w, http.StatusBadRequest, "agent is archived: "+idStr)
			return
		}
		if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, uuidToString(workspaceUUID)) {
			writeError(w, http.StatusForbidden, "you do not have access to agent: "+idStr)
			return
		}
		agentUUIDs = append(agentUUIDs, agentUUID)
	}

	// Build default title from agent names.
	title := req.Title
	if title == "" {
		names := make([]string, 0, len(uniqueIDs))
		for _, idStr := range uniqueIDs {
			agent, _ := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
				ID:          parseUUID(idStr),
				WorkspaceID: workspaceUUID,
			})
			if agent.Name != "" {
				names = append(names, agent.Name)
			}
		}
		if len(names) > 0 {
			title = strings.Join(names, ", ")
		}
		titleSource := "agent_names"
		_ = titleSource
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// Create session with kind=group, agent_id=orchestrator (legacy compat).
	session, err := qtx.CreateChatSessionV2(r.Context(), db.CreateChatSessionV2Params{
		WorkspaceID:         workspaceUUID,
		AgentID:             orchestratorID,
		CreatorID:           parseUUID(userID),
		Title:               title,
		Kind:                "group",
		OrchestratorAgentID: orchestratorID,
		TitleSource:         "agent_names",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create group chat session")
		return
	}

	// Insert orchestrator participant.
	_, err = qtx.AddChatSessionAgent(r.Context(), db.AddChatSessionAgentParams{
		ChatSessionID: session.ID,
		AgentID:       orchestratorID,
		Role:          "orchestrator",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create orchestrator participant")
		return
	}

	// Insert other participants.
	for _, agentUUID := range agentUUIDs {
		if uuidToString(agentUUID) == req.OrchestratorAgentID {
			continue // already added as orchestrator
		}
		_, err = qtx.AddChatSessionAgent(r.Context(), db.AddChatSessionAgentParams{
			ChatSessionID: session.ID,
			AgentID:       agentUUID,
			Role:          "participant",
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create participant")
			return
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit transaction")
		return
	}

	writeJSON(w, http.StatusCreated, chatSessionToResponse(session))
}

func (h *Handler) ListChatSessions(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())

	// Compute the accessible-agents set once and use it to drop sessions
	// whose target agent the caller no longer has access to — without this,
	// a member whose role was downgraded would still see the session list
	// (and transcripts via ListChatMessages) for any private agent they
	// previously had access to. Falls back to the user's role from the
	// workspace member context.
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}

	view := r.URL.Query().Get("view")
	status := r.URL.Query().Get("status")
	q := r.URL.Query().Get("q")

	var resp []ChatSessionResponse

	if view == "im" {
		// IM-first view: includes pin/archive state, last message preview, sorted by activity.
		includeArchived := r.URL.Query().Get("archived") == "true"
		rows, err := h.Queries.ListChatSessionsForIMV2(r.Context(), db.ListChatSessionsForIMV2Params{
			WorkspaceID: parseUUID(workspaceID),
			CreatorID:   parseUUID(userID),
			Column3:     includeArchived,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list chat sessions")
			return
		}

		// Batch-load participants for all sessions.
		sessionIDs := make([]pgtype.UUID, 0, len(rows))
		for _, s := range rows {
			sessionIDs = append(sessionIDs, s.ID)
		}
		participantRows, _ := h.Queries.ListChatSessionParticipantsBySessionIDs(r.Context(), sessionIDs)
		participantsBySession := make(map[string][]ParticipantResponse)
		for _, p := range participantRows {
			sid := uuidToString(p.ChatSessionID)
			pr := ParticipantResponse{
				AgentID: uuidToString(p.AgentID),
				Role:    p.Role,
				Name:    p.AgentName,
			}
			if p.AvatarUrl.Valid {
				pr.AvatarURL = &p.AvatarUrl.String
			}
			participantsBySession[sid] = append(participantsBySession[sid], pr)
		}

		resp = make([]ChatSessionResponse, 0, len(rows))
		for _, s := range rows {
			if _, ok := allowed[uuidToString(s.AgentID)]; !ok {
				continue
			}
			sid := uuidToString(s.ID)
			parts := participantsBySession[sid]
			kind := "direct"
			if len(parts) > 1 {
				kind = "group"
			}
			// Fallback: if no participant rows exist for a direct/legacy session,
			// construct a synthetic participant from chat_session.agent_id.
			// This handles pre-migration sessions and edge cases where backfill
			// missed a row. Group sessions should not silently fallback.
			if len(parts) == 0 && kind == "direct" {
				parts = []ParticipantResponse{{
					AgentID: uuidToString(s.AgentID),
					Role:    "participant",
				}}
			}
			item := ChatSessionResponse{
				ID:          sid,
				WorkspaceID: uuidToString(s.WorkspaceID),
				AgentID:     uuidToString(s.AgentID),
				CreatorID:   uuidToString(s.CreatorID),
				Title:       s.Title,
				Status:      s.Status,
				HasUnread:   s.HasUnread,
				CreatedAt:   timestampToString(s.CreatedAt),
				UpdatedAt:   timestampToString(s.UpdatedAt),
				Kind:        kind,
				IsPinned:    s.PinnedAt.Valid,
				Participants: parts,
			}
			if s.ArchivedAt.Valid {
				ts := timestampToString(s.ArchivedAt)
				item.ArchivedAt = &ts
			}
			if s.LastMessagePreview.Valid {
				item.LastMessagePreview = &s.LastMessagePreview.String
			}
			if s.LastMessageAt.Valid {
				ts := timestampToString(s.LastMessageAt)
				item.LastMessageAt = &ts
			}
			// Filter by search query.
			if q != "" {
				ql := strings.ToLower(q)
				titleMatch := strings.Contains(strings.ToLower(s.Title), ql)
				contentMatch := s.LastMessagePreview.Valid && strings.Contains(strings.ToLower(s.LastMessagePreview.String), ql)
				nameMatch := false
				for _, p := range parts {
					if strings.Contains(strings.ToLower(p.Name), ql) {
						nameMatch = true
						break
					}
				}
				if !titleMatch && !contentMatch && !nameMatch {
					continue
				}
			}
			resp = append(resp, item)
		}
	} else if status == "all" {
		// Legacy: all sessions including archived.
		rows, err := h.Queries.ListAllChatSessionsByCreator(r.Context(), db.ListAllChatSessionsByCreatorParams{
			WorkspaceID: parseUUID(workspaceID),
			CreatorID:   parseUUID(userID),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list chat sessions")
			return
		}
		resp = make([]ChatSessionResponse, 0, len(rows))
		for _, s := range rows {
			if _, ok := allowed[uuidToString(s.AgentID)]; !ok {
				continue
			}
			resp = append(resp, ChatSessionResponse{
				ID:          uuidToString(s.ID),
				WorkspaceID: uuidToString(s.WorkspaceID),
				AgentID:     uuidToString(s.AgentID),
				CreatorID:   uuidToString(s.CreatorID),
				Title:       s.Title,
				Status:      s.Status,
				HasUnread:   s.HasUnread,
				CreatedAt:   timestampToString(s.CreatedAt),
				UpdatedAt:   timestampToString(s.UpdatedAt),
			})
		}
	} else {
		// Legacy default: active sessions only.
		rows, err := h.Queries.ListChatSessionsByCreator(r.Context(), db.ListChatSessionsByCreatorParams{
			WorkspaceID: parseUUID(workspaceID),
			CreatorID:   parseUUID(userID),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list chat sessions")
			return
		}
		resp = make([]ChatSessionResponse, 0, len(rows))
		for _, s := range rows {
			if _, ok := allowed[uuidToString(s.AgentID)]; !ok {
				continue
			}
			resp = append(resp, ChatSessionResponse{
				ID:          uuidToString(s.ID),
				WorkspaceID: uuidToString(s.WorkspaceID),
				AgentID:     uuidToString(s.AgentID),
				CreatorID:   uuidToString(s.CreatorID),
				Title:       s.Title,
				Status:      s.Status,
				HasUnread:   s.HasUnread,
				CreatedAt:   timestampToString(s.CreatedAt),
				UpdatedAt:   timestampToString(s.UpdatedAt),
			})
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) loadChatSessionForUser(w http.ResponseWriter, r *http.Request, userID, workspaceID, sessionID string) (db.ChatSession, bool) {
	sessionUUID, ok := parseUUIDOrBadRequest(w, sessionID, "chat session id")
	if !ok {
		return db.ChatSession{}, false
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return db.ChatSession{}, false
	}
	session, err := h.Queries.GetChatSessionInWorkspace(r.Context(), db.GetChatSessionInWorkspaceParams{
		ID:          sessionUUID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "chat session not found")
		return db.ChatSession{}, false
	}
	if uuidToString(session.CreatorID) != userID {
		writeError(w, http.StatusForbidden, "not your chat session")
		return db.ChatSession{}, false
	}
	return session, true
}

// gateChatSessionForUser combines the session ownership check with the
// private-agent access gate so a member who has lost access to the target
// agent (role downgrade, ownership transfer, agent flipped to private)
// cannot continue reading the chat transcript even though they remain the
// session creator. Returns ok=false after writing the error response.
func (h *Handler) gateChatSessionForUser(w http.ResponseWriter, r *http.Request, userID, workspaceID, sessionID string) (db.ChatSession, bool) {
	session, ok := h.loadChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return db.ChatSession{}, false
	}
	agent, err := h.Queries.GetAgent(r.Context(), session.AgentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return db.ChatSession{}, false
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return db.ChatSession{}, false
	}
	return session, true
}

func (h *Handler) GetChatSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, chatSessionToResponse(session))
}

type UpdateChatSessionRequest struct {
	Title  *string `json:"title"`
	Status *string `json:"status"` // legacy compat: "archived" or "active"
}

// UpdateChatSession updates user-editable fields on a chat session.
// Accepts optional `title` for rename and optional `status` for legacy
// archive/unarchive compatibility. At least one field must be provided.
func (h *Handler) UpdateChatSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	var req UpdateChatSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	// Handle title update.
	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			writeError(w, http.StatusBadRequest, "title is required")
			return
		}
		if len([]rune(title)) > chatSessionTitleMaxLen {
			writeError(w, http.StatusBadRequest, "title is too long")
			return
		}
		updated, err := h.Queries.UpdateChatSessionTitle(r.Context(), db.UpdateChatSessionTitleParams{
			ID:    session.ID,
			Title: title,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update chat session")
			return
		}
		resolvedSessionID := uuidToString(updated.ID)
		h.publishChat(protocol.EventChatSessionUpdated, workspaceID, "member", userID, resolvedSessionID, protocol.ChatSessionUpdatedPayload{
			ChatSessionID: resolvedSessionID,
			Title:         updated.Title,
			UpdatedAt:     timestampToString(updated.UpdatedAt),
		})
		writeJSON(w, http.StatusOK, chatSessionToResponse(updated))
		return
	}

	// Handle legacy status update with transition sync to user_state.
	if req.Status != nil {
		if *req.Status != "archived" && *req.Status != "active" {
			writeError(w, http.StatusBadRequest, "status must be 'archived' or 'active'")
			return
		}
		if err := h.Queries.UpdateChatSessionStatus(r.Context(), db.UpdateChatSessionStatusParams{
			ID:     session.ID,
			Status: *req.Status,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update session status")
			return
		}
		// Transition sync: mirror to user_state.
		if *req.Status == "archived" {
			_, _ = h.Queries.UpsertChatSessionUserState(r.Context(), db.UpsertChatSessionUserStateParams{
				ChatSessionID: session.ID,
				UserID:        parseUUID(userID),
				WorkspaceID:   parseUUID(workspaceID),
				ArchivedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
			})
		} else {
			_ = h.Queries.ClearChatSessionUserArchived(r.Context(), db.ClearChatSessionUserArchivedParams{
				ChatSessionID: session.ID,
				UserID:        parseUUID(userID),
			})
		}
		// Re-fetch to return updated session.
		updated, _ := h.Queries.GetChatSession(r.Context(), session.ID)
		writeJSON(w, http.StatusOK, chatSessionToResponse(updated))
		return
	}
}

// DeleteChatSession hard-deletes a chat session owned by the caller. The
// row lock + cancel + delete run inside a single tx so a concurrent
// SendChatMessage cannot enqueue a task that would later be orphaned by
// the FK ON DELETE SET NULL on agent_task_queue.chat_session_id. Cancel
// failure aborts the delete; events fire only after commit.
func (h *Handler) DeleteChatSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	session, ok := h.loadChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// FOR UPDATE on the chat_session row blocks any concurrent INSERT into
	// agent_task_queue that references it (the FK validation needs a
	// KEY SHARE lock). After we commit the delete, the blocked INSERT
	// fails its FK check, so it can't land an orphaned task.
	if _, err := qtx.LockChatSessionForDelete(r.Context(), session.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Already gone — treat as idempotent success.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to lock chat session")
		return
	}

	cancelled, err := qtx.CancelAgentTasksByChatSession(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to cancel chat session tasks")
		return
	}

	if err := qtx.DeleteChatSession(r.Context(), session.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete chat session")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("commit chat session delete failed", "session_id", sessionID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to commit chat session delete")
		return
	}

	// Post-commit broadcasts. Subscribers should never observe events for a
	// tx that didn't actually persist.
	h.TaskService.BroadcastCancelledTasks(r.Context(), cancelled)

	resolvedSessionID := uuidToString(session.ID)
	h.publishChat(protocol.EventChatSessionDeleted, workspaceID, "member", userID, resolvedSessionID, protocol.ChatSessionDeletedPayload{
		ChatSessionID: resolvedSessionID,
	})

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Chat Messages
// ---------------------------------------------------------------------------

type SendChatMessageRequest struct {
	Content       string   `json:"content"`
	AttachmentIDs []string `json:"attachment_ids"`
	MentionIDs    []string `json:"mention_ids"` // agent IDs mentioned in group chat
}

type SendChatMessageResponse struct {
	MessageID string `json:"message_id"`
	TaskID    string `json:"task_id"`
	// CreatedAt anchors the chat StatusPill timer the instant the user
	// hits send. Without it the front-end falls back to its local clock
	// and the timer "snaps backwards" later when WS events deliver the
	// real created_at. Returning it here means the pill renders 0s from
	// the start with a stable anchor.
	CreatedAt string `json:"created_at"`
}

func (h *Handler) SendChatMessage(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	var req SendChatMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}

	// Pre-validate attachment ids early so invalid input returns 400 before
	// any state mutation. The actual link runs after CreateChatMessage so we
	// have a message_id to back-fill into the attachment rows.
	attachmentIDs, ok := parseUUIDSliceOrBadRequest(w, req.AttachmentIDs, "attachment_ids")
	if !ok {
		return
	}

	// Load chat session and re-check the private-agent gate on every send.
	// The session's creator passed the gate at create time, but their
	// workspace role (or the agent's owner) may have changed since — keep
	// stale sessions from being a back-door into a private agent the user
	// can no longer reach. Agent senders bypass to preserve A2A collaboration.
	session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}
	// New archive flow doesn't exist anymore, but legacy rows with
	// status='archived' may still be in the DB from before the feature
	// was removed. Refuse to enqueue new agent work for them — frontend
	// surfaces these as read-only.
	if session.Status != "active" {
		writeError(w, http.StatusBadRequest, "chat session is archived")
		return
	}

	// Create the user message first so the daemon can always find it.
	msg, err := h.Queries.CreateChatMessage(r.Context(), db.CreateChatMessageParams{
		ChatSessionID: session.ID,
		Role:          "user",
		Content:       req.Content,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create chat message")
		return
	}

	// Back-fill chat_message_id on attachments that were uploaded against
	// this session while the user was composing. The query only touches rows
	// where chat_session_id matches AND chat_message_id IS NULL, so it cannot
	// rebind an attachment that already belongs to an earlier message.
	if len(attachmentIDs) > 0 {
		if err := h.Queries.LinkAttachmentsToChatMessage(r.Context(), db.LinkAttachmentsToChatMessageParams{
			ChatMessageID: msg.ID,
			ChatSessionID: session.ID,
			Column3:       attachmentIDs,
		}); err != nil {
			// Don't fail the send — the message content is already saved and
			// the attachments remain on the session (still downloadable).
			slog.Warn("link chat attachments failed", "error", err, "message_id", uuidToString(msg.ID))
		}
	}

	// Determine target agent for task routing.
	// Direct chat: always use session's default agent.
	// Group chat: 0 or 2+ mentions → orchestrator; 1 mention → that agent.
	targetAgentID := session.AgentID
	if session.Kind == "group" && len(req.MentionIDs) > 0 {
		// Deduplicate and validate mentions against participants.
		seen := make(map[string]bool)
		uniqueMentions := make([]string, 0, len(req.MentionIDs))
		for _, id := range req.MentionIDs {
			if !seen[id] {
				seen[id] = true
				uniqueMentions = append(uniqueMentions, id)
			}
		}
		if len(uniqueMentions) == 1 {
			// Single mention: route directly to that agent.
			mentionUUID, ok := parseUUIDOrBadRequest(w, uniqueMentions[0], "mention_id")
			if !ok {
				return
			}
			// Validate the mentioned agent is a participant.
			isParticipant := false
			parts, _ := h.Queries.ListChatSessionParticipantsBySessionIDs(r.Context(), []pgtype.UUID{session.ID})
			for _, p := range parts {
				if uuidToString(p.AgentID) == uniqueMentions[0] {
					isParticipant = true
					break
				}
			}
			if !isParticipant {
				writeError(w, http.StatusBadRequest, "mentioned agent is not a participant in this chat")
				return
			}
			targetAgentID = mentionUUID
		}
		// 0 or 2+ mentions: use orchestrator (session.AgentID for group = orchestrator).
	}

	// Enqueue a chat task after the message exists.
	var task db.AgentTaskQueue
	if targetAgentID != session.AgentID {
		task, err = h.TaskService.EnqueueChatTaskForAgent(r.Context(), session, targetAgentID)
	} else {
		task, err = h.TaskService.EnqueueChatTask(r.Context(), session)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to enqueue chat task: "+err.Error())
		return
	}

	// Touch session updated_at.
	if err := h.Queries.TouchChatSession(r.Context(), session.ID); err != nil {
		slog.Warn("failed to touch chat session", "session_id", sessionID, "error", err)
	}
	taskContext := h.TaskService.AnalyticsContextForTask(r.Context(), task)
	h.Analytics.Capture(analytics.ChatMessageSent(
		userID,
		workspaceID,
		uuidToString(session.ID),
		uuidToString(task.ID),
		uuidToString(session.AgentID),
		taskContext.RuntimeMode,
		taskContext.Provider,
	))

	// Broadcast the user message.
	resolvedSessionID := uuidToString(session.ID)
	h.publishChat(protocol.EventChatMessage, workspaceID, "member", userID, resolvedSessionID, protocol.ChatMessagePayload{
		ChatSessionID: resolvedSessionID,
		MessageID:     uuidToString(msg.ID),
		Role:          "user",
		Content:       req.Content,
		TaskID:        uuidToString(task.ID),
		CreatedAt:     timestampToString(msg.CreatedAt),
	})

	writeJSON(w, http.StatusCreated, SendChatMessageResponse{
		MessageID: uuidToString(msg.ID),
		TaskID:    uuidToString(task.ID),
		CreatedAt: timestampToString(task.CreatedAt),
	})
}

func (h *Handler) ListChatMessages(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	messages, err := h.Queries.ListChatMessages(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list chat messages")
		return
	}

	messageIDs := make([]pgtype.UUID, len(messages))
	for i, m := range messages {
		messageIDs[i] = m.ID
	}
	groupedAtt := h.groupChatMessageAttachments(r.Context(), workspaceID, messageIDs)

	resp := make([]ChatMessageResponse, len(messages))
	for i, m := range messages {
		resp[i] = chatMessageToResponse(m, groupedAtt[uuidToString(m.ID)])
	}
	writeJSON(w, http.StatusOK, resp)
}

// PendingChatTaskResponse is returned by GetPendingChatTask — either the
// current in-flight task's id/status, or an empty object when none is active.
// CreatedAt is the anchor the frontend uses to time the chat StatusPill
// (elapsed seconds = now - CreatedAt). It must come from the server because
// optimistic seeds don't have a real task created_at and the timer needs to
// survive refresh / reopen.
type PendingChatTaskResponse struct {
	TaskID    string `json:"task_id,omitempty"`
	Status    string `json:"status,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// MarkChatSessionRead clears the session's unread_since (→ has_unread=false)
// and broadcasts chat:session_read so other devices of the same user drop
// their badges.
func (h *Handler) MarkChatSessionRead(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	if err := h.Queries.MarkChatSessionRead(r.Context(), session.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark session read")
		return
	}

	resolvedSessionID := uuidToString(session.ID)
	h.publishChat(protocol.EventChatSessionRead, workspaceID, "member", userID, resolvedSessionID, protocol.ChatSessionReadPayload{
		ChatSessionID: resolvedSessionID,
	})

	w.WriteHeader(http.StatusNoContent)
}

// PendingChatTasksResponse is the aggregate view consumed by the FAB.
type PendingChatTasksResponse struct {
	Tasks []PendingChatTaskItem `json:"tasks"`
}

type PendingChatTaskItem struct {
	TaskID        string `json:"task_id"`
	Status        string `json:"status"`
	ChatSessionID string `json:"chat_session_id"`
}

// ListPendingChatTasks returns every in-flight chat task owned by the current
// user in this workspace. Drives the FAB's "running" indicator when the chat
// window is closed (no per-session query is subscribed). Tasks belonging to
// private agents the caller has lost access to are dropped from the response.
func (h *Handler) ListPendingChatTasks(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())

	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}

	rows, err := h.Queries.ListPendingChatTasksByCreator(r.Context(), db.ListPendingChatTasksByCreatorParams{
		WorkspaceID: parseUUID(workspaceID),
		CreatorID:   parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list pending chat tasks")
		return
	}

	// Map session → agent so we can filter without an N+1. The user's own
	// session list is small, so one extra query is cheaper than per-row
	// lookups.
	sessions, err := h.Queries.ListAllChatSessionsByCreator(r.Context(), db.ListAllChatSessionsByCreatorParams{
		WorkspaceID: parseUUID(workspaceID),
		CreatorID:   parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve chat session agents")
		return
	}
	sessionAgent := make(map[string]string, len(sessions))
	for _, s := range sessions {
		sessionAgent[uuidToString(s.ID)] = uuidToString(s.AgentID)
	}

	items := make([]PendingChatTaskItem, 0, len(rows))
	for _, row := range rows {
		sessionID := uuidToString(row.ChatSessionID)
		agentID, hasAgent := sessionAgent[sessionID]
		if !hasAgent {
			continue
		}
		if _, ok := allowed[agentID]; !ok {
			continue
		}
		items = append(items, PendingChatTaskItem{
			TaskID:        uuidToString(row.TaskID),
			Status:        row.Status,
			ChatSessionID: sessionID,
		})
	}
	writeJSON(w, http.StatusOK, PendingChatTasksResponse{Tasks: items})
}

// GetPendingChatTask returns the most recent in-flight task (queued / dispatched
// / running) for a chat session. The frontend polls this on mount / session
// switch so pending UI state survives refresh and reopen.
func (h *Handler) GetPendingChatTask(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	task, err := h.Queries.GetPendingChatTask(r.Context(), session.ID)
	if err != nil {
		// No in-flight task — return an empty object, not an error.
		writeJSON(w, http.StatusOK, PendingChatTaskResponse{})
		return
	}

	writeJSON(w, http.StatusOK, PendingChatTaskResponse{
		TaskID:    uuidToString(task.ID),
		Status:    task.Status,
		CreatedAt: timestampToString(task.CreatedAt),
	})
}

// ---------------------------------------------------------------------------
// Task cancellation (user-facing, with ownership check)
// ---------------------------------------------------------------------------

// CancelTaskByUser cancels a task after verifying the requesting user owns
// the associated chat session or issue within the current workspace.
func (h *Handler) CancelTaskByUser(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	taskID := chi.URLParam(r, "taskId")
	taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task id")
	if !ok {
		return
	}

	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	// Verify ownership: for chat tasks, check workspace + creator;
	// for issue tasks, verify the issue belongs to the current workspace.
	if task.ChatSessionID.Valid {
		cs, err := h.Queries.GetChatSessionInWorkspace(r.Context(), db.GetChatSessionInWorkspaceParams{
			ID:          task.ChatSessionID,
			WorkspaceID: parseUUID(workspaceID),
		})
		if err != nil {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		if uuidToString(cs.CreatorID) != userID {
			writeError(w, http.StatusForbidden, "not your task")
			return
		}
	} else if task.IssueID.Valid {
		issue, err := h.Queries.GetIssue(r.Context(), task.IssueID)
		if err != nil || uuidToString(issue.WorkspaceID) != workspaceID {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
	} else {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	cancelled, err := h.TaskService.CancelTask(r.Context(), taskUUID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, taskToResponse(*cancelled))
}

// ---------------------------------------------------------------------------
// Response types & helpers
// ---------------------------------------------------------------------------

type ChatSessionResponse struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	AgentID     string `json:"agent_id"`
	CreatorID   string `json:"creator_id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	HasUnread bool   `json:"has_unread"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	// IM view fields — populated when view=im or when creating group chats.
	Kind                string              `json:"kind,omitempty"`
	OrchestratorAgentID *string             `json:"orchestrator_agent_id,omitempty"`
	Participants        []ParticipantResponse `json:"participants,omitempty"`
	IsPinned            bool                `json:"is_pinned,omitempty"`
	ArchivedAt          *string             `json:"archived_at,omitempty"`
	LastMessagePreview  *string             `json:"last_message_preview,omitempty"`
	LastMessageAt       *string             `json:"last_message_at,omitempty"`
}

type ParticipantResponse struct {
	AgentID   string  `json:"agent_id"`
	Role      string  `json:"role"`
	Name      string  `json:"name,omitempty"`
	AvatarURL *string `json:"avatar_url,omitempty"`
}

type ChatMessageResponse struct {
	ID            string  `json:"id"`
	ChatSessionID string  `json:"chat_session_id"`
	Role          string  `json:"role"`
	Content       string  `json:"content"`
	TaskID        *string `json:"task_id"`
	CreatedAt     string  `json:"created_at"`
	FailureReason *string `json:"failure_reason"`
	ElapsedMs     *int64  `json:"elapsed_ms"`
	Attachments   []AttachmentResponse `json:"attachments,omitempty"`
	// New fields for group chat / message model.
	AgentID     *string                `json:"agent_id,omitempty"`
	MessageType string                 `json:"message_type,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

func chatSessionToResponse(s db.ChatSession) ChatSessionResponse {
	resp := ChatSessionResponse{
		ID:          uuidToString(s.ID),
		WorkspaceID: uuidToString(s.WorkspaceID),
		AgentID:     uuidToString(s.AgentID),
		CreatorID:   uuidToString(s.CreatorID),
		Title:       s.Title,
		Status:      s.Status,
		Kind:        s.Kind,
		CreatedAt:   timestampToString(s.CreatedAt),
		UpdatedAt:   timestampToString(s.UpdatedAt),
	}
	if s.OrchestratorAgentID.Valid {
		orchID := uuidToString(s.OrchestratorAgentID)
		resp.OrchestratorAgentID = &orchID
	}
	return resp
}

func chatMessageToResponse(m db.ChatMessage, attachments []AttachmentResponse) ChatMessageResponse {
	resp := ChatMessageResponse{
		ID:            uuidToString(m.ID),
		ChatSessionID: uuidToString(m.ChatSessionID),
		Role:          m.Role,
		Content:       m.Content,
		TaskID:        uuidToPtr(m.TaskID),
		CreatedAt:     timestampToString(m.CreatedAt),
		FailureReason: textToPtr(m.FailureReason),
		ElapsedMs:     int8ToPtr(m.ElapsedMs),
		Attachments:   attachments,
		MessageType:   m.MessageType,
	}
	if m.AgentID.Valid {
		aid := uuidToString(m.AgentID)
		resp.AgentID = &aid
	}
	// Parse metadata JSONB.
	if len(m.Metadata) > 0 {
		var meta map[string]interface{}
		if json.Unmarshal(m.Metadata, &meta) == nil {
			resp.Metadata = meta
		}
	}
	return resp
}

// ---------------------------------------------------------------------------
// User State: Pin / Archive / Read
// ---------------------------------------------------------------------------

func (h *Handler) PinChatSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	_, err := h.Queries.UpsertChatSessionUserState(r.Context(), db.UpsertChatSessionUserStateParams{
		ChatSessionID: session.ID,
		UserID:        parseUUID(userID),
		WorkspaceID:   parseUUID(workspaceID),
		PinnedAt:      pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to pin session")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) UnpinChatSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	if err := h.Queries.ClearChatSessionUserPinned(r.Context(), db.ClearChatSessionUserPinnedParams{
		ChatSessionID: session.ID,
		UserID:        parseUUID(userID),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to unpin session")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ArchiveChatSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	_, err := h.Queries.UpsertChatSessionUserState(r.Context(), db.UpsertChatSessionUserStateParams{
		ChatSessionID: session.ID,
		UserID:        parseUUID(userID),
		WorkspaceID:   parseUUID(workspaceID),
		ArchivedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to archive session")
		return
	}

	// Also set legacy chat_session.status for consistency.
	_ = h.Queries.UpdateChatSessionStatus(r.Context(), db.UpdateChatSessionStatusParams{
		ID:     session.ID,
		Status: "archived",
	})

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) UnarchiveChatSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	if err := h.Queries.ClearChatSessionUserArchived(r.Context(), db.ClearChatSessionUserArchivedParams{
		ChatSessionID: session.ID,
		UserID:        parseUUID(userID),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to unarchive session")
		return
	}

	// Also reset legacy chat_session.status to 'active' for consistency.
	// Without this, sessions archived via legacy PATCH would stay status='archived'
	// even after unarchive, causing them to appear in the IM list with wrong state.
	_ = h.Queries.UpdateChatSessionStatus(r.Context(), db.UpdateChatSessionStatusParams{
		ID:     session.ID,
		Status: "active",
	})

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Legacy archive transition sync
// ---------------------------------------------------------------------------

// UpdateChatSessionStatus updates the legacy status field on a chat session.
// This is a transition-period bridge for Desktop's PATCH {status: "archived"}
// path — once Desktop migrates to the new archive API, this can be removed.
func (h *Handler) UpdateChatSessionStatus(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	var req struct {
		Status *string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Status == nil || (*req.Status != "archived" && *req.Status != "active") {
		writeError(w, http.StatusBadRequest, "status must be 'archived' or 'active'")
		return
	}

	session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	if err := h.Queries.UpdateChatSessionStatus(r.Context(), db.UpdateChatSessionStatusParams{
		ID:     session.ID,
		Status: *req.Status,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update session status")
		return
	}

	// Transition sync: mirror legacy status changes into user_state.
	if *req.Status == "archived" {
		_, syncErr := h.Queries.UpsertChatSessionUserState(r.Context(), db.UpsertChatSessionUserStateParams{
			ChatSessionID: session.ID,
			UserID:        parseUUID(userID),
			WorkspaceID:   parseUUID(workspaceID),
			ArchivedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
		})
		if syncErr != nil {
			slog.Warn("transition sync: failed to write user_state.archived_at from legacy archive",
				"session_id", sessionID, "error", syncErr)
		}
	} else {
		syncErr := h.Queries.ClearChatSessionUserArchived(r.Context(), db.ClearChatSessionUserArchivedParams{
			ChatSessionID: session.ID,
			UserID:        parseUUID(userID),
		})
		if syncErr != nil {
			slog.Warn("transition sync: failed to clear user_state.archived_at from legacy unarchive",
				"session_id", sessionID, "error", syncErr)
		}
	}

	writeJSON(w, http.StatusOK, chatSessionToResponse(session))
}
