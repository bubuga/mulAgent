"use client";

import { FilePlus2, FileEdit, Files } from "lucide-react";
import { parseArtifactSummary } from "@multica/core/chat";
import type { ChatMessage } from "@multica/core/types/chat";
import { useT } from "../../i18n";

interface ArtifactSummaryCardProps {
  message: ChatMessage;
}

export function ArtifactSummaryCard({ message }: ArtifactSummaryCardProps) {
  const { t } = useT("chat");
  const { artifact, valid } = parseArtifactSummary(message.metadata);

  // Malformed metadata → generic fallback card.
  if (!valid) {
    return (
      <div className="flex justify-center py-2">
        <div className="w-full max-w-lg rounded-lg border bg-card p-4 shadow-sm">
          <div className="flex items-center gap-2">
            <Files className="size-4 text-muted-foreground" />
            <span className="text-sm text-muted-foreground">
              {message.content || t(($) => $.artifact_summary.unavailable)}
            </span>
          </div>
        </div>
      </div>
    );
  }

  // Valid but no changes → don't render.
  if (artifact.total_changed_files === 0) {
    return null;
  }

  return (
    <div className="flex justify-center py-2">
      <div className="w-full max-w-lg rounded-lg border bg-card p-4 shadow-sm">
        <div className="flex items-center gap-2 mb-3">
          <Files className="size-4 text-muted-foreground" />
          <span className="text-sm font-medium">
            {t(($) => $.artifact_summary.title)}
          </span>
          <span className="text-xs text-muted-foreground">
            {t(($) => $.artifact_summary.files, { count: artifact.total_changed_files })}
          </span>
        </div>

        <div className="flex items-center gap-3 text-xs text-muted-foreground mb-3">
          {artifact.diff_stat.added > 0 && (
            <span className="flex items-center gap-1">
              <FilePlus2 className="size-3" />
              {artifact.diff_stat.added} {t(($) => $.artifact_summary.added)}
            </span>
          )}
          {artifact.diff_stat.modified > 0 && (
            <span className="flex items-center gap-1">
              <FileEdit className="size-3" />
              {artifact.diff_stat.modified} {t(($) => $.artifact_summary.modified)}
            </span>
          )}
        </div>

        <div className="space-y-1">
          {artifact.changed_files.map((file, i) => (
            <div key={i} className="flex items-center gap-2 text-xs">
              {file.change_type === "added" ? (
                <FilePlus2 className="size-3 shrink-0" />
              ) : file.change_type === "modified" ? (
                <FileEdit className="size-3 shrink-0" />
              ) : (
                <Files className="size-3 text-muted-foreground shrink-0" />
              )}
              <span className="truncate font-mono">{file.path}</span>
              <span className="text-muted-foreground shrink-0">
                {formatBytes(file.size_bytes)}
              </span>
            </div>
          ))}
        </div>

        {artifact.truncated && (
          <p className="text-xs text-muted-foreground mt-2">
            {t(($) => $.artifact_summary.truncated, { total: artifact.total_changed_files })}
          </p>
        )}
        {/* Warnings are stored in metadata/DB but not rendered in UI v1.
            They may contain local paths or debug info not suitable for end users. */}
      </div>
    </div>
  );
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
