"use client";

import { useState } from "react";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@multica/ui/components/ui/sheet";
import { Button } from "@multica/ui/components/ui/button";
import { Menu } from "lucide-react";
import { useNavigation } from "@multica/views/navigation";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  Inbox,
  CircleDot,
  FolderKanban,
  Bot,
  Workflow,
  BarChart3,
  Server,
  Zap,
  Settings,
} from "lucide-react";
import { useT } from "../../i18n";

interface NavItem {
  label: string;
  icon: React.ComponentType<{ className?: string }>;
  href: string;
}

export function ChatNavigationDrawer() {
  const { t } = useT("chat");
  const [open, setOpen] = useState(false);
  const { push } = useNavigation();
  const p = useWorkspacePaths();

  const navItems: NavItem[] = [
    { label: "Inbox", icon: Inbox, href: p.inbox() },
    { label: "Issues", icon: CircleDot, href: p.issues() },
    { label: "Projects", icon: FolderKanban, href: p.projects() },
    { label: "Agents", icon: Bot, href: p.agents() },
    { label: "Autopilots", icon: Workflow, href: p.autopilots() },
    { label: "Usage", icon: BarChart3, href: p.usage() },
    { label: "Runtimes", icon: Server, href: p.runtimes() },
    { label: "Skills", icon: Zap, href: p.skills() },
    { label: "Settings", icon: Settings, href: p.settings() },
  ];

  return (
    <>
      <Button
        variant="ghost"
        size="icon"
        aria-label={t(($) => $.shell.navigation_aria)}
        onClick={() => setOpen(true)}
      >
        <Menu className="size-5" />
      </Button>
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent side="left" className="w-72">
          <SheetHeader>
            <SheetTitle>{t(($) => $.shell.navigation_title)}</SheetTitle>
          </SheetHeader>
          <nav className="mt-4 flex flex-col gap-1">
            {navItems.map((item) => (
              <button
                key={item.href}
                onClick={() => {
                  push(item.href);
                  setOpen(false);
                }}
                className="flex items-center gap-3 rounded-md px-3 py-2 text-sm hover:bg-accent"
              >
                <item.icon className="size-4" />
                {item.label}
              </button>
            ))}
          </nav>
        </SheetContent>
      </Sheet>
    </>
  );
}
