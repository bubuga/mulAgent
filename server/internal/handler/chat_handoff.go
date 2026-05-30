package handler

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	handoffBundleVersion   = 1
	maxRecentMessages      = 20
	maxMessageContentChars = 1200
	maxPromptSummaryChars  = 2000
	maxApprovedPromptChars = 2000
	maxBundleBytes         = 30 * 1024 // ~30KB
)

// handoffQueries abstracts the DB queries needed by the handoff builder.
// This interface enables testing with fake queries for partial/critical failure scenarios.
type handoffQueries interface {
	GetStepHandoffContextByTaskID(ctx context.Context, taskID pgtype.UUID) (db.GetStepHandoffContextByTaskIDRow, error)
	ListRecentChatMessagesForHandoff(ctx context.Context, arg db.ListRecentChatMessagesForHandoffParams) ([]db.ListRecentChatMessagesForHandoffRow, error)
	ListPlanStepsForHandoff(ctx context.Context, planID pgtype.UUID) ([]db.ListPlanStepsForHandoffRow, error)
}

// buildHandoffBundle constructs a structured handoff context for step-linked tasks.
// Critical failure (can't resolve step/attempt/plan) returns an error → claim 500.
// Non-critical failures (messages, artifacts, revision) append warnings and continue.
func buildHandoffBundle(ctx context.Context, q handoffQueries, taskID pgtype.UUID) (*ChatHandoffBundleResponse, error) {
	// Critical: resolve task → attempt → step → plan → session
	hctx, err := q.GetStepHandoffContextByTaskID(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("resolve step context: %w", err)
	}

	bundle := &ChatHandoffBundleResponse{
		Version:        handoffBundleVersion,
		ChatSessionID:  uuidToString(hctx.ChatSessionID),
		PlanID:         uuidToString(hctx.PlanID),
		StepID:         uuidToString(hctx.StepID),
		AttemptID:      uuidToString(hctx.AttemptID),
		AttemptNumber:  hctx.AttemptNumber,
		Sequence:       hctx.Sequence,
		AgentID:        uuidToString(hctx.AgentID),
		AgentName:      hctx.AgentName,
		ApprovedPrompt: truncateString(hctx.ApprovedPrompt, maxApprovedPromptChars),
		RecentMessages: []HandoffMessage{},
		PlanSteps:      []HandoffPlanStep{},
		PreviousSteps:  []HandoffPreviousStep{},
		Revisions:      HandoffRevisions{},
	}

	// Non-critical: recent messages
	if msgs, err := q.ListRecentChatMessagesForHandoff(ctx, db.ListRecentChatMessagesForHandoffParams{
		ChatSessionID: hctx.ChatSessionID,
		Limit:         maxRecentMessages,
	}); err != nil {
		bundle.Warnings = append(bundle.Warnings, fmt.Sprintf("recent messages: %v", err))
	} else {
		bundle.RecentMessages = make([]HandoffMessage, 0, len(msgs))
		for _, m := range msgs {
			bundle.RecentMessages = append(bundle.RecentMessages, HandoffMessage{
				Role:        m.Role,
				AgentID:     uuidToString(m.AgentID),
				Content:     truncateString(m.Content, maxMessageContentChars),
				MessageType: m.MessageType,
				CreatedAt:   timestampToString(m.CreatedAt),
			})
		}
	}

	// Non-critical: plan steps with agent names, attempts, and result summaries (D9, D18)
	if steps, err := q.ListPlanStepsForHandoff(ctx, hctx.PlanID); err != nil {
		bundle.Warnings = append(bundle.Warnings, fmt.Sprintf("plan steps: %v", err))
	} else {
		bundle.PlanSteps = make([]HandoffPlanStep, 0, len(steps))
		bundle.PreviousSteps = make([]HandoffPreviousStep, 0, len(steps))
		for _, s := range steps {
			// D18: prompt summary priority: attempt_approved_prompt → es.approved_prompt → es.planned_prompt
			promptSummary := s.AttemptApprovedPrompt
			if promptSummary == "" {
				promptSummary = s.ApprovedPrompt.String
			}
			if promptSummary == "" {
				promptSummary = s.PlannedPrompt
			}

			ps := HandoffPlanStep{
				Sequence:       s.Sequence,
				AgentID:        uuidToString(s.AgentID),
				AgentName:      s.AgentName,
				Status:         s.Status,
				PromptSummary:  truncateString(promptSummary, maxPromptSummaryChars),
				AttemptNumber:  s.AttemptNumber,
				ResultRevision: s.ResultRevision.String,
			}
			bundle.PlanSteps = append(bundle.PlanSteps, ps)

			// Previous steps: only those before current sequence
			if s.Sequence < hctx.Sequence {
				prev := HandoffPreviousStep{
					Sequence:       s.Sequence,
					AgentName:      s.AgentName,
					Status:         s.Status,
					ResultRevision: s.ResultRevision.String,
				}
				// D9: result summary priority: assistant_reply → failure_reason+error → status
				prev.ResultSummary = buildStepResultSummary(s)
				bundle.PreviousSteps = append(bundle.PreviousSteps, prev)
			}

			// D13: artifact summaries from completed previous steps
			// PR9: filter by total_changed_files > 0 to exclude empty summaries.
			if s.Sequence < hctx.Sequence && s.ArtifactSummary != nil {
				var art struct {
					TotalChangedFiles int `json:"total_changed_files"`
				}
				if json.Unmarshal(s.ArtifactSummary, &art) == nil && art.TotalChangedFiles > 0 {
					raw := json.RawMessage(s.ArtifactSummary)
					bundle.ArtifactSummaries = append(bundle.ArtifactSummaries, HandoffArtifactSummary{
						StepSequence: s.Sequence,
						Summary:      truncateString(string(raw), maxMessageContentChars),
					})
				}
			}
		}
	}

	// Non-critical: revision data from attempt
	if hctx.BaseRevision.Valid || hctx.ResultRevision.Valid {
		if hctx.BaseRevision.Valid {
			bundle.Revisions.Base = parseRevisionJSON(hctx.BaseRevision.String)
		}
		if hctx.ResultRevision.Valid {
			bundle.Revisions.Result = parseRevisionJSON(hctx.ResultRevision.String)
		}
	}

	// Bound total bundle size: multi-level truncation
	bundle.Truncated = boundBundleSize(bundle)

	return bundle, nil
}

