"use client";

import { cn } from "@multica/ui/lib/utils";
import { Crown } from "lucide-react";
import type { ChatParticipant } from "@multica/core/types";

interface GroupRecipientSelectorProps {
  participants: ChatParticipant[];
  orchestratorAgentId?: string | null;
  selectedAgentId: string | null; // null = Auto (orchestrator)
  onSelect: (agentId: string | null) => void;
}

export function GroupRecipientSelector({
  participants,
  orchestratorAgentId,
  selectedAgentId,
  onSelect,
}: GroupRecipientSelectorProps) {
  return (
    <div className="flex items-center gap-1.5 px-3 py-1.5 border-b text-xs overflow-x-auto">
      <button
        onClick={() => onSelect(null)}
        className={cn(
          "shrink-0 rounded-full px-2.5 py-1 transition-colors",
          selectedAgentId === null
            ? "bg-primary text-primary-foreground"
            : "bg-muted text-muted-foreground hover:bg-accent",
        )}
      >
        Auto
      </button>
      {participants.map((p) => {
        const isOrchestrator = p.agent_id === orchestratorAgentId;
        return (
          <button
            key={p.agent_id}
            onClick={() => onSelect(p.agent_id)}
            className={cn(
              "flex items-center gap-1 shrink-0 rounded-full px-2.5 py-1 transition-colors",
              selectedAgentId === p.agent_id
                ? "bg-primary text-primary-foreground"
                : "bg-muted text-muted-foreground hover:bg-accent",
            )}
          >
            {isOrchestrator && <Crown className="size-3" />}
            <span>{p.name || "Agent"}</span>
          </button>
        );
      })}
    </div>
  );
}
