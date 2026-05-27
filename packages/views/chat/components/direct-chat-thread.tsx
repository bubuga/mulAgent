"use client";

import { useCallback, useEffect, useRef } from "react";
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
import { ChatMessageList, ChatMessageSkeleton } from "./chat-message-list";

interface DirectChatThreadProps {
  sessionId: string;
}

export function DirectChatThread({ sessionId }: DirectChatThreadProps) {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const markRead = useMarkChatSessionRead();
  const markReadInFlightRef = useRef<string | null>(null);

  const { data: sessions = [] } = useQuery(chatIMSessionsOptions(wsId));
  const session = sessions.find((item) => item.id === sessionId);
  const directAgent = session?.participants?.[0];
  const agentId = directAgent?.agent_id ?? session?.agent_id;
  const isArchived = session?.status === "archived" || !!session?.archived_at;

  const { data: rawMessages, isLoading } = useQuery(chatMessagesOptions(sessionId));
  const messages = rawMessages ?? [];
  const { data: pendingTask } = useQuery(pendingChatTaskOptions(sessionId));
  const pendingTaskId = pendingTask?.task_id ?? null;

  const presence = useAgentPresenceDetail(wsId, agentId);
  const availability = presence === "loading" ? undefined : presence.availability;

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

      const result = await api.sendChatMessage(sessionId, content, attachmentIds);
      qc.setQueryData<ChatPendingTask>(chatKeys.pendingTask(sessionId), {
        task_id: result.task_id,
        status: "queued",
        created_at: result.created_at,
      });
      qc.invalidateQueries({ queryKey: chatKeys.messages(sessionId) });
      qc.invalidateQueries({ queryKey: chatKeys.imSessions(wsId) });
    },
    [qc, sessionId, wsId],
  );

  const handleStop = useCallback(() => {
    if (!pendingTaskId) return;
    qc.setQueryData<ChatPendingTask>(chatKeys.pendingTask(sessionId), undefined);
    qc.invalidateQueries({ queryKey: chatKeys.messages(sessionId) });
    api.cancelTaskById(pendingTaskId).finally(() => {
      qc.invalidateQueries({ queryKey: chatKeys.pendingTask(sessionId) });
    });
  }, [pendingTaskId, qc, sessionId]);

  if (isLoading) return <ChatMessageSkeleton />;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <ChatMessageList
        messages={messages}
        pendingTask={pendingTask}
        availability={availability}
      />
      <ChatInput
        onSend={handleSend}
        onStop={handleStop}
        isRunning={!!pendingTaskId}
        disabled={isArchived}
        agentName={directAgent?.name}
        draftKeyOverride={sessionId}
        editorKeyOverride={agentId ?? sessionId}
      />
    </div>
  );
}
