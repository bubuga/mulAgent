import type { ChatParticipant } from "@multica/core/types";

const AGENT_MENTION_RE = /\[@?(?:\\.|[^\]])+\]\(mention:\/\/agent\/([^)]+)\)/g;
const ANY_MENTION_RE = /\[@?(?:\\.|[^\]])+\]\(mention:\/\/\w+\/[^)]+\)/g;

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

export function extractGroupAgentMentionIds(
  content: string,
  participants: ChatParticipant[],
): string[] {
  const participantIDs = new Set(participants.map((p) => p.agent_id));
  const seen = new Set<string>();
  const ids: string[] = [];

  for (const match of content.matchAll(AGENT_MENTION_RE)) {
    const id = match[1];
    if (!id || !participantIDs.has(id) || seen.has(id)) continue;
    seen.add(id);
    ids.push(id);
  }

  return ids;
}

export function findPlainTextAgentMentions(
  content: string,
  participants: ChatParticipant[],
): string[] {
  const withoutStructuredMentions = content.replace(ANY_MENTION_RE, " ");
  const found: string[] = [];
  const seen = new Set<string>();

  for (const participant of participants) {
    const name = participant.name?.trim();
    if (!name || seen.has(name)) continue;
    const pattern = new RegExp(
      `(^|[\\s([{])@${escapeRegExp(name)}(?=$|[\\s.,!?，。！？:：;；)\\]}])`,
      "u",
    );
    if (!pattern.test(withoutStructuredMentions)) continue;
    seen.add(name);
    found.push(name);
  }

  return found;
}
