"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { chatIMSessionsOptions } from "@multica/core/chat/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { ScrollArea } from "@multica/ui/components/ui/scroll-area";
import { MessageSquare, Users } from "lucide-react";
import { Avatar, AvatarFallback, AvatarImage } from "@multica/ui/components/ui/avatar";
import { Badge } from "@multica/ui/components/ui/badge";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

interface ChatSessionListProps {
  activeSessionId?: string;
  onSelectSession: (id: string) => void;
  searchQuery?: string;
}

export function ChatSessionList({ activeSessionId, onSelectSession, searchQuery }: ChatSessionListProps) {
  const { t } = useT("chat");
  const wsId = useWorkspaceId();
  const { data: sessions, isLoading } = useQuery(chatIMSessionsOptions(wsId));

  const filtered = useMemo(() => {
    if (!sessions || !searchQuery?.trim()) return sessions;
    const q = searchQuery.toLowerCase();
    return sessions.filter((s) => {
      if ((s.title || "").toLowerCase().includes(q)) return true;
      if ((s.last_message_preview || "").toLowerCase().includes(q)) return true;
      // Search participant names
      if (s.participants?.some((p) => (p.name || "").toLowerCase().includes(q))) return true;
      return false;
    });
  }, [sessions, searchQuery]);

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

  if (!filtered || filtered.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-12 text-muted-foreground">
        <MessageSquare className="size-8 mb-2" />
        <p className="text-sm">
          {searchQuery ? t(($) => $.session_list.no_matching) : t(($) => $.session_list.empty)}
        </p>
      </div>
    );
  }

  return (
    <ScrollArea className="flex-1">
      <div className="flex flex-col">
        {filtered.map((session) => {
          const isGroup = session.kind === "group";
          const firstParticipant = session.participants?.[0];

          return (
            <button
              key={session.id}
              onClick={() => onSelectSession(session.id)}
              className={cn(
                "flex items-center gap-3 px-4 py-3 text-left hover:bg-accent transition-colors",
                activeSessionId === session.id && "bg-accent",
              )}
            >
              {isGroup ? (
                <div className="flex size-10 items-center justify-center rounded-full bg-muted">
                  <Users className="size-5 text-muted-foreground" />
                </div>
              ) : (
                <Avatar className="size-10">
                  <AvatarImage src={firstParticipant?.avatar_url ?? undefined} />
                  <AvatarFallback>
                    {(session.title || firstParticipant?.name || "C").charAt(0).toUpperCase()}
                  </AvatarFallback>
                </Avatar>
              )}
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-medium truncate flex-1">
                    {session.title || t(($) => $.session_list.untitled)}
                  </span>
                  {isGroup && (
                    <Badge variant="secondary" className="text-[10px] px-1 py-0 shrink-0">
                      {t(($) => $.session_list.group_badge)}
                    </Badge>
                  )}
                  {session.last_message_at && (
                    <span className="text-xs text-muted-foreground shrink-0">
                      {formatRelativeTime(session.last_message_at, t(($) => $.session_list.time_now))}
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
                <div className="size-2 rounded-full bg-primary shrink-0" />
              )}
            </button>
          );
        })}
      </div>
    </ScrollArea>
  );
}

function formatRelativeTime(iso: string, nowLabel: string): string {
  const now = Date.now();
  const then = new Date(iso).getTime();
  const diff = now - then;
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return nowLabel;
  if (mins < 60) return `${mins}m`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h`;
  const days = Math.floor(hours / 24);
  return `${days}d`;
}
