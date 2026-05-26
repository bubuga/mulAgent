"use client";

import { useQuery } from "@tanstack/react-query";
import { chatIMSessionsOptions } from "@multica/core/chat/queries";
import { useRequiredWorkspaceSlug } from "@multica/core/paths";
import { ScrollArea } from "@multica/ui/components/ui/scroll-area";
import { MessageSquare } from "lucide-react";
import { Avatar, AvatarFallback } from "@multica/ui/components/ui/avatar";
import { cn } from "@multica/ui/lib/utils";

interface ChatSessionListProps {
  activeSessionId?: string;
  onSelectSession: (id: string) => void;
}

export function ChatSessionList({ activeSessionId, onSelectSession }: ChatSessionListProps) {
  const wsId = useRequiredWorkspaceSlug();
  const { data: sessions, isLoading } = useQuery(chatIMSessionsOptions(wsId));

  if (isLoading) {
    return (
      <div className="flex-1 p-4 space-y-3">
        {Array.from({ length: 5 }).map((_, i) => (
          <div key={i} className="flex items-center gap-3">
            <div className="size-10 rounded-full bg-muted animate-pulse" />
            <div className="flex-1 space-y-2">
              <div className="h-4 w-2/3 bg-muted rounded animate-pulse" />
              <div className="h-3 w-1/2 bg-muted rounded animate-pulse" />
            </div>
          </div>
        ))}
      </div>
    );
  }

  if (!sessions || sessions.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-12 text-muted-foreground">
        <MessageSquare className="size-8 mb-2" />
        <p className="text-sm">No conversations yet</p>
      </div>
    );
  }

  return (
    <ScrollArea className="flex-1">
      <div className="flex flex-col">
        {sessions.map((session) => (
          <button
            key={session.id}
            onClick={() => onSelectSession(session.id)}
            className={cn(
              "flex items-center gap-3 px-4 py-3 text-left hover:bg-accent transition-colors",
              activeSessionId === session.id && "bg-accent",
            )}
          >
            <Avatar className="size-10">
              <AvatarFallback>
                {(session.title || "C").charAt(0).toUpperCase()}
              </AvatarFallback>
            </Avatar>
            <div className="flex-1 min-w-0">
              <div className="flex items-center justify-between">
                <span className="text-sm font-medium truncate">
                  {session.title || "Untitled"}
                </span>
                {session.last_message_at && (
                  <span className="text-xs text-muted-foreground">
                    {formatRelativeTime(session.last_message_at)}
                  </span>
                )}
              </div>
              {session.last_message_preview && (
                <p className="text-xs text-muted-foreground truncate">
                  {session.last_message_preview}
                </p>
              )}
            </div>
            {session.has_unread && (
              <div className="size-2 rounded-full bg-primary" />
            )}
          </button>
        ))}
      </div>
    </ScrollArea>
  );
}

function formatRelativeTime(iso: string): string {
  const now = Date.now();
  const then = new Date(iso).getTime();
  const diff = now - then;
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return "now";
  if (mins < 60) return `${mins}m`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h`;
  const days = Math.floor(hours / 24);
  return `${days}d`;
}
