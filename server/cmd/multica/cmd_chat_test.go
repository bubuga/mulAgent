package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// Session ID resolution
// ---------------------------------------------------------------------------

func TestResolveSessionID_FromFlag(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("session", "", "")
	cmd.Flags().Set("session", "test-session-id")

	id, err := resolveSessionID(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "test-session-id" {
		t.Fatalf("expected test-session-id, got %s", id)
	}
}

func TestResolveSessionID_FromEnv(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("session", "", "")

	os.Setenv("MULTICA_CHAT_SESSION_ID", "env-session-id")
	defer os.Unsetenv("MULTICA_CHAT_SESSION_ID")

	id, err := resolveSessionID(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "env-session-id" {
		t.Fatalf("expected env-session-id, got %s", id)
	}
}

func TestResolveSessionID_FlagOverridesEnv(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("session", "", "")
	cmd.Flags().Set("session", "flag-id")

	os.Setenv("MULTICA_CHAT_SESSION_ID", "env-id")
	defer os.Unsetenv("MULTICA_CHAT_SESSION_ID")

	id, err := resolveSessionID(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "flag-id" {
		t.Fatalf("expected flag-id, got %s", id)
	}
}

func TestResolveSessionID_Missing(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("session", "", "")

	os.Unsetenv("MULTICA_CHAT_SESSION_ID")

	_, err := resolveSessionID(cmd)
	if err == nil {
		t.Fatal("expected error when session ID is missing")
	}
}

// ---------------------------------------------------------------------------
// JSON parsing
// ---------------------------------------------------------------------------

func TestPlanSubmitInput_JSONParsing(t *testing.T) {
	jsonData := `{"steps":[{"agent_id":"aaa-bbb","prompt":"Create hello.py"},{"agent_id":"ccc-ddd","prompt":"Create world.py"}]}`

	var input planSubmitInput
	if err := json.Unmarshal([]byte(jsonData), &input); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(input.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(input.Steps))
	}
	if input.Steps[0].AgentID != "aaa-bbb" {
		t.Fatalf("step 0 agent_id: %s", input.Steps[0].AgentID)
	}
	if input.Steps[0].Prompt != "Create hello.py" {
		t.Fatalf("step 0 prompt: %s", input.Steps[0].Prompt)
	}
	if input.Steps[1].AgentID != "ccc-ddd" {
		t.Fatalf("step 1 agent_id: %s", input.Steps[1].AgentID)
	}
}

func TestPlanSubmitInput_EmptySteps(t *testing.T) {
	jsonData := `{"steps":[]}`

	var input planSubmitInput
	if err := json.Unmarshal([]byte(jsonData), &input); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(input.Steps) != 0 {
		t.Fatalf("expected 0 steps, got %d", len(input.Steps))
	}
}

func TestPlanSubmitInput_InvalidJSON(t *testing.T) {
	jsonData := `{invalid json`

	var input planSubmitInput
	if err := json.Unmarshal([]byte(jsonData), &input); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// CLI HTTP behavior tests
// ---------------------------------------------------------------------------

// mockPlanServer creates a test HTTP server that records plan-related requests.
func mockPlanServer(t *testing.T, statusCode int, responseBody string) (*httptest.Server, *recordedRequest) {
	t.Helper()
	rec := &recordedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.Method = r.Method
		rec.Path = r.URL.Path
		rec.RawQuery = r.URL.RawQuery
		rec.AgentID = r.Header.Get("X-Agent-ID")
		rec.TaskID = r.Header.Get("X-Task-ID")
		rec.WorkspaceID = r.Header.Get("X-Workspace-ID")
		rec.Body, _ = io.ReadAll(r.Body)
		w.WriteHeader(statusCode)
		w.Write([]byte(responseBody))
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

type recordedRequest struct {
	Method      string
	Path        string
	RawQuery    string
	AgentID     string
	TaskID      string
	WorkspaceID string
	Body        []byte
}

// newTestCmd creates a cobra command with the flags needed for plan commands.
func newTestCmd(sessionID, serverURL, workspaceID, agentID, taskID string) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().String("session", "", "")
	cmd.Flags().String("file", "", "")
	cmd.Flags().Bool("dry-run", false, "")
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	if sessionID != "" {
		cmd.Flags().Set("session", sessionID)
	}
	// Set env vars that newAPIClient reads.
	os.Setenv("MULTICA_SERVER_URL", serverURL)
	os.Setenv("MULTICA_WORKSPACE_ID", workspaceID)
	os.Setenv("MULTICA_AGENT_ID", agentID)
	os.Setenv("MULTICA_TASK_ID", taskID)
	return cmd
}

func TestCLI_Submit_PostsToCorrectPath(t *testing.T) {
	srv, rec := mockPlanServer(t, http.StatusCreated, `{"id":"plan-1","status":"awaiting_approval"}`)
	cmd := newTestCmd("sess-123", srv.URL, "ws-1", "agent-1", "task-1")

	body := `{"steps":[{"agent_id":"agent-2","prompt":"Create hello.py"}]}`
	err := runChatPlanSubmitWithIO(cmd, strings.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rec.Method != "POST" {
		t.Fatalf("expected POST, got %s", rec.Method)
	}
	if rec.Path != "/api/chat/sessions/sess-123/plan" {
		t.Fatalf("unexpected path: %s", rec.Path)
	}
}

func TestCLI_Submit_DryRun_AddsQuery(t *testing.T) {
	srv, rec := mockPlanServer(t, http.StatusOK, `{"valid":true,"step_count":1}`)
	cmd := newTestCmd("sess-123", srv.URL, "ws-1", "agent-1", "task-1")
	cmd.Flags().Set("dry-run", "true")

	body := `{"steps":[{"agent_id":"agent-2","prompt":"Create hello.py"}]}`
	err := runChatPlanSubmitWithIO(cmd, strings.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rec.RawQuery != "dry_run=true" {
		t.Fatalf("expected dry_run=true query, got %s", rec.RawQuery)
	}
}

func TestCLI_Submit_SendsHeaders(t *testing.T) {
	srv, rec := mockPlanServer(t, http.StatusCreated, `{"id":"plan-1","status":"awaiting_approval"}`)
	cmd := newTestCmd("sess-123", srv.URL, "ws-1", "agent-1", "task-1")

	body := `{"steps":[{"agent_id":"agent-2","prompt":"Create hello.py"}]}`
	runChatPlanSubmitWithIO(cmd, strings.NewReader(body))

	if rec.AgentID != "agent-1" {
		t.Fatalf("expected X-Agent-ID=agent-1, got %s", rec.AgentID)
	}
	if rec.TaskID != "task-1" {
		t.Fatalf("expected X-Task-ID=task-1, got %s", rec.TaskID)
	}
	if rec.WorkspaceID != "ws-1" {
		t.Fatalf("expected X-Workspace-ID=ws-1, got %s", rec.WorkspaceID)
	}
}

func TestCLI_Submit_SendsCorrectBody(t *testing.T) {
	srv, rec := mockPlanServer(t, http.StatusCreated, `{"id":"plan-1","status":"awaiting_approval"}`)
	cmd := newTestCmd("sess-123", srv.URL, "ws-1", "agent-1", "task-1")

	body := `{"steps":[{"agent_id":"agent-2","prompt":"Create hello.py"}]}`
	runChatPlanSubmitWithIO(cmd, strings.NewReader(body))

	var sent planSubmitInput
	if err := json.Unmarshal(rec.Body, &sent); err != nil {
		t.Fatalf("unmarshal sent body: %v", err)
	}
	if len(sent.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(sent.Steps))
	}
	if sent.Steps[0].AgentID != "agent-2" {
		t.Fatalf("step agent_id: %s", sent.Steps[0].AgentID)
	}
	if sent.Steps[0].Prompt != "Create hello.py" {
		t.Fatalf("step prompt: %s", sent.Steps[0].Prompt)
	}
}

func TestCLI_Submit_409_ReturnsError(t *testing.T) {
	srv, _ := mockPlanServer(t, http.StatusConflict, `{"error":"an active plan already exists"}`)
	cmd := newTestCmd("sess-123", srv.URL, "ws-1", "agent-1", "task-1")

	body := `{"steps":[{"agent_id":"agent-2","prompt":"Create hello.py"}]}`
	err := runChatPlanSubmitWithIO(cmd, strings.NewReader(body))

	if err == nil {
		t.Fatal("expected error for 409 response")
	}
	if !strings.Contains(err.Error(), "409") {
		t.Fatalf("error should contain 409: %v", err)
	}
}

func TestCLI_Submit_400_ReturnsError(t *testing.T) {
	srv, _ := mockPlanServer(t, http.StatusBadRequest, `{"error":"at least one step is required"}`)
	cmd := newTestCmd("sess-123", srv.URL, "ws-1", "agent-1", "task-1")

	body := `{"steps":[]}`
	err := runChatPlanSubmitWithIO(cmd, strings.NewReader(body))

	if err == nil {
		t.Fatal("expected error for 400 response")
	}
}

func TestCLI_Clear_DeletesCorrectPath(t *testing.T) {
	srv, rec := mockPlanServer(t, http.StatusNoContent, "")
	cmd := newTestCmd("sess-123", srv.URL, "ws-1", "agent-1", "task-1")

	err := runChatPlanClear(cmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rec.Method != "DELETE" {
		t.Fatalf("expected DELETE, got %s", rec.Method)
	}
	if rec.Path != "/api/chat/sessions/sess-123/plan" {
		t.Fatalf("unexpected path: %s", rec.Path)
	}
}

func TestCLI_Clear_404_ReturnsError(t *testing.T) {
	srv, _ := mockPlanServer(t, http.StatusNotFound, `{"error":"no active plan found"}`)
	cmd := newTestCmd("sess-123", srv.URL, "ws-1", "agent-1", "task-1")

	err := runChatPlanClear(cmd, nil)
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

// runChatPlanSubmitWithIO is a test helper that wraps runChatPlanSubmit with
// a reader for stdin input.
func runChatPlanSubmitWithIO(cmd *cobra.Command, input io.Reader) error {
	// Temporarily replace stdin.
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	go func() {
		io.Copy(w, input)
		w.Close()
	}()
	defer func() { os.Stdin = oldStdin }()

	return runChatPlanSubmit(cmd, nil)
}
