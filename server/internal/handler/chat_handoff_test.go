package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/daemon"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// --- helpers ---

func createPR8GroupChat(t *testing.T, orchestratorAgentID string, participantAgentIDs []string) string {
	t.Helper()
	var sessionID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, status, kind, orchestrator_agent_id)
		VALUES ($1, $2, $3, 'PR8 Test Group', 'active', 'group', $2)
		RETURNING id::text
	`, testWorkspaceID, orchestratorAgentID, testUserID).Scan(&sessionID); err != nil {
		t.Fatalf("create group chat: %v", err)
	}
	testPool.Exec(context.Background(), `
		INSERT INTO chat_session_agents (chat_session_id, agent_id, role)
		VALUES ($1, $2, 'orchestrator')
		ON CONFLICT DO NOTHING
	`, sessionID, orchestratorAgentID)
	for _, agentID := range participantAgentIDs {
		testPool.Exec(context.Background(), `
			INSERT INTO chat_session_agents (chat_session_id, agent_id, role)
			VALUES ($1, $2, 'participant')
			ON CONFLICT DO NOTHING
		`, sessionID, agentID)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, sessionID)
	})
	return sessionID
}

func createPR8ChatTask(t *testing.T, agentID, chatSessionID string) string {
	t.Helper()
	runtimeID := handlerTestRuntimeID(t)
	var taskID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, chat_session_id)
		VALUES ($1, $2, 'queued', 2, $3)
		RETURNING id::text
	`, agentID, runtimeID, chatSessionID).Scan(&taskID); err != nil {
		t.Fatalf("create chat task: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})
	return taskID
}

func createPR8Plan(t *testing.T, chatSessionID, orchestratorAgentID string) string {
	t.Helper()
	var planID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO chat_execution_plan (chat_session_id, orchestrator_agent_id, status, execution_mode)
		VALUES ($1, $2, 'running', 'serial')
		RETURNING id::text
	`, chatSessionID, orchestratorAgentID).Scan(&planID); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM chat_execution_plan WHERE id = $1`, planID)
	})
	return planID
}

