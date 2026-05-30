package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
)

// PlanSubmitStep is the service-layer input for a single plan step.
type PlanSubmitStep struct {
	AgentID pgtype.UUID
	Prompt  string
}

// PlanResult is the service-layer output for a submitted plan.
type PlanResult struct {
	PlanID    string
	SessionID string
	Status    string
	Steps     []StepResult
	CreatedAt string
}

// StepResult is the service-layer output for a single step.
type StepResult struct {
	ID            string
	PlanID        string
	Sequence      int
	AgentID       string
	AgentName     string
	Status        string
	PlannedPrompt string
}

// DryRunResult is returned when dry_run=true.
type DryRunResult struct {
	Valid     bool         `json:"valid"`
	StepCount int          `json:"step_count"`
	Steps     []StepResult `json:"steps"`
}

// TaskNotifier is the interface for notifying the daemon about new tasks.
type TaskNotifier interface {
	NotifyTaskEnqueued(ctx context.Context, task db.AgentTaskQueue)
}

// ChatPlanService manages execution plan lifecycle.
type ChatPlanService struct {
	queries      *db.Queries
	txStarter    TxStarter
	bus          *events.Bus
	taskNotifier TaskNotifier
}

// NewChatPlanService creates a new plan service.
func NewChatPlanService(queries *db.Queries, txStarter TxStarter, bus *events.Bus) *ChatPlanService {
	return &ChatPlanService{
		queries:   queries,
		txStarter: txStarter,
		bus:       bus,
	}
}

// SetTaskNotifier sets the task notifier (called after TaskService is created).
func (s *ChatPlanService) SetTaskNotifier(n TaskNotifier) {
	s.taskNotifier = n
}

