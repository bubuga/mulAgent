package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// withChatTestWorkspaceCtx injects the workspace+member context that the
// real chi middleware chain would normally set. SendChatMessage (and most
// other chat handlers) read workspace ID from ctxWorkspaceID; without this
// the test harness, which calls handlers directly, gets "invalid workspace
// id" on the parseUUIDOrBadRequest call inside SendChatMessage.
func withChatTestWorkspaceCtx(t *testing.T, req *http.Request) *http.Request {
	t.Helper()
	memberRow, err := testHandler.Queries.GetMemberByUserAndWorkspace(context.Background(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      util.MustParseUUID(testUserID),
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load test member row: %v", err)
	}
	return req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, memberRow))
}

// TestSendChatMessage_LinksAttachments verifies that attachments uploaded
// against a chat_session (chat_message_id NULL) are back-filled with the
// message_id when SendChatMessage receives the matching attachment_ids.
func TestSendChatMessage_LinksAttachments(t *testing.T) {
	origStorage := testHandler.Storage
	testHandler.Storage = &mockStorage{}
	defer func() { testHandler.Storage = origStorage }()

	agentID := createHandlerTestAgent(t, "ChatSendAttachAgent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)

	// 1. Upload a file against the chat session.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "send-link.png")
	part.Write([]byte("\x89PNG\r\n\x1a\nbytes"))
	writer.WriteField("chat_session_id", sessionID)
	writer.Close()

	uploadReq := httptest.NewRequest("POST", "/api/upload-file", &body)
	uploadReq.Header.Set("Content-Type", writer.FormDataContentType())
	uploadReq.Header.Set("X-User-ID", testUserID)
	uploadReq.Header.Set("X-Workspace-ID", testWorkspaceID)

	uploadW := httptest.NewRecorder()
	testHandler.UploadFile(uploadW, uploadReq)
	if uploadW.Code != http.StatusOK {
		t.Fatalf("upload precondition: %d %s", uploadW.Code, uploadW.Body.String())
	}
	var uploadResp AttachmentResponse
	if err := json.Unmarshal(uploadW.Body.Bytes(), &uploadResp); err != nil {
		t.Fatalf("decode upload: %v", err)
	}
	attachmentID := uploadResp.ID
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM attachment WHERE id = $1`, attachmentID)
	})

	// 2. Send a chat message that references the attachment.
	sendReq := newRequest("POST", "/api/chat-sessions/"+sessionID+"/messages", map[string]any{
		"content":        "look at this ![](" + uploadResp.URL + ")",
		"attachment_ids": []string{attachmentID},
	})
	sendReq = withURLParam(sendReq, "sessionId", sessionID)
	sendReq = withChatTestWorkspaceCtx(t, sendReq)
	sendW := httptest.NewRecorder()
	testHandler.SendChatMessage(sendW, sendReq)
	if sendW.Code != http.StatusCreated {
		t.Fatalf("SendChatMessage: expected 201, got %d: %s", sendW.Code, sendW.Body.String())
	}

	var sendResp SendChatMessageResponse
	if err := json.Unmarshal(sendW.Body.Bytes(), &sendResp); err != nil {
		t.Fatalf("decode send: %v", err)
	}
	if sendResp.MessageID == "" {
		t.Fatal("expected non-empty message_id in send response")
	}

	// 3. Verify the attachment row now points at the new message.
	var dbMessageID *string
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT chat_message_id::text FROM attachment WHERE id = $1`,
		attachmentID,
	).Scan(&dbMessageID); err != nil {
		t.Fatalf("query attachment: %v", err)
	}
	if dbMessageID == nil {
		t.Fatal("chat_message_id is still NULL after send")
	}
	if *dbMessageID != sendResp.MessageID {
		t.Fatalf("chat_message_id mismatch: want %s, got %s", sendResp.MessageID, *dbMessageID)
	}
}