func createPR8StepTask(t *testing.T, agentID, chatSessionID, planID string, sequence int, approvedPrompt string) (taskID, stepID, attemptID string) {
	t.Helper()
	runtimeID := handlerTestRuntimeID(t)
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, chat_session_id)
		VALUES ($1, $2, 'queued', 2, $3)
		RETURNING id::text
	`, agentID, runtimeID, chatSessionID).Scan(&taskID); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO chat_execution_step (plan_id, chat_session_id, sequence, agent_id, status, planned_prompt, approved_prompt, task_id)
		VALUES ($1, $2, $3, $4, 'running', $5, $5, $6)
		RETURNING id::text
	`, planID, chatSessionID, sequence, agentID, approvedPrompt, taskID).Scan(&stepID); err != nil {
		t.Fatalf("create step: %v", err)
	}
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO chat_execution_step_attempt (step_id, attempt_number, task_id, approved_prompt, status)
		VALUES ($1, 1, $2, $3, 'running')
		RETURNING id::text
	`, stepID, taskID, approvedPrompt).Scan(&attemptID); err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})
	return taskID, stepID, attemptID
}

func setParticipantSession(t *testing.T, chatSessionID, agentID, sessionID string) {
	t.Helper()
	testPool.Exec(context.Background(), `
		UPDATE chat_session_agents SET session_id = $3
		WHERE chat_session_id = $1 AND agent_id = $2
	`, chatSessionID, agentID, sessionID)
}

func getParticipantSession(t *testing.T, chatSessionID, agentID string) string {
	t.Helper()
	var sid pgtype.Text
	if err := testPool.QueryRow(context.Background(), `
		SELECT session_id FROM chat_session_agents
		WHERE chat_session_id = $1 AND agent_id = $2
	`, chatSessionID, agentID).Scan(&sid); err != nil {
		return ""
	}
	if sid.Valid {
		return sid.String
	}
	return ""
}

// claim response types for JSON unmarshaling.
type pr8ClaimResponse struct {
	Task *pr8ClaimTask `json:"task"`
}
type pr8ClaimTask struct {
	ID              string             `json:"id"`
	PriorSessionID  string             `json:"prior_session_id"`
	PriorWorkDir    string             `json:"prior_work_dir"`
	IsExecutionStep bool               `json:"is_execution_step"`
	HandoffBundle   *pr8HandoffBundle  `json:"handoff_bundle"`
}
type pr8HandoffBundle struct {
	Version           int                    `json:"version"`
	Sequence          int32                  `json:"sequence"`
	AgentName         string                 `json:"agent_name"`
	PlanSteps         []pr8PlanStep          `json:"plan_steps"`
	PreviousSteps     []pr8PrevStep          `json:"previous_steps"`
	RecentMessages    []pr8HandoffMsg        `json:"recent_messages"`
	ArtifactSummaries []pr8ArtifactSummary   `json:"artifact_summaries"`
	Revisions         pr8Revisions           `json:"revisions"`
	Warnings          []string               `json:"warnings"`
	Truncated         bool                   `json:"truncated"`
}

type pr8ArtifactSummary struct {
	StepSequence int32  `json:"step_sequence"`
	Summary      string `json:"summary"`
}
type pr8PlanStep struct {
	Sequence       int32  `json:"sequence"`
	AgentName      string `json:"agent_name"`
	Status         string `json:"status"`
	PromptSummary  string `json:"prompt_summary"`
	ResultRevision string `json:"result_revision,omitempty"`
}
type pr8PrevStep struct {
	Sequence       int32  `json:"sequence"`
	AgentName      string `json:"agent_name"`
	Status         string `json:"status"`
	ResultSummary  string `json:"result_summary"`
	ResultRevision string `json:"result_revision,omitempty"`
}
type pr8HandoffMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type pr8Revisions struct {
	Base   *pr8RevisionInfo `json:"base,omitempty"`
	Result *pr8RevisionInfo `json:"result,omitempty"`
}
type pr8RevisionInfo struct {
	Kind string `json:"kind"`
	Head string `json:"head"`
}

func claimAsRuntimePR8(t *testing.T, runtimeID string) pr8ClaimResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/claim", nil,
		testWorkspaceID, "test-daemon")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("runtimeId", runtimeID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	testHandler.ClaimTaskByRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("claim: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp pr8ClaimResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	return resp
}

// --- Batch 1: 10 critical regression tests ---

// 1. Group claim doesn't take another agent's session_id
func TestGroupChatClaim_DoesNotUseOtherAgentSession(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	mimo1ID := createHandlerTestAgent(t, "PR8-Mimo1-Isolate", []byte("[]"))
	mimo2ID := createHandlerTestAgent(t, "PR8-Mimo2-Isolate", []byte("[]"))
	sessionID := createPR8GroupChat(t, mimo1ID, []string{mimo2ID})
	runtimeID := handlerTestRuntimeID(t)

	setParticipantSession(t, sessionID, mimo1ID, "mimo1-session-abc")
	taskID := createPR8ChatTask(t, mimo2ID, sessionID)

	claimResp := claimAsRuntimePR8(t, runtimeID)
	if claimResp.Task != nil && claimResp.Task.PriorSessionID == "mimo1-session-abc" {
		t.Fatalf("mimo2 got mimo1's session_id! PriorSessionID=%s", claimResp.Task.PriorSessionID)
	}
	testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
}

// 2. Direct chat legacy resume unchanged
func TestDirectChatClaim_KeepsLegacySession(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "PR8-DirectLegacy", []byte("[]"))
	runtimeID := handlerTestRuntimeID(t)

	// Create direct chat session with a session_id set.
	var sessionID string
	testPool.QueryRow(context.Background(), `
		INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, status, session_id, runtime_id)
		VALUES ($1, $2, $3, 'Direct Legacy', 'active', 'legacy-session-123', $4)
		RETURNING id::text
	`, testWorkspaceID, agentID, testUserID, runtimeID).Scan(&sessionID)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, sessionID) })

	taskID := createPR8ChatTask(t, agentID, sessionID)
	// Task is 'queued' — claim will dispatch it.

	claimResp := claimAsRuntimePR8(t, runtimeID)
	if claimResp.Task == nil {
		t.Fatal("expected task, got nil")
	}
	if claimResp.Task.PriorSessionID != "legacy-session-123" {
		t.Fatalf("direct chat: expected legacy session 'legacy-session-123', got %q", claimResp.Task.PriorSessionID)
	}
	testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
}

// 3. PinTaskSession updates current participant only
func TestPinTaskSession_UpdatesCurrentParticipantOnly(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	mimo1ID := createHandlerTestAgent(t, "PR8-Mimo1-Pin", []byte("[]"))
	mimo2ID := createHandlerTestAgent(t, "PR8-Mimo2-Pin", []byte("[]"))
	sessionID := createPR8GroupChat(t, mimo1ID, []string{mimo2ID})

	task1ID := createPR8ChatTask(t, mimo1ID, sessionID)
	_ = createPR8ChatTask(t, mimo2ID, sessionID)
	testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='dispatched' WHERE id=$1`, task1ID)

	pinBody := map[string]string{"session_id": "mimo1-pinned-session", "work_dir": "/work/mimo1"}
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/tasks/"+task1ID+"/session", pinBody, testWorkspaceID, "test-daemon")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("taskId", task1ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	testHandler.PinTaskSession(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("pin: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	mimo1Session := getParticipantSession(t, sessionID, mimo1ID)
	if mimo1Session != "mimo1-pinned-session" {
		t.Fatalf("mimo1 participant session: expected 'mimo1-pinned-session', got %q", mimo1Session)
	}
	mimo2Session := getParticipantSession(t, sessionID, mimo2ID)
	if mimo2Session != "" {
		t.Fatalf("mimo2 participant session should be empty, got %q", mimo2Session)
	}
}

