import { z } from "zod";

const ArtifactChangedFileSchema = z.object({
  path: z.string().default(""),
  change_type: z.string().default("unknown"),
  size_bytes: z.number().default(0),
}).loose();

const ArtifactDiffStatSchema = z.object({
  added: z.number().default(0),
  modified: z.number().default(0),
}).default({ added: 0, modified: 0 });

export const ArtifactSummarySchema = z.object({
  version: z.number().default(1),
  summary: z.string().default(""),
  changed_files: z.array(ArtifactChangedFileSchema).default([]),
  total_changed_files: z.number().default(0),
  truncated: z.boolean().default(false),
  diff_stat: ArtifactDiffStatSchema,
  warnings: z.array(z.string()).default([]),
}).loose();

export type ArtifactSummary = z.infer<typeof ArtifactSummarySchema>;

const EMPTY_ARTIFACT_SUMMARY: ArtifactSummary = {
  version: 1,
  summary: "",
  changed_files: [],
  total_changed_files: 0,
  truncated: false,
  diff_stat: { added: 0, modified: 0 },
  warnings: [],
};

export function parseArtifactSummary(input: unknown): {
  artifact: ArtifactSummary;
  valid: boolean;
} {
  if (input == null || typeof input !== "object" || Array.isArray(input)) {
    return { artifact: EMPTY_ARTIFACT_SUMMARY, valid: false };
  }
  const result = ArtifactSummarySchema.safeParse(input);
  if (!result.success) {
    return { artifact: EMPTY_ARTIFACT_SUMMARY, valid: false };
  }
  // Schema has .default() on every field, so {} parses as valid.
  // Check raw fields to distinguish "real empty summary" from "malformed/missing".
  const raw = input as Record<string, unknown>;
  const isValid =
    raw.version === 1 &&
    typeof raw.total_changed_files === "number" &&
    Array.isArray(raw.changed_files);
  return { artifact: result.data, valid: isValid };
}