// TestUpdateChatSession_RenamesTitle confirms PATCH writes the new title,
// returns the updated row, and the server-side row reflects it.
func TestUpdateChatSession_RenamesTitle(t *testing.T) {
	agentID := createHandlerTestAgent(t, "ChatRenameAgent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)

	req := newRequest("PATCH", "/api/chat/sessions/"+sessionID, map[string]any{
		"title": "  Renamed Session  ",
	})
	req = withURLParam(req, "sessionId", sessionID)
	req = withChatTestWorkspaceCtx(t, req)
	w := httptest.NewRecorder()
	testHandler.UpdateChatSession(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateChatSession: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ChatSessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if resp.Title != "Renamed Session" {
		t.Fatalf("response title: want %q, got %q", "Renamed Session", resp.Title)
	}

	var dbTitle string
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT title FROM chat_session WHERE id = $1`,
		sessionID,
	).Scan(&dbTitle); err != nil {
		t.Fatalf("query chat_session: %v", err)
	}
	if dbTitle != "Renamed Session" {
		t.Fatalf("db title: want %q, got %q", "Renamed Session", dbTitle)
	}
}

// TestUpdateChatSession_RejectsBlank refuses an empty/whitespace title with 400.
// (Untitled is a render-side fallback, not a stored value.)
func TestUpdateChatSession_RejectsBlank(t *testing.T) {
	agentID := createHandlerTestAgent(t, "ChatRenameBlankAgent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)

	req := newRequest("PATCH", "/api/chat/sessions/"+sessionID, map[string]any{
		"title": "   ",
	})
	req = withURLParam(req, "sessionId", sessionID)
	req = withChatTestWorkspaceCtx(t, req)
	w := httptest.NewRecorder()
	testHandler.UpdateChatSession(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("UpdateChatSession blank: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestSendChatMessage_InvalidAttachmentIDs rejects malformed UUIDs in
// attachment_ids with 400 before any side effects (no message row created).
func TestSendChatMessage_InvalidAttachmentIDs(t *testing.T) {
	agentID := createHandlerTestAgent(t, "ChatBadAttachAgent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)

	req := newRequest("POST", "/api/chat-sessions/"+sessionID+"/messages", map[string]any{
		"content":        "hi",
		"attachment_ids": []string{"not-a-uuid"},
	})
	req = withURLParam(req, "sessionId", sessionID)
	req = withChatTestWorkspaceCtx(t, req)
	w := httptest.NewRecorder()
	testHandler.SendChatMessage(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("SendChatMessage with bad attachment id: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// Confirm no message row was created.
	var count int
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM chat_message WHERE chat_session_id = $1`,
		sessionID,
	).Scan(&count); err != nil {
		t.Fatalf("count chat_message: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 chat_message rows after rejected send, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Plan test helpers
// ---------------------------------------------------------------------------

// createHandlerTestGroupChatSession creates a group chat session with an
// orchestrator and optional participant agents.
func createHandlerTestGroupChatSession(t *testing.T, orchestratorAgentID string, participantAgentIDs []string) string {
	t.Helper()

	var sessionID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, status, kind, orchestrator_agent_id)
		VALUES ($1, $2, $3, 'Plan Test Group Chat', 'active', 'group', $2)
		RETURNING id::text
	`, testWorkspaceID, orchestratorAgentID, testUserID).Scan(&sessionID); err != nil {
		t.Fatalf("create group chat session: %v", err)
	}

	// Add orchestrator as participant.
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO chat_session_agents (chat_session_id, agent_id, role)
		VALUES ($1, $2, 'participant')
	`, sessionID, orchestratorAgentID); err != nil {
		t.Fatalf("add orchestrator participant: %v", err)
	}

	for _, agentID := range participantAgentIDs {
		if _, err := testPool.Exec(context.Background(), `
			INSERT INTO chat_session_agents (chat_session_id, agent_id, role)
			VALUES ($1, $2, 'participant')
		`, sessionID, agentID); err != nil {
			t.Fatalf("add participant %s: %v", agentID, err)
		}
	}

	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, sessionID)
	})
	return sessionID
}

// createHandlerTestChatTask creates an agent_task_queue row linked to a chat session.
func createHandlerTestChatTask(t *testing.T, agentID, chatSessionID string) string {
	t.Helper()

	var taskID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, chat_session_id)
		VALUES ($1, $2, 'queued', 0, $3)
		RETURNING id::text
	`, agentID, handlerTestRuntimeID(t), chatSessionID).Scan(&taskID); err != nil {
		t.Fatalf("create chat task: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})
	return taskID
}

// planSubmitBody builds a plan submit request body.
func planSubmitBody(steps []map[string]any) map[string]any {
	return map[string]any{"steps": steps}
}

// countPlanSystemMessages counts plan-related system messages for a session.
func countPlanSystemMessages(t *testing.T, sessionID, messageType string) int {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM chat_message
		WHERE chat_session_id = $1 AND role = 'system' AND message_type = $2
	`, sessionID, messageType).Scan(&count); err != nil {
		t.Fatalf("count system messages: %v", err)
	}
	return count
}

// planStatusInDB returns the status of a plan.
func planStatusInDB(t *testing.T, planID string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(context.Background(), `
		SELECT status FROM chat_execution_plan WHERE id = $1
	`, planID).Scan(&status); err != nil {
		t.Fatalf("get plan status: %v", err)
	}
	return status
}

// getActivePlanID returns the active plan ID for a session, or empty string.
func getActivePlanID(t *testing.T, sessionID string) string {
	t.Helper()
	var planID string
	err := testPool.QueryRow(context.Background(), `
		SELECT id::text FROM chat_execution_plan
		WHERE chat_session_id = $1 AND status NOT IN ('completed', 'cancelled', 'failed')
		ORDER BY created_at DESC LIMIT 1
	`, sessionID).Scan(&planID)
	if err != nil {
		return ""
	}
	return planID
}