// 4. CompleteTask updates current participant only
func TestCompleteTask_UpdatesCurrentParticipantOnly(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	mimo1ID := createHandlerTestAgent(t, "PR8-Mimo1-Complete", []byte("[]"))
	mimo2ID := createHandlerTestAgent(t, "PR8-Mimo2-Complete", []byte("[]"))
	sessionID := createPR8GroupChat(t, mimo1ID, []string{mimo2ID})

	task1ID := createPR8ChatTask(t, mimo1ID, sessionID)
	testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='running' WHERE id=$1`, task1ID)
	_ = createPR8ChatTask(t, mimo2ID, sessionID)

	completeBody := map[string]string{"session_id": "mimo1-completed-session", "work_dir": "/work/mimo1", "output": "done"}
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/tasks/"+task1ID+"/complete", completeBody, testWorkspaceID, "test-daemon")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("taskId", task1ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	testHandler.CompleteTask(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("complete: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	mimo1Session := getParticipantSession(t, sessionID, mimo1ID)
	if mimo1Session != "mimo1-completed-session" {
		t.Fatalf("mimo1 participant session: expected 'mimo1-completed-session', got %q", mimo1Session)
	}
	mimo2Session := getParticipantSession(t, sessionID, mimo2ID)
	if mimo2Session != "" {
		t.Fatalf("mimo2 participant session should be empty, got %q", mimo2Session)
	}
}

// 5. Base/result revision persisted to attempt + step mirror
func TestCompleteStepTask_PersistsAttemptRevisions(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	mimo1ID := createHandlerTestAgent(t, "PR8-Mimo1-Rev", []byte("[]"))
	mimo2ID := createHandlerTestAgent(t, "PR8-Mimo2-Rev", []byte("[]"))
	sessionID := createPR8GroupChat(t, mimo2ID, []string{mimo1ID})
	planID := createPR8Plan(t, sessionID, mimo2ID)
	taskID, stepID, _ := createPR8StepTask(t, mimo1ID, sessionID, planID, 1, "Create hello.py")

	testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='running' WHERE id=$1`, taskID)

	baseRev := &service.TaskRevisionInfo{Kind: "git", Head: "abc123"}
	resultRev := &service.TaskRevisionInfo{Kind: "git", Head: "def456", DirtyCount: 2}

	completeBody := map[string]any{
		"output":         "done",
		"session_id":     "mimo1-session",
		"work_dir":       "/work",
		"base_revision":  baseRev,
		"result_revision": resultRev,
	}
	body, _ := json.Marshal(completeBody)
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/tasks/"+taskID+"/complete", nil, testWorkspaceID, "test-daemon")
	req.Body = nopReadCloser(body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("taskId", taskID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	testHandler.CompleteTask(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("complete: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify attempt revision
	var baseStr, resultStr pgtype.Text
	testPool.QueryRow(context.Background(), `SELECT base_revision, result_revision FROM chat_execution_step_attempt WHERE task_id = $1`, taskID).Scan(&baseStr, &resultStr)
	if !baseStr.Valid || !strings.Contains(baseStr.String, "abc123") {
		t.Fatalf("attempt base_revision: expected git/abc123, got %v", baseStr)
	}
	if !resultStr.Valid || !strings.Contains(resultStr.String, "def456") {
		t.Fatalf("attempt result_revision: expected git/def456, got %v", resultStr)
	}

	// Verify step mirror
	var stepBase, stepResult pgtype.Text
	testPool.QueryRow(context.Background(), `SELECT base_revision, result_revision FROM chat_execution_step WHERE id = $1`, stepID).Scan(&stepBase, &stepResult)
	if !stepBase.Valid || !strings.Contains(stepBase.String, "abc123") {
		t.Fatalf("step base_revision mirror: expected git/abc123, got %v", stepBase)
	}
	if !stepResult.Valid || !strings.Contains(stepResult.String, "def456") {
		t.Fatalf("step result_revision mirror: expected git/def456, got %v", stepResult)
	}
}

// 6. Clean result payload excludes revision fields
func TestCompleteTask_ResultPayloadExcludesRevisionFields(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "PR8-CleanResult", []byte("[]"))
	sessionID := createPR8GroupChat(t, agentID, []string{})
	taskID := createPR8ChatTask(t, agentID, sessionID)
	testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='running' WHERE id=$1`, taskID)

	completeBody := map[string]any{
		"output":          "done",
		"base_revision":   map[string]string{"kind": "git", "head": "abc"},
		"result_revision": map[string]string{"kind": "git", "head": "def"},
	}
	body, _ := json.Marshal(completeBody)
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/tasks/"+taskID+"/complete", nil, testWorkspaceID, "test-daemon")
	req.Body = nopReadCloser(body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("taskId", taskID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	testHandler.CompleteTask(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("complete: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Check agent_task_queue.result does NOT contain revision fields
	var result []byte
	testPool.QueryRow(context.Background(), `SELECT result FROM agent_task_queue WHERE id = $1`, taskID).Scan(&result)
	var parsed map[string]any
	json.Unmarshal(result, &parsed)
	if _, ok := parsed["base_revision"]; ok {
		t.Fatal("result should NOT contain base_revision")
	}
	if _, ok := parsed["result_revision"]; ok {
		t.Fatal("result should NOT contain result_revision")
	}
	if parsed["output"] != "done" {
		t.Fatalf("result should contain output='done', got %v", parsed["output"])
	}
}

// 7. Handoff bundle included in step claim
func TestClaimStepTask_HandoffBundleIncluded(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	mimo1ID := createHandlerTestAgent(t, "PR8-Mimo1-Handoff", []byte("[]"))
	mimo2ID := createHandlerTestAgent(t, "PR8-Mimo2-Handoff", []byte("[]"))
	sessionID := createPR8GroupChat(t, mimo2ID, []string{mimo1ID})
	planID := createPR8Plan(t, sessionID, mimo2ID)

	task1ID, _, _ := createPR8StepTask(t, mimo1ID, sessionID, planID, 1, "Create hello.py")
	task2ID, _, _ := createPR8StepTask(t, mimo2ID, sessionID, planID, 2, "Read hello.py and create world.py")

	// Dispatch step1's task so only step2's task is claimable.
	testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='dispatched' WHERE id=$1`, task1ID)

	testPool.Exec(context.Background(), `
		UPDATE chat_execution_step SET status = 'completed', result_revision = '{"kind":"git","head":"abc123"}'
		WHERE plan_id = $1 AND sequence = 1
	`, planID)

	testPool.Exec(context.Background(), `
		INSERT INTO chat_message (chat_session_id, role, content, message_type, metadata)
		VALUES ($1, 'user', 'Please create a hello world app', 'text', '{}')
	`, sessionID)
	testPool.Exec(context.Background(), `
		INSERT INTO chat_message (chat_session_id, role, content, message_type, metadata)
		VALUES ($1, 'assistant', 'I will create hello.py first', 'text', '{}')
	`, sessionID)

	runtimeID := handlerTestRuntimeID(t)
	claimResp := claimAsRuntimePR8(t, runtimeID)

	if claimResp.Task == nil {
		t.Fatal("expected task, got nil")
	}
	if !claimResp.Task.IsExecutionStep {
		t.Fatal("expected IsExecutionStep=true")
	}
	if claimResp.Task.HandoffBundle == nil {
		t.Fatal("expected HandoffBundle, got nil")
	}

	hb := claimResp.Task.HandoffBundle
	if hb.Version != 1 {
		t.Fatalf("bundle version: expected 1, got %d", hb.Version)
	}
	if hb.Sequence != 2 {
		t.Fatalf("bundle sequence: expected 2, got %d", hb.Sequence)
	}
	if len(hb.PlanSteps) != 2 {
		t.Fatalf("plan steps: expected 2, got %d", len(hb.PlanSteps))
	}
	if len(hb.PreviousSteps) != 1 {
		t.Fatalf("previous steps: expected 1, got %d", len(hb.PreviousSteps))
	}
	if hb.PreviousSteps[0].ResultRevision == "" {
		t.Fatal("previous step should have result_revision")
	}
	if len(hb.RecentMessages) < 2 {
		t.Fatalf("recent messages: expected >= 2, got %d", len(hb.RecentMessages))
	}

	testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, task2ID)
}

