"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentListOptions, memberListOptions } from "@multica/core/workspace/queries";
import { useCreateChatSession } from "@multica/core/chat/mutations";
import { useAuthStore } from "@multica/core/auth";
import { canAssignAgent } from "@multica/views/issues/components";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@multica/ui/components/ui/tabs";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Avatar, AvatarFallback, AvatarImage } from "@multica/ui/components/ui/avatar";
import { Check, Crown } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";

interface NewChatDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSessionCreated: (sessionId: string) => void;
}

export function NewChatDialog({ open, onOpenChange, onSessionCreated }: NewChatDialogProps) {
  const wsId = useWorkspaceId();
  const user = useAuthStore((s) => s.user);
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const createSession = useCreateChatSession();

  const currentMember = members.find((m) => m.user_id === user?.id);
  const memberRole = currentMember?.role;
  const availableAgents = agents.filter(
    (a) => !a.archived_at && canAssignAgent(a, user?.id, memberRole),
  );

  // Direct chat state
  const [directAgentId, setDirectAgentId] = useState<string | null>(null);
  const [directTitle, setDirectTitle] = useState("");

  // Group chat state
  const [groupStep, setGroupStep] = useState<1 | 2>(1);
  const [selectedAgentIds, setSelectedAgentIds] = useState<Set<string>>(new Set());
  const [orchestratorId, setOrchestratorId] = useState<string | null>(null);
  const [groupTitle, setGroupTitle] = useState("");

  const resetState = () => {
    setDirectAgentId(null);
    setDirectTitle("");
    setGroupStep(1);
    setSelectedAgentIds(new Set());
    setOrchestratorId(null);
    setGroupTitle("");
  };

  const handleDirectCreate = async () => {
    if (!directAgentId) return;
    try {
      const session = await createSession.mutateAsync({
        kind: "direct",
        agent_id: directAgentId,
        title: directTitle || undefined,
      });
      resetState();
      onOpenChange(false);
      onSessionCreated(session.id);
    } catch {
      // Error handled by mutation
    }
  };

  const handleGroupCreate = async () => {
    if (!orchestratorId || selectedAgentIds.size < 2) return;
    try {
      const session = await createSession.mutateAsync({
        kind: "group",
        agent_ids: Array.from(selectedAgentIds),
        orchestrator_agent_id: orchestratorId,
        title: groupTitle || undefined,
      });
      resetState();
      onOpenChange(false);
      onSessionCreated(session.id);
    } catch {
      // Error handled by mutation
    }
  };

  const toggleAgentSelection = (agentId: string) => {
    setSelectedAgentIds((prev) => {
      const next = new Set(prev);
      if (next.has(agentId)) {
        next.delete(agentId);
        if (orchestratorId === agentId) setOrchestratorId(null);
      } else {
        next.add(agentId);
      }
      return next;
    });
  };

  return (
    <Dialog open={open} onOpenChange={(v) => { if (!v) resetState(); onOpenChange(v); }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New Chat</DialogTitle>
        </DialogHeader>

        <Tabs defaultValue="direct" onValueChange={() => resetState()}>
          <TabsList className="w-full">
            <TabsTrigger value="direct" className="flex-1">Direct</TabsTrigger>
            <TabsTrigger value="group" className="flex-1">Group</TabsTrigger>
          </TabsList>

          {/* Direct Tab */}
          <TabsContent value="direct" className="mt-4 space-y-3">
            <div className="max-h-48 overflow-y-auto space-y-1">
              {availableAgents.map((agent) => (
                <button
                  key={agent.id}
                  onClick={() => setDirectAgentId(agent.id)}
                  className={cn(
                    "flex w-full items-center gap-3 rounded-md px-3 py-2 text-left text-sm hover:bg-accent",
                    directAgentId === agent.id && "bg-accent ring-1 ring-primary",
                  )}
                >
                  <Avatar className="size-8">
                    <AvatarImage src={agent.avatar_url ?? undefined} />
                    <AvatarFallback>{agent.name.charAt(0)}</AvatarFallback>
                  </Avatar>
                  <span className="flex-1 truncate">{agent.name}</span>
                  {directAgentId === agent.id && <Check className="size-4 text-primary" />}
                </button>
              ))}
              {availableAgents.length === 0 && (
                <p className="py-4 text-center text-sm text-muted-foreground">No agents available</p>
              )}
            </div>
            <Input
              placeholder="Title (optional)"
              value={directTitle}
              onChange={(e) => setDirectTitle(e.target.value)}
            />
            <Button
              className="w-full"
              disabled={!directAgentId || createSession.isPending}
              onClick={handleDirectCreate}
            >
              {createSession.isPending ? "Creating..." : "Create Direct Chat"}
            </Button>
          </TabsContent>

          {/* Group Tab */}
          <TabsContent value="group" className="mt-4 space-y-3">
            {groupStep === 1 && (
              <>
                <p className="text-xs text-muted-foreground">Select at least 2 agents</p>
                <div className="max-h-48 overflow-y-auto space-y-1">
                  {availableAgents.map((agent) => (
                    <button
                      key={agent.id}
                      onClick={() => toggleAgentSelection(agent.id)}
                      className={cn(
                        "flex w-full items-center gap-3 rounded-md px-3 py-2 text-left text-sm hover:bg-accent",
                        selectedAgentIds.has(agent.id) && "bg-accent",
                      )}
                    >
                      <div className={cn(
                        "flex size-5 items-center justify-center rounded border",
                        selectedAgentIds.has(agent.id) ? "border-primary bg-primary text-primary-foreground" : "border-muted-foreground/30",
                      )}>
                        {selectedAgentIds.has(agent.id) && <Check className="size-3" />}
                      </div>
                      <Avatar className="size-8">
                        <AvatarImage src={agent.avatar_url ?? undefined} />
                        <AvatarFallback>{agent.name.charAt(0)}</AvatarFallback>
                      </Avatar>
                      <span className="flex-1 truncate">{agent.name}</span>
                    </button>
                  ))}
                </div>
                <Button
                  className="w-full"
                  disabled={selectedAgentIds.size < 2}
                  onClick={() => setGroupStep(2)}
                >
                  Next: Choose Orchestrator
                </Button>
              </>
            )}

            {groupStep === 2 && (
              <>
                <button
                  onClick={() => setGroupStep(1)}
                  className="text-xs text-muted-foreground hover:text-foreground"
                >
                  ← Back to agent selection
                </button>
                <p className="text-xs text-muted-foreground">Choose orchestrator from selected agents</p>
                <div className="max-h-48 overflow-y-auto space-y-1">
                  {availableAgents
                    .filter((a) => selectedAgentIds.has(a.id))
                    .map((agent) => (
                      <button
                        key={agent.id}
                        onClick={() => setOrchestratorId(agent.id)}
                        className={cn(
                          "flex w-full items-center gap-3 rounded-md px-3 py-2 text-left text-sm hover:bg-accent",
                          orchestratorId === agent.id && "bg-accent ring-1 ring-primary",
                        )}
                      >
                        <Avatar className="size-8">
                          <AvatarImage src={agent.avatar_url ?? undefined} />
                          <AvatarFallback>{agent.name.charAt(0)}</AvatarFallback>
                        </Avatar>
                        <span className="flex-1 truncate">{agent.name}</span>
                        {orchestratorId === agent.id && (
                          <Crown className="size-4 text-yellow-500" />
                        )}
                      </button>
                    ))}
                </div>
                <Input
                  placeholder="Group title (optional)"
                  value={groupTitle}
                  onChange={(e) => setGroupTitle(e.target.value)}
                />
                <Button
                  className="w-full"
                  disabled={!orchestratorId || createSession.isPending}
                  onClick={handleGroupCreate}
                >
                  {createSession.isPending ? "Creating..." : "Create Group Chat"}
                </Button>
              </>
            )}
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  );
}