// setPlanStatusDirectly updates a plan's status directly in the DB (for test setup).
func setPlanStatusDirectly(t *testing.T, planID, status string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `
		UPDATE chat_execution_plan SET status = $2, updated_at = now() WHERE id = $1
	`, planID, status); err != nil {
		t.Fatalf("set plan status: %v", err)
	}
}

// submitPlanViaHandler submits a plan and returns the recorder + parsed response.
func submitPlanViaHandler(t *testing.T, sessionID, orchestratorID, taskID string, steps []map[string]any) (*httptest.ResponseRecorder, PlanResponse) {
	t.Helper()
	body := planSubmitBody(steps)
	req := newRequest("POST", "/api/chat/sessions/"+sessionID+"/plan", body)
	req.Header.Set("X-Agent-ID", orchestratorID)
	req.Header.Set("X-Task-ID", taskID)
	req = withURLParam(req, "sessionId", sessionID)
	req = withChatTestWorkspaceCtx(t, req)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", sessionID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	testHandler.SubmitPlan(w, req)

	var resp PlanResponse
	if w.Code == http.StatusCreated {
		json.Unmarshal(w.Body.Bytes(), &resp)
	}
	return w, resp
}

// ---------------------------------------------------------------------------
// SubmitPlan tests
// ---------------------------------------------------------------------------