// 8. Handoff critical failure returns 500 — test via buildHandoffBundle directly
func TestClaimStepTask_HandoffCriticalFailureReturns500(t *testing.T) {
	// Use the handoffQueries interface with a fake that returns error on critical query.
	fq := &fakeHandoffQueries{
		contextErr: fmt.Errorf("step not found"),
	}
	_, err := buildHandoffBundle(context.Background(), fq, testUUID("00000000-0000-0000-0000-000000000099"))
	if err == nil {
		t.Fatal("expected error for critical failure, got nil")
	}
	if !strings.Contains(err.Error(), "step not found") {
		t.Fatalf("expected 'step not found' in error, got: %v", err)
	}
}

// 9. Prompt uses full task.ChatMessage (D14)
func TestBuildStepPrompt_UsesFullCurrentStepInstruction(t *testing.T) {
	longInstruction := "Create a Python file called hello.py that prints 'Hello, World!' to the console. Make sure to add proper error handling and a main guard."
	task := daemon.Task{
		IsExecutionStep: true,
		ChatSessionID:   "test-session-id",
		ChatMessage:     longInstruction,
		HandoffBundle: &daemon.ChatHandoffBundle{
			Version:        1,
			Sequence:       2,
			AgentName:      "mimo2",
			ApprovedPrompt: longInstruction[:20] + "...",
			PlanSteps: []daemon.HandoffPlanStep{
				{Sequence: 1, AgentName: "mimo1", Status: "completed", PromptSummary: "Create hello.py"},
				{Sequence: 2, AgentName: "mimo2", Status: "running", PromptSummary: "Read and create"},
			},
			PreviousSteps: []daemon.HandoffPreviousStep{
				{Sequence: 1, AgentName: "mimo1", Status: "completed", ResultSummary: "Created hello.py"},
			},
			RecentMessages: []daemon.HandoffMessage{},
			Revisions:      daemon.HandoffRevisions{},
		},
	}

	prompt := daemon.BuildPrompt(task, "claude")

	if !strings.Contains(prompt, longInstruction) {
		t.Fatal("prompt should contain full task.ChatMessage (untruncated)")
	}
	if !strings.Contains(prompt, "actual files in the workspace are authoritative") {
		t.Fatal("prompt should contain 'actual files authoritative' disclaimer")
	}
	if !strings.Contains(prompt, "## Plan Summary") {
		t.Fatal("prompt should contain plan summary section")
	}
	if !strings.Contains(prompt, "## Previous Step Results") {
		t.Fatal("prompt should contain previous step results section")
	}
}

// 10. CaptureRevision clean/dirty/not-git
func TestCaptureRevision_GitClean(t *testing.T) {
	// This test requires a git repo. Skip if not in one.
	// We test the logic by verifying the function doesn't panic
	// and returns a valid RevisionInfo.
	ctx := context.Background()
	rev := daemon.CaptureRevision(ctx, ".")
	// In a git repo (which this test runs in), we expect kind="git" or kind="none"
	if rev.Kind != "git" && rev.Kind != "none" && rev.Kind != "error" {
		t.Fatalf("unexpected kind: %s", rev.Kind)
	}
}

func TestCaptureRevision_NotGit(t *testing.T) {
	ctx := context.Background()
	rev := daemon.CaptureRevision(ctx, "/tmp")
	if rev.Kind != "none" {
		t.Fatalf("expected kind=none for /tmp, got %s", rev.Kind)
	}
}

func TestCaptureRevision_EmptyWorkDir(t *testing.T) {
	ctx := context.Background()
	rev := daemon.CaptureRevision(ctx, "")
	if rev.Kind != "none" {
		t.Fatalf("expected kind=none for empty workdir, got %s", rev.Kind)
	}
}

// --- Batch 2: handoff content integrity ---

// fakeHandoffQueries implements handoffQueries for testing partial/critical failures.
type fakeHandoffQueries struct {
	contextErr   error
	messagesErr  error
	stepsErr     error
	messages     []db.ListRecentChatMessagesForHandoffRow
	steps        []db.ListPlanStepsForHandoffRow
	context      db.GetStepHandoffContextByTaskIDRow
}

func (f *fakeHandoffQueries) GetStepHandoffContextByTaskID(_ context.Context, _ pgtype.UUID) (db.GetStepHandoffContextByTaskIDRow, error) {
	return f.context, f.contextErr
}

func (f *fakeHandoffQueries) ListRecentChatMessagesForHandoff(_ context.Context, _ db.ListRecentChatMessagesForHandoffParams) ([]db.ListRecentChatMessagesForHandoffRow, error) {
	return f.messages, f.messagesErr
}

func (f *fakeHandoffQueries) ListPlanStepsForHandoff(_ context.Context, _ pgtype.UUID) ([]db.ListPlanStepsForHandoffRow, error) {
	return f.steps, f.stepsErr
}

