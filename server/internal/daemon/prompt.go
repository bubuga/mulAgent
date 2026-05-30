package daemon

import (
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// BuildPrompt constructs the task prompt for an agent CLI.
// Keep this minimal — detailed instructions live in CLAUDE.md / AGENTS.md
// injected by execenv.InjectRuntimeConfig. The provider string is used by
// comment-triggered tasks: Codex's per-turn reply template needs the
// platform-aware "stdin or file" variant, every other provider gets a
// lightweight inline template (or Windows file for any provider on
// Windows).
func BuildPrompt(task Task, provider string) string {
	if task.ChatSessionID != "" {
		return buildChatPrompt(task)
	}
	if task.TriggerCommentID != "" {
		return buildCommentPrompt(task, provider)
	}
	if task.AutopilotRunID != "" {
		return buildAutopilotPrompt(task)
	}
	if task.QuickCreatePrompt != "" {
		return buildQuickCreatePrompt(task)
	}
	var b strings.Builder
	b.WriteString("You are running as a local coding agent for a Multica workspace.\n\n")
	fmt.Fprintf(&b, "Your assigned issue ID is: %s\n\n", task.IssueID)
	fmt.Fprintf(&b, "Start by running `multica issue get %s --output json` to understand your task, then complete it.\n", task.IssueID)
	fmt.Fprintf(&b, "For comment history, follow the rule in your runtime workflow file (assignment-triggered tasks treat the read as mandatory). `multica issue comment list %s --output json` returns all comments for the issue (server caps at 2000). On long-running issues use `--recent 20 --output json` to read the 20 most recently active threads, then page older threads via the stderr `Next thread cursor: ...` line and the matching `--before` / `--before-id` until you have enough history. `--since <RFC3339>` is still available for incremental polling and may combine with `--recent`.\n", task.IssueID)
	return b.String()
}

// buildQuickCreatePrompt constructs a prompt for quick-create tasks. The
// user typed a single natural-language sentence in the create-issue modal;
// the agent's job is to translate it into one `multica issue create` CLI
// invocation, using its judgment to decide whether fetching referenced URLs
// would produce a better issue. No issue exists yet, so the agent must NOT
// call `multica issue get` or attempt to comment — there's nothing to read
// or reply to.
func buildQuickCreatePrompt(task Task) string {
	var b strings.Builder
	b.WriteString("You are running as a quick-create assistant for a Multica workspace.\n\n")
	b.WriteString("A user captured the following input via the quick-create modal. There is NO existing issue. Your job is to create a well-formed issue from this input with a single `multica issue create` command.\n\n")
	fmt.Fprintf(&b, "User input:\n> %s\n\n", task.QuickCreatePrompt)

	b.WriteString("Field rules:\n\n")

	// title
	b.WriteString("- **title**: required. A concise but semantically rich summary. If the input references external resources (PRs, issues, URLs), use your judgment on whether fetching the resource would produce a meaningfully better title — e.g. \"review PR #123\" → \"Review PR #123: Refactor auth module to OAuth2\". Strip filler words but preserve key semantic information.\n\n")

	// description — the core optimization
	b.WriteString("- **description**: The description is the executing agent's primary context. Aim for high fidelity — they should grasp the user's intent as if they had read the raw input themselves. Use a two-section structure:\n\n")
	b.WriteString("  1. **User request** — Faithfully restate what the user wants in their own words. Preserve specific names, identifiers, file paths, code snippets, and technical terms verbatim. Strip non-spec material before writing it (this is removal, not paraphrasing): verbal routing wrappers about creating the issue or routing it (e.g. \"create an issue\", \"分配给 X\", \"让 @X 处理\") and pure conversational fillers (e.g. \"对吧？\"). When in doubt, keep it.\n\n")
	b.WriteString("     CC exception: `multica issue create` has no `--subscriber` flag, and the platform auto-subscribes members whose `[@Name](mention://member/<uuid>)` link appears in the description. When the user wrote \"cc @Y\", strip the verbal \"cc\" wrapper from the User request body and append a final `CC: <mention link(s)>` line to the description so the cc routing still fires.\n\n")
	b.WriteString("  2. **Context** — include ONLY when the input cited external resources AND you successfully fetched them AND they produced verifiable facts worth recording. Summarize facts only (e.g. \"PR #45 changes auth to JWT\"), not interpretation or unsolicited reference implementations. If you have nothing factual to add, omit the section entirely — never use it as an apology log for resources you could not fetch.\n\n")
	b.WriteString("  Hard rules: never invent requirements, implementation details, or acceptance criteria the user did not express; never reduce multi-sentence input to a single vague sentence; never echo the title.\n\n")

	// priority
	b.WriteString("- **priority**: one of `urgent`, `high`, `medium`, `low`, or omit. Map P0/P1 → urgent/high; \"asap\" → urgent. If unspecified, omit.\n\n")

	// assignee
	b.WriteString("- **assignee**:\n")
	b.WriteString("    - When the user names someone (\"assign to X\" / \"@X\"), call `multica workspace member list --output json`, `multica agent list --output json`, and `multica squad list --output json` and find the matching entity by display name. Squads are first-class assignees too — a squad name (e.g. \"Super Human\") routes work to the squad leader, who then delegates. On a clean unambiguous match, prefer `--assignee-id <uuid>` using the `user_id` (member) or `id` (agent or squad) from that JSON — UUID matching is exact and robust to name collisions in workspaces with overlapping names. `--assignee <name>` (fuzzy) is acceptable as a fallback when names are unambiguous. On no match or ambiguous match, do NOT pass either flag — instead append a final line to the description: `Unrecognized assignee: X`.\n")
	b.WriteString("    - Treat bare @-routing as an assignee directive even when the user did not write the English word \"assign\". This includes Chinese imperatives like `让 @独立团 review 这个 PR`, `给 @X 处理`, or `交给 @X`; strip the leading `@`/`＠` before matching display names. Do not keep that routing wrapper or `@Name` in the description unless it is a true CC-style notification rather than ownership. If the matched entity is a squad, pass the squad's `id` as `--assignee-id`, not the leader agent's id.\n")
	agentID := ""
	agentName := ""
	if task.Agent != nil {
		agentID = task.Agent.ID
		agentName = task.Agent.Name
	}
	switch {
	case task.SquadID != "":
		// The user opened quick-create with a SQUAD selected. The task
		// runs on the squad's leader agent, but the squad is the expected
		// owner — assigning to the leader would mask the squad's
		// delegation flow. Always point the default at the squad UUID.
		if task.SquadName != "" {
			fmt.Fprintf(&b, "    - When the user did NOT name an assignee, default to the picker SQUAD %q: pass `--assignee-id %q` (the squad's UUID). The user opened quick-create with the squad selected; you (the leader agent) are running on the squad's behalf, so the squad — not you — is the expected owner. Never leave the issue unassigned, and do not assign it to your own agent UUID.\n\n", task.SquadName, task.SquadID)
		} else {
			fmt.Fprintf(&b, "    - When the user did NOT name an assignee, default to the picker SQUAD: pass `--assignee-id %q` (the squad's UUID). The user opened quick-create with the squad selected; you (the leader agent) are running on the squad's behalf, so the squad — not you — is the expected owner. Never leave the issue unassigned, and do not assign it to your own agent UUID.\n\n", task.SquadID)
		}
	case agentID != "":
		fmt.Fprintf(&b, "    - When the user did NOT name an assignee, default to YOURSELF: pass `--assignee-id %q` (your agent UUID). The picker agent is the expected owner because the user opened quick-create with you selected — never leave the issue unassigned. Use the UUID flag, not `--assignee <name>`, so the assignment is unambiguous even when other agents share part of your name.\n\n", agentID)
	case agentName != "":
		fmt.Fprintf(&b, "    - When the user did NOT name an assignee, default to YOURSELF: pass `--assignee %q`. The picker agent is the expected owner because the user opened quick-create with you selected — never leave the issue unassigned.\n\n", agentName)
	default:
		b.WriteString("    - When the user did NOT name an assignee, default to YOURSELF (the picker agent): pass `--assignee-id <your agent UUID>` (preferred) or `--assignee <your agent name>`. Never leave the issue unassigned.\n\n")
	}

	// project — pinned by the modal when the user picked one, otherwise
	// omitted so the platform routes to the workspace default. Always pass
	// the UUID (never a name) so the issue lands in the right project even
	// when several share a title.
	if task.ProjectID != "" {
		if task.ProjectTitle != "" {
			fmt.Fprintf(&b, "- **project**: required for this run. Pass `--project %q` so the new issue lands in project %q (the user picked it in the quick-create modal). Do not infer a different project from the prompt text — the modal selection is authoritative.\n", task.ProjectID, task.ProjectTitle)
		} else {
			fmt.Fprintf(&b, "- **project**: required for this run. Pass `--project %q` so the new issue lands in the project the user picked in the quick-create modal. Do not infer a different project from the prompt text — the modal selection is authoritative.\n", task.ProjectID)
		}
	} else {
		b.WriteString("- **project**: omit. The platform will route the issue to the workspace default.\n")
	}
	b.WriteString("- **status**: omit (defaults to `todo`).\n")
	b.WriteString("- **attachments**: do NOT pass `--attachment`. The flag only accepts LOCAL file paths. Any image URL in the user input is already markdown — keep it inline in `--description` instead.\n\n")

	// output format
	b.WriteString("Output format:\n")
	b.WriteString("- Run exactly one `multica issue create --output json` invocation. Do not retry for any reason — even on non-zero exit. The issue may already exist; another attempt would create a duplicate.\n")
	b.WriteString("- Parse the JSON response to read the created issue's `identifier` (preferred) or `id` (fallback). Do not scrape human output and do not assume any workspace issue prefix such as `MUL-`; workspaces can use custom prefixes.\n")
	b.WriteString("- After success, print exactly one line: `Created <identifier-or-id>: <title>` and exit. No commentary, no follow-up tool calls.\n")
	b.WriteString("- Do NOT call `multica issue get` or `multica issue comment add` — there is no issue to query or comment on.\n")
	b.WriteString("- On CLI error or JSON parse error, exit with the error as the only output. The platform writes a failure notification automatically.\n")
	return b.String()
}

// buildCommentPrompt constructs a prompt for comment-triggered tasks.
// The triggering comment content is embedded directly so the agent cannot
// miss it, even when stale output files exist in a reused workdir.
// The reply instructions (including the current TriggerCommentID as --parent)
// are re-emitted on every turn so resumed sessions cannot carry forward a
// previous turn's --parent UUID.
func buildCommentPrompt(task Task, provider string) string {
	var b strings.Builder
	b.WriteString("You are running as a local coding agent for a Multica workspace.\n\n")
	fmt.Fprintf(&b, "Your assigned issue ID is: %s\n\n", task.IssueID)
	if task.TriggerCommentContent != "" {
		authorLabel := "A user"
		if task.TriggerAuthorType == "agent" {
			name := task.TriggerAuthorName
			if name == "" {
				name = "another agent"
			}
			authorLabel = fmt.Sprintf("Another agent (%s)", name)
		}
		fmt.Fprintf(&b, "[NEW COMMENT] %s just left a new comment. Focus on THIS comment — do not confuse it with previous ones:\n\n", authorLabel)
		fmt.Fprintf(&b, "> %s\n\n", task.TriggerCommentContent)
		if task.TriggerAuthorType == "agent" {
			b.WriteString("⚠️ The triggering comment was posted by another agent. Decide whether a reply is warranted. If you produced actual work this turn (investigated, fixed something, answered a real question), post the result as a normal reply — that is NOT a noise comment, and the standard rule that final results must be delivered via comment still applies. If the triggering comment was a pure acknowledgment, thanks, or sign-off AND you produced no work this turn, do NOT reply — and do NOT post a comment saying 'No reply needed' or similar. Simply exit with no output. Silence is the preferred way to end agent-to-agent threads. If you do reply, do not @mention the other agent as a sign-off (that re-triggers them and starts a loop).\n\n")
		}
		if task.Agent != nil && strings.Contains(task.Agent.Instructions, "## Squad Operating Protocol") {
			fmt.Fprintf(&b, "⚠️ **Squad leader no_action rule:** If you decide no action is needed, call `multica squad activity %s no_action --reason \"...\"` and EXIT. DO NOT post any comment — not even one that says \"no action needed\" or \"exiting silently\". The squad activity call records your decision; a comment is redundant noise.\n\n", task.IssueID)
		}
	}
	fmt.Fprintf(&b, "Start by running `multica issue get %s --output json` to understand your task, then decide how to proceed.\n\n", task.IssueID)
	fmt.Fprintf(&b, "For comment history, read the triggering thread first: `multica issue comment list %s --thread %s --output json` returns the root and every reply in the same thread as the trigger comment. If you still need more context, `multica issue comment list %s --recent 20 --output json` pulls the 20 most recently active threads on the issue (each `--recent` page prints a `Next thread cursor: --before <ts> --before-id <root-id>` line on stderr — pass the same pair back to scroll older threads). Avoid the unfiltered `--output json` form on long-running issues; it dumps the full flat timeline (cap 2000) and wastes context. `--since <RFC3339>` is still available for incremental polling and may combine with `--thread` or `--recent`.\n\n", task.IssueID, task.TriggerCommentID, task.IssueID)
	b.WriteString(execenv.BuildCommentReplyInstructions(provider, task.IssueID, task.TriggerCommentID))
	return b.String()
}

// buildChatPrompt constructs a prompt for interactive chat tasks.
func buildChatPrompt(task Task) string {
	var b strings.Builder

	// Step execution tasks: use approved prompt, no plan CLI.
	if task.IsExecutionStep {
		if task.HandoffBundle != nil {
			return buildStepPromptWithHandoff(task, &b)
		}
		// Fallback: minimal prompt without handoff (backward compat).
		b.WriteString("You are executing an approved plan step in a group chat.\n")
		b.WriteString("Complete ONLY this step. Do NOT call `multica chat plan submit`.\n")
		b.WriteString("Do NOT create new plans. Just execute the step and report the result.\n\n")
		fmt.Fprintf(&b, "Step instruction:\n%s\n", task.ChatMessage)
		if len(task.ChatMessageAttachments) > 0 {
			b.WriteString("\nAttachments on this message:\n")
			for _, a := range task.ChatMessageAttachments {
				if a.ContentType != "" {
					fmt.Fprintf(&b, "- id=%s filename=%q content_type=%s\n", a.ID, a.Filename, a.ContentType)
				} else {
					fmt.Fprintf(&b, "- id=%s filename=%q\n", a.ID, a.Filename)
				}
			}
			b.WriteString("Use `multica attachment download <id>` to fetch each file locally before referring to it.\n")
		}
		return b.String()
	}

	if task.IsOrchestrator && task.ChatSessionKind == "group" {
		return buildOrchestratorPrompt(task, &b)
	}

	b.WriteString("You are running as a chat assistant for a Multica workspace.\n")
	b.WriteString("A user is chatting with you directly. Respond to their message.\n\n")
	fmt.Fprintf(&b, "User message:\n%s\n", task.ChatMessage)
	// List attachments by id + filename so the agent can fetch them via
	// the CLI. We deliberately do NOT inline the URL: chat attachments
	// live behind a signed CDN with a short TTL, so by the time the agent
	// has finished thinking the URL embedded in the markdown body may
	// have expired. `multica attachment download <id>` re-signs at click
	// time and is the only reliable path.
	if len(task.ChatMessageAttachments) > 0 {
		b.WriteString("\nAttachments on this message:\n")
		for _, a := range task.ChatMessageAttachments {
			if a.ContentType != "" {
				fmt.Fprintf(&b, "- id=%s filename=%q content_type=%s\n", a.ID, a.Filename, a.ContentType)
			} else {
				fmt.Fprintf(&b, "- id=%s filename=%q\n", a.ID, a.Filename)
			}
		}
		b.WriteString("Use `multica attachment download <id>` to fetch each file locally before referring to it.\n")
	}
	return b.String()
}

// buildAutopilotPrompt constructs a prompt for run_only autopilot tasks.
func buildAutopilotPrompt(task Task) string {
	var b strings.Builder
	b.WriteString("You are running as a local coding agent for a Multica workspace.\n\n")
	b.WriteString("This task was triggered by an Autopilot in run-only mode. There is no assigned Multica issue for this run.\n\n")
	fmt.Fprintf(&b, "Autopilot run ID: %s\n", task.AutopilotRunID)
	if task.AutopilotID != "" {
		fmt.Fprintf(&b, "Autopilot ID: %s\n", task.AutopilotID)
	}
	if task.AutopilotTitle != "" {
		fmt.Fprintf(&b, "Autopilot title: %s\n", task.AutopilotTitle)
	}
	if task.AutopilotSource != "" {
		fmt.Fprintf(&b, "Trigger source: %s\n", task.AutopilotSource)
	}
	if strings.TrimSpace(string(task.AutopilotTriggerPayload)) != "" {
		fmt.Fprintf(&b, "Trigger payload:\n%s\n", strings.TrimSpace(string(task.AutopilotTriggerPayload)))
	}
	b.WriteString("\nAutopilot instructions:\n")
	if strings.TrimSpace(task.AutopilotDescription) != "" {
		b.WriteString(task.AutopilotDescription)
		b.WriteString("\n\n")
	} else if task.AutopilotTitle != "" {
		fmt.Fprintf(&b, "%s\n\n", task.AutopilotTitle)
	} else {
		b.WriteString("No additional autopilot instructions were provided. Inspect the autopilot configuration before proceeding.\n\n")
	}
	if task.AutopilotID != "" {
		fmt.Fprintf(&b, "Start by running `multica autopilot get %s --output json` if you need the full autopilot configuration, then complete the instructions above.\n", task.AutopilotID)
	} else {
		b.WriteString("Complete the instructions above.\n")
	}
	b.WriteString("Do not run `multica issue get`; this run does not have an issue ID.\n")
	return b.String()
}

// buildOrchestratorPrompt constructs the prompt for a group chat Orchestrator.
// Overrides the direct-chat opening with group-chat context and injects the
// plan CLI instructions with the actual session ID.
func buildOrchestratorPrompt(task Task, b *strings.Builder) string {
	sessionID := task.ChatSessionID

	b.WriteString("## Group Chat — Orchestrator\n\n")
	b.WriteString("You are the Orchestrator agent in a group chat with multiple agents.\n")
	b.WriteString("Your job is to:\n")
	b.WriteString("1. Understand the user's request\n")
	b.WriteString("2. Break it into steps, assigning each step to the most suitable agent\n")
	b.WriteString("3. Submit the plan using the CLI below\n")
	b.WriteString("4. Explain the plan to the user in your visible reply\n\n")

	fmt.Fprintf(b, "User message:\n%s\n\n", task.ChatMessage)

	b.WriteString("### Available Agents\n\n")
	for _, p := range task.GroupParticipants {
		if p.Role == "orchestrator" {
			fmt.Fprintf(b, "- **%s** (you, Orchestrator, id: %s)\n", p.AgentName, p.AgentID)
		} else {
			fmt.Fprintf(b, "- **%s** (id: %s)\n", p.AgentName, p.AgentID)
		}
	}

	b.WriteString("\n### Plan CLI\n\n")
	fmt.Fprintf(b, "Submit a one-shot execution plan for this group chat (session: %s):\n\n", sessionID)
	b.WriteString("```bash\n")
	fmt.Fprintf(b, "multica chat plan submit --session %s <<'EOF'\n", sessionID)
	b.WriteString("{\"steps\":[{\"agent_id\":\"<agent-id>\",\"prompt\":\"<task description>\"}]}\n")
	b.WriteString("EOF\n```\n\n")
	fmt.Fprintf(b, "Or: multica chat plan submit --session %s --file plan.json\n\n", sessionID)
	b.WriteString("Rules:\n")
	b.WriteString("- Each step targets one agent by ID (use IDs from Available Agents above)\n")
	b.WriteString("- Minimum 1 step, maximum 8 steps per plan\n")
	b.WriteString("- Steps execute serially (first step runs first)\n")
	b.WriteString("- Submit is one-shot: provide the complete plan in one JSON\n")
	b.WriteString("- Do NOT enqueue steps yourself; the platform handles execution after user approval\n")
	b.WriteString("- Use `--dry-run` to validate without persisting\n")
	fmt.Fprintf(b, "- To cancel a plan: `multica chat plan clear --session %s`\n", sessionID)

	b.WriteString("\n### Handling 409 Conflict\n\n")
	b.WriteString("If `multica chat plan submit` returns HTTP 409 (active plan already exists):\n")
	b.WriteString("1. Do NOT automatically cancel the existing plan\n")
	b.WriteString("2. Reply to the user explaining that an active plan already exists\n")
	b.WriteString("3. Ask the user: \"当前会话已有一个活跃的执行计划。是否取消原计划并提交新计划？\"\n")
	b.WriteString("4. Only if the user explicitly confirms, then run:\n")
	fmt.Fprintf(b, "   multica chat plan clear --session %s\n", sessionID)
	fmt.Fprintf(b, "   multica chat plan submit --session %s ...\n", sessionID)
	b.WriteString("5. If the user declines, reply: \"已保留原计划，新建议不会创建为执行计划。\"\n")

	// Include attachments if any.
	if len(task.ChatMessageAttachments) > 0 {
		b.WriteString("\nAttachments on this message:\n")
		for _, a := range task.ChatMessageAttachments {
			if a.ContentType != "" {
				fmt.Fprintf(b, "- id=%s filename=%q content_type=%s\n", a.ID, a.Filename, a.ContentType)
			} else {
				fmt.Fprintf(b, "- id=%s filename=%q\n", a.ID, a.Filename)
			}
		}
		b.WriteString("Use `multica attachment download <id>` to fetch each file locally before referring to it.\n")
	}

	return b.String()
}

// buildStepPromptWithHandoff renders the full handoff-enriched prompt for
// step-linked tasks. D14: uses full task.ChatMessage for the instruction.
func buildStepPromptWithHandoff(task Task, b *strings.Builder) string {
	hb := task.HandoffBundle

	b.WriteString("You are executing an approved plan step in a group chat.\n")
	b.WriteString("Complete ONLY this step. Do NOT call `multica chat plan submit`.\n")
	b.WriteString("Do NOT create new plans. Just execute the step and report the result.\n\n")
	b.WriteString("Handoff summaries are guidance; actual files in the workspace are authoritative.\n")
	b.WriteString("Inspect relevant files before editing or reporting.\n\n")

	// D14: Current step instruction uses full task.ChatMessage (untruncated).
	fmt.Fprintf(b, "## Current Step (Step %d", hb.Sequence)
	totalSteps := len(hb.PlanSteps)
	if totalSteps > 0 {
		fmt.Fprintf(b, " of %d", totalSteps)
	}
	fmt.Fprintf(b, ")\n")
	fmt.Fprintf(b, "Agent: %s\n", hb.AgentName)
	fmt.Fprintf(b, "Instruction:\n%s\n\n", task.ChatMessage)

	// Plan Summary
	if len(hb.PlanSteps) > 0 {
		b.WriteString("## Plan Summary\n")
		for _, s := range hb.PlanSteps {
			marker := ""
			if s.Sequence == hb.Sequence {
				marker = " (current)"
			}
			fmt.Fprintf(b, "- Step %d: %s — %s%s — %s\n",
				s.Sequence, s.AgentName, s.Status, marker, truncatePrompt(s.PromptSummary, 80))
		}
		b.WriteString("\n")
	}

	// Previous Step Results (D9)
	if len(hb.PreviousSteps) > 0 {
		b.WriteString("## Previous Step Results\n")
		for _, ps := range hb.PreviousSteps {
			fmt.Fprintf(b, "### Step %d (%s): %s\n", ps.Sequence, ps.AgentName, ps.Status)
			if ps.ResultSummary != "" {
				b.WriteString(ps.ResultSummary)
				b.WriteString("\n")
			}
			if ps.ResultRevision != "" {
				fmt.Fprintf(b, "Revision: %s\n", ps.ResultRevision)
			}
			b.WriteString("\n")
		}
	}

	// Recent Chat Messages
	if len(hb.RecentMessages) > 0 {
		b.WriteString("## Recent Chat Messages\n")
		for _, m := range hb.RecentMessages {
			role := m.Role
			if len(m.AgentID) >= 8 {
				role = fmt.Sprintf("%s[%s]", m.Role, m.AgentID[:8])
			}
			fmt.Fprintf(b, "[%s] %s\n", role, truncatePrompt(m.Content, 200))
		}
		b.WriteString("\n")
	}

	// Artifacts
	if len(hb.ArtifactSummaries) > 0 {
		b.WriteString("## Artifacts\n")
		for _, a := range hb.ArtifactSummaries {
			fmt.Fprintf(b, "- Step %d: %s\n", a.StepSequence, truncatePrompt(a.Summary, 200))
		}
		b.WriteString("\n")
	}

	// Revision
	if hb.Revisions.Base != nil || hb.Revisions.Result != nil {
		b.WriteString("## Revision\n")
		if hb.Revisions.Base != nil {
			fmt.Fprintf(b, "Base: %s", hb.Revisions.Base.Head)
			if hb.Revisions.Base.DirtyCount > 0 {
				fmt.Fprintf(b, " (%d dirty files)", hb.Revisions.Base.DirtyCount)
			}
			b.WriteString("\n")
		}
		if hb.Revisions.Result != nil {
			fmt.Fprintf(b, "Result: %s", hb.Revisions.Result.Head)
			if hb.Revisions.Result.DirtyCount > 0 {
				fmt.Fprintf(b, " (%d dirty files)", hb.Revisions.Result.DirtyCount)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Warnings
	if len(hb.Warnings) > 0 {
		b.WriteString("## Warnings\n")
		for _, w := range hb.Warnings {
			fmt.Fprintf(b, "- %s\n", w)
		}
		b.WriteString("\n")
	}

	// Attachments
	if len(task.ChatMessageAttachments) > 0 {
		b.WriteString("Attachments on this message:\n")
		for _, a := range task.ChatMessageAttachments {
			if a.ContentType != "" {
				fmt.Fprintf(b, "- id=%s filename=%q content_type=%s\n", a.ID, a.Filename, a.ContentType)
			} else {
				fmt.Fprintf(b, "- id=%s filename=%q\n", a.ID, a.Filename)
			}
		}
		b.WriteString("Use `multica attachment download <id>` to fetch each file locally before referring to it.\n")
	}

	return b.String()
}

// truncatePrompt truncates s to maxLen for prompt display.
func truncatePrompt(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