func TestSubmitPlan_Success(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchOK", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerOK", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	taskID := createHandlerTestChatTask(t, orchestrator, sessionID)

	w, resp := submitPlanViaHandler(t, sessionID, orchestrator, taskID, []map[string]any{
		{"agent_id": worker, "prompt": "Create hello.py"},
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if resp.ID == "" {
		t.Fatal("expected non-empty plan ID")
	}
	if resp.Status != "awaiting_approval" {
		t.Fatalf("expected awaiting_approval, got %s", resp.Status)
	}
	if len(resp.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(resp.Steps))
	}
	if resp.Steps[0].Status != "awaiting_approval" {
		t.Fatalf("step 1: want awaiting_approval, got %s", resp.Steps[0].Status)
	}
	if count := countPlanSystemMessages(t, sessionID, "plan_created"); count != 1 {
		t.Fatalf("expected 1 plan_created message, got %d", count)
	}
}

func TestSubmitPlan_NotAgent_Forbidden(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchNoAgent", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerNoAgent", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})

	body := planSubmitBody([]map[string]any{{"agent_id": worker, "prompt": "Create hello.py"}})
	req := newRequest("POST", "/api/chat/sessions/"+sessionID+"/plan", body)
	req = withURLParam(req, "sessionId", sessionID)
	req = withChatTestWorkspaceCtx(t, req)
	w := httptest.NewRecorder()
	testHandler.SubmitPlan(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitPlan_WrongTask_Forbidden(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchWrongTask", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerWrongTask", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	wrongTaskID := createHandlerTestTaskForAgent(t, orchestrator)

	body := planSubmitBody([]map[string]any{{"agent_id": worker, "prompt": "Create hello.py"}})
	req := newRequest("POST", "/api/chat/sessions/"+sessionID+"/plan", body)
	req.Header.Set("X-Agent-ID", orchestrator)
	req.Header.Set("X-Task-ID", wrongTaskID)
	req = withURLParam(req, "sessionId", sessionID)
	req = withChatTestWorkspaceCtx(t, req)
	w := httptest.NewRecorder()
	testHandler.SubmitPlan(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitPlan_NotGroupChat_BadRequest(t *testing.T) {
	agent := createHandlerTestAgent(t, "PlanDirectAgent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agent)
	taskID := createHandlerTestChatTask(t, agent, sessionID)

	body := planSubmitBody([]map[string]any{{"agent_id": agent, "prompt": "Create hello.py"}})
	req := newRequest("POST", "/api/chat/sessions/"+sessionID+"/plan", body)
	req.Header.Set("X-Agent-ID", agent)
	req.Header.Set("X-Task-ID", taskID)
	req = withURLParam(req, "sessionId", sessionID)
	req = withChatTestWorkspaceCtx(t, req)
	w := httptest.NewRecorder()
	testHandler.SubmitPlan(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitPlan_InvalidAgent_BadRequest(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchInvalid", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerInvalid", []byte("[]"))
	outsider := createHandlerTestAgent(t, "PlanOutsider", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	taskID := createHandlerTestChatTask(t, orchestrator, sessionID)

	w, _ := submitPlanViaHandler(t, sessionID, orchestrator, taskID, []map[string]any{
		{"agent_id": outsider, "prompt": "Create hello.py"},
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitPlan_TooManySteps_BadRequest(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchTooMany", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerTooMany", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	taskID := createHandlerTestChatTask(t, orchestrator, sessionID)

	var steps []map[string]any
	for i := 0; i < 9; i++ {
		steps = append(steps, map[string]any{"agent_id": worker, "prompt": fmt.Sprintf("Step %d", i+1)})
	}
	w, _ := submitPlanViaHandler(t, sessionID, orchestrator, taskID, steps)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitPlan_ZeroSteps_BadRequest(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchZeroSteps", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerZeroSteps", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	taskID := createHandlerTestChatTask(t, orchestrator, sessionID)

	w, _ := submitPlanViaHandler(t, sessionID, orchestrator, taskID, []map[string]any{})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitPlan_EmptyPrompt_BadRequest(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchEmptyPrompt", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerEmptyPrompt", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	taskID := createHandlerTestChatTask(t, orchestrator, sessionID)

	w, _ := submitPlanViaHandler(t, sessionID, orchestrator, taskID, []map[string]any{
		{"agent_id": worker, "prompt": "   "},
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitPlan_ActivePlanConflict(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchConflict", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerConflict", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	taskID := createHandlerTestChatTask(t, orchestrator, sessionID)

	steps := []map[string]any{{"agent_id": worker, "prompt": "First plan"}}
	w1, _ := submitPlanViaHandler(t, sessionID, orchestrator, taskID, steps)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first submit: expected 201, got %d: %s", w1.Code, w1.Body.String())
	}

	// Second submit should get 409.
	w2, _ := submitPlanViaHandler(t, sessionID, orchestrator, taskID, []map[string]any{
		{"agent_id": worker, "prompt": "Second plan"},
	})
	if w2.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w2.Code, w2.Body.String())
	}
}

// ---------------------------------------------------------------------------
// dry_run tests (Task 3)
// ---------------------------------------------------------------------------

func TestSubmitPlan_DryRun_Orchestrator_Success(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchDryRun", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerDryRun", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	taskID := createHandlerTestChatTask(t, orchestrator, sessionID)

	body := planSubmitBody([]map[string]any{{"agent_id": worker, "prompt": "Dry run step"}})
	req := newRequest("POST", "/api/chat/sessions/"+sessionID+"/plan?dry_run=true", body)
	req.Header.Set("X-Agent-ID", orchestrator)
	req.Header.Set("X-Task-ID", taskID)
	req = withURLParam(req, "sessionId", sessionID)
	req = withChatTestWorkspaceCtx(t, req)
	w := httptest.NewRecorder()
	testHandler.SubmitPlan(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Valid     bool `json:"valid"`
		StepCount int  `json:"step_count"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Valid {
		t.Fatal("expected valid=true")
	}
	if resp.StepCount != 1 {
		t.Fatalf("expected 1 step, got %d", resp.StepCount)
	}

	// Verify nothing persisted.
	if planID := getActivePlanID(t, sessionID); planID != "" {
		t.Fatalf("dry run should not create plan, found %s", planID)
	}
	if count := countPlanSystemMessages(t, sessionID, "plan_created"); count != 0 {
		t.Fatalf("dry run should not create system messages, found %d", count)
	}
}

func TestSubmitPlan_DryRun_NotAgent_Forbidden(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchDryRunUser", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerDryRunUser", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})

	body := planSubmitBody([]map[string]any{{"agent_id": worker, "prompt": "Dry run step"}})
	req := newRequest("POST", "/api/chat/sessions/"+sessionID+"/plan?dry_run=true", body)
	req = withURLParam(req, "sessionId", sessionID)
	req = withChatTestWorkspaceCtx(t, req)
	w := httptest.NewRecorder()
	testHandler.SubmitPlan(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitPlan_DryRun_NonOrchestratorAgent_Forbidden(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchDryRunOther", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerDryRunOther", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	workerTaskID := createHandlerTestChatTask(t, worker, sessionID)

	body := planSubmitBody([]map[string]any{{"agent_id": worker, "prompt": "Dry run step"}})
	req := newRequest("POST", "/api/chat/sessions/"+sessionID+"/plan?dry_run=true", body)
	req.Header.Set("X-Agent-ID", worker)
	req.Header.Set("X-Task-ID", workerTaskID)
	req = withURLParam(req, "sessionId", sessionID)
	req = withChatTestWorkspaceCtx(t, req)
	w := httptest.NewRecorder()
	testHandler.SubmitPlan(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// TestSubmitPlan_DryRun_InvalidAgent_BadRequest verifies that dry_run uses the
// same validation as real submit — an invalid agent still returns 400.
func TestSubmitPlan_DryRun_InvalidAgent_BadRequest(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchDryRunInvalid", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerDryRunInvalid", []byte("[]"))
	outsider := createHandlerTestAgent(t, "PlanOutsiderDryRun", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	taskID := createHandlerTestChatTask(t, orchestrator, sessionID)

	body := planSubmitBody([]map[string]any{{"agent_id": outsider, "prompt": "Dry run with invalid agent"}})
	req := newRequest("POST", "/api/chat/sessions/"+sessionID+"/plan?dry_run=true", body)
	req.Header.Set("X-Agent-ID", orchestrator)
	req.Header.Set("X-Task-ID", taskID)
	req = withURLParam(req, "sessionId", sessionID)
	req = withChatTestWorkspaceCtx(t, req)
	w := httptest.NewRecorder()
	testHandler.SubmitPlan(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestSubmitPlan_DryRun_EmptyPrompt_BadRequest verifies dry_run rejects empty prompts.
func TestSubmitPlan_DryRun_EmptyPrompt_BadRequest(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchDryRunEmpty", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerDryRunEmpty", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	taskID := createHandlerTestChatTask(t, orchestrator, sessionID)

	body := planSubmitBody([]map[string]any{{"agent_id": worker, "prompt": "   "}})
	req := newRequest("POST", "/api/chat/sessions/"+sessionID+"/plan?dry_run=true", body)
	req.Header.Set("X-Agent-ID", orchestrator)
	req.Header.Set("X-Task-ID", taskID)
	req = withURLParam(req, "sessionId", sessionID)
	req = withChatTestWorkspaceCtx(t, req)
	w := httptest.NewRecorder()
	testHandler.SubmitPlan(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// System message metadata assertions
// ---------------------------------------------------------------------------

// TestSubmitPlan_SystemMessageMetadata verifies plan_created system message
// contains correct metadata.plan_id and metadata.step_count.
func TestSubmitPlan_SystemMessageMetadata(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchMeta", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerMeta", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	taskID := createHandlerTestChatTask(t, orchestrator, sessionID)

	_, resp := submitPlanViaHandler(t, sessionID, orchestrator, taskID, []map[string]any{
		{"agent_id": worker, "prompt": "Step one"},
		{"agent_id": worker, "prompt": "Step two"},
	})
	if resp.ID == "" {
		t.Fatal("setup: plan not created")
	}

	// Query the system message metadata directly.
	var metadata []byte
	if err := testPool.QueryRow(context.Background(), `
		SELECT metadata::text FROM chat_message
		WHERE chat_session_id = $1 AND role = 'system' AND message_type = 'plan_created'
		ORDER BY created_at DESC LIMIT 1
	`, sessionID).Scan(&metadata); err != nil {
		t.Fatalf("query system message: %v", err)
	}

	var meta map[string]interface{}
	if err := json.Unmarshal(metadata, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if meta["plan_id"] != resp.ID {
		t.Fatalf("metadata.plan_id: want %s, got %v", resp.ID, meta["plan_id"])
	}
	if meta["step_count"].(float64) != 2 {
		t.Fatalf("metadata.step_count: want 2, got %v", meta["step_count"])
	}
}

// TestClearPlan_SystemMessageMetadata verifies plan_cancelled system message
// contains correct metadata.plan_id.
func TestClearPlan_SystemMessageMetadata(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchClearMeta", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerClearMeta", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	taskID := createHandlerTestChatTask(t, orchestrator, sessionID)

	_, resp := submitPlanViaHandler(t, sessionID, orchestrator, taskID, []map[string]any{
		{"agent_id": worker, "prompt": "Step"},
	})
	if resp.ID == "" {
		t.Fatal("setup: plan not created")
	}

	// Clear the plan.
	clearReq := newRequest("DELETE", "/api/chat/sessions/"+sessionID+"/plan", nil)
	clearReq = withURLParam(clearReq, "sessionId", sessionID)
	clearReq = withChatTestWorkspaceCtx(t, clearReq)
	clearW := httptest.NewRecorder()
	testHandler.ClearPlan(clearW, clearReq)
	if clearW.Code != http.StatusNoContent {
		t.Fatalf("clear: expected 204, got %d", clearW.Code)
	}

	// Query the cancel system message metadata.
	var metadata []byte
	if err := testPool.QueryRow(context.Background(), `
		SELECT metadata::text FROM chat_message
		WHERE chat_session_id = $1 AND role = 'system' AND message_type = 'plan_cancelled'
		ORDER BY created_at DESC LIMIT 1
	`, sessionID).Scan(&metadata); err != nil {
		t.Fatalf("query system message: %v", err)
	}

	var meta map[string]interface{}
	if err := json.Unmarshal(metadata, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if meta["plan_id"] != resp.ID {
		t.Fatalf("metadata.plan_id: want %s, got %v", resp.ID, meta["plan_id"])
	}
}

// ---------------------------------------------------------------------------
// WS event verification
// ---------------------------------------------------------------------------

// TestSubmitPlan_Events verifies that submit publishes chat:plan_created and
// chat:step_awaiting_approval events with correct payloads.
func TestSubmitPlan_Events(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchEvents", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerEvents", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	taskID := createHandlerTestChatTask(t, orchestrator, sessionID)

	var planCreatedEvent, stepEvent events.Event
	planCreatedCh := make(chan events.Event, 1)
	stepCh := make(chan events.Event, 1)

	testHandler.Bus.Subscribe(protocol.EventChatPlanCreated, func(e events.Event) {
		select {
		case planCreatedCh <- e:
		default:
		}
	})
	testHandler.Bus.Subscribe(protocol.EventChatStepAwaitingApproval, func(e events.Event) {
		select {
		case stepCh <- e:
		default:
		}
	})

	submitPlanViaHandler(t, sessionID, orchestrator, taskID, []map[string]any{
		{"agent_id": worker, "prompt": "Step one"},
	})

	// Wait for events with timeout.
	select {
	case planCreatedEvent = <-planCreatedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for chat:plan_created event")
	}
	select {
	case stepEvent = <-stepCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for chat:step_awaiting_approval event")
	}

	// Verify plan_created payload.
	payload, ok := planCreatedEvent.Payload.(protocol.ChatPlanPayload)
	if !ok {
		t.Fatalf("plan_created payload type: %T", planCreatedEvent.Payload)
	}
	if payload.ChatSessionID != sessionID {
		t.Fatalf("plan_created ChatSessionID: want %s, got %s", sessionID, payload.ChatSessionID)
	}
	if payload.PlanID == "" {
		t.Fatal("plan_created PlanID is empty")
	}
	if payload.Status != "awaiting_approval" {
		t.Fatalf("plan_created Status: want awaiting_approval, got %s", payload.Status)
	}
	if payload.StepCount != 1 {
		t.Fatalf("plan_created StepCount: want 1, got %d", payload.StepCount)
	}

	// Verify step_awaiting_approval payload.
	stepPayload, ok := stepEvent.Payload.(protocol.ChatStepPayload)
	if !ok {
		t.Fatalf("step payload type: %T", stepEvent.Payload)
	}
	if stepPayload.ChatSessionID != sessionID {
		t.Fatalf("step ChatSessionID: want %s, got %s", sessionID, stepPayload.ChatSessionID)
	}
	if stepPayload.PlanID != payload.PlanID {
		t.Fatalf("step PlanID mismatch: %s vs %s", stepPayload.PlanID, payload.PlanID)
	}
	if stepPayload.StepID == "" {
		t.Fatal("step StepID is empty")
	}
	if stepPayload.Sequence != 1 {
		t.Fatalf("step Sequence: want 1, got %d", stepPayload.Sequence)
	}
	if stepPayload.Status != "awaiting_approval" {
		t.Fatalf("step Status: want awaiting_approval, got %s", stepPayload.Status)
	}
}

// TestClearPlan_Events verifies that clear publishes chat:plan_cancelled event.
func TestClearPlan_Events(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchClearEvt", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerClearEvt", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	taskID := createHandlerTestChatTask(t, orchestrator, sessionID)

	submitPlanViaHandler(t, sessionID, orchestrator, taskID, []map[string]any{
		{"agent_id": worker, "prompt": "Step"},
	})

	cancelledCh := make(chan events.Event, 1)
	testHandler.Bus.Subscribe(protocol.EventChatPlanCancelled, func(e events.Event) {
		select {
		case cancelledCh <- e:
		default:
		}
	})

	clearReq := newRequest("DELETE", "/api/chat/sessions/"+sessionID+"/plan", nil)
	clearReq = withURLParam(clearReq, "sessionId", sessionID)
	clearReq = withChatTestWorkspaceCtx(t, clearReq)
	clearW := httptest.NewRecorder()
	testHandler.ClearPlan(clearW, clearReq)
	if clearW.Code != http.StatusNoContent {
		t.Fatalf("clear: expected 204, got %d", clearW.Code)
	}

	var cancelEvent events.Event
	select {
	case cancelEvent = <-cancelledCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for chat:plan_cancelled event")
	}

	payload, ok := cancelEvent.Payload.(protocol.ChatPlanPayload)
	if !ok {
		t.Fatalf("plan_cancelled payload type: %T", cancelEvent.Payload)
	}
	if payload.ChatSessionID != sessionID {
		t.Fatalf("plan_cancelled ChatSessionID: want %s, got %s", sessionID, payload.ChatSessionID)
	}
	if payload.PlanID == "" {
		t.Fatal("plan_cancelled PlanID is empty")
	}
	if payload.Status != "cancelled" {
		t.Fatalf("plan_cancelled Status: want cancelled, got %s", payload.Status)
	}
}

// ---------------------------------------------------------------------------
// Active plan status matrix tests (Task 4)
// ---------------------------------------------------------------------------

func TestSubmitPlan_ActivePlanMatrix(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchMatrix", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerMatrix", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	taskID := createHandlerTestChatTask(t, orchestrator, sessionID)

	activeStatuses := []string{"awaiting_approval", "running"}
	for _, status := range activeStatuses {
		t.Run("active_"+status+"_returns_409", func(t *testing.T) {
			// Insert a plan directly with the target status.
			var planID string
			if err := testPool.QueryRow(context.Background(), `
				INSERT INTO chat_execution_plan (chat_session_id, orchestrator_agent_id, status, execution_mode)
				VALUES ($1, $2, $3, 'serial')
				RETURNING id::text
			`, sessionID, orchestrator, status).Scan(&planID); err != nil {
				t.Fatalf("insert plan: %v", err)
			}
			t.Cleanup(func() {
				testPool.Exec(context.Background(), `DELETE FROM chat_execution_plan WHERE id = $1`, planID)
			})

			w, _ := submitPlanViaHandler(t, sessionID, orchestrator, taskID, []map[string]any{
				{"agent_id": worker, "prompt": "Should fail"},
			})
			if w.Code != http.StatusConflict {
				t.Fatalf("status %s: expected 409, got %d: %s", status, w.Code, w.Body.String())
			}
		})
	}

	terminalStatuses := []string{"cancelled", "completed", "failed"}
	for _, status := range terminalStatuses {
		t.Run("terminal_"+status+"_allows_submit", func(t *testing.T) {
			// Insert a plan with terminal status.
			var planID string
			if err := testPool.QueryRow(context.Background(), `
				INSERT INTO chat_execution_plan (chat_session_id, orchestrator_agent_id, status, execution_mode)
				VALUES ($1, $2, $3, 'serial')
				RETURNING id::text
			`, sessionID, orchestrator, status).Scan(&planID); err != nil {
				t.Fatalf("insert plan: %v", err)
			}
			t.Cleanup(func() {
				testPool.Exec(context.Background(), `DELETE FROM chat_execution_plan WHERE id = $1`, planID)
			})

			w, resp := submitPlanViaHandler(t, sessionID, orchestrator, taskID, []map[string]any{
				{"agent_id": worker, "prompt": "Should succeed"},
			})
			if w.Code != http.StatusCreated {
				t.Fatalf("status %s: expected 201, got %d: %s", status, w.Code, w.Body.String())
			}
			// Clean up the newly created plan.
			if resp.ID != "" {
				testPool.Exec(context.Background(), `DELETE FROM chat_execution_plan WHERE id = $1`, resp.ID)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ClearPlan tests (Task 2)
// ---------------------------------------------------------------------------

func TestClearPlan_Success_AwaitingApproval(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchClearOK", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerClearOK", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	taskID := createHandlerTestChatTask(t, orchestrator, sessionID)

	_, resp := submitPlanViaHandler(t, sessionID, orchestrator, taskID, []map[string]any{
		{"agent_id": worker, "prompt": "Step to cancel"},
	})
	if resp.ID == "" {
		t.Fatal("setup: plan not created")
	}

	// Clear as session creator (user).
	clearReq := newRequest("DELETE", "/api/chat/sessions/"+sessionID+"/plan", nil)
	clearReq = withURLParam(clearReq, "sessionId", sessionID)
	clearReq = withChatTestWorkspaceCtx(t, clearReq)
	clearW := httptest.NewRecorder()
	testHandler.ClearPlan(clearW, clearReq)

	if clearW.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", clearW.Code, clearW.Body.String())
	}
	if status := planStatusInDB(t, resp.ID); status != "cancelled" {
		t.Fatalf("expected cancelled, got %s", status)
	}
	if count := countPlanSystemMessages(t, sessionID, "plan_cancelled"); count != 1 {
		t.Fatalf("expected 1 plan_cancelled message, got %d", count)
	}
}

func TestClearPlan_OrchestratorAgent_Success(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchClearAgent", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerClearAgent", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	taskID := createHandlerTestChatTask(t, orchestrator, sessionID)

	submitPlanViaHandler(t, sessionID, orchestrator, taskID, []map[string]any{
		{"agent_id": worker, "prompt": "Step"},
	})

	clearReq := newRequest("DELETE", "/api/chat/sessions/"+sessionID+"/plan", nil)
	clearReq.Header.Set("X-Agent-ID", orchestrator)
	clearReq.Header.Set("X-Task-ID", taskID)
	clearReq = withURLParam(clearReq, "sessionId", sessionID)
	clearReq = withChatTestWorkspaceCtx(t, clearReq)
	clearW := httptest.NewRecorder()
	testHandler.ClearPlan(clearW, clearReq)

	if clearW.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", clearW.Code, clearW.Body.String())
	}
}

func TestClearPlan_NonOrchestratorAgent_Forbidden(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchClearAuthz", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerClearAuthz", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	taskID := createHandlerTestChatTask(t, orchestrator, sessionID)

	submitPlanViaHandler(t, sessionID, orchestrator, taskID, []map[string]any{
		{"agent_id": worker, "prompt": "Step"},
	})

	workerTaskID := createHandlerTestChatTask(t, worker, sessionID)
	clearReq := newRequest("DELETE", "/api/chat/sessions/"+sessionID+"/plan", nil)
	clearReq.Header.Set("X-Agent-ID", worker)
	clearReq.Header.Set("X-Task-ID", workerTaskID)
	clearReq = withURLParam(clearReq, "sessionId", sessionID)
	clearReq = withChatTestWorkspaceCtx(t, clearReq)
	clearW := httptest.NewRecorder()
	testHandler.ClearPlan(clearW, clearReq)

	if clearW.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", clearW.Code, clearW.Body.String())
	}
}

func TestClearPlan_NoActivePlan_NotFound(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchClearNone", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, nil)

	clearReq := newRequest("DELETE", "/api/chat/sessions/"+sessionID+"/plan", nil)
	clearReq = withURLParam(clearReq, "sessionId", sessionID)
	clearReq = withChatTestWorkspaceCtx(t, clearReq)
	clearW := httptest.NewRecorder()
	testHandler.ClearPlan(clearW, clearReq)

	if clearW.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", clearW.Code, clearW.Body.String())
	}
}

// TestClearPlan_OnlyAwaitingApproval verifies that queued/running plans cannot
// be cancelled (Task 2).
func TestClearPlan_OnlyAwaitingApproval(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchClearRestrict", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerClearRestrict", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})

	nonCancellable := []string{"running"}
	for _, status := range nonCancellable {
		t.Run("status_"+status+"_rejected", func(t *testing.T) {
			// Insert a plan directly with the target status.
			var planID string
			if err := testPool.QueryRow(context.Background(), `
				INSERT INTO chat_execution_plan (chat_session_id, orchestrator_agent_id, status, execution_mode)
				VALUES ($1, $2, $3, 'serial')
				RETURNING id::text
			`, sessionID, orchestrator, status).Scan(&planID); err != nil {
				t.Fatalf("insert plan: %v", err)
			}
			t.Cleanup(func() {
				testPool.Exec(context.Background(), `DELETE FROM chat_execution_plan WHERE id = $1`, planID)
			})

			clearReq := newRequest("DELETE", "/api/chat/sessions/"+sessionID+"/plan", nil)
			clearReq = withURLParam(clearReq, "sessionId", sessionID)
			clearReq = withChatTestWorkspaceCtx(t, clearReq)
			clearW := httptest.NewRecorder()
			testHandler.ClearPlan(clearW, clearReq)

			if clearW.Code != http.StatusConflict {
				t.Fatalf("status %s: expected 409, got %d: %s", status, clearW.Code, clearW.Body.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GetPlan tests
// ---------------------------------------------------------------------------

func TestGetPlan_Success(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchGet", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerGet", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	taskID := createHandlerTestChatTask(t, orchestrator, sessionID)

	_, resp := submitPlanViaHandler(t, sessionID, orchestrator, taskID, []map[string]any{
		{"agent_id": worker, "prompt": "Create app.py"},
	})
	if resp.ID == "" {
		t.Fatal("setup: plan not created")
	}

	getReq := newRequest("GET", "/api/chat/plans/"+resp.ID, nil)
	getReq = withURLParam(getReq, "planId", resp.ID)
	getReq = withChatTestWorkspaceCtx(t, getReq)
	getW := httptest.NewRecorder()
	testHandler.GetPlan(getW, getReq)

	if getW.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", getW.Code, getW.Body.String())
	}

	var getResp PlanResponse
	json.Unmarshal(getW.Body.Bytes(), &getResp)
	if getResp.ID != resp.ID {
		t.Fatalf("plan ID mismatch: %s vs %s", getResp.ID, resp.ID)
	}
	if len(getResp.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(getResp.Steps))
	}
	if getResp.Steps[0].AgentName == "" {
		t.Fatal("expected agent_name to be populated")
	}
}

func TestGetPlan_WrongWorkspace_NotFound(t *testing.T) {
	orchestrator := createHandlerTestAgent(t, "PlanOrchGetWs", []byte("[]"))
	worker := createHandlerTestAgent(t, "PlanWorkerGetWs", []byte("[]"))
	sessionID := createHandlerTestGroupChatSession(t, orchestrator, []string{worker})
	taskID := createHandlerTestChatTask(t, orchestrator, sessionID)

	_, resp := submitPlanViaHandler(t, sessionID, orchestrator, taskID, []map[string]any{
		{"agent_id": worker, "prompt": "Step"},
	})
	if resp.ID == "" {
		t.Fatal("setup: plan not created")
	}

	// Use a different workspace ID in the context to simulate cross-workspace access.
	getReq := newRequest("GET", "/api/chat/plans/"+resp.ID, nil)
	getReq = withURLParam(getReq, "planId", resp.ID)
	fakeWorkspaceID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	getReq = getReq.WithContext(middleware.SetMemberContext(getReq.Context(), fakeWorkspaceID, db.Member{}))
	getW := httptest.NewRecorder()
	testHandler.GetPlan(getW, getReq)

	if getW.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", getW.Code, getW.Body.String())
	}
}