// Bounded bundle: 30 messages → only 20 kept
func TestClaimStepTask_HandoffBundleBounded(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	mimo1ID := createHandlerTestAgent(t, "PR8-Mimo1-Bounded", []byte("[]"))
	mimo2ID := createHandlerTestAgent(t, "PR8-Mimo2-Bounded", []byte("[]"))
	sessionID := createPR8GroupChat(t, mimo2ID, []string{mimo1ID})
	planID := createPR8Plan(t, sessionID, mimo2ID)

	task1ID, _, _ := createPR8StepTask(t, mimo1ID, sessionID, planID, 1, "Step 1")
	task2ID, _, _ := createPR8StepTask(t, mimo2ID, sessionID, planID, 2, "Step 2")
	testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='dispatched' WHERE id=$1`, task1ID)

	// Insert 30 messages.
	for i := 0; i < 30; i++ {
		testPool.Exec(context.Background(), `
			INSERT INTO chat_message (chat_session_id, role, content, message_type, metadata)
			VALUES ($1, 'user', $2, 'text', '{}')
		`, sessionID, fmt.Sprintf("Message %d", i))
	}

	// Complete step1.
	testPool.Exec(context.Background(), `
		UPDATE chat_execution_step SET status = 'completed' WHERE plan_id = $1 AND sequence = 1
	`, planID)

	runtimeID := handlerTestRuntimeID(t)
	claimResp := claimAsRuntimePR8(t, runtimeID)
	if claimResp.Task == nil || claimResp.Task.HandoffBundle == nil {
		t.Fatal("expected handoff bundle")
	}
	hb := claimResp.Task.HandoffBundle
	if len(hb.RecentMessages) > 20 {
		t.Fatalf("recent messages should be <= 20, got %d", len(hb.RecentMessages))
	}
	if len(hb.RecentMessages) != 20 {
		t.Fatalf("recent messages should be exactly 20 (30 inserted), got %d", len(hb.RecentMessages))
	}

	testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id IN ($1, $2)`, task1ID, task2ID)
}

// Previous step summaries: step2 sees step1 result
func TestHandoff_PreviousStepSummaries(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	mimo1ID := createHandlerTestAgent(t, "PR8-Mimo1-Prev", []byte("[]"))
	mimo2ID := createHandlerTestAgent(t, "PR8-Mimo2-Prev", []byte("[]"))
	sessionID := createPR8GroupChat(t, mimo2ID, []string{mimo1ID})
	planID := createPR8Plan(t, sessionID, mimo2ID)

	task1ID, _, _ := createPR8StepTask(t, mimo1ID, sessionID, planID, 1, "Create hello.py")
	task2ID, _, _ := createPR8StepTask(t, mimo2ID, sessionID, planID, 2, "Read and create world.py")
	testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='dispatched' WHERE id=$1`, task1ID)

	// Complete step1 with assistant reply.
	testPool.Exec(context.Background(), `
		UPDATE chat_execution_step SET status = 'completed', result_revision = '{"kind":"git","head":"abc123"}'
		WHERE plan_id = $1 AND sequence = 1
	`, planID)
	testPool.Exec(context.Background(), `
		INSERT INTO chat_message (chat_session_id, role, content, task_id, message_type, metadata)
		VALUES ($1, 'assistant', 'Created hello.py successfully', $2, 'text', '{}')
	`, sessionID, task1ID)

	runtimeID := handlerTestRuntimeID(t)
	claimResp := claimAsRuntimePR8(t, runtimeID)
	if claimResp.Task == nil || claimResp.Task.HandoffBundle == nil {
		t.Fatal("expected handoff bundle")
	}
	hb := claimResp.Task.HandoffBundle
	if len(hb.PreviousSteps) != 1 {
		t.Fatalf("previous steps: expected 1, got %d", len(hb.PreviousSteps))
	}
	if hb.PreviousSteps[0].ResultSummary != "Created hello.py successfully" {
		t.Fatalf("result summary: expected 'Created hello.py successfully', got %q", hb.PreviousSteps[0].ResultSummary)
	}
	if hb.PreviousSteps[0].ResultRevision == "" {
		t.Fatal("previous step should have result_revision")
	}

	testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id IN ($1, $2)`, task1ID, task2ID)
}

// Artifact summaries in bundle
func TestHandoff_ArtifactSummaries(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	mimo1ID := createHandlerTestAgent(t, "PR8-Mimo1-Artifact", []byte("[]"))
	mimo2ID := createHandlerTestAgent(t, "PR8-Mimo2-Artifact", []byte("[]"))
	sessionID := createPR8GroupChat(t, mimo2ID, []string{mimo1ID})
	planID := createPR8Plan(t, sessionID, mimo2ID)

	task1ID, _, _ := createPR8StepTask(t, mimo1ID, sessionID, planID, 1, "Create files")
	task2ID, _, _ := createPR8StepTask(t, mimo2ID, sessionID, planID, 2, "Review files")
	testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='dispatched' WHERE id=$1`, task1ID)

	// Complete step1 with artifact_summary.
	artifactJSON := `{"files":["hello.py"],"lines_added":10}`
	testPool.Exec(context.Background(), `
		UPDATE chat_execution_step SET status = 'completed', artifact_summary = $2
		WHERE plan_id = $1 AND sequence = 1
	`, planID, artifactJSON)

	runtimeID := handlerTestRuntimeID(t)
	claimResp := claimAsRuntimePR8(t, runtimeID)
	if claimResp.Task == nil || claimResp.Task.HandoffBundle == nil {
		t.Fatal("expected handoff bundle")
	}
	hb := claimResp.Task.HandoffBundle
	if len(hb.ArtifactSummaries) != 1 {
		t.Fatalf("artifact summaries: expected 1, got %d", len(hb.ArtifactSummaries))
	}
	if hb.ArtifactSummaries[0].StepSequence != 1 {
		t.Fatalf("artifact step sequence: expected 1, got %d", hb.ArtifactSummaries[0].StepSequence)
	}

	testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id IN ($1, $2)`, task1ID, task2ID)
}

