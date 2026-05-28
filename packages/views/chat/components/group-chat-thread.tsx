"use client";

import { useCallback, useEffect, useMemo, useRef } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  chatIMSessionsOptions,
  chatKeys,
  chatMessagesOptions,
  pendingChatTaskOptions,
} from "@multica/core/chat/queries";
import { useMarkChatSessionRead } from "@multica/core/chat/mutations";
import { useAgentPresenceDetail } from "@multica/core/agents";
import type { ChatMessage, ChatPendingTask } from "@multica/core/types";
import { ChatInput } from "./chat-input";
import { ChatMessageList } from "./chat-message-list";
import { Badge } from "@multica/ui/components/ui/badge";
import { Crown, Users } from "lucide-react";
import {
  extractGroupAgentMentionIds,
  findPlainTextAgentMentions,
} from "../lib/mention-routing";
import { useT } from "../../i18n";

interface GroupChatThreadProps {
  sessionId: string;
}

export function GroupChatThread({ sessionId }: GroupChatThreadProps) {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const markRead = useMarkChatSessionRead();
  const markReadInFlightRef = useRef<string | null>(null);
  const { t } = useT("chat");

  const { data: sessions = [] } = useQuery(chatIMSessionsOptions(wsId));
  const session = sessions.find((item) => item.id === sessionId);
  const participants = session?.participants ?? [];
  const orchestratorAgentId = session?.orchestrator_agent_id;
  const isArchived = session?.status === "archived" || !!session?.archived_at;

  const { data: rawMessages, isLoading } = useQuery(chatMessagesOptions(sessionId));
  const messages = rawMessages ?? [];
  const { data: pendingTask } = useQuery(pendingChatTaskOptions(sessionId));
  const pendingTaskId = pendingTask?.task_id ?? null;

  const presence = useAgentPresenceDetail(wsId, orchestratorAgentId ?? undefined);
  const availability = presence === "loading" ? undefined : presence.availability;
  const mentionContext = useMemo(
    () => ({
      kind: "group-chat" as const,
      participants,
      orchestratorAgentId,
    }),
    [orchestratorAgentId, participants],
  );

  // Mark read on open when session has unread.
  useEffect(() => {
    if (!session?.has_unread) {
      if (markReadInFlightRef.current === sessionId) {
        markReadInFlightRef.current = null;
      }
      return;
    }
    if (markReadInFlightRef.current === sessionId || markRead.isPending) return;
    markReadInFlightRef.current = sessionId;
    markRead.mutate(sessionId, {
      onSettled: () => {
        if (markReadInFlightRef.current === sessionId) {
          markReadInFlightRef.current = null;
        }
      },
    });
  }, [markRead, session?.has_unread, sessionId]);

  const handleSend = useCallback(
    async (content: string, attachmentIds?: string[]) => {
      const mentionIds = extractGroupAgentMentionIds(content, participants);
      const sentAt = new Date().toISOString();
      const optimistic: ChatMessage = {
        id: `optimistic-${Date.now()}`,
        chat_session_id: sessionId,
        role: "user",
        content,
        task_id: null,
        created_at: sentAt,
      };

      qc.setQueryData<ChatMessage[]>(
        chatKeys.messages(sessionId),
        (old) => (old ? [...old, optimistic] : [optimistic]),
      );
      qc.setQueryData<ChatPendingTask>(chatKeys.pendingTask(sessionId), {
        task_id: `optimistic-${optimistic.id}`,
        status: "queued",
        created_at: sentAt,
      });

      const result = await api.sendChatMessage(
        sessionId,
        content,
        attachmentIds,
        mentionIds.length > 0 ? mentionIds : undefined,
      );
      qc.setQueryData<ChatPendingTask>(chatKeys.pendingTask(sessionId), {
        task_id: result.task_id,
        status: "queued",
        created_at: result.created_at,
      });
      qc.invalidateQueries({ queryKey: chatKeys.messages(sessionId) });
      qc.invalidateQueries({ queryKey: chatKeys.imSessions(wsId) });
    },
    [participants, qc, sessionId, wsId],
  );

  const validateContent = useCallback(
    (content: string) => {
      const plainMentions = findPlainTextAgentMentions(content, participants);
      if (plainMentions.length === 0) return null;
      return t(($) => $.input.raw_agent_mention_error);
    },
    [participants, t],
  );

  const handleStop = useCallback(() => {
    if (!pendingTaskId) return;
    qc.setQueryData<ChatPendingTask>(chatKeys.pendingTask(sessionId), undefined);
    qc.invalidateQueries({ queryKey: chatKeys.messages(sessionId) });
    api.cancelTaskById(pendingTaskId).finally(() => {
      qc.invalidateQueries({ queryKey: chatKeys.pendingTask(sessionId) });
    });
  }, [pendingTaskId, qc, sessionId]);

  if (isLoading) {
    return (
      <div className="flex min-h-0 flex-1 flex-col items-center justify-center text-muted-foreground">
        <p className="text-sm">Loading messages...</p>
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {/* Group header */}
      <div className="flex items-center gap-2 border-b px-4 py-2">
        <div className="flex size-8 items-center justify-center rounded-full bg-muted">
          <Users className="size-4 text-muted-foreground" />
        </div>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium truncate">{session?.title || "Group Chat"}</span>
            {orchestratorAgentId && (
              <Badge variant="outline" className="text-[10px] px-1 py-0 gap-0.5">
                <Crown className="size-2.5" />
                Orchestrator
              </Badge>
            )}
          </div>
          <span className="text-xs text-muted-foreground">
            {participants.length} members
          </span>
        </div>
      </div>

      <ChatMessageList
        messages={messages}
        pendingTask={pendingTask}
        availability={availability}
        sessionKind="group"
        participants={participants}
        orchestratorAgentId={orchestratorAgentId}
        sessionId={sessionId}
      />
      <ChatInput
        onSend={handleSend}
        onStop={handleStop}
        isRunning={!!pendingTaskId}
        disabled={isArchived}
        draftKeyOverride={sessionId}
        editorKeyOverride={sessionId}
        validateContent={validateContent}
        mentionContext={mentionContext}
      />
    </div>
  );
}