// buildStepResultSummary extracts a human-readable result summary for a previous step (D9).
func buildStepResultSummary(s db.ListPlanStepsForHandoffRow) string {
	// Priority 1: assistant reply content
	if reply, ok := s.AssistantReply.(string); ok && reply != "" {
		return truncateString(reply, maxPromptSummaryChars)
	}
	// Priority 2: failure reason + error (for failed steps)
	if s.Status == "failed" {
		parts := ""
		if s.FailureReason.Valid && s.FailureReason.String != "" {
			parts = s.FailureReason.String
		}
		if s.Error.Valid && s.Error.String != "" {
			if parts != "" {
				parts += ": "
			}
			parts += s.Error.String
		}
		if parts != "" {
			return truncateString(parts, maxPromptSummaryChars)
		}
	}
	// Priority 3: status string
	return s.Status
}

// parseRevisionJSON parses a revision JSON string into TaskRevisionInfo.
func parseRevisionJSON(s string) *service.TaskRevisionInfo {
	var info service.TaskRevisionInfo
	if json.Unmarshal([]byte(s), &info) == nil {
		return &info
	}
	return &service.TaskRevisionInfo{Kind: "error", Warning: "failed to parse revision"}
}

// boundBundleSize truncates the bundle to fit within maxBundleBytes.
// Multi-level truncation strategy: messages → artifacts → previous steps → plan prompts.
func boundBundleSize(bundle *ChatHandoffBundleResponse) bool {
	data, _ := json.Marshal(bundle)
	if len(data) <= maxBundleBytes {
		return false
	}

	// Level 1: truncate older messages (from front)
	for len(bundle.RecentMessages) > 0 {
		bundle.RecentMessages = bundle.RecentMessages[1:]
		if !bundle.Truncated {
			bundle.Truncated = true
		}
		data, _ = json.Marshal(bundle)
		if len(data) <= maxBundleBytes {
			return true
		}
	}

	// Level 2: truncate artifact summaries
	if len(bundle.ArtifactSummaries) > 0 {
		bundle.ArtifactSummaries = bundle.ArtifactSummaries[:0]
		data, _ = json.Marshal(bundle)
		if len(data) <= maxBundleBytes {
			return true
		}
	}

	// Level 3: truncate previous step summaries
	for i := range bundle.PreviousSteps {
		bundle.PreviousSteps[i].ResultSummary = truncateString(bundle.PreviousSteps[i].ResultSummary, 200)
	}
	data, _ = json.Marshal(bundle)
	if len(data) <= maxBundleBytes {
		return true
	}

	// Level 4: truncate plan prompt summaries
	for i := range bundle.PlanSteps {
		bundle.PlanSteps[i].PromptSummary = truncateString(bundle.PlanSteps[i].PromptSummary, 200)
	}
	data, _ = json.Marshal(bundle)
	if len(data) <= maxBundleBytes {
		return true
	}

	return true
}

// truncateString truncates s to maxLen characters, adding "..." if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