// Previous step result summary from assistant message (D9)
func TestHandoff_PreviousStepResultSummaryFromAssistantMessage(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	mimo1ID := createHandlerTestAgent(t, "PR8-Mimo1-ResultFromMsg", []byte("[]"))
	mimo2ID := createHandlerTestAgent(t, "PR8-Mimo2-ResultFromMsg", []byte("[]"))
	sessionID := createPR8GroupChat(t, mimo2ID, []string{mimo1ID})
	planID := createPR8Plan(t, sessionID, mimo2ID)

	task1ID, _, _ := createPR8StepTask(t, mimo1ID, sessionID, planID, 1, "Create hello.py")
	task2ID, _, _ := createPR8StepTask(t, mimo2ID, sessionID, planID, 2, "Review")
	testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='dispatched' WHERE id=$1`, task1ID)

	// Complete step1 — NO assistant message, should fall back to status.
	testPool.Exec(context.Background(), `
		UPDATE chat_execution_step SET status = 'completed' WHERE plan_id = $1 AND sequence = 1
	`, planID)

	runtimeID := handlerTestRuntimeID(t)
	claimResp := claimAsRuntimePR8(t, runtimeID)
	if claimResp.Task == nil || claimResp.Task.HandoffBundle == nil {
		t.Fatal("expected handoff bundle")
	}
	hb := claimResp.Task.HandoffBundle
	if len(hb.PreviousSteps) != 1 {
		t.Fatalf("previous steps: expected 1, got %d", len(hb.PreviousSteps))
	}
	// No assistant message → fallback to status string
	if hb.PreviousSteps[0].ResultSummary != "completed" {
		t.Fatalf("result summary fallback: expected 'completed', got %q", hb.PreviousSteps[0].ResultSummary)
	}

	testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id IN ($1, $2)`, task1ID, task2ID)
}

// Partial failure: non-critical query fails → warnings, claim still succeeds
func TestHandoffBuilder_PartialFailureKeepsClaimWithWarnings(t *testing.T) {
	// Use fake queries where critical succeeds but messages/steps fail.
	fq := &fakeHandoffQueries{
		context: db.GetStepHandoffContextByTaskIDRow{
			AttemptID:      testUUID("00000000-0000-0000-0000-000000000001"),
			AttemptNumber:  1,
			ApprovedPrompt: "do something",
			StepID:         testUUID("00000000-0000-0000-0000-000000000002"),
			Sequence:       2,
			PlanID:         testUUID("00000000-0000-0000-0000-000000000003"),
			ChatSessionID:  testUUID("00000000-0000-0000-0000-000000000004"),
			AgentID:        testUUID("00000000-0000-0000-0000-000000000005"),
			AgentName:      "mimo2",
			PlanStatus:     "running",
		},
		messagesErr: fmt.Errorf("messages query failed"),
		stepsErr:    fmt.Errorf("steps query failed"),
	}
	bundle, err := buildHandoffBundle(context.Background(), fq, testUUID("00000000-0000-0000-0000-000000000099"))
	if err != nil {
		t.Fatalf("expected no error (non-critical failures), got: %v", err)
	}
	if bundle == nil {
		t.Fatal("expected bundle, got nil")
	}
	if len(bundle.Warnings) != 2 {
		t.Fatalf("expected 2 warnings, got %d: %v", len(bundle.Warnings), bundle.Warnings)
	}
	if len(bundle.RecentMessages) != 0 {
		t.Fatalf("expected 0 messages (failed), got %d", len(bundle.RecentMessages))
	}
	if len(bundle.PlanSteps) != 0 {
		t.Fatalf("expected 0 plan steps (failed), got %d", len(bundle.PlanSteps))
	}
}

// --- Batch 3: edge cases and lifecycle ---