// SubmitPlan creates an execution plan with steps in a single transaction.
// Returns a conflict error if an active plan already exists for the session.
func (s *ChatPlanService) SubmitPlan(
	ctx context.Context,
	session db.ChatSession,
	orchestratorAgentID pgtype.UUID,
	steps []PlanSubmitStep,
) (*PlanResult, error) {
	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)

	// Check for existing active plan.
	_, err = qtx.GetActivePlanBySession(ctx, session.ID)
	if err == nil {
		return nil, ErrActivePlanExists
	}

	// Create the plan.
	plan, err := qtx.CreateExecutionPlan(ctx, db.CreateExecutionPlanParams{
		ChatSessionID:       session.ID,
		OrchestratorAgentID: orchestratorAgentID,
		Status:              "awaiting_approval",
		ExecutionMode:       "serial",
	})
	if err != nil {
		return nil, fmt.Errorf("create plan: %w", err)
	}

	// Create steps.
	var stepResults []StepResult
	for i, step := range steps {
		status := "planned"
		if i == 0 {
			status = "awaiting_approval"
		}
		created, err := qtx.CreateExecutionStep(ctx, db.CreateExecutionStepParams{
			PlanID:        plan.ID,
			ChatSessionID: session.ID,
			Sequence:      int32(i + 1),
			AgentID:       step.AgentID,
			Status:        status,
			PlannedPrompt: step.Prompt,
		})
		if err != nil {
			return nil, fmt.Errorf("create step %d: %w", i+1, err)
		}
		stepResults = append(stepResults, StepResult{
			ID:            util.UUIDToString(created.ID),
			PlanID:        util.UUIDToString(created.PlanID),
			Sequence:      int(created.Sequence),
			AgentID:       util.UUIDToString(created.AgentID),
			Status:        created.Status,
			PlannedPrompt: created.PlannedPrompt,
		})
	}

	// Write plan_created system message.
	metadata, _ := json.Marshal(map[string]interface{}{
		"plan_id":    util.UUIDToString(plan.ID),
		"step_count": len(steps),
	})
	_, err = qtx.CreateChatSystemMessage(ctx, db.CreateChatSystemMessageParams{
		ChatSessionID: session.ID,
		Content:       fmt.Sprintf("Execution plan created with %d step(s)", len(steps)),
		MessageType:   "plan_created",
		Metadata:      metadata,
	})
	if err != nil {
		return nil, fmt.Errorf("create plan_created message: %w", err)
	}

	// PR7: Write step_confirmation for the first step (awaiting_approval).
	if len(stepResults) > 0 {
		firstAgent, _ := qtx.GetAgent(ctx, steps[0].AgentID)
		firstAgentName := ""
		if firstAgent.Name != "" {
			firstAgentName = firstAgent.Name
		}
		stepResults[0].AgentName = firstAgentName
		stepMeta := stepMetadata(
			util.UUIDToString(plan.ID), stepResults[0].ID,
			1, stepResults[0].AgentID, firstAgentName,
			"awaiting_approval", steps[0].Prompt, nil, 0, nil, nil,
		)
		if _, err := qtx.CreateChatSystemMessage(ctx, db.CreateChatSystemMessageParams{
			ChatSessionID: session.ID,
			Content:       fmt.Sprintf("Step 1: %s — awaiting your approval", firstAgentName),
			MessageType:   "step_confirmation",
			Metadata:      stepMeta,
		}); err != nil {
			return nil, fmt.Errorf("create step_confirmation message: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	// Post-commit: broadcast events.
	workspaceID := util.UUIDToString(session.WorkspaceID)
	sessionID := util.UUIDToString(session.ID)

	s.bus.Publish(events.Event{
		Type:          protocol.EventChatPlanCreated,
		WorkspaceID:   workspaceID,
		ActorType:     "system",
		ChatSessionID: sessionID,
		Payload: protocol.ChatPlanPayload{
			ChatSessionID: sessionID,
			PlanID:        util.UUIDToString(plan.ID),
			Status:        plan.Status,
			StepCount:     len(steps),
		},
	})

	if len(stepResults) > 0 {
		s.bus.Publish(events.Event{
			Type:          protocol.EventChatStepAwaitingApproval,
			WorkspaceID:   workspaceID,
			ActorType:     "system",
			ChatSessionID: sessionID,
			Payload: protocol.ChatStepPayload{
				ChatSessionID: sessionID,
				PlanID:        util.UUIDToString(plan.ID),
				StepID:        stepResults[0].ID,
				Sequence:      1,
				AgentID:       stepResults[0].AgentID,
				Status:        "awaiting_approval",
			},
		})
	}

	return &PlanResult{
		PlanID:    util.UUIDToString(plan.ID),
		SessionID: sessionID,
		Status:    plan.Status,
		Steps:     stepResults,
		CreatedAt: plan.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
	}, nil
}

// ClearPlan cancels the active plan for a session.
func (s *ChatPlanService) ClearPlan(ctx context.Context, sessionID pgtype.UUID) error {
	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)

	plan, err := qtx.GetActivePlanBySessionForUpdate(ctx, sessionID)
	if err != nil {
		return ErrNoActivePlan
	}

	if plan.Status != "awaiting_approval" {
		return ErrPlanNotCancellable
	}

	if err := qtx.CancelNonTerminalStepsByPlan(ctx, plan.ID); err != nil {
		return fmt.Errorf("cancel steps: %w", err)
	}

	if err := qtx.UpdatePlanStatus(ctx, db.UpdatePlanStatusParams{
		ID:     plan.ID,
		Status: "cancelled",
	}); err != nil {
		return fmt.Errorf("update plan status: %w", err)
	}

	metadata, _ := json.Marshal(map[string]interface{}{
		"plan_id": util.UUIDToString(plan.ID),
	})
	_, err = qtx.CreateChatSystemMessage(ctx, db.CreateChatSystemMessageParams{
		ChatSessionID: sessionID,
		Content:       "Execution plan cancelled",
		MessageType:   "plan_cancelled",
		Metadata:      metadata,
	})
	if err != nil {
		return fmt.Errorf("create system message: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	// Post-commit: broadcast event.
	s.bus.Publish(events.Event{
		Type:          protocol.EventChatPlanCancelled,
		WorkspaceID:   "", // Will be resolved by subscriber
		ActorType:     "system",
		ChatSessionID: util.UUIDToString(sessionID),
		Payload: protocol.ChatPlanPayload{
			ChatSessionID: util.UUIDToString(sessionID),
			PlanID:        util.UUIDToString(plan.ID),
			Status:        "cancelled",
		},
	})

	return nil
}

// Sentinel errors for handler to map to HTTP status codes.
var (
	ErrActivePlanExists      = fmt.Errorf("an active plan already exists for this session")
	ErrNoActivePlan          = fmt.Errorf("no active plan found for this session")
	ErrPlanNotCancellable    = fmt.Errorf("only plans in awaiting_approval status can be cancelled")
	ErrStepNotAwaiting       = fmt.Errorf("step is not in awaiting_approval status")
	ErrStepNotRetryable      = fmt.Errorf("step is not in a retryable status (failed/cancelled)")
	ErrStepNotCancellable    = fmt.Errorf("step is not in a cancellable status (queued/running)")
	ErrActiveStepTaskExists  = fmt.Errorf("another step is currently executing")
	ErrStepTaskAlreadyTerminal = fmt.Errorf("step task is already in a terminal state")
	ErrPlanHasActiveSteps    = fmt.Errorf("plan has active steps; cancel them before requesting replan")
)

// stepMetadata builds the standard metadata JSON for step_confirmation system messages.
func stepMetadata(planID, stepID string, seq int, agentID, agentName, status, plannedPrompt string, approvedPrompt *string, attemptNum int, failureReason, errMsg *string) []byte {
	m := map[string]interface{}{
		"plan_id":        planID,
		"step_id":        stepID,
		"sequence":       seq,
		"agent_id":       agentID,
		"agent_name":     agentName,
		"status":         status,
		"planned_prompt": plannedPrompt,
	}
	if approvedPrompt != nil {
		m["approved_prompt"] = *approvedPrompt
	}
	m["attempt_number"] = attemptNum
	if failureReason != nil {
		m["failure_reason"] = *failureReason
	}
	if errMsg != nil {
		m["error"] = *errMsg
	}
	data, _ := json.Marshal(m)
	return data
}

// stepUpdateMetadata builds a JSONB fragment for UpdateStepConfirmationMetadata.
// Explicitly passes null for fields that should be cleared (e.g. failure_reason
// after a successful retry).
func stepUpdateMetadata(status string, attemptNum int, approvedPrompt, failureReason, errMsg *string) []byte {
	m := map[string]interface{}{
		"status":         status,
		"attempt_number": attemptNum,
		"failure_reason": nil,
		"error":          nil,
	}
	if approvedPrompt != nil {
		m["approved_prompt"] = *approvedPrompt
	}
	if failureReason != nil {
		m["failure_reason"] = *failureReason
	}
	if errMsg != nil {
		m["error"] = *errMsg
	}
	data, _ := json.Marshal(m)
	return data
}

// updateStepCard updates the step_confirmation message metadata for a step.
func (s *ChatPlanService) updateStepCard(qtx *db.Queries, ctx context.Context, sessionID pgtype.UUID, stepID pgtype.UUID, status string, attemptNum int, approvedPrompt, failureReason, errMsg *string) {
	_ = qtx.UpdateStepConfirmationMetadata(ctx, db.UpdateStepConfirmationMetadataParams{
		ChatSessionID: sessionID,
		StepID:        util.UUIDToString(stepID),
		Metadata:      stepUpdateMetadata(status, attemptNum, approvedPrompt, failureReason, errMsg),
	})
}

// ContinueStep executes a step from awaiting_approval: creates task + attempt, sets step queued.
func (s *ChatPlanService) ContinueStep(ctx context.Context, sessionID pgtype.UUID, stepID pgtype.UUID, editedPrompt string) (*StepResult, error) {
	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)

	// Lock session for serial execution.
	if _, err := qtx.LockChatSessionForExecution(ctx, sessionID); err != nil {
		return nil, fmt.Errorf("lock session: %w", err)
	}

	// Re-read step inside transaction.
	step, err := qtx.GetExecutionStepForUpdate(ctx, stepID)
	if err != nil {
		return nil, fmt.Errorf("get step: %w", err)
	}
	if step.Status != "awaiting_approval" {
		return nil, ErrStepNotAwaiting
	}

	// Check for active step tasks.
	hasActive, err := qtx.HasActiveStepTask(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("check active step: %w", err)
	}
	if hasActive {
		return nil, ErrActiveStepTaskExists
	}

	// Validate agent.
	agent, err := qtx.GetAgent(ctx, step.AgentID)
	if err != nil {
		return nil, fmt.Errorf("get agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return nil, fmt.Errorf("agent is archived")
	}
	if !agent.RuntimeID.Valid {
		return nil, fmt.Errorf("agent has no runtime")
	}

	// Determine prompt.
	approvedPrompt := step.PlannedPrompt
	if editedPrompt != "" {
		approvedPrompt = editedPrompt
	}

	// Create task (transaction-internal, no broadcast).
	task, err := qtx.CreateStepTask(ctx, db.CreateStepTaskParams{
		AgentID:       step.AgentID,
		RuntimeID:     agent.RuntimeID,
		ChatSessionID: sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("create step task: %w", err)
	}

	// Update step with task and prompt.
	if err := qtx.UpdateStepTaskAndPrompt(ctx, db.UpdateStepTaskAndPromptParams{
		ID:             step.ID,
		TaskID:         task.ID,
		ApprovedPrompt: pgtype.Text{String: approvedPrompt, Valid: true},
	}); err != nil {
		return nil, fmt.Errorf("update step task: %w", err)
	}

	// Create attempt.
	attemptNum, err := qtx.GetLatestAttemptNumber(ctx, step.ID)
	if err != nil {
		return nil, fmt.Errorf("get attempt number: %w", err)
	}
	num := int32(0)
	if n, ok := attemptNum.(int64); ok {
		num = int32(n)
	} else if n, ok := attemptNum.(int32); ok {
		num = n
	}
	if _, err := qtx.CreateStepAttempt(ctx, db.CreateStepAttemptParams{
		StepID:         step.ID,
		AttemptNumber:  num + 1,
		TaskID:         task.ID,
		ApprovedPrompt: approvedPrompt,
	}); err != nil {
		return nil, fmt.Errorf("create attempt: %w", err)
	}

	// Plan → running.
	if err := qtx.UpdatePlanStatus(ctx, db.UpdatePlanStatusParams{
		ID:     step.PlanID,
		Status: "running",
	}); err != nil {
		return nil, fmt.Errorf("update plan status: %w", err)
	}

	// Update step card metadata.
	s.updateStepCard(qtx, ctx, sessionID, step.ID, "queued", int(num+1), &approvedPrompt, nil, nil)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	// Post-commit: broadcast + notify daemon.
	sessionIDStr := util.UUIDToString(sessionID)
	planIDStr := util.UUIDToString(step.PlanID)
	stepIDStr := util.UUIDToString(step.ID)

	s.bus.Publish(events.Event{
		Type:          protocol.EventChatStepQueued,
		WorkspaceID:   "",
		ActorType:     "system",
		ChatSessionID: sessionIDStr,
		Payload: protocol.ChatStepPayload{
			ChatSessionID: sessionIDStr,
			PlanID:        planIDStr,
			StepID:        stepIDStr,
			Sequence:      int(step.Sequence),
			AgentID:       util.UUIDToString(step.AgentID),
			Status:        "queued",
		},
	})

	if s.taskNotifier != nil {
		s.taskNotifier.NotifyTaskEnqueued(ctx, task)
	}

	return &StepResult{
		ID:            stepIDStr,
		PlanID:        planIDStr,
		Sequence:      int(step.Sequence),
		AgentID:       util.UUIDToString(step.AgentID),
		Status:        "queued",
		PlannedPrompt: step.PlannedPrompt,
	}, nil
}

// SkipStep skips a step from awaiting_approval and promotes the next step.
func (s *ChatPlanService) SkipStep(ctx context.Context, sessionID pgtype.UUID, stepID pgtype.UUID) error {
	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)

	step, err := qtx.GetExecutionStepForUpdate(ctx, stepID)
	if err != nil {
		return fmt.Errorf("get step: %w", err)
	}
	if step.Status != "awaiting_approval" {
		return ErrStepNotAwaiting
	}

	if err := qtx.UpdateStepStatus(ctx, db.UpdateStepStatusParams{
		ID: step.ID, Status: "skipped",
	}); err != nil {
		return fmt.Errorf("update step: %w", err)
	}

	// Update step card metadata for skipped step.
	s.updateStepCard(qtx, ctx, sessionID, step.ID, "skipped", 0, nil, nil, nil)

	// Try to promote next step.
	nextStep, err := qtx.GetNextPlannedStep(ctx, step.PlanID)
	var nextStepResult *StepResult
	if err == nil {
		// Promote next step to awaiting_approval.
		if err := qtx.UpdateStepStatus(ctx, db.UpdateStepStatusParams{
			ID: nextStep.ID, Status: "awaiting_approval",
		}); err != nil {
			return fmt.Errorf("promote next step: %w", err)
		}
		if err := qtx.UpdatePlanStatus(ctx, db.UpdatePlanStatusParams{
			ID: step.PlanID, Status: "awaiting_approval",
		}); err != nil {
			return fmt.Errorf("update plan: %w", err)
		}
		// Write step_confirmation for promoted step.
		agent, _ := qtx.GetAgent(ctx, nextStep.AgentID)
		agentName := ""
		if agent.Name != "" {
			agentName = agent.Name
		}
		nextStepResult = &StepResult{
			ID:            util.UUIDToString(nextStep.ID),
			PlanID:        util.UUIDToString(nextStep.PlanID),
			Sequence:      int(nextStep.Sequence),
			AgentID:       util.UUIDToString(nextStep.AgentID),
			AgentName:     agentName,
			Status:        "awaiting_approval",
			PlannedPrompt: nextStep.PlannedPrompt,
		}
		meta := stepMetadata(
			util.UUIDToString(step.PlanID), util.UUIDToString(nextStep.ID),
			int(nextStep.Sequence), util.UUIDToString(nextStep.AgentID), agentName,
			"awaiting_approval", nextStep.PlannedPrompt, nil, 0, nil, nil,
		)
		if _, err := qtx.CreateChatSystemMessage(ctx, db.CreateChatSystemMessageParams{
			ChatSessionID: sessionID,
			Content:       fmt.Sprintf("Step %d: %s — awaiting your approval", nextStep.Sequence, agentName),
			MessageType:   "step_confirmation",
			Metadata:      meta,
		}); err != nil {
			return fmt.Errorf("create system message: %w", err)
		}
	} else {
		// No next step: plan completed.
		if err := qtx.UpdatePlanStatus(ctx, db.UpdatePlanStatusParams{
			ID: step.PlanID, Status: "completed",
		}); err != nil {
			return fmt.Errorf("complete plan: %w", err)
		}
		meta, _ := json.Marshal(map[string]interface{}{"plan_id": util.UUIDToString(step.PlanID)})
		if _, err := qtx.CreateChatSystemMessage(ctx, db.CreateChatSystemMessageParams{
			ChatSessionID: sessionID,
			Content:       "Execution plan completed",
			MessageType:   "plan_completed",
			Metadata:      meta,
		}); err != nil {
			return fmt.Errorf("create system message: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	// Post-commit: broadcast.
	sessionIDStr := util.UUIDToString(sessionID)
	planIDStr := util.UUIDToString(step.PlanID)

	s.bus.Publish(events.Event{
		Type:          protocol.EventChatStepSkipped,
		WorkspaceID:   "",
		ActorType:     "system",
		ChatSessionID: sessionIDStr,
		Payload: protocol.ChatStepPayload{
			ChatSessionID: sessionIDStr,
			PlanID:        planIDStr,
			StepID:        util.UUIDToString(step.ID),
			Sequence:      int(step.Sequence),
			AgentID:       util.UUIDToString(step.AgentID),
			Status:        "skipped",
		},
	})

	if nextStepResult != nil {
		s.bus.Publish(events.Event{
			Type:          protocol.EventChatStepAwaitingApproval,
			WorkspaceID:   "",
			ActorType:     "system",
			ChatSessionID: sessionIDStr,
			Payload: protocol.ChatStepPayload{
				ChatSessionID: sessionIDStr,
				PlanID:        planIDStr,
				StepID:        nextStepResult.ID,
				Sequence:      nextStepResult.Sequence,
				AgentID:       nextStepResult.AgentID,
				Status:        "awaiting_approval",
			},
		})
	} else {
		s.bus.Publish(events.Event{
			Type:          protocol.EventChatPlanCompleted,
			WorkspaceID:   "",
			ActorType:     "system",
			ChatSessionID: sessionIDStr,
			Payload: protocol.ChatPlanPayload{
				ChatSessionID: sessionIDStr,
				PlanID:        planIDStr,
				Status:        "completed",
			},
		})
	}

	return nil
}

// CancelStep cancels an active step (queued/running) and returns plan to awaiting_approval.
func (s *ChatPlanService) CancelStep(ctx context.Context, sessionID pgtype.UUID, stepID pgtype.UUID) error {
	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)

	step, err := qtx.GetExecutionStepForUpdate(ctx, stepID)
	if err != nil {
		return fmt.Errorf("get step: %w", err)
	}
	if step.Status != "queued" && step.Status != "running" {
		return ErrStepNotCancellable
	}

	// Cancel the linked task if it exists and is not already terminal.
	if step.TaskID.Valid {
		task, err := qtx.GetAgentTask(ctx, step.TaskID)
		if err == nil {
			if task.Status == "completed" || task.Status == "failed" || task.Status == "cancelled" {
				return ErrStepTaskAlreadyTerminal
			}
			// Cancel the task.
			if _, err := qtx.CancelAgentTask(ctx, step.TaskID); err != nil {
				return fmt.Errorf("cancel task: %w", err)
			}
		}
	}

	// Update step status.
	if err := qtx.UpdateStepStatus(ctx, db.UpdateStepStatusParams{
		ID: step.ID, Status: "cancelled",
	}); err != nil {
		return fmt.Errorf("update step: %w", err)
	}

	// Update latest attempt to cancelled.
	attempt, err := qtx.GetLatestAttemptByStep(ctx, step.ID)
	if err == nil {
		qtx.UpdateStepAttemptStatus(ctx, db.UpdateStepAttemptStatusParams{
			ID: attempt.ID, Status: "cancelled",
		})
	}

	// Plan → awaiting_approval (does NOT promote next step).
	if err := qtx.UpdatePlanStatus(ctx, db.UpdatePlanStatusParams{
		ID: step.PlanID, Status: "awaiting_approval",
	}); err != nil {
		return fmt.Errorf("update plan: %w", err)
	}

	// Update step card metadata (no new card, just update existing).
	s.updateStepCard(qtx, ctx, sessionID, step.ID, "cancelled", 0, nil, nil, nil)

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	s.bus.Publish(events.Event{
		Type:          protocol.EventChatStepCancelled,
		WorkspaceID:   "",
		ActorType:     "system",
		ChatSessionID: util.UUIDToString(sessionID),
		Payload: protocol.ChatStepPayload{
			ChatSessionID: util.UUIDToString(sessionID),
			PlanID:        util.UUIDToString(step.PlanID),
			StepID:        util.UUIDToString(step.ID),
			Sequence:      int(step.Sequence),
			AgentID:       util.UUIDToString(step.AgentID),
			Status:        "cancelled",
		},
	})

	return nil
}

// RetryStep retries a failed/cancelled step with a new attempt and task.
func (s *ChatPlanService) RetryStep(ctx context.Context, sessionID pgtype.UUID, stepID pgtype.UUID, editedPrompt string) (*StepResult, error) {
	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)

	if _, err := qtx.LockChatSessionForExecution(ctx, sessionID); err != nil {
		return nil, fmt.Errorf("lock session: %w", err)
	}

	step, err := qtx.GetExecutionStepForUpdate(ctx, stepID)
	if err != nil {
		return nil, fmt.Errorf("get step: %w", err)
	}
	if step.Status != "failed" && step.Status != "cancelled" {
		return nil, ErrStepNotRetryable
	}

	hasActive, err := qtx.HasActiveStepTask(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("check active step: %w", err)
	}
	if hasActive {
		return nil, ErrActiveStepTaskExists
	}

	agent, err := qtx.GetAgent(ctx, step.AgentID)
	if err != nil {
		return nil, fmt.Errorf("get agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return nil, fmt.Errorf("agent is archived")
	}
	if !agent.RuntimeID.Valid {
		return nil, fmt.Errorf("agent has no runtime")
	}

	// Determine prompt: edited > last approved > planned.
	approvedPrompt := step.PlannedPrompt
	if step.ApprovedPrompt.Valid && step.ApprovedPrompt.String != "" {
		approvedPrompt = step.ApprovedPrompt.String
	}
	if editedPrompt != "" {
		approvedPrompt = editedPrompt
	}

	task, err := qtx.CreateStepTask(ctx, db.CreateStepTaskParams{
		AgentID: step.AgentID, RuntimeID: agent.RuntimeID, ChatSessionID: sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("create step task: %w", err)
	}

	if err := qtx.UpdateStepTaskAndPrompt(ctx, db.UpdateStepTaskAndPromptParams{
		ID: step.ID, TaskID: task.ID,
		ApprovedPrompt: pgtype.Text{String: approvedPrompt, Valid: true},
	}); err != nil {
		return nil, fmt.Errorf("update step: %w", err)
	}

	attemptNum, err := qtx.GetLatestAttemptNumber(ctx, step.ID)
	if err != nil {
		return nil, fmt.Errorf("get attempt number: %w", err)
	}
	num := int32(0)
	if n, ok := attemptNum.(int64); ok {
		num = int32(n)
	} else if n, ok := attemptNum.(int32); ok {
		num = n
	}
	if _, err := qtx.CreateStepAttempt(ctx, db.CreateStepAttemptParams{
		StepID: step.ID, AttemptNumber: num + 1,
		TaskID: task.ID, ApprovedPrompt: approvedPrompt,
	}); err != nil {
		return nil, fmt.Errorf("create attempt: %w", err)
	}

	if err := qtx.UpdatePlanStatus(ctx, db.UpdatePlanStatusParams{
		ID: step.PlanID, Status: "running",
	}); err != nil {
		return nil, fmt.Errorf("update plan: %w", err)
	}

	// Update step card metadata.
	s.updateStepCard(qtx, ctx, sessionID, step.ID, "queued", int(num+1), &approvedPrompt, nil, nil)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	sessionIDStr := util.UUIDToString(sessionID)
	planIDStr := util.UUIDToString(step.PlanID)
	stepIDStr := util.UUIDToString(step.ID)

	s.bus.Publish(events.Event{
		Type:          protocol.EventChatStepQueued,
		WorkspaceID:   "",
		ActorType:     "system",
		ChatSessionID: sessionIDStr,
		Payload: protocol.ChatStepPayload{
			ChatSessionID: sessionIDStr, PlanID: planIDStr,
			StepID: stepIDStr, Sequence: int(step.Sequence),
			AgentID: util.UUIDToString(step.AgentID), Status: "queued",
		},
	})

	if s.taskNotifier != nil {
		s.taskNotifier.NotifyTaskEnqueued(ctx, task)
	}

	return &StepResult{
		ID: stepIDStr, PlanID: planIDStr, Sequence: int(step.Sequence),
		AgentID: util.UUIDToString(step.AgentID), Status: "queued",
		PlannedPrompt: step.PlannedPrompt,
	}, nil
}

// RequestReplan cancels the active plan and enqueues a replan request to the orchestrator.
func (s *ChatPlanService) RequestReplan(ctx context.Context, session db.ChatSession) error {
	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)

	plan, err := qtx.GetActivePlanBySessionForUpdate(ctx, session.ID)
	if err != nil {
		return ErrNoActivePlan
	}

	// Check for running steps — user must cancel them first.
	runningSteps, err := qtx.ListRunningStepsByPlan(ctx, plan.ID)
	if err != nil {
		return fmt.Errorf("list running steps: %w", err)
	}
	if len(runningSteps) > 0 {
		return ErrPlanHasActiveSteps
	}

	// Cancel all non-terminal steps.
	if err := qtx.CancelNonTerminalStepsByPlan(ctx, plan.ID); err != nil {
		return fmt.Errorf("cancel steps: %w", err)
	}

	// Cancel plan.
	if err := qtx.UpdatePlanStatus(ctx, db.UpdatePlanStatusParams{
		ID: plan.ID, Status: "cancelled",
	}); err != nil {
		return fmt.Errorf("update plan: %w", err)
	}

	// Write system message.
	meta, _ := json.Marshal(map[string]interface{}{
		"plan_id": util.UUIDToString(plan.ID), "reason": "replan_requested",
	})
	if _, err := qtx.CreateChatSystemMessage(ctx, db.CreateChatSystemMessageParams{
		ChatSessionID: session.ID,
		Content:       "Execution plan cancelled — replan requested",
		MessageType:   "plan_cancelled",
		Metadata:      meta,
	}); err != nil {
		return fmt.Errorf("create system message: %w", err)
	}

	// Create user message for orchestrator to pick up.
	replanContent := "请重新规划执行计划。之前的计划已被取消。"
	if _, err := qtx.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ChatSessionID: session.ID,
		Role:          "user",
		Content:       replanContent,
		AgentID:       pgtype.UUID{},
		MessageType:   pgtype.Text{String: "replan_request", Valid: true},
		Metadata: func() []byte {
			m, _ := json.Marshal(map[string]interface{}{
				"plan_id": util.UUIDToString(plan.ID), "reason": "replan_requested",
			})
			return m
		}(),
	}); err != nil {
		return fmt.Errorf("create user message: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	// Post-commit: broadcast + enqueue orchestrator.
	s.bus.Publish(events.Event{
		Type:          protocol.EventChatPlanCancelled,
		WorkspaceID:   util.UUIDToString(session.WorkspaceID),
		ActorType:     "system",
		ChatSessionID: util.UUIDToString(session.ID),
		Payload: protocol.ChatPlanPayload{
			ChatSessionID: util.UUIDToString(session.ID),
			PlanID:        util.UUIDToString(plan.ID),
			Status:        "cancelled",
		},
	})

	return nil
}

// ---------------------------------------------------------------------------
// StepLifecycleHook implementation
// ---------------------------------------------------------------------------

// OnStepTaskCompleted advances the plan when a step-linked task completes.
func (s *ChatPlanService) OnStepTaskCompleted(ctx context.Context, taskID pgtype.UUID, artifactSummary *TaskArtifactSummary) error {
	attempt, err := s.queries.GetStepAttemptByTaskID(ctx, taskID)
	if err != nil {
		return nil // Not a step-linked task.
	}

	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)

	// Update attempt → completed.
	if err := qtx.UpdateStepAttemptStatusByTaskID(ctx, db.UpdateStepAttemptStatusByTaskIDParams{
		TaskID: taskID, Status: "completed",
	}); err != nil {
		return fmt.Errorf("update attempt: %w", err)
	}

	// Update step → completed.
	if err := qtx.UpdateStepStatus(ctx, db.UpdateStepStatusParams{
		ID: attempt.StepID, Status: "completed",
	}); err != nil {
		return fmt.Errorf("update step: %w", err)
	}

	// PR9: Persist artifact summary to step.
	if artifactSummary != nil {
		artBytes, err := json.Marshal(artifactSummary)
		if err != nil {
			slog.Warn("marshal artifact summary failed", "error", err)
		} else {
			if err := qtx.UpdateStepArtifactSummaryByTaskID(ctx, db.UpdateStepArtifactSummaryByTaskIDParams{
				TaskID:          taskID,
				ArtifactSummary: artBytes,
			}); err != nil {
				slog.Warn("update step artifact summary failed", "error", err)
			}
		}
	}

	// PR9: Create artifact system message if there are changed files.
	if artifactSummary != nil && artifactSummary.TotalChangedFiles > 0 {
		metaBytes, _ := json.Marshal(artifactSummary)
		msgContent := artifactSummary.Summary
		if msgContent == "" {
			msgContent = fmt.Sprintf("Changed %d files", artifactSummary.TotalChangedFiles)
		}
		if _, err := qtx.CreateChatSystemMessage(ctx, db.CreateChatSystemMessageParams{
			ChatSessionID: attempt.ChatSessionID,
			Content:       msgContent,
			MessageType:   "artifact_summary",
			Metadata:      metaBytes,
		}); err != nil {
			slog.Warn("create artifact summary message failed",
				"task_id", util.UUIDToString(taskID),
				"step_id", util.UUIDToString(attempt.StepID),
				"error", err,
			)
		}
	}

	// Update step card metadata for completed step.
	s.updateStepCard(qtx, ctx, attempt.ChatSessionID, attempt.StepID, "completed", int(attempt.AttemptNumber), nil, nil, nil)

	// Try to promote next step. Save info for post-commit broadcast.
	var promotedNextStep *db.ChatExecutionStep
	var promotedAgentName string

	nextStep, nextErr := qtx.GetNextPlannedStep(ctx, attempt.PlanID)
	if nextErr == nil {
		if err := qtx.UpdateStepStatus(ctx, db.UpdateStepStatusParams{
			ID: nextStep.ID, Status: "awaiting_approval",
		}); err != nil {
			return fmt.Errorf("promote next step: %w", err)
		}
		if err := qtx.UpdatePlanStatus(ctx, db.UpdatePlanStatusParams{
			ID: attempt.PlanID, Status: "awaiting_approval",
		}); err != nil {
			return fmt.Errorf("update plan: %w", err)
		}
		agent, _ := qtx.GetAgent(ctx, nextStep.AgentID)
		promotedAgentName = agent.Name
		promotedNextStep = &nextStep
		meta := stepMetadata(
			util.UUIDToString(attempt.PlanID), util.UUIDToString(nextStep.ID),
			int(nextStep.Sequence), util.UUIDToString(nextStep.AgentID), promotedAgentName,
			"awaiting_approval", nextStep.PlannedPrompt, nil, 0, nil, nil,
		)
		if _, err := qtx.CreateChatSystemMessage(ctx, db.CreateChatSystemMessageParams{
			ChatSessionID: attempt.ChatSessionID,
			Content:       fmt.Sprintf("Step %d: %s — awaiting your approval", nextStep.Sequence, promotedAgentName),
			MessageType:   "step_confirmation",
			Metadata:      meta,
		}); err != nil {
			return fmt.Errorf("create system message: %w", err)
		}
	} else {
		if err := qtx.UpdatePlanStatus(ctx, db.UpdatePlanStatusParams{
			ID: attempt.PlanID, Status: "completed",
		}); err != nil {
			return fmt.Errorf("complete plan: %w", err)
		}
		meta, _ := json.Marshal(map[string]interface{}{"plan_id": util.UUIDToString(attempt.PlanID)})
		if _, err := qtx.CreateChatSystemMessage(ctx, db.CreateChatSystemMessageParams{
			ChatSessionID: attempt.ChatSessionID,
			Content:       "Execution plan completed",
			MessageType:   "plan_completed",
			Metadata:      meta,
		}); err != nil {
			return fmt.Errorf("create system message: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	// Post-commit: broadcast events.
	session, _ := s.queries.GetChatSession(ctx, attempt.ChatSessionID)
	workspaceID := util.UUIDToString(session.WorkspaceID)
	sessionIDStr := util.UUIDToString(attempt.ChatSessionID)
	planIDStr := util.UUIDToString(attempt.PlanID)

	s.bus.Publish(events.Event{
		Type:          protocol.EventChatStepCompleted,
		WorkspaceID:   workspaceID,
		ActorType:     "system",
		ChatSessionID: sessionIDStr,
		Payload: protocol.ChatStepPayload{
			ChatSessionID: sessionIDStr,
			PlanID:        planIDStr,
			StepID:        util.UUIDToString(attempt.StepID),
			Sequence:      int(attempt.Sequence),
			AgentID:       util.UUIDToString(attempt.AgentID),
			Status:        "completed",
		},
	})

	if promotedNextStep != nil {
		s.bus.Publish(events.Event{
			Type:          protocol.EventChatStepAwaitingApproval,
			WorkspaceID:   workspaceID,
			ActorType:     "system",
			ChatSessionID: sessionIDStr,
			Payload: protocol.ChatStepPayload{
				ChatSessionID: sessionIDStr,
				PlanID:        planIDStr,
				StepID:        util.UUIDToString(promotedNextStep.ID),
				Sequence:      int(promotedNextStep.Sequence),
				AgentID:       util.UUIDToString(promotedNextStep.AgentID),
				Status:        "awaiting_approval",
			},
		})
	}

	return nil
}

// OnStepTaskFailed marks a step as failed and returns the plan to awaiting_approval.
func (s *ChatPlanService) OnStepTaskFailed(ctx context.Context, taskID pgtype.UUID, failureReason, errMsg string) error {
	attempt, err := s.queries.GetStepAttemptByTaskID(ctx, taskID)
	if err != nil {
		return nil // Not a step-linked task.
	}

	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)

	fr := pgtype.Text{String: failureReason, Valid: failureReason != ""}
	er := pgtype.Text{String: errMsg, Valid: errMsg != ""}
	if err := qtx.UpdateStepAttemptStatusByTaskID(ctx, db.UpdateStepAttemptStatusByTaskIDParams{
		TaskID: taskID, Status: "failed", FailureReason: fr, Error: er,
	}); err != nil {
		return fmt.Errorf("update attempt: %w", err)
	}

	if err := qtx.UpdateStepStatus(ctx, db.UpdateStepStatusParams{
		ID: attempt.StepID, Status: "failed",
	}); err != nil {
		return fmt.Errorf("update step: %w", err)
	}

	if err := qtx.UpdatePlanStatus(ctx, db.UpdatePlanStatusParams{
		ID: attempt.PlanID, Status: "awaiting_approval",
	}); err != nil {
		return fmt.Errorf("update plan: %w", err)
	}

	// Update step card metadata (no new card).
	s.updateStepCard(qtx, ctx, attempt.ChatSessionID, attempt.StepID, "failed", int(attempt.AttemptNumber), nil, &failureReason, &errMsg)

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	// Load workspace ID for event routing.
	session, _ := s.queries.GetChatSession(ctx, attempt.ChatSessionID)
	workspaceID := util.UUIDToString(session.WorkspaceID)

	s.bus.Publish(events.Event{
		Type:          protocol.EventChatStepFailed,
		WorkspaceID:   workspaceID,
		ActorType:     "system",
		ChatSessionID: util.UUIDToString(attempt.ChatSessionID),
		Payload: protocol.ChatStepPayload{
			ChatSessionID: util.UUIDToString(attempt.ChatSessionID),
			PlanID:        util.UUIDToString(attempt.PlanID),
			StepID:        util.UUIDToString(attempt.StepID),
			Sequence:      int(attempt.Sequence),
			AgentID:       util.UUIDToString(attempt.AgentID),
			Status:        "failed",
		},
	})

	return nil
}
