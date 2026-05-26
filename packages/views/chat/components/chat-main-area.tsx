"use client";

import { MessageSquarePlus } from "lucide-react";

interface ChatMainAreaProps {
  sessionId?: string;
}

export function ChatMainArea({ sessionId }: ChatMainAreaProps) {
  if (!sessionId) {
    return (
      <div className="flex flex-1 items-center justify-center">
        <div className="flex flex-col items-center gap-3 text-muted-foreground">
          <MessageSquarePlus className="size-12" />
          <p className="text-lg font-medium">Select a conversation</p>
          <p className="text-sm">
            Choose a chat from the sidebar or start a new one
          </p>
        </div>
      </div>
    );
  }

  // TODO(PR 4): Render chat thread for the selected session.
  return (
    <div className="flex flex-1 items-center justify-center">
      <div className="flex flex-col items-center gap-3 text-muted-foreground">
        <p className="text-sm">Session {sessionId} selected</p>
        <p className="text-xs">Thread rendering coming in PR 4</p>
      </div>
    </div>
  );
}