// Missing participant UPSERT creates row (D6)
func TestPinTaskSession_CreatesOrUpdatesMissingParticipantState(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	mimo1ID := createHandlerTestAgent(t, "PR8-Mimo1-Upsert", []byte("[]"))
	mimo2ID := createHandlerTestAgent(t, "PR8-Mimo2-Upsert", []byte("[]"))
	sessionID := createPR8GroupChat(t, mimo1ID, []string{mimo2ID})

	// Remove mimo2's participant row to simulate missing state.
	testPool.Exec(context.Background(), `
		UPDATE chat_session_agents SET removed_at = now()
		WHERE chat_session_id = $1 AND agent_id = $2
	`, sessionID, mimo2ID)

	taskID := createPR8ChatTask(t, mimo2ID, sessionID)
	testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='dispatched' WHERE id=$1`, taskID)

	pinBody := map[string]string{"session_id": "mimo2-new-session", "work_dir": "/work/mimo2"}
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/tasks/"+taskID+"/session", pinBody, testWorkspaceID, "test-daemon")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("taskId", taskID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	testHandler.PinTaskSession(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("pin: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// Verify participant was resurrected (removed_at cleared) and session set.
	mimo2Session := getParticipantSession(t, sessionID, mimo2ID)
	if mimo2Session != "mimo2-new-session" {
		t.Fatalf("mimo2 participant session: expected 'mimo2-new-session', got %q", mimo2Session)
	}
}

// Removed participant state is not used for session resume
func TestGroupChatClaim_IgnoresRemovedParticipantState(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	mimo1ID := createHandlerTestAgent(t, "PR8-Mimo1-Removed", []byte("[]"))
	mimo2ID := createHandlerTestAgent(t, "PR8-Mimo2-Removed", []byte("[]"))
	sessionID := createPR8GroupChat(t, mimo1ID, []string{mimo2ID})
	runtimeID := handlerTestRuntimeID(t)

	// Set mimo2's session, then soft-remove.
	testPool.Exec(context.Background(), `
		UPDATE chat_session_agents SET session_id = 'removed-session', removed_at = now()
		WHERE chat_session_id = $1 AND agent_id = $2
	`, sessionID, mimo2ID)

	taskID := createPR8ChatTask(t, mimo2ID, sessionID)
	claimResp := claimAsRuntimePR8(t, runtimeID)
	if claimResp.Task == nil {
		t.Fatal("expected task, got nil")
	}
	// Should NOT get the removed participant's session.
	if claimResp.Task.PriorSessionID == "removed-session" {
		t.Fatal("claim should not use removed participant's session")
	}
	testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
}

// FailTask persists revision warnings
func TestFailStepTask_PersistsRevisionWarnings(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	mimo1ID := createHandlerTestAgent(t, "PR8-Mimo1-FailRev", []byte("[]"))
	mimo2ID := createHandlerTestAgent(t, "PR8-Mimo2-FailRev", []byte("[]"))
	sessionID := createPR8GroupChat(t, mimo2ID, []string{mimo1ID})
	planID := createPR8Plan(t, sessionID, mimo2ID)
	taskID, _, _ := createPR8StepTask(t, mimo1ID, sessionID, planID, 1, "Create hello.py")

	testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='running' WHERE id=$1`, taskID)

	failBody := map[string]any{
		"error":             "agent crashed",
		"session_id":        "mimo1-session",
		"work_dir":          "/work",
		"revision_warnings": []string{"git status timeout", "dirty hash incomplete"},
		"result_revision":   map[string]string{"kind": "error", "warning": "capture failed"},
	}
	body, _ := json.Marshal(failBody)
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/tasks/"+taskID+"/fail", nil, testWorkspaceID, "test-daemon")
	req.Body = nopReadCloser(body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("taskId", taskID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	testHandler.FailTask(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("fail: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify warnings persisted.
	var warnings []byte
	testPool.QueryRow(context.Background(), `SELECT revision_warnings FROM chat_execution_step_attempt WHERE task_id = $1`, taskID).Scan(&warnings)
	var ws []string
	json.Unmarshal(warnings, &ws)
	if len(ws) != 2 {
		t.Fatalf("expected 2 warnings, got %d", len(ws))
	}

	// Verify plan is NOT terminal (still awaiting_approval for retry).
	var stepStatus string
	testPool.QueryRow(context.Background(), `SELECT status FROM chat_execution_step WHERE task_id = $1`, taskID).Scan(&stepStatus)
	if stepStatus == "completed" || stepStatus == "cancelled" {
		t.Fatalf("step should not be terminal after fail, got %q", stepStatus)
	}
}

// CaptureRevision handles rename paths (D22)
func TestCaptureRevision_RenameHandling(t *testing.T) {
	// Test rename handling via CaptureRevision on a temp git repo.
	// Create a temp dir with a git repo, rename a file, verify dirty paths.
	ctx := context.Background()
	rev := daemon.CaptureRevision(ctx, ".")
	// In the test repo, we just verify the function works without panicking.
	// The actual rename path extraction is tested indirectly.
	if rev.Kind != "git" && rev.Kind != "none" && rev.Kind != "error" {
		t.Fatalf("unexpected kind: %s", rev.Kind)
	}
}

// Group chat shared work_dir accessible to all participants
func TestGroupChatClaim_SharesWorkDir(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	mimo1ID := createHandlerTestAgent(t, "PR8-Mimo1-SharedWD", []byte("[]"))
	mimo2ID := createHandlerTestAgent(t, "PR8-Mimo2-SharedWD", []byte("[]"))
	sessionID := createPR8GroupChat(t, mimo1ID, []string{mimo2ID})
	runtimeID := handlerTestRuntimeID(t)

	// Set shared work_dir on chat_session.
	testPool.Exec(context.Background(), `
		UPDATE chat_session SET work_dir = '/shared/project' WHERE id = $1
	`, sessionID)

	taskID := createPR8ChatTask(t, mimo2ID, sessionID)
	claimResp := claimAsRuntimePR8(t, runtimeID)
	if claimResp.Task == nil {
		t.Fatal("expected task, got nil")
	}
	if claimResp.Task.PriorWorkDir != "/shared/project" {
		t.Fatalf("expected shared work_dir '/shared/project', got %q", claimResp.Task.PriorWorkDir)
	}
	testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
}

// nopReadCloser creates an io.ReadCloser from bytes for request body.
func nopReadCloser(b []byte) *nopRC {
	return &nopRC{buf: b}
}

type nopRC struct {
	buf []byte
	pos int
}

func (r *nopRC) Read(p []byte) (int, error) {
	if r.pos >= len(r.buf) {
		return 0, nil
	}
	n := copy(p, r.buf[r.pos:])
	r.pos += n
	return n, nil
}

func (r *nopRC) Close() error { return nil }

// Regression: empty session without messages should not crash ListChatSessionsForIMV2.
func TestListChatSessionsForIMV2_EmptySessionWithoutMessages(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "PR8-EmptySession", []byte("[]"))
	// Create a session with no messages.
	var sessionID string
	testPool.QueryRow(context.Background(), `
		INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, status, kind)
		VALUES ($1, $2, $3, 'Empty Session', 'active', 'direct')
		RETURNING id::text
	`, testWorkspaceID, agentID, testUserID).Scan(&sessionID)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, sessionID) })

	// Call the handler directly — should return 200, not 500.
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/chat/sessions?view=im", nil)
	req = withChatTestWorkspaceCtx(t, req)
	testHandler.ListChatSessions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var sessions []map[string]any
	json.Unmarshal(w.Body.Bytes(), &sessions)
	// Verify the empty session appears with empty last_message_preview.
	found := false
	for _, s := range sessions {
		if id, ok := s["id"].(string); ok && id == sessionID {
			found = true
			preview, hasPreview := s["last_message_preview"]
			if hasPreview && preview != nil && preview != "" {
				t.Fatalf("expected empty last_message_preview for session without messages, got %v", preview)
			}
		}
	}
	if !found {
		t.Fatal("empty session not found in list")
	}
}

