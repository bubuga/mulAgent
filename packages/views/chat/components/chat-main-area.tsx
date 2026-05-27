"use client";

import { MessageSquarePlus } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { chatIMSessionsOptions } from "@multica/core/chat/queries";
import { DirectChatThread } from "./direct-chat-thread";
import { GroupChatThread } from "./group-chat-thread";

interface ChatMainAreaProps {
  sessionId?: string;
}

export function ChatMainArea({ sessionId }: ChatMainAreaProps) {
  const wsId = useWorkspaceId();
  const { data: sessions = [] } = useQuery(chatIMSessionsOptions(wsId));
  const session = sessionId ? sessions.find((item) => item.id === sessionId) : null;

  if (!sessionId) {
    return (
      <div className="flex flex-1 items-center justify-center">
        <div className="flex flex-col items-center gap-3 text-muted-foreground">
          <div className="flex h-12 w-12 items-center justify-center rounded-full border border-border bg-muted/40">
            <MessageSquarePlus className="h-5 w-5" />
          </div>
          <p className="text-sm">Select a conversation</p>
        </div>
      </div>
    );
  }

  if (session?.kind === "group") {
    return <GroupChatThread sessionId={sessionId} />;
  }

  return <DirectChatThread sessionId={sessionId} />;
}
