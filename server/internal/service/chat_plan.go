package service

import (
	"context"
	"encoding/json"
	"fmt"

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

// ChatPlanService manages execution plan lifecycle.
type ChatPlanService struct {
	queries   *db.Queries
	txStarter TxStarter
	bus       *events.Bus
}

// NewChatPlanService creates a new plan service.
func NewChatPlanService(queries *db.Queries, txStarter TxStarter, bus *events.Bus) *ChatPlanService {
	return &ChatPlanService{
		queries:   queries,
		txStarter: txStarter,
		bus:       bus,
	}
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

	// Write system message.
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
		return nil, fmt.Errorf("create system message: %w", err)
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
	ErrActivePlanExists   = fmt.Errorf("an active plan already exists for this session")
	ErrNoActivePlan       = fmt.Errorf("no active plan found for this session")
	ErrPlanNotCancellable = fmt.Errorf("only plans in awaiting_approval status can be cancelled")
)
