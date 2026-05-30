"use client";

import { useState } from "react";
import { ChatSessionList } from "./chat-session-list";
import { ChatMainArea } from "./chat-main-area";
import { ChatNavigationDrawer } from "./chat-navigation-drawer";
import { NewChatDialog } from "./new-chat-dialog";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Plus, Search } from "lucide-react";
import { useT } from "../../i18n";

export function ChatShell() {
  const { t } = useT("chat");
  const [activeSessionId, setActiveSessionId] = useState<string>();
  const [searchQuery, setSearchQuery] = useState("");
  const [newChatOpen, setNewChatOpen] = useState(false);

  return (
    <div className="flex h-full">
      <div className="flex w-80 flex-col border-r">
        <div className="flex items-center gap-2 border-b px-4 py-3">
          <ChatNavigationDrawer />
          <h2 className="flex-1 text-sm font-semibold">{t(($) => $.shell.title)}</h2>
          <Button
            variant="ghost"
            size="icon"
            aria-label={t(($) => $.shell.new_chat_aria)}
            onClick={() => setNewChatOpen(true)}
          >
            <Plus className="size-4" />
          </Button>
        </div>
        <div className="px-3 py-2 border-b">
          <div className="relative">
            <Search className="absolute left-2.5 top-2.5 size-4 text-muted-foreground" />
            <Input
              placeholder={t(($) => $.shell.search_placeholder)}
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="pl-8 h-8 text-sm"
            />
          </div>
        </div>
        <ChatSessionList
          activeSessionId={activeSessionId}
          onSelectSession={setActiveSessionId}
          searchQuery={searchQuery}
        />
      </div>
      <ChatMainArea sessionId={activeSessionId} />
      <NewChatDialog
        open={newChatOpen}
        onOpenChange={setNewChatOpen}
        onSessionCreated={setActiveSessionId}
      />
    </div>
  );
}
