"use client";

import { MessageSquarePlus } from "lucide-react";

export function ChatMainArea() {
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
