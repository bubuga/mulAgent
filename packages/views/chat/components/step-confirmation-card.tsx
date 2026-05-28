"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button } from "@multica/ui/components/ui/button";
import { Badge } from "@multica/ui/components/ui/badge";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Spinner } from "@multica/ui/components/ui/spinner";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@multica/ui/components/ui/collapsible";
import {
  ChevronRight,
  ChevronDown,
  Play,
  SkipForward,
  X,
  RotateCcw,
  RefreshCw,
  CheckCircle2,
  AlertCircle,
  Clock,
  Loader2,
} from "lucide-react";
import { activePlanOptions } from "@multica/core/chat/queries";
import {
  useContinueStep,
  useSkipStep,
  useCancelStep,
  useRetryStep,
  useRequestReplan,
} from "@multica/core/chat/mutations";
import type { ChatMessage, StepAttempt } from "@multica/core/types";

interface StepConfirmationCardProps {
  message: ChatMessage;
  sessionId: string;
}

/**
 * StepConfirmationCard renders inside the chat message list for system messages
 * with message_type === "step_confirmation". It shows the step status and
 * action buttons based on the current state.
 *
 * Data source: reads from activePlan query for real-time state, falls back
 * to message.metadata snapshot if plan query hasn't loaded yet.
 */
export function StepConfirmationCard({ message, sessionId }: StepConfirmationCardProps) {
  const metadata = (message.metadata ?? {}) as Record<string, unknown>;
  const stepId = metadata.step_id as string | undefined;
  const sequence = (metadata.sequence as number) ?? 0;
  const agentName = (metadata.agent_name as string) ?? "Agent";
  const plannedPrompt = (metadata.planned_prompt as string) ?? "";
  const failureReason = metadata.failure_reason as string | undefined;
  const errorMsg = metadata.error as string | undefined;

  // Try to get real-time step state from activePlan query.
  const { data: activePlanData } = useQuery(activePlanOptions(sessionId));
  const plan = activePlanData?.plan;

  // Find the step in the plan's steps array.
  const step = plan?.steps?.find((s) => s.id === stepId);

  // Use real-time step status if available, otherwise fall back to metadata.
  const status = step?.status ?? (metadata.status as string) ?? "awaiting_approval";
  const attempts = step?.attempts ?? [];

  // Get the latest attempt for editing context.
  const latestAttempt = attempts.length > 0 ? attempts[attempts.length - 1] : null;

  return (
    <div className="flex justify-center py-2">
      <div className="w-full max-w-lg rounded-lg border bg-card p-4 shadow-sm">
        {/* Header */}
        <div className="flex items-center gap-2 mb-3">
          <StepStatusIcon status={status} />
          <span className="text-sm font-medium">
            Step {sequence}: {agentName}
          </span>
          <StepStatusBadge status={status} />
        </div>

        {/* Content based on status */}
        {(status === "awaiting_approval") && (
          <AwaitingApprovalContent
            stepId={stepId!}
            sessionId={sessionId}
            plannedPrompt={plannedPrompt}
            approvedPrompt={latestAttempt?.approved_prompt}
            sequence={sequence}
            agentName={agentName}
          />
        )}

        {(status === "queued" || status === "running") && (
          <ActiveStepContent
            stepId={stepId!}
            sessionId={sessionId}
            status={status}
            sequence={sequence}
          />
        )}

        {(status === "failed" || status === "cancelled") && (
          <FailedStepContent
            stepId={stepId!}
            sessionId={sessionId}
            planId={(metadata.plan_id as string) ?? ""}
            sequence={sequence}
            agentName={agentName}
            failureReason={failureReason}
            error={errorMsg}
            lastPrompt={latestAttempt?.approved_prompt ?? plannedPrompt}
          />
        )}

        {(status === "completed" || status === "skipped") && (
          <div className="text-sm text-muted-foreground">
            {status === "completed" ? "Step completed successfully." : "Step skipped."}
          </div>
        )}

        {/* Attempt history (collapsible) */}
        {attempts.length > 1 && (
          <AttemptHistory attempts={attempts} />
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Sub-components
// ---------------------------------------------------------------------------

function StepStatusIcon({ status }: { status: string }) {
  switch (status) {
    case "awaiting_approval":
      return <Clock className="h-4 w-4 text-amber-500" />;
    case "queued":
      return <Clock className="h-4 w-4 text-blue-500" />;
    case "running":
      return <Loader2 className="h-4 w-4 text-blue-500 animate-spin" />;
    case "completed":
      return <CheckCircle2 className="h-4 w-4 text-green-500" />;
    case "failed":
      return <AlertCircle className="h-4 w-4 text-red-500" />;
    case "cancelled":
      return <X className="h-4 w-4 text-muted-foreground" />;
    case "skipped":
      return <SkipForward className="h-4 w-4 text-muted-foreground" />;
    default:
      return <Clock className="h-4 w-4 text-muted-foreground" />;
  }
}

function StepStatusBadge({ status }: { status: string }) {
  const variant = (() => {
    switch (status) {
      case "awaiting_approval":
        return "default" as const;
      case "queued":
      case "running":
        return "secondary" as const;
      case "completed":
        return "default" as const;
      case "failed":
        return "destructive" as const;
      default:
        return "outline" as const;
    }
  })();

  const label = (() => {
    switch (status) {
      case "awaiting_approval":
        return "Awaiting Approval";
      case "queued":
        return "Queued";
      case "running":
        return "Running";
      case "completed":
        return "Completed";
      case "failed":
        return "Failed";
      case "cancelled":
        return "Cancelled";
      case "skipped":
        return "Skipped";
      default:
        return status;
    }
  })();

  return <Badge variant={variant} className="text-xs">{label}</Badge>;
}

function AwaitingApprovalContent({
  stepId,
  sessionId,
  plannedPrompt,
  approvedPrompt,
}: {
  stepId: string;
  sessionId: string;
  plannedPrompt: string;
  approvedPrompt?: string;
  sequence: number;
  agentName: string;
}) {
  const [prompt, setPrompt] = useState(approvedPrompt ?? plannedPrompt);
  const continueStep = useContinueStep(sessionId);
  const skipStep = useSkipStep(sessionId);

  const handleContinue = () => {
    continueStep.mutate({ stepId, prompt: prompt !== plannedPrompt ? prompt : undefined });
  };

  const handleSkip = () => {
    skipStep.mutate(stepId);
  };

  return (
    <div className="space-y-3">
      <div>
        <label className="text-xs text-muted-foreground mb-1 block">Prompt</label>
        <Textarea
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          className="text-sm min-h-[60px]"
          placeholder="Step prompt..."
        />
      </div>
      <div className="flex gap-2">
        <Button
          size="sm"
          onClick={handleContinue}
          disabled={continueStep.isPending}
        >
          {continueStep.isPending ? (
            <Spinner className="h-3 w-3 mr-1" />
          ) : (
            <Play className="h-3 w-3 mr-1" />
          )}
          Continue
        </Button>
        <Button
          size="sm"
          variant="outline"
          onClick={handleSkip}
          disabled={skipStep.isPending}
        >
          <SkipForward className="h-3 w-3 mr-1" />
          Skip
        </Button>
      </div>
    </div>
  );
}

function ActiveStepContent({
  stepId,
  sessionId,
  status,
  sequence,
}: {
  stepId: string;
  sessionId: string;
  status: string;
  sequence: number;
}) {
  const cancelStep = useCancelStep(sessionId);

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        {status === "running" ? (
          <Spinner className="h-3 w-3" />
        ) : (
          <Clock className="h-3 w-3" />
        )}
        <span>
          {status === "running"
            ? `Step ${sequence} is executing...`
            : `Step ${sequence} is queued for execution.`}
        </span>
      </div>
      <Button
        size="sm"
        variant="outline"
        onClick={() => cancelStep.mutate(stepId)}
        disabled={cancelStep.isPending}
      >
        <X className="h-3 w-3 mr-1" />
        Cancel
      </Button>
    </div>
  );
}

function FailedStepContent({
  stepId,
  sessionId,
  planId,
  failureReason,
  error,
  lastPrompt,
}: {
  stepId: string;
  sessionId: string;
  planId: string;
  sequence: number;
  agentName: string;
  failureReason?: string;
  error?: string;
  lastPrompt: string;
}) {
  const [prompt, setPrompt] = useState(lastPrompt);
  const retryStep = useRetryStep(sessionId);
  const requestReplan = useRequestReplan(sessionId);

  return (
    <div className="space-y-3">
      {/* Failure reason */}
      {(failureReason || error) && (
        <div className="rounded-md bg-destructive/10 p-2 text-sm text-destructive">
          {error || failureReason}
        </div>
      )}

      <div>
        <label className="text-xs text-muted-foreground mb-1 block">Prompt (editable for retry)</label>
        <Textarea
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          className="text-sm min-h-[60px]"
        />
      </div>

      <div className="flex gap-2">
        <Button
          size="sm"
          onClick={() => retryStep.mutate({ stepId, prompt: prompt !== lastPrompt ? prompt : undefined })}
          disabled={retryStep.isPending}
        >
          {retryStep.isPending ? (
            <Spinner className="h-3 w-3 mr-1" />
          ) : (
            <RotateCcw className="h-3 w-3 mr-1" />
          )}
          Retry
        </Button>
        <Button
          size="sm"
          variant="outline"
          onClick={() => requestReplan.mutate(planId)}
          disabled={requestReplan.isPending}
        >
          <RefreshCw className="h-3 w-3 mr-1" />
          Request Replan
        </Button>
      </div>
    </div>
  );
}

function AttemptHistory({ attempts }: { attempts: StepAttempt[] }) {
  const [open, setOpen] = useState(false);

  return (
    <Collapsible open={open} onOpenChange={setOpen} className="mt-3">
      <CollapsibleTrigger className="w-full">
        <Button variant="ghost" size="sm" className="w-full justify-start text-xs text-muted-foreground">
          {open ? <ChevronDown className="h-3 w-3 mr-1" /> : <ChevronRight className="h-3 w-3 mr-1" />}
          {attempts.length} attempts
        </Button>
      </CollapsibleTrigger>
      <CollapsibleContent className="space-y-1 mt-1">
        {attempts.map((attempt) => (
          <div
            key={attempt.id}
            className="flex items-center gap-2 text-xs text-muted-foreground px-2 py-1 rounded bg-muted/50"
          >
            <span className="font-mono">#{attempt.attempt_number}</span>
            <StepStatusBadge status={attempt.status} />
            {attempt.failure_reason && (
              <span className="text-destructive truncate">{attempt.failure_reason}</span>
            )}
          </div>
        ))}
      </CollapsibleContent>
    </Collapsible>
  );
}
