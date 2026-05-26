"use client";

import { ScrollArea } from "@multica/ui/components/ui/scroll-area";
import { MessageSquare } from "lucide-react";

export function ChatSessionList() {
  return (
    <ScrollArea className="flex-1">
      <div className="flex flex-col items-center justify-center py-12 text-muted-foreground">
        <MessageSquare className="size-8 mb-2" />
        <p className="text-sm">No conversations yet</p>
      </div>
    </ScrollArea>
  );
}