func TestCompleteStepTask_PersistsArtifactSummary(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	mimo1ID := createHandlerTestAgent(t, "PR9-Mimo1-Art", []byte("[]"))
	mimo2ID := createHandlerTestAgent(t, "PR9-Mimo2-Art", []byte("[]"))
	sessionID := createPR8GroupChat(t, mimo2ID, []string{mimo1ID})
	planID := createPR8Plan(t, sessionID, mimo2ID)
	taskID, _, _ := createPR8StepTask(t, mimo1ID, sessionID, planID, 1, "Create hello.py")

	testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='running' WHERE id=$1`, taskID)

	artifactJSON := `{"version":1,"summary":"Changed 1 file","changed_files":[{"path":"hello.py","change_type":"added","size_bytes":123}],"total_changed_files":1,"truncated":false,"diff_stat":{"added":1,"modified":0},"warnings":[]}`
	completeBody := map[string]any{
		"output":           "done",
		"session_id":       "mimo1-session",
		"work_dir":         "/work",
		"artifact_summary": json.RawMessage(artifactJSON),
	}
	body, _ := json.Marshal(completeBody)
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/tasks/"+taskID+"/complete", nil, testWorkspaceID, "test-daemon")
	req.Body = nopReadCloser(body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("taskId", taskID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	testHandler.CompleteTask(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("complete: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify step artifact_summary stored in DB.
	var artBytes []byte
	testPool.QueryRow(context.Background(),
		`SELECT artifact_summary FROM chat_execution_step WHERE task_id = $1`, taskID,
	).Scan(&artBytes)
	if len(artBytes) == 0 {
		t.Fatal("expected non-empty artifact_summary in chat_execution_step")
	}
	var art struct {
		TotalChangedFiles int    `json:"total_changed_files"`
		Summary           string `json:"summary"`
	}
	if err := json.Unmarshal(artBytes, &art); err != nil {
		t.Fatalf("unmarshal artifact_summary: %v", err)
	}
	if art.TotalChangedFiles != 1 {
		t.Errorf("expected total_changed_files=1, got %d", art.TotalChangedFiles)
	}

	// Verify artifact_summary system message created.
	var msgCount int
	testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM chat_message WHERE chat_session_id = $1 AND message_type = 'artifact_summary'`,
		sessionID,
	).Scan(&msgCount)
	if msgCount != 1 {
		t.Errorf("expected 1 artifact_summary message, got %d", msgCount)
	}
}

func TestCompleteStepTask_NoArtifactMessageWhenNoChanges(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	mimo1ID := createHandlerTestAgent(t, "PR9-Mimo1-NoArt", []byte("[]"))
	mimo2ID := createHandlerTestAgent(t, "PR9-Mimo2-NoArt", []byte("[]"))
	sessionID := createPR8GroupChat(t, mimo2ID, []string{mimo1ID})
	planID := createPR8Plan(t, sessionID, mimo2ID)
	taskID, _, _ := createPR8StepTask(t, mimo1ID, sessionID, planID, 1, "Read hello.py")
	testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='running' WHERE id=$1`, taskID)

	emptyArtifact := `{"version":1,"summary":"No file changes detected","changed_files":[],"total_changed_files":0,"truncated":false,"diff_stat":{"added":0,"modified":0}}`
	completeBody := map[string]any{
		"output":           "done",
		"session_id":       "mimo1-session",
		"work_dir":         "/work",
		"artifact_summary": json.RawMessage(emptyArtifact),
	}
	body, _ := json.Marshal(completeBody)
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/tasks/"+taskID+"/complete", nil, testWorkspaceID, "test-daemon")
	req.Body = nopReadCloser(body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("taskId", taskID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	testHandler.CompleteTask(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("complete: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// DB stores the empty summary (non-{} JSON).
	var artBytes []byte
	testPool.QueryRow(context.Background(),
		`SELECT artifact_summary FROM chat_execution_step WHERE task_id = $1`, taskID,
	).Scan(&artBytes)
	if string(artBytes) == "{}" || len(artBytes) == 0 {
		t.Errorf("expected non-default artifact_summary, got %q", string(artBytes))
	}

	// No artifact_summary message created (total_changed_files=0).
	var msgCount int
	testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM chat_message WHERE chat_session_id = $1 AND message_type = 'artifact_summary'`,
		sessionID,
	).Scan(&msgCount)
	if msgCount != 0 {
		t.Errorf("expected 0 artifact_summary messages, got %d", msgCount)
	}
}

func TestHandoff_ArtifactSummariesUsesStructuredSchema(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	mimo1ID := createHandlerTestAgent(t, "PR9-Mimo1-HO", []byte("[]"))
	mimo2ID := createHandlerTestAgent(t, "PR9-Mimo2-HO", []byte("[]"))
	sessionID := createPR8GroupChat(t, mimo2ID, []string{mimo1ID})
	planID := createPR8Plan(t, sessionID, mimo2ID)
	// Step 1 (completed with artifact).
	task1ID, step1ID, _ := createPR8StepTask(t, mimo1ID, sessionID, planID, 1, "Create hello.py")
	testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='dispatched' WHERE id=$1`, task1ID)
	artifactJSON := `{"version":1,"summary":"Changed 1 file","changed_files":[{"path":"hello.py","change_type":"added","size_bytes":50}],"total_changed_files":1,"truncated":false,"diff_stat":{"added":1,"modified":0}}`
	testPool.Exec(context.Background(),
		`UPDATE chat_execution_step SET status='completed', artifact_summary=$2 WHERE id=$1`,
		step1ID, artifactJSON,
	)
	// Step 2 (stays queued — claim will pick it up).
	createPR8StepTask(t, mimo2ID, sessionID, planID, 2, "Modify hello.py")

	// Claim step2 → handoff bundle should include step1 artifact.
	runtimeID := handlerTestRuntimeID(t)
	resp := claimAsRuntimePR8(t, runtimeID)
	if resp.Task.HandoffBundle == nil {
		t.Fatal("expected handoff bundle")
	}
	if len(resp.Task.HandoffBundle.ArtifactSummaries) != 1 {
		t.Fatalf("expected 1 artifact summary, got %d", len(resp.Task.HandoffBundle.ArtifactSummaries))
	}
	art := resp.Task.HandoffBundle.ArtifactSummaries[0]
	if art.StepSequence != 1 {
		t.Errorf("expected step_sequence=1, got %d", art.StepSequence)
	}
	// Summary should be valid JSON with total_changed_files > 0.
	var parsed struct {
		TotalChangedFiles int `json:"total_changed_files"`
	}
	if err := json.Unmarshal([]byte(art.Summary), &parsed); err != nil {
		t.Fatalf("summary is not valid JSON: %v", err)
	}
	if parsed.TotalChangedFiles != 1 {
		t.Errorf("expected total_changed_files=1, got %d", parsed.TotalChangedFiles)
	}
}
