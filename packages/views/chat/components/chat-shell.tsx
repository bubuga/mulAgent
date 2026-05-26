"use client";

import { ChatSessionList } from "./chat-session-list";
import { ChatMainArea } from "./chat-main-area";
import { ChatNavigationDrawer } from "./chat-navigation-drawer";
import { Button } from "@multica/ui/components/ui/button";
import { Plus } from "lucide-react";

export function ChatShell() {
  return (
    <div className="flex h-full">
      <div className="flex w-80 flex-col border-r">
        <div className="flex items-center gap-2 border-b px-4 py-3">
          <ChatNavigationDrawer />
          <h2 className="flex-1 text-sm font-semibold">Chats</h2>
          <Button variant="ghost" size="icon" aria-label="New chat">
            <Plus className="size-4" />
          </Button>
        </div>
        <ChatSessionList />
      </div>
      <ChatMainArea />
    </div>
  );
}
