import { describe, expect, it } from "vitest";
import type { ChatParticipant } from "@multica/core/types";
import {
  findPlainTextAgentMentions,
  extractGroupAgentMentionIds,
} from "./mention-routing";

const participants: ChatParticipant[] = [
  { agent_id: "agent-1", role: "participant", name: "mimo1" },
  { agent_id: "agent-2", role: "orchestrator", name: "mimo2" },
];

describe("group chat mention routing", () => {
  it("extracts structured agent mentions and deduplicates in order", () => {
    const content = [
      "[@mimo1](mention://agent/agent-1)",
      "[@someone](mention://member/member-1)",
      "[@mimo2](mention://agent/agent-2)",
      "[@mimo1](mention://agent/agent-1)",
      "[ABC-1](mention://issue/issue-1)",
    ].join(" ");

    expect(extractGroupAgentMentionIds(content, participants)).toEqual([
      "agent-1",
      "agent-2",
    ]);
  });

  it("ignores agent mentions outside the current group", () => {
    expect(
      extractGroupAgentMentionIds(
        "[@outsider](mention://agent/agent-outsider)",
        participants,
      ),
    ).toEqual([]);
  });

  it("detects plain text agent mentions that were not selected from the picker", () => {
    expect(findPlainTextAgentMentions("@mimo1 请处理", participants)).toEqual([
      "mimo1",
    ]);
    expect(
      findPlainTextAgentMentions(
        "[@mimo1](mention://agent/agent-1) 请处理",
        participants,
      ),
    ).toEqual([]);
  });
});
